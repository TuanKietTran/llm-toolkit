# 011 Accounts & quotas

## Accounts (REQ-011-1..4)
A provider may list several `accounts` (`id`, `api_key`, `disabled`). With none configured, one implicit account `default` uses the provider `api_key`.
- REQ-011-1: Routing expands each `provider/model` target into one target per enabled account. `account_strategy`: `fallback` (config order) or `round_robin` (rotate the start per request).
- REQ-011-2: Cooldown keys: `provider#account` for 429/401/403/token errors (account-level), `provider#account/model` for other failures. One bad account no longer blocks the others.
- REQ-011-3: OAuth providers keep a token per account (`TokenKey`: bare provider id for `default`, else `provider#account`); connect each account from the dashboard or admin API with `?account=`.
- REQ-011-4: Account ids contain no `/` or `#`.

## Quotas (REQ-011-5..9)
`quotas: [{window, max_requests, max_tokens}]` on a provider, applied independently to each account. `window` is a Go duration (`5h`, `24h`, `168h`).
- REQ-011-5: Fixed windows: a window starts at the first use after the previous one ended and resets after `window`.
- REQ-011-6: Requests are counted on a 2xx upstream answer; tokens (in+out) after the response completes.
- REQ-011-7: An account at or over any limit is skipped during routing, so traffic moves to the next account/target. If everything is exhausted or cooling, routing fails open and tries all (the upstream may still accept).
- REQ-011-8: `GET /admin/status` exposes `quotas` (usage, limits, reset time, exhausted flag); the dashboard renders it.
- REQ-011-9: Quota state survives config hot-reloads and restarts: windows are saved to SQLite (`quota_windows`) every 10s and on shutdown, only when changed, and only for accounts with a live window. Saved state is ignored for a quota whose `window` length changed. A crash can lose up to ~10s of counts. Upstream limits remain authoritative: 429 + `Retry-After` still triggers cooldown.
