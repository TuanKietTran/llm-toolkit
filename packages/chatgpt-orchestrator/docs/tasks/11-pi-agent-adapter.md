# Task 11 — pi agent adapter

**Goal:** drive the orchestrator from `pi` (`@earendil-works/pi-coding-agent`, installed at
`/opt/homebrew/bin/pi`, version 0.80.10 in this environment), not just Claude Code. Depends on
Task 8 (the MCP server is the shared surface). No Anthropic API key — pi uses its own provider
(default here resolves to `claude-cli`, which rides the Claude Code subscription with no API key
either way; pi's own default provider absent any config is `google`).

## Corrected facts about pi (the original "facts learned" bullets below were wrong)

> The original version of this file, written before actually reading pi's docs/source, assumed pi
> had a generic `--mcp-config` mechanism like Claude Code's `.mcp.json`, inferred from observing
> the bundled `claude-cli` extension. That assumption was **wrong** — corrected after reading
> `docs/extensions.md` and `~/.pi/extensions/claude-cli/index.ts` directly:

- **pi has no built-in MCP client, by design.** Straight from pi's own `docs/usage.md`: *"It
  intentionally does not include built-in MCP, sub-agents, permission popups, plan mode, to-dos,
  or background bash."* There is no `mcpServers` block pi reads and no top-level `--mcp-config`
  flag on `pi` itself.
- `--mcp-config` **is a flag of the separate `claude` binary**, forwarded by the `claude-cli`
  *provider* extension (`~/.pi/extensions/claude-cli/index.ts`) when it shells out to `claude -p
  ...`. It's real, but it's Claude Code's flag riding along inside one specific provider
  extension — not a pi-native mechanism, and it doesn't generalize to pi's other providers
  (llamacpp, Gemini, etc).
- Extensions ARE real and well-documented (`docs/extensions.md`): TS modules, `package.json`
  `"pi": {"extensions": ["./index.ts"]}`, `export default function(pi: ExtensionAPI) {...}`,
  `pi.registerTool({name, label, description, parameters, execute(toolCallId, params, signal,
  onUpdate, ctx) {...}})`. `parameters` is documented as a TypeBox `Type.Object(...)` schema in
  every example, but **empirically a plain JSON-Schema object works too** (verified — see 11.3).
- **Long-lived resources belong in `session_start`, not the top-level factory.** Docs explicitly
  warn the factory can run in invocations that never start a session; background
  processes/sockets must be started in `session_start` and torn down in `session_shutdown`.
- Install locally with `pi install <path> -l` (writes `.pi/settings.json`) or globally with
  `pi install <path>`.

## 11.1 — Decide the integration shape
- [x] ~~Option A: static `mcpServers` manifest~~ — **not possible**, pi has nothing to register it
      with (see corrected facts above).
- [x] **Option B (what got built): a pi extension that IS a minimal MCP client.** On
      `session_start` it spawns `uv run orchestrator serve-mcp --transport stdio`, connects with
      the official `@modelcontextprotocol/sdk` (TypeScript), calls `listTools()`, and registers one
      `pi.registerTool()` per discovered MCP tool that forwards `execute()` straight through
      `client.callTool()`. Fully dynamic — a new tool added to `mcp_server.py` shows up on the next
      pi session with zero changes here. Works with **any** pi provider, not just `claude-cli`,
      which is the whole point (this was the user's explicit ask: pi as a first-class orchestrator,
      not just "Claude Code with extra steps").

## 11.2 — Build `integrations/pi/`
- [x] `integrations/pi/package.json`: `"pi": {"extensions": ["./index.ts"]}`, dependency
      `@modelcontextprotocol/sdk@^1.29.0`. `npm install` run for real (via `bun`, which is what
      `node`/`npm` are aliased to in this environment) — 92 packages, `bun.lock` committed.
- [x] ~~`integrations/pi/mcp.json`~~ — dropped. There's nothing that reads a static mcpServers file
      (see 11.1); the spawn command is built programmatically in `index.ts` using
      `import.meta.url` to resolve the project root, so there's no path-templating file for users
      to hand-edit.
- [x] `integrations/pi/index.ts`: `connect()`/`disconnect()` manage a singleton
      `{client, transport}` bridge; `session_start` connects + registers tools; `session_shutdown`
      closes the client (which tears down the child process and, via the server's FastMCP
      lifespan, closes `OrchestratorCore`). An MCP tool's `inputSchema` (JSON Schema) is passed
      straight through as `parameters` — no TypeBox conversion needed (verified empirically, not
      just asserted).
- [x] `integrations/pi/README.md`: install (project-local vs global, with the pre-warm caveat for
      why project-local is recommended), usage with pi's default provider AND `--provider
      claude-cli`, configuration (env passthrough, `CHATGPT_ORCHESTRATOR_DIR` override), a
      "Quirks" section documenting everything non-obvious found while building this, and the
      "spawn 3 ChatGPT workers, draft variants, pick best" example prompt.

## 11.3 — Verify
- [x] `npm install` (`bun install`) succeeded in `integrations/pi/` — 92 packages, no errors.
- [x] Extension-load smoke test (no real model call needed —
      `pi -e ./integrations/pi/index.ts -p "hi" --no-tools`, which disables tool *calls* but not
      registration/extension loading):
      - First run (default `min_warm=1`): our MCP server subprocess started
        (`mcp_server_starting transport=stdio`), `OrchestratorCore.start()` ran for real —
        launched an actual (bundled, headed) Chromium and opened a real anonymous ChatGPT context
        via the autoscaler's pre-warm — then shut down cleanly on `session_shutdown`
        (`autoscaler_stopped` → `context_destroyed reason=pool_close` → `browser_closed` →
        `core_closed`). This is strong evidence the whole pipe (spawn → MCP handshake → tool list →
        core lifecycle → clean teardown) works end-to-end, achieved without spending a real model
        call (the actual chat completion attempt failed on an unrelated `claude-cli` "out of extra
        usage" 400 — irrelevant to the extension's correctness).
      - Added a temporary `console.error` in the registration loop to confirm all 6 tools
        (`spawn_worker`, `ask`, `ask_batch`, `converse`, `list_workers`, `close_worker`) register
        with **no exception thrown** — proving the plain-JSON-Schema-as-`parameters` approach is
        accepted by pi 0.80.10. Removed after confirming.
      - **Bug found and fixed via this same test:** tried `ORCH_SCALING__MIN_WARM=0` to skip the
        pre-warm and it had no effect — the MCP SDK's `StdioClientTransport` only inherits a
        safelisted environment subset by default (a deliberate SDK secret-leak guard), not the
        full parent env. Fixed by explicitly passing `env: {...process.env}`; re-ran and confirmed
        `MIN_WARM=0` then correctly suppressed the pre-warm (no `session_created`/`context_created`
        lines). Documented in the README's Quirks section.
      - No stray `chrome`/`orchestrator serve-mcp` processes left running after any of the above
        (`ps aux` checked clean).
- [ ] Full pi session with a real provider actually calling `chatgpt_spawn_worker` +
      `chatgpt_ask`/`chatgpt_ask_batch` end-to-end (needs a working model credential — the
      `claude-cli` provider in this environment is out of usage credits at time of writing, and no
      other provider API key is configured). The plumbing up to and including tool registration is
      proven (see above); the actual "pi's model decides to call our tool and gets a sensible
      ChatGPT reply back" step needs the user to run it with a funded/working provider.

## Acceptance
- [x] pi can list the orchestrator's MCP tools (registered as `chatgpt_*` pi tools; confirmed via
      the debug-logged registration pass, no exceptions).
- [x] The same `orchestrator serve-mcp` process/command works unchanged for both Claude Code
      (`.mcp.json`) and pi (this bridge) — proven by construction: neither integration touches
      `server/mcp_server.py`, both just point a standard MCP client at
      `uv run orchestrator serve-mcp --transport stdio`.
- [ ] An actual pi-model-initiated tool *call* completing successfully — blocked on a working model
      credential in this environment (see 11.3's last bullet); left for the user.
