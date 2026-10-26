"""Shared fakes for offline tests: no real browser, no network.

These stand in for the Playwright-backed layers (`BrowserSession`, `ChatGPTDriver`) so higher
layers (pool, workers, core, MCP server) can be exercised deterministically and fast.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from orchestrator.browser.session import SessionSpec


class Clock:
    """A controllable monotonic clock for deterministic budget/cooldown/completion tests."""

    def __init__(self, start: float = 0.0) -> None:
        self.now = start

    def __call__(self) -> float:
        return self.now

    def advance(self, seconds: float) -> None:
        self.now += seconds


class FakeSession:
    """Stands in for `BrowserSession`: tracks close() without touching Playwright."""

    def __init__(self, spec: SessionSpec) -> None:
        self.spec = spec
        self.closed = False

    async def page(self):  # pragma: no cover - not exercised unless a test calls it
        return None

    async def close(self) -> None:
        self.closed = True


@dataclass
class FakeDriver:
    """Stands in for `ChatGPTDriver`: canned open()/ask() with call tracking."""

    session: object
    settings: object
    replies: dict[str, str] = field(default_factory=dict)
    default_reply: str = "ok"
    raise_on: dict[str, Exception] = field(default_factory=dict)
    opened: bool = False
    sent: list[str] = field(default_factory=list)

    async def open(self) -> None:
        self.opened = True

    async def ask(self, prompt: str, *, timeout: float | None = None) -> str:
        self.sent.append(prompt)
        if prompt in self.raise_on:
            raise self.raise_on[prompt]
        return self.replies.get(prompt, f"{self.default_reply}:{prompt}")


class FakeToolHost:
    """Stands in for `InwardToolHost`: a fixed catalog + scripted call results."""

    def __init__(self, catalog: list[dict] | None = None, results: dict[str, str] | None = None) -> None:
        self._catalog = catalog or []
        self._results = results or {}
        self.started = False
        self.closed = False
        self.calls: list[tuple[str, dict]] = []

    async def start(self) -> None:
        self.started = True

    def catalog(self) -> list[dict]:
        return self._catalog

    async def call(self, name: str, args: dict) -> str:
        self.calls.append((name, args))
        if name not in self._results:
            raise KeyError(f"unknown tool: {name!r}")
        return self._results[name]

    async def close(self) -> None:
        self.closed = True


__all__ = ["Clock", "FakeDriver", "FakeSession", "FakeToolHost"]
