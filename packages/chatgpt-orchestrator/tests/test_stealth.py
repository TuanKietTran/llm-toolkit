"""Unit tests for fingerprint generation determinism, proxy parsing, and geo-consistent context
kwargs (Accept-Language must always match the effective locale)."""

from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock

import pytest

from orchestrator.browser.session import SessionSpec, build_context_kwargs, parse_proxy
from orchestrator.browser.stealth import build_stealth_init_script, generate_fingerprint, human_type
from orchestrator.config import BehaviorSettings


def test_fingerprint_sticky_by_seed():
    fp1 = generate_fingerprint("account-a")
    fp2 = generate_fingerprint("account-a")
    assert fp1 == fp2  # same seed -> identical fingerprint every time


def test_fingerprint_varies_by_seed():
    seeds = [f"account-{i}" for i in range(12)]
    fps = [generate_fingerprint(s) for s in seeds]
    # Not every field need differ, but the set of full fingerprints shouldn't all collapse to one.
    assert len({(fp.user_agent, fp.viewport, fp.webgl_renderer) for fp in fps}) > 1


def test_fingerprint_internally_consistent_profile():
    fp = generate_fingerprint("account-consistency")
    if fp.ua_platform == "Windows":
        assert fp.platform == "Win32"
        assert "Google Inc." in fp.webgl_vendor
    elif fp.ua_platform == "macOS":
        assert fp.platform == "MacIntel"
        assert "Apple" in fp.webgl_renderer


def test_fingerprint_viewport_smaller_than_screen():
    fp = generate_fingerprint("account-viewport")
    vw, vh = fp.viewport
    sw, sh = fp.screen
    assert vw <= sw
    assert vh < sh


def test_parse_proxy_full_url():
    proxy = parse_proxy("http://user:pass@proxy.example.com:8080")
    assert proxy == {
        "server": "http://proxy.example.com:8080",
        "username": "user",
        "password": "pass",
    }


def test_parse_proxy_bare_host_port():
    proxy = parse_proxy("proxy.example.com:8080")
    assert proxy == {"server": "http://proxy.example.com:8080"}


def test_parse_proxy_none_when_empty():
    assert parse_proxy(None) is None
    assert parse_proxy("") is None


def test_build_context_kwargs_geo_consistency_default():
    fp = generate_fingerprint("acct-geo-1")
    spec = SessionSpec(account_id="acct-geo-1")
    kwargs = build_context_kwargs(spec, fp)
    assert kwargs["locale"] == fp.locale
    assert kwargs["extra_http_headers"]["Accept-Language"] == fp.accept_language


def test_build_context_kwargs_geo_consistency_when_locale_overridden():
    fp = generate_fingerprint("acct-geo-2")  # default en-US fingerprint
    spec = SessionSpec(account_id="acct-geo-2", locale="en-GB", timezone_id="Europe/London")
    kwargs = build_context_kwargs(spec, fp)
    # Overriding just the locale must NOT leave a stale Accept-Language from the fingerprint.
    assert kwargs["locale"] == "en-GB"
    assert kwargs["extra_http_headers"]["Accept-Language"] == "en-GB,en;q=0.9"
    assert kwargs["timezone_id"] == "Europe/London"


def test_build_context_kwargs_explicit_accept_language_wins():
    fp = generate_fingerprint("acct-geo-3")
    spec = SessionSpec(account_id="acct-geo-3", locale="fr-FR", accept_language="fr-FR,fr;q=0.9,en;q=0.5")
    kwargs = build_context_kwargs(spec, fp)
    assert kwargs["extra_http_headers"]["Accept-Language"] == "fr-FR,fr;q=0.9,en;q=0.5"


def test_build_context_kwargs_includes_proxy_when_set():
    fp = generate_fingerprint("acct-geo-4")
    spec = SessionSpec(account_id="acct-geo-4", proxy="http://u:p@10.0.0.1:9000")
    kwargs = build_context_kwargs(spec, fp)
    assert kwargs["proxy"] == {"server": "http://10.0.0.1:9000", "username": "u", "password": "p"}


def test_build_context_kwargs_omits_proxy_when_unset():
    fp = generate_fingerprint("acct-geo-5")
    spec = SessionSpec(account_id="acct-geo-5")
    kwargs = build_context_kwargs(spec, fp)
    assert "proxy" not in kwargs


def test_stealth_init_script_contains_expected_patches():
    fp = generate_fingerprint("acct-init-script")
    script = build_stealth_init_script(fp)
    assert "deviceMemory" in script
    assert "hardwareConcurrency" in script
    assert "getParameter" in script  # WebGL vendor/renderer patch
    assert fp.webgl_renderer in script


@pytest.mark.asyncio
async def test_human_type_fills_long_generated_prompts():
    page = MagicMock()
    page.keyboard = AsyncMock()
    locator = AsyncMock()
    page.locator.return_value = locator
    text = "tool schema " * 100

    await human_type(page, "#composer", text, BehaviorSettings())

    locator.click.assert_awaited_once()
    locator.fill.assert_awaited_once_with(text)
    page.keyboard.type.assert_not_awaited()
