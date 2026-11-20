# Architecture

A Go modular monolith (`jcf`) runs the API, the workflow engine and the scheduler in one process. Python intelligence workers run as separate processes that pull work over HTTP. Ruxt hosts the UI and proxies to the core.

```text
 Browser (Ruxt /jobs)                 Ruxt server (Nitro)                 jcf (Go)                         intel (Python)
 ─ local profiles + CVs ─┐   fetch    ┌──────────────────────┐  bearer  ┌───────────────────────────┐ lease ┌──────────────────┐
 ─ preferences          ─┼──────────▶ │ /api/jobs/[...path]  │────────▶ │ api ─ workflow ─ policy   │◀──────│ facts / match /  │
 ─ snapshot builder     ─┘   SSE      │ owner check, allow-  │          │  │      │         │         │ result│ prepare / verify │
                                      │ list, token injection│          │ store (SQLite WAL)  execution────┐ └────────┬─────────┘
                                      └──────────────────────┘          │  jobs, apps, events, tasks │      │          │ optional
                                                                         │ sources ─ email intake     │      ▼          ▼
                                                                         └───────────────────────────┘  ATS API     OpenAI-compatible
                                                                                    ▲                   (mock today) model (gorouter)
                                                         job-alert / confirmation emails (RFC 822)
```

## Components (core)

| Package | Responsibility |
|---|---|
| `domain` | States and legal transitions, capabilities, canonical job, policy decision types |
| `store` | Migrations, repositories, CAS transitions with events, task queue, receipts |
| `normalize` | HTML stripping, Vietnamese-aware folding, company/title/location keys, salary and skills extraction, dedup keys |
| `sources` | Source registry with default compliance policies; Greenhouse and Lever GET adapters; LinkedIn/ITviec email parsers; RFC 822 reader |
| `workflow` | Engine: intake (snapshots, jobs, discovery, email), `Step` state machine, task completion, candidate actions, scheduler loop |
| `policy` | Pure, versioned rules deciding `automated` / `handoff` / `block`, with reasons |
| `execution` | `Executor` interface; `AuthorizedAPIExecutor`; failure classes (retryable / uncertain / permanent / auth) |
| `secrets` | AES-256-GCM vault with ref-bound ciphertexts; fails closed without a key |
| `api` | Candidate API, worker API, SSE, health/readiness/metrics |
| `mockats` | Local Greenhouse-shaped ATS with idempotency, lookup and fault injection |

## Interchangeability

- **Model provider**: `ModelProvider.complete(system, user)`. `NullProvider` uses deterministic templates; `OpenAICompatibleProvider` covers OpenAI, Anthropic via gorouter, local models and others. Switching is a config change, recorded per document as `engine.model_provider/model_id/prompt_version`.
- **Harness / worker**: anything that speaks the lease/complete protocol and returns contract-valid JSON can replace `jcf_intel`, including an agent harness. Go validates the output either way, so a harness cannot widen its authority.
- **Executor**: `execution.Executor`, keyed by access method. Adding a browser executor later is a registry entry plus a policy grant, not a workflow change.

## Why not microservices

There is one candidate. A single core process with an embedded queue gives atomic state + queue transactions, which would otherwise need an outbox, a broker and a second database. Python is separate because its dependencies and failure modes differ, and it can scale or crash on its own.
