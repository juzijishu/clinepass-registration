import unittest
import json
import os
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch
from herosms import Client

from herosms import UA, _build_request, _transient


class HeroSMSRequestTests(unittest.TestCase):
    def test_sms_connects_directly_ignoring_proxy_and_environment(self):
        # HeroSMS 对经代理发出的请求直接 403（实测）：接口必须走系统 socket
        # 直连，忽略 proxy 参数与 NO_PROXY/http_proxy 等环境变量。
        paths = []
        class Origin(BaseHTTPRequestHandler):
            def do_GET(self):
                paths.append(self.path)
                body = json.dumps({"activationId":"synthetic", "phoneNumber":"447700900123", "countryPhoneCode":44}).encode()
                self.send_response(200); self.end_headers(); self.wfile.write(body)
            def log_message(self, *args): pass
        server = ThreadingHTTPServer(("127.0.0.1",0),Origin)
        thread = threading.Thread(target=server.serve_forever,daemon=True); thread.start()
        try:
            with patch("herosms.HANDLER", f"http://127.0.0.1:{server.server_port}/handler"), \
                 patch.dict(os.environ, {"NO_PROXY": "*", "http_proxy": "http://127.0.0.1:1", "https_proxy": "http://127.0.0.1:1"}):
                c = Client("synthetic", "ot", "http://127.0.0.1:1")  # 伪中继地址，不得被使用
                number = c.get_number(16, 0.05)
            self.assertEqual(number["phone_code"], 44)
            self.assertEqual(len(paths), 1)
            self.assertIn("country=16", paths[0])
            self.assertIn("maxPrice=0.05", paths[0])
        finally:
            server.shutdown(); server.server_close(); thread.join()

    def test_wrong_country_is_rejected_before_charging(self):
        client = Client("synthetic", "ot")
        with patch("herosms._handler") as handler:
            with self.assertRaisesRegex(RuntimeError, "英格兰"):
                client.get_number(4, 0.05)
            handler.assert_not_called()

    def test_request_uses_browser_ua(self):
        req = _build_request("key", "getNumberV2", {"service": "ot", "country": 4})
        self.assertEqual(req.get_header("User-agent"), UA)
        self.assertNotIn("Python-urllib", req.get_header("User-agent"))
        self.assertIn("action=getNumberV2", req.full_url)

    def test_403_is_not_transient(self):
        self.assertFalse(_transient(RuntimeError("Hero SMS HTTP 403: Forbidden")))
        self.assertTrue(_transient(RuntimeError("Hero SMS HTTP 503")))

    def test_ssl_eof_is_transient(self):
        self.assertTrue(
            _transient(RuntimeError("Hero SMS 暂时不可用: [SSL: UNEXPECTED_EOF_WHILE_READING] EOF occurred"))
        )
        self.assertTrue(_transient(RuntimeError("<urlopen error [SSL: UNEXPECTED_EOF_WHILE_READING]>")))


class HeroSMSBudgetTests(unittest.TestCase):
    BODY = json.dumps({"activationId": "synthetic", "phoneNumber": "447700900123", "countryPhoneCode": 44, "activationCost": 0.06}).encode()

    def test_rejects_when_budget_cannot_cover_another_number(self):
        client = Client("synthetic", "ot", budget=0.04)
        with patch("herosms._handler", return_value=self.BODY) as handler:
            with self.assertRaisesRegex(RuntimeError, "预算不足"):
                client.get_number(16, 0.05)
            handler.assert_not_called()

    def test_charges_actual_cost_and_stops_at_budget(self):
        client = Client("synthetic", "ot", budget=0.11)
        with patch("herosms._handler", return_value=self.BODY):
            client.get_number(16, 0.06)
            with self.assertRaisesRegex(RuntimeError, "预算不足"):
                client.get_number(16, 0.06)
        self.assertAlmostEqual(client.spent, 0.06)

    def test_budget_requires_price_cap(self):
        client = Client("synthetic", "ot", budget=1.0)
        with patch("herosms._handler", return_value=self.BODY) as handler:
            with self.assertRaisesRegex(RuntimeError, "单号限价"):
                client.get_number(16, 0)
            handler.assert_not_called()


if __name__ == "__main__":
    unittest.main()
