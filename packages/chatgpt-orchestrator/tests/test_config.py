"""Unit tests for the typed config loader: TOML defaults, ORCH_ env overrides (nested delimiter +
JSON-array fields), base_url normalization, and model validators."""

from __future__ import annotations

import json

import pytest
from pydantic import ValidationError

from orchestrator.config import (
    AccountConfig,
    BehaviorSettings,
    BrowserSettings,
    ScalingSettings,
    load_settings,
)

_TOML = """
[browser]
driver = "playwright"
channel = ""
headless = true
nav_timeout_ms = 12345
base_url = "https://example.test"

[scaling]
min_warm = 2
max_contexts = 5
"""


def test_toml_defaults_load(tmp_path):
    toml_path = tmp_path / "config.toml"
    toml_path.write_text(_TOML)
    settings = load_settings(config_file=toml_path, env_file=None)
    assert settings.browser.driver == "playwright"
    assert settings.browser.headless is True
    assert settings.browser.nav_timeout_ms == 12345
    assert settings.scaling.min_warm == 2
    assert settings.scaling.max_contexts == 5
    # base_url is normalized with a trailing slash regardless of source.
    assert settings.browser.base_url == "https://example.test/"


def test_env_overrides_toml_nested(tmp_path, monkeypatch):
    toml_path = tmp_path / "config.toml"
    toml_path.write_text(_TOML)
    monkeypatch.setenv("ORCH_SCALING__MAX_CONTEXTS", "20")
    settings = load_settings(config_file=toml_path, env_file=None)
    assert settings.scaling.max_contexts == 20  # env beats TOML
    assert settings.scaling.min_warm == 2  # untouched field still comes from TOML


def test_env_json_accounts(tmp_path, monkeypatch):
    toml_path = tmp_path / "config.toml"
    toml_path.write_text(_TOML)
    accounts = [{"id": "a1", "mode": "anonymous"}, {"id": "a2", "mode": "anonymous"}]
    monkeypatch.setenv("ORCH_ACCOUNTS", json.dumps(accounts))
    settings = load_settings(config_file=toml_path, env_file=None)
    assert [a.id for a in settings.accounts] == ["a1", "a2"]


def test_base_url_gets_trailing_slash():
    assert BrowserSettings(base_url="https://chatgpt.com").base_url == "https://chatgpt.com/"
    assert BrowserSettings(base_url="https://chatgpt.com/").base_url == "https://chatgpt.com/"


def test_scaling_validation_errors():
    with pytest.raises(ValidationError):
        ScalingSettings(ema_alpha=0.0)
    with pytest.raises(ValidationError):
        ScalingSettings(ema_alpha=1.5)
    with pytest.raises(ValidationError):
        ScalingSettings(min_warm=5, max_contexts=2)
    with pytest.raises(ValidationError):
        ScalingSettings(max_contexts=0)


def test_behavior_validation_errors():
    with pytest.raises(ValidationError):
        BehaviorSettings(keystroke_min_ms=300, keystroke_max_ms=100)
    with pytest.raises(ValidationError):
        BehaviorSettings(think_min_ms=2000, think_max_ms=100)


def test_credentialed_account_requires_storage_state():
    with pytest.raises(ValidationError):
        AccountConfig(id="c1", mode="credentialed")
    cfg = AccountConfig(id="c1", mode="credentialed", storage_state="storage_state/c1.json")
    assert cfg.storage_state == "storage_state/c1.json"
