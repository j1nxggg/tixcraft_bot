"""穩定的 nodriver CDP 操作原語。

所有跟 nodriver Tab / Browser 打交道、容易撞到 CDP 怪毛病的邏輯集中在這邊,
外面的業務 flow(area_flow / ticket_flow / schedule / login_flow ...)只呼叫
這裡提供的 async 函式,不用自己處理 timeout / retry / stale node 的例外。

設計原則:
    1. 所有 CDP 呼叫都包 asyncio.wait_for — nodriver Transaction 在 OAuth
       多次 redirect 或 navigate 瞬間偶爾不會 resolve,不加 timeout 會讓
       呼叫者永遠卡住。
    2. DOM 查詢統一處理 "Could not find node with given id"(-32000)—
       發生時 sleep 一下重試,不要 disable + raise。
    3. URL 判斷優先用 Tab.url(靠 CDP Target 事件獨立更新),不走
       Runtime.evaluate("location.href"),後者會被 (1) 的 bug 拖住。

所有函式都有 timeout,不會無限期卡住;回傳值要麼是有意義的資料,
要麼是 None / False / "" 的退讓值。
"""
from __future__ import annotations

import asyncio
import json
from typing import Any, Awaitable, Callable, Generator

from nodriver import cdp
from nodriver.core.connection import ProtocolException

from logger import log


# ─────────────────────────────────────────────────────────────────────────
# 預設 timeout(秒)— 多數情境夠用,呼叫者若有特殊需求可覆寫
# ─────────────────────────────────────────────────────────────────────────
EVAL_TIMEOUT = 3.0        # 單次 Runtime.evaluate 等 response 的上限
QUERY_TIMEOUT = 3.0       # 單次 DOM.querySelector 等 response 的上限
SEND_TIMEOUT = 3.0        # 單次 page.send / element CDP roundtrip 的上限
URL_POLL_INTERVAL = 0.2   # URL 輪詢間隔
DOM_RETRY_INTERVAL = 0.1  # stale node 後重試間隔


# ─────────────────────────────────────────────────────────────────────────
# URL / 導航
# ─────────────────────────────────────────────────────────────────────────
async def current_url(tab) -> str:
    """取 Tab 當前 URL,優先 Runtime.evaluate,逾時 / 失敗 fallback 讀 Tab.url。

    Runtime.evaluate 在頁面過渡瞬間容易拿不到 response;Tab.url 是 CDP
    Target.TargetInfoChanged 事件獨立更新的,比較可靠。兩者一起用保險。
    """
    try:
        result = await asyncio.wait_for(
            tab.evaluate("location.href", return_by_value=True),
            timeout=1.0,
        )
        if isinstance(result, str) and result:
            return result
    except Exception:
        pass
    return (getattr(tab, "url", "") or "")


async def wait_url(
    tab,
    predicate: Callable[[str], bool],
    timeout: float,
    poll_interval: float = URL_POLL_INTERVAL,
    on_change: Callable[[str], Awaitable[None]] | None = None,
) -> bool:
    """輪詢 tab URL 直到 predicate 回 True 或逾時。

    on_change:URL 字面變動時的 callback(例如想印 log),可省略。
    """
    deadline = asyncio.get_event_loop().time() + timeout
    last_seen = ""

    while asyncio.get_event_loop().time() < deadline:
        url = await current_url(tab)
        if predicate(url):
            return True
        if on_change is not None and url != last_seen:
            try:
                await on_change(url)
            except Exception:
                pass
            last_seen = url
        await asyncio.sleep(poll_interval)

    return False


async def js_navigate(
    tab,
    target_url: str,
    evaluate_timeout: float = EVAL_TIMEOUT,
) -> None:
    """用 JS 觸發導航 — window.location.href = target_url。

    外層包 asyncio.wait_for,response 拿不到就當它 fire-and-forget;
    Chrome 端通常已經在跑 navigate,呼叫者之後用 wait_url 確認實際落點。
    """
    js = f"window.location.href = {json.dumps(target_url)};"
    try:
        await asyncio.wait_for(
            tab.evaluate(js, await_promise=False, return_by_value=True),
            timeout=evaluate_timeout,
        )
    except asyncio.TimeoutError:
        log(
            f"js_navigate({target_url}) response 逾時 {evaluate_timeout:.1f}s,"
            "改靠 URL polling 確認(Chrome 端通常已在跑 navigate)"
        )
    except Exception as exc:
        log(f"js_navigate({target_url}) 失敗:{exc!r}")


async def navigate_and_wait(
    tab,
    target_url: str,
    url_predicate: Callable[[str], bool],
    url_wait_timeout: float = 10.0,
) -> bool:
    """JS 導航 + 等 URL 落到滿足 predicate 的狀態。

    這是業務層最常用的「叫 tab 到某頁」組合動作。
    """
    await js_navigate(tab, target_url)
    return await wait_url(tab, url_predicate, timeout=url_wait_timeout)


# ─────────────────────────────────────────────────────────────────────────
# JS evaluate(帶 timeout 的安全版)
# ─────────────────────────────────────────────────────────────────────────
async def robust_evaluate(
    tab,
    expression: str,
    *,
    await_promise: bool = False,
    return_by_value: bool = True,
    timeout: float = EVAL_TIMEOUT,
) -> Any:
    """包 timeout 的 evaluate。

    逾時或例外都回 None,不會向外 raise — 呼叫者自己判斷 None 要怎麼處理。
    需要明確知道失敗原因的地方,不要用這個,直接用 tab.evaluate + 自己 try/except。
    """
    try:
        return await asyncio.wait_for(
            tab.evaluate(
                expression,
                await_promise=await_promise,
                return_by_value=return_by_value,
            ),
            timeout=timeout,
        )
    except asyncio.TimeoutError:
        log(f"robust_evaluate 逾時 {timeout:.1f}s: {expression[:60]}")
        return None
    except Exception as exc:
        log(f"robust_evaluate 失敗:{exc!r}")
        return None


async def robust_send(
    target,
    cdp_obj: Generator,
    *,
    timeout: float = SEND_TIMEOUT,
    log_prefix: str = "robust_send",
) -> Any:
    """包 timeout 的 target.send(cdp command)。

    nodriver 的 Connection.send() 會等待 Transaction future;若 listener / mapper
    race 導致 future 沒被 resolve,原生 await 會永遠卡住。這裡統一加上上限,
    讓業務流程能記錄失敗並繼續做自己的判斷。
    """
    try:
        return await asyncio.wait_for(target.send(cdp_obj), timeout=timeout)
    except asyncio.TimeoutError:
        log(f"{log_prefix} 逾時 {timeout:.1f}s")
        return None
    except Exception as exc:
        log(f"{log_prefix} 失敗:{exc!r}")
        return None


async def robust_bring_to_front(tab, *, timeout: float = SEND_TIMEOUT) -> None:
    """把 tab/window 帶到前景，避免 Tab.bring_to_front() 的 send 卡住。"""
    target_id = getattr(tab, "target_id", None)
    if not target_id:
        return
    await robust_send(
        tab,
        cdp.target.activate_target(target_id),
        timeout=timeout,
        log_prefix="bring_to_front",
    )


async def wait_ready_state(
    tab,
    state: str = "complete",
    timeout: float = 15.0,
    poll_interval: float = 0.2,
) -> bool:
    """等 document.readyState 到達指定狀態("interactive" / "complete")。

    常見用法:rush 到新頁面後先等 DOM 穩定再去 query_selector,
    可以大幅降低撞到 stale node 的機率。
    """
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        result = await robust_evaluate(tab, "document.readyState", timeout=1.0)
        if isinstance(result, str) and result == state:
            return True
        await asyncio.sleep(poll_interval)
    return False


# ─────────────────────────────────────────────────────────────────────────
# DOM 查詢(自動處理 stale node_id 的版本)
# ─────────────────────────────────────────────────────────────────────────
def _is_stale_node_error(exc: BaseException) -> bool:
    if not isinstance(exc, ProtocolException):
        return False
    # nodriver ProtocolException 的 message 在 __str__ / .message 都拿得到
    msg = str(exc).lower()
    return "could not find node" in msg


async def robust_select(
    tab,
    selector: str,
    *,
    timeout: float = 10.0,
    query_timeout: float = QUERY_TIMEOUT,
    retry_interval: float = DOM_RETRY_INTERVAL,
):
    """page.select 的穩定版。

    在 timeout 內重複嘗試 tab.query_selector,遇到以下情況會 sleep 後重試:
        * 元素還沒出現(query 回 None)
        * CDP "Could not find node with given id"(頁面切換瞬間 doc.node_id 失效)
        * 單次 query 逾時(CDP transaction 卡住,下一次重試通常就通)
        * 其它暫時性例外

    回傳找到的 Element,或 None(代表 timeout 內都沒找到)。
    """
    deadline = asyncio.get_event_loop().time() + timeout

    while asyncio.get_event_loop().time() < deadline:
        try:
            element = await asyncio.wait_for(
                tab.query_selector(selector),
                timeout=query_timeout,
            )
        except asyncio.TimeoutError:
            await asyncio.sleep(retry_interval)
            continue
        except ProtocolException as exc:
            if _is_stale_node_error(exc):
                await asyncio.sleep(retry_interval)
                continue
            raise
        except Exception:
            await asyncio.sleep(retry_interval)
            continue

        if element:
            return element
        await asyncio.sleep(retry_interval)

    return None


async def robust_select_all(
    tab,
    selector: str,
    *,
    timeout: float = 10.0,
    query_timeout: float = QUERY_TIMEOUT,
    retry_interval: float = DOM_RETRY_INTERVAL,
) -> list:
    """query_selector_all 的穩定版。

    跟 robust_select 同邏輯,只是抓全部 element。回傳空 list 代表 timeout
    內都沒找到任何元素(跟 nodriver 原生行為一致)。
    """
    deadline = asyncio.get_event_loop().time() + timeout

    while asyncio.get_event_loop().time() < deadline:
        try:
            elements = await asyncio.wait_for(
                tab.query_selector_all(selector),
                timeout=query_timeout,
            )
        except asyncio.TimeoutError:
            await asyncio.sleep(retry_interval)
            continue
        except ProtocolException as exc:
            if _is_stale_node_error(exc):
                await asyncio.sleep(retry_interval)
                continue
            raise
        except Exception:
            await asyncio.sleep(retry_interval)
            continue

        if elements:
            return list(elements)
        await asyncio.sleep(retry_interval)

    return []


async def robust_child_select(
    parent,
    selector: str,
    *,
    retries: int = 3,
    retry_interval: float = DOM_RETRY_INTERVAL,
):
    """在既有的 parent Element 下做 query_selector,遇到 stale node 重試。

    nodriver 裡 Element.query_selector 撞到 "could not find node" 時,
    內部其實會先 call _node.update() 再試一次(看 tab.py:574),這個函式再
    包一層外圈重試,應付 update 後又立刻 stale 的連環 race。
    """
    for attempt in range(retries + 1):
        try:
            element = await asyncio.wait_for(
                parent.query_selector(selector),
                timeout=QUERY_TIMEOUT,
            )
        except asyncio.TimeoutError:
            if attempt < retries:
                await asyncio.sleep(retry_interval)
                continue
            return None
        except ProtocolException as exc:
            if _is_stale_node_error(exc) and attempt < retries:
                await asyncio.sleep(retry_interval)
                continue
            raise
        return element
    return None


async def robust_child_select_all(
    parent,
    selector: str,
    *,
    retries: int = 3,
    retry_interval: float = DOM_RETRY_INTERVAL,
) -> list:
    """同 robust_child_select,回傳 list。"""
    for attempt in range(retries + 1):
        try:
            elements = await asyncio.wait_for(
                parent.query_selector_all(selector),
                timeout=QUERY_TIMEOUT,
            )
        except asyncio.TimeoutError:
            if attempt < retries:
                await asyncio.sleep(retry_interval)
                continue
            return []
        except ProtocolException as exc:
            if _is_stale_node_error(exc) and attempt < retries:
                await asyncio.sleep(retry_interval)
                continue
            raise
        return list(elements) if elements else []
    return []


# ─────────────────────────────────────────────────────────────────────────
# 元素屬性 / 點擊(對 stale node 加保險)
# ─────────────────────────────────────────────────────────────────────────
async def robust_get_attr(element, name: str, default: str = "") -> str:
    """讀 element attribute,遇到 stale node 先 update() 再試。"""
    try:
        await asyncio.wait_for(element.update(), timeout=QUERY_TIMEOUT)
    except Exception:
        pass
    value = getattr(element.attrs, name, None)
    if value is None:
        return default
    return str(value)


async def robust_click(element, *, retries: int = 3, retry_interval: float = 0.1) -> bool:
    """element.click() 撞到 stale node 時重試。回傳 True 代表成功。"""
    for attempt in range(retries + 1):
        try:
            await asyncio.wait_for(element.click(), timeout=SEND_TIMEOUT)
            return True
        except asyncio.TimeoutError:
            if attempt < retries:
                await asyncio.sleep(retry_interval)
                continue
            log(f"robust_click 逾時 {SEND_TIMEOUT:.1f}s")
            return False
        except ProtocolException as exc:
            if _is_stale_node_error(exc) and attempt < retries:
                await asyncio.sleep(retry_interval)
                try:
                    await asyncio.wait_for(element.update(), timeout=QUERY_TIMEOUT)
                except Exception:
                    pass
                continue
            log(f"robust_click 失敗:{exc!r}")
            return False
        except Exception as exc:
            log(f"robust_click 失敗:{exc!r}")
            return False
    return False


async def robust_send_keys(
    element,
    text: str,
    *,
    timeout: float = SEND_TIMEOUT,
) -> bool:
    """element.send_keys() 的 timeout 版。"""
    try:
        await asyncio.wait_for(element.send_keys(text), timeout=timeout)
        return True
    except asyncio.TimeoutError:
        log(f"robust_send_keys 逾時 {timeout:.1f}s")
        return False
    except Exception as exc:
        log(f"robust_send_keys 失敗:{exc!r}")
        return False


async def robust_select_option(
    option,
    *,
    timeout: float = SEND_TIMEOUT,
) -> bool:
    """option.select_option() 的 timeout 版。"""
    try:
        await asyncio.wait_for(option.select_option(), timeout=timeout)
        return True
    except asyncio.TimeoutError:
        log(f"robust_select_option 逾時 {timeout:.1f}s")
        return False
    except Exception as exc:
        log(f"robust_select_option 失敗:{exc!r}")
        return False
