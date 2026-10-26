"""ChatGPT web driver: navigation, brittle selectors, completion detection, typed errors."""

from .completion import await_completion
from .driver import ChatGPTDriver
from .exceptions import (
    ChallengePresented,
    ChatGPTError,
    CompletionTimeout,
    DomChanged,
    LoginRequired,
    RateLimited,
)

__all__ = [
    "ChallengePresented",
    "ChatGPTDriver",
    "ChatGPTError",
    "CompletionTimeout",
    "DomChanged",
    "LoginRequired",
    "RateLimited",
    "await_completion",
]
