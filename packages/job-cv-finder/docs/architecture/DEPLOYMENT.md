# Deployment

## Processes

| Process | Command | Notes |
|---|---|---|
| Core | `jcf` (Go binary) | One instance. SQLite is single-writer, and the engine assumes one scheduler |
| Intelligence worker | `python -m jcf_intel` | 1..n instances; stateless; safe to kill at any time |
| Ruxt | existing deployment | Add three env vars; no other change |

The core and workers belong on an always-on host the owner controls, such as a small VM or a home server. They do **not** run inside Cloudflare Workers: the workflow loop and SSE need a long-lived process.

## Configuration

Core (`cmd/jcf/main.go` documents every variable):

```sh
JCF_ADDR=127.0.0.1:8790          # put TLS in front (Caddy/nginx) if Ruxt reaches it over a network
JCF_DB=/var/lib/jcf/jcf.db
JCF_API_TOKEN=<random ≥24 chars>     # shared with Ruxt's server only
JCF_WORKER_TOKEN=<different random>  # shared with workers only
JCF_SECRET_KEY=<base64 of 32 random bytes>  # from the secret manager; required to store credentials
JCF_GREENHOUSE_BOARDS=acme:Acme,beta:Beta:Vietnam|Singapore   # optional public boards; optional :location regex
JCF_LEVER_SITES=gamma:Gamma
JCF_DISCOVER_EVERY=6h
```

Worker:

```sh
JCF_CORE_URL=http://127.0.0.1:8790
JCF_WORKER_TOKEN=<same as core>
JCF_MODEL_BASE_URL=http://127.0.0.1:20128   # optional: gorouter or any OpenAI-compatible endpoint
JCF_MODEL_ID=<model id>                     # optional
JCF_MODEL_API_KEY=<key>                     # optional; never sent anywhere else
```

Ruxt (server runtime config):

```sh
NUXT_JOB_AUTOMATION_URL=https://jcf.example.internal
NUXT_JOB_AUTOMATION_TOKEN=<JCF_API_TOKEN>
NUXT_JOB_AUTOMATION_OWNER=clerk:<your user id>
```

## Build

```sh
cd core && go build -o jcf ./cmd/jcf
cd intel && uv sync
```

## Verifying a deployment

1. `GET /readyz` returns 200.
2. In Ruxt, open `/jobs`. The Overview loads and shows the seeded sources.
3. CV workspace: send one CV. Overview shows the snapshot via `/api/v1/integration/health`.
4. Run `scripts/e2e.sh` on the host to exercise the full slice against the mock ATS. It uses throwaway ports and a temp database.

## Scaling and limits

At one candidate, SQLite WAL handles all write volume. Move to Postgres only if several cores must share state (see ADR-0002 for the migration path). Workers scale horizontally because leases make delivery safe.
