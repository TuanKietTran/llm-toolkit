# Job sources: capabilities and compliance

The policy data lives in `core/internal/sources/registry.go` and is visible in Ruxt under **Job sources**. This file records the evidence behind it. Re-verify the terms before changing any grant; record the date in `terms_verified_on`.

| Source | Role | Discovery | Job details | Submission | Tracking | Terms status |
|---|---|---|---|---|---|---|
| LinkedIn | Preferred | Job-alert emails (`jobalerts-noreply@linkedin.com`), pasted URLs | Pasted description | **Handoff** | Confirmation emails (`jobs-noreply@linkedin.com`, "your application was sent to …", job id) | **Unverified.** Automated fetch of the User Agreement failed (2026-11-20) |
| ITviec | Preferred | "Job Robot" alert emails, pasted jobs | Pasted description | **Handoff** | "ITviec đã nhận hồ sơ…" / "Chúc mừng! Hồ sơ của bạn … đã được gửi đi!" | **Unverified.** Terms page returned 403 to automated fetch |
| CareerViet | Candidate | Pasted jobs | Pasted description | **Handoff** | Candidate attestation (no parser yet) | Not reviewed |
| VietnamWorks | Candidate | Pasted jobs | Pasted description | **Handoff** | Candidate attestation (no parser yet) | Not reviewed |
| Greenhouse board | Per employer | Public Job Board API GET | `GET /jobs/{id}?questions=true` | **Handoff** (POST needs the employer's key) | — | Verified 2026-11-20 |
| Lever site | Per employer | Public Postings API GET ("may be scraped by third parties") | Same | **Handoff** (POST needs the employer's Super-Admin key) | — | Verified 2026-11-20 |
| Mock ATS | Test only | Greenhouse-shaped GET | Same | **Authorized API** | Lookup by idempotency key | Local fixture |

## Notes per source

**LinkedIn.** An alert block gives title, company, location and `/jobs/view/{id}`. The id is identical in the later confirmation email, so verification is an exact match. A URL pasted in Ruxt with `/jobs/view/{id}` gets the same id, so a pasted job, an alert and a confirmation collapse into one job. Alerts carry no description, so jobs are `partial` and matching confidence is reduced. Pasting the description re-runs matching. "Your application was viewed by X" emails are recorded as outcome events.

**ITviec.** Alert emails contain only tracking redirects, no job URL. The job id is derived from the canonical company+title, and the handoff link is an ITviec search for that title and company. Skill chips in the alert become required skills. Confirmation subjects give title and company; matching is by canonical key.

**Rate limits.** Polling sources default to 30 req/min, with discovery every 6 h. Email intake is bounded by the mailbox. The mock ATS allows 20 submissions a day.

**Data retention.** Raw snapshots and messages are kept for the source's `retention_days` (365 for platforms). See RUNBOOK for pruning.

## Explicitly not implemented

Scraping logged-in pages, automated login, storing platform passwords or cookies, CAPTCHA/MFA handling, stealth or fingerprinting, proxy or account rotation.
