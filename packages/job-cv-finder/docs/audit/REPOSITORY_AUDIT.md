# Repository audit (Phase 0)

Audit date: 2026-11-20. **Verified** means read in source or executed. **Assumption** means believed but not tested.

## 1. Job CV Finder (as found in `packages/chatgpt-orchestrator/job-cv-finder/`)

**Verified.** It is not an application. It is a run script plus an agent prompt:

| File | What it is |
|---|---|
| `run.sh`, `run.ps1` | Launch the `pi` agent CLI with `integrations/pi/index.ts`, an OpenAI-hosted model, and the prompt below |
| `prompts/orchestrate.md` | Natural-language procedure: search ITviec/LinkedIn, score leads with ChatGPT browser workers, tailor via cv-editor MCP tools, apply via `jobsite_apply`, print a markdown table |
| `INGEST_VIA_CHROME.md` | Manual procedure: an assistant reads Gmail through a browser extension and writes `leads/leads.json` |
| `leads/leads.example.json` | Historical lead schema (`source,title,company,url,snippet,date,status`) |

The executable logic lives in `chatgpt-orchestrator` (Python 3.12, Playwright/Patchright, FastMCP):

- `src/orchestrator/jobsite/driver.py` (409 lines) drives logged-in ITviec/LinkedIn sessions. It parses search cards and runs an apply flow (find Apply, upload CV, submit, check success text). It aborts on CAPTCHA markers.
- `jobsite/manager.py` loads one captured `storage_state.json` per site (human login, no scripted login).
- `jobsite/selectors.py` has search selectors read from live markup (2026-08-31). Its own docstring says the apply-flow selectors are unverified.
- `browser/stealth.py` handles fingerprint generation, init-script patches, and human-like typing and pacing. `driver.py` imports `human_type`/`think` from it.

**State, persistence, workflow:** none. Leads are a JSON file, and applications exist only as the agent's final markdown table. Nothing is durable, idempotent or replay-safe.

**Tests:** the orchestrator has 20 test modules for the ChatGPT pool (driver, pool, budget, MCP server). **The `jobsite` package has no tests.**

**Verification weakness (verified in code):** `_submit_and_verify` treats any URL change after the submit click as success (`if hit or url_after != url_before`). A redirect to an error page, or to a further step of a multi-page form, would be recorded as `submitted`.

## 2. Ruxt

**Verified** (origin/main, 2026-11-20): Nuxt 4 + Vue 3, Nitro server, deployable to Deno Deploy, Cloudflare Workers (D1/KV) and SQLite on-prem. Auth is Clerk with a legacy session fallback (`server/utils/auth-user.ts`). It has a CQRS mediator (`@ruxt/shared/cqrs`) and private domain packages (`@ruxt/core`, vendored tgz).

**Candidate data ownership:**

- CV documents are **browser-local** (`app/utils/localCvStore.ts`, `localStorage`). The server CV store is retired.
- Profiles (`CvProfileProps`: identity, contacts, experiences, skills, certifications, education, projects, languages) are **browser-local** (`app/utils/localProfiles.ts`).
- The MCP endpoint (`server/routes/mcp.ts`) exists, but every CV tool returns *"CV documents are stored in the browser and are not available through MCP yet."*
- Cloud sync exists only as consent records. Cloud sessions, templates and export are *Planned* (`specs/authenticated-cloud-data.md`).
- Non-goals (`specs/prd/non-goals.md`): "No cloud by default", "standalone local profiles are never synchronised", "No public API platform", "no PostgreSQL adapter".

**Existing server CV capabilities:** a PDF/image import pipeline (`server/pipeline/extract.py`, owner-scoped, 24h artifacts) and public templates.

**UI conventions (enforced by `scripts/lint-architecture.mjs`):** non-CV tools use the `editor` layout with `#sidebar`/`#workspace` slots and a registered editor context. Icons come from a fixed Lucide allow-list. Every spec must be mapped in `specs/_readme.md`.

## 3. LLM Toolkit

| Component | Verified facts | Reuse verdict |
|---|---|---|
| `gorouter` (Go 1.27) | OpenAI- and Anthropic-compatible router with fallback combos, quotas, OAuth, dashboard; SQLite via modernc; well tested | **Reuse as the model endpoint**: the Python worker's `OpenAICompatibleProvider` points at it. Its SQLite/modernc choice matches this package |
| `chatgpt-orchestrator` pool | Browser-driven free ChatGPT web sessions, exposed over MCP | **Do not use for the workflow.** It is non-deterministic, has no structured-output guarantee, and depends on a third-party web UI and its terms |
| `jobsite` driver | See §1 | **Isolate.** Not wired into the new platform (see `REUSE_AND_REPLACEMENT.md`) |
| Agent harness (`integrations/pi`) | Bridges MCP tools into the `pi` CLI | Out of the workflow path. Agents may call the core API like any client |

## 4. Platform facts that shaped the design

| Source | Verified | Not verified |
|---|---|---|
| Greenhouse Job Board API | GETs need no auth. "Only the application submission endpoint … requires Basic Auth". The key is the board owner's ("Any direct post … would reveal your secret key") | Numeric rate limits ("may be rate limited") |
| Lever Postings API | Published postings "may be scraped by third parties". POST apply needs a key "a Super Admin of your account can generate". 429 above 2 POST/s | — |
| LinkedIn | Job alert and confirmation emails carry a stable job id (`/jobs/view/{id}`), observed in the candidate's own mailbox | The User Agreement page could not be fetched by automation (socket closed). The bot/automation clause must be re-read by a human |
| ITviec | Alert emails list title, employer, salary and skills. Confirmation subjects name title and company | Terms page returned HTTP 403 to automated fetch |
| CareerViet, VietnamWorks | — | Terms not reviewed |

## 5. Architectural conflicts and their resolution

1. **"Ruxt Integration Service reads profile and CVs from Ruxt"** conflicts with **Ruxt being local-first, with no server read API**. Resolution (ADR-0003): the Ruxt feature builds a snapshot in the browser from the owner's explicit selection and pushes it. Ruxt stays authoritative, and the core stores a versioned read-only copy.
2. **"Execute applications autonomously on preferred platforms"** conflicts with **automation terms for LinkedIn/ITviec not being verified as permitted, and the existing driver using stealth.** Resolution (ADR-0005): automated submission only through authorized APIs. LinkedIn, ITviec, CareerViet and VietnamWorks use handoff, verified by the platforms' own confirmation emails.
3. **"Authorized ATS submission (Greenhouse/Lever)"** conflicts with **those APIs needing the employer's key.** Resolution: discovery uses their public GETs and submission is handoff. The automated path is built and tested against a mock ATS, ready for any source where a candidate-held key is legitimate.
4. **"PostgreSQL"** conflicts with **a single-candidate deployment, no Postgres on the host, and Ruxt itself using SQLite/D1.** Resolution (ADR-0002): SQLite in WAL mode with portable SQL and migrations, and Postgres as a documented upgrade path.
