# gorouter

Self-hosted LLM router in Go. One OpenAI + Anthropic compatible endpoint, many providers, ordered fallback, cross-format streaming, usage tracking. Design lives in [`specs/`](specs/README.md).

```sh
cp examples/config.example.json config.json   # edit keys / models
make run            # first run seeds data/gorouter.db from config.json
# dashboard: http://localhost:20128/dashboard/  (config edits hot-reload)
curl localhost:20128/v1/chat/completions -H 'Authorization: Bearer sk-gorouter-change-me' \
  -d '{"model":"premium-coding","messages":[{"role":"user","content":"hi"}]}'
# Claude Code: ANTHROPIC_BASE_URL=http://localhost:20128 ANTHROPIC_AUTH_TOKEN=<key>
```
`make test` runs the Go suite; `make e2e` drives the dashboard in a real browser (needs Node and Chrome/Brave).
