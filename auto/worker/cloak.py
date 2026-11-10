import os
from importlib.metadata import PackageNotFoundError, version as package_version

from protocol import log

DEFAULT_TIMEZONE = "Asia/Shanghai"
DEFAULT_LOCALE = "zh-CN"
GEOIP_MIN_BYTES = 1_000_000


def apply_cloak_env(settings: dict) -> None:
    mapping = {
        "cloak_cache_dir": "CLOAKBROWSER_CACHE_DIR",
        "cloak_binary_path": "CLOAKBROWSER_BINARY_PATH",
    }
    for key, env in mapping.items():
        val = (settings.get(key) or "").strip()
        if val:
            os.environ[env] = val
    license_key = (settings.get("license_key") or "").strip()
    if license_key:
        os.environ["CLOAKBROWSER_LICENSE_KEY"] = license_key
    else:
        os.environ.pop("CLOAKBROWSER_LICENSE_KEY", None)
        os.environ.pop("CLOAKBROWSER_VERSION", None)
    version = (settings.get("cloak_version") or "").strip()
    if version and license_key:
        os.environ["CLOAKBROWSER_VERSION"] = version


def chrome_args(settings: dict, seed: int) -> list[str]:
    args = [
        "--no-sandbox",
        "--disable-setuid-sandbox",
        "--disable-dev-shm-usage",
        "--fingerprint-storage-quota=5000",
        "--ignore-gpu-blocklist",
        "--fingerprint-windows-font-metrics",
        "--fingerprint-allow-3p-cookies",
    ]
    if settings.get("network_control_url"):
        args.extend(["--disable-quic", "--proxy-bypass-list=<-loopback>"])
    if seed > 0:
        args.append(f"--fingerprint={seed}")
    if settings.get("virtual_display") or settings.get("headless"):
        args.append("--disable-gpu")
    return args


def cloak_cache_dir() -> str:
    return os.environ.get("CLOAKBROWSER_CACHE_DIR") or os.path.expanduser("~/.cloakbrowser")


def geoip_db_path(root: str | None = None) -> str:
    return os.path.join(root or cloak_cache_dir(), "geoip", "GeoLite2-City.mmdb")


def geoip_db_ready(root: str | None = None) -> bool:
    path = geoip_db_path(root)
    try:
        return os.path.isfile(path) and os.path.getsize(path) >= GEOIP_MIN_BYTES
    except OSError:
        return False


def humanize_options() -> dict:
    """Existing interaction timing overrides, not the unmodified SDK preset."""
    return {
        "humanize": True,
        "human_preset": "default",
        "human_config": {
            "typing_delay": 22,
            "typing_delay_spread": 10,
            "typing_pause_chance": 0.02,
            "mistype_chance": 0,
            "idle_between_actions": False,
            "idle_between_duration": [0.05, 0.12],
        },
    }


COUNTRY_LOCALES = {
    "GB": "en-GB", "US": "en-US", "SG": "en-SG", "AU": "en-AU",
    "HK": "zh-HK", "TW": "zh-TW", "MO": "zh-HK", "CN": "zh-CN",
    "JP": "ja-JP", "KR": "ko-KR", "AR": "es-AR",
}


def resolve_exit_geo(proxy: str, root: str | None = None) -> tuple[str, str] | None:
    """经代理实测出口 IP 并用本地 GeoLite2 解析时区/语言。

    SDK 自带的 geoip 模式在浏览器指向本地中继（127.0.0.1）时会把回环地址
    当作代理去查库并直接抛错；这里改为显式解析，relay 环境同样安全。
    """
    try:
        import urllib.request

        import maxminddb

        opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({"http": proxy, "https": proxy})
        )
        ip = ""
        for _ in range(2):  # 出口检测偶发超时，重试一次
            try:
                ip = opener.open("https://ifconfig.me", timeout=10).read().decode(errors="replace").strip()
                break
            except Exception:
                continue
        if not ip or len(ip) > 45:
            return None
        with maxminddb.open_database(geoip_db_path(root)) as db:
            record = db.get(ip) or {}
        tz = ((record.get("location") or {}).get("time_zone") or "").strip()
        country = ((record.get("country") or {}).get("iso_code") or "").strip().upper()
        if not tz:
            return None
        return tz, COUNTRY_LOCALES.get(country, "en-US")
    except Exception:
        return None


def apply_geo_settings(kwargs: dict, proxy: str | None, root: str | None = None) -> dict:
    """Prefer an explicit timezone resolved from the real exit IP.

    The SDK geoip flag is only used with a reachable GeoLite2 City DB; first
    launch otherwise downloads GeoLite2 from GitHub (httpx timeout 300s) and
    that download is not bounded by CLOAKBROWSER_GEOIP_TIMEOUT_SECONDS.
    A stalled GitHub fetch looks like a hang at「正在启动浏览器」.
    """
    if proxy and geoip_db_ready(root):
        geo = resolve_exit_geo(proxy, root)
        if geo:
            kwargs["geoip"] = False
            kwargs["timezone"] = geo[0]
            kwargs["locale"] = geo[1]
            return kwargs
    kwargs["geoip"] = False
    kwargs["timezone"] = DEFAULT_TIMEZONE
    kwargs["locale"] = DEFAULT_LOCALE
    return kwargs


# 默认使用标准 Playwright Chromium：Cline 的 Radar 能识别 cloakbrowser 的
# 指纹特征并直接 policy_denied；标准 Chromium 已验证可完整通过注册流程。
# 设 AUTO_CLOAK_BROWSER=1 可切回 cloakbrowser（需要指纹伪装的场景）。
STANDARD_BROWSER_ENV = "AUTO_CLOAK_BROWSER"


def _standard_chromium_executable() -> str:
    """标准 Playwright Chromium 可执行文件（优先环境变量，其次缓存目录）。"""
    override = (os.environ.get("STANDARD_CHROMIUM_EXECUTABLE") or "").strip()
    if override:
        return override
    root = os.path.expanduser("~/Library/Caches/ms-playwright")
    if os.path.isdir(root):
        versions = sorted(
            (d for d in os.listdir(root) if d.startswith("chromium-") and d != "chromium_headless_shell"),
            key=lambda d: int(d.split("-")[-1]) if d.split("-")[-1].isdigit() else 0,
            reverse=True,
        )
        for version in versions:
            candidate = os.path.join(
                root, version, "chrome-mac-arm64",
                "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing",
            )
            if os.path.isfile(candidate):
                return candidate
    return ""


def _launch_standard(settings: dict, proxy: str | None, headless: bool):
    """用标准 Playwright Chromium 启动（不注入指纹伪装）。

    Cline 的 Radar 会识别 cloakbrowser 的指纹特征并直接 policy_denied；
    标准 Chromium 已验证可通过完整注册流程。
    """
    from playwright.sync_api import sync_playwright

    executable = _standard_chromium_executable()
    profile = (settings.get("profile_dir") or "").strip()
    if not profile:
        raise RuntimeError("缺少浏览器配置目录")
    os.makedirs(profile, exist_ok=True)
    playwright = sync_playwright().start()
    launch_args: dict = {
        "headless": headless,
        "args": ["--no-sandbox", "--disable-dev-shm-usage"],
    }
    if executable:
        launch_args["executable_path"] = executable
        log("使用标准 Chromium: %s", executable)
    if proxy:
        launch_args["proxy"] = {"server": proxy}
    try:
        context = playwright.chromium.launch_persistent_context(profile, **launch_args)
    except Exception:
        playwright.stop()
        raise
    context._zcode_playwright = playwright  # 关闭时一并释放 driver
    log("标准 Chromium 已启动（无指纹伪装）")
    return context


def launch_ctx(settings: dict, seed: int):
    # Apply settings before importing the SDK, which may read environment
    # defaults while initializing its modules.
    apply_cloak_env(settings)
    use_cloak = (os.environ.get(STANDARD_BROWSER_ENV) or "").strip() in ("1", "true", "on")
    if not use_cloak:
        proxy = (settings.get("browser_proxy") or settings.get("proxy") or "").strip() or None
        return _launch_standard(settings, proxy, bool(settings.get("headless")))
    from cloakbrowser import launch_persistent_context

    profile = (settings.get("profile_dir") or "").strip()
    if not profile:
        raise RuntimeError("缺少浏览器配置目录")
    os.makedirs(profile, exist_ok=True)
    proxy = (settings.get("browser_proxy") or settings.get("proxy") or "").strip() or None
    headless = bool(settings.get("headless"))
    license_key = (settings.get("license_key") or "").strip()
    version = (settings.get("cloak_version") or "").strip()
    args = chrome_args(settings, seed)
    if not license_key and not os.environ.get("CLOAKBROWSER_LICENSE_KEY"):
        log("Cloak 官方包装：任务未提供许可证，浏览器版本由 SDK 本地配置或指定程序决定")
    try:
        sdk_version = package_version("cloakbrowser")
    except PackageNotFoundError:
        sdk_version = "unknown"
    kwargs = {
        "user_data_dir": profile,
        "headless": headless,
        **humanize_options(),
        "args": args,
    }
    # CLOAK_STEALTH=0 关闭指纹伪装（排查风控误判用）：标准指纹更接近真实
    # 用户环境，部分风控系统对「指纹被改写」的判定反而更严格。
    stealth = os.environ.get("CLOAK_STEALTH", "").strip().lower()
    if stealth in ("0", "false", "off"):
        kwargs["stealth_args"] = False
        log("已关闭指纹伪装（CLOAK_STEALTH=0），使用浏览器原生指纹")
    # service_workers 保持默认（允许）：阻断会让风控拿不到 Service Worker
    # 侧的行为信号；短信验证页的隔离由 route 层负责。
    apply_geo_settings(kwargs, proxy)
    if proxy:
        kwargs["proxy"] = proxy
        if kwargs.get("geoip"):
            log("使用全局代理，已发现本地 GeoIP 库，将由 SDK 解析地区信息")
        else:
            log("使用全局代理，但没有本地 GeoIP 库，跳过 70MB 下载，时区固定 %s", DEFAULT_TIMEZONE)
    else:
        log("未设代理，不下载 GeoIP，使用固定时区 %s / %s", DEFAULT_TIMEZONE, DEFAULT_LOCALE)
    log(
        "Cloak 官方包装启动 sdk=%s humanize=default+custom geoip=%s tz=%s locale=%s headless=%s seed=%s requested_version=%s cache=%s runtime=%s",
        sdk_version,
        kwargs.get("geoip"),
        kwargs.get("timezone") or "",
        kwargs.get("locale") or "",
        headless,
        seed,
        version if version and license_key else "SDK默认",
        cloak_cache_dir(),
        os.environ.get("XDG_RUNTIME_DIR") or "",
    )
    if license_key:
        kwargs["license_key"] = license_key
    if version and (license_key or os.environ.get("CLOAKBROWSER_LICENSE_KEY")):
        kwargs["browser_version"] = version
    log("正在启动浏览器")
    # Preserve the actual failure. A license error, locked profile or crashed
    # browser must not be relabeled as GeoIP failure and launched a second time.
    # GeoIP errors also need to be surfaced instead of silently changing settings.
    ctx = launch_persistent_context(**kwargs)
    log("浏览器已启动")
    _suppress_webauthn_prompts(ctx)
    return ctx


WEBAUTHN_SUPPRESS_SCRIPT = """
// 自动化环境没有平台认证器。原生「通行密钥和安全密钥」弹窗是浏览器 UI，
// 页面 DOM 无法触达；这里让页面认为平台认证器不可用、创建请求立即以
// 「用户取消」失败——等效人工点取消，且不会真的创建密钥。
try {
  if (window.PublicKeyCredential) {
    PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable =
      function () { return Promise.resolve(false); };
  }
  if (navigator.credentials) {
    navigator.credentials.create = function () {
      return Promise.reject(new DOMException(
        'The operation either timed out or was not allowed.', 'NotAllowedError'));
    };
  }
} catch (e) {}
"""


def _suppress_webauthn_prompts(ctx) -> None:
    try:
        ctx.add_init_script(WEBAUTHN_SUPPRESS_SCRIPT)
        log("已注入 WebAuthn 抑制脚本：通行密钥弹窗将不出现，创建请求按取消处理")
    except Exception as exc:
        log("WebAuthn 抑制脚本注入失败（遇到密钥页可能需要人工取消）: %s", str(exc)[:80])
