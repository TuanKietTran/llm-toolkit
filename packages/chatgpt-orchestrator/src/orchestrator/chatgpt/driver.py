"""ChatGPT web driver: open → (dismiss modals / guard state) → send → await completion → scrape.

Operates on a page from a :class:`BrowserSession`. Every brittle selector comes from
``selectors.py``; completion detection from ``completion.py``. On unexpected DOM/timeouts it saves a
failure snapshot and raises a typed error (:mod:`.exceptions`) so the pool/account manager can
quarantine the context or cool down the account.
"""

from __future__ import annotations

import asyncio
import base64
import time
from typing import TYPE_CHECKING

from ..browser.session import BrowserSession
from ..browser.stealth import human_type, think
from ..config import Settings
from ..telemetry import get_logger, get_metrics, save_failure_snapshot
from . import selectors as sel
from .completion import await_completion
from .exceptions import (
    ChallengePresented,
    ChatGPTError,
    CompletionTimeout,
    DomChanged,
    EmptyReply,
    LoginRequired,
    RateLimited,
)

if TYPE_CHECKING:
    from playwright.async_api import Locator, Page

log = get_logger(__name__)
metrics = get_metrics()


class ChatGPTDriver:
    """Drives one ChatGPT conversation in one isolated context."""

    def __init__(self, session: BrowserSession, settings: Settings) -> None:
        self.session = session
        self.settings = settings
        self._behavior = settings.behavior
        self._base_url = settings.browser.base_url
        self._nav_timeout = settings.browser.nav_timeout_ms
        self._last_send = 0.0

    # ---- low-level helpers -----------------------------------------------------------

    async def _page(self) -> "Page":
        return await self.session.page()

    async def _first_visible(self, candidates: list[str]) -> "Locator | None":
        page = await self._page()
        for css in candidates:
            try:
                loc = page.locator(css).first
                if await loc.count() > 0 and await loc.is_visible():
                    return loc
            except Exception:  # noqa: BLE001 - invalid/again selectors are skipped
                continue
        return None

    async def _page_text(self) -> str:
        page = await self._page()
        try:
            body = page.locator("body")
            return (await body.inner_text()).lower()
        except Exception:  # noqa: BLE001
            return (await page.content()).lower()

    async def _snapshot(self, reason: str) -> None:
        page = await self._page()
        html = None
        shot = None
        try:
            html = await page.content()
        except Exception:  # noqa: BLE001
            pass
        try:
            shot = await page.screenshot()
        except Exception:  # noqa: BLE001
            pass
        save_failure_snapshot(
            reason,
            root=str(self.settings.snapshot_dir),
            html=html,
            screenshot=shot,
            url=page.url,
            meta={"account": self.session.spec.account_id},
        )

    async def read_context(self, *, include_screenshot: bool = False) -> dict:
        """On-demand page introspection for a headless worker: visible text, url, optional shot.

        Unlike ``_snapshot`` this is not failure-triggered and never writes to disk -- it is for
        the driving agent to check what a headless worker is actually looking at (there is no
        visible window to eyeball with ``browser.headless=true``), e.g. mid-run after a
        suspicious reply or before trusting a scrape.
        """
        page = await self._page()
        try:
            text = (await page.locator("body").inner_text()).strip()
        except Exception:  # noqa: BLE001
            try:
                text = await page.content()
            except Exception:  # noqa: BLE001
                text = ""
        result: dict = {"url": page.url, "text": text[:8000]}
        if include_screenshot:
            try:
                shot = await page.screenshot()
                result["screenshot_b64"] = base64.b64encode(shot).decode("ascii")
            except Exception:  # noqa: BLE001
                result["screenshot_b64"] = None
        return result

    # ---- state detection -------------------------------------------------------------

    async def guard_state(self) -> None:
        """Raise a typed error if the page is a challenge / rate-limit / login wall.

        chatgpt.com's anonymous UI always renders a "Log in" button next to a usable composer,
        so the mere presence of a login *offer* is not a blocking wall. When
        ``browser.allow_anonymous`` is set (the default) we only treat login as blocking if the
        composer is genuinely absent — otherwise anonymous use would be rejected on every page.
        """
        if await self._first_visible(sel.CHALLENGE_FRAME) is not None:
            metrics.incr("chatgpt_challenge")
            raise ChallengePresented("cloudflare/turnstile challenge frame present")

        text = await self._page_text()
        if any(t in text for t in sel.CHALLENGE_TEXT):
            metrics.incr("chatgpt_challenge")
            raise ChallengePresented("challenge text present")
        if any(t in text for t in sel.RATE_LIMIT_TEXT):
            metrics.incr("chatgpt_rate_limited")
            raise RateLimited("rate-limit banner present")

        login_offer = await self._first_visible(sel.LOGIN_BUTTON) is not None or any(
            t in text for t in sel.LOGIN_TEXT
        )
        if not login_offer:
            return
        if self.settings.browser.allow_anonymous:
            # A login offer only blocks us if there is no composer to type into anyway.
            if await self._first_visible(sel.PROMPT_INPUT) is not None:
                return
        metrics.incr("chatgpt_login_required")
        raise LoginRequired("login wall present (no usable composer)")

    async def _await_composer_ready(self, timeout_s: float = 12.0) -> "Locator | None":
        """Wait for the *hydrated* composer to be interactive.

        Current chatgpt.com builds server-render a hidden fallback ``<textarea>`` (class
        ``…fallbackTextarea``) that a loose ``is_visible()`` accepts but that fails click/type
        actionability, then swap in the real ProseMirror composer once JS hydrates. Interacting
        before hydration is the DOM race that produced ``DomChanged: failed to send prompt``. Poll
        until we find an element that is genuinely editable and has a real on-screen box.
        """
        deadline = time.monotonic() + timeout_s
        while time.monotonic() < deadline:
            loc = await self._first_visible(sel.COMPOSER_READY)
            if loc is not None:
                try:
                    box = await loc.bounding_box()
                    if box and box["width"] > 1 and box["height"] > 1:
                        return loc
                except Exception:  # noqa: BLE001 - element churning during hydration
                    pass
            await asyncio.sleep(0.3)
        # Timed out waiting for the hydrated composer; fall back to any input so the caller can
        # snapshot/raise a meaningful DomChanged rather than us returning None prematurely.
        return await self._first_visible(sel.PROMPT_INPUT)

    async def _dismiss_modals(self) -> None:
        """Best-effort: dismiss consent / 'stay logged out' / intro modals."""
        for group in (sel.STAY_LOGGED_OUT, sel.DISMISS_BUTTONS):
            loc = await self._first_visible(group)
            if loc is not None:
                try:
                    await loc.click(timeout=2000)
                    await think(self._behavior)
                except Exception:  # noqa: BLE001
                    pass

    # ---- lifecycle -------------------------------------------------------------------

    async def open(self) -> None:
        """Navigate to ChatGPT, dismiss modals, and verify we have a usable composer."""
        page = await self._page()
        try:
            await page.goto(self._base_url, wait_until="domcontentloaded", timeout=self._nav_timeout)
        except Exception as exc:  # noqa: BLE001
            await self._snapshot("navigation_failed")
            raise ChatGPTError(f"navigation to {self._base_url} failed: {exc}") from exc

        await self._dismiss_modals()
        await self.guard_state()

        if await self._await_composer_ready() is None:
            await self._snapshot("composer_missing")
            raise DomChanged("prompt composer not found after open()")
        log.info("chatgpt_opened", account=self.session.spec.account_id, url=page.url)

    # ---- completion probes -----------------------------------------------------------

    async def _is_generating(self) -> bool:
        if await self._first_visible(sel.STOP_BUTTON) is not None:
            return True
        return await self._first_visible(sel.STREAMING_MARKER) is not None

    async def _latest_assistant_text(self) -> str:
        page = await self._page()
        for msg_css in sel.ASSISTANT_MESSAGE:
            msgs = page.locator(msg_css)
            try:
                n = await msgs.count()
            except Exception:  # noqa: BLE001
                continue
            if n == 0:
                continue
            last = msgs.nth(n - 1)
            for content_css in sel.ASSISTANT_CONTENT:
                content = last.locator(content_css)
                try:
                    if await content.count() > 0:
                        return (await content.first.inner_text()).strip()
                except Exception:  # noqa: BLE001
                    continue
            try:
                return (await last.inner_text()).strip()
            except Exception:  # noqa: BLE001
                continue
        return ""

    # ---- send ------------------------------------------------------------------------

    async def _respect_min_gap(self) -> None:
        gap = self._behavior.min_gap_between_messages_ms / 1000.0
        elapsed = time.monotonic() - self._last_send
        if elapsed < gap:
            await asyncio.sleep(gap - elapsed)

    async def _submit(self) -> None:
        page = await self._page()
        send = await self._first_visible(sel.SEND_BUTTON)
        if send is not None:
            await send.click()
        else:
            await page.keyboard.press("Enter")

    async def ask(self, prompt: str, *, timeout: float | None = None) -> str:
        """Send one prompt, wait for the streamed reply to settle, and return its text."""
        timeout = timeout if timeout is not None else self.settings.agent.ask_timeout_seconds
        await self._respect_min_gap()

        input_loc = await self._await_composer_ready()
        if input_loc is None:
            await self._snapshot("input_missing")
            raise DomChanged("prompt input not found")

        before = await self._latest_assistant_text()
        page = await self._page()
        try:
            await think(self._behavior)
            await human_type(page, input_loc, prompt, self._behavior)
            await think(self._behavior)
            await self._submit()
        except Exception as exc:  # noqa: BLE001
            await self._snapshot("send_failed")
            raise DomChanged(f"failed to send prompt: {exc}") from exc

        self._last_send = time.monotonic()

        async def current_text() -> str:
            text = await self._latest_assistant_text()
            # Ignore the pre-existing last message until a new/changed one appears.
            return text if text != before else ""

        try:
            with metrics.timer("chatgpt_ask_latency"):
                reply = await await_completion(
                    self._is_generating,
                    current_text,
                    timeout=timeout,
                    clock=time.monotonic,
                )
        except CompletionTimeout:
            # A stall often means a challenge/limit appeared mid-stream — classify it.
            try:
                await self.guard_state()
            except ChatGPTError:
                raise
            await self._snapshot("completion_timeout")
            raise

        if not reply:
            # Empty reply: if it's actually a challenge/rate-limit/login, guard_state raises the
            # precise error. Otherwise it's a soft-throttled empty completion — surface it as a
            # retryable EmptyReply rather than returning a bogus empty success.
            await self.guard_state()
            metrics.incr("chatgpt_empty_reply")
            await self._snapshot("empty_reply")
            raise EmptyReply("generation finished but produced no text")
        metrics.incr("chatgpt_replies")
        log.info(
            "chatgpt_reply",
            account=self.session.spec.account_id,
            prompt_chars=len(prompt),
            reply_chars=len(reply),
        )
        return reply


__all__ = ["ChatGPTDriver"]
