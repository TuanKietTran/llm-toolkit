# 007 Roadmap (gap vs 9router)

| Feature | Status | Notes |
|---------|--------|-------|
| OpenAI + Anthropic endpoints | implemented | |
| Combos, aliases, strategies | implemented | fallback / round_robin / tier |
| Cooldown + Retry-After | implemented | |
| Streaming cross-format | implemented | |
| API-key auth, usage, cost | implemented | |
| OAuth (PKCE + device code) providers + token refresh | implemented | see 009 |
| Quota windows (5h/daily/weekly) tracking | implemented | see 011 |
| Gemini / Vertex formats | implemented | see 004/005; Vertex auth via token, OAuth or service-account key |
| Cursor / Kiro formats | won't do | see TODO |
| Multiple accounts per provider (rotation) | implemented | see 011 |
| SQLite store + hot-reload config API | implemented | see 008 |
| Web dashboard | implemented | see 010; no per-account quota view yet |
| Token saver (compression, cache-friendly prompts, budgets, telemetry) | in-progress | see 012/015 |
| Embeddings / audio / image endpoints | implemented | see 002 |
| Difficulty-based routing (RouteLLM style) | idea | classifier picks combo |
| Docker image / CI | implemented | Dockerfile, compose, GitHub Actions (CI green on first run); nonroot-owned `/data` so a fresh volume starts first time (014) |
