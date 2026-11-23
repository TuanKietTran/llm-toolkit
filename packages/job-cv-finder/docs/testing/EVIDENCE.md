# Test evidence (2026-11-23)

Statuses: PASS, FAIL, BLOCKED, NOT RUN, PARTIAL. A blocked or unrun test is never counted as a pass. "e2e" is `scripts/e2e.sh`: a real Go core, Python worker and **mock** ATS in separate processes, run in CI on every push (it caught a Linux-only bug in the script itself during this work).

## Automated suites (GitHub Actions, run 45134994867 on the deployed commit)

| Suite | Result |
|---|---|
| `gofmt`, `go vet`, `go build`, `go test -race ./...` (7 packages with tests) | PASS |
| `govulncheck ./...` | PASS |
| Python `pytest` (12 tests) and `pip-audit` on the hashed runtime lockfile | PASS |
| Cross-process e2e (snapshot, discovery, matching, CV selection, documents, policy, authorized-API submission, verification, receipt, email-intake handoff, no credential in logs) | PASS |
| `terraform fmt -check`, `validate`, `shellcheck`, Trivy IaC/secret scan | PASS (three documented exceptions in `main.tf`) |
| Image scan, core and worker (Trivy, fixable HIGH/CRITICAL) | PASS (after removing pip and setuptools from the worker image, which had flagged vendored copies) |

## Production checks (deployed host, same run)

| Check | Result | Evidence |
|---|---|---|
| OIDC role assumption from the workflow | PASS | after fixing the trust subject (see `JOURNAL.md`) |
| Terraform apply, re-apply is a no-op | PASS | second and later runs: 0 changes |
| Containers healthy, data on the separate XFS volume | PASS | core `healthy`; `jcf.db`, `-wal`, `-shm` under `/var/lib/jcf` |
| DNS resolves to the Elastic IP | PASS | `getent hosts` equals the Terraform output |
| HTTPS | PASS | Let's Encrypt certificate for the hostname, valid about 90 days; `http://` answers 308 to `https://` |
| Private routes not public | PASS | `/metrics` and `/internal/v1/...` return 404 |
| API auth | PASS | 401 without a token, 200 with it |
| Backup, restore verification | PASS | backup object written to S3; `jcf-restore verify`: `integrity=ok tables=21` |
| Real discovery from production (read-only public boards) | PASS | 154 jobs from Cloudflare (Greenhouse), Spotify (Lever) and GitLab (Greenhouse, paced detail calls) |
| Restart persistence | PASS | 154 jobs before and after `restart core`; `integrity_check` ok; WAL mode; API 200 afterwards |
| Rolling deploy over a running stack | PASS | two successful deployments, the second over the running stack; a pre-deploy backup was taken before the second |
| Automatic rollback on an unhealthy release | NOT RUN | logic exists in `deploy.sh`; no failing release was provoked in production |
| Daily snapshot policy produces a snapshot | NOT RUN | policy created; first snapshot is due 03:30 UTC |
| 6-hourly backup timer fires | NOT RUN | next run is scheduled; the manual and pre-deploy runs work |
| CloudWatch alarm delivery | NOT RUN | alarm exists; the SNS topic has no subscriber |

## Brief's 24-item user journey

| # | Item | Status | Note |
|---|---|---|---|
| 1 | Login | PARTIAL | The owner's Clerk session was already signed in; the sign-in flow itself was not exercised (needs the owner's credentials and MFA) |
| 2 | Logout | NOT RUN | Signing out would end the owner's session and it cannot be restored without their credentials |
| 3 | Session expiry and recovery | NOT RUN | same reason |
| 4 | Ruxt profile synchronization | PARTIAL | Snapshot path covered by tests and e2e with a fixture; not run against the AWS core, which Ruxt does not yet use (see `KNOWN_LIMITATIONS.md`) |
| 5 | Candidate facts and evidence | PASS | pytest, e2e (fixture data, not the owner's) |
| 6 | CV listing and version retrieval | PASS | exact revisions enforced by Go; tests and e2e |
| 7 | Preferences | NOT RUN | in the browser |
| 8 | Job source configuration | PASS | sources seeded on AWS from variables; `/jobs/sources` renders on production Ruxt |
| 9 | Account connection and session handling | PASS (mock ATS only) | e2e; write-only encrypted credentials |
| 10 | Job discovery | PASS | real, on production |
| 11 | Normalization and deduplication | PASS | Go tests; production list returns primary jobs only |
| 12 | Matching and ranking | PASS (fixture candidate) | pytest and e2e. Not run on AWS: no candidate snapshot was sent |
| 13-16 | Selection, CV preparation, cover letter, answer generation | PASS (fixture candidate, mock ATS) | e2e |
| 17 | Policy checks | PASS | policy table tests and e2e |
| 18-19 | Controlled sandbox submission and verification | PASS (mock ATS only) | no real employer was contacted; no real source grants automated submission (ADR-0005) |
| 20 | Receipt persistence | PASS | e2e, Go tests (a submission is never marked SUBMITTED without its receipt) |
| 21 | Application history | PASS | e2e; screen renders |
| 22 | Refresh and navigation | PARTIAL | all 11 `/jobs` routes were opened by client-side navigation in the signed-in browser: each rendered its heading with no console errors. A hard refresh on each was not run |
| 23 | Restart and recovery | PASS | production restart test above; crash-during-submission test in Go |
| 24 | Failure and retry handling | PASS | fault-injection tests (lease expiry, dead letter, stale result, 5xx during submission) |

Screenshots were not saved: the signed-in pages show the owner's personal data.

## Not verified at all

- Anything that needs the owner's accounts: LinkedIn, ITviec, real employer applications, Ruxt login and logout.
- The browser journey against the **AWS-backed** Ruxt, because production Ruxt was not repointed.
