# 003 Routing

## Resolution order (REQ-003-1)
alias -> combo -> `provider/model` -> bare model name. Nesting depth max 8. Result is de-duplicated, order preserved.

## Strategies (REQ-003-2)
- `fallback`: members in listed order.
- `round_robin`: rotate the starting member per request, rest follow in order.
- `tier`: stable-sort all expanded targets by provider `tier` ascending.

## Fallback loop (REQ-003-3)
Targets not on cooldown are tried in order. If all are on cooldown, all are tried (fail-open).
- 2xx -> success, clears cooldown.
- Retryable (401, 403, 408, 409, 425, 429, >=500) or network error -> cooldown, try next.
- Other 4xx (400, 404, 422...) -> returned immediately; request is at fault, not the provider.

## Accounts and quotas
Each target fans out over the provider's accounts; see [011](011-accounts-quotas.md).

## Cooldown (REQ-003-4)
Exponential backoff `base * 2^(fails-1)` capped at `max` (defaults 5s / 300s). `Retry-After` seconds override. 429/401/403 cool the whole provider (account-level); other failures cool the single `provider/model`. Success resets.

## Not yet
Quota-window awareness (5h/daily/weekly resets) and per-key accounts: see roadmap.
