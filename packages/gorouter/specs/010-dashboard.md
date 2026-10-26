# 010 Dashboard & admin API

`/dashboard/` is a single embedded HTML page (no build step). It asks for an API key (kept in localStorage) and calls the admin API with it.

| Endpoint | Purpose |
|----------|---------|
| `GET /admin/status` | providers, OAuth state, cooldowns, combos, aliases, token-saver savings breakdown |
| `GET /admin/usage` | per-target stats |
| `GET/PUT /admin/config` | read / validate+apply+persist config |
| `POST /admin/oauth/{id}/start`, `DELETE /admin/oauth/{id}` | connect / disconnect |

- REQ-010-1: All `/admin/*` routes use the same API-key auth as `/v1/*` (open when no keys are configured: only do that on localhost).
- REQ-010-4: Renders are guarded so a slow response never overwrites a different tab; `hashchange` switches tabs; the device-code poller stops when leaving the tab.
- REQ-010-3: Tabs are deep-linkable (`/dashboard/#providers`); numeric columns are right-aligned.
- REQ-010-2: Tabs: Overview (totals, usage table, cooldowns, account quotas, token-saver counter, combos; auto-refresh 5s), Providers & OAuth (per account: connect, device code, disconnect), Config editor, Playground.
