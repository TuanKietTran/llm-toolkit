# 014 Operations: setup / run / teardown

Two supported ways to run gorouter. The Docker path was exercised end to end on 2026-10-10 (Windows, Rancher Desktop, no local Go). The native path is taken from the `Makefile`, `scripts/smoke.sh` and `cmd/gorouter/main.go` and was not run on that machine.

| | Native | Docker |
|---|---|---|
| Needs | Go 1.27.1 (`go.mod`), `make`, bash; Node + Chrome/Brave for e2e | Docker with compose |
| Setup | `cp examples/config.example.json config.json` | same file, mounted read-only at `/config/config.json` |
| Run | `make run` -> `./gorouter -config config.json` | `docker compose up -d --build` |
| State | `data/gorouter.db` | named volume `gorouter_gorouter-data` at `/data` |
| Stop | SIGINT/SIGTERM | `docker compose down` |

`config.json`, `data/` and the `/gorouter` binary are gitignored. Listen defaults to `:20128`; dashboard at `/dashboard/`.

## Setup
- REQ-014-1: The seed config is read only when the DB holds no config (REQ-008-1). After the first start, edit config through the dashboard / `PUT /admin/config`, or pass `-reseed` to overwrite the stored copy. Editing `config.json` alone has no effect on an existing DB or volume.
- REQ-014-2: `env:NAME` secrets resolve inside the process. Compose forwards only `ANTHROPIC_API_KEY` and `GLM_API_KEY` (`docker-compose.yml`); any other `env:` name in the config (`ANTHROPIC_API_KEY_2`, `GEMINI_API_KEY`, `SUB_CLIENT_SECRET`, `GOOGLE_SA_JSON` in the example) must be added to the compose `environment` block first.
- REQ-014-3: The image must contain `/data` owned by `nonroot` (uid 65532, set in `Dockerfile`). A new named volume copies that ownership on first mount; without it the volume is root-owned and the process exits with `store: unable to open database file (14)`. A bind mount does not inherit it, so `chown 65532:65532` the host directory (or use `--user`).

## Run
- Native self-contained trial: `make smoke` (`scripts/smoke.sh`) builds gorouter and `cmd/mockupstream` into a `mktemp` dir, listens on `127.0.0.1:20150` (key `smoke-key`, mock on `9911`), and cleans up on Ctrl-C.
- Docker trial without provider keys: `cmd/mockupstream` is not in the image. Build it separately, attach it to the compose network (`gorouter_default`), and point provider `base_url`s at it by container name; `/bad/*` always answers 429 (fallback demo). Start the mock after `docker compose up` has created the network, and remove it before the network (see Teardown).
- Verification checklist (all passed on the Docker path with a `flaky` -> `good` combo):
  1. `GET /dashboard/` returns 200.
  2. `POST /v1/chat/completions` without a key returns 401.
  3. Chat on the combo returns the second provider's answer (first 429s, REQ-001-2 / 003).
  4. `stream:true` on the alias yields OpenAI SSE; `POST /v1/messages` yields Anthropic `message_start` / `content_block_delta` events.
  5. `GET /admin/status` shows a `cooldowns` entry for the 429ing provider with `remaining_sec`.

## Using Claude Code
Point Claude Code at the router (`~/.claude/settings.json` `env`, or shell env vars):

```json
{ "env": {
  "ANTHROPIC_BASE_URL": "http://localhost:20128",
  "ANTHROPIC_AUTH_TOKEN": "<an entry of api_keys>",
  "ANTHROPIC_MODEL": "premium-coding",
  "ANTHROPIC_DEFAULT_OPUS_MODEL": "premium-coding",
  "ANTHROPIC_DEFAULT_SONNET_MODEL": "premium-coding",
  "ANTHROPIC_DEFAULT_HAIKU_MODEL": "cheapest-first"
} }
```

Either `ANTHROPIC_AUTH_TOKEN` (Bearer) or `ANTHROPIC_API_KEY` (`x-api-key`) works (REQ-002-2). Order combos strongest model first.

Verified 2026-10-10 with Claude Code 2.1.296, `claude -p`, gorouter in Docker, mock upstream (`flaky` -> `good`), a logging proxy in front:
- REQ-014-5: Claude Code calls `POST /v1/messages?beta=true` with `stream:true`. It streams and falls back correctly (`X-Gorouter-Target: good/b`). It also probes `HEAD /api/hello` at startup; the router answers 404 and Claude Code carries on.
- REQ-014-6: Every model name Claude Code sends must resolve (REQ-002-1). `--model haiku` with no `ANTHROPIC_DEFAULT_HAIKU_MODEL` sent `claude-haiku-5-5`; the router answered 404 on both the streaming attempt and the non-streaming retry, and Claude Code reported a model error. Mapping the default model env var to a combo or alias fixes it; the same applies to the opus and sonnet defaults. Equivalent: add the real Claude model ids to `aliases` in the config.
- REQ-014-7: A custom model name makes Claude Code log `unrecognized_model` and assume a 200k context window. Append `[1m]` to the name (Claude Code strips the suffix; the router receives the bare name) or set `CLAUDE_CODE_MAX_CONTEXT_TOKENS` to the real window.
- Request body Claude Code sends: `model`, `messages`, `system`, `tools` (27 to 42 custom tools), `metadata`, `max_tokens` (32000 on the main model, 64000 to 128000 on the haiku-class one), `thinking` (`{"type":"adaptive"}`), `context_management`, `output_config`, `stream`, with `cache_control` on blocks and an `anthropic-beta` header listing ten betas. `AnthropicToOpenAI` and `OpenAIToAnthropic` copy only `model`, `messages`/`system`, `max_tokens`, `temperature`, `top_p`, `stream`, stop sequences, `tools` and `tool_choice`, so `thinking`, `context_management`, `output_config`, `metadata` and `cache_control` never reach the upstream (TODO F7).

Not verified: tool use (the mock only echoes text), `POST /v1/messages/count_tokens` (no such route; Claude Code never called it in one-shot runs), server-side tools such as web search, and interactive-session background calls. Large `max_tokens` values may exceed a non-Anthropic provider's limit.

## Tests without a local Go
- `docker run --rm -v "<repo>:/src" -w /src golang:1.27-alpine go test ./...` passes (Git Bash on Windows: prefix `MSYS_NO_PATHCONV=1` and use `pwd -W`).
- `-race` (REQ-013-1, CI) was not run in this form; it needs cgo, which the alpine image does not ship with. `make e2e` still needs a host Node and browser.

## Teardown
- REQ-014-4: Shutdown is graceful: on SIGINT/SIGTERM the server drains for up to 10s, then usage is flushed (REQ-008-4). `docker compose down` sends SIGTERM first.
- Native: stop the process; `rm -rf data/ gorouter config.json` for a clean slate. `make smoke` removes its own temp dir.
- Docker, full clean:
  ```sh
  docker rm -f gorouter-mock                 # mock first: a container on the network blocks its removal
  docker compose down -v --rmi local         # container, volume (destroys DB, tokens, usage), compose-built image
  docker network rm gorouter_default         # only if compose reported it still in use
  docker rmi gorouter:local gorouter-mock:local   # any manually tagged images
  rm config.json
  ```
  `-v` deletes the SQLite state; omit it to keep config, OAuth tokens and usage between runs. Base images (`golang:1.27-alpine`, distroless) stay cached.

## Current gaps
- Claude Code features that need fields the converters drop: see TODO F7.
- gcr.io lookups time out intermittently from some networks; a retried `docker build` succeeds.
