"""Per-identity browser session setup.

A :class:`BrowserSession` is one isolated ``BrowserContext`` (own cookies/storage/fingerprint)
bound to one account + proxy = one worker identity. This module owns the *shape* of a context:
fingerprint → context kwargs, proxy parsing, and storage_state load/save (so credentialed logins
and aged anonymous cookies persist across runs).
"""

from __future__ import annotations

import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import TYPE_CHECKING, Any
from urllib.parse import urlparse

if TYPE_CHECKING:  # avoid importing playwright at module import time
    from playwright.async_api import BrowserContext, Page


@dataclass(slots=True)
class Fingerprint:
    """An internally-consistent device fingerprint for one identity.

    Consistency matters more than novelty: UA/platform/WebGL/hardware must agree, and the geo
    fields (locale/timezone/accept_language) must match the proxy's region.
    """

    user_agent: str
    platform: str  # navigator.platform, e.g. "Win32" / "MacIntel"
    ua_platform: str  # sec-ch-ua-platform, e.g. "Windows" / "macOS"
    viewport: tuple[int, int]
    screen: tuple[int, int]
    device_scale_factor: float
    locale: str
    timezone_id: str
    accept_language: str
    device_memory: int
    hardware_concurrency: int
    webgl_vendor: str
    webgl_renderer: str
    color_scheme: str = "light"


@dataclass(slots=True)
class SessionSpec:
    """Everything needed to materialize one identity's context."""

    account_id: str | None = None
    mode: str = "anonymous"  # "anonymous" | "credentialed"
    proxy: str | None = None
    storage_state_path: str | None = None
    fingerprint: Fingerprint | None = None
    # Geo overrides (align these with the proxy's region when known).
    timezone_id: str | None = None
    locale: str | None = None
    accept_language: str | None = None


def parse_proxy(proxy: str | None) -> dict[str, str] | None:
    """Turn ``http://user:pass@host:port`` into Playwright's proxy dict, or ``None``."""
    if not proxy:
        return None
    parsed = urlparse(proxy)
    if not parsed.hostname:
        # Allow bare host:port with no scheme.
        parsed = urlparse("http://" + proxy)
    if not parsed.hostname:
        return None
    server = f"{parsed.scheme or 'http'}://{parsed.hostname}"
    if parsed.port:
        server += f":{parsed.port}"
    out: dict[str, str] = {"server": server}
    if parsed.username:
        out["username"] = parsed.username
    if parsed.password:
        out["password"] = parsed.password
    return out


def _derive_accept_language(locale: str) -> str:
    primary = locale.split("-")[0]
    return f"{locale},{primary};q=0.9" if primary != locale else locale


def build_context_kwargs(spec: SessionSpec, fingerprint: Fingerprint) -> dict[str, Any]:
    """Assemble ``browser.new_context(**kwargs)`` arguments for one identity."""
    vw, vh = fingerprint.viewport
    sw, sh = fingerprint.screen
    locale = spec.locale or fingerprint.locale
    # Keep Accept-Language consistent with the effective locale (mismatch is a fingerprint tell).
    if spec.accept_language:
        accept_language = spec.accept_language
    elif spec.locale:
        accept_language = _derive_accept_language(spec.locale)
    else:
        accept_language = fingerprint.accept_language
    kwargs: dict[str, Any] = {
        "user_agent": fingerprint.user_agent,
        "viewport": {"width": vw, "height": vh},
        "screen": {"width": sw, "height": sh},
        "device_scale_factor": fingerprint.device_scale_factor,
        "locale": locale,
        "timezone_id": spec.timezone_id or fingerprint.timezone_id,
        "color_scheme": fingerprint.color_scheme,
        "is_mobile": False,
        "has_touch": False,
        "extra_http_headers": {"Accept-Language": accept_language},
    }
    proxy = parse_proxy(spec.proxy)
    if proxy:
        kwargs["proxy"] = proxy
    # Load stored cookies/localStorage for credentialed identities (and aged anon ones).
    if spec.storage_state_path and Path(spec.storage_state_path).exists():
        kwargs["storage_state"] = spec.storage_state_path
    return kwargs


@dataclass(slots=True)
class BrowserSession:
    """A live isolated context for one identity, plus a lazily-created page."""

    context: "BrowserContext"
    fingerprint: Fingerprint
    spec: SessionSpec
    created_at: float = field(default_factory=time.monotonic)
    _page: "Page | None" = None

    async def page(self) -> "Page":
        """Return the identity's working page, creating it on first use."""
        if self._page is None or self._page.is_closed():
            pages = self.context.pages
            self._page = pages[0] if pages else await self.context.new_page()
        return self._page

    async def save_storage_state(self, path: str | None = None) -> str | None:
        """Persist cookies/localStorage so this identity ages instead of always starting fresh."""
        target = path or self.spec.storage_state_path
        if not target:
            return None
        Path(target).parent.mkdir(parents=True, exist_ok=True)
        await self.context.storage_state(path=target)
        return target

    async def close(self) -> None:
        try:
            await self.context.close()
        finally:
            self._page = None


__all__ = [
    "BrowserSession",
    "Fingerprint",
    "SessionSpec",
    "build_context_kwargs",
    "parse_proxy",
]
