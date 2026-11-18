# chatgpt-orchestrator

Claude is the reasoning/orchestration brain; a pool of **browser-driven free ChatGPT contexts** acts
as a fleet of cheap worker LLMs. The orchestrator is exposed to Claude as an **MCP server** (primary),
with **Skill** and **ACP** adapters as alternative connection surfaces. It runs one browser process
holding many isolated contexts with an autoscaler, and manages multiple accounts (anonymous or
logged-in) with rate-limit-aware rotation and per-account residential proxies.

> ⚠️ **Read `docs/PLAN.md` first.** Automating the free ChatGPT web UI violates OpenAI's usage
> policies; logged-in accounts risk suspension; Cloudflare/bot-detection is the central engineering
> challenge. This project is for authorized personal experimentation only.

## Status

Early scaffold. The full design and milestone roadmap live in [`docs/PLAN.md`](docs/PLAN.md).

## Layout

```
src/orchestrator/
  server/    # outward MCP server (FastMCP) + ACP adapter + tool schemas
  skill/     # Claude Code skill bundle (SKILL.md + helper scripts)
  browser/   # single-browser manager, context pool, autoscaler, session, stealth
  accounts/  # multi-account manager: budgets, cooldowns, rotation, secure store
  chatgpt/   # ChatGPT web driver: selectors, completion detection, scraping
  workers/   # worker abstraction + optional inward MCP tools + ReAct protocol
  telemetry/ # structured logging + metrics + failure snapshots
  cli.py     # serve-mcp / serve-acp / ask / accounts / doctor
```

## Quickstart (once implemented)

```bash
uv sync --extra all
uv run patchright install chromium   # or: uv run playwright install chromium
cp .env.example .env                 # fill in accounts/proxies
uv run orchestrator doctor           # verify browser/driver/proxy/MCP connectivity

# register with Claude Code:
cp .mcp.json.example .mcp.json
claude mcp add --transport stdio chatgpt-orchestrator -- uv run orchestrator serve-mcp
```

### Headed browser on a headless host

Chromium launches **headed** by default (`headless: false` in `config.py`) — a real, visible
browser window is deliberately harder for Cloudflare/bot-detection to flag than a headless one.
On a Linux host/container with no display server this fails with `Missing X server or $DISPLAY`.
Fix by installing a virtual display, not by flipping `headless: true`:

```bash
apt-get install -y xvfb xauth
xvfb-run -a uv run orchestrator doctor   # prefix any command that touches the browser
```

## Optional host-VPN guard

The orchestrator can fail closed unless an operator-managed VPN interface is already up. It never
starts a VPN or changes host routes:

```bash
ORCH_NETWORK__VPN_REQUIRED=true
ORCH_NETWORK__VPN_INTERFACE=wg0
uv run orchestrator doctor
```

Use the actual interface name from the host (for example `wg0`, `tun0`, or `tailscale0`). Per-account
HTTP/SOCKS proxy configuration remains separate.

## Job CV & Finder workflow

`job-cv-finder/` is a pi-driven campaign on top of this server: live ITviec/LinkedIn search, scoring
and CV tailoring on the worker pool, then `jobsite_apply`. Fill `leads/leads.json` per
`INGEST_VIA_CHROME.md` (schema: `leads/leads.example.json`), set `CANDIDATE_NAME` (env or
`job-cv-finder/.env.local`), then `job-cv-finder/run.sh` (Linux/macOS) or `job-cv-finder/run.ps1`
(Windows; `-Provider llama-swap -Model gemma4-12b` for a local model). `leads.json` and `.env.local`
are gitignored: they hold your real application history.
