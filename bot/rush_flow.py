"""搶票流程 — 導向購票 URL 並等待進入選區頁。"""
from __future__ import annotations

from browser_ops import js_navigate, wait_url
from logger import alog
from runtime_context import TICKET_AREA_PATH_FRAGMENT


RUSH_POLL_INTERVAL = 0.05        # 導頁後檢查 URL 的間隔
RUSH_TIMEOUT = 300.0             # TICKET_URL 設錯或站方異常時,最多 rush 5 分鐘


async def rush_to_area(page, purchase_url: str, timeout: float = RUSH_TIMEOUT) -> bool:
    """把 tab 導到購票 URL,並等待 URL 含 /ticket/area/。

    重試節奏由 main 的 60 秒 attempt 控制,這裡只負責單次導頁與 URL
    觀察,避免在 Chrome 還在載入時反覆重設同一個 navigation。
    """
    await js_navigate(page, purchase_url)
    reached = await wait_url(
        page,
        predicate=lambda url: TICKET_AREA_PATH_FRAGMENT in url,
        timeout=timeout,
        poll_interval=RUSH_POLL_INTERVAL,
    )
    current = getattr(page, "url", "") or ""
    if reached:
        await alog(f"rush 成功:{current}")
        return True

    await alog(f"rush 等待選區頁逾時({timeout:.0f}s),最後 URL:{current}")
    return False
