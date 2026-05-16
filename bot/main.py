"""Tixcraft Bot orchestration.

main 只串接高層流程。Chrome/CDP 細節放在 browser_session 與 browser_ops,
登入、rush、選區、填表各自留在對應 flow module。
"""
from __future__ import annotations

import asyncio
import sys
from datetime import datetime
from pathlib import Path

BOT_DIR = Path(__file__).resolve().parent
if str(BOT_DIR) not in sys.path:
    sys.path.insert(0, str(BOT_DIR))

# nodriver 本地 patch 必須早於 flow imports。
from runtime_context import (  # noqa: E402
    TAIPEI_TZ,
    load_env_config,
    patch_nodriver_connection_file,
    patch_nodriver_network_file,
)

patch_nodriver_network_file()
patch_nodriver_connection_file()

from area_flow import format_area_selection_result, select_area_ticket  # noqa: E402
from browser_ops import robust_select, wait_ready_state  # noqa: E402
from browser_session import BotBrowser  # noqa: E402
from logger import log, setup_logging  # noqa: E402
from login_flow import reload_to_clean_document, run_login  # noqa: E402
from ocr import ensure_ocr_model_ready  # noqa: E402
from rush_flow import rush_to_area  # noqa: E402
from schedule import build_game_url, build_purchase_url, locate_purchase_button  # noqa: E402
from ticket_flow import process_ticket_form, wait_ticket_form_ready  # noqa: E402
from time_sync import (  # noqa: E402
    calibrate_server_time_offset,
    parse_grab_time,
    periodic_recalibrate,
    wait_until_grab_time,
)


AREA_SELECT_SUCCESS_STATUSES = {"selected_exact", "selected_fallback"}
EARLY_RUSH_SECONDS = 1.0           # 提前開 rush 的秒數,抵消網路延遲
SCHEDULE_READY_TIMEOUT = 15.0      # 登入後等場次列表 DOM 出現的上限
AREA_ATTEMPT_TIMEOUT = 60.0        # 每輪從選區頁到填票頁的等待上限
AREA_RETRY_BACKOFF = 0.2           # 選區頁重試前的小間隔


async def _wait_schedule_ready(page, timeout: float = SCHEDULE_READY_TIMEOUT) -> bool:
    """等待場次列表第一列 DOM ready。"""
    row = await robust_select(page, "#gameList table tbody tr", timeout=timeout)
    return row is not None


async def _run_area_attempt(page, purchase_url: str, env_config: dict) -> str:
    """嘗試從購票 URL 進到填票頁。

    回傳:
        ready:已進入填票頁
        retry:本輪像是載入/導頁問題,可重新導向選區頁
        terminal:票區設定或庫存狀態不符合,重試通常不會改善
    """
    if not await rush_to_area(page, purchase_url, timeout=AREA_ATTEMPT_TIMEOUT):
        return "retry"
    log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 已進入選區頁")

    await wait_ready_state(page, state="complete", timeout=10.0)

    area_result = await select_area_ticket(
        page,
        env_config["TICKET_NAME"],
        env_config["TICKET_PRICE"],
        env_config["FALLBACK_POLICY"],
    )
    log(format_area_selection_result(area_result))

    status = area_result["status"]
    if status == "area_list_not_found":
        return "retry"
    if status not in AREA_SELECT_SUCCESS_STATUSES:
        return "terminal"

    await wait_ready_state(page, state="complete", timeout=10.0)
    if await wait_ticket_form_ready(page, timeout=AREA_ATTEMPT_TIMEOUT):
        return "ready"
    return "retry"


async def _prepare_ticket_form(page, purchase_url: str, env_config: dict) -> bool:
    """重試選區流程,直到進入填票頁或遇到不可重試狀態。"""
    attempt = 0

    while True:
        attempt += 1
        log(f"第 {attempt} 輪導向選區頁,最多等待 {AREA_ATTEMPT_TIMEOUT:.0f}s 進入填票頁")

        try:
            result = await asyncio.wait_for(
                _run_area_attempt(page, purchase_url, env_config),
                timeout=AREA_ATTEMPT_TIMEOUT,
            )
        except asyncio.TimeoutError:
            log(
                f"第 {attempt} 輪超過 {AREA_ATTEMPT_TIMEOUT:.0f}s "
                "仍未進入填票頁,重新導向選區頁"
            )
            await asyncio.sleep(AREA_RETRY_BACKOFF)
            continue

        if result == "ready":
            log("已進入填票頁,開始處理票券表單")
            return True

        if result == "terminal":
            return False

        log(f"第 {attempt} 輪未能進入填票頁,重新導向選區頁")
        await asyncio.sleep(AREA_RETRY_BACKOFF)


async def _cancel_recalibrate(
    recalibrate_task: asyncio.Task,
    stop_event: asyncio.Event,
) -> None:
    stop_event.set()
    try:
        await asyncio.wait_for(recalibrate_task, timeout=2)
    except asyncio.TimeoutError:
        recalibrate_task.cancel()
        try:
            await recalibrate_task
        except (asyncio.CancelledError, Exception):
            pass
    except (asyncio.CancelledError, Exception):
        pass


async def main():
    setup_logging()

    ocr_session = ensure_ocr_model_ready()
    env_config = load_env_config()

    try:
        grab_time = parse_grab_time(env_config["GRAB_TIME"])
    except RuntimeError as exc:
        log(f"解析 GRAB_TIME 失敗:{exc}")
        return

    time_state = {"offset": 0.0}
    try:
        time_state["offset"] = await calibrate_server_time_offset()
    except RuntimeError as exc:
        log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 初始時間校正失敗:{exc}")
        return
    log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 初始時間校正:{time_state['offset']:+.3f}s")

    stop_recalibrate = asyncio.Event()
    recalibrate_task = asyncio.create_task(
        periodic_recalibrate(time_state, stop_recalibrate)
    )

    try:
        await _run_bot(env_config, grab_time, time_state, stop_recalibrate, ocr_session)
    except Exception as exc:
        # 非預期例外完整記錄,避免 CLI 只顯示 bot 已結束。
        import traceback
        log(f"main 執行過程拋例外:{exc!r}")
        log(traceback.format_exc())
    finally:
        await _cancel_recalibrate(recalibrate_task, stop_recalibrate)
        del ocr_session
        await asyncio.sleep(0.1)  # 給底層 task 收尾時間


async def _run_bot(
    env_config: dict,
    grab_time: datetime,
    time_state: dict,
    stop_recalibrate: asyncio.Event,
    ocr_session,
) -> None:
    target_url = build_game_url(env_config["TICKET_URL"])

    async with BotBrowser(env_config["CHROME_PROFILE_DIR"]) as session:
        # 1. 初始化瀏覽器與登入狀態
        page = await session.open_ready_tab(target_url)

        if not await run_login(page, env_config["LOGIN_PROVIDER"], target_url):
            return

        await asyncio.sleep(0.5)
        if not await reload_to_clean_document(page, target_url):
            log("登入後重新載入場次頁失敗,中止")
            return

        if not await _wait_schedule_ready(page):
            log("場次列表 DOM 未在時限內 ready,中止")
            return

        # 2. 定位場次並等開賣
        _, info = await locate_purchase_button(page, env_config["SHOW_TIME"])
        status = info.get("status")

        if status == "row_not_found":
            log(f"找不到符合 SHOW_TIME 的場次:{env_config['SHOW_TIME']}")
            return

        if status == "unavailable":
            log(f"無法購票:{info.get('status_text', '')}")
            return

        game_id = info["game_id"]
        if not game_id:
            log("找不到 game_id,無法組出購票 URL")
            return

        purchase_url = build_purchase_url(env_config["TICKET_URL"], game_id)
        log(f"目標場次:{info['time']} | {info['name']} | {info['venue']}")
        log(f"game_id={game_id}")
        log(f"purchase_url={purchase_url}")

        if status == "on_sale":
            log("場次已開賣,直接進入搶票")
        else:
            log(
                f"場次尚未開賣,等待 GRAB_TIME:{grab_time.isoformat()} "
                f"(提前 {EARLY_RUSH_SECONDS:.1f} 秒開始衝票)"
            )
            await wait_until_grab_time(
                grab_time,
                lambda: time_state["offset"],
                advance_seconds=EARLY_RUSH_SECONDS,
            )
            log(
                f"[{datetime.now(TAIPEI_TZ).isoformat()}] "
                f"提早 {EARLY_RUSH_SECONDS:.1f} 秒進入搶票"
            )

        # rush 前主動停背景校時;finally 裡還會再 set 一次做 cleanup。
        stop_recalibrate.set()

        # 3-4. 導向選區頁並選區,60 秒內沒進填票頁就重新導向。
        if not await _prepare_ticket_form(page, purchase_url, env_config):
            return

        # 5. 填票券表單並送出
        await process_ticket_form(page, ocr_session, env_config["TICKET_QUANTITY"])

    # 離開 with 只斷 CDP,不殺 Chrome。


if __name__ == "__main__":
    asyncio.run(main())
