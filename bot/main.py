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
from ticket_flow import process_ticket_form  # noqa: E402
from time_sync import (  # noqa: E402
    calibrate_server_time_offset,
    parse_grab_time,
    periodic_recalibrate,
    wait_until_grab_time,
)


AREA_SELECT_SUCCESS_STATUSES = {"selected_exact", "selected_fallback"}
EARLY_RUSH_SECONDS = 1.0           # 提前開 rush 的秒數,抵消網路延遲
SCHEDULE_READY_TIMEOUT = 15.0      # 登入後等場次列表 DOM 出現的上限


async def _wait_schedule_ready(page, timeout: float = SCHEDULE_READY_TIMEOUT) -> bool:
    """等待場次列表第一列 DOM ready。"""
    row = await robust_select(page, "#gameList table tbody tr", timeout=timeout)
    return row is not None


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

        # 3. rush 到選區頁
        if not await rush_to_area(page, purchase_url):
            log("rush 未能在時限內進入選區頁,中止")
            return
        log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 已進入選區頁")

        await wait_ready_state(page, state="complete", timeout=10.0)

        # 4. 選區
        area_result = await select_area_ticket(
            page,
            env_config["TICKET_NAME"],
            env_config["TICKET_PRICE"],
            env_config["FALLBACK_POLICY"],
        )
        log(format_area_selection_result(area_result))

        if area_result["status"] not in AREA_SELECT_SUCCESS_STATUSES:
            return

        await wait_ready_state(page, state="complete", timeout=10.0)

        # 5. 填票券表單並送出
        await process_ticket_form(page, ocr_session, env_config["TICKET_QUANTITY"])

    # 離開 with 只斷 CDP,不殺 Chrome。


if __name__ == "__main__":
    asyncio.run(main())
