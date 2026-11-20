# Security and compliance

## Trust boundaries

| Boundary | Control |
|---|---|
| Browser → Ruxt | Ruxt session (Clerk or legacy); `/api/jobs/*` requires the configured owner (other accounts get 404) |
| Ruxt → core | Bearer `JCF_API_TOKEN` held only by Ruxt's server; path and method allow-list in the proxy |
| Worker → core | Separate bearer `JCF_WORKER_TOKEN`; workers cannot call the candidate API and the reverse (tested) |
| Core → ATS | Credential decrypted only for the call it is scoped to |
| Untrusted content (job descriptions, emails, pages) | Parsed by fixed patterns; passed to models only inside an inert data block; model output validated against evidence; never interpreted as instructions |

## Secrets

- AES-256-GCM with a 32-byte key from the environment or secret manager. The ciphertext is bound to its reference (AAD), and the key id is recorded. Without a key the vault fails closed.
- Credentials are write-only: no endpoint returns them, accounts expose only `has_secret`, and task payloads never contain them. Tested: the stored key does not appear in API responses or logs.
- Platform passwords and browser sessions are refused for every source without an authorized-API grant.
- The logger redacts secret-looking attribute keys.

## Data minimisation

- The core stores only what the owner sends from Ruxt (one profile, selected CVs, preferences), plus derived facts with evidence locations.
- `GET /api/v1/candidate` returns snapshot metadata, never the CV content. Task listings omit payloads.

## Platform compliance

Enforced in code and policy (ADR-0005; `docs/sources/SOURCES.md`):

- Explicit capability grants per source, with a basis and the terms verification date.
- No automated submission without an `authorized_api` grant, `automated_submit=true`, and a connected account.
- No automated login, scraping of logged-in pages, CAPTCHA/MFA handling, stealth, fingerprint spoofing, or proxy/account rotation.
- Per-source concurrency of 1 for submissions and a daily submission limit.

## Integrity of the record

- No `SUBMITTED` state without a receipt: an ATS lookup, the platform's confirmation email, or an explicit candidate attestation (stored as unverified).
- Candidate facts are never invented. Unknown answers block with a question for the candidate. Generated statements carry evidence and are validated twice.
- Every state change is an append-only event in the same transaction.

## Known gaps

- TLS between Ruxt and the core is the deployment's responsibility (terminate at a reverse proxy).
- There is no per-request rate limit on the core API; it binds to localhost by default.
- `SECURITY` review of the Ruxt proxy by a second reviewer is pending.
