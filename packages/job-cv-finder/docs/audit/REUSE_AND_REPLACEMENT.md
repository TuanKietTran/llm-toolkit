# Reuse and replacement

| Existing piece | Decision | Reason |
|---|---|---|
| `job-cv-finder/prompts/orchestrate.md` + `run.*` | **Kept, superseded.** Left in place, not called by the platform | It is an agent procedure with no durable state. Its rules (no CAPTCHA solving, count only verified submissions, never invent CV content) are now enforced in code: policy engine, receipts, evidence validator |
| `leads/leads.json` history | **Replaced** by `jobs` + `applications` + `inbound_messages` | Dedup now uses canonical keys across sources, and confirmation emails create verified receipts |
| `INGEST_VIA_CHROME.md` (Gmail via browser assistant) | **Replaced** by `POST /api/v1/intake/emails` (JSON or raw RFC 822) | Deterministic parsers with tests. Any mail fetcher can feed it, including an export or a scheduled forwarder |
| `jobsite/driver.py` search parsing | **Isolated, not reused** | Polling LinkedIn/ITviec pages with a logged-in automated browser is not established as permitted (see SOURCES.md). Email intake gives the same jobs, with stable ids for LinkedIn |
| `jobsite/driver.py` apply flow | **Isolated, not reused** | It imports `browser/stealth.py` (fingerprint spoofing, human-like input), which the requirements forbid. Its selectors are self-described as unverified. "URL changed" counts as success. It has no tests |
| `jobsite/manager.py` storage_state contract (human login only) | **Principle reused** | The new account model never performs a login or stores platform passwords |
| `jobsite` CAPTCHA abort | **Principle reused** | No path in the new platform touches CAPTCHA or MFA |
| `browser/stealth.py` | **Not used by job-cv-finder** | It remains the ChatGPT pool's concern and is out of scope here |
| ChatGPT browser worker pool | **Not used** | Not a reliable structured-output provider. Replaced by `ModelProvider` (any OpenAI-compatible endpoint) with validated output |
| `gorouter` | **Reused** as the recommended model endpoint | Already provides provider fallback and quotas behind one OpenAI-compatible URL |
| cv-editor MCP tools (`list_templates`, `render_pdf`, …) | **Not reused** | These belong to Ruxt's retired server store. Ruxt's MCP tools currently all return "unavailable" |

## What would be needed to re-enable browser submission

All of the following, recorded in the source's policy (`domain.SourcePolicy`):

1. Written permission or terms that allow it, read by a human, with the date recorded (`terms_verified`, `terms_verified_on`).
2. A `PermittedBrowserExecutor` without stealth, running in an isolated container with one context per account.
3. Positive verification by receipt (email or platform record), never a URL change.
4. Tests against a recorded or mock form.

Until then, LinkedIn, ITviec, CareerViet and VietnamWorks use handoff.
