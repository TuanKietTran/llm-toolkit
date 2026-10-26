"""Optionally equip a worker with tailored MCP tools ('inward' MCP).

This is the secondary role from the plan: a ChatGPT worker can consume tools (filesystem, web fetch,
custom domain tools) from configured MCP servers. The host connects to each server, aggregates their
tools into a catalog for :mod:`.protocol`, and dispatches calls. Uses the `mcp` client SDK.
"""

from __future__ import annotations

from contextlib import AsyncExitStack

from ..config import InwardMcpServerConfig
from ..telemetry import get_logger

log = get_logger(__name__)


class InwardToolHost:
    """Connects to inward MCP servers and exposes a flat tool catalog + dispatcher."""

    def __init__(self, servers: list[InwardMcpServerConfig]) -> None:
        self._servers = servers
        self._stack: AsyncExitStack | None = None
        # tool name -> (session, spec dict)
        self._tools: dict[str, tuple[object, dict]] = {}

    async def start(self) -> None:
        """Connect all configured servers and list their tools. Idempotent."""
        if self._stack is not None:
            return
        from mcp import ClientSession, StdioServerParameters
        from mcp.client.stdio import stdio_client

        stack = AsyncExitStack()
        try:
            for cfg in self._servers:
                if cfg.transport == "stdio":
                    params = StdioServerParameters(
                        command=cfg.command or "", args=cfg.args, env=cfg.env or None
                    )
                    read, write = await stack.enter_async_context(stdio_client(params))
                elif cfg.transport == "http":
                    from mcp.client.streamable_http import streamablehttp_client

                    read, write, _ = await stack.enter_async_context(
                        streamablehttp_client(cfg.url or "")
                    )
                else:  # pragma: no cover - guarded by config validation
                    continue

                session = await stack.enter_async_context(ClientSession(read, write))
                await session.initialize()
                listed = await session.list_tools()
                for tool in listed.tools:
                    spec = {
                        "name": tool.name,
                        "description": tool.description or "",
                        "input_schema": getattr(tool, "inputSchema", None) or {},
                        "server": cfg.name,
                    }
                    if tool.name in self._tools:
                        log.warning("inward_tool_name_collision", tool=tool.name, server=cfg.name)
                    self._tools[tool.name] = (session, spec)
                log.info("inward_server_connected", server=cfg.name, tools=len(listed.tools))
        except Exception:
            await stack.aclose()
            raise
        self._stack = stack

    def catalog(self) -> list[dict]:
        return [spec for (_session, spec) in self._tools.values()]

    async def call(self, name: str, args: dict) -> str:
        """Invoke a tool and return its textual result."""
        entry = self._tools.get(name)
        if entry is None:
            raise KeyError(f"unknown tool: {name!r} (have: {', '.join(self._tools) or 'none'})")
        session, _spec = entry
        result = await session.call_tool(name, args)  # type: ignore[attr-defined]
        return _result_text(result)

    async def close(self) -> None:
        if self._stack is not None:
            await self._stack.aclose()
            self._stack = None
            self._tools.clear()


def _result_text(result: object) -> str:
    """Flatten an MCP CallToolResult's content blocks into text."""
    content = getattr(result, "content", None)
    if content is None:
        return str(result)
    parts: list[str] = []
    for block in content:
        text = getattr(block, "text", None)
        if text is not None:
            parts.append(text)
        else:
            parts.append(str(getattr(block, "data", block)))
    return "\n".join(parts)


__all__ = ["InwardToolHost"]
