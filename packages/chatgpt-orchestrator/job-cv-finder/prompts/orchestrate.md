# Job CV & Finder — orchestration task

You control three tool surfaces. **The bridge that exposes them to you prefixes every tool name
with `chatgpt_`, regardless of which surface it actually belongs to** — that's a naming quirk of
`integrations/pi/index.ts`, not a hint about what the tool does. Call `pi list-tools` mentally by
their real names below; MCP *resources* (anything with a `://` in its name) are not bridged to you
at all, only tools are — don't try to read one.

- A pool of browser-driven ChatGPT worker contexts (`chatgpt_spawn_worker` /
  `chatgpt_ask_batch` / `chatgpt_close_worker`) — anonymous, cheap, used for
  scoring/drafting text.
- A CV editor via the cv-editor MCP tools (`chatgpt_list_templates`,
  `chatgpt_get_template`, `chatgpt_open_project`, `chatgpt_save_project`,
  `chatgpt_render_pdf`).
- Real, credentialed job-site sessions (`chatgpt_jobsite_search`,
  `chatgpt_jobsite_apply`, `chatgpt_jobsite_read_context`) — logged in as
  Kyle's own ITviec/LinkedIn accounts. There is no scripted login here by
  design (see `src/orchestrator/jobsite/manager.py`): if a site has no
  captured session yet, `chatgpt_jobsite_search`/`chatgpt_jobsite_apply`
  raise a `LoginRequired` tool error naming the missing storage_state path.
  Treat that as "skip this site, report it" — never as something to retry
  or work around.

Workers and job-site sessions all run **headless** (no visible browser
window). If a reply or apply result looks wrong, empty, or you suspect a
login/challenge wall, call `chatgpt_read_context` / `chatgpt_jobsite_read_context`
(`include_screenshot=true`) before trusting or retrying — they return the
actual page url/text/screenshot so you can tell a real stall from a bad
prompt.

Master CV content lives in cv-editor project `master`. Five **base subject
CVs** already exist as cv-editor projects, one per specialization: adapt
whichever one's project name most closely matches — call
`chatgpt_list_templates` to get the exact current names (as of the last
manual campaign they were `data-engineering-tidemark`,
`erp-invoice-automation`, `rag-throughline`, `streaming-slipstream`,
`systems-integration-interlock`).

Historical record (already-applied leads, for dedup only — NOT a source of
new leads, most entries have no URL): `{{LEADS_PATH}}`.

## Scope (defaults — Kyle can override at check-in, otherwise apply these)

- Location: Ho Chi Minh City or fully remote roles only.
- Stack: prefer AI/LLM, DevOps/DevSecOps, fullstack, or backend roles.
- Exclude pure-QA sourcing roles.

## 1. Search (live, both sites)

For each of `itviec` and `linkedin`, call `chatgpt_jobsite_search(site,
query, location=..., remote=...)` with a handful of queries covering the
scope above (e.g. "backend engineer", "devops", "AI engineer LLM",
"fullstack developer"). Merge and dedupe results by `(company, title)`
lowercased.

Drop any lead whose `(company, title)` already appears in `{{LEADS_PATH}}`
with `status: "applied"` — don't re-apply to something already submitted in
a prior run.

If `chatgpt_jobsite_search` raises `LoginRequired` for a site, skip that
site, record why, and continue with whichever site(s) still work — don't
abort the whole run over one missing session.

## 2. Scoring (all deduped leads)

In batches no larger than 8 concurrent workers (`scaling.max_contexts=8`):
1. Spawn a worker.
2. Ask it to score fit 0-100 against the master CV's actual content, plus a
   one-line rationale.
3. Collect scores via `chatgpt_ask_batch`; close each worker once scored.

## 3. Tailoring (top 5 scored leads only)

For each of the top 5:
1. Pick whichever base subject CV project's specialization best matches the
   role's actual stack (not `master` directly — the subject CVs are already
   tailored starting points).
2. Spawn a worker to draft 3-5 tailored resume bullets and a 3-sentence
   cover note **using only real content already present in the chosen base
   CV / master CV** — never invent employers, dates, or skills that aren't
   there. If the role wants something the CV doesn't have, say so honestly
   in the cover note (a stated gap, not a fabricated skill) rather than
   omitting or embellishing.
3. `chatgpt_open_project` the chosen base subject project, apply the
   tailored bullets, `chatgpt_save_project` as `<company>-<role>`
   (slugified), reusing the base project's CSS.
4. `chatgpt_render_pdf` the saved project, then rename/copy the output file
   to `{{CANDIDATE_NAME}} - <Employer>.pdf` (never edit a base subject CV
   file in place — always clone-and-rename a new file per application).

## 4. Apply (top 5, after tailoring)

For each tailored lead, call `chatgpt_jobsite_apply(lead, cv_path,
cover_note)` using the exact renamed PDF path from step 3.

- **CAPTCHA / bot-check rule, non-negotiable:** `chatgpt_jobsite_apply`
  already aborts and returns `status="skipped_captcha"` the moment it sees
  one — never retry that lead, never call `chatgpt_jobsite_apply` on it
  again, never try to work around it another way. Log it as skipped and
  move on.
- Only count a lead as actually submitted if the result has `verified:
  true` and `status: "submitted"` — a clicked-but-unverified attempt
  (`status: "failed"`) is not a submission; note the `detail` field and move
  on rather than retrying blindly.
- `status: "skipped_login_required"` means that site's session expired
  mid-run — stop applying to that site for the rest of this run and report
  it (needs a fresh `capture_session` run from a human), but keep going on
  the other site.
- If `chatgpt_jobsite_apply`'s upload confirmation looks ambiguous (see its
  `detail`/`evidence_text`), call `chatgpt_jobsite_read_context(site,
  include_screenshot=true)` before deciding whether to trust it.

## Output

A single markdown table, nothing else after it:

| title | company | score | rationale | pdf path | application url | apply status | verified |
|---|---|---|---|---|---|---|---|

`apply status` is the `jobsite_apply` result's `status` field exactly
(`submitted` / `skipped_captcha` / `skipped_login_required` / `failed`).
`verified` is its `verified` field. Only rows with `verified: true` count as
real applications when you summarize the run afterward.
