"""Typed configuration for the orchestrator.

Layering (lowest → highest priority):
  1. field defaults in the models below
  2. ``config/config.toml`` (section defaults, checked into the repo)
  3. environment variables prefixed ``ORCH_`` (nested via ``__``), plus ``.env``
  4. explicit values passed to the constructor

Secrets (accounts, proxies, inward MCP servers) come from the environment / ``.env`` as JSON
so they never live in version control. See ``.env.example``.
"""

from __future__ import annotations

import os
from functools import lru_cache
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, Field, field_validator, model_validator
from pydantic_settings import (
    BaseSettings,
    SettingsConfigDict,
    TomlConfigSettingsSource,
)

# --------------------------------------------------------------------------------------
# Section models (mirror the [sections] in config/config.toml)
# --------------------------------------------------------------------------------------


class BrowserSettings(BaseModel):
    """How the single shared browser process is launched."""

    driver: Literal["patchright", "playwright"] = "patchright"
    channel: str = "chrome"  # real Chrome channel > bundled Chromium for TLS fingerprint
    headless: bool = False
    nav_timeout_ms: int = 45_000
    base_url: str = "https://chatgpt.com/"
    # chatgpt.com's anonymous UI always renders a "Log in" button next to a usable composer.
    # When True (default), treat the login wall as blocking only if the composer is ALSO absent,
    # so anonymous use isn't rejected just because a login *offer* is on the page. Set False to
    # require a fully logged-out-free page (old strict behavior).
    allow_anonymous: bool = True

    @field_validator("base_url")
    @classmethod
    def _ensure_trailing_slash(cls, v: str) -> str:
        return v if v.endswith("/") else v + "/"


class NetworkSettings(BaseModel):
    """Optional host-VPN guard.

    The orchestrator does not bring a VPN up or alter host routes. When required, it refuses to
    launch unless the configured interface already exists and is up.
    """

    vpn_required: bool = False
    vpn_interface: str = ""

    @model_validator(mode="after")
    def _required_interface(self) -> "NetworkSettings":
        if self.vpn_required and not self.vpn_interface.strip():
            raise ValueError("network.vpn_interface is required when network.vpn_required=true")
        return self


class ScalingSettings(BaseModel):
    """Autoscaler knobs (see browser/autoscaler.py)."""

    min_warm: int = 1
    max_contexts: int = 8
    per_context_mem_mb: int = 250
    mem_safety_factor: float = 1.3
    idle_ttl_seconds: float = 180.0
    scale_tick_seconds: float = 2.0
    scale_action_cooldown_seconds: float = 5.0
    max_create_per_tick: int = 2
    ema_alpha: float = 0.4
    max_queue: int = 128

    @model_validator(mode="after")
    def _sanity(self) -> "ScalingSettings":
        if self.max_contexts < 1:
            raise ValueError("scaling.max_contexts must be >= 1")
        if self.min_warm > self.max_contexts:
            raise ValueError("scaling.min_warm cannot exceed scaling.max_contexts")
        if not 0.0 < self.ema_alpha <= 1.0:
            raise ValueError("scaling.ema_alpha must be in (0, 1]")
        return self


class AgentSettings(BaseModel):
    """Worker-side ReAct loop caps (only used when a worker is equipped with tools)."""

    max_steps: int = 6
    ask_timeout_seconds: float = 120.0
    repair_attempts: int = 2


class BehaviorSettings(BaseModel):
    """Human-like pacing to reduce per-session flagging."""

    keystroke_min_ms: int = 60
    keystroke_max_ms: int = 220
    think_min_ms: int = 400
    think_max_ms: int = 1800
    min_gap_between_messages_ms: int = 2500

    @model_validator(mode="after")
    def _ordered(self) -> "BehaviorSettings":
        if self.keystroke_min_ms > self.keystroke_max_ms:
            raise ValueError("behavior.keystroke_min_ms cannot exceed keystroke_max_ms")
        if self.think_min_ms > self.think_max_ms:
            raise ValueError("behavior.think_min_ms cannot exceed think_max_ms")
        return self


# --------------------------------------------------------------------------------------
# Secret-bearing models (supplied via env/.env as JSON)
# --------------------------------------------------------------------------------------


class EmailSettings(BaseModel):
    """Email aliasing/masking + OTP reading for multi-account management.

    ``provider`` generates masked alias addresses (DuckDuckGo Email Protection forwards
    ``random@duck.com`` to a real inbox). Because DDG is a *forwarder*, reading ChatGPT login
    codes needs an IMAP reader pointed at that destination inbox. All secrets come from env/.env.
    """

    provider: Literal["none", "duckduckgo"] = "none"

    # DuckDuckGo Email Protection (unofficial quack API). Token from the extension/dashboard.
    ddg_access_token: str | None = None  # secret
    ddg_username: str | None = None

    # Destination inbox that aliases forward to, for reading OTP codes.
    imap_host: str | None = None
    imap_port: int = 993
    imap_username: str | None = None
    imap_password: str | None = None  # secret (Gmail: an app password)
    imap_mailbox: str = "INBOX"
    imap_ssl: bool = True
    # Match window/sender for OTP scraping.
    otp_from_contains: str = "openai"
    otp_newer_than_seconds: int = 300


class AccountConfig(BaseModel):
    """One identity the fleet can bind a worker to.

    ``anonymous``   → ephemeral identity (fresh context + dedicated proxy).
    ``credentialed`` → reuse a captured ``storage_state`` from a one-time scripted login.
    """

    id: str
    mode: Literal["anonymous", "credentialed"] = "anonymous"
    email: str | None = None  # login alias/address (e.g. a DDG @duck.com alias)
    proxy: str = ""  # e.g. http://user:pass@host:port ; residential recommended
    storage_state: str | None = None
    budget_messages: int = 8  # rolling-window message budget (~10/5h free)
    budget_window_seconds: int = 18_000  # 5h
    cooldown_seconds: float = 900.0  # applied after a rate-limit / challenge

    @model_validator(mode="after")
    def _credentialed_needs_state(self) -> "AccountConfig":
        if self.mode == "credentialed" and not self.storage_state:
            raise ValueError(
                f"account {self.id!r}: credentialed mode requires a storage_state path"
            )
        return self


class JobSiteAccountConfig(BaseModel):
    """One real, credentialed identity for job-site search+apply (ITviec, LinkedIn).

    Unlike ``AccountConfig`` (which supports an ``anonymous`` mode for the ChatGPT pool), a
    job-site identity is always ``credentialed`` -- there is no useful anonymous mode for
    applying to jobs under Kyle's real name. ``storage_state`` must point at a session captured
    by ``jobsite/capture_session.py`` (a human logging in by hand); this config never triggers a
    login itself. See ``jobsite/manager.py`` for the full bootstrap story.
    """

    id: str
    site: Literal["itviec", "linkedin"]
    storage_state: str
    proxy: str = ""

    @field_validator("storage_state")
    @classmethod
    def _storage_state_required(cls, v: str) -> str:
        if not v.strip():
            raise ValueError("jobsite account storage_state must be a non-empty path")
        return v


class InwardMcpServerConfig(BaseModel):
    """A tailored MCP server a worker can optionally be equipped with."""

    name: str
    transport: Literal["stdio", "http"] = "stdio"
    command: str | None = None
    args: list[str] = Field(default_factory=list)
    env: dict[str, str] = Field(default_factory=dict)
    url: str | None = None

    @model_validator(mode="after")
    def _transport_shape(self) -> "InwardMcpServerConfig":
        if self.transport == "stdio" and not self.command:
            raise ValueError(f"inward MCP server {self.name!r}: stdio transport needs a command")
        if self.transport == "http" and not self.url:
            raise ValueError(f"inward MCP server {self.name!r}: http transport needs a url")
        return self


# --------------------------------------------------------------------------------------
# Top-level settings
# --------------------------------------------------------------------------------------


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_prefix="ORCH_",
        env_nested_delimiter="__",
        # Plain Settings() is deterministic. load_settings()/get_settings() opt into .env.
        env_file=None,
        env_file_encoding="utf-8",
        extra="ignore",
    )

    log_level: str = "INFO"
    log_json: bool = False
    snapshot_dir: Path = Path("snapshots")

    browser: BrowserSettings = Field(default_factory=BrowserSettings)
    network: NetworkSettings = Field(default_factory=NetworkSettings)
    scaling: ScalingSettings = Field(default_factory=ScalingSettings)
    agent: AgentSettings = Field(default_factory=AgentSettings)
    behavior: BehaviorSettings = Field(default_factory=BehaviorSettings)
    email: EmailSettings = Field(default_factory=EmailSettings)

    accounts: list[AccountConfig] = Field(default_factory=list)
    jobsite_accounts: list[JobSiteAccountConfig] = Field(default_factory=list)
    inward_mcp_servers: list[InwardMcpServerConfig] = Field(default_factory=list)

    @classmethod
    def settings_customise_sources(
        cls,
        settings_cls,
        init_settings,
        env_settings,
        dotenv_settings,
        file_secret_settings,
    ):
        # Priority: init > env > .env > config.toml > file secrets.
        sources: list = [init_settings, env_settings, dotenv_settings]
        toml_path = _resolve_config_path()
        if toml_path is not None and toml_path.exists():
            sources.append(TomlConfigSettingsSource(settings_cls, toml_file=toml_path))
        sources.append(file_secret_settings)
        return tuple(sources)


# --------------------------------------------------------------------------------------
# Loading helpers
# --------------------------------------------------------------------------------------


def _resolve_config_path() -> Path | None:
    """Find ``config/config.toml``: explicit env override, else search upward from cwd."""
    override = os.environ.get("ORCH_CONFIG_FILE")
    if override:
        return Path(override)
    start = Path.cwd()
    for base in (start, *start.parents):
        candidate = base / "config" / "config.toml"
        if candidate.exists():
            return candidate
    return None


def load_settings(
    config_file: str | Path | None = None,
    env_file: str | Path | None = ".env",
) -> Settings:
    """Build a fresh :class:`Settings`, optionally overriding the TOML/``.env`` locations."""
    toml_path = Path(config_file) if config_file is not None else _resolve_config_path()

    class _ScopedSettings(Settings):
        @classmethod
        def settings_customise_sources(
            cls,
            settings_cls,
            init_settings,
            env_settings,
            dotenv_settings,
            file_secret_settings,
        ):
            sources: list = [init_settings, env_settings, dotenv_settings]
            if toml_path is not None and toml_path.exists():
                sources.append(TomlConfigSettingsSource(settings_cls, toml_file=toml_path))
            sources.append(file_secret_settings)
            return tuple(sources)

    # Passing None explicitly disables the model-configured default .env. Omitting the keyword
    # would unexpectedly load the repository .env during isolated tests/callers.
    return _ScopedSettings(_env_file=env_file)


@lru_cache(maxsize=1)
def get_settings() -> Settings:
    """Process-wide cached settings. Call ``get_settings.cache_clear()`` to reload."""
    return load_settings()


__all__ = [
    "AccountConfig",
    "AgentSettings",
    "BehaviorSettings",
    "BrowserSettings",
    "EmailSettings",
    "InwardMcpServerConfig",
    "JobSiteAccountConfig",
    "NetworkSettings",
    "ScalingSettings",
    "Settings",
    "get_settings",
    "load_settings",
]
