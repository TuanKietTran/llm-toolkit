# Specs

Source of truth for what gorouter is and does. **Update the relevant spec in the same change as the code.**

| Spec | Topic | Status |
|------|-------|--------|
| [000-overview](000-overview.md) | Goals, non-goals, glossary | stable |
| [001-architecture](001-architecture.md) | Packages, request lifecycle | stable |
| [002-api](002-api.md) | HTTP endpoints, auth, errors | implemented |
| [003-routing](003-routing.md) | Model resolution, combos, fallback, cooldown | implemented |
| [004-translation](004-translation.md) | OpenAI <-> Anthropic mapping, streaming | implemented |
| [005-providers](005-providers.md) | Provider types & config | implemented (openai, anthropic) |
| [006-usage](006-usage.md) | Usage, cost, persistence | implemented (basic) |
| [011-accounts-quotas](011-accounts-quotas.md) | Multi-account rotation, quota windows | implemented |
| [008-storage](008-storage.md) | SQLite store, hot-reload config | implemented |
| [009-oauth](009-oauth.md) | OAuth 2.0 PKCE login + refresh | implemented |
| [010-dashboard](010-dashboard.md) | Admin API + web UI | implemented |
| [012-token-saver](012-token-saver.md) | Tool-output compression, terse prompt | implemented |
| [013-testing](013-testing.md) | Test layers, e2e suite | implemented |
| [014-operations](014-operations.md) | Setup / run / teardown, native and Docker | implemented |
| [015-token-optimization](015-token-optimization.md) | Cache-friendly prompts, dedupe, history limits, route budgets, savings telemetry | implemented |
| [TODO](TODO.md) | Work queue (open / won't do / done) | living |
| [007-roadmap](007-roadmap.md) | Gap list vs 9router, planned work | living |

## Conventions
- Any new idea, gap or follow-up goes into [TODO.md](TODO.md) first, and any new behaviour gets a spec here before/with the code.
- Status values: `planned` -> `in-progress` -> `implemented`.
- Each spec lists requirements as `REQ-<spec>-<n>`; tests reference them in comments when relevant.
- New feature flow: add/adjust spec + roadmap row -> implement -> tests -> mark implemented.
