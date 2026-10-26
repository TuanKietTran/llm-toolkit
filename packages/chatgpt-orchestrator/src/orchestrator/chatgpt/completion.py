"""Streaming-completion detection.

Deciding when a ChatGPT reply is "done" is the trickiest part of scraping. We combine two signals:
  * the send↔stop button transition (stop button present ⇒ generating), and
  * DOM settle (the assistant text stops changing for ``settle_ms``).

This is written against two injected async probes (``is_generating`` / ``current_text``) rather than
a Playwright page, so the state machine is deterministic and unit-testable. The driver supplies the
probes backed by real selectors.
"""

from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable

from .exceptions import CompletionTimeout

IsGenerating = Callable[[], Awaitable[bool]]
CurrentText = Callable[[], Awaitable[str]]


async def await_completion(
    is_generating: IsGenerating,
    current_text: CurrentText,
    *,
    timeout: float = 120.0,
    settle_ms: float = 700.0,
    poll_ms: float = 120.0,
    start_grace_ms: float = 8000.0,
    clock: Callable[[], float] = time.monotonic,
    sleep: Callable[[float], Awaitable[None]] = asyncio.sleep,
) -> str:
    """Return the final assistant text once streaming settles, or raise CompletionTimeout.

    ``start_grace_ms`` bounds how long we wait for generation to *begin* (button flip or first text).
    ``settle_ms`` is how long the text must be unchanged (with no stop button) to count as done.
    """
    start = clock()
    last_text = await current_text()
    last_change = start
    started = False

    # Phase 1: wait for the reply to begin (button flips to stop, or text starts changing).
    while (clock() - start) * 1000.0 < start_grace_ms:
        if await is_generating():
            started = True
            break
        text = await current_text()
        if text != last_text:
            started = True
            last_text = text
            last_change = clock()
            break
        await sleep(poll_ms / 1000.0)

    # Phase 2: wait for it to finish and settle.
    while True:
        now = clock()
        if (now - start) >= timeout:
            raise CompletionTimeout(partial=last_text or None)

        generating = await is_generating()
        text = await current_text()
        if text != last_text:
            last_text = text
            last_change = now

        settled = (now - last_change) * 1000.0 >= settle_ms
        if not generating and settled and (started or last_text):
            return last_text

        await sleep(poll_ms / 1000.0)


__all__ = ["await_completion"]
