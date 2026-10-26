"""Unit tests for the FastMCP server: tool/resource listing and round-trip calls, via an in-process
MCP client session (mcp.shared.memory). No network, no real browser.

The in-memory harness bypasses FastMCP's own lifespan, so tests start/close the core explicitly.
"""

from __future__ import annotations

import json

import pytest
from mcp.shared.memory import create_connected_server_and_client_session

from orchestrator.server.mcp_server import build_server

# FastMCP renders a `list[dict]` tool return as one TextContent block *per item* (so
# content[0].text is just the first item, not the whole list) but always mirrors the real
# Python return value in structuredContent["result"] — use that for list-returning tools.


@pytest.mark.asyncio
async def test_lists_all_tools_and_resources(fake_core):
    server = build_server(fake_core)
    async with create_connected_server_and_client_session(server._mcp_server) as client:
        await fake_core.start()
        tools = await client.list_tools()
        names = sorted(t.name for t in tools.tools)
        assert names == [
            "ask",
            "ask_batch",
            "close_worker",
            "converse",
            "list_workers",
            "spawn_worker",
            "write_code",
            "write_code_batch",
        ]
        resources = await client.list_resources()
        uris = sorted(str(r.uri) for r in resources.resources)
        assert uris == ["accounts://status", "pool://status"]
        await fake_core.close()


@pytest.mark.asyncio
async def test_spawn_ask_close_round_trip_over_mcp(fake_core):
    server = build_server(fake_core)
    async with create_connected_server_and_client_session(server._mcp_server) as client:
        await fake_core.start()

        spawned = await client.call_tool("spawn_worker", {})
        wid = spawned.content[0].text
        assert wid.startswith("w-")

        asked = await client.call_tool("ask", {"worker_id": wid, "prompt": "hi"})
        assert asked.content[0].text == "reply:hi"

        listed = await client.call_tool("list_workers", {})
        workers = listed.structuredContent["result"]
        assert workers == [
            {"id": wid, "account_id": workers[0]["account_id"], "messages_sent": 1, "equipped": False}
        ]

        closed = await client.call_tool("close_worker", {"worker_id": wid})
        assert closed.content[0].text == f"closed {wid}"

        await fake_core.close()


@pytest.mark.asyncio
async def test_resources_reflect_live_state(fake_core):
    server = build_server(fake_core)
    async with create_connected_server_and_client_session(server._mcp_server) as client:
        await fake_core.start()

        pool_before = json.loads((await client.read_resource("pool://status")).contents[0].text)
        assert pool_before["busy"] == 0

        wid = (await client.call_tool("spawn_worker", {})).content[0].text
        pool_after = json.loads((await client.read_resource("pool://status")).contents[0].text)
        assert pool_after["busy"] == 1

        accounts = json.loads((await client.read_resource("accounts://status")).contents[0].text)
        assert accounts["capacity"] == 3

        await client.call_tool("close_worker", {"worker_id": wid})
        await fake_core.close()


@pytest.mark.asyncio
async def test_ask_batch_over_mcp(fake_core):
    server = build_server(fake_core)
    async with create_connected_server_and_client_session(server._mcp_server) as client:
        await fake_core.start()
        result = await client.call_tool("ask_batch", {"prompts": ["a", "b"], "n_parallel": 2})
        items = result.structuredContent["result"]
        assert {i["reply"] for i in items} == {"reply:a", "reply:b"}
        await fake_core.close()


@pytest.mark.asyncio
async def test_write_code_over_mcp(fake_core):
    server = build_server(fake_core)
    async with create_connected_server_and_client_session(server._mcp_server) as client:
        await fake_core.start()
        # The fake worker echoes the prompt (no code fence), so had_fence is False and the prompt —
        # including the spec we passed — is what comes back as best-effort code.
        result = await client.call_tool(
            "write_code", {"spec": "is_prime(n)", "language": "python"}
        )
        # A dict tool return is serialized into content[0].text (structuredContent may be unset for a
        # bare-dict annotation); parse that.
        payload = json.loads(result.content[0].text)
        assert payload["ok"] is True
        assert payload["had_fence"] is False
        assert payload["language"] == "python"
        assert "is_prime(n)" in payload["code"]
        await fake_core.close()
