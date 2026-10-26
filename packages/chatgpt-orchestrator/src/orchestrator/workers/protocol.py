"""Worker-side ReAct protocol (optional).

Most flows: the *connected agent* (Claude Code / pi) does the reasoning and a worker just answers a
prompt. But a worker can be *equipped* with inward MCP tools; then it needs a minimal, robust
tool-use loop driven purely through the ChatGPT chat surface. We prime the worker with a tool
catalog and a strict fenced-JSON contract, parse tolerantly, and nudge on malformed output.

Parsing (:func:`parse_tool_call`) and priming (:func:`build_priming`) are pure and unit-testable.
"""

from __future__ import annotations

import json
import re
from dataclasses import dataclass
from typing import TYPE_CHECKING

from ..telemetry import get_logger

if TYPE_CHECKING:
    from ..config import AgentSettings

log = get_logger(__name__)

_FENCE_RE = re.compile(r"```(?P<tag>[a-zA-Z_]*)\s*\n(?P<body>.*?)```", re.DOTALL)
_OBJ_RE = re.compile(r"\{.*\}", re.DOTALL)


@dataclass(slots=True)
class ToolCall:
    tool: str
    args: dict


def build_priming(catalog: list[dict]) -> str:
    """Build the system priming text: the tool catalog + the strict tool-call contract."""
    lines = [
        "You have tools. To use one, reply with ONLY a fenced block tagged tool_call:",
        "```tool_call",
        '{"tool": "<name>", "args": { ... }}',
        "```",
        "Use exactly one tool call per reply. After you receive the tool result, either call another "
        "tool or give your FINAL answer as plain text (no fences). Do not explain the JSON.",
        "",
        "Available tools:",
    ]
    for spec in catalog:
        schema = spec.get("input_schema") or spec.get("inputSchema") or {}
        props = ", ".join((schema.get("properties") or {}).keys())
        lines.append(f"- {spec['name']}({props}): {spec.get('description', '').strip()}")
    return "\n".join(lines)


def _coerce_call(obj: object) -> ToolCall | None:
    if isinstance(obj, dict) and isinstance(obj.get("tool"), str):
        args = obj.get("args")
        if not isinstance(args, dict):
            args = {}
        return ToolCall(tool=obj["tool"], args=args)
    return None


def parse_tool_call(text: str) -> ToolCall | None:
    """Extract a tool call from a worker reply, or None if it's a final answer.

    Accepts: a ```tool_call fence, any fenced JSON with a "tool" key, or a bare JSON object.
    """
    if not text:
        return None
    # 1) Prefer an explicit tool_call fence.
    for m in _FENCE_RE.finditer(text):
        body = m.group("body").strip()
        if m.group("tag").lower() in ("tool_call", "json", ""):
            try:
                call = _coerce_call(json.loads(body))
            except json.JSONDecodeError:
                call = None
            if call is not None:
                return call
    # 2) Bare JSON object containing a "tool" key.
    m = _OBJ_RE.search(text)
    if m:
        try:
            return _coerce_call(json.loads(m.group(0)))
        except json.JSONDecodeError:
            return None
    return None


def looks_like_tool_attempt(text: str) -> bool:
    """Heuristic: the worker seemed to *try* a tool call but produced malformed output."""
    lowered = text.lower()
    return ("tool_call" in lowered or '"tool"' in lowered) and parse_tool_call(text) is None


def strip_final(text: str) -> str:
    """Remove a stray ```final fence if the worker wrapped its answer."""
    for m in _FENCE_RE.finditer(text):
        if m.group("tag").lower() == "final":
            return m.group("body").strip()
    return text.strip()


_REPAIR_NUDGE = (
    "That was not a valid tool call. Reply with ONLY a fenced ```tool_call block containing "
    'valid JSON {"tool": "...", "args": {...}}, or give your final answer as plain text.'
)


async def run_react(worker, question: str, settings: "AgentSettings") -> str:
    """Drive an equipped worker through a bounded ReAct loop; return the final answer text."""
    catalog = worker.tool_host.catalog()
    priming = build_priming(catalog)
    reply = await worker.send(f"{priming}\n\nUser request:\n{question}")

    steps = 0
    repairs = 0
    while steps < settings.max_steps:
        call = parse_tool_call(reply)
        if call is None:
            if looks_like_tool_attempt(reply) and repairs < settings.repair_attempts:
                repairs += 1
                log.info("react_repair", worker=getattr(worker, "id", "?"), attempt=repairs)
                reply = await worker.send(_REPAIR_NUDGE)
                continue
            return strip_final(reply)

        steps += 1
        try:
            result = await worker.tool_host.call(call.tool, call.args)
        except Exception as exc:  # noqa: BLE001
            result = f"ERROR calling {call.tool}: {exc}"
        log.info("react_tool_call", worker=getattr(worker, "id", "?"), tool=call.tool, step=steps)
        reply = await worker.send(
            f"Tool `{call.tool}` returned:\n{result}\n\n"
            "Continue with another tool call, or give your final answer as plain text."
        )

    return strip_final(reply)


__all__ = [
    "ToolCall",
    "build_priming",
    "looks_like_tool_attempt",
    "parse_tool_call",
    "run_react",
    "strip_final",
]
