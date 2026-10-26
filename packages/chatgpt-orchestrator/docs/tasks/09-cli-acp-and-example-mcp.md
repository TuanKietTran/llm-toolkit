# Task 9 — CLI + ACP adapter + example inward MCP

**Goal:** launchable entrypoints and the secondary connection surfaces. Depends on Task 8.

Files: `src/orchestrator/cli.py`, `src/orchestrator/server/acp_agent.py`,
`examples/example_mcp_server.py`, `src/orchestrator/skill/SKILL.md` (+ `scripts/`).

## 9.1 — CLI (`cli.py`, Typer app `app`)
- [x] `serve-mcp --transport stdio|http --host --port`: build core, `run_stdio`/`run_http`.
      Configure logging first; **stdio mode must not print to stdout** (logs → stderr).
- [x] `serve-acp`: launch the ACP adapter (9.2).
- [x] `ask "<prompt>" [--equip-tools]`: one-shot — start core, spawn worker, print reply, close.
      This is the **guarded live smoke** path (hits real ChatGPT). Print a ToS reminder once.
- [x] `accounts list`: print an account status table.
      > Deviation: built directly off `AccountManager(settings).report()` rather than spinning up
      > a full `OrchestratorCore` (which would launch the browser) just to list accounts — same
      > data shape (`core.accounts_status()` just delegates to `accounts.report()` anyway).
- [x] `accounts ddg-login --username U`: call `DuckDuckGoEmailProvider.request_login_otp` then
      prompt for the OTP and `login()` → print the access token to store in `.env` (never write it).
- [x] `accounts alloc-email`: mint one DDG alias (sanity check the token).
- [x] `doctor`: checks, each PASS/FAIL, non-zero exit on failure:
  - [x] settings load OK; driver import (patchright/playwright) OK
  - [x] browser launches (`BrowserManager.start`) and opens a real context via `new_session`
        (stronger than `about:blank` — exercises fingerprint/stealth init-script wiring too); closes.
  - [ ] optional: proxy reachability per account; IMAP login if configured; DDG token if configured.
        Skipped for now (genuinely optional, and Task 10 hasn't installed the browser yet to test
        proxy-bound launches against). Revisit if account provisioning becomes a recurring pain point.
  - [x] MCP server builds (construct `build_server` without serving)
- [x] Register script already in `pyproject.toml` (`orchestrator = "orchestrator.cli:app"`).
- [x] **Verify:** `uv run orchestrator --help`; `uv run orchestrator doctor` (browser check may fail
      until Task 10 installs chromium — that's expected/what doctor reports).

## 9.2 — ACP adapter (`server/acp_agent.py`)
- [x] Implement an ACP agent: JSON-RPC 2.0 over stdio (ACP spec v1). Map ACP `session/new` →
      `core.spawn_worker`, `session/prompt` turns → `core.ask/converse`, teardown → `close_worker`.
- [x] Keep it a thin shim over `OrchestratorCore`; reuse the same engine as MCP.
- [x] **Verify:** hand-feed a couple JSON-RPC frames on stdin (initialize, one prompt) against a
      fake-driver core; assert well-formed responses. (Full Zed round-trip is manual/optional.)

## 9.3 — Example inward MCP (`examples/example_mcp_server.py`)
- [x] A tiny FastMCP server exposing one safe tool (e.g. `fetch_url(url)` via httpx, size-capped) so
      workers can be *equipped* end-to-end.
- [x] Document adding it to `ORCH_INWARD_MCP_SERVERS`.
- [x] **Verify:** `InwardToolHost([that server]).start()` lists the tool; `call("fetch_url", {...})`
      returns text (use a localhost stub or `example.com`).

## 9.4 — Skill bundle (`skill/SKILL.md` + `scripts/`)
- [x] `SKILL.md`: fan-out / aggregate recipes that call the MCP tools (or CLI). Thin now that MCP
      exists. Include the "spawn N workers, draft variants, pick best" recipe from PLAN Verification.
- [x] **Verify:** markdown renders; commands referenced exist.

## Acceptance
- [x] `orchestrator --help` shows all subcommands; `doctor` runs and reports each check.
- [x] ACP adapter answers initialize + one prompt against a fake core.
- [x] A worker equipped with the example MCP server completes one tool round-trip.
