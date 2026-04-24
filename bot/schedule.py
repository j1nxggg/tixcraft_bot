from datetime import datetime
from pathlib import Path

from browser_ops import (
    robust_child_select,
    robust_child_select_all,
    robust_get_attr,
    robust_select,
)
from runtime_context import SCREENSHOT_DIR, TARGET_TABLE_HEADERS
from urllib.parse import urlparse


def build_game_url(ticket_url: str) -> str:
    return ticket_url.replace("/detail/", "/game/", 1)


def build_purchase_url(ticket_url: str, game_id: str) -> str:
    path = urlparse(ticket_url).path.rstrip("/")
    game_code = path.rsplit("/", 1)[-1]
    return f"https://tixcraft.com/ticket/area/{game_code}/{game_id}"


def normalize_text(value: str) -> str:
    return " ".join(value.split())


def parse_show_time_parts(show_time: str) -> tuple[str, str]:
    normalized = show_time.strip()
    for layout in ("%Y/%m/%d %H:%M", "%Y/%m/%d %H:%M:%S"):
        try:
            parsed = datetime.strptime(normalized, layout)
        except ValueError:
            continue
        return parsed.strftime("%Y/%m/%d"), parsed.strftime("%H:%M")

    raise RuntimeError(f"SHOW_TIME 格式無法解析：{show_time}")


def build_schedule_screenshot_path(show_time: str) -> Path:
    SCREENSHOT_DIR.mkdir(parents=True, exist_ok=True)
    safe_name = show_time.replace("/", "-").replace(":", "-").replace(" ", "_")
    return SCREENSHOT_DIR / f"show-time-{safe_name}.png"


def schedule_time_matches(cell_text: str, show_date: str, show_clock: str) -> bool:
    normalized = normalize_text(cell_text)
    return show_date in normalized and show_clock in normalized


async def find_matching_schedule_row(page, show_time: str):
    table = await robust_select(page, "table.table.table-bordered", timeout=10)
    if not table:
        return None

    header_cells = await robust_child_select_all(table, "thead tr th")
    headers = tuple(normalize_text(cell.text_all) for cell in header_cells[:4])
    if headers != TARGET_TABLE_HEADERS:
        return None

    show_date, show_clock = parse_show_time_parts(show_time)
    rows = await robust_child_select_all(table, "tbody tr")
    for row in rows:
        cells = await robust_child_select_all(row, "td")
        if len(cells) < 4:
            continue

        cell_text = normalize_text(cells[0].text_all)
        if schedule_time_matches(cell_text, show_date, show_clock):
            return row, cells

    return None


async def locate_purchase_button(page, show_time: str):
    matched = await find_matching_schedule_row(page, show_time)
    if not matched:
        return None, {"status": "row_not_found", "message": "找不到符合 SHOW_TIME 的場次"}

    row, cells = matched
    purchase_cell = cells[3]
    purchase_button = await robust_child_select(purchase_cell, "button[data-href]")
    status_text = normalize_text(purchase_cell.text_all)

    info = {
        "time": normalize_text(cells[0].text_all),
        "name": normalize_text(cells[1].text_all),
        "venue": normalize_text(cells[2].text_all),
        "game_id": await robust_get_attr(row, "data-key"),
        "status_text": status_text,
    }

    if purchase_button:
        info["status"] = "on_sale"
        info["purchase_url"] = await robust_get_attr(purchase_button, "data-href")
        return purchase_button, info

    if "開賣" in status_text and "剩餘" in status_text:
        info["status"] = "not_yet_on_sale"
    else:
        info["status"] = "unavailable"
    return None, info
