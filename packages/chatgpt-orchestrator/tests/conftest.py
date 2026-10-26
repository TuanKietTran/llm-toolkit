"""Shared pytest fixtures: tiny-knob Settings, and an OrchestratorCore wired to fakes."""

from __future__ import annotations

import pytest

from orchestrator.chatgpt import driver as driver_mod
from orchestrator.config import AccountConfig, Settings
from orchestrator.server.core import OrchestratorCore

from .fakes import FakeSession


@pytest.fixture
def tiny_settings() -> Settings:
    """A Settings instance with fast scaling/budget knobs so tests don't wait on real timers."""
    settings = Settings(
        accounts=[
            AccountConfig(id="a1", mode="anonymous", cooldown_seconds=5.0),
            AccountConfig(id="a2", mode="anonymous", cooldown_seconds=5.0),
            AccountConfig(id="a3", mode="anonymous", cooldown_seconds=5.0),
        ],
    )
    settings.scaling.min_warm = 0
    settings.scaling.max_contexts = 10
    settings.scaling.scale_tick_seconds = 0.05
    settings.scaling.scale_action_cooldown_seconds = 0.0
    settings.scaling.idle_ttl_seconds = 0.05
    return settings


@pytest.fixture
def fake_core(tiny_settings, monkeypatch):
    """An OrchestratorCore wired to FakeSession/ChatGPTDriver — no real browser, no network.

    Baseline driver behavior just echoes ``reply:<prompt>``; tests that need specific failures
    can further ``monkeypatch.setattr(driver_mod.ChatGPTDriver, "ask", ...)`` after requesting
    this fixture.
    """
    core = OrchestratorCore(tiny_settings)

    async def fake_start(*, enable_context_proxies=False):
        pass

    async def fake_new_session(spec):
        return FakeSession(spec)

    async def fake_close():
        pass

    core.browser.start = fake_start
    core.browser.new_session = fake_new_session
    core.browser.close = fake_close

    async def fake_open(self):
        return None

    async def fake_ask(self, prompt, *, timeout=None):
        return f"reply:{prompt}"

    monkeypatch.setattr(driver_mod.ChatGPTDriver, "open", fake_open)
    monkeypatch.setattr(driver_mod.ChatGPTDriver, "ask", fake_ask)

    return core
