import json
from pathlib import Path

from runtime_context import PROFILE_DIR


def normalize_profile_exit_state() -> None:
    local_state_path = PROFILE_DIR / "Local State"
    profile_dirs = [
        path
        for path in PROFILE_DIR.iterdir()
        if path.is_dir() and (path.name == "Default" or path.name.startswith("Profile "))
    ]

    if local_state_path.exists():
        patch_json_file(local_state_path, apply_local_state_patch)

    for profile_dir in profile_dirs:
        preferences_path = profile_dir / "Preferences"
        if preferences_path.exists():
            patch_json_file(preferences_path, apply_preferences_patch)


def patch_json_file(path: Path, patcher) -> None:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not patcher(data):
        return

    path.write_text(
        json.dumps(data, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )


def apply_local_state_patch(data: dict) -> bool:
    changed = False

    was = data.setdefault("was", {})
    if was.get("restarted") is not False:
        was["restarted"] = False
        changed = True

    return changed


def apply_preferences_patch(data: dict) -> bool:
    changed = False

    profile = data.setdefault("profile", {})
    if profile.get("exit_type") != "Normal":
        profile["exit_type"] = "Normal"
        changed = True
    if profile.get("exited_cleanly") is not True:
        profile["exited_cleanly"] = True
        changed = True

    session = data.setdefault("session", {})
    if session.get("exit_type") != "Normal":
        session["exit_type"] = "Normal"
        changed = True
    if session.get("exited_cleanly") is not True:
        session["exited_cleanly"] = True
        changed = True

    return changed
