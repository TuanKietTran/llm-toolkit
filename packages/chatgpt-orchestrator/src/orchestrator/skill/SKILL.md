---
name: chatgpt-orchestrator
description: Fan out drafting/extraction/first-pass work to a pool of free ChatGPT browser workers, then reason over and pick the best result. Use when a task benefits from many cheap parallel LLM drafts (e.g. "try N variants and pick the best", "summarize each of these N documents") rather than one expensive careful pass.
---

# chatgpt-orchestrator

This skill is a thin recipe layer over the `chatgpt-orchestrator` **MCP server** — register it
first (`.mcp.json` / `claude mcp add`), then use these recipes. All the actual work happens through
the MCP tools (`spawn_worker`, `ask`, `ask_batch`, `converse`, `list_workers`, `close_worker`) and
resources (`pool://status`, `accounts://status`); this file just documents *when* and *how* to
combine them. There is no separate "brain" here — you (the connected agent) are the brain; the
workers are cheap, disposable ChatGPT contexts you drive.

> ToS note: this automates the free ChatGPT web UI, which is against OpenAI's usage policies, and
> logged-in accounts risk suspension. That's a decision the operator has already made by running
> this system — don't re-litigate it, just be aware error messages may reflect account cooldowns.

## Recipe 1 — fan out N drafts, pick the best

The headline use case: decompose a task into N independent one-shot prompts, run them in parallel
across the worker pool, then apply your own judgment to pick/merge the results.

1. (Optional) Read `accounts://status` to see how many accounts are schedulable right now — that's
   your real parallelism ceiling, not just `len(prompts)`.
2. Call `ask_batch(prompts=[...], n_parallel=<len(prompts) or less>)`.
3. Each item comes back as `{"ok": true, "reply": "..."}` or `{"ok": false, "error": "..."}` — a
   failure on one prompt (rate limit, challenge) never aborts the batch. Expect some failures; the
   free tier's rate limits are real (`accounts://status` shows `budget_used`/`budget_limit`).
4. Read the successful replies and pick/synthesize the best one yourself — that reasoning step is
   yours, not a worker's.

Example prompt to yourself: *"spawn 3 ChatGPT workers, have each draft a different intro for X,
then pick the best."* → this is exactly one `ask_batch` call with 3 prompts, followed by your own
comparison of the three replies.

## Recipe 2 — multi-turn delegation on one worker

When a single sub-task needs back-and-forth (e.g. "draft this, then revise based on my note"),
don't re-spawn for every turn:

1. `spawn_worker()` once → `worker_id`.
2. `converse(worker_id, message)` for each turn — it's the *same* browser context/conversation, so
   ChatGPT retains what was said before.
3. `close_worker(worker_id)` when done to free the context back to the pool.

## Recipe 3 — equip a worker with tools

If a sub-task needs a worker to fetch data or call a domain tool mid-answer (not just draft text),
spawn it with `equip_tools=true`. This only does something if `ORCH_INWARD_MCP_SERVERS` is
configured (see `examples/example_mcp_server.py` for a template `fetch_url` tool) — the worker then
runs its own bounded ReAct loop against those tools instead of a single-turn reply. Prefer Recipe 1
unless the sub-task genuinely needs the worker to look something up.

## Checking capacity before a big fan-out

Real throughput is `min(max_contexts, RAM headroom, schedulable account budget)`, not "however many
prompts you send." Before a large `ask_batch`, read `pool://status` (`idle`/`busy`/`account_capacity`
/`ema_pending`) and `accounts://status` (`capacity`, per-account `budget_used`/`budget_limit`,
`cooldown_remaining_s`) so you don't ask for 50-way parallelism when only 3 accounts are schedulable
— the excess just queues (bounded by `max_queue`) rather than failing outright, but it's slower than
expected if you don't account for it.

## Troubleshooting

- A worker's `ask`/`converse` call raising an error usually means the underlying context hit a
  rate limit or a Cloudflare/login challenge — the orchestrator already quarantines that context and
  cools its account down automatically; just retry with a fresh `spawn_worker()`.
- If `accounts://status` shows every account `"schedulable": false`, wait for `cooldown_remaining_s`
  or `retry_after` before fanning out more work — spawning will otherwise just queue or fail.
