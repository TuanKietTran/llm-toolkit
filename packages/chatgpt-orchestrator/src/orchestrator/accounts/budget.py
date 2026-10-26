"""Per-account rate budgets and cooldowns.

Free ChatGPT enforces a rolling message cap (~10/5h, less on ad-opt-out). We stay *under* the cap
to reduce both bans and challenge frequency, and we apply a cooldown when a limit/challenge banner
appears. Clocks are injectable so this is deterministically testable.
"""

from __future__ import annotations

import time
from collections import deque
from collections.abc import Callable


class RollingBudget:
    """A sliding-window message budget: at most ``limit`` sends per ``window_seconds``."""

    def __init__(
        self,
        limit: int,
        window_seconds: float,
        *,
        clock: Callable[[], float] = time.monotonic,
    ) -> None:
        self.limit = limit
        self.window = window_seconds
        self._clock = clock
        self._events: deque[float] = deque()

    def _prune(self, now: float) -> None:
        cutoff = now - self.window
        while self._events and self._events[0] <= cutoff:
            self._events.popleft()

    def used(self) -> int:
        self._prune(self._clock())
        return len(self._events)

    def remaining(self) -> int:
        return max(0, self.limit - self.used())

    def available(self) -> bool:
        return self.remaining() > 0

    def consume(self, n: int = 1) -> None:
        now = self._clock()
        self._prune(now)
        for _ in range(n):
            self._events.append(now)

    def retry_after(self) -> float:
        """Seconds until at least one slot frees; 0 if budget is available now."""
        now = self._clock()
        self._prune(now)
        if len(self._events) < self.limit:
            return 0.0
        return max(0.0, self._events[0] + self.window - now)


class Cooldown:
    """A monotonic 'do not schedule until' gate."""

    def __init__(self, *, clock: Callable[[], float] = time.monotonic) -> None:
        self._clock = clock
        self._until = 0.0

    def active(self) -> bool:
        return self._clock() < self._until

    def remaining(self) -> float:
        return max(0.0, self._until - self._clock())

    def trigger(self, seconds: float) -> None:
        self._until = max(self._until, self._clock() + seconds)

    def clear(self) -> None:
        self._until = 0.0


__all__ = ["Cooldown", "RollingBudget"]
