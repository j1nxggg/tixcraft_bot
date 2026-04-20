import json
from datetime import datetime

from runtime_context import (
    ENV_PATH,
    INVALID_ENV_VALUES,
    PROFILE_META_PATH,
    REQUIRED_ENV_KEYS,
)


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
