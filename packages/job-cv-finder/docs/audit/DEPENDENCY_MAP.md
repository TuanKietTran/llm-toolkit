# Dependency map

```text
Ruxt (Nuxt, browser)                         llm-toolkit
  app/pages/jobs.vue ──fetch──▶ Ruxt server   packages/job-cv-finder
  (local CVs/profiles)          /api/jobs/*  ──HTTPS + bearer──▶ core (Go: jcf)
                                                               │  SQLite (WAL)
                                                               │  task queue (same DB)
                                       intel (Python worker) ◀─┘  /internal/v1 lease/complete
                                          │ optional
                                          ▼
                                      gorouter / any OpenAI-compatible endpoint
```

## Go core (`core/go.mod`)

| Module | Why |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite (no cgo), same driver gorouter uses |
| `github.com/santhosh-tekuri/jsonschema/v6` | Validates worker results and Ruxt snapshots against the shared JSON Schemas |
| `golang.org/x/text` | Diacritic folding for Vietnamese company and location normalization |

Everything else is the Go standard library (`net/http`, `log/slog`, `crypto/aes`, `net/mail`, `mime/multipart`).

## Python worker (`intel/pyproject.toml`)

| Package | Why |
|---|---|
| `httpx` | Worker ↔ core protocol; OpenAI-compatible model calls |
| `jsonschema` | Validates every result against the contract before sending |
| `pytest` (dev) | Tests |

## Ruxt additions

No new dependencies. It uses `$fetch`/`EventSource` and the existing `@ruxt/shared/cv` types.

## Deliberately not added

Redis, Kafka, Temporal, Kubernetes, Postgres, a browser-automation stack. See ADR-0002 and ADR-0004.

## Licensing

All added Go and Python dependencies are BSD/MIT/Apache-2.0 licensed (modernc sqlite: BSD-3; jsonschema/v6: Apache-2.0; x/text: BSD-3; httpx: BSD-3; jsonschema (py): MIT).
