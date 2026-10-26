"""Unit tests for the pure OTP-extraction/matching helpers, plus find_otp against an injected fake
IMAP connection (no real network/mail server)."""

from __future__ import annotations

import email
from email.message import Message

import pytest

from orchestrator.accounts.inbox import ImapInboxReader, extract_otp, message_matches


def test_extract_otp_six_digit_code():
    assert extract_otp("Your ChatGPT verification code is 482913.") == "482913"


def test_extract_otp_ignores_embedded_in_longer_number():
    assert extract_otp("order id 1234567890123, code: 55512") == "55512"


def test_extract_otp_none_when_no_code():
    assert extract_otp("no codes here at all") is None


def test_extract_otp_none_for_empty_text():
    assert extract_otp("") is None


def _msg(*, from_addr="", subject="", to="") -> Message:
    m = email.message.EmailMessage()
    if from_addr:
        m["From"] = from_addr
    if subject:
        m["Subject"] = subject
    if to:
        m["To"] = to
    return m


def test_message_matches_by_from_contains():
    msg = _msg(from_addr="OpenAI <noreply@openai.com>", subject="Your code")
    assert message_matches(msg, from_contains="openai", alias=None) is True


def test_message_matches_by_alias_in_to():
    msg = _msg(from_addr="someone@example.com", to="myalias@duck.com")
    assert message_matches(msg, from_contains="openai", alias="myalias@duck.com") is True


def test_message_matches_false_when_neither_matches():
    msg = _msg(from_addr="spam@example.com", subject="buy now")
    assert message_matches(msg, from_contains="openai", alias="myalias@duck.com") is False


def test_message_matches_case_insensitive():
    msg = _msg(from_addr="Noreply@OpenAI.com")
    assert message_matches(msg, from_contains="OPENAI", alias=None) is True


class _FakeImap:
    """Minimal imaplib.IMAP4-shaped fake returning one scripted message."""

    def __init__(self, raw_message: bytes | None):
        self._raw = raw_message
        self.flagged: list[str] = []

    def select(self, mailbox):
        return "OK", [b""]

    def search(self, charset, criterion):
        if self._raw is None:
            return "OK", [b""]
        return "OK", [b"1"]

    def fetch(self, msg_id, parts):
        return "OK", [(b"1 (RFC822 {n})", self._raw)]

    def store(self, msg_id, flag, value):
        self.flagged.append(msg_id.decode() if isinstance(msg_id, bytes) else msg_id)
        return "OK", []

    def logout(self):
        return "OK", []


def _raw_email(*, from_addr: str, subject: str, body: str) -> bytes:
    msg = email.message.EmailMessage()
    msg["From"] = from_addr
    msg["Subject"] = subject
    msg.set_content(body)
    return bytes(msg)


@pytest.mark.asyncio
async def test_find_otp_returns_code_from_matching_message():
    raw = _raw_email(from_addr="noreply@openai.com", subject="Your code", body="Code: 738291")
    fake = _FakeImap(raw)
    reader = ImapInboxReader("host", 993, "user", "pass", connect=lambda: fake)

    code = await reader.find_otp(alias=None, from_contains="openai", timeout=1.0, poll_interval=0.05)
    assert code == "738291"
    assert fake.flagged == ["1"]  # marks the message seen once consumed


@pytest.mark.asyncio
async def test_find_otp_times_out_when_nothing_matches():
    fake = _FakeImap(None)
    reader = ImapInboxReader("host", 993, "user", "pass", connect=lambda: fake)

    code = await reader.find_otp(alias=None, from_contains="openai", timeout=0.15, poll_interval=0.05)
    assert code is None
