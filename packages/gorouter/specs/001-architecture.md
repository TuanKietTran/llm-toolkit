# 001 Architecture

```
client --(OpenAI | Anthropic)--> server --> translate(to pivot) --> router.Execute
   ^                                                                    |
   |                                              provider.BuildRequest (pivot -> upstream)
   +-- translate(from pivot) <-- upstream response/stream <-- HTTP -----+
```

| Package | Responsibility |
|---------|----------------|
| `cmd/gorouter` | flags, wiring, graceful shutdown |
| `internal/config` | JSON load, defaults, validation, `env:` secrets |
| `internal/server` | HTTP handlers, auth, response/stream writing, usage recording |
| `internal/router` | resolve model -> targets, fallback loop, `Health` cooldowns |
| `internal/provider` | build upstream HTTP request per provider type |
| `internal/translate` | request/response/stream conversion, SSE reader |
| `internal/usage` | counters, cost, pluggable persistence |
| `internal/router/quota.go` | per-account fixed-window quotas |
| `internal/store` | SQLite: config doc, OAuth tokens, usage |
| `internal/oauth` | PKCE flow, token refresh |
| `internal/dashboard` | embedded admin UI |

## Lifecycle
1. Auth (REQ-002-2). 2. Parse body; if Anthropic inbound, convert to pivot. 3. `Resolve` model. 4. Try targets until a 2xx *header* arrives. 5. Convert body/stream to the client's format. 6. Record usage.

## Decisions
- REQ-001-1: Bodies are `map[string]any` so unknown provider-specific fields pass through untouched.
- REQ-001-2: Fallback only happens before the first response byte; once streaming starts the target is committed.
- REQ-001-3: Stdlib only to keep builds trivial; add deps only with a spec note.
