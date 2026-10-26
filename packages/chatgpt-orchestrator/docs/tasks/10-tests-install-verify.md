# Task 10 — Tests, install, verification

**Goal:** a real pytest suite over the offline-testable logic, browser install, and guarded live
smoke. Most of the ad-hoc `uv run python - <<PY` checks used during the build become permanent tests.

Files: `tests/` (see below), maybe `tests/conftest.py`, `tests/fakes.py`.

## 10.1 — Test scaffolding
- [x] `tests/fakes.py`: `FakeSession` (has `.spec.account_id`, async `close`), `FakeDriver`
      (canned `open`/`ask`), `FakeToolHost`, controllable `Clock`, fake httpx transport helpers.
- [x] `tests/conftest.py`: fixtures for `Settings` with tiny scaling/budget knobs; a core backed by fakes.
      > `tests/__init__.py` was also added (empty) so `conftest.py`/tests can use package-relative
      > imports of `tests/fakes.py` under pytest's default "prepend" import mode.

## 10.2 — Unit tests (no browser/network)
- [x] `test_config.py`: TOML defaults, `ORCH_` nested override, JSON accounts, `base_url` slash,
      validation errors (bad ema, min_warm>max_contexts, credentialed w/o storage_state, keystroke
      ordering). Accept-Language geo derivation ended up in `test_stealth.py` instead — that's
      where `build_context_kwargs` actually lives, not `config.py`.
- [x] `test_budget.py`: rolling window prune, `remaining`, `retry_after`; cooldown trigger/clear/
      "trigger only extends".
- [x] `test_accounts.py`: `acquire` exclusive (no double-book), `capacity` = in_use+free,
      `penalize`→cooldown→retire after N strikes, `release`, `retry_after` (zero when something's
      schedulable, soonest-cooldown when everything's cooling, None when all dead), default anon synth.
- [x] `test_context_pool.py`: grow to max, backpressure at max_queue/timeout (+ waiter cleanup),
      LRU reuse, quarantine+reap (closer invoked), `scale_down` TTL keeping `min_warm`,
      capacity_provider cap, mem-headroom cap, acquire-on-closed-pool.
- [x] `test_autoscaler.py`: `tick` pre-warms to `min_warm`, grows on EMA demand, respects the action
      cooldown (proven by forcing renewed demand across two ticks), caps at `max_create_per_tick`,
      stops when the pool can't grow, `scale_down` always runs even while the growth cooldown is active.
- [x] `test_completion.py`: fast cached reply, streaming→settle, timeout(+partial text), never-starts
      -within-grace. Virtual clock + scripted probes, no Playwright.
- [x] `test_protocol.py`: parser across formats (tool_call fence/json fence/bare object/missing
      args/final-text/malformed/empty), `looks_like_tool_attempt`, `strip_final`, `build_priming`,
      full `run_react` loop (single call, repair-then-succeed, immediate final answer, max_steps cutoff).
- [x] `test_stealth.py`: `generate_fingerprint` sticky-by-seed + varies-by-seed + internally
      consistent per OS profile + viewport<screen; `parse_proxy` (full url/bare host:port/empty);
      `build_context_kwargs` geo consistency (default, locale-override re-derives Accept-Language,
      explicit accept_language wins, proxy included/omitted); init script patch contents.
- [x] `test_inbox.py`: `extract_otp` (6-digit, ignores longer embedded numbers, none-found, empty),
      `message_matches` (from-contains, alias-in-to, neither, case-insensitive), `find_otp` against
      a fake IMAP4-shaped connection (found + flagged-seen, and timeout-when-nothing-matches).
- [x] `test_email_provider.py`: DDG `allocate` via `httpx.MockTransport` (success, missing-address,
      HTTP error), `request_login_otp`/`login` flow, missing-access-token requires a ValueError,
      `NullEmailProvider` raises, `build_email_provider` for both `none` and `duckduckgo`.
- [x] `test_core.py`: fake-backed core start/close, spawn→ask→close, `converse` reuses the same
      context, `ask_batch` all-succeed + isolates-a-single-failure, `RateLimited` quarantines the
      context and cools the account by exactly one (no double-release), unknown-worker errors,
      `pool_status`/`accounts_status`/`list_workers` shapes.
- [x] `test_mcp_server.py`: in-memory MCP client (`mcp.shared.memory`) lists all 6 tools + 2
      resources, round-trips `spawn_worker`/`ask`/`list_workers`/`close_worker`, resources reflect
      live pool/account state, `ask_batch` round-trip.
      > Gotcha worth remembering: FastMCP renders a `list[dict]` tool return as one `TextContent`
      > block *per item* (so `content[0].text` is just the first item's JSON, not the whole array);
      > the full Python return value is mirrored in `result.structuredContent["result"]` — use that
      > for list-returning tools, not `content[0].text`.
- [x] *(added, not in the original outline)* `test_cli.py`: `--help`/`accounts --help` list
      commands, invalid `--transport` exits 2, `accounts list` shows the synthesized default,
      `doctor` reports every check and — when a browser binary is actually installed — all-PASS
      with exit 0 (skips gracefully otherwise). Via `typer.testing.CliRunner`.
- [x] *(added, not in the original outline)* `test_acp_agent.py`: formalizes what was ad-hoc-verified
      earlier — `initialize`/`session/new`/`session/prompt` round trip, multi-block prompt text is
      concatenated, unknown session → JSON-RPC error (not a crash), unsupported method → error,
      notifications return `None`, `close()` tears down every open session.

## 10.3 — Browser install + live smoke (guarded, opt-in)
- [x] Installed via `uv sync --extra stealth --group dev` (patchright + playwright-stealth + ruff)
      then `uv run patchright install chromium`. Real Chrome isn't installed on this machine, so
      `BrowserManager` falls back to bundled Chromium — confirmed working (see `doctor` below).
- [x] `tests/test_live_smoke.py`: `@pytest.mark.live`, skipped unless `ORCH_LIVE=1`. One test:
      anonymous round-trip via `OrchestratorCore` (the **M1 feasibility gate**).
      > **Not executed.** Running it means real outbound traffic to chatgpt.com through an
      > anonymous session — a live, hard-to-fully-reverse action against a third party — so it was
      > left for the user to run explicitly: `ORCH_LIVE=1 uv run pytest -q -m live
      > tests/test_live_smoke.py -s`. Cloudflare pass/fail rate is therefore still undetermined;
      > this is the next thing to check by hand before relying on this for real work.
- [ ] Record pass/fail rate through Cloudflare — pending the manual run above.

## 10.4 — CI-safe integration (no live calls)
- [x] `tests/fixtures/stub_chatgpt.html` + `tests/test_driver_integration.py`: a static local page
      matching `selectors.py` (contenteditable composer, send/stop button flip, incrementally
      "streamed" assistant message) driven by the **real** `ChatGPTDriver` (not fakes) through a
      real (bundled) Chromium via `BrowserManager`. Proves the actual selector/completion/human-type
      wiring works, without touching chatgpt.com. Skips gracefully if no browser binary is present.

## 10.5 — Run + tidy
- [x] `uv run pytest -q` green (excluding `live`): **122 passed, 1 skipped** in ~2-8s depending on
      whether the browser-backed tests run.
- [x] `uv run ruff check src tests` clean. One pre-existing unused import
      (`dataclasses.field` in `accounts/manager.py`, left over from an earlier refactor) was
      removed as part of getting to a clean baseline.
- [x] Coverage sanity (`--cov=orchestrator`): **79%** overall; the pure logic layers (budget,
      protocol, stealth, config, context_pool, autoscaler, completion) sit at 85-100%. Lower spots
      are expected, not gaps to chase: `cli.py`/`acp_agent.py`'s real-stdio entrypoints and
      `schemas.py` (unused pydantic shapes, kept for future structured-output use) aren't exercised
      by unit tests; `driver.py`'s failure/snapshot branches beyond the stub DOM's happy path aren't
      hit; `store.py`/`inward_mcp.py`/`snapshots.py` are thin I/O wrappers already covered indirectly
      (store via `accounts.py`'s default-synth path, inward_mcp via the example-MCP verification
      in Task 9, snapshots by design only fires on driver failures).
- [x] *(root-cause fix surfaced by `test_cli.py`)* `telemetry/logging.py` used to snapshot
      `sys.stderr` once into a cached structlog `PrintLogger`. Click's `CliRunner` (used by
      `test_cli.py`) temporarily swaps and then closes `sys.stderr`; once that happened, every
      later log call in the process raised `ValueError: I/O operation on closed file` — a real
      robustness bug (would bite equally under `contextlib.redirect_stderr`), not a test-only
      artifact. Fixed with a small `_LiveStderr` proxy that looks up `sys.stderr` fresh on every
      write instead of capturing a reference.

## Acceptance
- [x] `uv run pytest -q` passes offline (122 passed, 1 skipped); `ruff` clean.
- [x] Chromium installed; `orchestrator doctor` browser check PASSes (verified: all 4 checks PASS,
      exit code 0).
- [ ] (Manual) one live anonymous round-trip succeeds, or its failure mode is documented — **left
      for the user to run** (`ORCH_LIVE=1 uv run pytest -q -m live tests/test_live_smoke.py -s`),
      since it's a real network action against chatgpt.com and not something to fire automatically.
