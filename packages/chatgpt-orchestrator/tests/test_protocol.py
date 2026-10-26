"""Unit tests for the worker-side ReAct protocol: tool-call parsing across formats, the repair
path, strip_final, build_priming, and a full run_react loop against a fake worker."""

from __future__ import annotations

import pytest

from orchestrator.config import AgentSettings
from orchestrator.workers.protocol import (
    ToolCall,
    build_priming,
    looks_like_tool_attempt,
    parse_tool_call,
    run_react,
    strip_final,
)


def test_parse_tool_call_fenced_tool_call_tag():
    text = '```tool_call\n{"tool": "fetch_url", "args": {"url": "http://x"}}\n```'
    call = parse_tool_call(text)
    assert call == ToolCall(tool="fetch_url", args={"url": "http://x"})


def test_parse_tool_call_fenced_json_tag():
    text = '```json\n{"tool": "search", "args": {"q": "cats"}}\n```'
    call = parse_tool_call(text)
    assert call == ToolCall(tool="search", args={"q": "cats"})


def test_parse_tool_call_bare_object():
    text = 'Sure, calling it now: {"tool": "search", "args": {}} done'
    call = parse_tool_call(text)
    assert call == ToolCall(tool="search", args={})


def test_parse_tool_call_missing_args_defaults_to_empty_dict():
    call = parse_tool_call('{"tool": "ping"}')
    assert call == ToolCall(tool="ping", args={})


def test_parse_tool_call_final_answer_returns_none():
    assert parse_tool_call("The answer is 42.") is None


def test_parse_tool_call_malformed_json_returns_none():
    assert parse_tool_call("```tool_call\n{not valid json}\n```") is None


def test_parse_tool_call_empty_text():
    assert parse_tool_call("") is None


def test_looks_like_tool_attempt_true_for_malformed_attempt():
    assert looks_like_tool_attempt('I will use tool_call: {"tool": "x", bad}') is True


def test_looks_like_tool_attempt_false_for_plain_text():
    assert looks_like_tool_attempt("Just a normal final answer.") is False


def test_looks_like_tool_attempt_false_for_valid_call():
    text = '```tool_call\n{"tool": "x", "args": {}}\n```'
    assert looks_like_tool_attempt(text) is False  # a valid call isn't a "malformed attempt"


def test_strip_final_removes_final_fence():
    text = "preamble\n```final\nThe answer is 42.\n```"
    assert strip_final(text) == "The answer is 42."


def test_strip_final_passthrough_when_no_fence():
    assert strip_final("  plain answer  ") == "plain answer"


def test_build_priming_lists_tools_and_contract():
    catalog = [{"name": "fetch_url", "description": "Fetch a URL.", "input_schema": {"properties": {"url": {}}}}]
    priming = build_priming(catalog)
    assert "tool_call" in priming
    assert "fetch_url(url): Fetch a URL." in priming


class _FakeWorker:
    def __init__(self, tool_host, scripted_replies):
        self.id = "w-test"
        self.tool_host = tool_host
        self._replies = list(scripted_replies)
        self.sent: list[str] = []

    async def send(self, text: str) -> str:
        self.sent.append(text)
        return self._replies.pop(0)


class _FakeToolHost:
    def __init__(self, catalog, results):
        self._catalog = catalog
        self._results = results
        self.calls: list[tuple[str, dict]] = []

    def catalog(self):
        return self._catalog

    async def call(self, name, args):
        self.calls.append((name, args))
        if name not in self._results:
            raise KeyError(name)
        return self._results[name]


@pytest.mark.asyncio
async def test_run_react_single_tool_call_then_final_answer():
    catalog = [{"name": "fetch_url", "description": "fetch", "input_schema": {}}]
    host = _FakeToolHost(catalog, {"fetch_url": "page contents"})
    worker = _FakeWorker(
        host,
        [
            '```tool_call\n{"tool": "fetch_url", "args": {"url": "http://x"}}\n```',
            "The page says: page contents",
        ],
    )
    settings = AgentSettings(max_steps=6, repair_attempts=2)
    result = await run_react(worker, "what's on http://x?", settings)
    assert result == "The page says: page contents"
    assert host.calls == [("fetch_url", {"url": "http://x"})]


@pytest.mark.asyncio
async def test_run_react_repairs_malformed_call_then_succeeds():
    catalog = [{"name": "fetch_url", "description": "fetch", "input_schema": {}}]
    host = _FakeToolHost(catalog, {"fetch_url": "ok"})
    worker = _FakeWorker(
        host,
        [
            'I will use tool_call: {"tool": "fetch_url" bad json}',  # malformed -> triggers repair
            '```tool_call\n{"tool": "fetch_url", "args": {}}\n```',
            "Final answer.",
        ],
    )
    settings = AgentSettings(max_steps=6, repair_attempts=2)
    result = await run_react(worker, "go", settings)
    assert result == "Final answer."
    assert len(worker.sent) == 3  # priming+question, repair nudge, tool result


@pytest.mark.asyncio
async def test_run_react_returns_final_answer_immediately_if_no_tool_call():
    worker = _FakeWorker(_FakeToolHost([], {}), ["Just answering directly, no tools needed."])
    settings = AgentSettings()
    result = await run_react(worker, "hi", settings)
    assert result == "Just answering directly, no tools needed."


@pytest.mark.asyncio
async def test_run_react_stops_at_max_steps():
    catalog = [{"name": "loop_tool", "description": "loops", "input_schema": {}}]
    host = _FakeToolHost(catalog, {"loop_tool": "again"})
    # Always emits another tool call, never a final answer -> must stop at max_steps.
    call_text = '```tool_call\n{"tool": "loop_tool", "args": {}}\n```'
    worker = _FakeWorker(host, [call_text] * 10)
    settings = AgentSettings(max_steps=3, repair_attempts=0)
    result = await run_react(worker, "loop forever", settings)
    # Loop is cut off after max_steps; the return value is strip_final() of whatever the worker
    # last replied (still an unresolved tool_call fence here, since it never gave a final answer).
    assert result == call_text
    assert len(host.calls) == 3
