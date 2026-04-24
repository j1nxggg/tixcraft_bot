"""標準化的 log 輸出。

設計重點:
    - 走 stdlib logging 模組、只輸出到 sys.stderr,不額外寫檔
    - 時戳用 ISO 8601 格式帶台北時區,跟之前 `log()` 的輸出格式對齊
    - 提供 `log()`(同步)與 `alog()`(非同步)兩版:
        * 非關鍵路徑用 `log()`,簡單直寫
        * 搶票關鍵路徑(rush / captcha 提交 / dialog handler)用 `alog()`,
          把 emit 丟到 thread,避免 logging lock 短暫 block asyncio loop

使用方式(都要在 asyncio.run 之前先呼叫一次 `setup_logging()`):

    from logger import setup_logging, log, alog
    setup_logging()
    log("bot 啟動")
    await alog("rush 成功,第 3 次嘗試")
"""
from __future__ import annotations

import asyncio
import logging
import sys
from datetime import datetime, timedelta, timezone

_TAIPEI_TZ = timezone(timedelta(hours=8))
_LOGGER_NAME = "tixcraft_bot"

# 獨立 logger instance,避免跟第三方 library 的 root handler 互相影響
_logger = logging.getLogger(_LOGGER_NAME)


class _TaipeiFormatter(logging.Formatter):
    """把 record.created(float epoch)轉成台北時區 ISO 字串。"""

    def formatTime(self, record: logging.LogRecord, datefmt: str | None = None) -> str:
        dt = datetime.fromtimestamp(record.created, tz=_TAIPEI_TZ)
        return dt.isoformat(timespec="microseconds")


def setup_logging(level: int = logging.INFO) -> None:
    """把 StreamHandler 掛到 tixcraft_bot logger,多次呼叫只生效一次。"""
    if _logger.handlers:
        return

    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(_TaipeiFormatter("[%(asctime)s] %(message)s"))
    _logger.addHandler(handler)
    _logger.setLevel(level)
    # 不往 root logger 傳,避免被第三方 handler 再印一次
    _logger.propagate = False


def log(msg: str) -> None:
    """同步版 — 非關鍵路徑用這個。

    若 setup_logging 還沒呼叫(例如 unit test 場景),用 print 當安全 fallback,
    不會因為忘了初始化就噎掉訊息。
    """
    if _logger.handlers:
        _logger.info(msg)
    else:
        print(msg, file=sys.stderr, flush=True)


async def alog(msg: str) -> None:
    """非同步版 — 搶票關鍵路徑用這個。

    logging.Handler.emit 會取 threading.RLock,在高併發的 asyncio 迴圈裡
    可能短暫 block。用 run_in_executor 把 emit 丟到 default thread pool,
    讓 event loop 繼續跑。
    """
    if not _logger.handlers:
        # fallback:setup_logging 還沒跑,至少把訊息印出來,別吞掉
        print(msg, file=sys.stderr, flush=True)
        return

    loop = asyncio.get_running_loop()
    await loop.run_in_executor(None, _logger.info, msg)


def get_logger() -> logging.Logger:
    """需要直接用 logger.warning / logger.error / logger.debug 時呼叫這個拿 instance。"""
    return _logger

