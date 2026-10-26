---
name: pi-agent-delegation
description: Delegate bounded coding, investigation, testing, or review tasks to a local Pi agent using the Pi extension and local-model harness. Use when a task can be isolated with explicit files, acceptance criteria, and a short handoff; especially for local Ollama/llama.cpp inference, per-request token budgets, prompt caching, or model effort.
---

# Pi Agent Delegation

Use Pi for a focused subtask, not an unbounded second project owner. Give it a narrow scope,
explicit output, a wall-clock timeout, and a validation command; inspect its result before
incorporating it.

## Delegate

1. Define one outcome: investigate, implement, test, or review.
2. Name the allowed files/directories and files it must not touch.
3. Include acceptance criteria and the exact check to run.
4. Set a timeout appropriate to the task. Local inference can be slow or stall.
5. Ask Pi to report changed files, tests, risks, and unresolved questions.
6. Review the diff and run the check yourself before handing off.

Example:

```text
Work only in integrations/pi/local-model-harness.ts.
Add validation for /local-budget without changing existing command behavior.
Acceptance: invalid values show a warning; valid values apply on the next request;
run git diff --check and report the result. Do not edit other files.
```

Run a local Pi agent from the repository root:

```bash
timeout 10m pi --provider ollama --model gemma4:12b --thinking low --verbose -p "<bounded task>"
```

Use `--thinking low` for straightforward edits, `medium` for normal implementation, and `high`
only for difficult reasoning. Keep the task prompt stable when possible so the local prompt/KV
cache remains useful.

## Timeouts and diagnostics

- Always wrap non-interactive delegation in `timeout`; increase it for large scans or slow models.
- Use `--verbose` when diagnosing startup, provider, extension, or model-selection issues.
- A timeout is not proof of failure. Inspect partial output and `git diff`, then rerun a smaller task.
- Report the exit status, whether the timeout fired, partial artifacts, and the last visible operation.
- Do not blindly retry a timed-out mutating task; first check whether files changed.

## Interactive controls

The local-model harness is session-local and exposes:

- `/local-harness` — show budget, effort, keep-alive, and cache state.
- `/local-budget 2048|auto` — reset the per-request output budget or use the model default.
- `/local-effort off|minimal|low|medium|high|xhigh|auto` — change reasoning effort.
- `/local-keepalive 30m|off|default` — control Ollama model residency.
- `/local-cache on|off` — enable or disable local cache hints.

The token budget resets on every provider request, not once for the entire agent run. Tool-heavy
tasks can therefore make several requests. Prefer the model's configured `maxTokens`; use
`/local-budget` only for the current session.

## Safety and handoff

- Do not give Pi secrets, credentials, or unrelated workspace contents.
- Do not run multiple agents concurrently against the same files.
- Require confirmation before destructive operations, dependency upgrades, migrations, or external network actions.
- Treat Pi output as a draft: inspect the diff, verify tests, and check for unrelated changes.
- If the task is exploratory, request a report only and prohibit edits.

Use this handoff format:

```text
Result: <one sentence>
Changed: <files or “none”>
Verified: <commands and outcomes>
Timeout: <yes/no; partial output if yes>
Risks: <remaining concerns>
Next: <one concrete follow-up, if needed>
```
