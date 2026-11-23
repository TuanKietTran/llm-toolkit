# Baseline (2026-11-23)

Checkpoint taken before the AWS deployment work. "Verified" means observed directly in this session; the rest is carried over from earlier reports and marked as such.

## Repository

| Item | State | Status |
|---|---|---|
| Remote / default branch | `TuanKietTran/llm-toolkit`, public, `main` unprotected | Verified (`gh api`) |
| `origin/main` at start | `ba0e491` | Verified |
| Working branch | `feat/job-cv-finder-aws`, one local commit `3ff183d` (bootstrap template) on top of `ba0e491`, tree clean | Verified |
| Go | `go vet ./...` and `go test ./...` pass with SDK 1.27.4 (`GOTOOLCHAIN=local`) | Verified |
| Python | 12 tests, run in CI (`uv run pytest`) | Verified in CI |
| Application database | SQLite WAL, queue in the same database (ADR-0002, ADR-0004). No Postgres code, so no RDS | Verified in code |
| CI before this work | none | Verified (`.github/` absent) |
| Dockerfiles / IaC before this work | none | Verified |

## AWS (read-only, through the signed-in console and the deploy role)

| Claim | Result |
|---|---|
| `jcf-bootstrap` stack created the deploy role | Role `jcf-deploy-llm-toolkit` exists, trust is the account's GitHub OIDC provider. Verified in the IAM console |
| Trust policy subject `repo:TuanKietTran/llm-toolkit:environment:aws-prod` | From the template; the first workflow run assumes the role through that subject (see `docs/ops/JOURNAL.md`) |
| Policies `jcf-boundary`, `jcf-deploy-ec2`, `jcf-deploy-platform`, `jcf-deploy-iam-guard` | Defined in the template; actual effective permissions are exercised by Terraform plan and apply in CI |
| GitHub environment `aws-prod` absent | Confirmed absent (404), created in this session |
| Eight `lab-deploy-*` roles vs four in the local template | **Resolved by inspection.** The account has eight: `double-entry-ledger-go`, `event-archive`, `fraud-detection-go`, `hiring-platform`, `loan-platform-go`, `payment-orchestration-go`, `payment-reconciler`, `portfolio-tracker-go`. The live `lab-bootstrap` stack covers the five Go fintech repos plus three others, while the lab template kept outside this repository still lists four repos. That local copy is stale; no lab resource was touched |
| Roles in the account | 15: six service-linked, `jcf-deploy-llm-toolkit`, eight `lab-deploy-*` |

## DNS

`mashallow.cloud` is on Cloudflare. `jcf.mashallow.cloud` **already exists** as a proxied Cloudflare Tunnel record (`jcf`) and answers `/healthz` with 200: a Job CV Finder instance runs on the owner's PC behind it. It was not changed. The AWS deployment uses `jcf-aws.mashallow.cloud` instead; moving the preferred hostname is a deliberate cut-over (see `docs/ops/RUNBOOK.md`).

## Ruxt

The Ruxt repository already holds the secrets `NUXT_JOB_AUTOMATION_URL`, `NUXT_JOB_AUTOMATION_TOKEN` and `NUXT_JOB_AUTOMATION_OWNER`, which its deploy workflow writes to the Worker. They currently target the local instance.
