"""The single shared browser process.

One browser holds N isolated contexts (one per worker identity). This class owns launch/teardown
and the creation of per-identity :class:`BrowserSession` objects (fingerprint + stealth applied).
Lifecycle/health/recycling of the pool lives in ``context_pool.py``; multi-account binding in
``accounts/``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from ..config import Settings
from ..network import verify_vpn
from ..telemetry import get_logger, get_metrics
from .session import BrowserSession, SessionSpec, build_context_kwargs
from .stealth import build_stealth_init_script, generate_fingerprint

if TYPE_CHECKING:
    from playwright.async_api import Browser, Playwright

log = get_logger(__name__)
metrics = get_metrics()

# Placeholder proxy so Chromium enables per-context proxies (its per-context proxy only takes
# effect if the browser itself was launched with a proxy).
_PER_CONTEXT_PROXY = {"server": "http://per-context"}

_PLAYWRIGHT_STEALTH_ARGS = [
    "--disable-blink-features=AutomationControlled",
    "--no-first-run",
    "--no-default-browser-check",
]


class BrowserLaunchError(RuntimeError):
    """Raised when the browser cannot be launched (missing driver/channel)."""


class BrowserManager:
    """Launches and owns exactly one browser process."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self._pw: "Playwright | None" = None
        self._browser: "Browser | None" = None
        self._enable_context_proxies = False

    @property
    def is_running(self) -> bool:
        return self._browser is not None

    def _load_async_playwright(self):
        name = self.settings.browser.driver
        if name == "patchright":
            try:
                from patchright.async_api import async_playwright  # type: ignore
            except ImportError as exc:  # pragma: no cover - depends on optional extra
                raise BrowserLaunchError(
                    "driver=patchright but patchright is not installed. "
                    "Install it (`uv sync --extra stealth`) or set browser.driver=playwright."
                ) from exc
            return async_playwright
        from playwright.async_api import async_playwright

        return async_playwright

    async def start(self, *, enable_context_proxies: bool = False) -> None:
        """Launch the browser. Idempotent.

        ``enable_context_proxies`` must be True if any identity will use a per-context proxy
        (Chromium requires the browser be launched with a proxy for per-context proxies to work).
        """
        if self._browser is not None:
            return
        verify_vpn(self.settings.network)
        self._enable_context_proxies = enable_context_proxies

        async_playwright = self._load_async_playwright()
        self._pw = await async_playwright().start()

        launch_kwargs: dict = {"headless": self.settings.browser.headless}
        channel = self.settings.browser.channel or None
        if channel:
            launch_kwargs["channel"] = channel
        if enable_context_proxies:
            launch_kwargs["proxy"] = _PER_CONTEXT_PROXY
        if self.settings.browser.driver == "playwright":
            launch_kwargs["args"] = list(_PLAYWRIGHT_STEALTH_ARGS)

        try:
            self._browser = await self._pw.chromium.launch(**launch_kwargs)
        except Exception as exc:  # noqa: BLE001
            # A real Chrome channel is best, but fall back to bundled Chromium in dev.
            if channel:
                log.warning(
                    "browser_channel_launch_failed_fallback_bundled",
                    channel=channel,
                    error=str(exc),
                )
                launch_kwargs.pop("channel", None)
                try:
                    self._browser = await self._pw.chromium.launch(**launch_kwargs)
                except Exception as exc2:  # noqa: BLE001
                    await self._teardown_pw()
                    raise BrowserLaunchError(
                        f"failed to launch browser (channel and bundled): {exc2}. "
                        "Run `uv run playwright install chromium`."
                    ) from exc2
            else:
                await self._teardown_pw()
                raise BrowserLaunchError(
                    f"failed to launch browser: {exc}. Run `uv run playwright install chromium`."
                ) from exc

        metrics.incr("browser_started")
        log.info(
            "browser_started",
            driver=self.settings.browser.driver,
            channel=channel,
            headless=self.settings.browser.headless,
            context_proxies=enable_context_proxies,
        )

    async def new_session(self, spec: SessionSpec) -> BrowserSession:
        """Create one isolated identity context with fingerprint + stealth applied."""
        if self._browser is None:
            raise BrowserLaunchError("browser not started; call start() first")

        fingerprint = spec.fingerprint or generate_fingerprint(
            spec.account_id,
            locale=spec.locale,
            timezone_id=spec.timezone_id,
            accept_language=spec.accept_language,
        )
        if spec.proxy and not self._enable_context_proxies:
            log.warning(
                "context_proxy_ignored",
                account=spec.account_id,
                reason="browser launched without proxy support; restart with enable_context_proxies=True",
            )

        kwargs = build_context_kwargs(spec, fingerprint)
        context = await self._browser.new_context(**kwargs)
        await context.add_init_script(build_stealth_init_script(fingerprint))

        session = BrowserSession(context=context, fingerprint=fingerprint, spec=spec)
        await session.page()  # pre-create the working page

        metrics.incr("browser_sessions_created")
        log.info(
            "session_created",
            account=spec.account_id,
            mode=spec.mode,
            proxied=bool(spec.proxy),
            ua_platform=fingerprint.ua_platform,
            viewport=list(fingerprint.viewport),
            tz=kwargs.get("timezone_id"),
        )
        return session

    async def _teardown_pw(self) -> None:
        if self._pw is not None:
            try:
                await self._pw.stop()
            finally:
                self._pw = None

    async def close(self) -> None:
        """Close the browser and stop Playwright. Idempotent."""
        if self._browser is not None:
            try:
                await self._browser.close()
            finally:
                self._browser = None
        await self._teardown_pw()
        log.info("browser_closed")

    async def __aenter__(self) -> "BrowserManager":
        await self.start()
        return self

    async def __aexit__(self, *exc) -> None:
        await self.close()


__all__ = ["BrowserManager", "BrowserLaunchError"]
