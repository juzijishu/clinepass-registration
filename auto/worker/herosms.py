import json
import ssl
import time
import urllib.error
import urllib.parse
import urllib.request

HANDLER = "https://hero-sms.com/stubs/handler_api.php"
DEFAULT_SERVICE = "ot"
UA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

ITU = {
    1, 7, 20, 27, 30, 31, 32, 33, 34, 36, 39, 40, 41, 43, 44, 45, 46, 47, 48, 49,
    51, 52, 53, 54, 55, 56, 57, 58, 60, 61, 62, 63, 64, 65, 66, 81, 82, 84, 86,
    90, 91, 92, 93, 94, 95, 98,
}

# 本 worker 进程累计的接码花费，登录结果里上报给 Go 侧记账。
SPENT = 0.0


def spent() -> float:
    return round(SPENT, 2)


def digits(s: str) -> str:
    return "".join(ch for ch in (s or "") if ch.isdigit())


def _sms_error(text: str):
    text = (text or "").strip()
    mapping = {
        "BAD_KEY": "Hero SMS API Key 无效",
        "BAD_API_KEY": "Hero SMS API Key 无效",
        "NO_NUMBERS": "这个区域/报价没有号码",
        "NO_BALANCE": "Hero SMS 余额不足",
        "WRONG_SERVICE": "Hero SMS 服务代码无效",
        "WRONG_MAX_PRICE": "报价无效，请重新选择",
    }
    if text in mapping:
        raise RuntimeError(mapping[text])
    if text.startswith("BANNED"):
        raise RuntimeError("Hero SMS 账号被限制")


def _build_request(api_key: str, action: str, extra: dict | None = None) -> urllib.request.Request:
    q = {"api_key": api_key, "action": action}
    if extra:
        q.update({k: str(v) for k, v in extra.items() if v is not None and v != ""})
    url = HANDLER + "?" + urllib.parse.urlencode(q)
    return urllib.request.Request(
        url,
        headers={
            "User-Agent": UA,
            "Accept": "*/*",
            "Connection": "close",
        },
    )


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        raise RuntimeError("Hero SMS 不接受重定向，已停止请求")


def _handler(api_key: str, action: str, extra: dict | None = None, proxy: str = "") -> bytes:
    req = _build_request(api_key, action, extra)
    # HeroSMS 对经代理发出的请求直接 403（实测）：接口一律走系统 socket
    # 直连（与浏览器流量分离）。proxy 参数仅用于校验中继配置存在。
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    if proxy:
        parsed = urllib.parse.urlparse(proxy)
        if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost") or parsed.username:
            raise RuntimeError("Hero SMS 缺少有效的本地代理中继")
    try:
        with opener.open(req, timeout=30) as resp:
            body = resp.read()
    except urllib.error.HTTPError as exc:
        body = exc.read() if exc.fp else b""
        text = body.decode("utf-8", "replace").strip()
        _sms_error(text)
        raise RuntimeError(f"Hero SMS HTTP {exc.code}: {text[:200] or exc.reason}") from exc
    except (urllib.error.URLError, ssl.SSLError, TimeoutError, ConnectionError) as exc:
        reason = getattr(exc, "reason", exc)
        raise RuntimeError(f"Hero SMS 暂时不可用: {reason}") from exc
    text = body.decode("utf-8", "replace").strip()
    _sms_error(text)
    return body


def _transient(exc: Exception) -> bool:
    msg = str(exc).lower()
    needles = (
        "暂时不可用",
        "http 429",
        "http 502",
        "http 503",
        "http 504",
        "timed out",
        "timeout",
        "unexpected_eof",
        "eof occurred",
        "ssl",
        "urlopen error",
        "connection reset",
        "broken pipe",
        "remote end closed",
    )
    return any(n in msg for n in needles)


def _split_phone(phone: str, hint: int = 0) -> tuple[int, str]:
    phone = digits(phone)
    if phone.startswith("00"):
        phone = phone[2:]
    if hint > 0:
        prefix = str(hint)
        rest = phone
        if phone.startswith(prefix) and len(phone) > len(prefix) + 4:
            rest = phone[len(prefix) :]
        rest = rest.lstrip("0") or phone
        return hint, rest
    for n in (3, 2, 1):
        if len(phone) < n + 6:
            continue
        try:
            code = int(phone[:n])
        except ValueError:
            continue
        if code not in ITU and n != 1:
            continue
        local = phone[n:].lstrip("0")
        if local:
            return code, local
    return 0, phone


class Client:
    def __init__(self, api_key: str, service: str = "", proxy: str = "", budget: float = 0.0):
        self.api_key = (api_key or "").strip()
        self.service = (service or "").strip() or DEFAULT_SERVICE
        self.proxy = proxy
        # budget 与 spent 均为货币单位；budget 为 0 表示不限制。
        self.budget = budget
        self.spent = 0.0

    def _request(self, action, extra=None):
        return _handler(self.api_key, action, extra, self.proxy)

    def _charge(self, cost: float) -> None:
        global SPENT
        self.spent += cost
        SPENT += cost

    def get_number(self, country: int, max_price: float = 0) -> dict:
        if country != 16:
            raise RuntimeError("接码区域必须为英格兰")
        if self.budget > 0:
            if max_price <= 0:
                raise RuntimeError("启用接码预算时必须设置单号限价")
            if self.spent + max_price > self.budget + 1e-9:
                raise RuntimeError("接码预算不足，已停止取号")
        extra = {"service": self.service, "country": country}
        if max_price > 0:
            extra["maxPrice"] = ("%g" % max_price)
            extra["fixedPrice"] = "true"
        last = None
        for i in range(3):
            try:
                body = self._request("getNumberV2", extra)
                last = None
                break
            except Exception as exc:
                last = exc
                if not _transient(exc) or i == 2:
                    raise
                time.sleep(8)
        if last is not None:
            raise last
        text = body.decode("utf-8", "replace").strip()
        if text.startswith("ACCESS_NUMBER:"):
            parts = text.split(":")
            phone = digits(parts[-1])
            code, local = _split_phone(phone)
            self._charge(max_price)
            return {"id": parts[1], "phone": phone, "phone_code": code, "local_number": local}
        raw = json.loads(body)
        phone = digits(str(raw.get("phoneNumber") or raw.get("phone") or raw.get("number") or ""))
        hint = int(raw.get("countryPhoneCode") or raw.get("phoneCode") or 0)
        code, local = _split_phone(phone, hint)
        cost = float(raw.get("activationCost") or raw.get("cost") or 0) or max_price
        self._charge(cost)
        return {
            "id": str(raw.get("activationId") or raw.get("id") or ""),
            "phone": phone,
            "phone_code": code,
            "local_number": local,
        }

    def wait_code(self, num_id: str, timeout: float = 120) -> str:
        deadline = time.time() + timeout
        last = None
        while time.time() < deadline:
            try:
                body = self._request("getStatus", {"id": num_id})
            except Exception as exc:
                if not _transient(exc):
                    raise
                last = exc
                from protocol import log

                log("Hero SMS 查询中断，继续等验证码: %s", exc)
                time.sleep(8)
                continue
            text = body.decode("utf-8", "replace").strip()
            if text in ("STATUS_WAIT_CODE", "STATUS_WAIT_RETRY") or text.startswith("STATUS_WAIT_RETRY:"):
                time.sleep(8)
                continue
            if text.startswith("STATUS_OK:"):
                return digits(text.split(":", 1)[1])
            if text in ("STATUS_CANCEL", "STATUS_CANCELLED"):
                raise RuntimeError("接码已取消或失败")
            time.sleep(8)
        if last is not None:
            raise TimeoutError("等待验证码超时") from last
        raise TimeoutError("等待验证码超时")

    def finish(self, num_id: str) -> None:
        try:
            self._request("setStatus", {"id": num_id, "status": 6})
        except Exception:
            pass

    def cancel(self, num_id: str) -> None:
        try:
            self._request("setStatus", {"id": num_id, "status": 8})
        except Exception:
            pass
