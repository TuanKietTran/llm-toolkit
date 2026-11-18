# Job-lead ingestion via bourbon's own Claude Code + claude-in-chrome

Run this from the Claude Code session already logged in natively on bourbon
(the one with claude-in-chrome already configured against your real Chrome
profile) — not via IMAP/app-password. This replaces the
earlier `ingest_leads.py` IMAP approach, which is no longer used.

## Task

1. Use claude-in-chrome to open `https://mail.google.com` (should already be
   logged in as your job-search Gmail account in this Chrome profile).
2. Search Gmail for leads from two sources:
   - `from:itviec` — application-confirmation emails (Vietnamese subject
     lines, pattern: `Chúc mừng! Hồ sơ của bạn cho vị trí <TITLE> tại/đến
     <COMPANY> đã được gửi đi!` or the "Cảm ơn bạn đã gửi hồ sơ..." receipt
     variant of the same).
   - `from:jobs-noreply@linkedin.com` — status notifications (pattern:
     `Your application was viewed by <COMPANY>`, etc).
3. For each matching thread, extract: `source` (`"itviec"` or `"linkedin"`),
   `title`, `company`, `date` (thread date), `snippet` (the subject line
   verbatim is fine), `url` (leave `""` if no direct posting link is visible
   in the email itself — don't guess one), `status` (`"applied"` for
   confirmation/receipt emails, `"in-conversation"` for back-and-forth
   recruiter threads).
4. Dedupe by `(company, title)` lowercased — ITviec sends a receipt *and* a
   confirmation email per application; that's one lead, not two.
5. Write the result as a JSON array to:
   `packages/chatgpt-orchestrator/job-cv-finder/leads/leads.json` in your llm-toolkit checkout
   (gitignored — it holds your real application history)
   (`run.sh` / `run.ps1` read it from there; override with `LEADS_PATH`).

## Schema (match this exactly — the orchestrate prompt reads it as-is)

```json
[
  {
    "source": "itviec",
    "title": "Senior Frontend Engineer (Banking, TypeScript)",
    "company": "RAKSUL Vietnam",
    "url": "",
    "snippet": "Chúc mừng! Hồ sơ của bạn cho vị trí Senior Frontend Engineer (Banking, TypeScript) tại RAKSUL Vietnam đã được gửi đi!",
    "date": "2026-08-21",
    "status": "applied"
  }
]
```

## Re-running later

Re-run this whenever you want fresh leads — overwrite
`leads.json` wholesale each time (M3 always re-scores the full current set,
it doesn't diff against a previous run).
