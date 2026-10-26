# 000 Overview

gorouter is a self-hosted LLM gateway in Go (stdlib only). Clients (Claude Code, Cursor, Cline, SDKs) talk to one endpoint; gorouter picks the upstream model, translates formats and fails over on quota/errors.

## Goals
- REQ-000-1: One OpenAI-compatible and one Anthropic-compatible endpoint in front of many providers.
- REQ-000-2: Ordered fallback across tiers (subscription -> cheap -> free) with automatic cooldown.
- REQ-000-3: Named combos and aliases as stable model names for clients.
- REQ-000-4: Streaming everywhere, including cross-format streaming.
- REQ-000-5: Usage and estimated cost tracking.
- REQ-000-6: Single static binary, JSON config, no external services.

## Non-goals (for now)
- Billing/payment, multi-tenant user management, a web dashboard (see roadmap).

## Glossary
- **Target**: concrete `provider/model` pair.
- **Combo**: named, ordered list of targets/aliases/combos with a strategy.
- **Pivot format**: OpenAI chat-completions, used internally for all translation.
- **Cooldown**: temporary exclusion of a provider/target after failure.
