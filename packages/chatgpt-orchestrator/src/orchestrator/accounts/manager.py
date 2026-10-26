"""Multi-account manager: registry, scheduling, budgets/cooldowns, proxy binding.

Picks a healthy, in-budget, not-cooled-down account for each new worker context (least-loaded, then
LRU; sticky binding keeps a multi-turn conversation on the same identity). Exposes ``capacity`` so
the autoscaler never grows past the available account budget, and builds the per-identity
:class:`SessionSpec` (mode + proxy + storage_state + geo). Optionally mints masked aliases and reads
login OTP codes via the configured email provider + IMAP reader.
"""

from __future__ import annotations

import time
from collections.abc import Callable
from dataclasses import dataclass

from ..browser.session import SessionSpec
from ..config import AccountConfig, Settings
from ..telemetry import get_logger, get_metrics
from .budget import Cooldown, RollingBudget
from .email_provider import EmailProvider, build_email_provider
from .inbox import ImapInboxReader, build_inbox_reader
from .store import AccountStore

log = get_logger(__name__)
metrics = get_metrics()

# Retire an identity after this many consecutive challenges/limits (its fingerprint looks burned).
_RETIRE_AFTER_CHALLENGES = 5


class NoAccountAvailable(RuntimeError):
    """No account is currently schedulable (all busy-at-budget, cooled down, or dead)."""


@dataclass(slots=True)
class AccountState:
    config: AccountConfig
    budget: RollingBudget
    cooldown: Cooldown
    inflight: int = 0
    last_used: float = 0.0
    consecutive_challenges: int = 0
    dead: bool = False

    @property
    def id(self) -> str:
        return self.config.id


class AccountManager:
    """Owns account identities and mediates their scheduling."""

    def __init__(
        self,
        settings: Settings,
        *,
        clock: Callable[[], float] = time.monotonic,
        store: AccountStore | None = None,
        email_provider: EmailProvider | None = None,
        inbox: ImapInboxReader | None = None,
    ) -> None:
        self.settings = settings
        self._clock = clock
        self.store = store or AccountStore()
        self._email = email_provider  # lazily built on demand
        self._inbox = inbox  # lazily built on demand
        self._states: dict[str, AccountState] = {}

        configs = list(settings.accounts) or [AccountConfig(id="anon-default", mode="anonymous")]
        for cfg in configs:
            self._add_state(cfg)

    def _add_state(self, cfg: AccountConfig) -> AccountState:
        state = AccountState(
            config=cfg,
            budget=RollingBudget(cfg.budget_messages, cfg.budget_window_seconds, clock=self._clock),
            cooldown=Cooldown(clock=self._clock),
        )
        self._states[cfg.id] = state
        return state

    # ---- scheduling ------------------------------------------------------------------

    def _schedulable(self, s: AccountState) -> bool:
        return not s.dead and not s.cooldown.active() and s.budget.available()

    def any_proxied(self) -> bool:
        """Whether any identity uses a proxy (→ launch browser with per-context proxy support)."""
        return any(bool(s.config.proxy) for s in self._states.values())

    def get(self, account_id: str) -> AccountState | None:
        return self._states.get(account_id)

    def capacity(self) -> int:
        """Ceiling on total live contexts: identities in use + free schedulable identities.

        One identity backs at most one context, so this bounds the autoscaler to the account count
        and never double-books an identity.
        """
        free = sum(1 for s in self._states.values() if s.inflight == 0 and self._schedulable(s))
        in_use = sum(1 for s in self._states.values() if s.inflight > 0 and not s.dead)
        return free + in_use

    def acquire(self, *, sticky: str | None = None) -> AccountState:
        """Reserve a *free* identity for a new context (exclusive). Raises NoAccountAvailable."""
        if sticky is not None:
            s = self._states.get(sticky)
            if s is not None and s.inflight == 0 and self._schedulable(s):
                return self._reserve(s)
        candidates = [s for s in self._states.values() if s.inflight == 0 and self._schedulable(s)]
        if not candidates:
            raise NoAccountAvailable("no free schedulable account (all in use, cooled, or at budget)")
        chosen = min(candidates, key=lambda s: s.last_used)  # LRU among free identities
        return self._reserve(chosen)

    def _reserve(self, s: AccountState) -> AccountState:
        s.inflight += 1
        s.last_used = self._clock()
        metrics.gauge("account_capacity", self.capacity())
        return s

    def release(self, s: AccountState, *, messages_sent: int = 0) -> None:
        """Free the identity when its context is destroyed (does not apply cooldown)."""
        s.inflight = max(0, s.inflight - 1)
        s.last_used = self._clock()
        if messages_sent:
            s.budget.consume(messages_sent)
        metrics.gauge("account_capacity", self.capacity())

    def penalize(self, s: AccountState, reason: str) -> None:
        """Cool an identity down after a rate-limit/challenge; retire it after repeated strikes."""
        s.cooldown.trigger(s.config.cooldown_seconds)
        s.consecutive_challenges += 1
        metrics.incr("account_cooldowns", reason=reason)
        log.warning(
            "account_cooldown",
            account=s.id,
            reason=reason,
            cooldown_s=s.config.cooldown_seconds,
            strikes=s.consecutive_challenges,
        )
        if s.consecutive_challenges >= _RETIRE_AFTER_CHALLENGES:
            s.dead = True
            metrics.incr("accounts_retired")
            log.error("account_retired", account=s.id, strikes=s.consecutive_challenges)
        metrics.gauge("account_capacity", self.capacity())

    def note_success(self, s: AccountState) -> None:
        """Reset the strike counter after a clean interaction."""
        s.consecutive_challenges = 0

    def retry_after(self) -> float | None:
        """Smallest wait (s) until some identity becomes schedulable; None if all dead."""
        waits: list[float] = []
        for s in self._states.values():
            if s.dead:
                continue
            if self._schedulable(s):
                return 0.0
            waits.append(max(s.cooldown.remaining(), s.budget.retry_after()))
        return min(waits) if waits else None

    # ---- session spec ----------------------------------------------------------------

    def session_spec(self, s: AccountState) -> SessionSpec:
        """Build the browser identity spec for an account (proxy, storage_state, geo)."""
        cfg = s.config
        storage = cfg.storage_state or self.store.storage_state_path(cfg.id)
        return SessionSpec(
            account_id=cfg.id,
            mode=cfg.mode,
            proxy=cfg.proxy or None,
            storage_state_path=storage,
        )

    # ---- reporting -------------------------------------------------------------------

    def report(self) -> dict:
        now = self._clock()
        accounts = []
        for s in self._states.values():
            accounts.append(
                {
                    "id": s.id,
                    "mode": s.config.mode,
                    "email": s.config.email,
                    "proxied": bool(s.config.proxy),
                    "inflight": s.inflight,
                    "budget_used": s.budget.used(),
                    "budget_limit": s.budget.limit,
                    "cooldown_remaining_s": round(s.cooldown.remaining(), 1),
                    "schedulable": self._schedulable(s),
                    "strikes": s.consecutive_challenges,
                    "dead": s.dead,
                }
            )
        return {"capacity": self.capacity(), "accounts": accounts, "as_of": now}

    # ---- email aliasing / OTP (lazy) -------------------------------------------------

    def email_provider(self) -> EmailProvider:
        if self._email is None:
            self._email = build_email_provider(self.settings.email)
        return self._email

    def inbox(self) -> ImapInboxReader | None:
        if self._inbox is None:
            self._inbox = build_inbox_reader(self.settings.email)
        return self._inbox

    async def allocate_email(self) -> str:
        """Mint a fresh masked alias (e.g. via DuckDuckGo Email Protection)."""
        return await self.email_provider().allocate()

    async def read_login_code(self, alias: str | None = None, *, timeout: float = 120.0) -> str | None:
        """Read a ChatGPT login OTP from the forwarding inbox, or None if unconfigured/timeout."""
        reader = self.inbox()
        if reader is None:
            log.warning("otp_read_skipped", reason="no imap inbox configured")
            return None
        return await reader.find_otp(
            alias=alias,
            from_contains=self.settings.email.otp_from_contains,
            timeout=timeout,
        )

    def register_provisioned(self, cfg: AccountConfig) -> AccountState:
        """Add a runtime-provisioned account and persist its metadata."""
        state = self._add_state(cfg)
        self.store.upsert(
            {"id": cfg.id, "mode": cfg.mode, "email": cfg.email, "proxy": cfg.proxy}
        )
        log.info("account_registered", account=cfg.id, mode=cfg.mode, email=cfg.email)
        return state


__all__ = ["AccountManager", "AccountState", "NoAccountAvailable"]
