"""Guarded live smoke test: one real anonymous round-trip through chatgpt.com.

Opt-in only — skipped unless ORCH_LIVE=1. This is the **M1 feasibility gate**: it automates the
free ChatGPT web UI (against OpenAI's usage policies — a deliberate choice documented in
docs/PLAN.md's Risks section) and needs a real network egress plus a passing Cloudflare check to
succeed. Never run automatically in CI.

Run explicitly:

    ORCH_LIVE=1 uv run pytest -q -m live tests/test_live_smoke.py -s
"""

from __future__ import annotations

import os

import pytest

from orchestrator.config import Settings
from orchestrator.server.core import OrchestratorCore

pytestmark = pytest.mark.live


@pytest.mark.skipif(
    os.environ.get("ORCH_LIVE") != "1", reason="set ORCH_LIVE=1 to run live smoke tests"
)
@pytest.mark.asyncio
async def test_anonymous_round_trip_against_real_chatgpt():
    settings = Settings()
    async with OrchestratorCore(settings) as core:
        wid = await core.spawn_worker()
        try:
            reply = await core.ask(wid, "Say hi in 3 words.")
        finally:
            await core.close_worker(wid)
    assert reply.strip()
    print(f"live reply: {reply!r}")
