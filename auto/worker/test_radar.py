import unittest
from types import SimpleNamespace
from unittest.mock import patch

import radar
from radar import is_no_sms, should_retry_phone


class RadarRetryTests(unittest.TestCase):
    def test_used_number_should_retry(self):
        self.assertTrue(should_retry_phone(RuntimeError("发送验证码失败，号码可能已被使用")))
        self.assertTrue(
            should_retry_phone(
                RuntimeError(
                    "等待 URL 超时，当前 URL=https://authkit.cline.bot/radar-challenge/send?user_id=user_01ABC"
                )
            )
        )

    def test_sms_timeout_should_retry(self):
        self.assertTrue(is_no_sms(RuntimeError("等待验证码超时")))
        self.assertTrue(should_retry_phone(RuntimeError("等待验证码超时")))

    def test_other_errors_do_not_retry(self):
        self.assertFalse(should_retry_phone(RuntimeError("填写区号失败")))
        self.assertFalse(
            should_retry_phone(RuntimeError("等待 URL 超时，当前 URL=https://app.cline.bot/dashboard"))
        )


class ManualVerificationTests(unittest.TestCase):
    def test_wait_returns_when_page_leaves_radar(self):
        urls = iter(
            [
                "https://authkit.cline.bot/radar-challenge/send?user_id=user_01",
                "https://authkit.cline.bot/radar-challenge/verify?user_id=user_01",
                "https://app.cline.bot/dashboard",
            ]
        )
        notes = []
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send")
        with patch("radar.sleep_ms"), patch("radar.page_url", side_effect=lambda _page: next(urls)), patch(
            "radar.log", side_effect=lambda msg, *args: notes.append(msg)
        ):
            radar.wait_manual_verification(page, {"manual_wait_seconds": 60})
        self.assertTrue(any("人工验证已完成" in note for note in notes))

    def test_wait_times_out_as_worker_error(self):
        clock = [0.0]

        def tick(_ms):
            clock[0] += 1.0

        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send")
        with patch("radar.sleep_ms", side_effect=tick), patch(
            "radar.page_url", side_effect=lambda _page: page.url
        ), patch("radar.time.monotonic", side_effect=lambda: clock[0]):
            with self.assertRaises(radar.WorkerError) as raised:
                radar.wait_manual_verification(page, {"manual_wait_seconds": 60})
        self.assertEqual(raised.exception.code, "manual_timeout")

    def test_manual_mode_skips_herosms_requirements(self):
        # 人工模式下缺少密钥、国家和预算也必须能进入等待，不取号。
        entered = []
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send?user_id=user_01")
        with patch("radar.wait_radar_form"), patch(
            "radar.goto_radar_send"
        ), patch("radar.wait_manual_verification", side_effect=lambda *_a: entered.append(True)):
            radar.handle_radar(page, {"manual_sms": True})
        self.assertEqual(entered, [True])

    def test_manual_mode_normalizes_verify_url_back_to_send(self):
        seen = {}
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/verify?user_id=user_01")

        def record(_page, target):
            seen["target"] = target

        with patch("radar.goto_radar_send", side_effect=record), patch("radar.wait_radar_form"), patch(
            "radar.wait_manual_verification"
        ):
            radar.handle_radar(page, {"manual_sms": True})
        self.assertEqual(seen["target"], "https://authkit.cline.bot/radar-challenge/send?user_id=user_01")

    def test_manual_mode_does_not_touch_herosms_client(self):
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send")
        with patch("radar.goto_radar_send"), patch("radar.wait_radar_form"), patch(
            "radar.wait_manual_verification"
        ), patch("radar.Client") as client:
            radar.handle_radar(page, {"manual_sms": True})
        client.assert_not_called()

    def test_manual_wait_detects_completion_in_another_tab(self):
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send")
        other = SimpleNamespace(url="https://app.cline.bot/dashboard")
        context = SimpleNamespace(pages=[page, other])
        with patch("radar.sleep_ms"), patch("radar.time.monotonic", side_effect=[0.0, 1.0]):
            radar.wait_manual_verification(page, {"manual_wait_seconds": 60}, context)

    def test_manual_wait_ignores_browser_error_pages_until_timeout(self):
        page = SimpleNamespace(url="chrome-error://chromewebdata/")
        clock = [0.0]

        def tick(_ms):
            clock[0] += 1.0

        with patch("radar.sleep_ms", side_effect=tick), patch(
            "radar.time.monotonic", side_effect=lambda: clock[0]
        ):
            with self.assertRaises(radar.WorkerError):
                radar.wait_manual_verification(page, {"manual_wait_seconds": 60})

    def test_manual_wait_stops_when_browser_closed(self):
        page = SimpleNamespace(url="https://authkit.cline.bot/radar-challenge/send", is_closed=lambda: True)
        with self.assertRaises(radar.WorkerError) as raised:
            radar.wait_manual_verification(page, {"manual_wait_seconds": 60})
        self.assertEqual(raised.exception.code, "manual_timeout")

    def test_manual_wait_seconds_clamped(self):
        self.assertEqual(radar.manual_wait_seconds({}), 1800)
        self.assertEqual(radar.manual_wait_seconds({"manual_wait_seconds": 1}), 60)
        self.assertEqual(radar.manual_wait_seconds({"manual_wait_seconds": 10**9}), 7200)
        self.assertEqual(radar.manual_wait_seconds({"manual_wait_seconds": 900}), 900)


if __name__ == "__main__":
    unittest.main()
