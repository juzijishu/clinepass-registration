import urllib.request

from protocol import log
from urls import AUTH_HOST, on_cline_owned, on_radar_url, url_host


class StageNetwork:
    def __init__(self, switch):
        self.switch = switch
        self.direct = False
        self.owner = None

    def allow(self, request):
        frame = request.frame
        page = frame.page
        # owner 页面（验证码页）自身的所有请求——包括提交验证码的 XHR——
        # 永远放行；此前「direct 且非 owner 一律阻断」会把验证页自己的
        # 提交请求也拦下，表现为填完验证码后页面卡住。
        if self.direct and self.owner is not None and page is not self.owner:
            return False
        if request.is_navigation_request() and frame.parent_frame is None:
            url = request.url
            # 网络出口由中继按目标域名分流（Cline 自有域直连、其余走代理），
            # 这里不再做全局切换：切换会关闭所有活动连接，把正在进行的页面
            # 加载或 OAuth 回调掐断（表现为空白页 / 回调失败）。
            # 仅保留「短信验证页期间锁定标签页」的隔离职责。
            sms_page = url_host(url) == AUTH_HOST and on_radar_url(url)
            self.direct = sms_page
            self.owner = page if sms_page else None
        return True

    @classmethod
    def install(cls, context, settings):
        endpoint = settings.get("network_control_url")
        token = settings.get("network_control_token")
        if not endpoint or not token:
            raise RuntimeError("缺少分阶段网络控制，已停止登录")
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

        def switch(mode):
            req = urllib.request.Request(endpoint, data=mode.encode(), method="POST",
                headers={"Authorization": "Bearer " + token})
            try:
                with opener.open(req, timeout=10) as response:
                    if response.status != 200:
                        raise RuntimeError("network switch rejected")
            except Exception:
                raise RuntimeError("阶段网络切换失败，已停止请求") from None

        network = cls(switch)

        def route_request(route):
            try:
                allowed = network.allow(route.request)
            except Exception:
                route.abort("blockedbyclient")
                raise RuntimeError("阶段网络切换失败，已停止请求") from None
            if allowed:
                route.continue_()
            else:
                route.abort("blockedbyclient")

        context.route("**/*", route_request)
        # WebSocket 保持可用：Cline/Radar 可能依赖它上报行为信号（鼠标轨迹、
        # 打字节奏）；关闭 WebSocket 会让风控拿不到信号而按高风险处理。
        # 短信验证页期间由 route_request 阻断其他标签页即可。
        return network
