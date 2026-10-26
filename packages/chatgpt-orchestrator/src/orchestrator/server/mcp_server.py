"""The outward MCP server: the surface Claude Code, pi, or any MCP client drives.

A thin FastMCP wrapper around :class:`OrchestratorCore` — every tool just delegates. No model/API
client lives here; the connected agent supplies the reasoning, this process supplies ChatGPT
browser workers as tools.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

from mcp.server.fastmcp import FastMCP

from ..telemetry import get_logger
from .core import OrchestratorCore

log = get_logger(__name__)

_INSTRUCTIONS = (
    "Orchestrates a pool of browser-driven free ChatGPT worker contexts as cheap parallel LLMs. "
    "Typical flow: spawn_worker -> ask (or ask_batch to fan out many prompts at once) -> "
    "close_worker. converse keeps a multi-turn thread going on one worker. read_context "
    "inspects a worker's live page (url/text/optional screenshot) -- useful when running "
    "headless, since there is no window to eyeball. write_code / "
    "write_code_batch have a worker write code and return the extracted code block (not prose) — "
    "review it before running. Check the pool://status and accounts://status resources to gauge "
    "current throughput/capacity before a large fan-out — real concurrency is capped by account "
    "budgets and RAM, not just prompt count. jobsite_search / jobsite_apply drive real, "
    "credentialed ITviec/LinkedIn sessions (separate from the anonymous ChatGPT pool above) to "
    "search live postings and submit real applications — jobsite_apply always verifies success "
    "via post-submit text/URL before reporting ok=true, and always skips (never solves) a "
    "CAPTCHA/bot-check, reporting status='skipped_captcha' instead. Check jobsite://status for "
    "which sites currently have a captured login before calling either."
)


def build_server(core: OrchestratorCore, *, host: str = "127.0.0.1", port: int = 8000) -> FastMCP:
    """Build the FastMCP app. ``core.start()``/``close()`` are wired to the server lifespan."""

    @asynccontextmanager
    async def lifespan(_: FastMCP) -> AsyncIterator[None]:
        await core.start()
        try:
            yield None
        finally:
            await core.close()

    mcp = FastMCP(
        "chatgpt-orchestrator",
        instructions=_INSTRUCTIONS,
        lifespan=lifespan,
        host=host,
        port=port,
    )

    @mcp.tool()
    async def spawn_worker(equip_tools: bool = False) -> str:
        """Start a new ChatGPT worker (fresh browser context + reserved account); returns its id.

        Set equip_tools=True to have the worker run its own tool-use loop against the configured
        inward MCP servers instead of a plain single-turn reply (no-op if none are configured).
        """
        return await core.spawn_worker(equip_tools=equip_tools)

    @mcp.tool()
    async def ask(worker_id: str, prompt: str) -> str:
        """Send one prompt to an existing worker and return its reply."""
        return await core.ask(worker_id, prompt)

    @mcp.tool()
    async def ask_batch(
        prompts: list[str],
        n_parallel: int | None = None,
        equip_tools: bool = False,
    ) -> list[dict]:
        """Fan out prompts across fresh workers in parallel and collect all results.

        Each result is {"ok": true, "reply": str} or {"ok": false, "error": str}; one prompt
        failing never aborts the rest of the batch. Real throughput is capped by pool/account
        capacity (see pool://status) — asking for more n_parallel than that just queues, it does
        not create more contexts than accounts allow.
        """
        return await core.ask_batch(prompts, n_parallel=n_parallel, equip_tools=equip_tools)

    @mcp.tool()
    async def converse(worker_id: str, message: str) -> str:
        """Continue a multi-turn conversation on an existing worker (keeps prior context)."""
        return await core.converse(worker_id, message)

    @mcp.tool()
    async def read_context(worker_id: str, include_screenshot: bool = False) -> dict:
        """Inspect a worker's current page: {"url": str, "text": str, "screenshot_b64"?: str}.

        For headless runs (``browser.headless=true``) there is no visible window to check, so use
        this to see what a worker is actually looking at -- e.g. after a suspicious reply, before
        trusting a scrape, or to debug a stuck worker. screenshot_b64 is a base64 PNG, omitted
        unless include_screenshot=True (keeps the common case cheap).
        """
        return await core.read_context(worker_id, include_screenshot=include_screenshot)

    @mcp.tool()
    async def write_code(
        spec: str,
        language: str | None = None,
        worker_id: str | None = None,
    ) -> dict:
        """Have a ChatGPT worker write code for `spec`; returns the extracted code, not prose.

        Prompt it with what you want built (optionally name the `language`); the worker's reply is
        parsed down to its fenced code block. Returns {"ok": true, "code": str, "language": str|null,
        "had_fence": bool, "raw": str} or {"ok": false, "error": str}. had_fence=false means the
        worker didn't fence the code, so `code` is the whole reply — review it before trusting it.

        Pass an existing `worker_id` to iterate (write, then `converse` a fix on the same worker);
        omit it for a one-shot on a fresh worker that's closed automatically. Always review
        worker-written code yourself before running it.
        """
        return await core.write_code(spec, language=language, worker_id=worker_id)

    @mcp.tool()
    async def write_code_batch(
        specs: list[str],
        language: str | None = None,
        n_parallel: int | None = None,
    ) -> list[dict]:
        """Fan out several code requests; each item is a `write_code` result (ok/code or ok/error).

        One item failing never aborts the rest. Anonymous throughput is ~1 at a time, so a large
        batch is effectively serialized — size it against pool/account capacity.
        """
        return await core.write_code_batch(specs, language=language, n_parallel=n_parallel)

    @mcp.tool()
    async def list_workers() -> list[dict]:
        """List currently live workers: id, account_id, messages_sent, equipped."""
        return core.list_workers()

    @mcp.tool()
    async def close_worker(worker_id: str) -> str:
        """Close a worker and free its context back to the pool for reuse."""
        await core.close_worker(worker_id)
        return f"closed {worker_id}"

    @mcp.tool()
    async def jobsite_search(
        site: str, query: str, location: str = "", remote: bool = False
    ) -> list[dict]:
        """Search live postings on a job site. site: "itviec" or "linkedin".

        Returns a list of {source, title, company, url, snippet, date, location, remote} leads
        with real posting URLs -- pass one straight into jobsite_apply. Uses that site's real
        credentialed session; raises if no session has been captured yet for it (see
        jobsite://status and jobsite/manager.py's capture_session instructions).
        """
        return await core.jobsite_search(site, query, location=location, remote=remote)

    @mcp.tool()
    async def jobsite_apply(lead: dict, cv_path: str, cover_note: str = "") -> dict:
        """Apply to one lead (from jobsite_search) using the CV at cv_path.

        cv_path should be a freshly-cloned/renamed PDF, never the shared base CV. Returns
        {ok, verified, lead_url, status, detail, evidence_text, screenshot_path, cv_path}.
        status is one of "submitted" (ok=true, verified), "skipped_captcha" (a CAPTCHA/bot-check
        was hit -- always skipped, never solved), "skipped_login_required", or "failed". A submit
        click alone never produces ok=true -- this only reports success after seeing real
        confirmation text or a URL change, so check `verified` before counting an application as
        sent.
        """
        return await core.jobsite_apply(lead, cv_path, cover_note=cover_note)

    @mcp.tool()
    async def jobsite_read_context(site: str, include_screenshot: bool = False) -> dict:
        """Inspect a job-site session's current page: {"url": str, "text": str, "screenshot_b64"?: str}.

        Same purpose as chatgpt read_context -- these sessions run headless too, so use this to
        see what jobsite_search/jobsite_apply actually saw, e.g. after a suspicious or ambiguous
        result.
        """
        return await core.jobsite_read_context(site, include_screenshot=include_screenshot)

    @mcp.resource("jobsite://status")
    def jobsite_status() -> dict:
        """Which job-site accounts are configured (i.e. have a storage_state path set)."""
        return core.jobsite_status()

    @mcp.resource("pool://status")
    def pool_status() -> dict:
        """Live context pool stats: idle/busy/quarantined counts, capacity, EMA pending demand."""
        return core.pool_status()

    @mcp.resource("accounts://status")
    def accounts_status() -> dict:
        """Live account fleet stats: capacity, and per-account budget/cooldown/health."""
        return core.accounts_status()

    return mcp


async def run_stdio(core: OrchestratorCore) -> None:
    """Serve over stdio (Claude Code / pi local registration). Logs go to stderr, never stdout."""
    server = build_server(core)
    log.info("mcp_server_starting", transport="stdio")
    await server.run_stdio_async()


async def run_http(core: OrchestratorCore, *, host: str = "127.0.0.1", port: int = 8000) -> None:
    """Serve over streamable-http (remote/claude.ai clients)."""
    server = build_server(core, host=host, port=port)
    log.info("mcp_server_starting", transport="streamable-http", host=host, port=port)
    await server.run_streamable_http_async()


__all__ = ["build_server", "run_http", "run_stdio"]
