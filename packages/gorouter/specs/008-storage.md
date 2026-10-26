# 008 Storage & hot reload

SQLite (pure Go, `modernc.org/sqlite`) at `-db` (default `data/gorouter.db`), WAL mode, one connection.

| Table | Content |
|-------|---------|
| `kv` | key `config` = raw config JSON document |
| `oauth_tokens` | per provider: access, refresh, expiry |
| `usage_stats` | per `provider/model` JSON stat |
| `quota_windows` | per account+quota index: window start, length, requests, tokens (REQ-011-9) |

- REQ-008-1: On start the DB config wins; the `-config` file only seeds an empty DB. `-reseed` overwrites the DB copy.
- REQ-008-2: `PUT /admin/config` validates (`config.Parse`), persists, then atomically swaps router state. In-flight requests finish on the old router; cooldown state (`Health`) is carried over.
- REQ-008-3: An invalid document is rejected with 400 and nothing changes.
- REQ-008-4: Usage is flushed every 10s and on shutdown.
- Secrets in the stored document stay as written (`env:NAME` references are preserved; literals are stored in plaintext in the DB, protect the file).
