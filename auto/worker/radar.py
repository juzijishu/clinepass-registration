import re
import time

from errors import WorkerError
from herosms import Client
from pageutil import click_one_of, field_ready, input_value, page_url, sleep_ms, visible, wait_any_url
from protocol import log
from urls import on_cline, on_radar_url, radar_send_url

# 人工等待时检查页面是否完成的轮询间隔。
MANUAL_POLL_MS = 500


def digits_only(s: str) -> str:
    return "".join(ch for ch in (s or "") if ch.isdigit())


def authkit_input_match(page, selector: str, want: str) -> bool:
    return digits_only(input_value(page, selector)) == digits_only(want)


def type_authkit_field(page, selector: str, value: str) -> None:
    loc = page.locator(selector).first
    try:
        loc.fill(value, timeout=12000)
        if authkit_input_match(page, selector, value):
            return
    except Exception:
        pass
    loc.click(timeout=8000)
    # macOS 的全选是 Meta+a；Control+a 只会移到行首导致旧值残留（如 +4444）
    page.keyboard.press("Meta+a")
    page.keyboard.press("Control+a")
    page.keyboard.press("Backspace")
    loc.type(value, delay=18, timeout=12000)
    if not authkit_input_match(page, selector, value):
        # AuthKit 受控输入可能自动补回前缀：再以差量补正（期望 +44、已存 44 等）
        current = input_value(page, selector)
        if current.strip() and value.startswith(current.strip()) and current.strip() != value:
            loc.type(value[len(current.strip()):], delay=18, timeout=12000)


def fill_authkit_field(page, selector: str, value: str) -> None:
    from pageutil import wait_visible

    wait_visible(page, selector, 20000, stop_if_done=False)
    # AuthKit 国家码字段会记住上一次选择并自动补前缀：换号重试后页面常已
    # 显示 +44，再整值填入会拼成 +4444。先按「数字等价或前缀已存在」跳过，
    # 有残留差量时只补差量。
    current = input_value(page, selector).strip()
    if current and digits_only(current) == digits_only(value):
        return
    if current.startswith("+") and value.startswith(current) and len(value) > len(current):
        value = value[len(current):]
    type_authkit_field(page, selector, value)
    # 残留值（如 +4444）说明受控字段拼上了旧前缀：彻底清空后重填
    if not authkit_input_match(page, selector, value):
        loc = page.locator(selector).first
        loc.click(timeout=8000)
        page.keyboard.press("Meta+a")
        page.keyboard.press("Backspace")
        sleep_ms(120)
        if input_value(page, selector).strip():
            # 逐字退格兜底（Meta+a 可能被组件拦截）
            for _ in range(len(input_value(page, selector)) + 2):
                page.keyboard.press("Backspace")
                sleep_ms(40)
        loc.type(value, delay=18, timeout=12000)
        sleep_ms(120)
        if not authkit_input_match(page, selector, value):
            raise RuntimeError(f'页面是 "{input_value(page, selector)}"，期望 "{value}"')
    if authkit_input_match(page, selector, value):
        return
    typed = value
    current = input_value(page, selector)
    if current.strip().startswith("+") and value.startswith("+"):
        typed = value[1:]
    type_authkit_field(page, selector, typed)
    if not authkit_input_match(page, selector, value):
        raise RuntimeError(f'页面是 "{input_value(page, selector)}"，期望 "{value}"')


def radar_form_ready(page) -> bool:
    return field_ready(page, 'input[name="country_code"]') and field_ready(page, 'input[name="local_number"]')


def wait_radar_form(page, timeout_ms: float = 15000) -> None:
    deadline = time.time() + timeout_ms / 1000.0
    while time.time() < deadline:
        if radar_form_ready(page):
            return
        sleep_ms(200)
    raise RuntimeError(f"接码页没有可填的手机号输入框，当前 URL={page.url}")


def fill_otp(page, code: str) -> None:
    code = digits_only(code)
    if len(code) < 4:
        raise RuntimeError("验证码无效")
    code = code[:6]
    log("填写验证码")
    group = 'input[data-test="otp-input"], input[data-index], .ak-Otp input, input[name="code"], input[autocomplete="one-time-code"]'
    cells = page.locator(group)
    total = cells.count()
    if total > 1:
        # 分格输入：显式从第一格开始逐格填入（旧实现点击 first 可能落在
        # 任意一格，导致错位只填了后几位）。
        target = min(total, len(code))
        for i in range(target):
            cell = cells.nth(i)
            cell.click(timeout=8000)
            page.keyboard.press("Meta+a")
            page.keyboard.press("Backspace")
            page.keyboard.type(code[i], delay=30)
            sleep_ms(90)
        sleep_ms(150)
        # 校验：所有格子数字拼起来应等于验证码
        filled = "".join(
            (cells.nth(i).input_value() or "").strip()
            for i in range(total)
        ).replace(" ", "")
        if digits_only(filled) != digits_only(code):
            raise RuntimeError(f"验证码填入不完整：页面为 {filled!r}，期望 {code!r}")
    else:
        loc = page.locator(group).first
        loc.click(timeout=12000)
        page.keyboard.press("Meta+a")
        page.keyboard.press("Backspace")
        page.keyboard.type(code, delay=18)
        sleep_ms(120)
    try:
        click_one_of(
            page,
            ['button[data-hak-cta][type="submit"]', 'button.ak-PrimaryButton[type="submit"]', 'button[type="submit"]'],
            4000,
            "提交验证码",
        )
    except Exception:
        pass


def on_radar_send(u: str) -> bool:
    return "radar-challenge/send" in u


def goto_radar_send(page, send_url: str) -> None:
    if on_radar_send(page.url) and visible(page, 'input[name="local_number"]'):
        return
    target = (send_url or "").strip()
    if not target or "radar-challenge/verify" in target:
        target = radar_send_url(page.url)
    if target:
        log("打开手机号输入页")
        try:
            page.goto(target, wait_until="domcontentloaded", timeout=30000)
        except Exception as exc:
            log("打开手机号页失败: %s，尝试后退", exc)
            try:
                page.go_back()
            except Exception:
                pass
    else:
        try:
            page.go_back()
        except Exception:
            pass
    sleep_ms(300)
    if on_radar_send(page.url) or visible(page, 'input[name="local_number"]'):
        return
    raise RuntimeError(f"无法回到手机号输入页，当前 URL={page.url}")


def request_radar_code(page, settings: dict, sms: Client, attempt: int) -> None:
    wait_radar_form(page)
    country = int(settings.get("hero_sms_country") or 0)
    price = float(settings.get("hero_sms_max_price") or 0)
    # HERO_SMS_REUSE_ID: 复用仍在租期内的激活（同一号码可再收码，不新增扣费）。
    # 号码配额受限（Cline 每账号最多绑 3 个手机号）时用这个口子续用现有号码。
    import os
    reuse_id = (settings.get("hero_sms_reuse_id") or os.environ.get("HERO_SMS_REUSE_ID") or "").strip()
    if reuse_id:
        log("复用 Hero SMS 激活 id=%s（不新取号）", reuse_id)
        status = sms._request("getStatus", {"id": reuse_id}).decode("utf-8", "replace").strip()
        # 复用路径同样要先填手机号并点「发送验证码」——HeroSMS 会向同一号码
        # 发出新码（getStatus 始终返回最新一条）。旧码（如手动测试用过的）作废。
        phone_re = re.search(r"STATUS_OK:(\d+)", status)
        if phone_re:
            code = digits_only(phone_re.group(1))
            log("激活有待用验证码（可能为旧码），先填手机号再请求新码")
        else:
            log("激活状态: %s", status[:60])
        # 从激活记录取回号码（getActiveActivations 拿不到时用 77 前缀的英国手机号）
        # 这里用固定复用号码：7716327581
        local_number = os.environ.get("HERO_SMS_REUSE_LOCAL", "7716327581")
        num = {"id": reuse_id, "phone_code": 44, "local_number": local_number, "phone": "+44" + local_number}
        fill_authkit_field(page, 'input[name="country_code"]', "+44")
        sleep_ms(150)
        fill_authkit_field(page, 'input[name="local_number"]', local_number)
        sleep_ms(200)
        click_one_of(
            page,
            ['button[data-hak-cta][type="submit"]', 'button.ak-PrimaryButton[type="submit"]', 'button[type="submit"]'],
            15000,
            "发送验证码（复用号码）",
        )
        try:
            wait_any_url(page, ["radar-challenge/verify"], 30000)
        except Exception as exc:
            if on_radar_send(page.url) or visible(page, 'input[name="local_number"]'):
                raise RuntimeError("复用号码发送验证码失败") from exc
            raise
        sleep_ms(200)
        log("等待复用号码的新短信验证码")
        new_code = sms.wait_code(reuse_id, _clamp(settings.get("sms_wait_seconds"), 120, 30, 600))
        log("收到新验证码: %s", new_code)
        finished = False
        try:
            fill_otp(page, new_code)
            deadline = time.time() + 45
            last_resubmit = time.time()
            while time.time() < deadline:
                if "radar-challenge" not in page.url:
                    sms.finish(reuse_id)
                    finished = True
                    log("已离开接码页（复用激活），URL=%s", page.url)
                    return
                if time.time() - last_resubmit > 8:
                    last_resubmit = time.time()
                    try:
                        click_one_of(page, ['button[data-hak-cta][type="submit"]', 'button.ak-PrimaryButton[type="submit"]', 'button[type="submit"]'], 3000, "重提验证码")
                    except Exception:
                        pass
                sleep_ms(300)
            raise RuntimeError("复用验证码提交后仍停在验证页")
        finally:
            pass
    log("Hero SMS 取号 country=%d price=%s（第 %d 次）", country, ("%g" % price) if price > 0 else "auto", attempt)
    num = sms.get_number(country, price)
    finished = False
    try:
        log("已取号 +%s %s id=%s", num["phone_code"], num["local_number"], num["id"])
        sleep_ms(200)
        if int(num.get("phone_code") or 0) <= 0 or not num.get("local_number"):
            raise RuntimeError(f'取到的号码无法拆分区号: {num.get("phone")}')
        cc = f'+{num["phone_code"]}'
        fill_authkit_field(page, 'input[name="country_code"]', cc)
        sleep_ms(150)
        fill_authkit_field(page, 'input[name="local_number"]', num["local_number"])
        sleep_ms(200)
        click_one_of(
            page,
            ['button[data-hak-cta][type="submit"]', 'button.ak-PrimaryButton[type="submit"]', 'button[type="submit"]'],
            15000,
            "发送验证码",
        )
        try:
            wait_any_url(page, ["radar-challenge/verify"], 30000)
        except Exception as exc:
            if on_radar_send(page.url) or visible(page, 'input[name="local_number"]'):
                raise RuntimeError("发送验证码失败，号码可能已被使用") from exc
            raise
        sleep_ms(200)
        log("等待短信验证码")
        code = sms.wait_code(num["id"], _clamp(settings.get("sms_wait_seconds"), 120, 30, 600))
        # 短信码有有效期且号码配额有限（Cline 每账号最多绑定 3 个手机号）：
        # 提交后若未通过，优先点页面上的「请求新的验证码」同号重发，其次重填，
        # 最后才把失败抛给上层换号。
        for round_index in range(3):
            log("收到验证码（第 %d 次）", round_index + 1)
            sleep_ms(120)
            fill_otp(page, code)
            log("验证码已填，等待离开验证页")
            deadline = time.time() + 45
            last_resubmit = time.time()
            left = False
            while time.time() < deadline:
                u = page.url
                if "radar-challenge" not in u:
                    sms.finish(num["id"])
                    finished = True
                    log("已离开接码页，当前 URL=%s", u)
                    return
                if time.time() - last_resubmit > 8:
                    last_resubmit = time.time()
                    # 提交按钮可能未触发：再次点击
                    try:
                        click_one_of(
                            page,
                            ['button[data-hak-cta][type="submit"]', 'button.ak-PrimaryButton[type="submit"]', 'button[type="submit"]'],
                            3000,
                            "重提验证码",
                        )
                    except Exception:
                        pass
                sleep_ms(300)
            if round_index == 2:
                break
            # 同号重发：点「请求新的验证码」，等待新短信
            log("验证码未通过，请求同号新码（不换号）")
            try:
                click_one_of(page, ['a:has-text("请求新的验证码")', 'a:has-text("Resend")', 'button:has-text("请求新的验证码")'], 6000, "请求新的验证码")
            except Exception as exc:
                raise RuntimeError("提交验证码后仍停在验证页，且无法请求新码") from exc
            code = sms.wait_code(num["id"], _clamp(settings.get("sms_wait_seconds"), 120, 30, 600))
        raise RuntimeError(f"提交验证码后仍停在验证页，当前 URL={page.url}")
    finally:
        if not finished:
            sms.cancel(num["id"])


def is_no_sms(exc: Exception) -> bool:
    msg = str(exc)
    return isinstance(exc, TimeoutError) or "等待验证码超时" in msg or "接码已取消" in msg or "Hero SMS 暂时不可用" in msg


def should_retry_phone(exc: Exception) -> bool:
    if is_no_sms(exc):
        return True
    msg = str(exc)
    if "号码可能已被使用" in msg:
        return True
    return "等待 URL 超时" in msg and "radar-challenge/send" in msg


def manual_wait_seconds(settings: dict) -> int:
    return _clamp(settings.get("manual_wait_seconds"), 1800, 60, 7200)


def page_closed(page) -> bool:
    try:
        is_closed = getattr(page, "is_closed", None)
        return bool(is_closed()) if callable(is_closed) else False
    except Exception:
        return True


def left_radar_url(raw: str) -> bool:
    """页面离开接码地址（人工完成验证、被拒绝或跳回登录页）。

    空白页与浏览器错误页不算离开：它们只是切换过程中的瞬时状态。
    """
    return (
        bool(raw)
        and raw != "about:blank"
        and not raw.startswith("chrome-error")
        and not raw.startswith("chrome://")
        and "radar-challenge" not in raw
    )


def cline_reached_elsewhere(context, page) -> bool:
    """人工验证可能在另一个标签页完成：任一标签页进入 Cline 即视为完成。"""
    if context is None:
        return False
    try:
        pages = list(getattr(context, "pages", None) or [])
    except Exception:
        return False
    for candidate in pages:
        if candidate is page:
            continue
        if on_cline(page_url(candidate)):
            return True
    return False


def wait_manual_verification(page, settings: dict, context=None) -> None:
    """停在号码页交由人工操作：浏览器、Cookie 与分阶段网络全部保持原样。

    只等待页面离开接码地址（人工完成验证或被拒绝），期间不点击、不提交、
    不取号。超时或人工关闭浏览器时抛错，由上层走正常失败收尾。
    """
    seconds = manual_wait_seconds(settings)
    log("已到号码页，交由人工完成验证（最多等待 %d 秒）；浏览器与分阶段网络保持不动", seconds)
    deadline = time.monotonic() + seconds
    last_note = time.monotonic()
    closed_polls = 0
    while True:
        sleep_ms(MANUAL_POLL_MS)
        if page_closed(page):
            raise WorkerError("浏览器已被关闭，无法确认验证结果；请重新排队", "manual_timeout")
        raw = page_url(page)
        if raw == "":
            # 页面切换瞬间可能取不到地址；连续取不到才判定为已关闭。
            closed_polls += 1
            if closed_polls >= 10:
                raise WorkerError("浏览器页面不可用，无法确认验证结果；请重新排队", "manual_timeout")
        else:
            closed_polls = 0
            if left_radar_url(raw):
                log("人工验证已完成，页面已离开接码地址")
                return
            if cline_reached_elsewhere(context, page):
                log("人工验证已在其他标签页完成，继续后续步骤")
                return
        now = time.monotonic()
        if now >= deadline:
            raise WorkerError(
                "人工接码等待超时，未检测到验证完成；请重新排队并检查浏览器窗口",
                "manual_timeout",
            )
        if now - last_note >= 60:
            last_note = now
            log("仍在等待人工完成验证，剩余约 %d 秒", int(deadline - now))


def handle_radar(page, settings: dict, context=None) -> None:
    if not on_radar_url(page.url):
        return
    if settings.get("manual_sms"):
        # 人工接码不调用 HeroSMS：没有密钥、国家和限价要求；
        # 把页面留在手机号输入页，由人工填写号码与验证码。
        log("人工接码模式：跳过 HeroSMS 取号与预算检查")
        send_url = page.url
        if "radar-challenge/verify" in send_url:
            send_url = radar_send_url(send_url)
        goto_radar_send(page, send_url)
        wait_radar_form(page)
        wait_manual_verification(page, settings, context)
        return
    if not (settings.get("hero_sms_api_key") or "").strip():
        raise RuntimeError("遇到手机验证，请先在设置里填写 Hero SMS API Key")
    if int(settings.get("hero_sms_country") or 0) != 16:
        raise RuntimeError("接码区域必须为英格兰")
    wait_radar_form(page)
    send_url = page.url
    if "radar-challenge/verify" in send_url:
        send_url = radar_send_url(send_url)
    goto_radar_send(page, send_url)
    send_url = page.url
    proxy = (settings.get("hero_sms_proxy") or "").strip()
    if not proxy:
        raise RuntimeError("HeroSMS 必须配置独立代理，已停止取号")
    budget = _budget(settings)
    sms = Client(settings.get("hero_sms_api_key") or "", settings.get("hero_sms_service") or "", proxy, budget)
    last = None
    attempts = _clamp(settings.get("sms_max_attempts"), 3, 1, 20)
    for i in range(1, attempts + 1):
        try:
            request_radar_code(page, settings, sms, i)
            return
        except Exception as exc:
            last = exc
            if not should_retry_phone(exc):
                raise
            log("第 %d/%d 次接码失败: %s", i, attempts, exc)
            if i < attempts:
                log("返回手机号输入页，换一个 Hero SMS 号码重试")
                sleep_ms(300)
                goto_radar_send(page, send_url)
    raise WorkerError("多次接码失败，需要重新登录", "sms_relogin") from last


def _clamp(value, fallback: int, low: int, high: int) -> int:
    try:
        n = int(value)
    except (TypeError, ValueError):
        return fallback
    if n <= 0:
        return fallback
    return max(low, min(high, n))


def _budget(settings: dict) -> float:
    """账号与批次剩余额度取小；都为 0 表示不限制。"""
    limits = [float(settings.get(key) or 0) for key in ("sms_account_remaining", "sms_batch_remaining")]
    limits = [v for v in limits if v > 0]
    return min(limits) if limits else 0.0


def handle_terms(page) -> None:
    if not visible(page, "label#terms, #terms"):
        return
    log("勾选条款")
    click_one_of(page, ["label#terms", "#terms"], 8000, "勾选条款")
    sleep_ms(400)
    click_one_of(
        page,
        ['button[type="button"].w-full', "button.bg-black[type=button]", 'button[type="button"]'],
        15000,
        "确认注册",
    )
