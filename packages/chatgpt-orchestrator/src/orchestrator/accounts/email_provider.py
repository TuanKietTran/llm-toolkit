"""Email alias / masking providers.

An :class:`EmailProvider` mints fresh masked addresses for account identities so OpenAI never sees
your real inbox and a burned alias is disposable. Backends are swappable behind this interface.

Primary backend: **DuckDuckGo Email Protection** — unlimited ``random@duck.com`` aliases via the
unofficial ``quack.duckduckgo.com`` API. NOTE: DDG *forwards* mail (no inbox API), so reading OTP
codes is a separate concern handled by :mod:`orchestrator.accounts.inbox`. Also note OpenAI may
reject ``@duck.com`` at signup (masked-domain blocklists) — treat this as experimental.
"""

from __future__ import annotations

from typing import Protocol, runtime_checkable

import httpx

from ..config import EmailSettings
from ..telemetry import get_logger

log = get_logger(__name__)

_QUACK_BASE = "https://quack.duckduckgo.com"


@runtime_checkable
class EmailProvider(Protocol):
    name: str

    async def allocate(self) -> str:
        """Return a fresh, unique email address to register/login an identity with."""
        ...


class NullEmailProvider:
    """No-op provider: aliasing disabled. Accounts must supply their own ``email``."""

    name = "none"

    async def allocate(self) -> str:  # pragma: no cover - trivial
        raise RuntimeError(
            "email.provider is 'none'; set provider=duckduckgo (with a token) to mint aliases"
        )


class DuckDuckGoEmailProvider:
    """Mints ``<random>@duck.com`` aliases via DuckDuckGo Email Protection.

    Requires an access token (from the DDG browser extension/app dashboard, or via :meth:`login`).
    The token is long-lived; keep it in env/.env, never in the repo.
    """

    name = "duckduckgo"

    def __init__(self, access_token: str, *, client: httpx.AsyncClient | None = None) -> None:
        if not access_token:
            raise ValueError("DuckDuckGoEmailProvider requires an access_token")
        self._token = access_token
        self._client = client
        self._owns_client = client is None

    async def _http(self) -> httpx.AsyncClient:
        if self._client is None:
            self._client = httpx.AsyncClient(timeout=20.0)
        return self._client

    async def allocate(self) -> str:
        client = await self._http()
        resp = await client.post(
            f"{_QUACK_BASE}/api/email/addresses",
            headers={"Authorization": f"Bearer {self._token}"},
        )
        resp.raise_for_status()
        address = resp.json().get("address")
        if not address:
            raise RuntimeError(f"DDG alias response missing 'address': {resp.text[:200]}")
        alias = f"{address}@duck.com"
        log.info("ddg_alias_allocated", alias=alias)
        return alias

    async def aclose(self) -> None:
        if self._owns_client and self._client is not None:
            await self._client.aclose()
            self._client = None

    # -- one-time token acquisition (unofficial; may change) ---------------------------

    @classmethod
    async def request_login_otp(cls, username: str, *, client: httpx.AsyncClient) -> None:
        """Ask DDG to email a one-time passphrase to ``username``'s Email Protection inbox."""
        resp = await client.get(f"{_QUACK_BASE}/api/auth/loginlink", params={"user": username})
        resp.raise_for_status()

    @classmethod
    async def login(cls, username: str, otp: str, *, client: httpx.AsyncClient) -> str:
        """Exchange a username + emailed OTP passphrase for a long-lived access token."""
        resp = await client.get(
            f"{_QUACK_BASE}/api/auth/login", params={"user": username, "otp": otp}
        )
        resp.raise_for_status()
        login_token = resp.json().get("token")
        if not login_token:
            raise RuntimeError("DDG login did not return a token")
        dash = await client.get(
            f"{_QUACK_BASE}/api/email/dashboard",
            headers={"Authorization": f"Bearer {login_token}"},
        )
        dash.raise_for_status()
        access_token = dash.json().get("user", {}).get("access_token")
        if not access_token:
            raise RuntimeError("DDG dashboard did not return an access_token")
        return access_token


def build_email_provider(settings: EmailSettings) -> EmailProvider:
    """Construct the configured provider (or the null provider)."""
    if settings.provider == "duckduckgo":
        if not settings.ddg_access_token:
            raise ValueError(
                "email.provider=duckduckgo but email.ddg_access_token is unset "
                "(ORCH_EMAIL__DDG_ACCESS_TOKEN)"
            )
        return DuckDuckGoEmailProvider(settings.ddg_access_token)
    return NullEmailProvider()


__all__ = [
    "DuckDuckGoEmailProvider",
    "EmailProvider",
    "NullEmailProvider",
    "build_email_provider",
]
