# job-cv-finder

A personal job discovery and application platform. A Go core owns durable workflow, policy and execution. A Python worker does candidate-fact extraction, matching and evidence-checked document generation. The UI is a native `/jobs` section inside Ruxt.

```text
core/     Go: API, workflow engine, scheduler, policy, executors, SQLite store, mock ATS
intel/    Python: facts, matching, CV selection, tailoring, cover letters, answers, model providers
docs/     audit, architecture, ADRs, sources/compliance, runbook, security
scripts/  e2e.sh: full slice across processes against the mock ATS
```

The earlier agent-prompt version lives in `../chatgpt-orchestrator/job-cv-finder/` and is superseded (see `docs/audit/REUSE_AND_REPLACEMENT.md`).

## Quick start (local, mock ATS)

```sh
bash scripts/e2e.sh          # builds, starts core + mock ATS + worker, runs the slice, asserts, cleans up
```

Manual run:

```sh
cd core && go run ./cmd/mockats &                                   # MOCKATS_KEY=...
JCF_API_TOKEN=... JCF_WORKER_TOKEN=... JCF_SECRET_KEY=... JCF_MOCKATS_URL=http://127.0.0.1:8791 go run ./cmd/jcf
cd ../intel && uv sync && JCF_WORKER_TOKEN=... uv run python -m jcf_intel
```

Then configure Ruxt (`NUXT_JOB_AUTOMATION_URL/TOKEN/OWNER`) and open `/jobs`. See `docs/architecture/DEPLOYMENT.md`.

## Deploying to AWS

One on-demand `t4g.small` host (stopped by default; `gh workflow run jcf-power.yml -f action=start|stop|status`; Caddy, core, worker), SQLite on a separate encrypted volume, S3 and snapshot backups, deployed by GitHub Actions through OIDC. Start at `docs/ops/AWS_DEPLOYMENT.md`; recovery in `docs/ops/BACKUP_ROLLBACK_RECOVERY.md`; what is and is not verified in `docs/testing/EVIDENCE.md` and `docs/ops/KNOWN_LIMITATIONS.md`.

## Tests

```sh
cd core && go test ./...            # unit + integration (workflow with mock ATS, faults, crash recovery, API, SSE)
cd intel && uv run pytest           # facts, matching, generation, validation, injection, worker protocol
bash scripts/e2e.sh                 # cross-process end to end
```

## Status

Status values follow the brief: implemented and tested; implemented but not fully verified; partially implemented; blocked; not implemented.

| Capability | Status | Evidence / note |
|---|---|---|
| Go backend (API, workflow, scheduler) | Implemented and tested | `go test ./...` (7 packages), race detector clean on workflow |
| Python intelligence worker | Implemented and tested | `pytest` (12 tests), contract-validated output |
| Go/Python contracts | Implemented and tested | Shared JSON Schemas loaded by both test suites |
| Ruxt integration (snapshot push, owner-only proxy, artifact "registration" as new local CV) | Implemented but not fully verified | Ruxt unit test, architecture lint and `nuxt build` pass; proxy and page not yet exercised in a signed-in browser session |
| Native Ruxt `/jobs` feature (11 sections + detail, SSE) | Implemented but not fully verified | Builds; follows editor-context conventions; not yet clicked through in a browser |
| Discovery: Greenhouse / Lever public APIs | Implemented and tested | Adapter tests with recorded shapes; not yet run against a live board |
| Discovery: LinkedIn / ITviec alert emails | Implemented and tested | Parsers tested on fixtures that reproduce real email layouts |
| Discovery: CareerViet / VietnamWorks | Partially implemented | Pasted jobs only; no alert-email parser yet |
| Normalization and dedup (deterministic + flagged similarity) | Implemented and tested | `normalize`, `store` tests |
| Evidence-backed matching | Implemented and tested | Dimensions, uncertainty and partial-data confidence |
| CV selection from Ruxt CVs (exact revision) | Implemented and tested | Go rejects CVs or revisions not in the snapshot |
| Document preparation with validation | Implemented and tested | Template path; model path validated (fabricating fake model rejected) |
| Model provider interchange | Implemented and tested with fakes | `OpenAICompatibleProvider` not yet run against a live endpoint |
| Explicit capability enforcement | Implemented and tested | Policy table tests; API refuses platform passwords |
| Durable workflow, idempotency, recovery | Implemented and tested | Lease expiry, dead letter, stale results, crash mid-submission |
| Authorized-API submission + verification | Implemented and tested **against the mock ATS only** | No real source currently grants it (ADR-0005) |
| Handoff + email-receipt verification (LinkedIn, ITviec) | Implemented and tested | Including confirmations for jobs applied outside the system |
| Automated submission on LinkedIn / ITviec / CareerViet / VietnamWorks | Blocked by policy | Terms not verified as permitting automation; existing driver relies on stealth |
| Browser executor | Not implemented | Interface slot only, by decision |
| Account/session isolation | Implemented and tested | One account per source, write-only encrypted credentials |
| Mail fetching (IMAP / Gmail API) | Not implemented | Intake accepts emails; fetching them is left to the deployment (export, forwarder) |
| Retention pruning job | Not implemented | Manual SQL in the runbook |
| Postgres backend | Not implemented | SQLite by decision (ADR-0002) |
| Deployment docs, runbook, security docs | Written | `docs/` |
| AWS deployment (Terraform, containers, Caddy/HTTPS, backups, CI/CD) | Implemented and verified in production | `docs/testing/EVIDENCE.md`; deployed host passes HTTPS, auth, backup/restore and restart-persistence checks |
| Production Ruxt using the AWS core | Not done | Ruxt still points at the previous core; cut-over procedure in `docs/ops/RUNBOOK.md` |
