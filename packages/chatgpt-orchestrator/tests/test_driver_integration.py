"""CI-safe integration test: drives the real ChatGPTDriver (Playwright/Patchright + selectors.py +
completion.py) against a local stub HTML page that mimics just enough of chatgpt.com's DOM. No
network calls to chatgpt.com — this validates the actual driver wiring deterministically, unlike
the fake-driver tests elsewhere in this suite.

Skips (does not fail) if no browser binary is installed, since browser install is a separate
prerequisite (`uv run patchright install chromium` / `playwright install chromium`) — the rest of
the suite must stay green either way.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from orchestrator.browser.manager import BrowserLaunchError, BrowserManager
from orchestrator.browser.session import SessionSpec
from orchestrator.chatgpt.driver import ChatGPTDriver
from orchestrator.config import Settings

_STUB_URL = (Path(__file__).parent / "fixtures" / "stub_chatgpt.html").as_uri()


def _fast_settings() -> Settings:
    """Real driver code paths, but with human-pacing knobs turned down so the test stays quick."""
    settings = Settings()
    settings.browser.headless = True
    settings.behavior.keystroke_min_ms = 1
    settings.behavior.keystroke_max_ms = 3
    settings.behavior.think_min_ms = 1
    settings.behavior.think_max_ms = 5
    settings.behavior.min_gap_between_messages_ms = 10
    return settings


@pytest.fixture
async def browser_manager():
    settings = _fast_settings()
    mgr = BrowserManager(settings)
    try:
        await mgr.start()
    except BrowserLaunchError as exc:
        pytest.skip(f"no browser binary installed: {exc}")
    yield mgr
    await mgr.close()


@pytest.mark.asyncio
async def test_driver_ask_against_stub_dom(browser_manager):
    session = await browser_manager.new_session(SessionSpec(account_id="stub-test"))
    try:
        page = await session.page()
        await page.goto(_STUB_URL)

        driver = ChatGPTDriver(session, _fast_settings())
        await driver.guard_state()  # must not raise: no challenge/rate-limit/login text present

        reply = await driver.ask("hello stub", timeout=10.0)
        assert reply == "Hello from stub reply #1"
    finally:
        await session.close()


@pytest.mark.asyncio
async def test_driver_ask_twice_advances_the_conversation(browser_manager):
    session = await browser_manager.new_session(SessionSpec(account_id="stub-test-2"))
    try:
        page = await session.page()
        await page.goto(_STUB_URL)
        driver = ChatGPTDriver(session, _fast_settings())

        first = await driver.ask("one", timeout=10.0)
        second = await driver.ask("two", timeout=10.0)
        assert first == "Hello from stub reply #1"
        assert second == "Hello from stub reply #2"  # scrape picks up the *latest* assistant message
    finally:
        await session.close()
