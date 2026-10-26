"""Unit tests for AccountManager: exclusive acquire, capacity accounting, penalize/release split,
retirement after repeated strikes, retry_after, and the synthesized default anonymous account."""

from __future__ import annotations

import pytest

from orchestrator.accounts.manager import AccountManager, NoAccountAvailable
from orchestrator.config import AccountConfig, Settings

from .fakes import Clock


def _settings(**account_kwargs) -> Settings:
    defaults = dict(budget_messages=8, budget_window_seconds=3600, cooldown_seconds=10.0)
    defaults.update(account_kwargs)
    return Settings(
        accounts=[
            AccountConfig(id="a1", mode="anonymous", **defaults),
            AccountConfig(id="a2", mode="anonymous", **defaults),
        ],
    )


def test_default_anon_account_synthesized_when_none_configured():
    mgr = AccountManager(Settings())
    assert mgr.capacity() == 1
    s = mgr.acquire()
    assert s.id == "anon-default"


def test_acquire_is_exclusive_no_double_booking():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    s1 = mgr.acquire()
    s2 = mgr.acquire()
    assert {s1.id, s2.id} == {"a1", "a2"}
    with pytest.raises(NoAccountAvailable):
        mgr.acquire()  # both identities are now in-flight


def test_capacity_is_in_use_plus_free():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    assert mgr.capacity() == 2  # both free
    s1 = mgr.acquire()
    assert mgr.capacity() == 2  # one in-use + one free = still 2
    mgr.release(s1)
    assert mgr.capacity() == 2  # back to both free


def test_release_frees_identity_without_cooldown():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    s = mgr.acquire()
    mgr.release(s, messages_sent=1)
    assert s.inflight == 0
    assert not s.cooldown.active()
    assert s.budget.used() == 1
    # freed identity is acquirable again
    reacquired = mgr.acquire(sticky=s.id)
    assert reacquired.id == s.id


def test_penalize_cools_down_the_account():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    s = mgr.get("a1")
    mgr.penalize(s, "RateLimited")
    mgr.release(s)
    assert s.cooldown.active()
    assert s.cooldown.remaining() == pytest.approx(10.0)
    assert mgr.capacity() == 1  # a1 cooling, a2 still free
    assert mgr.retry_after() == 0.0  # a2 is schedulable now, so no wait is needed fleet-wide


def test_retry_after_reflects_soonest_cooldown_when_all_are_cooling():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    for aid in ("a1", "a2"):
        mgr.penalize(mgr.get(aid), "RateLimited")
    ra = mgr.retry_after()
    assert ra is not None and ra == pytest.approx(10.0)


def test_penalize_retires_after_repeated_strikes():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    s = mgr.get("a1")
    for _ in range(5):  # _RETIRE_AFTER_CHALLENGES = 5
        mgr.penalize(s, "ChallengePresented")
    assert s.dead is True
    assert mgr.capacity() == 1  # only a2 counts now


def test_note_success_resets_strikes():
    clock = Clock()
    mgr = AccountManager(_settings(), clock=clock)
    s = mgr.get("a1")
    mgr.penalize(s, "RateLimited")
    mgr.penalize(s, "RateLimited")
    assert s.consecutive_challenges == 2
    mgr.note_success(s)
    assert s.consecutive_challenges == 0


def test_retry_after_zero_when_something_schedulable():
    mgr = AccountManager(_settings())
    assert mgr.retry_after() == 0.0


def test_retry_after_none_when_all_dead():
    mgr = AccountManager(_settings())
    for aid in ("a1", "a2"):
        s = mgr.get(aid)
        for _ in range(5):
            mgr.penalize(s, "ChallengePresented")
    assert mgr.retry_after() is None
