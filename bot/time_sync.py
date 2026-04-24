import asyncio
from datetime import datetime, timezone
from email.utils import parsedate_to_datetime
from statistics import median

import aiohttp

from runtime_context import (
    CALIBRATION_INTERVAL_SECONDS,
    CALIBRATION_SAMPLE_COUNT,
    CALIBRATION_URL,
    TAIPEI_TZ,
    log,
)


async def calibrate_server_time_offset() -> float:
    samples: list[float] = []
    failures: list[str] = []

    try:
        async with aiohttp.ClientSession() as session:
            for sample_index in range(1, CALIBRATION_SAMPLE_COUNT + 1):
                try:
                    t_before = datetime.now(timezone.utc).timestamp()
                    async with session.head(
                        CALIBRATION_URL,
                        timeout=aiohttp.ClientTimeout(total=5),
                    ) as resp:
                        t_after = datetime.now(timezone.utc).timestamp()
                        date_header = resp.headers.get("Date")
                        if not date_header:
                            failures.append(
                                f"sample #{sample_index}: status={resp.status} 缺少 Date header"
                            )
                            continue

                        server_time = parsedate_to_datetime(date_header).timestamp()
                        local_mid = (t_before + t_after) / 2
                        offset = server_time - local_mid
                        rtt_ms = (t_after - t_before) * 1000
                        samples.append(offset)
                        log(
                            "時間同步 sample "
                            f"#{sample_index}: status={resp.status} "
                            f"offset={offset:+.3f}s rtt={rtt_ms:.0f}ms"
                        )
                except Exception as exc:
                    failures.append(f"sample #{sample_index}: {exc!r}")
                await asyncio.sleep(0.2)
    except Exception as exc:
        raise RuntimeError(f"建立時間同步 session 失敗：{exc!r}") from exc

    if not samples:
        detail = "；".join(failures) if failures else "沒有有效樣本"
        raise RuntimeError(f"無法從 {CALIBRATION_URL} 取得可用的 Date header：{detail}")

    offset = median(samples)
    log(
        f"時間同步成功：{len(samples)}/{CALIBRATION_SAMPLE_COUNT} 個有效樣本，"
        f"median offset={offset:+.3f}s"
    )
    if failures:
        log("時間同步失敗樣本：" + "；".join(failures))
    return offset


async def periodic_recalibrate(time_state: dict, stop_event: asyncio.Event) -> None:
    while not stop_event.is_set():
        try:
            await asyncio.wait_for(stop_event.wait(), timeout=CALIBRATION_INTERVAL_SECONDS)
            return
        except asyncio.TimeoutError:
            pass

        try:
            new_offset = await calibrate_server_time_offset()
        except RuntimeError as exc:
            log(f"[{datetime.now(TAIPEI_TZ).isoformat()}] 重新校正時間失敗：{exc}")
            continue

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


async def wait_until_grab_time(grab_time: datetime, get_offset, advance_seconds: float = 0.0) -> None:
    target_timestamp = grab_time.timestamp() - max(advance_seconds, 0.0)

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
