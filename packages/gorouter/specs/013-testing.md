# 013 Testing

| Layer | Where | What |
|-------|-------|------|
| Unit | `internal/*/_test.go` | translation (incl. streaming), quota windows, cooldown, token saver, gcpauth, device flow |
| Integration | `internal/server/*_test.go` | real HTTP server + mock upstreams/IdPs: fallback, accounts, quotas, OAuth + refresh, Gemini/Vertex, raw endpoints, hot reload, SQLite persistence |
| End to end | `e2e/dashboard.e2e.js` | real binary + real Chrome/Brave (puppeteer-core) + fake upstream and IdP; drives the dashboard |

- REQ-013-1: `go test -race ./...` must pass; CI runs gofmt, vet, race tests, build.
- REQ-013-2: The e2e suite covers: API-key entry, wrong-key error, OAuth Connect URL (PKCE), device-code login, playground through an OAuth account, config save + hot reload, invalid config rejection, disconnect, no console errors. Run with `make e2e` (`BROWSER` selects the binary).
- REQ-013-5: `make smoke` (`scripts/smoke.sh`) starts gorouter plus `cmd/mockupstream` (an OpenAI-compatible mock whose `/bad/*` path always 429s) and prints curl examples and the dashboard URL for manual exploration.
- REQ-013-3: Source files are plain `.js` (the e2e package sets `"type": "module"`); no `.mjs`.
- REQ-013-4: Tests that build `config.Config` by hand must call `cfg.ApplyDefaults()` (creates the default account).

## Bugs the e2e suite caught
Dashboard ignored hash navigation; a late poll could overwrite the current tab with a stale render.
