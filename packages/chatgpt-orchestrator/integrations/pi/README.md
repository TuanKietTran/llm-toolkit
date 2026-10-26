# chatgpt-orchestrator for pi

Drives the [chatgpt-orchestrator](../../README.md) MCP server from
[`pi`](https://github.com/earendil-works/pi-coding-agent), not just Claude Code.

## Why this exists: pi has no built-in MCP client

Unlike Claude Code, pi **intentionally does not include built-in MCP support** (straight from its
own docs: *"It intentionally does not include built-in MCP, sub-agents, permission popups, plan
mode, to-dos, or background bash."*). There is no `--mcp-config` flag on `pi` itself and no
`mcpServers` block in `.pi/settings.json` that pi will read.

The `--mcp-config` flag you may see referenced elsewhere (e.g. in the bundled `claude-cli`
provider extension) is a flag of the separate **`claude` binary** that extension shells out to —
it's forwarded, not native to pi, and it only exists for whatever provider extension chooses to
pass it along. It doesn't generalize to pi's own providers (llamacpp, Gemini, etc).

So instead of a static `mcpServers` manifest, **this extension IS a minimal MCP client**: on
`session_start` it spawns `uv run orchestrator serve-mcp --transport stdio` as a child process,
connects to it with the official MCP TypeScript SDK, lists its tools, and registers one pi tool
per MCP tool (`chatgpt_spawn_worker`, `chatgpt_ask`, `chatgpt_ask_batch`, `chatgpt_converse`,
`chatgpt_list_workers`, `chatgpt_close_worker`) that forwards calls straight through. New tools the
server adds later show up automatically on the next session — nothing here is
orchestrator-specific beyond the spawn command, so it works with **any** pi provider, not just
`claude-cli`.

## Install

Project-local (recommended — see the pre-warm caveat below):

```bash
cd /path/to/your/project
pi install /path/to/chatgpt-orchestrator/integrations/pi -l
```

Global (available in every pi session):

```bash
pi install /path/to/chatgpt-orchestrator/integrations/pi
```

Either way, `npm install` (or `bun install`) must have been run once inside
`integrations/pi/` to fetch `@modelcontextprotocol/sdk` — already done if you're working from this
repo checkout; `pi install` does not run it for you.

Verify:

```bash
pi list   # should show pi-extension-chatgpt-orchestrator
```

## Use

With pi's default provider, or any provider — the bridge doesn't care which model is driving:

```bash
pi "spawn 3 ChatGPT workers, have each draft a different one-paragraph intro for a blog post about
tide pools, then pick the best one and tell me why"
```

pi's model calls `chatgpt_spawn_worker` / `chatgpt_ask_batch` / `chatgpt_close_worker` the same way
it calls its built-in `read`/`bash`/`edit` tools — no special syntax needed, just ask for it.

With the bundled `claude-cli` provider (rides your Claude Code subscription, no API key):

```bash
pi --provider claude-cli "spawn 3 ChatGPT workers, draft variants, pick the best"
```

This works identically — the bridge doesn't know or care which provider is active. It's proof the
underlying MCP server is genuinely agent-agnostic: the exact same `orchestrator serve-mcp` process
also serves Claude Code via a plain `.mcp.json` entry, unchanged.

## Configuration

The child process inherits your full shell environment (not just a safelist — see Quirks below),
so the usual `ORCH_*` env vars and `.env` file (loaded by the Python process itself) both work:

```bash
ORCH_SCALING__MAX_CONTEXTS=4 pi "..."
```

Override the project directory the bridge spawns `uv run orchestrator` from (defaults to two
directories up from this extension, i.e. the repo root) via `CHATGPT_ORCHESTRATOR_DIR`:

```bash
CHATGPT_ORCHESTRATOR_DIR=/path/to/chatgpt-orchestrator pi "..."
```

### Local-model harness

The package also loads `local-model-harness.ts`. It is intentionally limited to local providers
(`ollama`, `llama.cpp`, `llamacpp`, and `local` by default): each provider request receives a fresh
output budget, while the model server remains warm and its prompt/KV cache can be reused.

The preferred configuration is Pi's model definition: set `maxTokens` in `~/.pi/agent/models.json`
or a project model configuration. The harness uses that value automatically. `PI_TURN_MAX_TOKENS`
is only an override; if neither is set, it falls back to 4096. Similarly, Ollama's normal keep-alive
policy is used unless `PI_OLLAMA_KEEP_ALIVE` is explicitly set.

```bash
PI_LOCAL_PROMPT_CACHE=1 \
pi --provider ollama --model gemma4:12b --thinking medium "inspect this project and fix the failing test"
```

For llama.cpp, the harness adds `cache_prompt: true`; for Ollama, it adds `keep_alive` so model
weights stay resident when explicitly configured. Pi's `--thinking off|minimal|low|medium|high|xhigh`
controls model effort; `PI_MODEL_EFFORT` can override it for automation and causes Ollama's `think`
flag to be set. The harness also sends `x-pi-prompt-cache-key`, `x-pi-turn`, and related metadata,
which a local proxy can use for cache accounting. Override the provider allow-list with
`PI_LOCAL_PROVIDERS=ollama,my-proxy`, or disable prompt-cache fields with
`PI_LOCAL_PROMPT_CACHE=0`. The harness does not send cache fields to cloud providers.

In an interactive Pi session, these settings can also be changed without environment variables:

```text
/local-harness                 # show current settings
/local-budget 2048             # reset each request to 2048 output tokens
/local-budget auto              # return to model/default maxTokens
/local-effort high              # off|minimal|low|medium|high|xhigh|auto
/local-keepalive 30m            # Ollama only; use off or default
/local-cache off                # disable cache hints for this session
```

These slash-command changes are session-local and reset when Pi restarts.

## Quirks / things that surprised us building this

- **pi has no native MCP support.** See "Why this exists" above — this is the load-bearing fact
  that shaped the whole design. Don't expect a `--mcp-config` equivalent on plain `pi`.
- **Connecting pre-warms a real browser context.** `orchestrator serve-mcp`'s server lifespan
  calls `OrchestratorCore.start()` on connect, which (per `scaling.min_warm`, default `1`)
  immediately launches a browser and opens a real anonymous ChatGPT context — *before* any
  `chatgpt_*` tool is ever called. Since pi fires `session_start` on **every** pi invocation (not
  just ones that end up using this extension), installing it **globally** means every `pi` command
  you run opens a browser window in the background for a second or two. Two ways to avoid that:
  - Install **project-local** (`-l`) only in projects where you actually want ChatGPT workers, or
  - Set `ORCH_SCALING__MIN_WARM=0` to disable pre-warming (contexts are then created on first
    demand instead, adding a few seconds of latency to the first `chatgpt_ask`/`chatgpt_ask_batch`
    call instead of paying it upfront at session start).
- **The MCP SDK's stdio transport does NOT inherit your full environment by default** — only a
  safelisted subset (a deliberate secret-leak guard in the SDK). We explicitly pass
  `env: {...process.env}` in `index.ts` so `ORCH_*` shell overrides behave the same here as they
  do for a directly-invoked `uv run orchestrator ...`; if you fork this and drop that, shell-level
  overrides will silently stop reaching the orchestrator (`.env`-file config is unaffected either
  way, since that's read straight off disk by the Python process).
- **An MCP tool's `inputSchema` is plain JSON Schema and pi accepted it as-is** for
  `pi.registerTool()`'s `parameters` field — no conversion to TypeBox's `Type.Object(...)` needed,
  even though pi's own docs/examples always show TypeBox. Verified empirically against pi 0.80.10.
- **`--no-tools` disables tool *calls*, not tool *registration*/extension loading** — useful for a
  quick "does this extension even load" smoke test without spending a real model call:
  `pi -e ./integrations/pi/index.ts -p "hi" --no-tools`.

## Example prompt

> spawn 3 ChatGPT workers, draft variants, pick best

Watch for `chatgpt_spawn_worker` / `chatgpt_ask_batch` calls in pi's tool-call rendering, followed
by pi's own model comparing and picking a winner from the returned drafts.
