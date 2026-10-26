# Task 8 — Agent-agnostic OrchestratorCore + FastMCP server

**Goal:** one shared engine (`OrchestratorCore`) wrapping the browser/pool/accounts/workers, and a
thin FastMCP server exposing it over stdio + streamable-http. This is the surface Claude Code, pi,
and any MCP client consume. **No model API key** anywhere.

Files: `src/orchestrator/server/core.py`, `src/orchestrator/server/schemas.py`,
`src/orchestrator/server/mcp_server.py`, update `src/orchestrator/server/__init__.py`.

## 8.1 — `server/core.py`: OrchestratorCore skeleton
- [x] Class holding `settings`, `AccountManager`, `BrowserManager`, and (after start) `ContextPool`,
      `Autoscaler`, `_workers: dict[str, Worker]`, `_worker_ids = itertools.count(1)`, `_started`.
- [x] `async start()` (idempotent):
  - [x] `await self.browser.start(enable_context_proxies=self.accounts.any_proxied())`
  - [x] define `session_factory()`: `acct = self.accounts.acquire(); spec = self.accounts.session_spec(acct); return await self.browser.new_session(spec)`
  - [x] define `closer(session)`: `st = self.accounts.get(session.spec.account_id); if st: self.accounts.release(st); await session.close()`
  - [x] `self.pool = ContextPool(settings, session_factory, closer=closer, capacity_provider=self.accounts.capacity)`
  - [x] `self.autoscaler = Autoscaler(self.pool, settings); await self.autoscaler.start()`
- [x] `async close()`: stop autoscaler → `await pool.close()` → `await browser.close()`.
- [x] **Verify:** construct core with 2 fake accounts + a fake `BrowserManager.new_session`
      (returns a stub session), `await start()`, assert pool/autoscaler exist, `await close()`.

## 8.2 — Worker lifecycle on the core
> **Deviation:** implemented as `spawn_worker(*, equip_tools=False)` — dropped `mode`/`account_hint`.
> The pool's `session_factory` is a zero-arg callable invoked generically by `ContextPool`/the
> autoscaler, so a per-call hint has no path to influence which account backs a *newly created*
> context. Sticky multi-turn behavior doesn't need it either: `converse()` reuses the same `Worker`
> instance rather than re-spawning, so it never re-picks an account. Revisit only if a real use case
> needs "prefer account X for a fresh spawn" (would require threading a hint through the pool).
- [x] `async spawn_worker(mode=None, account_hint=None, equip_tools=False) -> str`:
  - [x] `ctx = await self.pool.acquire(timeout=settings.agent.ask_timeout_seconds)`
  - [x] `acct = self.accounts.get(ctx.account_id)`; build `ChatGPTDriver(ctx.session, settings)`
  - [x] if `equip_tools and settings.inward_mcp_servers`: `tool_host = InwardToolHost(settings.inward_mcp_servers)` else `None`
  - [x] `wid = f"w-{next(self._worker_ids)}"`; `worker = Worker(wid, ctx, acct, driver, settings, tool_host=tool_host)`
  - [x] `await worker.open()` inside try; on `ChatGPTError` subclasses → `await self._fail_worker(worker, exc)` then re-raise
  - [x] register `self._workers[wid] = worker`; return `wid`
- [x] `async _fail_worker(worker, exc)`: `self.accounts.penalize(worker.account, type(exc).__name__)`;
      `self._workers.pop(worker.id, None)`; `await worker.close()`;
      `await self.pool.release(worker.ctx, quarantine=type(exc).__name__)`; `await self.pool.reap_quarantined()`.
- [x] `async close_worker(wid)`: pop worker; `await worker.close()`; `await self.pool.release(worker.ctx)`
      (returns context idle for reuse; account stays reserved until TTL reap). `note_success` the account.
- [x] `_get_worker(wid)` helper → raises `WorkerNotFound` (a `KeyError` subclass, message-carrying)
      if missing; `close_worker` raises the same on an unknown id.

## 8.3 — Ask / converse / fan-out
- [x] `async ask(wid, prompt, wait=True) -> str`: `w=_get_worker(wid)`; try `return await w.ask(prompt)`;
      on `ChatGPTError` → `await self._fail_worker(w, exc)`; re-raise. On success `accounts.note_success(w.account)`.
- [x] `async converse(wid, message) -> str`: same wrapping around `w.converse(message)`.
- [x] `async ask_batch(prompts, n_parallel=None) -> list[dict]`:
  - [x] `sem = asyncio.Semaphore(max(1, n_parallel or len(prompts)))`
  - [x] per prompt (under sem): spawn worker → ask → `{"ok":True,"reply":...}` else `{"ok":False,"error":...}`;
        `finally: await close_worker(wid)` (guard if spawn failed). `asyncio.gather` all.
  - [x] Fan-out relies on the autoscaler: many pending `pool.acquire` waiters → grow up to capacity.
- [x] **Verify (fakes):** monkeypatch `ChatGPTDriver.open/ask` to return canned text; drive
      `spawn_worker`/`ask`/`ask_batch` with 3 fake accounts; assert replies + that a raised
      `RateLimited` quarantines the ctx and cools the account (capacity drops).

## 8.4 — Status resources
- [x] `pool_status() -> dict`: `{**self.pool.stats(), "ema_pending": self.autoscaler.ema_pending, "workers": len(self._workers)}`.
- [x] `accounts_status() -> dict`: `self.accounts.report()`.
- [x] `list_workers() -> list[dict]`: `[{"id","account_id","messages_sent","equipped"}]`.

## 8.5 — `server/schemas.py`
- [x] Small pydantic models / TypedDicts for tool results (`AskResult`, `BatchItem`, `WorkerInfo`,
      `PoolStatus`, `AccountsStatus`). Keep loose; FastMCP infers inputs from type hints.

## 8.6 — `server/mcp_server.py` (FastMCP)
- [x] `from mcp.server.fastmcp import FastMCP`. `def build_server(core: OrchestratorCore) -> FastMCP`.
- [x] Register tools (async), each delegating to `core`:
      `spawn_worker`, `ask`, `ask_batch`, `converse`, `list_workers`, `close_worker`.
- [x] Register resources: `pool://status` → `core.pool_status()`, `accounts://status` → `core.accounts_status()`.
- [x] Tool docstrings must be crisp (they become the agent-facing schema). Note capacity/rate limits
      in `ask_batch`'s docstring so the agent reasons about throughput.
- [x] Lifespan: start `core` on server startup, close on shutdown (FastMCP lifespan/context).
- [x] `run_stdio(core)` and `run_http(core, host, port)` entrypoints (used by the CLI in Task 9).
- [x] **Verify:** in-process MCP client (`mcp.client.session.ClientSession` over an in-memory or stdio
      transport) lists tools and calls `list_workers`/`pool_status` against a core backed by fake
      drivers. No network, no real browser.

## 8.7 — Wire exports
- [x] `server/__init__.py` exports `OrchestratorCore`, `build_server`, `run_stdio`, `run_http`.
- [x] **Verify:** `uv run python -c "from orchestrator.server import OrchestratorCore, build_server"`.

## Acceptance
- [x] Fake-backed core drives spawn→ask→ask_batch→close and reports pool/accounts status.
- [x] MCP client lists all 6 tools + 2 resources and round-trips a tool call.
- [x] No LLM/model API client instantiated anywhere in `server/`.
