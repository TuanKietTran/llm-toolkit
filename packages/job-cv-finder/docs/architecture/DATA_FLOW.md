# Data flow and workflow

## States

```text
                     ┌─────────── out-of-band confirmation email / attestation ───────────┐
                     │                                                                     ▼
MATCHING ─▶ MATCHED ─▶ SELECTED ─▶ PREPARING ─▶ PREPARED ─▶ POLICY_CHECK ─▶ EXECUTING ─▶ VERIFYING ─▶ SUBMITTED
   │           │                       │                        │   │            │  │  │
   │           └▶ NOT_SELECTED         │                        │   └▶ HANDOFF_PENDING (candidate applies; email verifies)
   └────────────────┬──────────────────┘                        ▼            │  │  └▶ FAILED_RETRYABLE ─▶ POLICY_CHECK | FAILED_FINAL
                    ▼                                  BLOCKED_EXCEPTION ◀───┘  └▶ SUBMISSION_UNKNOWN ─▶ VERIFYING | POLICY_CHECK (safe retry) | FAILED_FINAL
              BLOCKED_EXCEPTION ── candidate action (retry / answer / regenerate / rematch) ──▶ back into the flow
```

The edge list is code: `core/internal/domain/state.go`. Every change is a compare-and-swap on `applications.version` and writes an `execution_events` row in the same transaction.

Job-level stages (`INGESTED → NORMALIZED → DEDUPLICATED`) happen inside one ingest transaction and are recorded as `job.ingested` events with the outcome (`new|changed|seen|duplicate`). Discovery runs have their own lifecycle (`SCHEDULED/DISCOVERING/COMPLETED/FAILED`) in `discovery_runs`.

## Vertical slice

1. **Candidate data.** The owner selects a profile and CVs in Ruxt. The browser builds a v1 snapshot and POSTs it through the Ruxt proxy. Go validates it against the schema and stores it (deduplicated by content hash). An application (`MATCHING`) is created for every unique active job that does not have one yet, and its `intel.match` task is enqueued in the same transaction.
2. **Discovery.** A polling source (Greenhouse, Lever, mock) or an email alert (LinkedIn, ITviec) produces `RawJob`s. They are normalized, the raw snapshot is preserved, and they are deduplicated (see below). New unique jobs start applications when candidate data exists.
3. **Match (Python).** The worker extracts facts from the profile and the exact CV revisions, scores dimensions, and returns `MatchResult`. Go checks that every fact cites a document and version present in the snapshot, stores facts and the match, and moves to `MATCHED`.
4. **Selection (Go).** `strong_match`/`match` and not ineligible goes to `SELECTED`. Anything else goes to `NOT_SELECTED`; the candidate may override.
5. **Prepare (Python).** CV selection by skill coverage, a tailored CV (reordered lines only), a cover letter (template or validated model output), and answers from candidate preferences or facts. Go re-validates evidence, synthesizes file-question answers from documents, and goes to `PREPARED`, or to `BLOCKED_EXCEPTION` listing missing information.
6. **Policy (Go).** Versioned rules produce `automated` (authorized API), `handoff`, or `block`. The decision and its reasons are persisted.
7. **Execute.** A write-ahead `execution_runs` row (`started`) commits *before* the external call. The result is classified: success goes to `VERIFYING`; retryable goes to `FAILED_RETRYABLE` with backoff; uncertain goes to `SUBMISSION_UNKNOWN`; auth goes to `BLOCKED_EXCEPTION` and marks the account; permanent goes to `FAILED_FINAL`.
8. **Verify.** A lookup by idempotency key must find the application. Only then is an `ats_api` receipt stored and the state set to `SUBMITTED`.
9. **Handoff verification.** A LinkedIn "your application was sent to" email (matched by job id) or an ITviec "đã nhận hồ sơ" / "đã được gửi đi" email (matched by canonical title+company) stores an `email_confirmation` receipt and moves the application to `SUBMITTED`. A candidate attestation stores an unverified `user_attested` receipt.
10. **History.** Events stream to Ruxt over SSE. The application detail shows match evidence, documents, answers, policy decisions, runs, receipts and the timeline.

## Deduplication

1. Same `(source, external_job_id)` is the same row. Content changes create a new snapshot and set status `changed`. A partial email summary never overwrites a full description.
2. Same canonical key (`company_norm | title_norm`) on another row means `duplicate_of` the earliest primary. Applications are keyed by canonical key, so one real job gets one application across sources.
3. Same company with title token Jaccard ≥ 0.8 is flagged `probable_duplicate_of` and never merged automatically.

## Exactly-once logical submission

External exactly-once is impossible, so the system combines several layers:
- One application per canonical job (unique `logical_key`).
- One idempotency key per application, reused on every retry (the mock ATS dedupes on it).
- A write-ahead run record, so a crash mid-call is detected on restart (`EXECUTING` not in flight goes to `SUBMISSION_UNKNOWN`).
- Reconciliation by lookup before any retry. Retry happens only if the lookup is authoritative and finds nothing.
- Receipts unique by `(kind, external_ref)`, and the policy blocks if any receipt exists.

These behaviours are covered by `TestStoredButErroredSubmissionIsReconciledNotDuplicated`, `TestRejectedBeforeStoreIsRetriedSafely` and `TestCrashDuringSubmissionRecovers`.
