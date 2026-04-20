import asyncio
import json
import subprocess
import sys
from pathlib import Path

from nodriver import Config, cdp, start
from nodriver.core import util as nodriver_util

from config import PROFILE_DIR, PROJECT_ROOT, TICKET_AREA_PATH_FRAGMENT, log


async def close_browser_gracefully(browser) -> None:
    close_error = None

    try:
        if browser.connection and not browser.connection.closed:
            await browser.connection.send(cdp.browser.close())
    except Exception as exc:
        close_error = exc

    process = getattr(browser, "_process", None)
    if process is not None:
        try:
            await asyncio.wait_for(process.wait(), timeout=10)
        except Exception:
            browser.stop()

    try:
        if browser.connection and not browser.connection.closed:
            await browser.connection.disconnect()
    finally:
        nodriver_util.get_registered_instances().discard(browser)

    if close_error is not None:
        raise close_error


def detach_browser_process(browser) -> None:
    process = getattr(browser, "_process", None)
    if process is None:
        return

    stdin = getattr(process, "stdin", None)
    if stdin is not None:
        try:
            stdin.close()
        except Exception:
            pass
        try:
            process.stdin = None
        except Exception:
            pass

    for pipe_name in ("stdout", "stderr"):
        pipe = getattr(process, pipe_name, None)
        if pipe is not None:
            transport = getattr(pipe, "_transport", None)
            if transport is not None:
                try:
                    transport.close()
                except Exception:
                    pass
            try:
                setattr(process, pipe_name, None)
            except Exception:
                pass

    browser._process = None
    browser._process_pid = None


async def disconnect_browser_session(browser) -> None:
    try:
        if browser.connection and not browser.connection.closed:
            await browser.connection.disconnect()
    finally:
        detach_browser_process(browser)
        nodriver_util.get_registered_instances().discard(browser)


async def close_extra_startup_tabs(browser, keep_tab) -> None:
    for tab in list(browser.tabs):
        if tab is keep_tab:
            continue

        url = (getattr(tab, "url", "") or "").lower()
        if url in {"", "about:blank", "chrome://newtab/", "chrome://welcome/"}:
            await tab.close()


def pick_startup_tab(browser):
    for tab in browser.tabs:
        url = (getattr(tab, "url", "") or "").lower()
        if url in {"", "about:blank", "chrome://newtab/", "chrome://welcome/"}:
            return tab

    return browser.tabs[0]


async def start_detached_browser(chrome_profile_dir: str):
    port = nodriver_util.free_port()
    config = Config(
        user_data_dir=str(PROFILE_DIR),
        headless=False,
        browser_args=[
            "--hide-crash-restore-bubble",
            f"--profile-directory={chrome_profile_dir}",
        ],
        host="127.0.0.1",
        port=port,
    )

    creationflags = 0
    if sys.platform == "win32":
        creationflags = subprocess.DETACHED_PROCESS | subprocess.CREATE_NEW_PROCESS_GROUP

    executable = str(config.browser_executable_path)
    launch_args = [executable, *(str(arg) for arg in config())]

    subprocess.Popen(
        launch_args,
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        close_fds=True,
        creationflags=creationflags,
        cwd=str(PROJECT_ROOT),
    )

    return await start(config=config)


async def open_tixcraft_login_provider(page, provider: str) -> bool:
    login_link = await page.select("a[href='#login'][data-bs-toggle='modal']", timeout=8)
    if not login_link:
        return False

    await login_link.click()
    await page.sleep(1)

    provider_selector = "#loginGoogle" if provider == "google" else "#loginFacebook"
    provider_button = await page.select(provider_selector, timeout=8)
    if not provider_button:
        return False

    await provider_button.click()
    return True


async def wait_for_first_login_confirmation(provider: str) -> bool:
    provider_label = "Google" if provider == "google" else "Facebook"
    prompt = (
        "\n首次啟動需要手動登入這份 Profile。\n"
        f"請在 Chrome 手動點選 {provider_label} 完成目標站點登入後，回到此終端按 Enter。\n"
        "若要放棄，請按 Ctrl+C。\n\n"
        "按 Enter 繼續..."
    )

    try:
        await asyncio.to_thread(input, prompt)
        return True
    except (EOFError, KeyboardInterrupt):
        return False


async def rush_purchase_url(page, purchase_url: str) -> None:
    attempt = 0
    while True:
        attempt += 1
        try:
            await page.get(purchase_url)
        except Exception as exc:
            log(f"rush attempt #{attempt} page.get 失敗：{exc!r}")
            continue

        current_url = getattr(page, "url", "") or ""
        if TICKET_AREA_PATH_FRAGMENT in current_url:
            log(f"rush 成功（第 {attempt} 次嘗試）：{current_url}")
            return


def normalize_profile_exit_state() -> None:
    local_state_path = PROFILE_DIR / "Local State"
    profile_dirs = [
        path
        for path in PROFILE_DIR.iterdir()
        if path.is_dir() and (path.name == "Default" or path.name.startswith("Profile "))
    ]

    if local_state_path.exists():
        _patch_json_file(local_state_path, _apply_local_state_patch)

    for profile_dir in profile_dirs:
        preferences_path = profile_dir / "Preferences"
        if preferences_path.exists():
            _patch_json_file(preferences_path, _apply_preferences_patch)


def _patch_json_file(path: Path, patcher) -> None:
    data = json.loads(path.read_text(encoding="utf-8"))
    if not patcher(data):
        return

    path.write_text(
        json.dumps(data, ensure_ascii=False, separators=(",", ":")),
        encoding="utf-8",
    )


def _apply_local_state_patch(data: dict) -> bool:
    changed = False

    was = data.setdefault("was", {})
    if was.get("restarted") is not False:
        was["restarted"] = False
        changed = True

    return changed


def _apply_preferences_patch(data: dict) -> bool:
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
