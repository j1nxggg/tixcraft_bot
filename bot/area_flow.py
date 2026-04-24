import re
import unicodedata

from browser_ops import (
    robust_child_select,
    robust_child_select_all,
    robust_get_attr,
    robust_select,
)
from schedule import normalize_text


AREA_LIST_SELECTOR = "div.zone.area-list"


def _normalize_ticket_name(value: str) -> str:
    # NFKC 把全形 ASCII / 兼容字元轉成半形;lower 處理英數大小寫差異
    # 再把空白全部壓掉,避免「1 樓 A 區」vs「1樓A區」不相等
    normalized = unicodedata.normalize("NFKC", value)
    stripped = "".join(normalized.split())
    return stripped.lower()


def _normalize_ticket_price(value: str) -> str:
    # 價錢先做 NFKC 防止全形數字混進來
    normalized = unicodedata.normalize("NFKC", value)
    return re.sub(r"[^\d]", "", normalized)


def _split_area_label(label: str) -> tuple[str, str]:
    # 先 NFKC 把全形數字 / 全形逗號轉成半形,regex 才能抓到尾端價錢
    nfkc_normalized = unicodedata.normalize("NFKC", label)
    normalized = normalize_text(nfkc_normalized).strip()
    match = re.match(r"^(.*?)(\d[\d,]*)$", normalized)
    if not match:
        return _normalize_ticket_name(normalized), ""

    ticket_name = _normalize_ticket_name(match.group(1))
    ticket_price = _normalize_ticket_price(match.group(2))
    return ticket_name, ticket_price


def _parse_remaining_count(remaining_text: str) -> int | None:
    match = re.search(r"剩餘\s*(\d+)", normalize_text(remaining_text))
    if not match:
        return None
    return int(match.group(1))


def _has_stock(entry: dict) -> bool:
    remaining_count = entry["remaining_count"]
    return remaining_count is None or remaining_count > 0


def _build_entry_description(entry: dict) -> str:
    remaining_text = entry["remaining_text"]
    if remaining_text:
        return f"{entry['label']} | {remaining_text}"
    return entry["label"]


async def _collect_area_entries(page) -> list[dict]:
    # rush 成功後 Chrome 端 document swap 需要幾毫秒穩定下來,
    # 這邊用 robust_select 自動處理 "Could not find node" 的 stale node race。
    container = await robust_select(page, AREA_LIST_SELECTOR, timeout=10)
    if not container:
        return []

    links = await robust_child_select_all(container, "ul.area-list li a")
    entries = []
    for index, link in enumerate(links):
        raw_text = normalize_text(link.text_all)
        font = await robust_child_select(link, "font")
        remaining_text = normalize_text(font.text_all) if font else ""

        label = raw_text
        if remaining_text:
            label = normalize_text(label.replace(remaining_text, " "))

        ticket_name, ticket_price = _split_area_label(label)
        href = await robust_get_attr(link, "href")

        entries.append(
            {
                "index": index,
                "link": link,
                "href": href,
                "label": label.strip(),
                "ticket_name": ticket_name,
                "ticket_price": ticket_price,
                "remaining_text": remaining_text,
                "remaining_count": _parse_remaining_count(remaining_text),
            }
        )

    return entries


def _find_target_index(entries: list[dict], ticket_name: str, ticket_price: str) -> int | None:
    normalized_name = _normalize_ticket_name(ticket_name)
    normalized_price = _normalize_ticket_price(ticket_price)

    for index, entry in enumerate(entries):
        if entry["ticket_name"] == normalized_name and entry["ticket_price"] == normalized_price:
            return index
    return None


def _find_fallback_entry(entries: list[dict], start_index: int, fallback_policy: str):
    step = 1 if fallback_policy == "往下找" else -1
    index = start_index + step

    while 0 <= index < len(entries):
        entry = entries[index]
        if _has_stock(entry):
            return entry
        index += step

    return None


async def _navigate_to_entry(page, entry: dict) -> None:
    from browser_ops import js_navigate, robust_click

    href = entry.get("href") or ""
    if href.startswith("/"):
        href = f"https://tixcraft.com{href}"
    if href:
        # 用 JS navigate 代替 page.get(Page.navigate CDP)可避開
        # rush 後 CDP transaction 卡死的 race
        await js_navigate(page, href)
        return
    # 萬一拿不到 href 才 fallback 去點擊
    await robust_click(entry["link"])


async def select_area_ticket(page, ticket_name: str, ticket_price: str, fallback_policy: str) -> dict:
    entries = await _collect_area_entries(page)
    if not entries:
        return {"status": "area_list_not_found"}

    target_index = _find_target_index(entries, ticket_name, ticket_price)
    if target_index is None:
        return {
            "status": "target_not_found",
            "ticket_name": _normalize_ticket_name(ticket_name),
            "ticket_price": _normalize_ticket_price(ticket_price),
        }

    target_entry = entries[target_index]
    if _has_stock(target_entry):
        await _navigate_to_entry(page, target_entry)
        return {
            "status": "selected_exact",
            "selected_entry": target_entry,
        }

    fallback_entry = _find_fallback_entry(entries, target_index, fallback_policy)
    if fallback_entry is None:
        return {
            "status": "no_available_fallback",
            "target_entry": target_entry,
            "fallback_policy": fallback_policy,
        }

    await _navigate_to_entry(page, fallback_entry)
    return {
        "status": "selected_fallback",
        "target_entry": target_entry,
        "selected_entry": fallback_entry,
        "fallback_policy": fallback_policy,
    }


def format_area_selection_result(result: dict) -> str:
    status = result["status"]

    if status == "area_list_not_found":
        return "進入選區頁後找不到區域清單"
    if status == "target_not_found":
        return (
            "找不到符合票券設定的區域："
            f"{result['ticket_name']} {result['ticket_price']}"
        )
    if status == "selected_exact":
        return f"已導向目標區域：{_build_entry_description(result['selected_entry'])}"
    if status == "selected_fallback":
        return (
            "目標區域剩餘 0，"
            f"依 {result['fallback_policy']} 改選："
            f"{_build_entry_description(result['selected_entry'])}"
        )
    if status == "no_available_fallback":
        return (
            "目標區域剩餘 0，且依 "
            f"{result['fallback_policy']} 找不到可用區域："
            f"{_build_entry_description(result['target_entry'])}"
        )

    return f"未知的區域選取結果：{status}"
