# 015 Token optimization

Extends the optional `token_saver` middleware with provider-compatible prompt caching and bounded history controls. Transformations are fail-open and report removed characters.

Configuration:

| Field | Default | Meaning |
|---|---:|---|
| `cache_control` | false (on in `examples/config.example.json`) | Preserve cache metadata and mark the final system/text block as ephemeral where supported |
| `dedupe_tool_messages` | false | Remove consecutive duplicate tool messages |
| `max_history_messages` | 0 | Keep system messages and the newest N non-system messages; zero disables |
| `max_prompt_chars` | 0 | Keep system messages and newest messages within this character budget; zero disables |
| `route_budgets` | `{}` | Per requested model/alias budgets overriding the global limits |

A route budget has `max_tool_chars`, `max_history_messages`, and `max_prompt_chars`. `route_budgets` keys are matched against the requested model before routing.

- REQ-015-1: Cache-control fields on request/message/content blocks survive Anthropic-to-pivot conversion; optional ephemeral metadata is added without changing ordinary requests.
- REQ-015-2: Consecutive identical tool messages may be deduplicated, while system and user/assistant messages are not rewritten by dedupe.
- REQ-015-3: History limits retain all system messages and the newest eligible messages, preserving request order.
- REQ-015-4: Route budgets apply before routing and override only fields they specify; all transformations are fail-open and never increase the serialized content.
- REQ-015-5: `/admin/status` exposes cumulative saved characters plus saved tool, history, and dedupe characters since process start.
- REQ-015-7: With `cache_control` on, `OpenAIToAnthropic` carries markers on system blocks (system becomes text blocks only when a marker exists, otherwise stays a string), user text parts and tools. Providers on `api.openai.com` have markers stripped; OpenAI-compatible gateways receive them. Verified end to end against a capturing Anthropic upstream.
- REQ-015-6: Existing tool compression and terse prompts remain compatible with these controls.
