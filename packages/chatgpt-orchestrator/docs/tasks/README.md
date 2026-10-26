# Work breakdown

Granular, step-by-step execution plan for the remaining work, kept **in the repo** (not in agent
memory). Start with `00-status.md`, then do the numbered files in order.

| File | Task | Status |
|------|------|--------|
| [00-status.md](00-status.md) | Current state, design decisions, where we paused | — |
| [08-orchestrator-core-and-mcp-server.md](08-orchestrator-core-and-mcp-server.md) | Agent-agnostic `OrchestratorCore` + FastMCP server | ✅ done |
| [09-cli-acp-and-example-mcp.md](09-cli-acp-and-example-mcp.md) | CLI (`serve-mcp`/`ask`/`accounts`/`doctor`) + ACP adapter + example inward MCP | ✅ done |
| [10-tests-install-verify.md](10-tests-install-verify.md) | Test suite, browser install, live smoke, CI-safe integration | ✅ done (live smoke run pending, manual) |
| [11-pi-agent-adapter.md](11-pi-agent-adapter.md) | Drive the orchestrator from `pi` (extension + MCP registration) | ✅ done (real model call pending, needs a funded provider) |

Conventions:
- Each step is a checkbox. Tick it when done **and verified**.
- "Verify" blocks are runnable; prefer them over eyeballing.
- Don't commit secrets or `storage_state`. No subagents. Respect the ToS caveat in `docs/PLAN.md`.
