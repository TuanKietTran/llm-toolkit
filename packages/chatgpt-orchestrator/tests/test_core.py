"""Unit tests for OrchestratorCore against the fake-backed core fixture (no real browser/network).

Covers the spawn -> ask -> ask_batch -> close lifecycle and the failure path: a raised ChatGPTError
must quarantine the context, cool the account down, and drop the worker (never double-releasing
the account).
"""

from __future__ import annotations

import pytest

from orchestrator.chatgpt import driver as driver_mod
from orchestrator.chatgpt.exceptions import RateLimited
from orchestrator.server.core import WorkerNotFound


@pytest.mark.asyncio
async def test_start_wires_pool_and_autoscaler_then_close(fake_core):
    await fake_core.start()
    assert fake_core.pool is not None
    assert fake_core.autoscaler is not None
    await fake_core.close()


@pytest.mark.asyncio
async def test_spawn_ask_close_round_trip(fake_core):
    async with fake_core as core:
        wid = await core.spawn_worker()
        reply = await core.ask(wid, "hello")
        assert reply == "reply:hello"
        await core.close_worker(wid)
        assert wid not in core._workers


@pytest.mark.asyncio
async def test_converse_reuses_same_worker_context(fake_core):
    async with fake_core as core:
        wid = await core.spawn_worker()
        ctx_id_before = core._workers[wid].ctx.id
        await core.converse(wid, "turn 1")
        await core.converse(wid, "turn 2")
        assert core._workers[wid].ctx.id == ctx_id_before  # same context, no re-spawn
        await core.close_worker(wid)


@pytest.mark.asyncio
async def test_ask_batch_all_succeed(fake_core):
    async with fake_core as core:
        results = await core.ask_batch(["p1", "p2", "p3"], n_parallel=3)
        assert [r["ok"] for r in results] == [True, True, True]
        assert {r["reply"] for r in results} == {"reply:p1", "reply:p2", "reply:p3"}
        # every spawned worker must be cleaned up by ask_batch's finally block
        assert core.list_workers() == []


@pytest.mark.asyncio
async def test_ask_batch_isolates_a_single_failure(fake_core, monkeypatch):
    async def flaky_ask(self, prompt, *, timeout=None):
        if prompt == "bad":
            raise RateLimited("nope")
        return f"reply:{prompt}"

    monkeypatch.setattr(driver_mod.ChatGPTDriver, "ask", flaky_ask)

    async with fake_core as core:
        results = await core.ask_batch(["good1", "bad", "good2"], n_parallel=3)
        by_ok = {r["ok"] for r in results}
        assert by_ok == {True, False}
        failed = [r for r in results if not r["ok"]]
        assert "RateLimited" in failed[0]["error"]


@pytest.mark.asyncio
async def test_rate_limited_quarantines_context_and_cools_account(fake_core, monkeypatch):
    async def failing_ask(self, prompt, *, timeout=None):
        raise RateLimited("hit limit")

    monkeypatch.setattr(driver_mod.ChatGPTDriver, "ask", failing_ask)

    async with fake_core as core:
        capacity_before = core.accounts.capacity()
        wid = await core.spawn_worker()
        with pytest.raises(RateLimited):
            await core.ask(wid, "anything")
        assert wid not in core._workers  # failed worker is dropped
        capacity_after = core.accounts.capacity()
        assert capacity_after == capacity_before - 1  # exactly one account now cooling (no double count)


@pytest.mark.asyncio
async def test_ask_on_unknown_worker_raises(fake_core):
    async with fake_core as core:
        with pytest.raises(WorkerNotFound):
            await core.ask("w-does-not-exist", "hi")


@pytest.mark.asyncio
async def test_close_worker_on_unknown_worker_raises(fake_core):
    async with fake_core as core:
        with pytest.raises(WorkerNotFound):
            await core.close_worker("w-does-not-exist")


@pytest.mark.asyncio
async def test_pool_status_and_accounts_status_report(fake_core):
    async with fake_core as core:
        wid = await core.spawn_worker()
        pool_status = core.pool_status()
        assert pool_status["busy"] == 1
        assert pool_status["workers"] == 1

        accounts_status = core.accounts_status()
        assert accounts_status["capacity"] == 3  # 1 in-use + 2 free, all 3 accounts still healthy
        await core.close_worker(wid)


@pytest.mark.asyncio
async def test_list_workers_reports_expected_shape(fake_core):
    async with fake_core as core:
        wid = await core.spawn_worker()
        listed = core.list_workers()
        assert listed == [
            {"id": wid, "account_id": listed[0]["account_id"], "messages_sent": 0, "equipped": False}
        ]
        await core.close_worker(wid)
