from datetime import timedelta, timezone
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
