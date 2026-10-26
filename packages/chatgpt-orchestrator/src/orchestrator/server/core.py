"""OrchestratorCore: the agent-agnostic engine wrapping browser/pool/accounts/workers.

This is the single shared surface driven by the outward MCP server (``mcp_server.py``), the CLI's
``ask`` command, and the ACP adapter. It holds **no model/API client** — it only manages ChatGPT
browser workers; the reasoning agent (Claude Code, pi, ...) lives outside this process and calls in
via tools.
"""

from __future__ import annotations

import asyncio
import itertools

from ..accounts.manager import AccountManager
from ..browser.autoscaler import Autoscaler
from ..browser.context_pool import ContextPool
from ..browser.manager import BrowserManager
from ..browser.session import BrowserSession
from ..chatgpt.driver import ChatGPTDriver
from ..chatgpt.exceptions import ChatGPTError
from ..config import Settings
from ..jobsite.driver import JobSiteDriver
from ..jobsite.manager import JobSiteManager
from ..jobsite.schemas import JobLead
from ..jobsite.selectors import itviec_search_url, linkedin_search_url
from ..telemetry import get_logger, get_metrics
from ..workers.codegen import build_code_prompt, extract_code
from ..workers.inward_mcp import InwardToolHost
from ..workers.worker import Worker

log = get_logger(__name__)
metrics = get_metrics()


class WorkerNotFound(KeyError):
    """Raised when a worker id passed to a core method is unknown."""


class OrchestratorCore:
    """Owns the browser, account manager, context pool, autoscaler, and live workers."""

    def __init__(self, settings: Settings) -> None:
        self.settings = settings
        self.accounts = AccountManager(settings)
        self.browser = BrowserManager(settings)
        self.jobsites = JobSiteManager(self.browser, settings)
        self.pool: ContextPool | None = None
        self.autoscaler: Autoscaler | None = None
        self._workers: dict[str, Worker] = {}
        self._worker_ids = itertools.count(1)
        self._started = False

    def _require_pool(self) -> ContextPool:
        if self.pool is None:
            raise RuntimeError("OrchestratorCore.start() must be called first")
        return self.pool

    # ---- lifecycle ---------------------------------------------------------------------

    async def start(self) -> None:
        """Launch the browser and wire the pool/autoscaler. Idempotent."""
        if self._started:
            return

        await self.browser.start(enable_context_proxies=self.accounts.any_proxied())

        async def session_factory() -> BrowserSession:
            acct = self.accounts.acquire()
            spec = self.accounts.session_spec(acct)
            try:
                return await self.browser.new_session(spec)
            except Exception:
                # Session creation failed after the identity was reserved: nothing else will
                # release it (no PooledContext was ever created, so `closer` never fires).
                self.accounts.release(acct)
                raise

        async def closer(session: BrowserSession) -> None:
            state = self.accounts.get(session.spec.account_id)
            if state is not None:
                self.accounts.release(state)
            await session.close()

        self.pool = ContextPool(
            self.settings,
            session_factory,
            closer=closer,
            capacity_provider=self.accounts.capacity,
        )
        self.autoscaler = Autoscaler(self.pool, self.settings)
        await self.autoscaler.start()
        self._started = True
        log.info("core_started")

    async def close(self) -> None:
        """Close all workers, stop the autoscaler, close the pool, then the browser."""
        if not self._started:
            return
        for wid in list(self._workers):
            worker = self._workers.pop(wid, None)
            if worker is not None:
                await worker.close()
        if self.autoscaler is not None:
            await self.autoscaler.stop()
        if self.pool is not None:
            await self.pool.close()
        await self.jobsites.close_all()
        await self.browser.close()
        self._started = False
        log.info("core_closed")

    async def __aenter__(self) -> "OrchestratorCore":
        await self.start()
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self.close()

    # ---- worker lifecycle ----------------------------------------------------------------

    async def spawn_worker(self, *, equip_tools: bool = False) -> str:
        """Acquire a context+account, open a ChatGPT driver on it, and return a new worker id."""
        pool = self._require_pool()
        ctx = await pool.acquire(timeout=self.settings.agent.ask_timeout_seconds)
        acct = self.accounts.get(ctx.account_id) if ctx.account_id else None
        if acct is None:  # invariant violation: every pooled context is backed by an account
            await pool.release(ctx, quarantine="no_account")
            raise RuntimeError(f"context {ctx.id} has no backing account")

        driver = ChatGPTDriver(ctx.session, self.settings)
        tool_host = None
        if equip_tools and self.settings.inward_mcp_servers:
            tool_host = InwardToolHost(self.settings.inward_mcp_servers)

        wid = f"w-{next(self._worker_ids)}"
        worker = Worker(wid, ctx, acct, driver, self.settings, tool_host=tool_host)
        try:
            await worker.open()
        except ChatGPTError as exc:
            await self._fail_worker(worker, exc)
            raise
        self._workers[wid] = worker
        metrics.gauge("workers_active", len(self._workers))
        log.info("worker_spawned", worker=wid, account=acct.id, equipped=worker.equipped)
        return wid

    async def _fail_worker(self, worker: Worker, exc: Exception) -> None:
        """Cool the account down, quarantine+reap the context, and drop the worker.

        Never also calls ``accounts.release`` here — the quarantine reap destroys the context,
        which invokes the pool's ``closer`` and releases the account exactly once.
        """
        self.accounts.penalize(worker.account, type(exc).__name__)
        self._workers.pop(worker.id, None)
        await worker.close()
        pool = self._require_pool()
        await pool.release(worker.ctx, quarantine=type(exc).__name__)
        await pool.reap_quarantined()
        metrics.gauge("workers_active", len(self._workers))
        log.warning("worker_failed", worker=worker.id, account=worker.account_id, error=str(exc))

    async def close_worker(self, wid: str) -> None:
        """Close a worker and return its context to the pool idle (ready for reuse)."""
        worker = self._workers.pop(wid, None)
        if worker is None:
            raise WorkerNotFound(f"unknown worker id: {wid!r}")
        await worker.close()
        pool = self._require_pool()
        await pool.release(worker.ctx)
        self.accounts.note_success(worker.account)
        metrics.gauge("workers_active", len(self._workers))
        log.info("worker_closed", worker=wid)

    def _get_worker(self, wid: str) -> Worker:
        worker = self._workers.get(wid)
        if worker is None:
            raise WorkerNotFound(f"unknown worker id: {wid!r}")
        return worker

    # ---- ask / converse / fan-out ----------------------------------------------------------

    async def ask(self, wid: str, prompt: str) -> str:
        """Answer a single prompt on an existing worker."""
        worker = self._get_worker(wid)
        try:
            reply = await worker.ask(prompt)
        except ChatGPTError as exc:
            await self._fail_worker(worker, exc)
            raise
        self.accounts.note_success(worker.account)
        return reply

    async def converse(self, wid: str, message: str) -> str:
        """Continue a multi-turn conversation on an existing worker (same context, no re-open)."""
        worker = self._get_worker(wid)
        try:
            reply = await worker.converse(message)
        except ChatGPTError as exc:
            await self._fail_worker(worker, exc)
            raise
        self.accounts.note_success(worker.account)
        return reply

    async def read_context(self, wid: str, *, include_screenshot: bool = False) -> dict:
        """Inspect an existing worker's page -- for headless runs with no visible window."""
        worker = self._get_worker(wid)
        return await worker.read_context(include_screenshot=include_screenshot)

    async def ask_batch(
        self,
        prompts: list[str],
        *,
        n_parallel: int | None = None,
        equip_tools: bool = False,
    ) -> list[dict]:
        """Fan out one-shot prompts across the pool; each prompt gets its own worker.

        Real throughput is bounded by ``min(max_contexts, RAM headroom, account capacity)`` — the
        autoscaler grows the pool toward the number of concurrently pending prompts, up to that
        ceiling. A per-item failure is reported inline (``{"ok": False, "error": ...}``); it never
        aborts the rest of the batch.
        """
        sem = asyncio.Semaphore(max(1, n_parallel or len(prompts)))

        async def run_one(prompt: str) -> dict:
            async with sem:
                wid: str | None = None
                try:
                    wid = await self.spawn_worker(equip_tools=equip_tools)
                    reply = await self.ask(wid, prompt)
                    return {"ok": True, "reply": reply}
                except Exception as exc:  # noqa: BLE001 - isolate this item's failure from the batch
                    return {"ok": False, "error": f"{type(exc).__name__}: {exc}"}
                finally:
                    if wid is not None and wid in self._workers:
                        await self.close_worker(wid)

        return list(await asyncio.gather(*(run_one(p) for p in prompts)))

    # ---- code generation ---------------------------------------------------------------------

    async def write_code(
        self,
        spec: str,
        *,
        language: str | None = None,
        worker_id: str | None = None,
        equip_tools: bool = False,
    ) -> dict:
        """Have a ChatGPT worker write code for ``spec`` and return the extracted code.

        Returns ``{"ok": True, "code": str, "language": str|None, "had_fence": bool, "raw": str}``
        or ``{"ok": False, "error": str}``. ``had_fence`` is False when the worker didn't wrap the
        code in a fenced block (so ``code`` is the whole reply, best-effort — treat with suspicion).

        Pass ``worker_id`` to reuse an existing worker (e.g. to iterate: write, then ``converse`` a
        fix); omit it for a one-shot on a fresh worker that's closed automatically afterwards.
        """
        prompt = build_code_prompt(spec, language)
        spawned = worker_id is None
        wid = worker_id
        try:
            if spawned:
                wid = await self.spawn_worker(equip_tools=equip_tools)
            reply = await self.ask(wid, prompt)
        except Exception as exc:  # noqa: BLE001 - report failure to the caller, don't crash the tool
            return {"ok": False, "error": f"{type(exc).__name__}: {exc}"}
        finally:
            if spawned and wid is not None and wid in self._workers:
                await self.close_worker(wid)
        result = extract_code(reply, language)
        return {
            "ok": True,
            "code": result.code,
            "language": result.language,
            "had_fence": result.had_fence,
            "raw": result.raw,
        }

    async def write_code_batch(
        self,
        specs: list[str],
        *,
        language: str | None = None,
        n_parallel: int | None = None,
        equip_tools: bool = False,
    ) -> list[dict]:
        """Fan out several code requests across the pool; each item is a ``write_code`` result.

        A per-item failure is reported inline and never aborts the rest (same as ``ask_batch``).
        Anonymous throughput is ~1 at a time, so a big batch is effectively serialized — size it
        against pool/account capacity.
        """
        prompts = [build_code_prompt(s, language) for s in specs]
        results = await self.ask_batch(prompts, n_parallel=n_parallel, equip_tools=equip_tools)
        out: list[dict] = []
        for r in results:
            if r.get("ok"):
                cr = extract_code(r["reply"], language)
                out.append(
                    {
                        "ok": True,
                        "code": cr.code,
                        "language": cr.language,
                        "had_fence": cr.had_fence,
                        "raw": r["reply"],
                    }
                )
            else:
                out.append(r)
        return out

    # ---- job-site search / apply --------------------------------------------------------------

    _SEARCH_URL_BUILDERS = {
        "itviec": itviec_search_url,
        "linkedin": linkedin_search_url,
    }

    async def jobsite_search(
        self, site: str, query: str, *, location: str = "", remote: bool = False
    ) -> list[dict]:
        """Search one job site live and return matching leads (title/company/url/...).

        ``site`` is "itviec" or "linkedin". Uses the site's own credentialed session (see
        ``jobsite/manager.py``) -- raises ``LoginRequired`` with setup instructions if no
        storage_state has been captured for it yet.
        """
        builder = self._SEARCH_URL_BUILDERS.get(site)
        if builder is None:
            raise ValueError(f"unknown jobsite {site!r}; expected 'itviec' or 'linkedin'")
        kwargs = {"remote": remote}
        if location:
            kwargs["location" if site == "linkedin" else "city"] = location
        url = builder(query, **kwargs)

        session = await self.jobsites.session_for(site)
        driver = JobSiteDriver(site, session, self.settings)
        try:
            leads = await driver.search(url)
        finally:
            await self.jobsites.persist(site)
        return [lead.model_dump() for lead in leads]

    async def jobsite_apply(
        self, lead: dict, cv_path: str, *, cover_note: str = ""
    ) -> dict:
        """Apply to one lead (as returned by ``jobsite_search``) with the given CV file.

        Always verifies success via post-submit text/URL before reporting ``ok=True`` -- a click
        alone is never sufficient. On any CAPTCHA/bot-check, aborts that lead and reports
        ``status="skipped_captcha"`` rather than attempting to solve it. Never raises for expected
        outcomes (captcha/login-required/verification-failed); only unexpected errors propagate.
        """
        job_lead = JobLead.model_validate(lead)
        site = job_lead.source
        session = await self.jobsites.session_for(site)
        driver = JobSiteDriver(site, session, self.settings)
        try:
            result = await driver.apply(job_lead, cv_path, cover_note=cover_note)
        finally:
            await self.jobsites.persist(site)
        return result.model_dump()

    async def jobsite_read_context(self, site: str, *, include_screenshot: bool = False) -> dict:
        """Inspect a job-site session's current page -- for headless runs with no visible window."""
        session = await self.jobsites.session_for(site)
        driver = JobSiteDriver(site, session, self.settings)
        return await driver.read_context(include_screenshot=include_screenshot)

    def jobsite_status(self) -> dict:
        return {"configured_sites": self.jobsites.configured_sites()}

    # ---- status ------------------------------------------------------------------------------

    def pool_status(self) -> dict:
        pool = self._require_pool()
        if self.autoscaler is None:
            raise RuntimeError("OrchestratorCore.start() must be called first")
        return {
            **pool.stats(),
            "ema_pending": round(self.autoscaler.ema_pending, 3),
            "workers": len(self._workers),
        }

    def accounts_status(self) -> dict:
        return self.accounts.report()

    def list_workers(self) -> list[dict]:
        return [
            {
                "id": w.id,
                "account_id": w.account_id,
                "messages_sent": w.messages_sent,
                "equipped": w.equipped,
            }
            for w in self._workers.values()
        ]


__all__ = ["OrchestratorCore", "WorkerNotFound"]
