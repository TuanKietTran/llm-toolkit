"""Multi-account layer: budgets/cooldowns, scheduling, secure store, email aliasing + OTP."""

from .budget import Cooldown, RollingBudget
from .email_provider import (
    DuckDuckGoEmailProvider,
    EmailProvider,
    NullEmailProvider,
    build_email_provider,
)
from .inbox import ImapInboxReader, build_inbox_reader, extract_otp, message_matches
from .manager import AccountManager, AccountState, NoAccountAvailable
from .store import AccountStore

__all__ = [
    "AccountManager",
    "AccountState",
    "AccountStore",
    "Cooldown",
    "DuckDuckGoEmailProvider",
    "EmailProvider",
    "ImapInboxReader",
    "NoAccountAvailable",
    "NullEmailProvider",
    "RollingBudget",
    "build_email_provider",
    "build_inbox_reader",
    "extract_otp",
    "message_matches",
]
