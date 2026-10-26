# Status & design decisions (paused 2026-07-18)

## Design decisions locked (supersede any older text in PLAN.md)

1. **Agent-agnostic orchestrator.** The orchestrator is an MCP **tool server**; the reasoning
   "brain" is any external agent. First-class frontends: **Claude Code** and **pi**
   (`@earendil-works/pi-coding-agent`). Both consume the *same* MCP server. ACP + Skill secondary.
2. **Claude via Claude Code subscription over MCP — no Anthropic API key.** The orchestrator holds
   **no model API key** and never instantiates an LLM client. It only exposes tools.
3. **Email aliasing = DuckDuckGo Email Protection** behind a swappable `EmailProvider`, plus an IMAP
   `InboxReader` for login OTP codes (DDG only forwards). Caveats: `@duck.com` may be blocklisted at
   ChatGPT signup; masking doesn't solve phone/SMS verification; the quack API is unofficial.
4. Single browser process, many isolated contexts, autoscaler. **One identity/account ↔ one context**
   (no double-booking). Anti-detection per `docs/PLAN.md` §6.

## Built & verified (Tasks 1–7)

All modules import cleanly (`uv run python -c "import orchestrator..."`) and each layer has an
offline verification that passed.

- **config** (`config.py`): pydantic-settings; TOML defaults + `ORCH_` env (nested `__`) + `.env`.
  Models: Browser/Scaling/Agent/Behavior/**Email** settings, AccountConfig, InwardMcpServerConfig.
- **telemetry** (`telemetry/`): structlog → **stderr** (stdout reserved for MCP stdio), metrics
  (counters/gauges/timers), failure snapshots.
- **browser** (`browser/`): `BrowserManager` (one process, channel fallback, per-context proxy
  support), `session.py` (Fingerprint, SessionSpec, `build_context_kwargs`, geo-consistent
  Accept-Language), `stealth.py` (sticky per-identity fingerprint, init-script patches, human typing),
  `context_pool.py` (acquire/release, grow/backpressure, quarantine/reap, `scale_down`),
  `autoscaler.py` (EMA + cooldown + pre-warm + demand growth).
- **accounts** (`accounts/`): `budget.py` (RollingBudget + Cooldown), `store.py` (storage_state paths
  + provisioned registry), `email_provider.py` (DDG quack API + Null), `inbox.py` (IMAP OTP reader,
  pure `extract_otp`/`message_matches`), `manager.py` (scheduler, `capacity`, `acquire` exclusive,
  `release`/`penalize`/`note_success`/`get`, `session_spec`, `report`, alias/OTP helpers).
- **chatgpt** (`chatgpt/`): `selectors.py` (all brittle selectors + fallbacks), `completion.py`
  (probe-driven settle/timeout state machine — unit tested), `driver.py` (open/guard/ask/scrape +
  typed errors + snapshots), `exceptions.py`.
- **workers** (`workers/`): `protocol.py` (ReAct parser + priming + loop — unit tested), `worker.py`
  (binds context+account+driver, budget consume per send), `inward_mcp.py` (`InwardToolHost`).

## Built & verified (Task 8)

- **server** (`server/`): `core.py` (`OrchestratorCore` — owns `AccountManager`, `BrowserManager`,
  and after `start()`, `ContextPool`+`Autoscaler`; `spawn_worker`/`ask`/`converse`/`ask_batch`/
  `close_worker`, status methods, `_fail_worker` penalize+quarantine+reap path), `schemas.py`
  (pydantic result shapes), `mcp_server.py` (FastMCP: 6 tools + `pool://status`/`accounts://status`
  resources, lifespan starts/stops the core, `run_stdio`/`run_http`). **No model/API client
  anywhere in `server/`** (grepped clean).
  - Verified offline with fakes (fake `BrowserManager.start/new_session/close`, monkeypatched
    `ChatGPTDriver.open/ask`): spawn→ask→close round-trip; `ask_batch` 3/3 ok; a raised
    `RateLimited` correctly quarantines the context, cools the account, and drops capacity by
    exactly 1 (no double-release). MCP layer verified via `mcp.shared.memory` in-process client:
    lists all 6 tools + 2 resources, round-trips `spawn_worker`/`ask`/`list_workers`/
    `close_worker` calls and both resource reads.
  - Deviation from the original sketch: `spawn_worker` dropped `mode`/`account_hint` params (see
    note in `08-orchestrator-core-and-mcp-server.md` §8.2 — the pool's zero-arg `session_factory`
    has no path to honor a per-call account hint, and `converse()` already avoids needing one by
    reusing the same `Worker`).
  - `ruff` is not yet installed/synced in this env — deferred to Task 10.5, not blocking.

## Built & verified (Task 9)

- **`cli.py`** (Typer app `orchestrator`): `serve-mcp --transport stdio|http`, `serve-acp`,
  `ask "<prompt>" [--equip-tools]` (guarded live smoke, prints ToS reminder to stderr),
  `accounts list|alloc-email|ddg-login`, `doctor` (settings/driver-import/browser-launch/
  mcp-server-build checks, PASS/FAIL per line, non-zero exit on any failure).
  - Verified: `uv run orchestrator --help` lists all 5 top-level commands; `doctor` correctly
    PASSes settings-load + mcp-server-build and FAILs driver-import + browser-launch (patchright
    not installed yet — expected, that's Task 10's job) with exit code 1; `accounts list` prints
    the synthesized `anon-default` account.
- **`server/acp_agent.py`**: `AcpAgent` (pure `handle_message(dict) -> dict|None`, one ACP session
  = one `Worker`, `session/prompt` → `core.converse`) + `run_acp_stdio` (real newline-delimited
  JSON-RPC over stdin/stdout). Verified offline against a fake-driver core: `initialize` →
  `session/new` → `session/prompt` round-trips correctly; an unknown `sessionId` returns a
  JSON-RPC error (not a crash); a notification (no `id`) correctly returns no response.
- **`examples/example_mcp_server.py`**: tiny FastMCP server with one `fetch_url(url)` tool
  (http/https only, ~20KB cap, 10s timeout). Verified via `InwardToolHost` against a local stub
  HTTP server: lists `fetch_url` in the catalog, round-trips a call.
- **`skill/SKILL.md`**: fan-out/multi-turn/tool-equip recipes over the MCP tools, incl. the
  "spawn N workers, draft variants, pick best" recipe and a capacity-checking note
  (`pool://status`/`accounts://status`) before a big `ask_batch`. No `scripts/` — genuinely thin
  now that the MCP server exists, nothing to wrap.
- Two logged deviations from the literal task-file sketch (see `09-...md` for detail): `accounts
  list` reads `AccountManager.report()` directly instead of spinning up a full `OrchestratorCore`
  (would launch the browser needlessly); `doctor`'s browser check opens a real context via
  `new_session` (not just `about:blank`) to also exercise fingerprint/stealth wiring. The optional
  proxy/IMAP/DDG-token doctor sub-checks were skipped (genuinely optional; revisit if account
  provisioning becomes painful).

## Built & verified (Task 10)

- **`tests/`**: 122 tests passing offline (1 live test correctly skipped by default) — 15 test
  files covering every pure-logic module from Tasks 1-9 (config, budget, accounts, context_pool,
  autoscaler, completion, protocol, stealth, inbox, email_provider, core, mcp_server) plus two
  files added beyond the original outline (`test_cli.py`, `test_acp_agent.py` — formalizing what
  had only been ad-hoc-verified during Task 9). `tests/fakes.py` (`FakeSession`, `FakeDriver`,
  `FakeToolHost`, `Clock`) + `tests/conftest.py` (`tiny_settings`, `fake_core`) are shared
  scaffolding. `tests/__init__.py` is required (empty) for relative imports under pytest's default
  import mode.
- **`tests/test_driver_integration.py`** + **`tests/fixtures/stub_chatgpt.html`**: the CI-safe
  integration test — drives the *real* `ChatGPTDriver` (not fakes) through a real bundled Chromium
  against a local static page shaped like chatgpt.com's DOM. Proves the actual selector/completion/
  human-type wiring, not just the fake-driver contract. Skips gracefully if no browser is installed.
- **`tests/test_live_smoke.py`**: the M1 feasibility gate, `@pytest.mark.live`, gated on
  `ORCH_LIVE=1`. **Not run** — it's a real network round-trip to chatgpt.com, left for the user to
  fire explicitly (`ORCH_LIVE=1 uv run pytest -q -m live tests/test_live_smoke.py -s`). Cloudflare
  pass/fail rate is therefore still an open question.
- **Environment now has**: `patchright` + `playwright-stealth` synced (`uv sync --extra stealth
  --group dev`), Chromium installed (`uv run patchright install chromium`), `ruff` in the dev
  group. `orchestrator doctor` now reports all 4 checks PASS, exit 0 (verified — it falls back to
  bundled Chromium since real Chrome isn't installed on this machine).
- **`ruff check src tests`**: clean. One pre-existing unused import in `accounts/manager.py` was
  removed to get there.
- **Coverage**: 79% overall; pure logic layers 85-100%, thin I/O wrappers and real-stdio
  entrypoints intentionally lower (see `10-...md` §10.5 for the itemized reasoning).
- **Root-cause fix, not a test hack**: `telemetry/logging.py`'s structlog factory used to snapshot
  `sys.stderr` once into a cached logger. Anything that temporarily swaps and later closes stderr
  (Click's `CliRunner`, `contextlib.redirect_stderr`) would then break *every* subsequent log call
  in the process with `ValueError: I/O operation on closed file`. `test_cli.py` surfaced this via
  Click's `CliRunner`; fixed with a `_LiveStderr` proxy that resolves `sys.stderr` fresh on every
  write instead of capturing a reference — a real robustness fix independent of testing.

## Built & verified (Task 11)

- **`integrations/pi/`**: `package.json` (`@modelcontextprotocol/sdk@^1.29.0`, `npm install` run
  for real via `bun` — 92 packages, `bun.lock` committed, `node_modules/` gitignored),
  `index.ts` (a minimal MCP client: `session_start` spawns `uv run orchestrator serve-mcp
  --transport stdio`, connects with the MCP TS SDK, lists tools, registers one `pi.registerTool()`
  per discovered tool that forwards `execute()` through `client.callTool()`;
  `session_shutdown` disconnects), `README.md`.
- **Corrected a wrong assumption from the original plan**: pi has **no built-in MCP client** by
  design (its own docs say so explicitly). The `--mcp-config` flag observed on the bundled
  `claude-cli` extension belongs to the separate `claude` binary it shells out to, not to pi
  itself — so a static `mcpServers` manifest (the originally-planned approach) had nothing to
  register with. The extension had to *be* the MCP client instead. `docs/PLAN.md` and
  `11-pi-agent-adapter.md` are updated to reflect this.
  - **This is also what makes pi genuinely agent-agnostic-compatible here**: any pi provider
    (not just `claude-cli`) can call the registered `chatgpt_*` tools, matching the original ask
    ("support more than just claude as orchestrator, starting with pi") more literally than the
    claude-cli-only path would have.
- **Verified empirically, not just written**: `pi -e ./integrations/pi/index.ts -p "hi" --no-tools`
  (loads extensions/registers tools without spending a real model call) proved: all 6 tools
  register with no exception; an MCP tool's raw JSON-Schema `inputSchema` is accepted directly as
  `pi.registerTool()`'s `parameters` (no TypeBox conversion needed, despite every doc example using
  `Type.Object(...)`); the full spawn → MCP handshake → `OrchestratorCore.start()` (real pre-warmed
  browser context!) → `session_shutdown` → clean teardown cycle works end to end.
  - **Found and fixed a real bug via this same test**: the MCP SDK's `StdioClientTransport` only
    inherits a safelisted environment subset by default, not the full parent env — so
    `ORCH_SCALING__MIN_WARM=0` set in the shell silently had no effect until `index.ts` was fixed
    to explicitly pass `env: {...process.env}` to the transport. Confirmed fixed by re-running.
- **Not verified**: an actual pi-model-initiated tool *call* returning a real ChatGPT reply — the
  `claude-cli` provider available in this environment is out of usage credits, and no other
  provider API key is configured here. Left for the user to run with a funded provider.

## Where we paused

**All 11 tasks now done** (Task 11's one open item — a real end-to-end tool *call* — needs a
funded model provider the user has, not more code). Nothing is queued. If resuming later:
possible next steps are the two explicitly-deferred manual verifications
(`ORCH_LIVE=1` live smoke test in Task 10; a real pi tool call in Task 11), or anything new the
user asks for.

## Key invariants to preserve while resuming

- **Every context destruction releases its account** (pool `closer` → `accounts.release`). Error
  handling additionally calls `accounts.penalize` (cooldown) — never both a manual release *and* the
  closer release for the same event (double-decrement).
- **`driver.open()` navigates to `base_url`**, which starts a fresh ChatGPT chat — so reusing an idle
  pooled context for a new worker is safe (no conversation bleed). `converse` reuses the *same* worker
  without re-opening → continues the conversation.
- Budget is consumed live in `Worker.send` (`account.budget.consume(1)`), not on release.
- Logs go to **stderr**. Never print to stdout in server mode.

## Environment facts

- `uv` present; base deps + `stealth` extra + `dev` group synced (`.venv`). Chromium **is**
  installed (`uv run patchright install chromium`, Task 10) — real Chrome is not present on this
  machine, so `BrowserManager` falls back to bundled Chromium (confirmed working via `doctor`).
- `pi` at `/opt/homebrew/bin/pi` (`@earendil-works/pi-coding-agent` 0.80.2), default provider
  `llamacpp`; has a `claude-cli` provider extension and attaches MCP via `--mcp-config`.
- Constraints: no subagents; no committing `.env`/`storage_state`/snapshots; ToS caveat is the user's.
