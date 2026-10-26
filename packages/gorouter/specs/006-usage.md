# 006 Usage

- REQ-006-1: Per `provider/model`: requests, errors, in/out tokens, estimated cost, total latency, last used.
- REQ-006-2: Cost = tokens/1e6 * price; estimate only.
- REQ-006-3: Persisted to SQLite (see 008) every 10s and on shutdown.
- REQ-006-5: Every failed upstream attempt of a request that ultimately fails is counted as a request + error against its `provider/model`, so the error rate reflects routing failures (requests that succeed after a fallback count only the successful target).
- REQ-006-4: Streamed token counts come from the final usage chunk; missing usage records 0.
