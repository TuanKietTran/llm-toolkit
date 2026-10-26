"""Context pool: lifecycle, health, and backpressured acquire/release of worker contexts.

Each :class:`PooledContext` wraps one :class:`BrowserSession` (= one isolated identity). The pool
hands out idle contexts, grows on demand (bounded by ``max_contexts``, RAM headroom, and account
capacity), quarantines unhealthy ones, and reaps idle/quarantined contexts. The autoscaler
(``autoscaler.py``) drives pre-warming and idle trimming; this module enforces the invariants.
"""

from __future__ import annotations

import asyncio
import itertools
import time
from collections.abc import Awaitable, Callable
from dataclasses import dataclass, field
from typing import Optional

import psutil

from ..config import Settings
from ..telemetry import get_logger, get_metrics
from .session import BrowserSession

log = get_logger(__name__)
metrics = get_metrics()

IDLE = "idle"
BUSY = "busy"
QUARANTINED = "quarantined"

SessionFactory = Callable[[], Awaitable[BrowserSession]]
SessionCloser = Callable[[BrowserSession], Awaitable[None]]
CapacityProvider = Callable[[], Optional[int]]
MemProbe = Callable[[], float]


class Backpressure(RuntimeError):
    """Raised when the pool is saturated and the wait queue is full or timed out."""


class PoolClosed(RuntimeError):
    """Raised when acquiring from a closed pool."""


@dataclass(slots=True)
class PooledContext:
    id: str
    session: BrowserSession
    state: str = IDLE
    account_id: str | None = None
    created_at: float = field(default_factory=time.monotonic)
    last_used_at: float = field(default_factory=time.monotonic)
    message_count: int = 0
    quarantine_reason: str | None = None

    @property
    def age_seconds(self) -> float:
        return time.monotonic() - self.created_at

    @property
    def idle_seconds(self) -> float:
        return time.monotonic() - self.last_used_at


class ContextPool:
    """Owns the set of live contexts and mediates access to them."""

    def __init__(
        self,
        settings: Settings,
        session_factory: SessionFactory,
        *,
        closer: SessionCloser | None = None,
        capacity_provider: CapacityProvider | None = None,
        mem_available_mb: MemProbe | None = None,
    ) -> None:
        self.settings = settings
        self._s = settings.scaling
        self._factory = session_factory
        self._closer = closer
        self._capacity_provider = capacity_provider
        self._mem_probe = mem_available_mb or (lambda: psutil.virtual_memory().available / 1e6)

        self._contexts: dict[str, PooledContext] = {}
        self._lock = asyncio.Lock()
        self._release_event = asyncio.Event()
        self._provisioning = 0  # in-flight creations (reserve capacity)
        self._pending = 0  # acquirers currently waiting (queue depth)
        self._closed = False
        self._ids = itertools.count(1)

    # ---- introspection ---------------------------------------------------------------

    def _active_locked(self) -> int:
        """Contexts that count toward capacity (idle + busy, not quarantined)."""
        return sum(1 for c in self._contexts.values() if c.state != QUARANTINED)

    def _idle_locked(self) -> list[PooledContext]:
        return [c for c in self._contexts.values() if c.state == IDLE]

    def idle_count(self) -> int:
        return sum(1 for c in self._contexts.values() if c.state == IDLE)

    @property
    def pending(self) -> int:
        return self._pending

    def stats(self) -> dict:
        idle = busy = quar = 0
        for c in self._contexts.values():
            if c.state == IDLE:
                idle += 1
            elif c.state == BUSY:
                busy += 1
            else:
                quar += 1
        cap = self._capacity_provider() if self._capacity_provider else None
        return {
            "idle": idle,
            "busy": busy,
            "quarantined": quar,
            "active": idle + busy,
            "provisioning": self._provisioning,
            "pending": self._pending,
            "max_contexts": self._s.max_contexts,
            "account_capacity": cap,
            "mem_available_mb": round(self._mem_probe(), 1),
        }

    # ---- growth checks ---------------------------------------------------------------

    def _mem_ok_for_one_more(self) -> bool:
        need = self._s.per_context_mem_mb * self._s.mem_safety_factor
        return self._mem_probe() >= need

    def _can_grow_locked(self) -> bool:
        projected = self._active_locked() + self._provisioning
        if projected >= self._s.max_contexts:
            return False
        if not self._mem_ok_for_one_more():
            return False
        cap = self._capacity_provider() if self._capacity_provider else None
        if cap is not None and projected >= cap:
            return False
        return True

    async def can_grow(self) -> bool:
        async with self._lock:
            return not self._closed and self._can_grow_locked()

    # ---- creation / destruction ------------------------------------------------------

    async def _create(self, *, busy: bool) -> PooledContext:
        """Create a context via the factory and register it. Provisioning must be pre-reserved."""
        session = await self._factory()
        cid = f"ctx-{next(self._ids)}"
        ctx = PooledContext(
            id=cid,
            session=session,
            state=BUSY if busy else IDLE,
            account_id=session.spec.account_id,
        )
        async with self._lock:
            self._contexts[cid] = ctx
        metrics.incr("contexts_created")
        metrics.gauge("contexts_total", len(self._contexts))
        log.info("context_created", ctx=cid, account=ctx.account_id, busy=busy)
        return ctx

    async def create_context(self) -> PooledContext | None:
        """Pre-warm one idle context if growth is allowed (used by the autoscaler)."""
        async with self._lock:
            if self._closed or not self._can_grow_locked():
                return None
            self._provisioning += 1
        try:
            ctx = await self._create(busy=False)
        except Exception:  # noqa: BLE001
            metrics.incr("context_create_errors")
            log.error("context_create_failed", exc_info=True)
            ctx = None
        finally:
            async with self._lock:
                self._provisioning -= 1
        self._release_event.set()  # a new idle context may unblock a waiter
        return ctx

    async def _destroy(self, ctx: PooledContext, reason: str) -> None:
        async with self._lock:
            self._contexts.pop(ctx.id, None)
        try:
            if self._closer is not None:
                await self._closer(ctx.session)
            else:
                await ctx.session.close()
        except Exception:  # noqa: BLE001
            log.warning("context_close_error", ctx=ctx.id, exc_info=True)
        metrics.incr("contexts_closed", reason=reason)
        metrics.gauge("contexts_total", len(self._contexts))
        log.info("context_destroyed", ctx=ctx.id, reason=reason, messages=ctx.message_count)

    # ---- acquire / release -----------------------------------------------------------

    async def acquire(self, *, timeout: float | None = None) -> PooledContext:
        """Get a busy-marked context, growing or waiting as needed. Raises on saturation."""
        deadline = None if timeout is None else time.monotonic() + timeout
        while True:
            grow = False
            async with self._lock:
                if self._closed:
                    raise PoolClosed("context pool is closed")
                idle = self._idle_locked()
                if idle:
                    ctx = min(idle, key=lambda c: c.last_used_at)  # LRU
                    ctx.state = BUSY
                    ctx.last_used_at = time.monotonic()
                    metrics.incr("contexts_acquired")
                    return ctx
                if self._can_grow_locked():
                    self._provisioning += 1
                    grow = True
                else:
                    if self._pending >= self._s.max_queue:
                        raise Backpressure(
                            f"pool saturated: {self._pending} waiting >= max_queue "
                            f"{self._s.max_queue}"
                        )
                    self._pending += 1
                    self._release_event.clear()

            if grow:
                try:
                    ctx = await self._create(busy=True)
                    ctx.last_used_at = time.monotonic()
                    metrics.incr("contexts_acquired")
                    return ctx
                finally:
                    async with self._lock:
                        self._provisioning -= 1
                # (create failure falls through to raise below)

            else:
                remaining = None if deadline is None else max(0.0, deadline - time.monotonic())
                try:
                    await asyncio.wait_for(self._release_event.wait(), timeout=remaining)
                except asyncio.TimeoutError:
                    async with self._lock:
                        self._pending -= 1
                    raise Backpressure("acquire timed out waiting for a free context") from None
                async with self._lock:
                    self._pending -= 1
                # loop and retry

    async def release(
        self,
        ctx: PooledContext,
        *,
        messages_sent: int = 0,
        quarantine: str | None = None,
    ) -> None:
        """Return a context to the pool (idle) or quarantine it."""
        async with self._lock:
            ctx.message_count += messages_sent
            ctx.last_used_at = time.monotonic()
            if quarantine:
                ctx.state = QUARANTINED
                ctx.quarantine_reason = quarantine
                metrics.incr("contexts_quarantined", reason=quarantine)
                log.warning("context_quarantined", ctx=ctx.id, reason=quarantine)
            else:
                ctx.state = IDLE
            self._release_event.set()

    # ---- reaping ---------------------------------------------------------------------

    async def scale_down(self) -> int:
        """Close idle contexts older than idle_ttl, keeping >= min_warm idle. Returns count."""
        now = time.monotonic()
        async with self._lock:
            idle = sorted(self._idle_locked(), key=lambda c: c.last_used_at)
            keep = self._s.min_warm
            victims: list[PooledContext] = []
            removable = max(0, len(idle) - keep)
            for c in idle[:removable]:
                if (now - c.last_used_at) >= self._s.idle_ttl_seconds:
                    victims.append(c)
        for c in victims:
            await self._destroy(c, reason="idle_ttl")
        return len(victims)

    async def reap_quarantined(self) -> list[tuple[str, str | None, str | None]]:
        """Destroy quarantined contexts. Returns (ctx_id, account_id, reason) for account cooldown."""
        async with self._lock:
            doomed = [c for c in self._contexts.values() if c.state == QUARANTINED]
        out: list[tuple[str, str | None, str | None]] = []
        for c in doomed:
            out.append((c.id, c.account_id, c.quarantine_reason))
            await self._destroy(c, reason=f"quarantine:{c.quarantine_reason}")
        return out

    async def close(self) -> None:
        """Close the pool and all contexts."""
        async with self._lock:
            self._closed = True
            contexts = list(self._contexts.values())
            self._release_event.set()
        for c in contexts:
            await self._destroy(c, reason="pool_close")


__all__ = [
    "Backpressure",
    "ContextPool",
    "PoolClosed",
    "PooledContext",
    "BUSY",
    "IDLE",
    "QUARANTINED",
]
