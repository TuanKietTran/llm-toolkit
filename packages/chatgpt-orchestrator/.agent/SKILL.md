---
name: chatgpt-orchestrator
description: Agent-agnostic guide to driving this repo's pool of free browser ChatGPT workers. For ANY reasoning agent (Claude, a local model, an autonomous loop, an editor agent) connecting through ANY surface (MCP, ACP, CLI, or the embedded Python core). Use it to offload cheap, parallelizable work — research, extraction, first-pass drafts, "N variants pick the best" — to disposable workers while you stay the decision-maker.
---

# chatgpt-orchestrator — a guide for any agent

This is the **host-neutral** skill: it assumes nothing about which agent framework you are or which
surface you connect through. For host-specific runbooks see `.claude/skills/orchestrate/SKILL.md`
(Claude Code operator runbook, with this-environment env flags) and `src/orchestrator/skill/SKILL.md`
(MCP-tool recipe layer). This file is the shared model underneath both.

## Mental model

The orchestrator core is **agent-agnostic by design** — it contains no model and no LLM API client.
It owns one browser process holding many isolated ChatGPT contexts, an autoscaler, and a
multi-account manager. **You** are the reasoning brain; the ChatGPT contexts are cheap, disposable
**workers** you fan work out to and then reason over. The value is throughput: decompose a task,
delegate the drudge parts to many workers, aggregate and decide yourself.

> **Terms of service:** this automates the free ChatGPT web UI, which is against OpenAI's usage
> policies; logged-in accounts risk suspension. The operator accepted that by running the system —
> don't re-litigate it. Expect errors that reflect account cooldowns and bot-detection challenges.

## Choose your surface

You reach the same orchestration core through whichever of these fits your runtime:

- **MCP server** (primary). `orchestrator serve-mcp --transport stdio` for a local client, or
  `--transport http` for remote. Register it in your client, then call its tools/resources.
- **ACP agent.** `orchestrator serve-acp` speaks JSON-RPC 2.0 over stdio (Agent Client Protocol) for
  editor agents (Zed/JetBrains-style). Session/prompt turns map onto the same core.
- **CLI / one-shot.** `orchestrator ask "..."` for a single round-trip; `orchestrator accounts ...`
  to manage accounts; `orchestrator doctor` to check browser/driver/MCP wiring before real runs.
- **Embedded.** Import `OrchestratorCore` from `orchestrator.server.core` and drive it in-process
  (`await core.start()` … `await core.ask_batch(...)` … `await core.close()`). No server needed.

Whatever the surface, the operations and the invariants below are identical.

## The operation contract (surface-independent)

| Operation | Purpose | Returns |
|-----------|---------|---------|
| `spawn_worker(equip_tools?)` | Acquire one worker (a context bound to an account) | a worker id |
| `ask(worker_id, prompt)` | One-shot prompt on one worker | reply text |
| `converse(worker_id, message)` | Multi-turn on the same worker (keeps context) | reply text |
| `ask_batch(prompts[], n_parallel?)` | Fan out one-shot prompts across the pool | per-item `{ok, reply|error}` |
| `list_workers()` / `close_worker(id)` | Inspect / release workers | — |
| pool status / accounts status | Live capacity, budgets, cooldowns (resources or methods) | status object |

`ask_batch` isolates failures per item — one prompt failing (rate limit, challenge) never aborts the
rest of the batch.

## Universal invariants (true on every surface)

1. **Serialize anonymous work (`n_parallel=1`).** Concurrent anonymous requests from one IP get
   soft-throttled: sends succeed but completions come back empty. One worker at a time is reliable.
   Real parallelism needs multiple accounts on distinct residential proxies.
2. **You review before you trust.** Worker output is a free-tier first pass — cross-check anything
   load-bearing; the deciding/merging is yours, never a worker's.
3. **Capacity is `min(max_contexts, RAM headroom, schedulable account budget)`,** not `len(prompts)`.
   Check accounts status before a big fan-out; excess prompts queue (bounded) and then backpressure.
4. **Prompts are self-contained.** A worker sees only its prompt — not your context, not the repo,
   not the other prompts. Bound the output ("be concise", "5 bullets"). One concern per prompt.
5. **Never send secrets or PII to a worker**, and never commit `.env`, credentials, `storage_state`,
   or failure snapshots (all git-ignored).

## Failure taxonomy (same meanings everywhere)

| Signal | Meaning | Response |
|--------|---------|----------|
| empty reply / `EmptyReply` | Soft-throttled empty completion (usually concurrent anonymous load) | Serialize; retry on a fresh worker |
| `RateLimited` | Free-plan message cap hit | Account auto-cools down; wait its `retry_after`, or add accounts |
| `ChallengePresented` | Cloudflare/Turnstile/"verify you're human" | Context auto-quarantined; retry later; residential proxy for reliability |
| `LoginRequired` | Login wall **with no usable composer** | Anonymous unavailable from this IP; needs a credentialed account |
| `Backpressure` | Queue full at max capacity | Over-fanned; reduce batch or wait |
| `DomChanged` | chatgpt.com UI changed | Patch `src/orchestrator/chatgpt/selectors.py` only; snapshots saved under the snapshot dir |

The pool quarantines bad contexts and cools their accounts automatically; for a one-off failure just
retry with a fresh worker.

## Typical flow

1. Decompose the task into a few self-contained prompts.
2. (Optional) Read accounts/pool status to size your parallelism honestly.
3. `ask_batch(prompts, n_parallel=1)` (raise parallelism only with multiple accounts).
4. Collect per-item replies; discard/retry failures.
5. **Reason over the results yourself** and produce the real output. Attribute honestly what came
   from a worker and couldn't be verified.
