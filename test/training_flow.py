"""Automation flow for https://ticket-training.onrender.com/."""
from __future__ import annotations

import asyncio
import base64
import json

from nodriver import cdp

from browser_ops import (
    js_navigate,
    robust_child_select,
    robust_click,
    robust_evaluate,
    robust_select,
    robust_select_option,
    robust_send,
    robust_send_keys,
    wait_ready_state,
)
from logger import alog, log
from ocr import recognize_captcha_bytes


TRAINING_URL = "https://ticket-training.onrender.com/"

COUNTDOWN_INPUT_SELECTOR = "#countdownInput"
START_BUTTON_SELECTOR = "#startButton"
PURCHASE_SECTION_BUTTON_SELECTOR = "#purchaseButton"
ORDER_BUTTON_SELECTOR = "button.purchase-button"
SEAT_ITEM_SELECTOR = "div.seat-item"
QUANTITY_SELECT_SELECTOR = "select.quantity-select[name='quantity']"
CAPTCHA_IMAGE_SELECTOR = "#captcha-image"
CAPTCHA_INPUT_SELECTOR = "#captcha-input"
TERMS_CHECKBOX_SELECTOR = "#terms-checkbox"
CONFIRM_BUTTON_SELECTOR = "button.confirm-btn[type='submit']"
SUCCESS_MESSAGE_SELECTOR = "div.success-message"

SEAT_NAME = "B2層平面002區"
SEAT_PRICE = "7880"
SEAT_CLICK_TIMEOUT = 30.0
SEAT_CLICK_INTERVAL = 0.08
FORM_MAX_RETRIES = 30
SUBMIT_RESULT_TIMEOUT = 5.0
SUBMIT_RESULT_POLL = 0.1
SUCCESS_WAIT_TIMEOUT = 10.0
HOME_WAIT_TIMEOUT = 10.0


_SET_COUNTDOWN_JS = """
(() => {
  const input = document.querySelector('#countdownInput');
  if (!input) return false;
  input.value = '1';
  input.dispatchEvent(new Event('input', { bubbles: true }));
  input.dispatchEvent(new Event('change', { bubbles: true }));
  return true;
})()
"""

_CLICK_TARGET_SEAT_JS = f"""
(() => {{
  const seats = Array.from(document.querySelectorAll({SEAT_ITEM_SELECTOR!r}));
  const target = seats.find((seat) => {{
    const text = (seat.innerText || seat.textContent || '').replace(/\\s+/g, ' ');
    return text.includes({SEAT_NAME!r}) && text.includes({SEAT_PRICE!r});
  }});
  if (!target) return false;
  target.click();
  return true;
}})()
"""

_FETCH_CAPTCHA_JS = """
(async () => {
  const img = document.querySelector('#captcha-image');
  if (!img) return null;
  if (!img.complete || img.naturalWidth === 0) {
    await new Promise((resolve) => {
      const done = () => resolve();
      img.addEventListener('load', done, { once: true });
      img.addEventListener('error', done, { once: true });
      setTimeout(done, 3000);
    });
  }

  const src = img.currentSrc || img.src || img.getAttribute('src');
  if (!src) return null;
  const url = new URL(src, location.href).toString();
  try {
    const resp = await fetch(url, { credentials: 'include', cache: 'no-store' });
    if (resp.ok) {
      const buf = await resp.arrayBuffer();
      const bytes = new Uint8Array(buf);
      let binary = '';
      const chunk = 0x8000;
      for (let i = 0; i < bytes.length; i += chunk) {
        binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
      }
      return btoa(binary);
    }
  } catch (_) {
  }

  try {
    const canvas = document.createElement('canvas');
    canvas.width = img.naturalWidth || img.width;
    canvas.height = img.naturalHeight || img.height;
    const ctx = canvas.getContext('2d');
    ctx.drawImage(img, 0, 0);
    const dataUrl = canvas.toDataURL('image/png');
    return dataUrl.split(',', 2)[1] || null;
  } catch (_) {
    return null;
  }
})()
"""


async def run_training_flow(page, ocr_session, *, run_count: int = 1) -> bool:
    run_count = min(max(int(run_count), 1), 30)
    log(f"測試模式：開始執行 ticket-training 流程，連續執行 {run_count} 次")
    await wait_ready_state(page, state="complete", timeout=10.0)
    alert_flag = {"triggered": False, "message": ""}
    _register_dialog_handler(page, alert_flag)
    await robust_send(page, cdp.page.enable(), timeout=3.0, log_prefix="測試模式：啟用 Page domain")
    stats = {
        "elapsed_times": [],
        "captcha_errors": 0,
    }

    for run_index in range(1, run_count + 1):
        log(f"測試模式：開始第 {run_index}/{run_count} 次")

        if run_index == 1 and not await _set_countdown_to_one(page):
            return False

        success_info = await _run_single_training_round(page, ocr_session, alert_flag, stats)
        if success_info is None:
            return False

        elapsed = success_info.get("elapsed_seconds")
        if isinstance(elapsed, (int, float)):
            stats["elapsed_times"].append(float(elapsed))
            log(f"測試模式：第 {run_index}/{run_count} 次搶票成功，花費 {elapsed:.2f} 秒")
        else:
            log(f"測試模式：第 {run_index}/{run_count} 次搶票成功，未解析到花費時間")

        if run_index < run_count:
            await alog(f"測試模式：準備進行第 {run_index + 1}/{run_count} 次，直接回到測試首頁")
            if not await _navigate_home_for_next_round(page):
                return False

    _log_summary(run_count, stats)
    log(f"測試模式：已完成連續 {run_count} 次")
    return True


async def _run_single_training_round(page, ocr_session, alert_flag: dict, stats: dict) -> dict | None:
    if not await _click_required(START_BUTTON_SELECTOR, page, "開始倒數計時"):
        return None

    await asyncio.sleep(1.0)
    if not await _click_required(PURCHASE_SECTION_BUTTON_SELECTOR, page, "立即購票"):
        return None
    if not await _click_required(ORDER_BUTTON_SELECTOR, page, "立即訂購"):
        return None

    if not await _click_seat_until_form_ready(page):
        return None
    return await _process_training_form(page, ocr_session, alert_flag, stats)


def _log_summary(run_count: int, stats: dict) -> None:
    elapsed_times = list(stats.get("elapsed_times") or [])
    captcha_errors = int(stats.get("captcha_errors") or 0)
    if not elapsed_times:
        log("測試模式統計：沒有可統計的花費時間")
        return

    if run_count <= 1:
        log("測試模式統計\n" + _format_summary_table([
            ("花費時間", f"{elapsed_times[0]:.2f} 秒"),
        ]))
        return

    average = sum(elapsed_times) / len(elapsed_times)
    log("測試模式統計\n" + _format_summary_table([
        ("執行次數", f"{len(elapsed_times)} 次"),
        ("平均花費時間", f"{average:.2f} 秒"),
        ("花費最多時間", f"{max(elapsed_times):.2f} 秒"),
        ("花費最少時間", f"{min(elapsed_times):.2f} 秒"),
        ("圖片驗證碼錯誤次數", f"{captcha_errors} 次"),
    ]))


def _format_summary_table(rows: list[tuple[str, str]]) -> str:
    headers = ("指標", "次數/時間")
    all_rows = [headers, *rows]
    metric_width = max(_display_width(row[0]) for row in all_rows)
    time_width = max(_display_width(row[1]) for row in all_rows)

    border = (
        "+"
        + "-" * (metric_width + 2)
        + "+"
        + "-" * (time_width + 2)
        + "+"
    )

    lines = [border, _format_table_row(headers, metric_width, time_width), border]
    for row in rows:
        lines.append(_format_table_row(row, metric_width, time_width))
    lines.append(border)
    return "\n".join(lines)


def _format_table_row(row: tuple[str, str], metric_width: int, time_width: int) -> str:
    metric, value = row
    return (
        "| "
        + metric
        + " " * (metric_width - _display_width(metric))
        + " | "
        + value
        + " " * (time_width - _display_width(value))
        + " |"
    )


def _display_width(value: str) -> int:
    # 粗略把 CJK 字元算成 2 格,讓中文表格在終端中比較齊。
    width = 0
    for char in value:
        width += 2 if ord(char) > 127 else 1
    return width


def _register_dialog_handler(page, alert_flag: dict) -> None:
    async def on_dialog(event: cdp.page.JavascriptDialogOpening):
        alert_flag["triggered"] = True
        alert_flag["message"] = event.message
        await alog(f"測試模式：偵測到 alert:{event.message}")
        await robust_send(
            page,
            cdp.page.handle_java_script_dialog(accept=True),
            timeout=2.0,
            log_prefix="測試模式：關閉 alert",
        )

    page.add_handler(cdp.page.JavascriptDialogOpening, on_dialog)


async def _set_countdown_to_one(page) -> bool:
    input_element = await robust_select(page, COUNTDOWN_INPUT_SELECTOR, timeout=10.0)
    if not input_element:
        log("測試模式：找不到倒數秒數輸入框")
        return False

    result = await robust_evaluate(page, _SET_COUNTDOWN_JS, timeout=2.0)
    if result is True:
        await alog("測試模式：倒數秒數已設定為 1")
        return True

    log("測試模式：設定倒數秒數失敗")
    return False


async def _click_required(selector: str, page, label: str) -> bool:
    element = await robust_select(page, selector, timeout=10.0)
    if not element:
        log(f"測試模式：找不到 {label} 按鈕")
        return False

    if not await robust_click(element):
        log(f"測試模式：點擊 {label} 失敗")
        return False

    await alog(f"測試模式：已點擊 {label}")
    return True


async def _click_seat_until_form_ready(page) -> bool:
    deadline = asyncio.get_event_loop().time() + SEAT_CLICK_TIMEOUT
    clicked_count = 0

    while asyncio.get_event_loop().time() < deadline:
        form = await robust_select(page, QUANTITY_SELECT_SELECTOR, timeout=0.2)
        if form:
            await alog("測試模式：已進入張數/驗證碼頁")
            return True

        clicked = await robust_evaluate(page, _CLICK_TARGET_SEAT_JS, timeout=1.0)
        if clicked is True:
            clicked_count += 1
            if clicked_count == 1 or clicked_count % 20 == 0:
                await alog(f"測試模式：嘗試點擊目標區域 {clicked_count} 次")

        await asyncio.sleep(SEAT_CLICK_INTERVAL)

    log(
        "測試模式：持續點擊目標區域逾時，"
        f"找不到 {SEAT_NAME} {SEAT_PRICE} 或未進入表單"
    )
    return False


async def _process_training_form(page, ocr_session, alert_flag: dict, stats: dict) -> dict | None:
    for attempt in range(1, FORM_MAX_RETRIES + 1):
        await alog(f"測試模式：表單第 {attempt} 輪嘗試")
        alert_flag["triggered"] = False
        alert_flag["message"] = ""

        if not await _fill_training_form(page, ocr_session):
            return False

        result = await _wait_submit_result(page, alert_flag)
        if result == "captcha_error":
            stats["captcha_errors"] += 1
            await alog("測試模式：驗證碼錯誤，重新執行票數、驗證碼、條款與確認")
            await _clear_captcha_input(page)
            continue

        if isinstance(result, dict) and result.get("status") == "success":
            return result

    log(f"測試模式：已達表單重試上限 {FORM_MAX_RETRIES} 次")
    return None


async def _fill_training_form(page, ocr_session) -> bool:
    if not await _select_quantity_one(page):
        return False

    captcha_bytes = await _fetch_captcha_bytes(page)
    if captcha_bytes is None:
        return False

    code = recognize_captcha_bytes(ocr_session, captcha_bytes)
    await alog(f"測試模式：OCR 辨識結果:{code}")

    captcha_input = await robust_select(page, CAPTCHA_INPUT_SELECTOR, timeout=5.0)
    if not captcha_input:
        log("測試模式：找不到驗證碼輸入框")
        return False
    await _clear_captcha_input(page)
    if not await robust_send_keys(captcha_input, code):
        return False

    if not await _ensure_terms_checked(page):
        return False

    confirm = await robust_select(page, CONFIRM_BUTTON_SELECTOR, timeout=5.0)
    if not confirm:
        log("測試模式：找不到確認張數按鈕")
        return False
    return await robust_click(confirm)


async def _wait_submit_result(page, alert_flag: dict) -> str | dict:
    deadline = asyncio.get_event_loop().time() + max(SUBMIT_RESULT_TIMEOUT, SUCCESS_WAIT_TIMEOUT)
    while asyncio.get_event_loop().time() < deadline:
        if alert_flag["triggered"]:
            message = str(alert_flag.get("message") or "")
            if "驗證碼" in message and "不正確" in message:
                return "captcha_error"
            return "captcha_error"

        success_info = await _read_success_message(page)
        if success_info is not None:
            return success_info

        await asyncio.sleep(SUBMIT_RESULT_POLL)

    log("測試模式：送出後未偵測到搶票成功或驗證碼錯誤")
    return "captcha_error"


async def _read_success_message(page) -> dict | None:
    raw = await robust_evaluate(
        page,
        """
        (() => {
          const message = document.querySelector('div.success-message');
          if (!message) return null;

          const title = message.querySelector('h2');
          const elapsed = message.querySelector('#elapsedTimeDisplay');
          const titleText = (title?.textContent || '').trim();
          const elapsedText = (elapsed?.textContent || message.textContent || '').trim();
          if (titleText !== '搶票成功!') return null;

          const match =
            elapsedText.match(/總共花費了\\s*(\\d+(?:\\.\\d+)?)\\s*秒/) ||
            elapsedText.match(/(\\d+(?:\\.\\d+)?)/);
          if (!match) return null;

          return JSON.stringify({
            status: 'success',
            title: titleText,
            elapsed_text: elapsedText,
            elapsed_seconds: Number(match[1] || match[0])
          });
        })()
        """,
        timeout=1.0,
    )
    if not isinstance(raw, str):
        return None

    try:
        parsed = json.loads(raw)
    except json.JSONDecodeError:
        return None

    if parsed.get("status") != "success":
        return None
    return parsed


async def _success_title_visible(page) -> bool:
    return await _read_success_message(page) is not None


async def _navigate_home_for_next_round(page) -> bool:
    await js_navigate(page, TRAINING_URL)
    if await _wait_home_ready(page, timeout=HOME_WAIT_TIMEOUT):
        await alog("測試模式：已直接導回首頁，準備下一次")
        return True

    log("測試模式：直接導回首頁後仍未偵測到開始按鈕")
    return False


async def _wait_home_ready(page, timeout: float) -> bool:
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        ready = await robust_evaluate(
            page,
            """
            (() => {
              const start = document.querySelector('#startButton');
              const input = document.querySelector('#countdownInput');
              return Boolean(start && input);
            })()
            """,
            timeout=1.0,
        )
        if ready is True:
            return True
        await asyncio.sleep(0.1)
    return False


async def _select_quantity_one(page) -> bool:
    select_element = await robust_select(page, QUANTITY_SELECT_SELECTOR, timeout=10.0)
    if not select_element:
        log("測試模式：找不到票數 select")
        return False

    option = await robust_child_select(select_element, "option[value='1']")
    if not option:
        log("測試模式：找不到票數 1 的 option")
        return False

    if not await robust_select_option(option):
        log("測試模式：選擇票數 1 失敗")
        return False

    await alog("測試模式：已選擇票數 1")
    return True


async def _ensure_terms_checked(page) -> bool:
    checkbox = await robust_select(page, TERMS_CHECKBOX_SELECTOR, timeout=5.0)
    if not checkbox:
        log("測試模式：找不到同意條款 checkbox")
        return False

    checked = await robust_evaluate(
        page,
        """
        (() => {
          const checkbox = document.querySelector('#terms-checkbox');
          if (!checkbox) return false;
          if (!checkbox.checked) {
            checkbox.checked = true;
            checkbox.dispatchEvent(new Event('input', { bubbles: true }));
            checkbox.dispatchEvent(new Event('change', { bubbles: true }));
          }
          return checkbox.checked;
        })()
        """,
        timeout=2.0,
    )
    if checked is not True:
        log("測試模式：勾選同意條款失敗")
        return False

    await alog("測試模式：已勾選同意條款")
    return True


async def _clear_captcha_input(page) -> None:
    await robust_evaluate(
        page,
        """
        (() => {
          const input = document.querySelector('#captcha-input');
          if (!input) return false;
          input.value = '';
          input.dispatchEvent(new Event('input', { bubbles: true }));
          input.dispatchEvent(new Event('change', { bubbles: true }));
          return true;
        })()
        """,
        timeout=1.0,
    )


async def _fetch_captcha_bytes(page) -> bytes | None:
    image = await robust_select(page, CAPTCHA_IMAGE_SELECTOR, timeout=5.0)
    if not image:
        log("測試模式：找不到驗證碼圖片")
        return None

    await _wait_captcha_image_loaded(page)

    result = await robust_evaluate(
        page,
        _FETCH_CAPTCHA_JS,
        await_promise=True,
        return_by_value=True,
        timeout=10.0,
    )
    if not result or not isinstance(result, str):
        log(f"測試模式：fetch captcha 回傳非預期內容:{result!r}")
        return None

    try:
        return base64.b64decode(result)
    except Exception as exc:
        log(f"測試模式：解碼 captcha base64 失敗:{exc!r}")
        return None


async def _wait_captcha_image_loaded(page) -> bool:
    deadline = asyncio.get_event_loop().time() + 5.0
    while asyncio.get_event_loop().time() < deadline:
        loaded = await robust_evaluate(
            page,
            """
            (() => {
              const img = document.querySelector('#captcha-image');
              return Boolean(img && img.complete && img.naturalWidth > 0);
            })()
            """,
            timeout=1.0,
        )
        if loaded is True:
            return True
        await asyncio.sleep(0.1)

    log("測試模式：等待驗證碼圖片載入逾時，仍嘗試讀取圖片")
    return False
