"""Autoscaler: keep the context pool sized to demand.

A periodic tick (EMA-smoothed, with an action cooldown to avoid flapping):
  * trims idle contexts past their TTL (delegated to ``pool.scale_down``),
  * pre-warms up to ``min_warm`` idle contexts,
  * grows toward smoothed pending demand, bounded by ``max_create_per_tick`` and everything
    ``pool.can_grow`` enforces (max_contexts, RAM headroom, account capacity).

``tick`` is pure-ish and directly unit-testable against a fake pool.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass

from ..config import Settings
from ..telemetry import get_logger, get_metrics

log = get_logger(__name__)
metrics = get_metrics()


@dataclass
class TickResult:
    created: int
    closed: int
    ema_pending: float
    idle: int
    busy: int


class Autoscaler:
    """Drives a :class:`ContextPool` toward demand on a fixed cadence."""

    def __init__(self, pool, settings: Settings) -> None:
        self.pool = pool
        self._s = settings.scaling
        self._ema_pending = 0.0
        self._last_scale_up_at = 0.0
        self._task: asyncio.Task | None = None
        self._stop = asyncio.Event()

    @property
    def ema_pending(self) -> float:
        return self._ema_pending

    async def start(self) -> None:
        if self._task is not None:
            return
        self._stop.clear()
        self._task = asyncio.create_task(self._run(), name="autoscaler")
        log.info("autoscaler_started", tick_s=self._s.scale_tick_seconds)

    async def stop(self) -> None:
        self._stop.set()
        if self._task is not None:
            try:
                await self._task
            finally:
                self._task = None
        log.info("autoscaler_stopped")

    async def _run(self) -> None:
        while not self._stop.is_set():
            try:
                await self.tick()
            except Exception:  # noqa: BLE001 - one bad tick must not kill the loop
                log.error("autoscaler_tick_error", exc_info=True)
            try:
                await asyncio.wait_for(self._stop.wait(), timeout=self._s.scale_tick_seconds)
            except asyncio.TimeoutError:
                pass

    async def tick(self) -> TickResult:
        stats = self.pool.stats()
        pending = stats["pending"]
        idle = stats["idle"]
        busy = stats["busy"]

        a = self._s.ema_alpha
        self._ema_pending = a * pending + (1.0 - a) * self._ema_pending

        # Scale down is always allowed (independent of the cooldown).
        closed = await self.pool.scale_down()

        now = time.monotonic()
        cooling = (now - self._last_scale_up_at) < self._s.scale_action_cooldown_seconds

        created = 0
        if not cooling:
            budget = self._s.max_create_per_tick
            while budget > 0:
                cur_idle = self.pool.idle_count()
                need_warm = cur_idle < self._s.min_warm
                need_demand = self._ema_pending > cur_idle
                if not (need_warm or need_demand):
                    break
                if not await self.pool.can_grow():
                    break
                ctx = await self.pool.create_context()
                if ctx is None:
                    break
                created += 1
                budget -= 1
            if created:
                self._last_scale_up_at = now

        metrics.gauge("autoscaler_ema_pending", round(self._ema_pending, 3))
        metrics.gauge("contexts_idle", idle)
        metrics.gauge("contexts_busy", busy)
        metrics.gauge("pool_pending", pending)
        if created or closed:
            log.info(
                "autoscaler_action",
                created=created,
                closed=closed,
                ema_pending=round(self._ema_pending, 2),
                idle=idle,
                busy=busy,
            )
        return TickResult(
            created=created,
            closed=closed,
            ema_pending=self._ema_pending,
            idle=idle,
            busy=busy,
        )


__all__ = ["Autoscaler", "TickResult"]
