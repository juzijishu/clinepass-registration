import unittest
from types import SimpleNamespace

from stage_network import StageNetwork


class StageNetworkTests(unittest.TestCase):
    def test_verification_locks_other_tabs_without_network_switching(self):
        # 出口由中继按域名分流，脚本层不再做全局切换（避免掐断连接）
        transitions = []
        network = StageNetwork(lambda mode: transitions.append(mode))
        page = object()
        other = object()

        def request(url, owner=page, navigation=True, main=True):
            return SimpleNamespace(url=url, is_navigation_request=lambda: navigation,
                frame=SimpleNamespace(page=owner, parent_frame=None if main else object()))

        self.assertTrue(network.allow(request("https://login.live.com/oauth20_authorize.srf")))
        self.assertTrue(network.allow(request("https://authkit.cline.bot/radar-challenge/send?session=private")))
        # 进入验证/Cline 环节后，其他标签页的请求被阻止
        self.assertFalse(network.allow(request("https://cdn.example.test/app.js", owner=other, navigation=False)))
        self.assertTrue(network.direct)
        # 全程无网络切换
        self.assertEqual(transitions, [])
        # 回到微软身份提供商页面恢复其他标签页
        self.assertTrue(network.allow(request("https://login.live.com/x")))
        self.assertFalse(network.direct)

    def test_cline_owned_predicate_excludes_identity_providers(self):
        from urls import on_cline_owned
        self.assertTrue(on_cline_owned("https://authkit.cline.bot/oauth/callback?code=private"))
        self.assertTrue(on_cline_owned("https://app.cline.bot/dashboard"))
        self.assertTrue(on_cline_owned("https://auth.workos.com/x"))
        self.assertFalse(on_cline_owned("https://login.microsoftonline.com/common/oauth2/v2.0/authorize"))
        self.assertFalse(on_cline_owned("https://login.live.com/x"))
        self.assertFalse(on_cline_owned("https://accounts.google.com/x"))

    def test_query_or_untrusted_host_cannot_select_direct(self):
        transitions = []
        n = StageNetwork(transitions.append)
        for url in ("https://evil.test/radar-challenge/send", "https://login.live.com/?next=/radar-challenge/send"):
            req = SimpleNamespace(url=url,is_navigation_request=lambda:True,
                frame=SimpleNamespace(page=object(),parent_frame=None))
            self.assertTrue(n.allow(req))
        self.assertEqual(transitions, [])

if __name__ == "__main__": unittest.main()
