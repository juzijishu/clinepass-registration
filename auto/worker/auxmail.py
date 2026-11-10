"""供应商辅助邮箱 IMAP 收码。

交付六列中的第 5 列是辅助邮箱地址、第 6 列是该邮箱的密码；供应商的
邮箱服务器使用自签名证书。微软「验证你的电子邮件」挑战把验证码发到
辅助邮箱，这里负责提交后轮询收取。
"""

import email
import email.utils
import imaplib
import re
import ssl
import time
from datetime import datetime, timedelta, timezone

from protocol import log

CODE_RES = (
    re.compile(r"(?:code is|一次性代码为|代码为|验证码[是为:：]?)\s*[:：]?\s*(\d{4,8})", re.IGNORECASE),
    re.compile(r"\b(\d{4,8})\b"),
)
POLL_INTERVAL = 4.0


def _message_text(raw: bytes) -> tuple[str, datetime | None]:
    msg = email.message_from_bytes(raw)
    when = None
    try:
        when = email.utils.parsedate_to_datetime(msg.get("Date", ""))
    except (TypeError, ValueError):
        pass
    chunks = [msg.get("Subject", "") or ""]
    if msg.is_multipart():
        for part in msg.walk():
            if part.get_content_type() == "text/plain" and not part.get_filename():
                payload = part.get_payload(decode=True)
                if payload:
                    chunks.append(payload.decode("utf-8", "replace"))
    else:
        payload = msg.get_payload(decode=True)
        if payload:
            chunks.append(payload.decode("utf-8", "replace"))
    return "\n".join(chunks), when


def _poll_once(host: str, address: str, password: str, since: datetime) -> str | None:
    ctx = ssl._create_unverified_context()
    conn = imaplib.IMAP4_SSL(host, 993, ssl_context=ctx, timeout=15)
    try:
        conn.login(address, password)
        conn.select("INBOX", readonly=True)
        typ, data = conn.search(None, "ALL")
        if typ != "OK":
            return None
        ids = data[0].split()
        for uid in reversed(ids[-5:]):
            typ, fetched = conn.fetch(uid, "(RFC822)")
            if typ != "OK" or not fetched or not fetched[0]:
                continue
            text, when = _message_text(fetched[0][1])
            if when and when.astimezone(timezone.utc) < since:
                continue
            for pattern in CODE_RES:
                found = pattern.search(text)
                if found:
                    return found.group(1)
        return None
    finally:
        try:
            conn.logout()
        except Exception:
            pass


def fetch_aux_code(address: str, password: str, timeout: float = 110.0) -> str | None:
    """轮询辅助邮箱，返回最新验证码；失败返回 None。"""
    host = address.rsplit("@", 1)[-1].strip().lower()
    if not host:
        return None
    since = datetime.now(timezone.utc) - timedelta(seconds=120)
    deadline = time.monotonic() + timeout
    attempt = 0
    while time.monotonic() < deadline:
        attempt += 1
        try:
            code = _poll_once(host, address, password, since)
            if code:
                log("已从辅助邮箱收到验证码（第 %d 次查询）", attempt)
                return code
        except Exception as exc:  # IMAP 抖动按可重试处理，直到总超时
            log("辅助邮箱第 %d 次查询失败：%s", attempt, str(exc)[:80])
        time.sleep(POLL_INTERVAL)
    return None
