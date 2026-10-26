"""Unit tests for Autoscaler.tick against a fake pool (no real ContextPool/browser involved)."""

from __future__ import annotations

import pytest

from orchestrator.browser.autoscaler import Autoscaler
from orchestrator.config import Settings


class FakePool:
    """Just enough of ContextPool's surface for Autoscaler.tick to drive."""

    def __init__(self, *, pending=0, idle=0, busy=0, can_grow=True):
        self.pending = pending
        self.idle = idle
        self.busy = busy
        self._can_grow = can_grow
        self.created = 0
        self.scale_down_calls = 0

    def stats(self):
        return {"pending": self.pending, "idle": self.idle, "busy": self.busy}

    def idle_count(self):
        return self.idle

    async def can_grow(self):
        return self._can_grow

    async def create_context(self):
        if not self._can_grow:
            return None
        self.created += 1
        self.idle += 1
        return object()

    async def scale_down(self):
        self.scale_down_calls += 1
        return 0


def _settings(**scaling_kwargs) -> Settings:
    s = Settings()
    for k, v in scaling_kwargs.items():
        setattr(s.scaling, k, v)
    return s


@pytest.mark.asyncio
async def test_tick_pre_warms_to_min_warm():
    settings = _settings(min_warm=3, max_create_per_tick=10, scale_action_cooldown_seconds=0.0)
    pool = FakePool(pending=0, idle=0, busy=0)
    scaler = Autoscaler(pool, settings)

    result = await scaler.tick()
    assert result.created == 3
    assert pool.created == 3


@pytest.mark.asyncio
async def test_tick_grows_on_ema_demand():
    settings = _settings(
        min_warm=0, max_create_per_tick=10, scale_action_cooldown_seconds=0.0, ema_alpha=1.0
    )
    pool = FakePool(pending=5, idle=0, busy=0)
    scaler = Autoscaler(pool, settings)

    result = await scaler.tick()
    assert result.ema_pending == 5.0  # alpha=1.0 -> EMA snaps straight to the sample
    assert result.created == 5  # grows until idle_count catches up to ema_pending


@pytest.mark.asyncio
async def test_tick_respects_action_cooldown():
    settings = _settings(
        min_warm=0, max_create_per_tick=10, scale_action_cooldown_seconds=1000.0, ema_alpha=1.0
    )
    pool = FakePool(pending=5, idle=0, busy=0)
    scaler = Autoscaler(pool, settings)

    first = await scaler.tick()
    assert first.created == 5
    # Second tick still wants to grow (idle=5 now, but pending stays 5 so no more need); force
    # demand again to prove the cooldown (not lack of demand) is what blocks the next grow.
    pool.pending = 20
    second = await scaler.tick()
    assert second.created == 0  # blocked by the cooldown window, not by demand


@pytest.mark.asyncio
async def test_tick_caps_growth_at_max_create_per_tick():
    settings = _settings(
        min_warm=0, max_create_per_tick=2, scale_action_cooldown_seconds=0.0, ema_alpha=1.0
    )
    pool = FakePool(pending=10, idle=0, busy=0)
    scaler = Autoscaler(pool, settings)

    result = await scaler.tick()
    assert result.created == 2


@pytest.mark.asyncio
async def test_tick_stops_growing_when_pool_cannot_grow():
    settings = _settings(min_warm=5, max_create_per_tick=10, scale_action_cooldown_seconds=0.0)
    pool = FakePool(pending=0, idle=0, busy=0, can_grow=False)
    scaler = Autoscaler(pool, settings)

    result = await scaler.tick()
    assert result.created == 0


@pytest.mark.asyncio
async def test_tick_always_calls_scale_down_even_when_cooling():
    settings = _settings(min_warm=0, scale_action_cooldown_seconds=1000.0)
    pool = FakePool(pending=0, idle=0, busy=0)
    scaler = Autoscaler(pool, settings)
    await scaler.tick()
    await scaler.tick()
    assert pool.scale_down_calls == 2  # trimming idle is never blocked by the action cooldown
