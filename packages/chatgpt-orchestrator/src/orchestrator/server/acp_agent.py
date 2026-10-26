"""ACP (Agent Client Protocol) adapter: presents the orchestrator as a JSON-RPC 2.0 agent over
stdio, for ACP-speaking editors (e.g. Zed). A thin shim over :class:`OrchestratorCore` — every ACP
session maps 1:1 onto a :class:`~.worker.Worker`, and every prompt turn calls ``core.converse``.
Reuses the same engine as the MCP server; this is a secondary connection surface.

Framing: one JSON object per line on stdin/stdout (the ACP spec uses newline-delimited JSON-RPC,
not LSP-style Content-Length headers). :meth:`AcpAgent.handle_message` is pure/testable — it takes
and returns plain dicts with no stdio involved; :func:`run_acp_stdio` wires it to real stdio.
"""

from __future__ import annotations

import asyncio
import itertools
import json
import sys
from typing import Any

from ..telemetry import get_logger
from .core import OrchestratorCore, WorkerNotFound

log = get_logger(__name__)

PROTOCOL_VERSION = 1


def _text_of(prompt: list[dict[str, Any]]) -> str:
    """Concatenate the text content blocks of an ACP prompt into one string."""
    return "\n".join(block.get("text", "") for block in prompt if block.get("type") == "text")


class AcpAgent:
    """Maps ACP sessions onto :class:`OrchestratorCore` workers."""

    def __init__(self, core: OrchestratorCore) -> None:
        self.core = core
        self._sessions: dict[str, str] = {}  # sessionId -> worker_id
        self._session_ids = itertools.count(1)

    async def handle_message(self, msg: dict[str, Any]) -> dict[str, Any] | None:
        """Dispatch one JSON-RPC frame. Returns a response dict, or None if none is owed
        (a notification, or an inbound response/error frame we have no use for)."""
        if "method" not in msg:
            return None
        method = msg["method"]
        params = msg.get("params") or {}
        msg_id = msg.get("id")

        try:
            result = await self._dispatch(method, params)
        except Exception as exc:  # noqa: BLE001 - report as a JSON-RPC error, never crash the loop
            log.warning("acp_handler_error", method=method, error=str(exc))
            if msg_id is None:
                return None
            return {"jsonrpc": "2.0", "id": msg_id, "error": {"code": -32000, "message": str(exc)}}

        if msg_id is None:  # notification: no response owed
            return None
        return {"jsonrpc": "2.0", "id": msg_id, "result": result}

    async def _dispatch(self, method: str, params: dict[str, Any]) -> Any:
        if method == "initialize":
            await self.core.start()
            return {
                "protocolVersion": PROTOCOL_VERSION,
                "agentCapabilities": {"loadSession": False, "promptCapabilities": {"text": True}},
            }
        if method == "session/new":
            wid = await self.core.spawn_worker()
            sid = f"s-{next(self._session_ids)}"
            self._sessions[sid] = wid
            return {"sessionId": sid}
        if method == "session/prompt":
            sid = params["sessionId"]
            wid = self._sessions.get(sid)
            if wid is None:
                raise WorkerNotFound(f"unknown ACP session: {sid!r}")
            reply = await self.core.converse(wid, _text_of(params.get("prompt", [])))
            return {"stopReason": "end_turn", "content": [{"type": "text", "text": reply}]}
        if method == "session/cancel":
            return None  # single-shot asks under the hood; nothing in-flight to cancel
        raise ValueError(f"unsupported ACP method: {method!r}")

    async def close(self) -> None:
        for sid, wid in list(self._sessions.items()):
            self._sessions.pop(sid, None)
            try:
                await self.core.close_worker(wid)
            except WorkerNotFound:
                pass


async def run_acp_stdio(core: OrchestratorCore) -> None:
    """Wire an :class:`AcpAgent` to real stdin/stdout, one JSON object per line."""
    agent = AcpAgent(core)
    loop = asyncio.get_running_loop()

    reader = asyncio.StreamReader()
    read_protocol = asyncio.StreamReaderProtocol(reader)
    await loop.connect_read_pipe(lambda: read_protocol, sys.stdin)

    w_transport, w_protocol = await loop.connect_write_pipe(
        asyncio.streams.FlowControlMixin, sys.stdout
    )
    writer = asyncio.StreamWriter(w_transport, w_protocol, reader, loop)

    try:
        while True:
            line = await reader.readline()
            if not line:
                break
            line = line.strip()
            if not line:
                continue
            try:
                msg = json.loads(line)
            except json.JSONDecodeError:
                log.warning("acp_bad_json", line=line[:200])
                continue
            response = await agent.handle_message(msg)
            if response is not None:
                writer.write((json.dumps(response) + "\n").encode())
                await writer.drain()
    finally:
        await agent.close()
        await core.close()


__all__ = ["AcpAgent", "run_acp_stdio"]
