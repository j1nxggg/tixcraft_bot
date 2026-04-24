"""Tixcraft 登入流程。

登入狀態只看桌面 navbar:
    nav#bs-navbar 內含「會員登入」 => 需要登入
    nav#bs-navbar 存在但不含「會員登入」 => 已登入

需要登入時會點會員登入入口與指定 OAuth provider,再等 URL 回到場次頁。
"""
from __future__ import annotations

import asyncio
import json

from browser_ops import (
    current_url,
    js_navigate,
    robust_click,
    robust_evaluate,
    robust_select,
    wait_url,
)
from logger import log
from urllib.parse import urlparse
from runtime_context import complete_first_login, first_login_pending


LOGIN_STATE_LOGGED_IN = "logged_in"
LOGIN_STATE_REQUIRED = "login_required"
LOGIN_STATE_UNKNOWN = "unknown"

_TIXCRAFT_DOMAIN = "tixcraft.com"
_OAUTH_INTERMEDIATE_FRAGMENTS = ("/login/", "accounts.google.com", "facebook.com")

LOGIN_WAIT_TIMEOUT = 1200.0        # 已有 Profile 但 session 失效時,仍保留 20 分鐘
FIRST_LOGIN_WAIT_TIMEOUT = 1200.0  # 首次建立 Profile 時給使用者手動登入
LOGIN_LINK_CHECK_TIMEOUT = 3.0     # 找登入入口的短等待
LOGIN_STATE_CHECK_TIMEOUT = 8.0    # 等 navbar 渲染出登入狀態
RELOAD_TARGET_TIMEOUT = 10.0       # OAuth 後重新導回場次頁的等待上限


def _is_on_target_page(url: str, target_url: str) -> bool:
    def _extract_game_code(u: str) -> str:
        path = urlparse(u.lower()).path.rstrip("/")
        if "/game/" not in path:
            return ""
        return path.rsplit("/", 1)[-1]

    normalized = url.lower().rstrip("/")
    if _TIXCRAFT_DOMAIN not in normalized:
        return False
    if any(frag in normalized for frag in _OAUTH_INTERMEDIATE_FRAGMENTS):
        return False

    current_code = _extract_game_code(url)
    target_code = _extract_game_code(target_url)
    return bool(current_code) and current_code == target_code


async def _click_login_provider(page, provider: str) -> bool:
    """打開登入 modal 並點擊指定 OAuth provider。"""
    if not await _click_login_entry(page):
        log("找不到可點擊的會員登入入口")
        return False

    await asyncio.sleep(1.0)

    provider_selector = "#loginGoogle" if provider == "google" else "#loginFacebook"
    provider_button = await robust_select(page, provider_selector, timeout=8.0)
    if provider_button:
        return await robust_click(provider_button)

    provider_url = f"https://tixcraft.com/login/{provider}"
    log(f"找不到 {provider} 登入按鈕,改直接導向 {provider_url}")
    await js_navigate(page, provider_url)
    return True


_LOGIN_LINK_SELECTOR = "a[href='#login'][data-bs-toggle='modal']"
_LOGIN_MODAL_SELECTOR = "a[href='#login'][data-bs-toggle='modal'], a[href='#login']"
_LOGIN_PAGE_SELECTOR = "a[href='/login'], a[href='https://tixcraft.com/login']"
_LOGIN_STATE_JS = r"""
(() => {
  const nav = document.querySelector('nav#bs-navbar');
  if (!nav) {
    return JSON.stringify({ status: 'unknown', reason: 'bs_navbar_not_found' });
  }

  const text = (nav.innerText || nav.textContent || '').replace(/\s+/g, ' ').trim();
  if (text.includes('會員登入')) {
    return JSON.stringify({
      status: 'login_required',
      reason: 'bs_navbar_contains_login_text',
      text
    });
  }

  return JSON.stringify({
    status: 'logged_in',
    reason: 'bs_navbar_without_login_text',
    text: text.slice(0, 120)
  });
})()
"""


async def _login_required(page) -> bool:
    """相容舊呼叫:只有明確看到會員登入才回 True。"""
    return await detect_login_state(page) == LOGIN_STATE_REQUIRED


async def detect_login_state(page) -> str:
    """回傳 logged_in / login_required / unknown。"""
    deadline = asyncio.get_event_loop().time() + LOGIN_STATE_CHECK_TIMEOUT
    last_state = {"status": LOGIN_STATE_UNKNOWN, "reason": "not_checked"}

    while asyncio.get_event_loop().time() < deadline:
        raw = await robust_evaluate(page, _LOGIN_STATE_JS, timeout=2.0)
        if isinstance(raw, str):
            try:
                state = json.loads(raw)
            except json.JSONDecodeError:
                state = {"status": LOGIN_STATE_UNKNOWN, "reason": "invalid_json", "raw": raw[:120]}
        elif isinstance(raw, dict):
            state = raw
        else:
            state = {"status": LOGIN_STATE_UNKNOWN, "reason": f"unexpected:{type(raw).__name__}"}

        status = str(state.get("status", LOGIN_STATE_UNKNOWN))
        last_state = state
        if status in {LOGIN_STATE_LOGGED_IN, LOGIN_STATE_REQUIRED}:
            detail = str(state.get("text") or state.get("reason") or "").strip()
            log(f"登入狀態判斷:{status} {detail}")
            return status

        await asyncio.sleep(0.2)

    log(f"登入狀態判斷失敗:{last_state}")
    return LOGIN_STATE_UNKNOWN


async def _click_login_entry(page) -> bool:
    """點擊 navbar 的會員登入入口,必要時退回 /login 頁。"""
    login_link = await robust_select(page, _LOGIN_MODAL_SELECTOR, timeout=LOGIN_LINK_CHECK_TIMEOUT)
    if login_link and await robust_click(login_link):
        return True

    # DOM 已存在但 nodriver query 短暫失敗時,用 JS click 補一次。
    clicked = await robust_evaluate(
        page,
        """
        (() => {
          const el = document.querySelector('a[href="#login"][data-bs-toggle="modal"], a[href="#login"]');
          if (!el) return false;
          el.click();
          return true;
        })()
        """,
        timeout=2.0,
    )
    if clicked is True:
        return True

    login_page_link = await robust_select(page, _LOGIN_PAGE_SELECTOR, timeout=1.0)
    if login_page_link:
        return await robust_click(login_page_link)
    return False


async def run_login(
    page,
    login_provider: str,
    target_url: str,
    *,
    wait_timeout: float | None = None,
) -> bool:
    """確認登入狀態;需要登入時才啟動 OAuth 流程。"""
    pending_first_login = first_login_pending()
    if wait_timeout is None:
        wait_timeout = FIRST_LOGIN_WAIT_TIMEOUT if pending_first_login else LOGIN_WAIT_TIMEOUT

    login_state = await detect_login_state(page)
    if login_state == LOGIN_STATE_LOGGED_IN:
        log("navbar 未顯示會員登入,判定目前已登入,繼續流程")
        if pending_first_login:
            complete_first_login()
            log("已記錄首次登入完成")
        return True
    if login_state == LOGIN_STATE_UNKNOWN:
        log("無法判斷登入狀態,中止以避免未登入時誤闖後續流程")
        return False

    if pending_first_login:
        log(f"偵測到新 Profile 首次登入,保留 {wait_timeout / 60:.0f} 分鐘給使用者輸入帳密")
    else:
        log(f"navbar 仍顯示會員登入,啟動 {login_provider} OAuth 流程,最長等待 {wait_timeout / 60:.0f} 分鐘")
    log("若 provider 已保留登入狀態,通常會自動回跳場次頁")

    if not await _click_login_provider(page, login_provider):
        log(f"登入按鈕流程失敗({login_provider}),中止")
        return False

    log(f"已點擊 {login_provider} 登入按鈕,開始等待 OAuth 回跳")

    async def _on_url_change(url: str) -> None:
        log(f"登入等待中:{url}")

    returned = await wait_url(
        page,
        predicate=lambda u: _is_on_target_page(u, target_url),
        timeout=wait_timeout,
        poll_interval=0.5,
        on_change=_on_url_change,
    )

    if not returned:
        log(f"登入等待逾時({wait_timeout:.0f} 秒),中止")
        return False

    log("偵測到已回到場次頁,登入完成")
    if pending_first_login:
        complete_first_login()
        log("已記錄首次登入完成")
    return True


async def reload_to_clean_document(
    page,
    target_url: str,
    *,
    timeout: float = RELOAD_TARGET_TIMEOUT,
) -> bool:
    """OAuth 回跳後重新導向目標頁,避免 tab 留在中繼 document。"""
    await js_navigate(page, target_url)

    returned = await wait_url(
        page,
        predicate=lambda u: _is_on_target_page(u, target_url),
        timeout=timeout,
    )
    if not returned:
        final_url = await current_url(page)
        log(f"登入後重新載入場次頁逾時({timeout:.1f}s),目前 URL:{final_url}")
        return False
    return True
