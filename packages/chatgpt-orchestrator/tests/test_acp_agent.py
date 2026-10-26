"""Unit tests for the ACP adapter's pure message dispatch (AcpAgent.handle_message), against the
fake-backed core fixture. Real stdio wiring (run_acp_stdio) is exercised manually/via serve-acp;
this covers the actual protocol logic: initialize, session/new, session/prompt, errors, and
notifications."""

from __future__ import annotations

import pytest

from orchestrator.server.acp_agent import AcpAgent


@pytest.mark.asyncio
async def test_initialize_starts_core_and_returns_capabilities(fake_core):
    agent = AcpAgent(fake_core)
    resp = await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
    assert resp == {
        "jsonrpc": "2.0",
        "id": 1,
        "result": {
            "protocolVersion": 1,
            "agentCapabilities": {"loadSession": False, "promptCapabilities": {"text": True}},
        },
    }
    assert fake_core._started
    await agent.close()
    await fake_core.close()


@pytest.mark.asyncio
async def test_session_new_then_prompt_round_trip(fake_core):
    agent = AcpAgent(fake_core)
    await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})

    new_resp = await agent.handle_message({"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {}})
    sid = new_resp["result"]["sessionId"]
    assert sid

    prompt_resp = await agent.handle_message(
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "session/prompt",
            "params": {"sessionId": sid, "prompt": [{"type": "text", "text": "hello acp"}]},
        }
    )
    assert prompt_resp["result"] == {
        "stopReason": "end_turn",
        "content": [{"type": "text", "text": "reply:hello acp"}],
    }
    await agent.close()
    await fake_core.close()


@pytest.mark.asyncio
async def test_prompt_concatenates_multiple_text_blocks(fake_core):
    agent = AcpAgent(fake_core)
    await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
    sid = (
        await agent.handle_message({"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {}})
    )["result"]["sessionId"]

    resp = await agent.handle_message(
        {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "session/prompt",
            "params": {
                "sessionId": sid,
                "prompt": [{"type": "text", "text": "line one"}, {"type": "text", "text": "line two"}],
            },
        }
    )
    assert resp["result"]["content"][0]["text"] == "reply:line one\nline two"
    await agent.close()
    await fake_core.close()


@pytest.mark.asyncio
async def test_prompt_on_unknown_session_returns_json_rpc_error_not_a_crash(fake_core):
    agent = AcpAgent(fake_core)
    await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})

    resp = await agent.handle_message(
        {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "session/prompt",
            "params": {"sessionId": "bogus", "prompt": [{"type": "text", "text": "x"}]},
        }
    )
    assert resp["id"] == 2
    assert "error" in resp
    assert "bogus" in resp["error"]["message"]
    await agent.close()
    await fake_core.close()


@pytest.mark.asyncio
async def test_unsupported_method_returns_error():
    from orchestrator.server.core import OrchestratorCore
    from orchestrator.config import Settings

    agent = AcpAgent(OrchestratorCore(Settings()))
    resp = await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "nonexistent/method", "params": {}})
    assert "error" in resp
    assert "nonexistent/method" in resp["error"]["message"]


@pytest.mark.asyncio
async def test_notification_returns_none_no_response_owed(fake_core):
    agent = AcpAgent(fake_core)
    resp = await agent.handle_message({"jsonrpc": "2.0", "method": "session/cancel", "params": {}})
    assert resp is None


@pytest.mark.asyncio
async def test_response_frame_from_client_is_ignored(fake_core):
    agent = AcpAgent(fake_core)
    # A frame with no "method" is a response/error the client sent us; nothing to dispatch.
    resp = await agent.handle_message({"jsonrpc": "2.0", "id": 1, "result": {}})
    assert resp is None


@pytest.mark.asyncio
async def test_close_closes_all_open_sessions(fake_core):
    agent = AcpAgent(fake_core)
    await agent.handle_message({"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {}})
    await agent.handle_message({"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": {}})
    assert len(fake_core._workers) == 1

    await agent.close()
    assert len(fake_core._workers) == 0
    await fake_core.close()
