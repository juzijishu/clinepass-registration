import time
from urllib.parse import urlsplit

from auxmail import fetch_aux_code
from authkit import MICROSOFT_AUTH, raise_callback
from errors import WorkerError
from pageutil import (
    click_one_of,
    human_click,
    first_ready_field,
    input_value,
    logged_in,
    sleep_ms,
    type_field,
    visible,
    wait_any_url,
)
from protocol import log
from urls import AUTH_HOST, authkit_callback_error, microsoft_step, on_cline, on_microsoft_url, url_host

EMAIL_SELS = ['input[name="loginfmt"]', "input#i0116"]
PASS_SELS = ["input#passwordEntry", 'input[name="passwd"]', "input#i0118"]
NEXT_SELS = ['button[data-testid="primaryButton"]', "input#idSIButton9", 'input[type="submit"]']
# 供货商交付账号首登常触发「验证你的电子邮件」挑战。验证码发到辅助邮箱
# （六列第 5 列地址 + 第 6 列密码，IMAP 收取）；部分页面提供「使用密码」
# 入口，作为没有辅助邮箱凭据时的兜底。
USE_PASSWORD_SELS = ["a#usePassword", "#usePassword"]
USE_PASSWORD_TEXT_SELS = ('a:has-text("使用密码")', 'a:has-text("Use password")')
VERIFY_EMAIL_FIELD_SELS = ["#proof-confirmation-email-input"] + EMAIL_SELS + ['input[type="email"]', 'input[name="email"]']
VERIFY_EMAIL_NEXT_SELS = NEXT_SELS + [
    'button[type="submit"]',
    'button:has-text("发送验证代码")',
    'button:has-text("发送代码")',
    'button:has-text("Send code")',
    'button:has-text("验证")',
    'button:has-text("Verify")',
    'button:has-text("下一步")',
    'button:has-text("Next")',
    'button:has-text("继续")',
    'button:has-text("Continue")',
]
OTC_SELS = ('input[name="otc"]', 'input[name="otcInput"]', "input#iOttText")
# 新版 Fluent UI 的分格验证码：每格 id=codeEntry-N、autocomplete=one-time-code；
# 第一格 maxlength=6，其余格才是 maxlength=1，所以必须用 autocomplete/id 定位整组。
OTP_CELL_SELS = ('input[autocomplete="one-time-code"]', 'input[id^="codeEntry"]', 'input[maxlength="1"]')
VERIFY_EMAIL_PHRASES = ("verify your email", "验证你的电子邮件", "验证您的电子邮件")
AWAIT_CODE_TIMEOUT = 300.0
CARD_SETTLE = 1.0
NO_PROGRESS_TIMEOUT = 35.0
LOGIN_TIMEOUT = 120.0
CHALLENGE_SELS = (
    'input[name="otc"]', 'input[name="otcInput"]', "input#iOttText",
    'input[name="ProofConfirmation"]', "#idDiv_SAOTCC_Description",
    "#idRichContext_DisplaySign", 'iframe[src*="captcha"]', "#wlspispHIPSolutionContainer",
)


def safe_page_location(raw: str) -> str:
    """Keep OAuth state, authorization codes and login hints out of messages."""
    try:
        parsed = urlsplit(raw)
        return (parsed.hostname or "") + (parsed.path or "/")
    except ValueError:
        return "未知页面"


def microsoft_problem(text: str, challenge_visible: bool = False) -> tuple[str, str]:
    text = " ".join(text.lower().replace("’", "'").split())
    groups = (
        (("too many requests", "请求过多", "请求太频繁"),
         "微软对当前出口 IP 限流（Too Many Requests），请更换代理节点后重试", "microsoft_ip_limited"),
        (("account has been locked", "account is locked", "account has been suspended", "too many times", "账号已锁定", "帐户已锁定", "账户已锁定", "尝试次数过多", "尝试登录次数过多", "暂时阻止"),
         "微软账号已锁定或尝试过于频繁，请先人工检查账号", "microsoft_locked"),
        (("account doesn't exist", "account does not exist", "couldn't find an account", "could not find an account", "该 microsoft 帐户不存在", "该 microsoft 账户不存在", "找不到该帐户", "账号不存在", "帐户不存在", "账户不存在"),
         "微软账号不存在，请检查邮箱地址", "microsoft_account_missing"),
        (("account or password is incorrect", "password is incorrect", "password isn't correct", "incorrect password", "密码不正确", "密码错误", "密码有误"),
         "微软账号或密码错误，请检查凭据后重试", "microsoft_credentials"),
        (("verify your identity", "help us protect your account", "approve sign in request", "approve the sign in", "enter the code we sent", "enter your security code", "help us beat the robots", "prove you're not a robot", "update your password", "change your password", "more information required", "验证你的身份", "验证您的身份", "验证你的标识", "帮助我们保护", "批准登录请求", "输入我们发送", "输入安全代码", "添加安全信息", "提供更多信息", "证明你不是机器人", "证明您不是机器人", "更新你的密码", "更改密码"),
         "微软要求验证码、身份验证或安全信息，请先人工完成验证后重试", "microsoft_manual_required"),
    )
    for phrases, message, code in groups:
        if any(phrase in text for phrase in phrases):
            return message, code
    if challenge_visible:
        return "微软要求验证码或身份验证，请先人工完成验证后重试", "microsoft_manual_required"
    return "", ""


def microsoft_confirmation(text: str) -> str:
    text = " ".join(text.lower().split())
    if any(s in text for s in ("stay signed in?", "保持登录状态", "是否保持登录")):
        return "stay_signed_in"
    if any(s in text for s in ("let this app access your info", "permissions requested", "允许此应用访问", "让此应用访问", "请求的权限")):
        return "consent"
    # 供货商交付账号首登验证通过后，微软常插入服务条款更新确认页
    if any(s in text for s in ("即将更新条款", "更新服务协议", "we're updating our terms", "updating the microsoft services agreement")):
        return "tou"
    # 码验证/条款之后微软常推广「添加通行密钥」，必须点取消而不是主按钮「下一步」
    if any(s in text for s in ("通行密钥", "passkey", "安全密钥", "windows hello", "指纹", "面容", "生物识别", "无密码", "设置密钥", "正在设置密钥", "安全窗口", "security key", "fido")):
        return "passkey"
    return ""


# Cline AuthKit 侧的访问拦截，与微软无关，需单独诊断
AUTHKIT_BLOCKED_PHRASES = ("访问被阻止", "access blocked", "contact support", "联系支持")


def authkit_blocked(text: str) -> bool:
    lowered = " ".join(text.lower().split())
    return any(phrase in lowered for phrase in AUTHKIT_BLOCKED_PHRASES)


def microsoft_page_text(page) -> str:
    try:
        # inner_text reads rendered text, not hidden error templates or script data.
        return page.locator("body").inner_text(timeout=1000)[:12000]
    except Exception:
        return ""


def fill_microsoft(page, selector: str, value: str, label: str) -> None:
    type_field(page, selector, value)
    if input_value(page, selector).strip() != value.strip():
        type_field(page, selector, value)
    if input_value(page, selector).strip() != value.strip():
        raise WorkerError(f"微软{label}未能填入输入框，请检查页面后重试", "microsoft_input")


def click_microsoft_once(page, selectors, label: str) -> None:
    """Wait for a button, but never resubmit after an ambiguous click timeout."""
    deadline = time.monotonic() + 8
    while time.monotonic() < deadline:
        if logged_in(page):
            return
        for selector in selectors:
            loc = page.locator(selector).first
            try:
                ready = loc.is_visible() and loc.is_enabled()
            except Exception:
                continue
            if not ready:
                continue
            try:
                if human_click(page, selector):
                    return
            except Exception:
                if logged_in(page):
                    return
                raise WorkerError(f"微软{label}提交未完成，请检查页面后重试", "microsoft_submit") from None
        sleep_ms(200)
    raise WorkerError(f"微软{label}按钮未就绪，请检查页面后重试", "microsoft_submit")


def verify_email_challenge(text: str) -> bool:
    lowered = " ".join(text.lower().replace("’", "'").split())
    return any(phrase in lowered for phrase in VERIFY_EMAIL_PHRASES)


def click_use_password(page) -> bool:
    """Switch a verify-email challenge to password sign-in; False when unavailable."""
    for selectors in (USE_PASSWORD_SELS, USE_PASSWORD_TEXT_SELS):
        for selector in selectors:
            try:
                if human_click(page, selector):
                    return True
            except Exception:
                continue
    return False


def _type_fluent_field(page, selector: str, value: str) -> None:
    """Fluent UI 受控输入框：逐字符输入并补派 change/blur，确保 React 表单状态同步。"""
    loc = page.locator(selector).first
    try:
        loc.fill("", timeout=8000)
    except Exception:
        pass
    try:
        loc.press_sequentially(value, delay=60, timeout=20000)
    except Exception:
        loc.fill(value, timeout=12000)
    try:
        page.evaluate(
            "(sel) => { const el = document.querySelector(sel);"
            " if (!el) return; el.dispatchEvent(new Event('input', {bubbles: true}));"
            " el.dispatchEvent(new Event('change', {bubbles: true})); el.blur(); }",
            selector,
        )
    except Exception:
        pass


def _click_primary_native(page) -> bool:
    """确认页主按钮：真实点击（可信输入）优先，表单原生提交次之，合成事件兜底。

    微软页面对 el.click() 之类不可信合成事件可能不响应——人工能点而脚本
    无效的原因就在这里；真实 Playwright 点击走浏览器输入管线，与人工一致。
    """
    for selector in ('button[data-testid="primaryButton"]', 'button[type="submit"]', "input#idSIButton9", 'input[type="submit"]'):
        try:
            if human_click(page, selector):
                return True
        except Exception:
            continue
    try:
        if page.evaluate(
            "() => { const f = document.querySelector('form');"
            " if (f && f.requestSubmit) { f.requestSubmit(); return true; } return false; }"
        ):
            return True
    except Exception:
        pass
    try:
        return bool(page.evaluate(
            "() => { const btns = [...document.querySelectorAll('button')]"
            ".filter(b => b.offsetWidth > 0 && !b.disabled && !/帮助|help/i.test(b.textContent || ''));"
            " if (!btns.length) return false;"
            " const primary = btns.find(b => /primary/i.test(b.className || '')"
            " || b.getAttribute('data-testid') === 'primaryButton');"
            " (primary || btns[0]).click(); return true; }"
        ))
    except Exception:
        return False


# 确认类页面的正面按钮按文字找：主按钮在某些授权页上是「取消/否」
CONFIRM_POSITIVE_SELS = {
    "consent": (
        'button:has-text("接受")', 'button:has-text("允许")', 'button:has-text("Yes")',
        'button:has-text("Accept")', 'button:has-text("Allow")', 'button:has-text("同意")',
    ),
}

# 「保持登录状态？」点「否」：用户实测点击否不会出现通行密钥弹窗，
# 且不持久化会话更符合采购账号的使用方式。
STAY_SIGNED_IN_NO_SELS = (
    'button:has-text("否")', 'button:has-text("No")',
    'button:has-text("不")', 'input[value="No"]',
)

PASSKEY_CANCEL_SELS = [
    'button:has-text("取消")', 'a:has-text("取消")',
    'button:has-text("跳过")', 'a:has-text("跳过")',
    'button:has-text("以后再说")', 'a:has-text("以后再说")',
    'button:has-text("暂时跳过")', 'a:has-text("暂时跳过")',
    'button:has-text("Skip for now")', 'a:has-text("Skip for now")',
    'button:has-text("Not now")', 'a:has-text("Not now")',
    'button:has-text("Cancel")', 'a:has-text("Cancel")',
    'button:has-text("No thanks")', 'a:has-text("No thanks")',
    'button:has-text("Skip")', 'a:has-text("Skip")',
]


def _passkey_page_text(text: str) -> bool:
    lowered = " ".join(text.lower().replace("’", "'").split())
    return any(phrase in lowered for phrase in (
        "通行密钥", "passkey", "安全密钥", "设置密钥", "正在设置密钥",
        "安全窗口", "windows hello", "security key",
    ))


def _click_passkey_cancel(page, timeout: float = 60.0) -> bool:
    """像真人一样取消通行密钥：轮询等待「取消」从禁用变为可用再点击。

    安全窗口请求挂起期间取消按钮是禁用的；真人会等系统弹窗出现/消失后
    再点。这里按 2 秒一轮轮询：可点就点，点不动就按 Escape 关掉系统
    安全窗口，直到页面离开密钥流程或超时。timeout=0 表示只试一轮。
    """
    deadline = time.monotonic() + timeout
    while True:
        for selector in PASSKEY_CANCEL_SELS:
            try:
                if human_click(page, selector):
                    return True
            except Exception:
                pass
        try:
            if page.evaluate(
                "() => { const els = [...document.querySelectorAll('a, button')]"
                ".filter(e => e.offsetWidth > 0 && !e.disabled"
                " && /取消|跳过|以后再说|skip|cancel|not now|no thanks/i.test(e.textContent || ''));"
                " if (!els.length) return false; els[0].click(); return true; }"
            ):
                return True
        except Exception:
            pass
        # 系统安全窗口可能压住页面：Escape 关闭后再等下一轮
        try:
            page.keyboard.press("Escape", timeout=2000)
        except Exception:
            pass
        sleep_ms(2000)
    return False


def submit_verify_email(page, address: str) -> bool:
    """Fill the verify-email challenge with the auxiliary address; False when no field."""
    selector = first_ready_field(page, VERIFY_EMAIL_FIELD_SELS)
    if not selector:
        try:
            snippets = page.evaluate(
                "() => [...document.querySelectorAll('input')].map(i => i.outerHTML.slice(0, 300)).slice(0, 10)"
            )
            with open("/tmp/verify_email_dom.txt", "w") as fh:
                fh.write("\n".join(snippets or []))
        except Exception:
            pass
        return False
    try:
        _type_fluent_field(page, selector, address)
        sleep_ms(800)
        if input_value(page, selector).strip().lower() != address.strip().lower():
            _type_fluent_field(page, selector, address)
            sleep_ms(800)
        if input_value(page, selector).strip().lower() != address.strip().lower():
            raise WorkerError("微软辅助邮箱地址未能填入输入框，请检查页面后重试", "microsoft_input")
        click_microsoft_once(page, VERIFY_EMAIL_NEXT_SELS, "辅助邮箱")
    except WorkerError:
        raise
    except Exception:
        return False
    return True


def _type_cell(loc, value: str) -> None:
    """单个 Fluent OTP 格子：键盘输入触发组件的按键处理与焦点前进。"""
    try:
        loc.click(timeout=6000)
    except Exception:
        pass
    try:
        loc.press_sequentially(value, delay=80, timeout=10000)
    except Exception:
        loc.fill(value, timeout=6000)


def submit_aux_code(page, address: str, password: str) -> None:
    """Wait for the code prompt, fetch the code from the auxiliary mailbox, submit it."""
    code = fetch_aux_code(address, password, timeout=240.0)
    if not code:
        raise WorkerError("未能从辅助邮箱收到微软验证码，请检查辅助邮箱后重试", "microsoft_manual_required")
    cell = first_ready_field(page, OTP_CELL_SELS)
    if cell:
        cells = page.locator(cell)
        try:
            total = cells.count()
        except Exception:
            total = 1
        try:
            diagnostics = page.evaluate(
                "() => [...document.querySelectorAll('input')]"
                ".filter(i => i.offsetWidth > 0)"
                ".map(i => i.outerHTML.slice(0, 220)).slice(0, 12)"
            )
            with open("/tmp/otp_dom.txt", "w") as fh:
                fh.write(f"selector={cell} count={total}\n" + "\n".join(diagnostics or []))
        except Exception:
            pass
        try:
            btns = page.evaluate(
                "() => [...document.querySelectorAll('button, input[type=submit]')]"
                ".filter(e => e.offsetWidth > 0)"
                ".map(e => e.tagName + ':' + (e.textContent || e.value || '').trim().slice(0, 30) + ':disabled=' + e.disabled)"
            )
            with open("/tmp/otp_buttons.txt", "w") as fh:
                fh.write("\n".join(btns or []))
        except Exception:
            pass
        typed = ""
        try:
            for idx in range(min(total, len(code))):
                _type_cell(cells.nth(idx), code[idx])
                typed += code[idx]
                sleep_ms(140)
        except Exception:
            raise WorkerError("微软分格验证码未能填入，请检查页面后重试", "microsoft_input")
        try:
            page.evaluate(
                "() => { const els = document.querySelectorAll('input');"
                " const el = els[els.length - 1]; if (el) { el.dispatchEvent(new Event('change', {bubbles: true})); el.blur(); } }"
            )
        except Exception:
            pass
        sleep_ms(900)
        # 填满最后一格后 Fluent OTP 常会自动提交；没触发时依次尝试 Enter、原生点击、按钮定位。
        if not logged_in(page) and first_ready_field(page, OTP_CELL_SELS):
            try:
                cells.nth(max(total - 1, 0)).press("Enter", timeout=5000)
            except Exception:
                pass
            sleep_ms(1500)
        if not logged_in(page) and first_ready_field(page, OTP_CELL_SELS):
            _click_primary_native(page)
            sleep_ms(1200)
        if not logged_in(page) and first_ready_field(page, OTP_CELL_SELS):
            try:
                click_microsoft_once(page, VERIFY_EMAIL_NEXT_SELS, "邮箱验证码")
            except WorkerError:
                if first_ready_field(page, OTP_CELL_SELS):
                    raise
        return
    selector = first_ready_field(page, OTC_SELS)
    if not selector:
        raise WorkerError("微软验证码输入框未出现，请先人工完成验证后重试", "microsoft_manual_required")
    fill_microsoft(page, selector, code, "邮箱验证码")
    click_microsoft_once(page, VERIFY_EMAIL_NEXT_SELS, "邮箱验证码")



def microsoft_login(page, acc: dict) -> None:
    started = last_progress = time.monotonic()
    email_done = pass_done = False
    email_accepted = False
    verify_email_done = False
    awaiting_code = False
    last_trace = 0.0
    state_trace_at = 0.0
    code_wait_from = 0.0
    confirm_attempts: dict = {}
    confirm_next_retry: dict = {}
    aux_email = (acc.get("recovery_email") or "").strip()
    aux_password = (acc.get("delivery_extra") or "").strip()
    confirmations = set()
    seen_states = set()
    held_state = ""
    held_since = started
    waiting = "登录页面加载"
    log("等待微软登录页面")
    while True:
        if logged_in(page):
            log("微软登录完成")
            return
        now = time.monotonic()
        # 等邮件是外部依赖（微软发信延迟可达数分钟），不计入登录耗时预算：
        # awaiting_code 期间暂停计时，收到码后把等待时长补回 started。
        if not awaiting_code and now - started >= LOGIN_TIMEOUT:
            raise WorkerError("微软登录总耗时超过 120 秒，已停止本次登录，请检查页面后重试", "microsoft_stalled")
        stall_limit = AWAIT_CODE_TIMEOUT if awaiting_code else NO_PROGRESS_TIMEOUT
        if now - last_progress >= stall_limit:
            raise WorkerError(f"微软{waiting}超过 {int(stall_limit)} 秒无进展，已停止本次登录，请检查账号或网络后重试", "microsoft_stalled")
        raw = page.url
        if url_host(raw) == AUTH_HOST and authkit_blocked(microsoft_page_text(page)):
            raise WorkerError(
                "Cline 拒绝了来自当前环境的访问（访问被阻止），已停止；请更换出口或联系 Cline 支持",
                "authkit_blocked",
            )
        if now - state_trace_at >= 10:
            state_trace_at = now
            try:
                with open("/tmp/ms_state.txt", "w") as fh:
                    fh.write(
                        f"{time.strftime('%H:%M:%S')} url={safe_page_location(raw)}"
                        f" state={state if 'state' in dir() else '?'}"
                        f" verify_done={verify_email_done} awaiting={awaiting_code}"
                        f" text={microsoft_page_text(page)[:120]!r}\n"
                    )
            except Exception:
                pass
        if url_host(raw) == AUTH_HOST:
            code = authkit_callback_error(raw)
            if code:
                raise_callback(code, safe_page_location(raw))
            state = "callback"
            if state not in seen_states:
                log("微软授权已返回，等待登录完成")
                waiting = "授权回调"
        else:
            text = microsoft_page_text(page)
            if not awaiting_code:
                message, code = microsoft_problem(text, any(visible(page, sel) for sel in CHALLENGE_SELS))
                if message:
                    raise WorkerError(message, code)
            email_sel = first_ready_field(page, EMAIL_SELS)
            pass_sel = first_ready_field(page, PASS_SELS)
            if email_done and not email_sel:
                email_accepted = True
            state = microsoft_step(bool(email_sel), bool(pass_sel), email_accepted)
            if state == "other" and on_microsoft_url(raw):
                state = microsoft_confirmation(text) or "other"
            if (
                state == "other" and not awaiting_code and verify_email_done
                and now >= confirm_next_retry.get("_unknown_cancel", 0)
                and ("/fido/" in safe_page_location(raw) or _passkey_page_text(text))
                and not first_ready_field(page, EMAIL_SELS + PASS_SELS + list(OTC_SELS) + list(OTP_CELL_SELS))
            ):
                # 登录后期的未知密钥弹窗：快速单次尝试取消，绝不阻塞状态机
                confirm_next_retry["_unknown_cancel"] = now + 10.0
                if _click_passkey_cancel(page, timeout=0.0):
                    log("登录后期出现未知弹窗，已尝试点击取消")
                    waiting = "关闭未知弹窗"
                    last_progress = time.monotonic()
            if state == "other" and awaiting_code:
                # 辅助邮箱验证码页：码框出现即收码提交
                if now - last_trace >= 5:
                    last_trace = now
                    try:
                        with open("/tmp/await_trace.txt", "a") as fh:
                            fh.write(
                                f"{time.strftime('%H:%M:%S')} url={safe_page_location(raw)}"
                                f" codebox={bool(first_ready_field(page, OTC_SELS + OTP_CELL_SELS))}"
                                f" text={text[:70]!r}\n"
                            )
                    except Exception:
                        pass
                if first_ready_field(page, OTC_SELS + OTP_CELL_SELS):
                    submit_aux_code(page, aux_email, aux_password)
                    awaiting_code = False
                    if code_wait_from:
                        started += time.monotonic() - code_wait_from  # 等信时长不计入预算
                        code_wait_from = 0.0
                    log("已提交微软邮箱验证码，等待校验结果")
                    waiting = "邮箱验证码校验"
                    last_progress = time.monotonic()
                elif "次数已达到上限" in text or "hit the limit" in text.lower():
                    # 邮箱验证方式被限流时，微软提供「使用密码」入口
                    if not click_use_password(page):
                        raise WorkerError("微软邮箱验证方式受限且无法改用密码，请稍后重试", "microsoft_locked")
                    awaiting_code = False
                    log("邮箱验证方式达到上限，已改用密码登录")
                    waiting = "改用密码登录"
                    last_progress = time.monotonic()
            if state == "other" and not awaiting_code and "/fido/" in safe_page_location(raw):
                state = "passkey"
            elif state == "other" and not verify_email_done and verify_email_challenge(text):
                if aux_email and aux_password and submit_verify_email(page, aux_email):
                    verify_email_done = True
                    awaiting_code = True
                    code_wait_from = time.monotonic()
                    log("微软要求邮箱验证，已提交辅助邮箱，等待验证码")
                    waiting = "辅助邮箱验证码"
                    last_progress = time.monotonic()
                elif click_use_password(page):
                    verify_email_done = True
                    log("微软要求邮箱验证，已改用密码登录")
                    waiting = "改用密码登录"
                    last_progress = time.monotonic()
                else:
                    raise WorkerError("微软要求邮箱验证且无法自动完成，请先人工完成验证后重试", "microsoft_manual_required")
            if state != held_state:
                held_state, held_since = state, now
            if state in ("email", "password") and now - held_since >= CARD_SETTLE:
                if state == "email" and not email_done:
                    fill_microsoft(page, email_sel, acc.get("email") or "", "账号")
                    email_done = True
                    click_microsoft_once(page, NEXT_SELS, "账号")
                    log("已提交微软账号，等待密码页面")
                    waiting = "账号提交后等待"
                    last_progress = time.monotonic()
                elif state == "password" and not pass_done:
                    fill_microsoft(page, pass_sel, acc.get("password") or "", "密码")
                    pass_done = True
                    click_microsoft_once(page, NEXT_SELS, "密码")
                    log("已提交微软密码，等待验证结果")
                    waiting = "密码验证"
                    last_progress = time.monotonic()
            elif state in ("passkey", "stay_signed_in", "consent", "tou") and state not in confirmations:
                labels = {"passkey": "跳过通行密钥", "stay_signed_in": "保持登录", "tou": "服务条款更新", "consent": "授权确认"}
                label = labels[state]
                attempts = confirm_attempts.get(state, 0)
                # 4 次后停止重试，交给无进展超时报错，避免误杀可恢复页面。
                # CARD_SETTLE 稳定期等待 React hydration 完成，过早点击会静默失效。
                if attempts < 4 and now - held_since >= CARD_SETTLE and now >= confirm_next_retry.get(state, 0):
                    confirm_attempts[state] = attempts + 1
                    confirm_next_retry[state] = now + 8.0
                    if state == "passkey":
                        # 非阻塞单轮尝试：每次主循环只点一次并立即返回事件循环，
                        # 避免长轮询把状态机堵死（页面可能同时发生其他跳转）。
                        if _click_passkey_cancel(page, timeout=0.0):
                            log("已点击通行密钥取消（第 %d 次），等待下一提示", confirm_attempts[state])
                            confirm_attempts["passkey_cancel_fail"] = 0
                        else:
                            failures = confirm_attempts.get("passkey_cancel_fail", 0) + 1
                            confirm_attempts["passkey_cancel_fail"] = failures
                            if failures == 3:
                                log("通行密钥页暂不可取消（第 %d 次），继续尝试", failures)
                            if failures >= 20:
                                # 长期不可点（安全窗口挂起）：登录态已建立，回授权入口绕过
                                from flow import AUTH_URL
                                log("通行密钥页无法取消，返回授权入口继续登录")
                                page.goto(AUTH_URL, wait_until="domcontentloaded", timeout=60000)
                                confirm_attempts["passkey_cancel_fail"] = 0
                        waiting = "跳过通行密钥"
                    else:
                        if state == "tou":
                            try:
                                elems = page.evaluate(
                                    "() => [...document.querySelectorAll('a, button')]"
                                    ".filter(e => e.offsetWidth > 0)"
                                    ".map(e => e.tagName + ' | ' + (e.textContent || '').trim().slice(0, 40) + ' | ' + e.outerHTML.slice(0, 200))"
                                )
                                with open("/tmp/tou_dom.txt", "w") as fh:
                                    fh.write(f"url={safe_page_location(raw)}\n" + "\n".join(elems or []))
                            except Exception:
                                pass
                        # 三种真人级机制轮换：真实点击 / 键盘 Enter / 表单原生提交
                        handled = False
                        if state == "stay_signed_in":
                            # 点「否」：不持久化会话，且不会触发后续通行密钥弹窗
                            try:
                                btns = page.evaluate(
                                    "() => [...document.querySelectorAll('button, a, input[type=submit]')]"
                                    ".filter(e => e.offsetWidth > 0)"
                                    ".map(e => e.tagName + '|' + (e.textContent || e.value || '').trim().slice(0, 16)"
                                    " + '|' + (e.getAttribute('data-testid') || '') + '|' + (e.id || ''))"
                                )
                                with open("/tmp/stay_buttons.txt", "w") as fh:
                                    fh.write("\n".join(btns or []))
                            except Exception:
                                pass
                            clicked_no = False
                            for selector in STAY_SIGNED_IN_NO_SELS:
                                try:
                                    if human_click(page, selector):
                                        clicked_no = True
                                        break
                                except Exception:
                                    continue
                            if clicked_no:
                                log("已在「保持登录状态」页点击「否」")
                                waiting = label
                                last_progress = time.monotonic()
                                sleep_ms(300)
                                continue
                            else:
                                log("「保持登录状态」页未找到「否」按钮，改走通用确认")
                                waiting = label
                                last_progress = time.monotonic()
                        positive = CONFIRM_POSITIVE_SELS.get(state)
                        if positive:
                            # 先按文字点「接受/是」：主按钮在部分授权页上是「取消/否」
                            for selector in positive:
                                try:
                                    if human_click(page, selector):
                                        handled = True
                                        break
                                except Exception:
                                    continue
                            if not handled and state == "consent":
                                try:
                                    btns = page.evaluate(
                                        "() => [...document.querySelectorAll('button')]"
                                        ".filter(e => e.offsetWidth > 0)"
                                        ".map(e => (e.textContent || '').trim().slice(0, 20) + ' | ' + (e.getAttribute('data-testid') || ''))"
                                    )
                                    with open("/tmp/consent_buttons.txt", "w") as fh:
                                        fh.write("\n".join(btns or []))
                                except Exception:
                                    pass
                        mechanism = attempts % 3
                        if mechanism == 1:
                            try:
                                btn = page.locator('button[data-testid="primaryButton"]').first
                                btn.focus(timeout=4000)
                                btn.press("Enter", timeout=6000)
                                handled = True
                            except Exception:
                                handled = False
                        elif mechanism == 2:
                            try:
                                handled = bool(page.evaluate(
                                    "() => { const f = document.querySelector('form');"
                                    " if (f && f.requestSubmit) { f.requestSubmit(); return true; } return false; }"
                                ))
                            except Exception:
                                handled = False
                        if not handled and not _click_primary_native(page):
                            click_microsoft_once(page, VERIFY_EMAIL_NEXT_SELS, label)
                    log("已确认微软%s（第 %d 次），等待跳转", label, attempts + 1)
                    waiting = label
                    last_progress = time.monotonic()
        # Only a new recognized phase counts as progress. DOM flicker, URL tokens,
        # and a credential field reappearing must not extend the stall deadline.
        if state != "other" and state not in seen_states:
            seen_states.add(state)
            last_progress = time.monotonic()
        sleep_ms(200)


def start_microsoft(page, acc: dict, context=None):
    from pageutil import page_url
    from urls import microsoft_ready_url

    if logged_in(page):
        return page
    if not on_microsoft_url(page_url(page)):
        try:
            click_one_of(page, MICROSOFT_AUTH, 20000, "选择 Microsoft 登录")
        except Exception:
            if not on_microsoft_url(page_url(page)) and not on_cline(page_url(page)):
                raise
            log("已进入微软授权流程")
    page = _wait_microsoft_page(page, context, 10000)
    if not microsoft_ready_url(page_url(page)) and url_host(page_url(page)) == AUTH_HOST:
        log("等待微软授权页，重试一次登录入口")
        click_microsoft_once(page, MICROSOFT_AUTH, "登录入口")
    page = _wait_microsoft_page(page, context, 35000)
    u = page_url(page)
    if on_microsoft_url(u) or visible(page, 'input[name="loginfmt"], input#i0116, input[name="passwd"], input#passwordEntry'):
        microsoft_login(page, acc)
        return page
    if microsoft_ready_url(u):
        return page
    raise WorkerError("未进入微软登录页，请检查网络或登录入口后重试", "microsoft_stalled")


def _wait_microsoft_page(page, context, timeout_ms: float):
    from pageutil import follow_identity_page
    from urls import microsoft_ready_url

    try:
        return wait_any_url(page, [], timeout_ms, context=context, ready=microsoft_ready_url)
    except Exception:
        return follow_identity_page(context, page, microsoft_ready_url)
