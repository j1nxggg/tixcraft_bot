"""搶票流程 — 連續 JS navigate 到購票 URL 直到進入選區頁。"""
from __future__ import annotations

import asyncio

from browser_ops import js_navigate
from logger import alog
from runtime_context import TICKET_AREA_PATH_FRAGMENT


RUSH_POLL_INTERVAL = 0.3         # 每次 JS navigate 後等多久檢 URL
RUSH_FAIL_BACKOFF = 0.05         # JS navigate 失敗後的小 backoff
RUSH_TIMEOUT = 300.0             # TICKET_URL 設錯或站方異常時,最多 rush 5 分鐘


async def rush_to_area(page, purchase_url: str, timeout: float = RUSH_TIMEOUT) -> bool:
    """連續嘗試把 tab 導到購票 URL,直到 URL 含 /ticket/area/。

    拓元開賣瞬間伺服器 302 / 503 / captcha 交錯,我們用 JS navigate 持續
    推,每次等 RUSH_POLL_INTERVAL 看 Tab.url(CDP Target 事件獨立更新)
    是否已進選區頁。timeout 是防呆保險絲,避免設定錯誤時跑整晚。
    """
    attempt = 0
    deadline = asyncio.get_event_loop().time() + timeout

    while asyncio.get_event_loop().time() < deadline:
        attempt += 1
        try:
            await js_navigate(page, purchase_url)
        except Exception as exc:
            await alog(f"rush attempt #{attempt} JS 導向失敗:{exc!r}")
            await asyncio.sleep(RUSH_FAIL_BACKOFF)
            continue

        await asyncio.sleep(RUSH_POLL_INTERVAL)
        current = getattr(page, "url", "") or ""
        if TICKET_AREA_PATH_FRAGMENT in current:
            await alog(f"rush 成功(第 {attempt} 次嘗試):{current}")
            return True

    current = getattr(page, "url", "") or ""
    await alog(f"rush 逾時({timeout:.0f}s),最後 URL:{current}")
    return False
