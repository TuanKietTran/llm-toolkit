# Go / Python boundary

## Rule

Go owns state and decisions. Python owns intelligence. Python never touches the database, never sees credentials, and cannot change workflow state except by returning a result that Go validates.

| Concern | Owner | Where |
|---|---|---|
| Workflow states and transitions, idempotency, retries, recovery | Go | `core/internal/workflow`, `core/internal/domain/state.go` |
| Task queue (lease, redelivery, dead letter) | Go | `core/internal/store/tasks.go` |
| Source registry, capability policy, discovery adapters, email parsing | Go | `core/internal/sources` |
| Normalization and deterministic dedup | Go | `core/internal/normalize` |
| Policy decisions, execution, verification, receipts | Go | `core/internal/policy`, `core/internal/execution` |
| Secrets | Go | `core/internal/secrets` |
| Candidate fact extraction and evidence mapping | Python | `intel/src/jcf_intel/facts.py` |
| Matching and ranking | Python | `intel/src/jcf_intel/matching.py` |
| CV selection, tailoring, cover letters, answers | Python | `intel/src/jcf_intel/prepare.py` |
| Generation evidence validation | Python **and** Go | `prepare.validate_document`; Go re-checks refs in `workflow/steps.go:applyPrepare` |
| Model providers | Python | `intel/src/jcf_intel/llm.py` |

## Protocol

HTTP. Go serves `/internal/v1` with a separate bearer token (`JCF_WORKER_TOKEN`).

```http
POST /internal/v1/tasks/lease        {"kinds":["intel.match","intel.prepare"],"worker":"w1","lease_seconds":300}
  → 200 Task | 204 (empty)
POST /internal/v1/tasks/{id}/complete {"worker":"w1","result":{...}}   → 200 | 409 lease lost | 422 contract violation
POST /internal/v1/tasks/{id}/fail     {"worker":"w1","error":"...","retryable":true}
```

- **Leases** expire, and an expired task is redelivered. After `max_attempts` it is dead, and the application becomes `BLOCKED_EXCEPTION` with the reason.
- **Completion is transactional with the workflow change.** The task is marked done only if the state transition commits.
- **Stale results are ignored safely.** If the application moved on (re-match, out-of-band confirmation), the result is recorded as `task.result_ignored`.

## Contracts

`core/contracts/*.schema.json` (JSON Schema 2020-12) is the single source of truth:

- `candidate-snapshot.schema.json`: Ruxt → core
- `match-result.schema.json`: `intel.match` result
- `prepare-result.schema.json`: `intel.prepare` result

Go embeds the files (`core/contracts/contracts.go`). Python's worker validates against the same files before sending, and its tests load them too. A schema change breaks both test suites until both sides agree.

Beyond the schema, Go checks semantics. Result ids must match the task. Facts must cite a document and version that exist in the snapshot. The selected CV must be in the snapshot at the exact revision. Every statement needs evidence ids that exist. Required questions without an evidence- or candidate-backed answer block the application.

## Why HTTP + DB queue, not gRPC or a broker

There is one candidate and a handful of tasks per hour, so throughput is irrelevant. What matters is atomic enqueue-with-transition and redelivery after crashes. Both come free from keeping the queue in the workflow's database. HTTP keeps the worker language-agnostic and debuggable with curl. gRPC would add codegen for no measurable gain (ADR-0004).
