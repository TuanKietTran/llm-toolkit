"""Unit tests for the DuckDuckGo Email Protection provider (via httpx.MockTransport, no real
network) and the build_email_provider() factory."""

from __future__ import annotations

import httpx
import pytest

from orchestrator.accounts.email_provider import (
    DuckDuckGoEmailProvider,
    NullEmailProvider,
    build_email_provider,
)
from orchestrator.config import EmailSettings


def _client(handler) -> httpx.AsyncClient:
    return httpx.AsyncClient(transport=httpx.MockTransport(handler))


@pytest.mark.asyncio
async def test_allocate_returns_duck_com_alias():
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/email/addresses"
        assert request.headers["authorization"] == "Bearer tok123"
        return httpx.Response(200, json={"address": "abc123"})

    provider = DuckDuckGoEmailProvider("tok123", client=_client(handler))
    alias = await provider.allocate()
    assert alias == "abc123@duck.com"
    await provider.aclose()


@pytest.mark.asyncio
async def test_allocate_raises_on_missing_address_field():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json={})

    provider = DuckDuckGoEmailProvider("tok123", client=_client(handler))
    with pytest.raises(RuntimeError, match="missing 'address'"):
        await provider.allocate()
    await provider.aclose()


@pytest.mark.asyncio
async def test_allocate_raises_on_http_error():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(401, json={"error": "bad token"})

    provider = DuckDuckGoEmailProvider("tok123", client=_client(handler))
    with pytest.raises(httpx.HTTPStatusError):
        await provider.allocate()
    await provider.aclose()


def test_provider_requires_access_token():
    with pytest.raises(ValueError):
        DuckDuckGoEmailProvider("")


@pytest.mark.asyncio
async def test_login_flow_exchanges_otp_for_access_token():
    calls = []

    def handler(request: httpx.Request) -> httpx.Response:
        calls.append(request.url.path)
        if request.url.path == "/api/auth/loginlink":
            return httpx.Response(200, json={})
        if request.url.path == "/api/auth/login":
            return httpx.Response(200, json={"token": "login-tok"})
        if request.url.path == "/api/email/dashboard":
            assert request.headers["authorization"] == "Bearer login-tok"
            return httpx.Response(200, json={"user": {"access_token": "final-access-tok"}})
        raise AssertionError(f"unexpected path {request.url.path}")

    async with _client(handler) as client:
        await DuckDuckGoEmailProvider.request_login_otp("someone", client=client)
        token = await DuckDuckGoEmailProvider.login("someone", "123456", client=client)
    assert token == "final-access-tok"
    assert calls == ["/api/auth/loginlink", "/api/auth/login", "/api/email/dashboard"]


@pytest.mark.asyncio
async def test_login_raises_when_dashboard_missing_access_token():
    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/auth/login":
            return httpx.Response(200, json={"token": "login-tok"})
        if request.url.path == "/api/email/dashboard":
            return httpx.Response(200, json={"user": {}})
        raise AssertionError

    async with _client(handler) as client:
        with pytest.raises(RuntimeError, match="access_token"):
            await DuckDuckGoEmailProvider.login("someone", "123456", client=client)


@pytest.mark.asyncio
async def test_null_provider_raises():
    with pytest.raises(RuntimeError):
        await NullEmailProvider().allocate()


def test_build_email_provider_none_returns_null():
    provider = build_email_provider(EmailSettings(provider="none"))
    assert isinstance(provider, NullEmailProvider)


def test_build_email_provider_duckduckgo_requires_token():
    with pytest.raises(ValueError, match="ddg_access_token"):
        build_email_provider(EmailSettings(provider="duckduckgo"))


def test_build_email_provider_duckduckgo_with_token():
    provider = build_email_provider(EmailSettings(provider="duckduckgo", ddg_access_token="tok"))
    assert isinstance(provider, DuckDuckGoEmailProvider)
