import json
from datetime import datetime, timedelta, timezone
from pathlib import Path


BOT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = BOT_DIR.parent
PROFILE_DIR = PROJECT_ROOT / "Profile"
PROFILE_META_PATH = PROJECT_ROOT / ".profile-meta.json"
ENV_PATH = BOT_DIR / ".env"
SCREENSHOT_DIR = BOT_DIR / "screenshots"

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
TARGET_TABLE_HEADERS = ("演出時間", "場次名稱", "場地", "購買狀態")

TAIPEI_TZ = timezone(timedelta(hours=8))
CALIBRATION_URL = "https://tixcraft.com/"
CALIBRATION_SAMPLE_COUNT = 5
CALIBRATION_INTERVAL_SECONDS = 15 * 60
TICKET_AREA_PATH_FRAGMENT = "/ticket/area/"


def log(msg: str) -> None:
    print(msg, flush=True)


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


def load_env_config() -> dict[str, str]:
    if not ENV_PATH.exists():
        raise RuntimeError(f"找不到設定檔：{ENV_PATH}")

    config: dict[str, str] = {}
    for line in ENV_PATH.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue

        key, value = line.split("=", 1)
        config[key.strip()] = value.strip().strip("'\"")

    invalid_keys = []
    for key in REQUIRED_ENV_KEYS:
        value = config.get(key, "").strip()
        if value.lower() in INVALID_ENV_VALUES:
            invalid_keys.append(key)

    if invalid_keys:
        joined = ", ".join(invalid_keys)
        raise RuntimeError(f".env 缺少必要欄位或值無效：{joined}")

    provider = config["LOGIN_PROVIDER"].strip().lower()
    if provider not in {"google", "facebook"}:
        raise RuntimeError("LOGIN_PROVIDER 只接受 google 或 facebook")
    config["LOGIN_PROVIDER"] = provider

    return config


def load_profile_metadata() -> dict:
    if not PROFILE_META_PATH.exists():
        return {}

    try:
        return json.loads(PROFILE_META_PATH.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}


def save_profile_metadata(metadata: dict) -> None:
    PROFILE_META_PATH.write_text(
        json.dumps(metadata, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def first_login_pending(metadata: dict) -> bool:
    return not bool(str(metadata.get("first_login_completed_at", "")).strip())


def complete_first_login(metadata: dict) -> None:
    metadata["first_login_completed_at"] = datetime.now().astimezone().isoformat()
    save_profile_metadata(metadata)
