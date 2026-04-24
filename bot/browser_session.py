"""Chrome lifecycle wrapper.

負責啟動或接管 Chrome、管理 Profile/.bot-cdp.json、取得乾淨工作 tab。
離開 context 時只斷 CDP,保留 Chrome 給使用者完成後續操作。

使用方式:
    async with BotBrowser(profile_dir="Default") as session:
        page = await session.open_ready_tab("https://tixcraft.com/activity/game/xxx")
"""
from __future__ import annotations

import asyncio
import json
import socket
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

from nodriver import Config, start
from nodriver.core import util as nodriver_util

from browser_ops import robust_bring_to_front, wait_ready_state
from logger import log
from runtime_context import (
    PROFILE_DIR,
    PROJECT_ROOT,
    clear_cdp_endpoint,
    load_cdp_endpoint,
    save_cdp_endpoint,
)


_CDP_READY_TIMEOUT = 20.0       # 等 Chrome CDP endpoint 就緒的最長秒數
_CDP_PROBE_TIMEOUT = 1.5        # 單次 /json/version 探測逾時
_NODRIVER_START_TIMEOUT = 20.0  # nodriver attach/start 等待上限
_TAB_CREATE_TIMEOUT = 10.0      # browser.get(new_tab=True) 逾時
_TAB_CLOSE_TIMEOUT = 3.0        # 單個 tab.close() 逾時
_URL_SETTLE_TIMEOUT = 8.0       # 等新 tab URL 落到目標站點
_PAGE_READY_TIMEOUT = 10.0      # 初始工作頁 document.readyState 等待上限
_BROWSER_ARGS = [
    "--hide-crash-restore-bubble",
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-session-crashed-bubble",
    "--restore-last-session=false",
]


def _find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _probe_cdp_endpoint(port: int, timeout: float = _CDP_PROBE_TIMEOUT) -> bool:
    """探測 /json/version 是否可用。"""
    url = f"http://127.0.0.1:{port}/json/version"
    try:
        with urllib.request.urlopen(url, timeout=timeout) as resp:
            if resp.status != 200:
                return False
            json.loads(resp.read())
            return True
    except (urllib.error.URLError, ConnectionError, OSError, ValueError):
        return False


async def _wait_cdp_ready(port: int, timeout: float = _CDP_READY_TIMEOUT) -> bool:
    """等待剛啟動的 Chrome 開好 CDP endpoint。"""
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        if _probe_cdp_endpoint(port, timeout=1.0):
            return True
        await asyncio.sleep(0.1)
    return False


def _normalize_profile_exit_state() -> None:
    """修正 Profile 狀態,避免 Chrome 顯示異常關閉提示。"""
    if not PROFILE_DIR.exists():
        return

    local_state = PROFILE_DIR / "Local State"
    if local_state.exists():
        _patch_json_file(local_state, _apply_local_state_patch)

    for sub in PROFILE_DIR.iterdir():
        if not sub.is_dir():
            continue
        if not (sub.name == "Default" or sub.name.startswith("Profile ")):
            continue
        prefs = sub / "Preferences"
        if prefs.exists():
            _patch_json_file(prefs, _apply_preferences_patch)


def _patch_json_file(path: Path, patcher) -> None:
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return
    if not patcher(data):
        return
    try:
        path.write_text(
            json.dumps(data, ensure_ascii=False, separators=(",", ":")),
            encoding="utf-8",
        )
    except OSError:
        pass


def _apply_local_state_patch(data: dict) -> bool:
    changed = False
    was = data.setdefault("was", {})
    if was.get("restarted") is not False:
        was["restarted"] = False
        changed = True
    return changed


def _apply_preferences_patch(data: dict) -> bool:
    changed = False
    profile = data.setdefault("profile", {})
    if profile.get("exit_type") != "Normal":
        profile["exit_type"] = "Normal"
        changed = True
    if profile.get("exited_cleanly") is not True:
        profile["exited_cleanly"] = True
        changed = True
    session = data.setdefault("session", {})
    if session.get("exit_type") != "Normal":
        session["exit_type"] = "Normal"
        changed = True
    if session.get("exited_cleanly") is not True:
        session["exited_cleanly"] = True
        changed = True
    return changed


class BotBrowser:
    """Chrome 生命週期的 async context manager。"""

    def __init__(self, chrome_profile_dir: str):
        self.chrome_profile_dir = chrome_profile_dir
        self.browser = None

    async def __aenter__(self) -> "BotBrowser":
        _normalize_profile_exit_state()
        self.browser = await _launch_or_attach(self.chrome_profile_dir)
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self._teardown()

    async def _teardown(self) -> None:
        """只斷 CDP,不動 Chrome process。"""
        if self.browser is None:
            return
        # 從 nodriver registry 移除,避免 atexit 清掉我們刻意保留的 Chrome。
        try:
            nodriver_util.get_registered_instances().discard(self.browser)
        except Exception:
            pass
        try:
            await asyncio.shield(_disconnect_browser(self.browser))
        except Exception:
            pass
        self.browser = None

    async def open_fresh_tab(self, target_url: str, timeout: float = 30.0):
        """開新 tab 到 target_url,再關掉其它既有 tab。"""
        if self.browser is None:
            raise RuntimeError("BotBrowser 尚未啟動,請在 async with 區塊內使用")

        deadline = asyncio.get_event_loop().time() + timeout

        # attach 後 tabs 可能還沒同步完成。
        while asyncio.get_event_loop().time() < deadline:
            if self.browser.tabs:
                break
            await asyncio.sleep(0.2)
        else:
            raise RuntimeError("Chrome 啟動後沒偵測到任何 tab")

        try:
            new_tab = await asyncio.wait_for(
                self.browser.get(target_url, new_tab=True),
                timeout=_TAB_CREATE_TIMEOUT,
            )
        except Exception as exc:
            raise RuntimeError(f"無法開新 tab 導向 {target_url}:{exc!r}") from exc

        log(f"已開新 tab 導向:{target_url}")

        # 只看 Tab.url,避免初始頁面還沒 ready 時做 Runtime.evaluate。
        await _wait_tab_url_contains(new_tab, "tixcraft.com", timeout=_URL_SETTLE_TIMEOUT)

        new_target_id = getattr(new_tab, "target_id", None)
        if new_target_id:
            await self._close_other_tabs(new_target_id)

        return new_tab

    async def open_ready_tab(self, target_url: str):
        """取得可交給後續流程使用的乾淨 tab。"""
        page = await self.open_fresh_tab(target_url)
        await robust_bring_to_front(page)

        # 用 asyncio.sleep,避開 nodriver page.sleep 連帶 update_targets。
        await asyncio.sleep(0.5)
        await wait_ready_state(page, state="complete", timeout=_PAGE_READY_TIMEOUT)
        return page

    async def _close_other_tabs(self, keep_target_id: str) -> None:
        for tab in list(self.browser.tabs):
            target_id = getattr(tab, "target_id", None)
            if target_id == keep_target_id:
                continue
            url = getattr(tab, "url", "") or ""
            try:
                await asyncio.wait_for(tab.close(), timeout=_TAB_CLOSE_TIMEOUT)
                log(f"關閉既有 tab:{url!r}")
            except Exception as exc:
                log(f"關閉既有 tab 失敗(忽略):{url!r} {exc!r}")


def _build_browser_config(chrome_profile_dir: str, port: int) -> Config:
    return Config(
        user_data_dir=str(PROFILE_DIR),
        headless=False,
        browser_args=[*_BROWSER_ARGS, f"--profile-directory={chrome_profile_dir}"],
        host="127.0.0.1",
        port=port,
    )


async def _launch_or_attach(chrome_profile_dir: str):
    """優先重用既有 CDP endpoint,不可用時才啟新 Chrome。"""
    existing = load_cdp_endpoint()
    existing_port = existing.get("port") if isinstance(existing, dict) else None

    if isinstance(existing_port, int) and _probe_cdp_endpoint(existing_port):
        log(f"偵測到現有 Chrome CDP endpoint(port={existing_port}),直接連線重用")
        config = _build_browser_config(chrome_profile_dir, existing_port)
        try:
            browser = await asyncio.wait_for(
                start(config=config),
                timeout=_NODRIVER_START_TIMEOUT,
            )
        except Exception as exc:
            log(f"連線現有 CDP endpoint 失敗:{exc!r},改為重啟 Chrome")
            clear_cdp_endpoint()
        else:
            save_cdp_endpoint(existing_port)
            return browser

    # 沒可重用 endpoint,或 attach 失敗,才啟新 Chrome。
    port = _find_free_port()
    config = _build_browser_config(chrome_profile_dir, port)

    creationflags = 0
    if sys.platform == "win32":
        creationflags = (
            subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP
        )

    executable = str(config.browser_executable_path)
    launch_args = [executable, *(str(arg) for arg in config())]

    proc = subprocess.Popen(
        launch_args,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        close_fds=True,
        creationflags=creationflags,
        cwd=str(PROJECT_ROOT),
    )

    if not await _wait_cdp_ready(port):
        clear_cdp_endpoint()
        raise RuntimeError(f"等待 Chrome CDP endpoint 逾時 (port={port})")

    # host+port 都給,nodriver start() 會 attach,不會再啟 Chrome。
    browser = await asyncio.wait_for(
        start(config=config),
        timeout=_NODRIVER_START_TIMEOUT,
    )
    save_cdp_endpoint(port, pid=proc.pid)
    return browser


async def _disconnect_browser(browser) -> None:
    try:
        if browser.connection and not browser.connection.closed:
            await asyncio.wait_for(browser.connection.disconnect(), timeout=3.0)
    except Exception:
        pass


async def _wait_tab_url_contains(tab, fragment: str, timeout: float) -> bool:
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        url = (getattr(tab, "url", "") or "").lower()
        if fragment in url:
            return True
        await asyncio.sleep(0.2)
    log(f"等待 tab URL 含 {fragment!r} 逾時,目前 URL:{getattr(tab, 'url', '')!r}")
    return False
