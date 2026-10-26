"""Owns one credentialed browser session per job site (ITviec, LinkedIn), reusing the single
shared :class:`~orchestrator.browser.manager.BrowserManager` process the ChatGPT pool also runs on.

## Credential bootstrap -- read this before wiring up real accounts

There is no scripted login here, and there never should be one for these two sites: ITviec and
LinkedIn both routinely gate login behind MFA/OTP/CAPTCHA, and this codebase's hard rule is to
never attempt to solve or click through a bot-check. So a real session always starts as a
**captured storage_state.json** from a one-time *human* login, exactly like the ``credentialed``
mode already supported for ChatGPT accounts in ``accounts/manager.py`` -- this module is that same
storage_state contract, just for job sites instead of chatgpt.com.

To capture one:

    uv run python -m orchestrator.jobsite.capture_session --site itviec --out storage_state/itviec.storage_state.json
    uv run python -m orchestrator.jobsite.capture_session --site linkedin --out storage_state/linkedin.storage_state.json

(see ``capture_session.py``) -- opens a **headed** browser, a human logs in by hand (solving any
MFA/CAPTCHA themselves), then presses Enter in the terminal once the site's own logged-in home
page is showing; the script saves ``context.storage_state()`` and exits. Re-run it whenever
``JobSiteManager`` starts logging ``jobsite_login_stale`` (session expired / logged out).

Until a storage_state file exists at the configured path, :meth:`JobSiteManager.session_for`
raises :class:`~orchestrator.jobsite.exceptions.LoginRequired` rather than silently proceeding
logged-out -- a logged-out apply attempt would either hit a login wall (useless) or, worse,
silently submit as the wrong (anonymous) identity.
"""

from __future__ import annotations

from pathlib import Path

from ..browser.manager import BrowserManager
from ..browser.session import BrowserSession, SessionSpec
from ..config import JobSiteAccountConfig, Settings
from ..telemetry import get_logger
from .exceptions import LoginRequired

log = get_logger(__name__)


class JobSiteManager:
    """Lazily opens and reuses one :class:`BrowserSession` per configured job-site account."""

    def __init__(self, browser: BrowserManager, settings: Settings) -> None:
        self.browser = browser
        self.settings = settings
        self._configs: dict[str, JobSiteAccountConfig] = {
            cfg.id: cfg for cfg in settings.jobsite_accounts
        }
        self._sessions: dict[str, BrowserSession] = {}

    def configured_sites(self) -> list[str]:
        return list(self._configs)

    def _config(self, site_id: str) -> JobSiteAccountConfig:
        cfg = self._configs.get(site_id)
        if cfg is None:
            raise LoginRequired(
                f"no jobsite account configured for {site_id!r}. Add one to ORCH_JOBSITE_ACCOUNTS "
                "(see .env.example) pointing at a captured storage_state file."
            )
        return cfg

    async def session_for(self, site_id: str) -> BrowserSession:
        """Return the live session for a site, opening it (from storage_state) on first use."""
        existing = self._sessions.get(site_id)
        if existing is not None:
            return existing

        cfg = self._config(site_id)
        if not cfg.storage_state or not Path(cfg.storage_state).exists():
            raise LoginRequired(
                f"jobsite account {site_id!r} has no storage_state at {cfg.storage_state!r}. "
                "Run `uv run python -m orchestrator.jobsite.capture_session --site "
                f"{cfg.site} --out {cfg.storage_state}` once, interactively, to log in as a human "
                "and capture the session -- this module will never attempt that login itself."
            )

        spec = SessionSpec(
            account_id=cfg.id,
            mode="credentialed",
            proxy=cfg.proxy or None,
            storage_state_path=cfg.storage_state,
        )
        session = await self.browser.new_session(spec)
        self._sessions[site_id] = session
        log.info("jobsite_session_opened", site=site_id)
        return session

    async def persist(self, site_id: str) -> None:
        """Save the session's current cookies/localStorage back to its storage_state file.

        Call after search/apply activity so the session ages naturally (picks up refreshed auth
        tokens) instead of silently going stale between runs.
        """
        session = self._sessions.get(site_id)
        if session is None:
            return
        path = await session.save_storage_state()
        log.info("jobsite_session_persisted", site=site_id, path=path)

    async def close_all(self) -> None:
        for site_id, session in list(self._sessions.items()):
            try:
                await session.save_storage_state()
            finally:
                await session.close()
            self._sessions.pop(site_id, None)
        log.info("jobsite_sessions_closed")


__all__ = ["JobSiteManager"]
