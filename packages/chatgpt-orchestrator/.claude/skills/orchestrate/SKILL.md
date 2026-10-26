---
name: orchestrate
description: Delegate cheap, parallelizable drudge work — research, extraction, first-pass drafts, "N variants pick the best" — to a pool of free browser ChatGPT workers via this chatgpt-orchestrator repo, then reason over and apply the results yourself. Use whenever a task benefits from many disposable LLM answers rather than one careful pass, and you want the workers to do the legwork while you stay the brain.
---

# orchestrate — drive the ChatGPT worker pool

**You are the brain. The workers are cheap, disposable ChatGPT browser contexts you fan work out
to.** This skill is the operator's playbook for actually making that work from this repo — the env
setup, the copy-paste driver, and the failure modes, all tuned to what's been verified to work here.

For the MCP-tool recipe layer (when the server is registered in a client via `.mcp.json`), see the
in-package skill at `src/orchestrator/skill/SKILL.md`. This file is the ground-truth *runbook*,
including the direct-Python path that needs no MCP registration.

> **ToS:** this automates the free ChatGPT web UI, against OpenAI's usage policies; logged-in
> accounts risk suspension. The operator made that call by running this system — don't re-litigate
> it; just expect error messages that reflect account cooldowns/challenges.

## The two golden rules (learned the hard way)

1. **Serialize anonymous work: `n_parallel=1`.** Firing several anonymous requests at once from one
   IP gets soft-throttled — sends are accepted but completions come back **empty** (surfaced as
   `EmptyReply`). One worker at a time returns full answers reliably. Parallelism only pays off once
   you have multiple accounts on distinct residential proxies.
2. **Review before you trust.** Worker output is a free-tier LLM's first pass — generic, sometimes
   outdated or wrong on specifics. Treat it as a research memo, cross-check anything load-bearing,
   and do the deciding/merging yourself. That reasoning step is yours, never a worker's.

## Environment setup (this headless, no-Chrome-channel box)

The `config/config.toml` defaults (`driver=patchright`, `channel=chrome`, `headless=false`) are
tuned for anti-detection on a real workstation. In a headless CI-like box, override them:

```bash
export ORCH_BROWSER__DRIVER=playwright   # bundled Chromium, not patchright+real-Chrome
export ORCH_BROWSER__CHANNEL=            # empty: no real Chrome channel here
export ORCH_BROWSER__HEADLESS=true       # no display
export ORCH_SCALING__MIN_WARM=0          # don't pre-warm a context you may not use
export ORCH_SCALING__MAX_CONTEXTS=2      # small; anonymous throughput is ~1 anyway
```

Anonymous access works out of the box: `browser.allow_anonymous` (default on) means chatgpt.com's
ever-present "Log in" button is treated as blocking **only** when there's no usable composer, and
the driver waits for the hydrated composer instead of racing its pre-hydration fallback textarea.

Sanity-check the browser/driver/MCP wiring before real runs: `uv run orchestrator doctor`.

## Recipe A — direct-Python fan-out (no MCP needed; proven here)

Decompose the task into 2–6 **self-contained** prompts (each worker sees only its prompt — not the
repo, not your conversation, not the other prompts), then:

```python
# scratch file, e.g. /tmp/fanout.py
import asyncio, json
from pathlib import Path
from orchestrator.config import get_settings
from orchestrator.server.core import OrchestratorCore

PROMPTS = [
    "…self-contained prompt 1 (bound the output: 'be concise', '5 bullets')…",
    "…self-contained prompt 2…",
]
OUT = Path("/tmp/fanout_out.json")

async def main():
    core = OrchestratorCore(get_settings())
    await core.start()
    try:
        print("pool:", core.pool_status())
        results = await core.ask_batch(PROMPTS, n_parallel=1)   # serialize! (rule #1)
    finally:
        await core.close()
    OUT.write_text(json.dumps([{"prompt": p, **r} for p, r in zip(PROMPTS, results)], indent=2))
    for i, r in enumerate(results, 1):
        print(f"[{i}] {'OK' if r.get('ok') else 'FAIL'}: {(r.get('reply') or r.get('error'))[:80]}")

asyncio.run(main())
```

Run it with the env from above:

```bash
ORCH_BROWSER__DRIVER=playwright ORCH_BROWSER__CHANNEL= ORCH_BROWSER__HEADLESS=true \
  uv run python /tmp/fanout.py
```

Each item returns `{"ok": true, "reply": "..."}` or `{"ok": false, "error": "..."}`; one item's
failure never aborts the batch. Persist replies to a file (as above) so results survive and you can
fold them into your real work. Then **you** read, verify, and synthesize.

Other core calls: `spawn_worker()` → `ask(wid, prompt)` / `converse(wid, msg)` (multi-turn, same
context) → `close_worker(wid)`; `list_workers()`, `pool_status()`, `accounts_status()`.

## Recipe B — via the MCP tools (when a client has the server registered)

If you're a client with this server in `.mcp.json`, drive it through the MCP tools/resources instead
of Python — `spawn_worker` / `ask` / `ask_batch` / `converse` / `close_worker`, and read
`pool://status` / `accounts://status`. The recipes for that live in `src/orchestrator/skill/SKILL.md`
(fan-out, multi-turn, tool-equip). Same golden rules apply.

## Capacity — don't ask for parallelism you don't have

Real throughput is `min(max_contexts, RAM headroom, schedulable account budget)`, not `len(prompts)`.
Before a big batch, check `core.accounts_status()` (or `accounts://status`): if every account shows
`schedulable: false`, wait for `cooldown_remaining_s`. Excess prompts queue (bounded by `max_queue`)
and return `Backpressure` when full — expected, not a crash.

## Failure modes and what they mean

| Error | Meaning | What to do |
|-------|---------|-----------|
| `EmptyReply` | Soft-throttled empty completion (usually concurrent anonymous load) | Serialize (`n_parallel=1`); retry on a fresh worker |
| `RateLimited` | Free-plan message cap hit | Account auto-cools down; wait `retry_after`, or add accounts |
| `ChallengePresented` | Cloudflare/Turnstile/"verify you're human" | Context auto-quarantined; retry later; needs residential proxy for reliability |
| `LoginRequired` | Login wall **with no usable composer** | Genuine block from this IP; anonymous is unavailable — needs a credentialed account |
| `Backpressure` | Queue full at max capacity | You over-fanned; reduce batch or wait |
| `DomChanged` | chatgpt.com UI changed | Patch `src/orchestrator/chatgpt/selectors.py` only; snapshots saved under `snapshot_dir` |

The pool already quarantines bad contexts and cools their accounts automatically — for a one-off
failure, just retry with a fresh `spawn_worker()`.

## Guardrails

- **Serialize anonymous (`n_parallel=1`); review before trust.** (The two golden rules.)
- **Self-contained, bounded prompts**, one concern each — no repo/secret/PII context to workers.
- **Attribute honestly** — if an answer came from a worker and you couldn't verify it, say so.
- **Never commit** `.env`, credentials, `storage_state`, or failure snapshots (all git-ignored).
