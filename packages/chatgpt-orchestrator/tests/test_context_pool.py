"""Unit tests for ContextPool: growth to max, backpressure, LRU reuse, quarantine/reap, scale_down.

The session factory here just fabricates a cheap object per call (no real BrowserSession).
"""

from __future__ import annotations

import asyncio
import itertools

import pytest

from orchestrator.browser.context_pool import Backpressure, ContextPool, PoolClosed
from orchestrator.browser.session import SessionSpec
from orchestrator.config import Settings

from .fakes import FakeSession


def _settings(*, max_contexts=3, max_queue=1, idle_ttl=0.0, min_warm=0) -> Settings:
    s = Settings()
    s.scaling.max_contexts = max_contexts
    s.scaling.max_queue = max_queue
    s.scaling.idle_ttl_seconds = idle_ttl
    s.scaling.min_warm = min_warm
    s.scaling.per_context_mem_mb = 1  # keep the RAM-headroom check a non-factor in tests
    return s


def _factory_and_closer():
    ids = itertools.count(1)
    closed: list[FakeSession] = []

    async def factory():
        return FakeSession(SessionSpec(account_id=f"acc-{next(ids)}"))

    async def closer(session):
        closed.append(session)

    return factory, closer, closed


@pytest.mark.asyncio
async def test_grows_up_to_max_contexts():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=3)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    ctxs = [await pool.acquire() for _ in range(3)]
    assert len({c.id for c in ctxs}) == 3
    assert pool.stats()["active"] == 3
    await pool.close()


@pytest.mark.asyncio
async def test_backpressure_at_max_queue_and_capacity():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=1, max_queue=0)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    await pool.acquire()  # fills the only slot
    with pytest.raises(Backpressure):
        await pool.acquire(timeout=0.05)
    await pool.close()


@pytest.mark.asyncio
async def test_acquire_timeout_raises_backpressure_and_unblocks_queue():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=1, max_queue=5)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    await pool.acquire()
    with pytest.raises(Backpressure):
        await pool.acquire(timeout=0.05)
    assert pool.stats()["pending"] == 0  # the waiter must clean itself up after timing out
    await pool.close()


@pytest.mark.asyncio
async def test_release_makes_context_idle_and_lru_reused():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=2)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    c1 = await pool.acquire()
    await pool.release(c1)
    c2 = await pool.acquire()
    assert c2.id == c1.id  # only idle context available: it must be reused, not regrown
    await pool.close()


@pytest.mark.asyncio
async def test_release_with_quarantine_then_reap_invokes_closer():
    factory, closer, closed = _factory_and_closer()
    settings = _settings(max_contexts=2)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    c1 = await pool.acquire()
    await pool.release(c1, quarantine="RateLimited")
    assert pool.stats()["quarantined"] == 1

    reaped = await pool.reap_quarantined()
    assert reaped == [(c1.id, c1.account_id, "RateLimited")]
    assert closed == [c1.session]
    assert pool.stats()["quarantined"] == 0
    await pool.close()


@pytest.mark.asyncio
async def test_scale_down_closes_idle_past_ttl_keeping_min_warm():
    factory, closer, closed = _factory_and_closer()
    settings = _settings(max_contexts=5, idle_ttl=0.0, min_warm=1)
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)

    c1 = await pool.acquire()
    c2 = await pool.acquire()
    await pool.release(c1)
    await pool.release(c2)
    await asyncio.sleep(0.01)  # ensure idle_seconds >= idle_ttl_seconds (0.0)

    n_closed = await pool.scale_down()
    assert n_closed == 1  # keeps min_warm=1 idle, closes the rest
    assert pool.stats()["idle"] == 1
    await pool.close()


@pytest.mark.asyncio
async def test_acquire_on_closed_pool_raises():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings()
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10_000)
    await pool.close()
    with pytest.raises(PoolClosed):
        await pool.acquire()


@pytest.mark.asyncio
async def test_capacity_provider_caps_growth_below_max_contexts():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=5)
    pool = ContextPool(
        settings, factory, closer=closer, capacity_provider=lambda: 1, mem_available_mb=lambda: 10_000
    )
    await pool.acquire()
    assert not await pool.can_grow()  # account capacity (1) already exhausted
    await pool.close()


@pytest.mark.asyncio
async def test_mem_headroom_blocks_growth():
    factory, closer, _closed = _factory_and_closer()
    settings = _settings(max_contexts=5)
    settings.scaling.per_context_mem_mb = 1000
    settings.scaling.mem_safety_factor = 1.0
    pool = ContextPool(settings, factory, closer=closer, mem_available_mb=lambda: 10)  # far too little
    assert not await pool.can_grow()
    await pool.close()
