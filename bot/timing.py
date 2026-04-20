import asyncio
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from statistics import median

import aiohttp

from config import (
    CALIBRATION_INTERVAL_SECONDS,
    CALIBRATION_SAMPLE_COUNT,
    CALIBRATION_URL,
    TAIPEI_TZ,
    log,
)


async def calibrate_server_time_offset() -> float:
    samples: list[float] = []
    try:
        async with aiohttp.ClientSession() as session:
            for _ in range(CALIBRATION_SAMPLE_COUNT):
                try:
                    t_before = datetime.now(timezone.utc).timestamp()
                    async with session.head(
                        CALIBRATION_URL,
                        timeout=aiohttp.ClientTimeout(total=5),
                    ) as resp:
                        t_after = datetime.now(timezone.utc).timestamp()
                        date_header = resp.headers.get("Date")
                        if not date_header:
                            continue
                        server_time = parsedate_to_datetime(date_header).timestamp()
                        local_mid = (t_before + t_after) / 2
                        samples.append(server_time - local_mid)
                except Exception:
                    continue
                await asyncio.sleep(0.2)
    except Exception:
        return 0.0

    if not samples:
        return 0.0
    return median(samples)


async def periodic_recalibrate(time_state: dict, stop_event: asyncio.Event) -> None:
    while not stop_event.is_set():
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=CALIBRATION_INTERVAL_SECONDS)
            return
        except asyncio.TimeoutError:
            pass

        new_offset = await calibrate_server_time_offset()
        old_offset = time_state["offset"]
        time_state["offset"] = new_offset
        log(
            f"[{datetime.now(TAIPEI_TZ).isoformat()}] "
            f"重新校正時間：{old_offset:+.3f}s → {new_offset:+.3f}s"
        )


def parse_grab_time(grab_time_str: str) -> datetime:
    for layout in ("%Y/%m/%d %H:%M", "%Y/%m/%d %H:%M:%S"):
        try:
            naive = datetime.strptime(grab_time_str.strip(), layout)
            return naive.replace(tzinfo=TAIPEI_TZ)
        except ValueError:
            continue
    raise RuntimeError(f"GRAB_TIME 格式無法解析：{grab_time_str}")


def server_now(time_offset: float) -> float:
    return datetime.now().timestamp() + time_offset


async def wait_until_grab_time(grab_time: datetime, get_offset) -> None:
    target_timestamp = grab_time.timestamp()

    while True:
        now_server = server_now(get_offset())
        remaining = target_timestamp - now_server

        if remaining <= 0:
            return

        if remaining > 60:
            await asyncio.sleep(remaining - 60)
        elif remaining > 10:
            await asyncio.sleep(1)
        elif remaining > 1:
            await asyncio.sleep(0.1)
        else:
            await asyncio.sleep(0.01)
