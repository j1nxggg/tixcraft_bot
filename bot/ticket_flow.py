"""票券表單流程 — 選票數、OCR 驗證碼、送出表單、等 checkout。"""
from __future__ import annotations

import asyncio
import base64
import io
import sys
import wave
from pathlib import Path

from nodriver import cdp

from browser_ops import (
    robust_child_select,
    robust_click,
    robust_evaluate,
    robust_select,
    robust_select_option,
    robust_send,
    robust_send_keys,
)
from logger import alog, log
from ocr import recognize_captcha_bytes
from runtime_context import BOT_DIR


SUCCESS_SOUND_PATH = BOT_DIR / "sound" / "Tixcraft_Sueecss_sound.aiff"

QUANTITY_SELECT_SELECTOR = "select[id^='TicketForm_ticketPrice_']"
CAPTCHA_IMAGE_SELECTOR = "#TicketForm_verifyCode-image"
VERIFY_CODE_INPUT_SELECTOR = "#TicketForm_verifyCode"
AGREE_CHECKBOX_SELECTOR = "#TicketForm_agree"
SUBMIT_BUTTON_SELECTOR = "button.btn-primary.btn-green[type='submit']"

CHECKOUT_URL_FRAGMENT = "/ticket/checkout"
SUBMIT_RESULT_TIMEOUT = 3600.0  # 一小時,搶票高峰主機可能很慢
SUBMIT_RESULT_POLL = 0.2
MAX_RETRIES = 30
SETUP_FAILED_MAX_RETRIES = 5
SETUP_FAILED_BACKOFF = 0.5

# 在瀏覽器裡跑 fetch,走現成 session cookie,直接拿 captcha 圖的原始 bytes。
# 回傳 base64 string 讓 CDP 能把 binary 帶回 Python。
_FETCH_CAPTCHA_JS = """
(async () => {
  const img = document.querySelector('#TicketForm_verifyCode-image');
  if (!img) return null;
  const src = img.getAttribute('src');
  if (!src) return null;
  const resp = await fetch(src, { credentials: 'include', cache: 'no-store' });
  if (!resp.ok) return null;
  const buf = await resp.arrayBuffer();
  const bytes = new Uint8Array(buf);
  let binary = '';
  const chunk = 0x8000;
  for (let i = 0; i < bytes.length; i += chunk) {
    binary += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk));
  }
  return btoa(binary);
})()
"""


# ─────────────────────────────────────────────────────────────────────────
# 表單個別動作
# ─────────────────────────────────────────────────────────────────────────
async def _select_quantity(page, quantity: str) -> bool:
    select_element = await robust_select(page, QUANTITY_SELECT_SELECTOR, timeout=15)
    if not select_element:
        log("找不到票數 select 元素")
        return False

    option = await robust_child_select(select_element, f"option[value='{quantity}']")
    if not option:
        log(f"找不到 value={quantity} 的 option")
        return False

    return await robust_select_option(option)


async def _fetch_captcha_bytes(page) -> bytes | None:
    # 用 robust_evaluate 的 await_promise=True 版本拿 base64 字串。
    # timeout 拉長到 10s,因為伺服器回 captcha 圖有時會慢。
    result = await robust_evaluate(
        page,
        _FETCH_CAPTCHA_JS,
        await_promise=True,
        return_by_value=True,
        timeout=10.0,
    )

    if not result or not isinstance(result, str):
        log(f"fetch captcha 回傳非預期內容:{result!r}")
        return None

    try:
        return base64.b64decode(result)
    except Exception as exc:
        log(f"解碼 captcha base64 失敗:{exc!r}")
        return None


async def _fill_verify_code(page, code: str) -> bool:
    input_element = await robust_select(page, VERIFY_CODE_INPUT_SELECTOR, timeout=5)
    if not input_element:
        log("找不到驗證碼輸入框")
        return False

    # 每輪打開表單時欄位本來就是空的,不用 clear_input
    return await robust_send_keys(input_element, code)


async def _check_agreement(page) -> bool:
    checkbox = await robust_select(page, AGREE_CHECKBOX_SELECTOR, timeout=5)
    if not checkbox:
        log("找不到同意條款 checkbox")
        return False

    return await robust_click(checkbox)


async def _submit_form(page) -> bool:
    submit_button = await robust_select(page, SUBMIT_BUTTON_SELECTOR, timeout=5)
    if not submit_button:
        log("找不到送出按鈕")
        return False

    return await robust_click(submit_button)


# ─────────────────────────────────────────────────────────────────────────
# Dialog handler — 拓元在驗證碼錯誤時會跳 alert,用 CDP 事件攔下自動按掉;
# 其它 dialog(confirm 取消訂單)也一樣 accept=True,讓使用者手動取消行為不變。
# ─────────────────────────────────────────────────────────────────────────
def _register_dialog_handler(page, alert_flag: dict) -> None:
    async def on_dialog(event: cdp.page.JavascriptDialogOpening):
        alert_flag["triggered"] = True
        # 關鍵路徑的 log 用 async 版,避免 emit lock 瞬間 block event loop
        await alog(f"偵測到 alert:{event.message}")
        await robust_send(
            page,
            cdp.page.handle_java_script_dialog(accept=True),
            timeout=2.0,
            log_prefix="關閉 alert",
        )

    page.add_handler(cdp.page.JavascriptDialogOpening, on_dialog)


# ─────────────────────────────────────────────────────────────────────────
# 搶票成功提示音(播 AIFF,失敗 fallback 蜂鳴)
# ─────────────────────────────────────────────────────────────────────────
def _play_success_sound_sync() -> None:
    if sys.platform != "win32":
        return

    if not SUCCESS_SOUND_PATH.exists():
        log(f"找不到提示音檔 {SUCCESS_SOUND_PATH},使用蜂鳴 fallback")
        _beep_fallback()
        return

    try:
        _play_aiff_with_winsound(SUCCESS_SOUND_PATH)
    except Exception as exc:
        log(f"播放 {SUCCESS_SOUND_PATH.name} 失敗:{exc!r},使用蜂鳴 fallback")
        _beep_fallback()


def _play_aiff_with_winsound(path: Path) -> None:
    # winsound 只支援 WAV,拓元給的是 AIFF-C。用 soundfile 解碼後
    # 用 stdlib wave 重新包成 WAV bytes 餵給 winsound.PlaySound(SND_MEMORY)。
    import soundfile as sf
    import winsound

    data, samplerate = sf.read(str(path), dtype="int16")
    channels = 1 if data.ndim == 1 else data.shape[1]

    buf = io.BytesIO()
    with wave.open(buf, "wb") as wav:
        wav.setnchannels(channels)
        wav.setsampwidth(2)  # int16 = 2 bytes
        wav.setframerate(samplerate)
        wav.writeframes(data.tobytes())

    winsound.PlaySound(buf.getvalue(), winsound.SND_MEMORY | winsound.SND_NODEFAULT)


def _beep_fallback() -> None:
    try:
        import winsound
        # 1000 Hz 蜂鳴,分 5 次各 500 ms
        for _ in range(5):
            winsound.Beep(1000, 500)
    except Exception as exc:
        log(f"蜂鳴 fallback 失敗:{exc!r}")


async def _play_success_sound() -> None:
    try:
        await asyncio.to_thread(_play_success_sound_sync)
    except Exception as exc:
        log(f"播放提示音失敗:{exc!r}")


# ─────────────────────────────────────────────────────────────────────────
# 結果等待 & 重試主迴圈
# ─────────────────────────────────────────────────────────────────────────
async def _wait_submit_result(page, alert_flag: dict) -> str:
    # 回傳 "success" / "captcha_error" / "timeout"
    # 策略:同時監看 alert 旗標 + Tab.url,看哪個先到。
    # URL 靠 CDP Target 事件更新,不用 evaluate,避免被 CDP bug 拖住。
    alert_flag["triggered"] = False
    deadline = asyncio.get_event_loop().time() + SUBMIT_RESULT_TIMEOUT

    while asyncio.get_event_loop().time() < deadline:
        if alert_flag["triggered"]:
            return "captcha_error"

        current_url = (getattr(page, "url", "") or "").lower()
        if CHECKOUT_URL_FRAGMENT in current_url:
            return "success"

        await asyncio.sleep(SUBMIT_RESULT_POLL)

    return "timeout"


async def _run_ticket_attempt(page, ocr_session, quantity: str, alert_flag: dict) -> str:
    if not await _select_quantity(page, quantity):
        return "setup_failed"
    await alog(f"已選取票數 {quantity}")

    captcha_bytes = await _fetch_captcha_bytes(page)
    if captcha_bytes is None:
        return "setup_failed"

    code = recognize_captcha_bytes(ocr_session, captcha_bytes)
    await alog(f"OCR 辨識結果:{code}")

    if not await _fill_verify_code(page, code):
        return "setup_failed"
    await alog("已填入驗證碼")

    if not await _check_agreement(page):
        return "setup_failed"
    await alog("已勾選同意條款")

    if not await _submit_form(page):
        return "setup_failed"
    await alog("已送出表單,等待結果")

    return await _wait_submit_result(page, alert_flag)


async def process_ticket_form(page, ocr_session, quantity: str) -> bool:
    log(f"開始處理票券表單,票數={quantity}")

    alert_flag = {"triggered": False}
    _register_dialog_handler(page, alert_flag)

    # 開啟 Page domain 事件才會被觸發,否則 javascriptDialogOpening 收不到
    await robust_send(page, cdp.page.enable(), timeout=3.0, log_prefix="啟用 Page domain")

    setup_failed_count = 0
    for attempt in range(1, MAX_RETRIES + 1):
        await alog(f"第 {attempt} 輪嘗試")
        result = await _run_ticket_attempt(page, ocr_session, quantity, alert_flag)

        if result == "success":
            log("搶票成功,已進入 checkout 頁面")
            await _play_success_sound()
            log("請盡快在瀏覽器完成付款!程式結束但瀏覽器保留。")
            return True

        if result == "captcha_error":
            setup_failed_count = 0
            await alog("驗證碼錯誤,重新嘗試")
            # alert 被按掉後頁面會留在原本的 ticket 表單,可以直接進下一輪
            continue

        if result == "setup_failed":
            setup_failed_count += 1
            if setup_failed_count > SETUP_FAILED_MAX_RETRIES:
                log(f"表單元素操作連續失敗 {setup_failed_count} 次,中止")
                return False
            await alog(
                "表單元素操作失敗,"
                f"等待 {SETUP_FAILED_BACKOFF:.1f}s 後重試"
                f"({setup_failed_count}/{SETUP_FAILED_MAX_RETRIES})"
            )
            await asyncio.sleep(SETUP_FAILED_BACKOFF)
            continue

        if result == "timeout":
            log("等待結果逾時一小時,中止")
            return False

    log(f"已達重試上限 {MAX_RETRIES} 次,中止")
    return False
