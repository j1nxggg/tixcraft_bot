import json
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path

from dotenv import dotenv_values

from logger import log  # noqa: F401  re-export for backward-compat


BOT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = BOT_DIR.parent
PROFILE_DIR = PROJECT_ROOT / "Profile"
PROFILE_META_PATH = PROFILE_DIR / ".bot-meta.json"
CDP_ENDPOINT_PATH = PROFILE_DIR / ".bot-cdp.json"
ENV_PATH = BOT_DIR / ".env"
SCREENSHOT_DIR = BOT_DIR / "screenshots"
TEMP_DIR = BOT_DIR / "temp"

REQUIRED_ENV_KEYS = (
    "CHROME_PROFILE_DIR",
    "LOGIN_PROVIDER",
    "TICKET_URL",
    "TICKET_NAME",
    "TICKET_PRICE",
    "TICKET_QUANTITY",
    "SHOW_TIME",
    "FALLBACK_POLICY",
    "GRAB_TIME",
)
INVALID_ENV_VALUES = {"", "none", "null"}
VALID_FALLBACK_POLICIES = {"往下找", "往上找"}
TARGET_TABLE_HEADERS = ("演出時間", "場次名稱", "場地", "購買狀態")

TAIPEI_TZ = timezone(timedelta(hours=8))
CALIBRATION_URL = "https://tixcraft.com/"
CALIBRATION_SAMPLE_COUNT = 5
CALIBRATION_INTERVAL_SECONDS = 15 * 60
TICKET_AREA_PATH_FRAGMENT = "/ticket/area/"


def patch_nodriver_network_file() -> None:
    network_path = (
        PROJECT_ROOT
        / "venv"
        / "Lib"
        / "site-packages"
        / "nodriver"
        / "cdp"
        / "network.py"
    )

    if not network_path.exists():
        return

    data = network_path.read_bytes()
    cleaned = data.replace(b"\xB1", b"").replace("\uFFFD".encode("utf-8"), b"")
    if cleaned != data:
        network_path.write_bytes(cleaned)


# Python 3.14 對 `finally` 裡的 continue 會 SyntaxWarning(未來可能變 SyntaxError)。
# nodriver _register_handlers 裡有一個結構上多餘的 finally:\n continue,
# 同迴圈的 except 已經 continue,拿掉不影響行為。
_FINALLY_CONTINUE_PATTERN = re.compile(
    r"^[ \t]+finally:[ \t]*\r?\n[ \t]+continue[ \t]*\r?\n",
    re.MULTILINE,
)
_TRANSACTION_SET_EXCEPTION_PATTERN = re.compile(
    r"return self\.set_exception\(ProtocolException\(response\[\x22error\x22\]\)\)"
)
_TRANSACTION_SET_RESULT_PATTERN = re.compile(
    r"^([ \t]+)self\.set_result\(e\.value\)[ \t]*$",
    re.MULTILINE,
)


def patch_nodriver_connection_file() -> None:
    connection_path = (
        PROJECT_ROOT
        / "venv"
        / "Lib"
        / "site-packages"
        / "nodriver"
        / "core"
        / "connection.py"
    )

    if not connection_path.exists():
        return

    text = connection_path.read_text(encoding="utf-8")
    patched = _FINALLY_CONTINUE_PATTERN.sub("", text)
    patched = _TRANSACTION_SET_EXCEPTION_PATTERN.sub(
        "return None if self.done() else self.set_exception(ProtocolException(response[\"error\"]))",
        patched,
    )
    patched = _TRANSACTION_SET_RESULT_PATTERN.sub(
        r"\1if not self.done():\n\1    self.set_result(e.value)",
        patched,
    )

    if patched == text:
        return

    connection_path.write_text(patched, encoding="utf-8")


def load_env_config() -> dict[str, str]:
    if not ENV_PATH.exists():
        raise RuntimeError(f"找不到設定檔：{ENV_PATH}")

    # dotenv_values 不會污染 os.environ,且自動處理引號與跳脫
    raw = dotenv_values(ENV_PATH, encoding="utf-8")
    config: dict[str, str] = {
        key: (value or "").strip() for key, value in raw.items() if key
    }

    invalid_keys = [
        key
        for key in REQUIRED_ENV_KEYS
        if config.get(key, "").lower() in INVALID_ENV_VALUES
    ]
    if invalid_keys:
        joined = ", ".join(invalid_keys)
        raise RuntimeError(f".env 缺少必要欄位或值無效：{joined}")

    provider = config["LOGIN_PROVIDER"].lower()
    if provider not in {"google", "facebook"}:
        raise RuntimeError("LOGIN_PROVIDER 只接受 google 或 facebook")
    config["LOGIN_PROVIDER"] = provider

    fallback_policy = config["FALLBACK_POLICY"]
    if fallback_policy not in VALID_FALLBACK_POLICIES:
        raise RuntimeError("FALLBACK_POLICY 只接受 往下找 或 往上找")
    config["FALLBACK_POLICY"] = fallback_policy

    return config


def load_cdp_endpoint() -> dict:
    # 讀取上次成功啟動 Chrome 時寫入的 CDP endpoint 資訊(port/pid)。
    # 回傳空 dict 代表沒有可重用的 endpoint。
    if not CDP_ENDPOINT_PATH.exists():
        return {}

    try:
        return json.loads(CDP_ENDPOINT_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def load_profile_metadata() -> dict:
    if not PROFILE_META_PATH.exists():
        return {}

    try:
        return json.loads(PROFILE_META_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def save_profile_metadata(metadata: dict) -> None:
    try:
        PROFILE_META_PATH.parent.mkdir(parents=True, exist_ok=True)
        PROFILE_META_PATH.write_text(
            json.dumps(metadata, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    except OSError:
        pass


def first_login_pending() -> bool:
    """CLI 新複製 Profile 時會寫 .bot-meta.json 但沒有 first_login_completed_at。"""
    metadata = load_profile_metadata()
    if not metadata:
        return False
    return not bool(str(metadata.get("first_login_completed_at", "")).strip())


def complete_first_login() -> None:
    metadata = load_profile_metadata()
    if not metadata:
        metadata = {}
    if metadata.get("first_login_completed_at"):
        return
    metadata["first_login_completed_at"] = datetime.now(TAIPEI_TZ).isoformat()
    save_profile_metadata(metadata)


def save_cdp_endpoint(port: int, pid: int | None = None) -> None:
    payload = {"port": int(port)}
    if pid is not None:
        payload["pid"] = int(pid)

    try:
        CDP_ENDPOINT_PATH.parent.mkdir(parents=True, exist_ok=True)
        CDP_ENDPOINT_PATH.write_text(
            json.dumps(payload, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )
    except OSError:
        pass


def clear_cdp_endpoint() -> None:
    try:
        CDP_ENDPOINT_PATH.unlink(missing_ok=True)
    except OSError:
        pass
