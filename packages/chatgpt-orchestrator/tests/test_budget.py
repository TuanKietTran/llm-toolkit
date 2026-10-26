"""Unit tests for RollingBudget (sliding-window rate limit) and Cooldown, via an injected clock."""

from __future__ import annotations

from orchestrator.accounts.budget import Cooldown, RollingBudget

from .fakes import Clock


def test_rolling_budget_consume_and_remaining():
    clock = Clock()
    budget = RollingBudget(limit=3, window_seconds=100, clock=clock)
    assert budget.available()
    assert budget.remaining() == 3

    budget.consume(2)
    assert budget.used() == 2
    assert budget.remaining() == 1
    assert budget.available()

    budget.consume(1)
    assert budget.remaining() == 0
    assert not budget.available()


def test_rolling_budget_prunes_old_events():
    clock = Clock()
    budget = RollingBudget(limit=2, window_seconds=10, clock=clock)
    budget.consume(2)
    assert not budget.available()

    clock.advance(11)  # past the window: old events should prune away
    assert budget.available()
    assert budget.used() == 0


def test_rolling_budget_retry_after():
    clock = Clock()
    budget = RollingBudget(limit=1, window_seconds=10, clock=clock)
    assert budget.retry_after() == 0.0  # slot free, no wait

    budget.consume(1)
    assert budget.retry_after() == 10.0  # exactly one window until the slot frees

    clock.advance(4)
    assert budget.retry_after() == 6.0

    clock.advance(6)
    assert budget.retry_after() == 0.0
    assert budget.available()


def test_cooldown_trigger_and_clear():
    clock = Clock()
    cooldown = Cooldown(clock=clock)
    assert not cooldown.active()
    assert cooldown.remaining() == 0.0

    cooldown.trigger(30.0)
    assert cooldown.active()
    assert cooldown.remaining() == 30.0

    clock.advance(10)
    assert cooldown.active()
    assert cooldown.remaining() == 20.0

    clock.advance(20)
    assert not cooldown.active()
    assert cooldown.remaining() == 0.0


def test_cooldown_trigger_only_extends():
    clock = Clock()
    cooldown = Cooldown(clock=clock)
    cooldown.trigger(30.0)
    cooldown.trigger(10.0)  # a shorter trigger must not shorten an existing cooldown
    assert cooldown.remaining() == 30.0

    cooldown.clear()
    assert not cooldown.active()


def test_cooldown_clear():
    clock = Clock()
    cooldown = Cooldown(clock=clock)
    cooldown.trigger(100.0)
    cooldown.clear()
    assert not cooldown.active()
    assert cooldown.remaining() == 0.0
