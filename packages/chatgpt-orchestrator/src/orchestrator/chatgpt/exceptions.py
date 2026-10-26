"""Typed errors the ChatGPT driver raises so the pool/account manager can react precisely."""

from __future__ import annotations


class ChatGPTError(RuntimeError):
    """Base for all driver errors."""


class RateLimited(ChatGPTError):
    """Free-plan message limit hit. The account should cool down."""

    def __init__(self, message: str = "rate limited", *, retry_after: float | None = None) -> None:
        super().__init__(message)
        self.retry_after = retry_after


class ChallengePresented(ChatGPTError):
    """A Cloudflare/Turnstile/'verify you are human' challenge appeared. Quarantine the context."""


class LoginRequired(ChatGPTError):
    """A login wall blocks progress (session expired or anonymous access disabled)."""


class DomChanged(ChatGPTError):
    """An expected element was missing/ambiguous — the ChatGPT UI likely changed. Patch selectors."""


class CompletionTimeout(ChatGPTError):
    """The reply did not finish streaming within the timeout."""

    def __init__(self, message: str = "completion timed out", *, partial: str | None = None) -> None:
        super().__init__(message)
        self.partial = partial


class OperationTimeout(ChatGPTError):
    """The complete ask operation exceeded its deadline."""


class EmptyReply(ChatGPTError):
    """Generation finished but produced no text, with no challenge/rate-limit/login to explain it.

    Observed when the free anonymous tier soft-throttles concurrent requests from one IP: the
    send↔stop button flips (so generation "started") but the completion comes back empty. It is
    transient and worth a retry on a fresh context, so it is surfaced rather than returned as a
    bogus empty success.
    """


__all__ = [
    "ChallengePresented",
    "ChatGPTError",
    "CompletionTimeout",
    "DomChanged",
    "EmptyReply",
    "LoginRequired",
    "OperationTimeout",
    "RateLimited",
]
