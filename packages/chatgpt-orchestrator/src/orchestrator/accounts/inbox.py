"""Read ChatGPT login OTP codes from the inbox that masked aliases forward to.

DuckDuckGo (and most alias services) only *forward* mail, so the verification code lands in a real
mailbox. This reads it over IMAP: poll recent/unseen messages, keep the ones that look like they
came from OpenAI (or were addressed to the alias), and pull the 6-digit code out. The parsing
(:func:`extract_otp`, :func:`message_matches`) is pure and unit-testable; IMAP I/O runs in a thread.
"""

from __future__ import annotations

import asyncio
import email
import imaplib
import re
import time
from collections.abc import Callable
from email.message import Message

from ..config import EmailSettings
from ..telemetry import get_logger

log = get_logger(__name__)

# 4–8 digit codes, not embedded in a longer number. ChatGPT uses 6.
_DEFAULT_OTP_RE = re.compile(r"(?<![0-9])([0-9]{4,8})(?![0-9])")


def extract_otp(text: str, pattern: re.Pattern[str] = _DEFAULT_OTP_RE) -> str | None:
    """Return the first standalone numeric code in ``text``, or ``None``."""
    if not text:
        return None
    m = pattern.search(text)
    return m.group(1) if m else None


def _body_text(msg: Message) -> str:
    """Best-effort concatenation of text/plain (preferred) and text/html parts."""
    parts: list[str] = []
    if msg.is_multipart():
        for part in msg.walk():
            ctype = part.get_content_type()
            if ctype in ("text/plain", "text/html"):
                try:
                    payload = part.get_payload(decode=True)
                    if payload:
                        parts.append(payload.decode(part.get_content_charset() or "utf-8", "replace"))
                except (LookupError, ValueError):
                    continue
    else:
        payload = msg.get_payload(decode=True)
        if payload:
            parts.append(payload.decode(msg.get_content_charset() or "utf-8", "replace"))
    return "\n".join(parts)


def message_matches(msg: Message, *, from_contains: str, alias: str | None) -> bool:
    """True if the message looks like the OTP we want (from OpenAI, or addressed to the alias)."""
    haystack = " ".join(
        filter(None, [msg.get("From", ""), msg.get("Subject", ""), msg.get("To", ""), msg.get("Delivered-To", "")])
    ).lower()
    if alias and alias.lower() in haystack:
        return True
    return from_contains.lower() in haystack


class ImapInboxReader:
    """Polls an IMAP mailbox for the latest matching OTP."""

    def __init__(
        self,
        host: str,
        port: int,
        username: str,
        password: str,
        *,
        ssl: bool = True,
        mailbox: str = "INBOX",
        connect: Callable[[], imaplib.IMAP4] | None = None,
    ) -> None:
        self.host = host
        self.port = port
        self.username = username
        self.password = password
        self.ssl = ssl
        self.mailbox = mailbox
        self._connect = connect  # injectable for tests

    def _open(self) -> imaplib.IMAP4:
        if self._connect is not None:
            return self._connect()
        conn: imaplib.IMAP4 = (
            imaplib.IMAP4_SSL(self.host, self.port)
            if self.ssl
            else imaplib.IMAP4(self.host, self.port)
        )
        conn.login(self.username, self.password)
        return conn

    def _scan_once(self, *, from_contains: str, alias: str | None, limit: int) -> str | None:
        conn = self._open()
        try:
            conn.select(self.mailbox)
            typ, data = conn.search(None, "UNSEEN")
            if typ != "OK" or not data or not data[0]:
                return None
            ids = data[0].split()[-limit:]
            for msg_id in reversed(ids):  # newest first
                typ, msg_data = conn.fetch(msg_id, "(RFC822)")
                if typ != "OK" or not msg_data or not isinstance(msg_data[0], tuple):
                    continue
                msg = email.message_from_bytes(msg_data[0][1])
                if not message_matches(msg, from_contains=from_contains, alias=alias):
                    continue
                code = extract_otp(msg.get("Subject", "")) or extract_otp(_body_text(msg))
                if code:
                    conn.store(msg_id, "+FLAGS", "\\Seen")
                    return code
            return None
        finally:
            try:
                conn.logout()
            except Exception:  # noqa: BLE001
                pass

    async def find_otp(
        self,
        *,
        alias: str | None = None,
        from_contains: str = "openai",
        timeout: float = 120.0,
        poll_interval: float = 3.0,
        limit: int = 25,
    ) -> str | None:
        """Poll until a matching code appears or ``timeout`` elapses."""
        deadline = time.monotonic() + timeout
        while True:
            try:
                code = await asyncio.to_thread(
                    self._scan_once, from_contains=from_contains, alias=alias, limit=limit
                )
            except Exception as exc:  # noqa: BLE001
                log.warning("imap_scan_error", error=str(exc))
                code = None
            if code:
                log.info("otp_found", alias=alias)
                return code
            if time.monotonic() >= deadline:
                log.warning("otp_timeout", alias=alias, timeout=timeout)
                return None
            await asyncio.sleep(poll_interval)


def build_inbox_reader(settings: EmailSettings) -> ImapInboxReader | None:
    """Construct an IMAP reader if inbox creds are configured, else ``None``."""
    if not (settings.imap_host and settings.imap_username and settings.imap_password):
        return None
    return ImapInboxReader(
        settings.imap_host,
        settings.imap_port,
        settings.imap_username,
        settings.imap_password,
        ssl=settings.imap_ssl,
        mailbox=settings.imap_mailbox,
    )


__all__ = [
    "ImapInboxReader",
    "build_inbox_reader",
    "extract_otp",
    "message_matches",
]
