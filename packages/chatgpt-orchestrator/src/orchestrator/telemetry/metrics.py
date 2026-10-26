"""Lightweight in-process metrics.

Counters (monotonic) and gauges (point-in-time), plus simple timing accumulators. This is
intentionally dependency-free; ``pool_status`` / ``accounts_status`` MCP resources and the
``doctor`` command read snapshots from here. Swap for Prometheus later if needed.
"""

from __future__ import annotations

import threading
import time
from contextlib import contextmanager


def _key(name: str, labels: dict[str, str]) -> str:
    if not labels:
        return name
    tags = ",".join(f"{k}={labels[k]}" for k in sorted(labels))
    return f"{name}{{{tags}}}"


class Metrics:
    """Thread-safe counters/gauges/timers with a plain-dict snapshot."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._counters: dict[str, float] = {}
        self._gauges: dict[str, float] = {}
        self._timers: dict[str, tuple[float, int]] = {}  # key -> (total_seconds, count)

    def incr(self, name: str, value: float = 1.0, **labels: str) -> None:
        key = _key(name, labels)
        with self._lock:
            self._counters[key] = self._counters.get(key, 0.0) + value

    def gauge(self, name: str, value: float, **labels: str) -> None:
        key = _key(name, labels)
        with self._lock:
            self._gauges[key] = value

    def observe(self, name: str, seconds: float, **labels: str) -> None:
        key = _key(name, labels)
        with self._lock:
            total, count = self._timers.get(key, (0.0, 0))
            self._timers[key] = (total + seconds, count + 1)

    @contextmanager
    def timer(self, name: str, **labels: str):
        start = time.perf_counter()
        try:
            yield
        finally:
            self.observe(name, time.perf_counter() - start, **labels)

    def snapshot(self) -> dict[str, dict]:
        with self._lock:
            timers = {
                k: {
                    "total_seconds": total,
                    "count": count,
                    "avg_seconds": (total / count) if count else 0.0,
                }
                for k, (total, count) in self._timers.items()
            }
            return {
                "counters": dict(self._counters),
                "gauges": dict(self._gauges),
                "timers": timers,
            }

    def reset(self) -> None:
        with self._lock:
            self._counters.clear()
            self._gauges.clear()
            self._timers.clear()


_METRICS = Metrics()


def get_metrics() -> Metrics:
    """Return the process-wide metrics registry."""
    return _METRICS


__all__ = ["Metrics", "get_metrics"]
