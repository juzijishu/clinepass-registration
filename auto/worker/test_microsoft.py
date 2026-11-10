import unittest
from types import SimpleNamespace
from unittest.mock import patch

import microsoft
from errors import WorkerError


class Clock:
    def __init__(self):
        self.now = 0.0

    def sleep(self, ms):
        self.now += ms / 1000


class Locator:
    def __init__(self, page, selector):
        self.page, self.selector = page, selector
        self.first = self

    def count(self):
        return int(self.is_visible())

    def is_visible(self):
        state = self.page.current_state()
        if self.selector == "body":
            return True
        if self.selector in microsoft.EMAIL_SELS:
            return state in ("email", "both")
        if self.selector in microsoft.PASS_SELS:
            return state in ("password", "wrong_password", "both")
        if self.selector in microsoft.NEXT_SELS:
            return True  # Even unknown/challenge pages have a primary button.
        if self.selector in microsoft.USE_PASSWORD_SELS:
            return self.page.current_state() == "verify_email" and not self.page.hide_use_password
        if self.selector in microsoft.VERIFY_EMAIL_FIELD_SELS:
            return self.page.current_state() == "verify_email"
        if self.selector in microsoft.OTC_SELS:
            return self.page.current_state() in ("code_page", "otp")
        if self.selector in microsoft.PASSKEY_CANCEL_SELS:
            return self.page.current_state() == "passkey"
        return state == "otp" and self.selector == microsoft.CHALLENGE_SELS[0]

    def is_enabled(self):
        return True

    def is_editable(self):
        return True

    def scroll_into_view_if_needed(self, **_kwargs):
        return None

    def bounding_box(self, **_kwargs):
        return {"x": 10.0, "y": 10.0, "width": 120.0, "height": 36.0}

    def get_attribute(self, _name):
        return None

    def inner_text(self, **_kwargs):
        return self.page.texts.get(self.page.current_state(), "")

    def fill(self, value, **_kwargs):
        self.page.fills.append(self.selector)
        self.page.values[self.selector] = value

    def input_value(self):
        return self.page.values.get(self.selector, "")

    def click(self, **_kwargs):
        state = self.page.current_state()
        self.page.clicks.append(state)
        if self.page.click_error:
            raise RuntimeError("Timeout after dispatch; do not retry")
        self.page.state = self.page.transitions.get(state, state)


class Page:
    def __init__(self, state, transitions=None):
        self.state = state
        self.transitions = transitions or {}
        self.clicks, self.fills, self.values = [], [], {}
        self.click_error = False
        self.hide_use_password = False
        self.urls_on_goto = []
        self.goto_calls = []
        self.state_callback = None
        self.mouse = SimpleNamespace(move=lambda *a, **k: None, down=lambda **k: None, up=lambda **k: None)
        self.url_reads = 0
        self.texts = {
            "password": "输入密码 忘记密码？ 使用验证码登录",
            "stay_signed_in": "保持登录状态?",
            "consent": "Let this app access your info?",
            "wrong_password": "Your account or password is incorrect.",
            "locked": "Your account has been locked.",
            "manual": "Help us protect your account",
            "verify_email": "验证你的电子邮件 我们将向 o***@wmhotmail.com 发送代码。 使用密码",
            "passkey": "创建你的通行密钥 下一步 取消",
        }

    def current_state(self):
        return self.state_callback() if self.state_callback else self.state

    @property
    def url(self):
        self.url_reads += 1
        state = self.current_state()
        if state == "done":
            return "https://app.cline.bot/dashboard"
        if state == "callback":
            return "https://authkit.cline.bot/callback?authorization_session_id=secret"
        # Rotating query parameters must never reset the progress deadline.
        return f"https://login.live.com/oauth20_authorize.srf?state=secret-{self.url_reads}#private"

    def evaluate(self, *_args, **_kwargs):
        return []

    def goto(self, url, **_kwargs):
        self.goto_calls.append(url)
        self.state = "done"
        self._goto_index = getattr(self, "_goto_index", 0)
        target = (self.urls_on_goto or ["about:blank"])[min(self._goto_index, len(self.urls_on_goto or ["about:blank"]) - 1)]
        self._goto_index += 1
        return target

    def locator(self, selector):
        return Locator(self, selector)


class MicrosoftLoginTests(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.logs = []
        self.patches = [
            patch.object(microsoft.time, "monotonic", side_effect=lambda: self.clock.now),
            patch.object(microsoft, "sleep_ms", side_effect=self.clock.sleep),
            patch.object(microsoft, "log", side_effect=lambda fmt, *args: self.logs.append(fmt % args if args else fmt)),
        ]
        for item in self.patches:
            item.start()
            self.addCleanup(item.stop)

    def run_login(self, page, extra=None):
        acc = {"email": "test@example.test", "password": "example"}
        acc.update(extra or {})
        microsoft.microsoft_login(page, acc)

    def assert_stalled(self, page, expected_clicks):
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertEqual(raised.exception.code, "microsoft_stalled")
        self.assertEqual(page.clicks, expected_clicks)
        self.assertLess(self.clock.now, 40)
        self.assertNotIn("secret", " ".join(self.logs) + str(raised.exception))

    def test_password_is_submitted_once_then_stops_after_no_progress(self):
        self.assert_stalled(Page("password"), ["password"])

    def test_email_is_submitted_once_then_stops_after_no_progress(self):
        self.assert_stalled(Page("email"), ["email"])

    def test_simultaneous_cards_do_not_submit_password_before_email_accepted(self):
        page = Page("both")
        self.assert_stalled(page, ["both"])
        self.assertTrue(all(selector in microsoft.EMAIL_SELS for selector in page.fills))

    def test_unknown_primary_button_is_never_clicked(self):
        self.assert_stalled(Page("unknown"), [])

    def test_known_confirmation_retries_with_interval_then_stalls(self):
        # Fluent 点击偶发失效：确认页最多重试 4 次、间隔 8 秒，之后按无进展超时
        page = Page("stay_signed_in")
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertEqual(raised.exception.code, "microsoft_stalled")
        self.assertEqual(page.clicks, ["stay_signed_in"] * 4)
        self.assertLess(self.clock.now, 70)

    def test_flickering_password_field_does_not_restart_timeout(self):
        page = Page("password")
        page.state_callback = lambda: "password" if self.clock.now < 2 or int(self.clock.now) % 2 else "unknown"
        self.assert_stalled(page, ["password"])

    def test_normal_flow_finishes_without_waiting_for_missing_email_card(self):
        page = Page("email", {"email": "password", "password": "stay_signed_in", "stay_signed_in": "consent", "consent": "done"})
        self.run_login(page)
        self.assertEqual(page.clicks, ["email", "password", "stay_signed_in", "consent"])
        self.assertLess(self.clock.now, 8)

    def test_existing_password_card_finishes_promptly(self):
        page = Page("password", {"password": "done"})
        self.run_login(page)
        self.assertEqual(page.clicks, ["password"])
        self.assertLess(self.clock.now, 2)

    def test_already_logged_in_skips_all_controls(self):
        page = Page("done")
        self.run_login(page)
        self.assertEqual(page.clicks, [])
        self.assertEqual(self.clock.now, 0)

    def test_password_error_is_reported_immediately_without_retry(self):
        page = Page("password", {"password": "wrong_password"})
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertEqual(raised.exception.code, "microsoft_credentials")
        self.assertEqual(page.clicks, ["password"])
        self.assertLess(self.clock.now, 2)

    def test_locked_account_and_security_challenges_stop_without_clicking(self):
        for state, code in (("locked", "microsoft_locked"), ("manual", "microsoft_manual_required"), ("otp", "microsoft_manual_required")):
            with self.subTest(state=state):
                page = Page(state)
                with self.assertRaises(WorkerError) as raised:
                    self.run_login(page)
                self.assertEqual(raised.exception.code, code)
                self.assertEqual(page.clicks, [])

    def test_ambiguous_click_failure_is_never_resubmitted(self):
        page = Page("password")
        page.click_error = True
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertEqual(raised.exception.code, "microsoft_submit")
        self.assertEqual(page.clicks, ["password"])

    def test_passkey_prompt_is_cancelled_and_finishes(self):
        page = Page("passkey", {"passkey": "stay_signed_in", "stay_signed_in": "consent", "consent": "done"})
        self.run_login(page)
        self.assertEqual(page.clicks, ["passkey", "stay_signed_in", "consent"])
        self.assertTrue(any("通行密钥" in entry for entry in self.logs))
        self.assertLess(self.clock.now, 8)

    def test_passkey_cancel_is_retried_without_blocking(self):
        # 取消失败时保持非阻塞重试，不把状态机堵死；页面转入完成态后正常结束
        page = Page("passkey", {"passkey": "done"})
        attempts = {"n": 0}

        def cancel(_page, timeout=0.0):
            attempts["n"] += 1
            return False

        # 模拟：安全窗口约 10 秒后自行消失、页面转入完成态
        page.state_callback = lambda: "passkey" if self.clock.now < 10 else "done"
        with patch.object(microsoft, "_click_passkey_cancel", side_effect=cancel):
            self.run_login(page)
        self.assertGreater(attempts["n"], 1)  # 期间持续重试，未死锁
        self.assertTrue(any("通行密钥" in entry for entry in self.logs))

    def test_verify_email_uses_aux_mailbox_code(self):
        page = Page("verify_email", {"verify_email": "code_page", "code_page": "done"})
        with patch.object(microsoft, "fetch_aux_code", return_value="1234567") as fetch:
            self.run_login(page, {"recovery_email": "aux@wmhotmail.test", "delivery_extra": "secret"})
        self.assertEqual(page.clicks, ["verify_email", "code_page"])
        fetch.assert_called_once_with("aux@wmhotmail.test", "secret", timeout=240.0)
        self.assertTrue(any("辅助邮箱" in entry for entry in self.logs))
        self.assertTrue(any(sel in microsoft.OTC_SELS for sel in page.fills))
        self.assertLess(self.clock.now, 5)

    def test_verify_email_code_failure_reports_manual(self):
        page = Page("verify_email", {"verify_email": "code_page"})
        with patch.object(microsoft, "fetch_aux_code", return_value=None):
            with self.assertRaises(WorkerError) as raised:
                self.run_login(page, {"recovery_email": "aux@wmhotmail.test", "delivery_extra": "secret"})
        self.assertEqual(raised.exception.code, "microsoft_manual_required")
        self.assertEqual(page.clicks, ["verify_email"])

    def test_verify_email_challenge_switches_to_password_and_finishes(self):
        page = Page("verify_email", {"verify_email": "password", "password": "stay_signed_in", "stay_signed_in": "consent", "consent": "done"})
        self.run_login(page)  # 无辅助邮箱凭据时回退到「使用密码」链接
        self.assertEqual(page.clicks, ["verify_email", "password", "stay_signed_in", "consent"])
        self.assertTrue(any("改用密码" in entry for entry in self.logs))
        self.assertLess(self.clock.now, 5)

    def test_verify_email_without_any_path_requires_manual(self):
        page = Page("verify_email")
        page.hide_use_password = True
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertEqual(raised.exception.code, "microsoft_manual_required")
        self.assertEqual(page.clicks, [])
        self.assertLess(self.clock.now, 5)

    def test_callback_wait_does_not_repeat_oauth_login(self):
        self.assert_stalled(Page("callback"), [])

    def test_total_timeout_has_its_own_reason_after_real_stage_changes(self):
        page = Page("email")
        stages = ["email", "password", "stay_signed_in", "callback"]
        page.state_callback = lambda: stages[min(int(self.clock.now // 30), len(stages) - 1)]
        with self.assertRaises(WorkerError) as raised:
            self.run_login(page)
        self.assertIn("总耗时超过 120 秒", str(raised.exception))
        self.assertNotIn("无进展", str(raised.exception))
        self.assertEqual(page.clicks, ["email", "password"] + ["stay_signed_in"] * 4)
        self.assertLess(self.clock.now, 121)

    def test_identity_popup_is_still_followed(self):
        entry = SimpleNamespace(url="https://authkit.cline.bot/sign-in")
        popup = Page("password")
        context = SimpleNamespace(pages=[entry, popup])
        with patch("protocol.log"):
            self.assertIs(microsoft._wait_microsoft_page(entry, context, 1000), popup)

    def test_start_with_existing_session_never_opens_login_again(self):
        page = Page("done")
        self.assertIs(microsoft.start_microsoft(page, {}), page)
        self.assertEqual(page.clicks, [])

    def test_security_messages_and_known_confirmation_detection(self):
        for message in ("验证你的身份", "Enter the code we sent", "More information required", "添加安全信息"):
            self.assertEqual(microsoft.microsoft_problem(message)[1], "microsoft_manual_required")
        self.assertEqual(microsoft.microsoft_problem("That Microsoft account doesn’t exist.")[1], "microsoft_account_missing")
        self.assertEqual(microsoft.microsoft_problem("Sign in 输入密码 忘记密码？ 使用验证码登录"), ("", ""))
        self.assertEqual(microsoft.microsoft_confirmation("Next / Continue / 下一步"), "")
        self.assertEqual(microsoft.safe_page_location("https://login.live.com/path?code=secret#secret"), "login.live.com/path")


if __name__ == "__main__":
    unittest.main()
