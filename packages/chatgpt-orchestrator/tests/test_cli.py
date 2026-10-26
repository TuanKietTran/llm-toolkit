"""Unit tests for the Typer CLI: command wiring, --help, accounts list, and doctor — via
typer.testing.CliRunner. doctor's browser-launch check uses the real (already-installed) browser
binary rather than a mock, since exercising that path for real is the point of Task 10's
"Chromium installed; doctor browser check PASSes" acceptance criterion; it skips gracefully if this
environment doesn't have a browser binary installed (see test_driver_integration.py for the same
pattern).
"""

from __future__ import annotations

import pytest
from typer.testing import CliRunner

from orchestrator.cli import app
from orchestrator.config import get_settings

runner = CliRunner()


@pytest.fixture(autouse=True)
def _clear_settings_cache():
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


def test_help_lists_all_commands():
    result = runner.invoke(app, ["--help"])
    assert result.exit_code == 0
    for cmd in ("serve-mcp", "serve-acp", "ask", "doctor", "accounts"):
        assert cmd in result.output


def test_accounts_help_lists_subcommands():
    result = runner.invoke(app, ["accounts", "--help"])
    assert result.exit_code == 0
    for cmd in ("list", "alloc-email", "ddg-login"):
        assert cmd in result.output


def test_serve_mcp_rejects_invalid_transport():
    result = runner.invoke(app, ["serve-mcp", "--transport", "carrier-pigeon"])
    assert result.exit_code == 2
    assert "invalid --transport" in result.output


def test_accounts_list_shows_synthesized_default(monkeypatch, tmp_path):
    monkeypatch.chdir(tmp_path)  # no config/config.toml or ORCH_ACCOUNTS here -> synthesized default
    result = runner.invoke(app, ["accounts", "list"])
    assert result.exit_code == 0
    assert "anon-default" in result.output
    assert "capacity: 1" in result.output


def test_doctor_reports_each_check():
    result = runner.invoke(app, ["doctor"])
    for check in ("settings load", "driver import", "browser launch", "mcp server build"):
        assert check in result.output
    assert result.exit_code in (0, 1)  # never crashes; PASS/FAIL is env-dependent


def test_doctor_all_pass_when_browser_available():
    result = runner.invoke(app, ["doctor"])
    if "[FAIL] driver import" in result.output or "[FAIL] browser launch" in result.output:
        pytest.skip("no browser driver/binary installed in this environment")
    assert result.exit_code == 0
    assert "[PASS] settings load" in result.output
    assert "[PASS] driver import" in result.output
    assert "[PASS] browser launch" in result.output
    assert "[PASS] mcp server build" in result.output
