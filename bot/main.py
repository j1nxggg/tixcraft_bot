import asyncio
import sys
from datetime import datetime
from pathlib import Path

BOT_DIR = Path(__file__).resolve().parent

if str(BOT_DIR) not in sys.path:
    sys.path.insert(0, str(BOT_DIR))

from config import TAIPEI_TZ, load_env_config, log, patch_nodriver_network_file

patch_nodriver_network_file()

from browser import (
    close_extra_startup_tabs,
    disconnect_browser_session,
    normalize_profile_exit_state,
    pick_startup_tab,
    rush_purchase_url,
    start_detached_browser,
)
from ocr import ensure_ocr_model_ready
from page_flow import build_game_url, build_purchase_url, locate_purchase_button
from timing import (
    calibrate_server_time_offset,
    parse_grab_time,
    periodic_recalibrate,
    wait_until_grab_time,
)


async def main():
    ocr_session = ensure_ocr_model_ready()
    env_config = load_env_config()

    time_state = {"offset": 0.0}
    initial_offset = await calibrate_server_time_offset()
    time_state["offset"] = initial_offset
    log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 初始時間校正：{initial_offset:+.3f}s")

    stop_recalibrate = asyncio.Event()
    recalibrate_task = asyncio.create_task(periodic_recalibrate(time_state, stop_recalibrate))

    try:
        grab_time = parse_grab_time(env_config["GRAB_TIME"])
    except RuntimeError as exc:
        log(f"解析 GRAB_TIME 失敗：{exc}")
        stop_recalibrate.set()
        return

    target_url = build_game_url(env_config["TICKET_URL"])
    chrome_profile_dir = env_config["CHROME_PROFILE_DIR"]
    show_time = env_config["SHOW_TIME"]
    normalize_profile_exit_state()
    browser = None

    try:
        browser = await start_detached_browser(chrome_profile_dir)
        await browser.sleep(1)
        page = pick_startup_tab(browser)
        page = await page.get(target_url)
        await page.sleep(0.3)
        await page.wait_for(selector="#gameList", timeout=10)
        await page.wait_for("#gameList table tbody tr", timeout=10)
        await close_extra_startup_tabs(browser, page)

        _, info = await locate_purchase_button(page, show_time)
        status = info.get("status")

        if status == "row_not_found":
            log(f"找不到符合 SHOW_TIME 的場次：{show_time}")
            return

        if status == "unavailable":
            log(f"無法購票：{info.get('status_text', '')}")
            return

        game_id = info["game_id"]
        if not game_id:
            log("找不到 game_id，無法組出購票 URL")
            return

        purchase_url = build_purchase_url(env_config["TICKET_URL"], game_id)
        log(f"目標場次：{info['time']} | {info['name']} | {info['venue']}")
        log(f"game_id={game_id}")
        log(f"purchase_url={purchase_url}")

        if status == "on_sale":
            log("場次已開賣，直接進入衝刺階段")
        else:
            log(f"場次尚未開賣，等待 GRAB_TIME：{grab_time.isoformat()}")
            await wait_until_grab_time(grab_time, lambda: time_state["offset"])
            log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] GRAB_TIME 到達，進入衝刺階段")

        stop_recalibrate.set()
        await rush_purchase_url(page, purchase_url)

        log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 已進入選區頁")
        # TODO: 選取票名 & 價錢 並且按下
    finally:
        stop_recalibrate.set()
        try:
            await asyncio.wait_for(recalibrate_task, timeout=2)
        except asyncio.TimeoutError:
            recalibrate_task.cancel()
        except Exception:
            pass

        del ocr_session
        if browser is not None:
            await disconnect_browser_session(browser)
        await asyncio.sleep(0.1)


if __name__ == "__main__":
    asyncio.run(main())
