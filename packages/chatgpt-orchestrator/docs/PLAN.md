# Plan: Agent-Orchestrated Pool of Browser ChatGPT Workers (via MCP / Skill / ACP)

## Amendments (2026-07-18)

Steering decisions taken after the original plan; these supersede conflicting text below:

1. **Agent-agnostic orchestrator.** The orchestrator is NOT Claude-specific. It is an
   agent-agnostic **MCP tool server**; the reasoning "brain" is any external agent that connects.
   First-class frontends: **Claude Code** and **pi** (`@earendil-works/pi-coding-agent`, already
   installed). Both consume the *same* `orchestrator serve-mcp` process/command, unmodified. Claude
   Code via a plain `.mcp.json` entry (it has native MCP support). pi has **no built-in MCP
   client** (confirmed in its own docs), so `integrations/pi/` ships a small pi extension that IS a
   minimal MCP client — it spawns the server, lists its tools via the official MCP TS SDK, and
   registers one pi tool per MCP tool. (`--mcp-config` is a flag of the separate `claude` binary,
   forwarded by pi's bundled `claude-cli` *provider* extension — not a pi-native mechanism; an
   earlier draft of this plan assumed otherwise.) ACP/Skill remain secondary surfaces.
2. **Claude uses the Claude Code subscription over MCP — no Anthropic API key.** The orchestrator
   holds NO model API key; it is the tool server, not the LLM caller. Never instantiate an
   Anthropic/OpenAI model client here.
3. **Email aliasing = DuckDuckGo Email Protection.** Multi-account management mints masked
   `random@duck.com` aliases (unofficial quack API) behind a swappable `EmailProvider`; because DDG
   only forwards, login OTP codes are read via an IMAP `InboxReader` on the destination inbox.
   Caveats: `@duck.com` may be blocklisted at ChatGPT signup; masking does not solve phone/SMS
   verification; the quack API is unofficial.

Read "Claude" below as "the connected reasoning agent (Claude Code subscription, pi, or any
MCP/ACP client)".

## Context

We want a new (greenfield) Python system where **Claude is the top-level reasoning + orchestration
brain**, and a **pool of browser-driven free ChatGPT contexts** acts as a scalable fleet of cheap
"worker" LLMs. Claude connects to the system as an **MCP server** (primary) — and optionally as a
**Claude Code Skill** and/or an **ACP agent** — and uses its tools to spawn, query, fan-out to, and
recycle ChatGPT worker contexts. Each worker can *optionally* be equipped with tailored MCP tools of
its own (filesystem, web fetch, custom domain tools).

The system runs **one browser process holding as many isolated contexts as resources allow**, with an
**autoscaling algorithm**, and manages **multiple accounts** (anonymous and logged-in, configurable)
with rate-limit-aware rotation and per-account residential proxies.

Decisions locked with the user:
- **Claude = the reasoner/orchestrator.** ChatGPT browser contexts are workers Claude drives.
- **Connection surfaces:** expose the orchestrator as an **MCP server** (primary), plus **Skill** and
  **ACP** adapters, so Claude (Claude Code / claude.ai / ACP editors) can connect.
- **Inward MCP (optional, per-worker):** ChatGPT workers can consume tailored MCP tools too.
- **Stack:** Python (async). **Concurrency:** single browser, many contexts, autoscaler.
- **Auth:** configurable anonymous *and* logged-in, **with multi-account management**.

> ⚠️ **Read the Risks section first.** Automating the free ChatGPT web UI violates OpenAI's usage
> policies; logged-in accounts risk suspension; and Cloudflare/bot-detection — not the MCP wiring — is
> the central engineering challenge. These constraints drive the whole design.

---

## Architecture (data flow)

```
┌──────────────────────── Claude (reasoning + orchestration brain) ────────────────────────┐
│  connects via ONE of:                                                                     │
│   • MCP server  (stdio for Claude Code, streamable-http for claude.ai / remote)  ◄─ primary
│   • Claude Code Skill  (SKILL.md + helper scripts wrapping the MCP/CLI)                    │
│   • ACP agent  (JSON-RPC 2.0 over stdio; Zed / JetBrains editors)                          │
└───────────────────────────────────────────┬───────────────────────────────────────────────┘
             tool calls: spawn / ask / ask_batch / converse / status / close
                                             ▼
                         Orchestration core (async)
                                             │
                 Context Pool  ◄── Autoscaler (queue + RAM + account-budget aware)
                                             │
                 Session Manager ◄── Multi-Account Manager (budgets, cooldowns, proxies)
                                             │
                 ChatGPT Driver (send → await completion → scrape reply)  ── anti-detection
                                             │
                 Single Chromium/Patchright browser → N isolated worker contexts
                                             │
   (optional, per worker) ─────────────────►│ inward MCP Client → tailored MCP servers
                                              (equip a worker with fetch/fs/custom tools)
```

**Value proposition:** Claude decomposes a task, **fans it out to many free ChatGPT workers in
parallel** (cheap drudge work — generation, extraction, first-pass drafts), then reasons over and
aggregates their results. Throughput scales with contexts *and* available account budget.

## Tech stack

- **Language/runtime:** Python 3.12+, fully async (`asyncio` + Playwright async API). Deps via **uv**.
- **Browser driver:** **Patchright** (patched Playwright) primary; Playwright/Chrome-channel fallback;
  evaluate **Camoufox** if Chromium keeps getting flagged. `playwright-stealth` baseline.
- **Outward MCP server:** **FastMCP** — expose orchestration tools over **stdio** (Claude Code local)
  and **streamable-http** (remote / claude.ai), per the `mcp` v2 SDK & 2026-07-28 spec.
- **Skill:** a Claude Code skill bundle (`SKILL.md` + scripts) that documents the workflows and calls
  the MCP tools / CLI.
- **ACP adapter:** JSON-RPC 2.0 over stdio (ACP spec v1) presenting the orchestrator as an ACP agent.
- **Inward MCP client (optional):** `mcp` SDK `ClientSession`/`stdio_client` to equip workers with tools.
- **Entrypoint:** CLI (Typer) for `serve`/`ask`/`accounts`/`doctor`; the MCP/ACP servers are launch modes.
- **Config/secrets:** TOML/YAML + `.env`/keyring. **Never commit** credentials or `storage_state`.

## Repo layout

```
chatgpt-orchestrator/
  pyproject.toml                 # uv-managed
  config/config.toml             # browser, scaling, accounts, mcp_servers, agent
  .mcp.json.example              # ready-to-copy Claude Code MCP registration
  .env.example                   # secrets template (git-ignored real .env)
  src/orchestrator/
    server/
      mcp_server.py              # FastMCP: outward tools (spawn/ask/ask_batch/status/close)
      acp_agent.py               # ACP adapter (JSON-RPC 2.0 over stdio)
      schemas.py                 # tool input/output models
    skill/
      SKILL.md                   # Claude Code skill (workflows + fan-out recipes)
      scripts/                   # thin helpers invoking MCP/CLI
    browser/
      manager.py                 # launch 1 browser; own contexts
      context_pool.py            # acquire/release, lifecycle, health, recycling
      autoscaler.py              # scale up/down algorithm + metrics/EMA
      session.py                 # anonymous vs credentialed context setup, storage_state
      stealth.py                 # fingerprint, human typing/mouse, proxy wiring
    accounts/
      manager.py                 # registry, scheduler, rotation
      budget.py                  # per-account rate budget + cooldown windows
      store.py                   # secure credential/session-state storage
    chatgpt/
      driver.py                  # send message, await completion, scrape reply
      selectors.py               # ALL DOM selectors centralized (brittle → isolated)
      completion.py              # streaming-done detection
    workers/
      worker.py                  # Worker = context + account + optional inward tools
      inward_mcp.py              # optional: equip a worker with tailored MCP tools
      protocol.py                # optional: worker-side ReAct tool-call contract + parser
    telemetry/metrics.py         # structured logs, counters, failure snapshots
    cli.py                       # serve-mcp / serve-acp / ask / accounts / doctor
  examples/example_mcp_server.py # FastMCP starter tool for inward equipping (e.g. web-fetch)
  tests/
```

---

## Component detail

### 1. Outward MCP server (primary integration — how Claude drives it)
FastMCP server exposing tools Claude calls to orchestrate the worker fleet:
- `spawn_worker(mode?, account_hint?, equip_tools?)` → `worker_id`
- `ask(worker_id, prompt, wait=true)` → reply text  *(single turn on one worker)*
- `ask_batch(prompts[], n_parallel?)` → results[]  *(fan-out across the pool; autoscaler grows it)*
- `converse(worker_id, message)` → reply  *(multi-turn, keeps context)*
- `list_workers()` / `close_worker(worker_id)`
- **Resources:** `pool_status`, `accounts_status` (live counts, budgets, cooldowns) for Claude to
  reason about capacity.
Transports: **stdio** (Claude Code registers via `.mcp.json` / `claude mcp add --transport stdio`) and
**streamable-http** (remote / claude.ai). Ship a copy-paste `.mcp.json.example`.

### 2. Skill + ACP adapters (secondary surfaces)
- **Skill:** `SKILL.md` documenting fan-out/aggregation recipes and invoking the MCP tools (or CLI) —
  thin once the MCP server exists.
- **ACP adapter:** presents the orchestrator as an ACP agent (JSON-RPC 2.0 over stdio, spec v1) so
  Zed/JetBrains can drive it; maps ACP session/prompt turns onto the same orchestration core.
  *(Interpretation: ACP = Zed's Agent Client Protocol. See Open items.)*

### 3. Browser session manager + context pool (single browser, many contexts)
- Launch **one** browser process. Each unit of concurrency is a `BrowserContext` (own
  cookies/storage/fingerprint) = one worker identity/conversation → the "as many contexts as possible"
  requirement.
- `acquire()` returns an idle, healthy context bound to an available account; `release()` returns or
  recycles it. Context state: `idle|busy|quarantined`, age, message count, bound account+proxy.
- Health/recycling: detect Cloudflare/Turnstile, login wall, rate-limit banner, or DOM breakage →
  quarantine, cool down the account, recreate.

### 4. Autoscaler (resource + queue + account aware)
Tick (~2s), EMA-smoothed, with an action cooldown to avoid flapping:
```
pending      = queued asks (esp. ask_batch fan-out)
idle, busy   = context counts
mem_headroom = available_mem - total_contexts * per_context_mem_estimate * safety_factor

SCALE UP  if pending > idle and total_contexts < max_contexts and mem_headroom > 0
          and an account slot is free (not all in cooldown) → create up to max_create_per_tick
SCALE DOWN close idle contexts older than idle_ttl, keeping >= min_warm
BACKPRESSURE at max: bounded queue; return retry-after when full
```
Real capacity = min(`max_contexts`, RAM budget, Σ available account budgets).

### 5. Multi-account manager
- **Account** = `{id, mode: anonymous|credentialed, storage_state?, proxy, budget, cooldown_until, health}`.
  - *Anonymous:* ephemeral identity = fresh context + dedicated proxy (still fingerprinted → isolate).
  - *Credentialed:* stored `storage_state` from a one-time scripted login; supports several accounts.
- **Scheduler:** pick a healthy, not-cooled-down account with remaining budget (LRU/least-loaded).
  Optional **sticky** binding so a multi-turn `converse` stays on the same worker+account.
- **Budget/cooldown:** track messages per rolling window (~10/5h free, ~5/3h ad-opt-out). On limit or
  limit/challenge banner → set `cooldown_until`, stop scheduling until it clears.
- **Proxy binding:** each account pinned to a residential proxy so IP + fingerprint stay consistent.
- **Secure store:** credentials/session-state in `.env`/keyring/encrypted file, git-ignored.

### 6. Anti-detection & per-session isolation (highest technical risk — see Risks)
Two goals: keep an individual session from being flagged, **and** stop a flag on one session from
cascading to the others (correlation). Core principle: **one identity = one context + one sticky
residential IP + one consistent fingerprint + human pacing, fully isolated, auto-quarantined on any
challenge.**

- **Isolation (anti-correlation):** each worker gets its own `BrowserContext` (or `user_data_dir`) with
  isolated cookies/cache/service-workers; a **dedicated sticky residential/mobile proxy per identity**
  (never share one IP across many contexts — the #1 correlation vector); a distinct-but-internally-
  consistent fingerprint (UA, platform, screen/viewport, deviceMemory, hardwareConcurrency, WebGL
  vendor/renderer, canvas/audio, fonts). Persist each identity's `storage_state` so aged, continuous
  cookies look more human than always-fresh ones.
- **Geo consistency:** timezone/locale/`Accept-Language`/geolocation/UA-CH client hints must all match
  the proxy's region (mismatched timezone-vs-IP is a classic tell).
- **Strip automation signals:** **Patchright** or **Camoufox** (removes `navigator.webdriver`, CDP /
  `Runtime.enable` leaks that vanilla `playwright-stealth` no longer hides); real **Chrome channel**
  over bundled Chromium; match the **TLS/HTTP2 (JA3/JA4)** fingerprint to the claimed browser.
- **Environment realism:** run **headed** (or headless + xvfb) with a **real GPU/WebGL** (headless
  SwiftShader renderer is a tell).
- **Human behavior:** variable typing (60–220 ms + corrections), Bézier mouse, scroll/focus/blur,
  think-time before submit, jittered pacing between messages, and staying **well under** the account's
  rate limit (throttling below the cap cuts both bans and challenge frequency).
- **Containment (blast-radius):** detect Turnstile/CAPTCHA, "verify you're human", rate-limit banners,
  login walls → immediately quarantine that context and cool down its account+IP; never reuse a burned
  IP/fingerprint right after a challenge; retire identities with a high challenge rate. CAPTCHA solver
  integration is out of scope v1.
- **Honest limits:** persistent device fingerprinting + server-side ML correlation mean this is
  risk-*reduction*, not invisibility — "anonymous" is still tracked, and best-case pass rates are
  ~75–95%. Design for graceful cooldown, not perfection.

### 7. ChatGPT driver
Navigate `chatgpt.com`; dismiss consent / "stay logged out" modals; type with human pacing; submit.
**Completion detection** (`completion.py`): stop→send button transition + streaming-DOM settle + hard
timeout; scrape latest assistant message. Raise typed exceptions (`RateLimited`, `ChallengePresented`,
`LoginRequired`, `DomChanged`) for the pool/account manager. All selectors in `selectors.py`; snapshot
HTML + screenshot on failure.

### 8. Workers + optional inward tools (`workers/`)
A `Worker` binds a context + account. For most flows Claude does the reasoning and the worker just
answers a prompt. **Optionally**, a worker can be *equipped* with tailored MCP tools: `inward_mcp.py`
connects to configured MCP servers, and `protocol.py` runs a minimal worker-side ReAct loop (priming
with a tool catalog + a strict fenced-JSON tool-call contract + tolerant parser + bounded repair
nudges). This is secondary — Claude is the primary brain.

### 9. Entrypoint
`orchestrator serve-mcp` (stdio|http), `serve-acp`, `ask "…"` (one-off), `accounts …`,
`doctor` (verify browser/driver, proxy, stealth, MCP connectivity before real runs).

---

## Implementation roadmap (milestones)

- **M0 — Scaffold:** uv project, config loader, logging, CLI skeleton, `doctor`.
- **M1 — Driver spike (FEASIBILITY GATE):** one anonymous context does a full round-trip through
  Cloudflare and scrapes a reply. *Do this before anything else;* if it can't pass reliably, revisit
  stack/proxy before building further.
- **M2 — Minimal MCP server + Claude integration:** FastMCP with `spawn_worker` + `ask` over stdio;
  register in Claude Code via `.mcp.json`; confirm Claude can drive one worker. *Proves the core value
  early.*
- **M3 — Anti-detection hardening:** Patchright/Camoufox, residential proxies, human input, fingerprint,
  challenge detection.
- **M4 — Context pool + autoscaler:** lifecycle, health/recycle, resource+queue+account scaling,
  backpressure.
- **M5 — Multi-account manager:** registry, budgets/cooldowns, rotation, proxy binding, secure store.
- **M6 — Fan-out + resources:** `ask_batch`, `converse`, `pool_status`/`accounts_status` resources so
  Claude orchestrates the fleet in parallel.
- **M7 — Skill + ACP adapters:** `SKILL.md` recipes; ACP JSON-RPC agent; streamable-http transport for
  remote/claude.ai.
- **M8 — Optional inward worker tools:** equip workers with tailored MCP tools + worker-side ReAct.
- **M9 — Hardening:** metrics/failure snapshots, retries/backoff, tests, docs.

---

## Key risks & mitigations

1. **ToS / legal (your decision):** automating the free ChatGPT web UI violates OpenAI usage policies;
   logged-in accounts can be suspended. *Mitigation:* proceed knowingly; consider whether any path can
   use the official API (not free, but robust) — flagged, not assumed.
2. **Cloudflare / bot detection (biggest technical risk):** vanilla Playwright on a datacenter IP is
   blocked in seconds; reliable access needs residential IP + realistic fingerprint + human-paced input,
   and even then ~75–95% pass, not 100%. *Mitigation:* M1 gate, Patchright/Camoufox, proxy pool,
   quarantine+cooldown. Ongoing maintenance cost — accept it.
3. **Persistent device fingerprint:** "anonymous" ChatGPT is still tracked across cookie clears.
   *Mitigation:* per-context fingerprint + dedicated proxy per account; isolation is imperfect.
4. **DOM brittleness:** the ChatGPT UI changes often. *Mitigation:* centralized selectors, failure
   snapshots, typed `DomChanged` errors, fast selector-only patch path.
5. **Rate limits bound throughput:** ~10 msgs/5h (free) → real throughput = Σ(account budgets).
   *Mitigation:* budgets/cooldowns + honest capacity reporting via `pool_status`; scale accounts.
6. **Integration/transport gaps:** streamable-http auth, MCP v2 spec churn, ACP being editor-oriented
   (may need a thin session shim). *Mitigation:* stdio-first for Claude Code; add http/ACP after core.
7. **Memory pressure:** each context is heavy (~150–300 MB). *Mitigation:* resource-aware autoscaler.

---

## Verification

- **Claude integration (headline):** `claude mcp add --transport stdio orchestrator -- <cmd>`, then in a
  Claude Code session `/mcp` shows the server + tools; ask Claude to *"spawn 3 ChatGPT workers and have
  each draft a different intro, then pick the best"* → observe `spawn_worker`/`ask_batch` calls and
  aggregated output.
- **Unit:** autoscaler decisions on synthetic metrics; account scheduler (rotation, budget → cooldown);
  worker inward dispatcher against a mock MCP server; protocol parser (well-formed/malformed/repair).
- **Integration (CI-safe):** run the MCP server + a **stubbed/recorded** ChatGPT page (no live calls) to
  validate spawn→ask→result deterministically.
- **Manual live smoke (opt-in, guarded):** one real anonymous round-trip via `orchestrator ask "…"`.
- **Load/scaling:** drive `ask_batch` with many prompts → watch the autoscaler grow/shrink contexts and
  the account manager rotate/cooldown under budget limits (metrics + logs).
- **ACP:** launch `serve-acp` and connect from an ACP client (e.g. Zed) for a session round-trip.

## Open items / recommendations
- **Surface priority:** build **MCP-first** (works with Claude Code + claude.ai); Skill is a thin
  wrapper; ACP is an adapter for editor users. Confirm if ACP is required for v1 or can be deferred to M7.
- **ACP interpretation:** assumed **Agent Client Protocol** (Zed). Confirm if you meant something else.
- Default to **Patchright + residential proxy pool**; keep **Camoufox** as fallback. Use **uv**.
- Start credentialed accounts via a one-time scripted login that captures `storage_state`.
