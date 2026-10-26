"""Unit tests for await_completion's streaming-settle state machine, via a virtual clock and
scripted probes (no Playwright involved)."""

from __future__ import annotations

import pytest

from orchestrator.chatgpt.completion import await_completion
from orchestrator.chatgpt.exceptions import CompletionTimeout

from .fakes import Clock


def _script(clock: Clock, events: list[tuple[float, bool, str]]):
    """events: list of (at_seconds, is_generating, text) — the state effective from that time on."""
    state = {"generating": events[0][1], "text": events[0][2]}
    idx = {"i": 0}

    async def sleep(seconds: float) -> None:
        clock.advance(seconds)
        while idx["i"] + 1 < len(events) and clock.now >= events[idx["i"] + 1][0]:
            idx["i"] += 1
            state["generating"] = events[idx["i"]][1]
            state["text"] = events[idx["i"]][2]

    async def is_generating() -> bool:
        return state["generating"]

    async def current_text() -> str:
        return state["text"]

    return is_generating, current_text, sleep


@pytest.mark.asyncio
async def test_fast_cached_reply_settles_quickly():
    clock = Clock()
    # Text appears immediately and never changes again -> should settle once settle_ms elapses.
    is_gen, cur_text, sleep = _script(clock, [(0.0, False, "hello")])
    reply = await await_completion(
        is_gen, cur_text, timeout=10.0, settle_ms=500, poll_ms=100, clock=clock, sleep=sleep
    )
    assert reply == "hello"


@pytest.mark.asyncio
async def test_streaming_then_settle():
    clock = Clock()
    events = [
        (0.0, True, ""),  # starts generating, no text yet
        (0.3, True, "Hel"),
        (0.6, True, "Hello wor"),
        (0.9, False, "Hello world"),  # stop button gone; text stable from here
    ]
    is_gen, cur_text, sleep = _script(clock, events)
    reply = await await_completion(
        is_gen, cur_text, timeout=10.0, settle_ms=500, poll_ms=100, clock=clock, sleep=sleep
    )
    assert reply == "Hello world"


@pytest.mark.asyncio
async def test_timeout_raises_with_partial_text():
    clock = Clock()
    # Perpetually generating -> never settles -> must time out with whatever text we last saw.
    is_gen, cur_text, sleep = _script(clock, [(0.0, True, "still going")])
    with pytest.raises(CompletionTimeout) as excinfo:
        await await_completion(
            is_gen, cur_text, timeout=1.0, settle_ms=500, poll_ms=100, clock=clock, sleep=sleep
        )
    assert excinfo.value.partial == "still going"


@pytest.mark.asyncio
async def test_never_starts_within_grace_then_settles_on_nothing():
    clock = Clock()
    # Nothing ever happens: is_generating always False, text never changes from the initial value.
    is_gen, cur_text, sleep = _script(clock, [(0.0, False, "")])
    with pytest.raises(CompletionTimeout):
        await await_completion(
            is_gen,
            cur_text,
            timeout=0.5,
            settle_ms=100,
            poll_ms=50,
            start_grace_ms=200,
            clock=clock,
            sleep=sleep,
        )
