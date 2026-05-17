"""Ticket training mode entrypoint.

這個入口只服務 CLI 的測試模式。正式 bot 不 import 這個目錄,因此移除
test/ 不會影響正式搶票流程。
"""
from __future__ import annotations

import asyncio
import os
import sys
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
TEST_DIR = Path(__file__).resolve().parent
BOT_DIR = PROJECT_ROOT / "bot"
if str(TEST_DIR) not in sys.path:
    sys.path.insert(0, str(TEST_DIR))
if str(BOT_DIR) not in sys.path:
    sys.path.insert(0, str(BOT_DIR))

# nodriver 本地 patch 必須早於 browser_session / flow imports。
from runtime_context import (  # noqa: E402
    patch_nodriver_connection_file,
    patch_nodriver_network_file,
)

patch_nodriver_network_file()
patch_nodriver_connection_file()

from browser_session import BotBrowser  # noqa: E402
from logger import log, setup_logging  # noqa: E402
from ocr import ensure_ocr_model_ready  # noqa: E402
from training_flow import TRAINING_URL, run_training_flow  # noqa: E402


async def main() -> None:
    setup_logging()
    ocr_session = ensure_ocr_model_ready()
    chrome_profile_dir = os.getenv("CHROME_PROFILE_DIR", "Default").strip() or "Default"
    run_count = _parse_run_count(os.getenv("TEST_RUN_COUNT", "1"))

    try:
        async with BotBrowser(chrome_profile_dir) as session:
            page = await session.open_ready_tab(TRAINING_URL)
            success = await run_training_flow(page, ocr_session, run_count=run_count)
            if not success:
                raise RuntimeError("測試模式流程未完成")
    except Exception as exc:
        import traceback

        log(f"測試模式執行過程拋例外:{exc!r}")
        log(traceback.format_exc())
    finally:
        del ocr_session
        await asyncio.sleep(0.1)


def _parse_run_count(value: str) -> int:
    try:
        parsed = int(str(value).strip())
    except ValueError:
        return 1
    return min(max(parsed, 1), 30)


if __name__ == "__main__":
    asyncio.run(main())
