#!/usr/bin/env bash
# Job CV & Finder: pi is the controller; chatgpt-orchestrator's browser worker pool does the
# token-heavy scoring/drafting, and the jobsite_* MCP tools (src/orchestrator/jobsite/) drive
# real ITviec/LinkedIn sessions to search live postings and submit real applications.
#
# Config (env, or job-cv-finder/.env.local which is gitignored):
#   CANDIDATE_NAME   name used for rendered PDFs ("<name> - <Employer>.pdf")   [required]
#   PI_PROVIDER / PI_MODEL / PI_THINKING   default: openai-codex / gpt-5.6-sol / medium
#   LEADS_PATH / PROMPT_PATH               default: ./leads/leads.json, ./prompts/orchestrate.md
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ -f "$HERE/.env.local" ] && set -a && . "$HERE/.env.local" && set +a

LEADS_PATH="${LEADS_PATH:-$HERE/leads/leads.json}"
PROMPT_PATH="${PROMPT_PATH:-$HERE/prompts/orchestrate.md}"
: "${CANDIDATE_NAME:?set CANDIDATE_NAME (env or job-cv-finder/.env.local)}"

if [ ! -f "$LEADS_PATH" ]; then
  echo "No leads file at $LEADS_PATH -- follow INGEST_VIA_CHROME.md first." >&2
  exit 1
fi

PROMPT="$(sed -e "s#{{LEADS_PATH}}#$LEADS_PATH#g" -e "s#{{CANDIDATE_NAME}}#$CANDIDATE_NAME#g" "$PROMPT_PATH")"

# pi's project-local package auto-load (.pi/settings.json "packages") did not register the
# extension's tools in testing -- `-e` explicitly loading integrations/pi/index.ts is the only
# invocation that exposed the chatgpt_*/jobsite_* tools. Re-check `pi list` before "simplifying".
cd "$HERE/.."
RUN=()
if [ -z "${DISPLAY:-}" ] && [ "$(uname -s)" = "Linux" ] && command -v xvfb-run >/dev/null; then
  RUN=(xvfb-run -a)
fi
ORCH_BROWSER__HEADLESS="${ORCH_BROWSER__HEADLESS:-true}" "${RUN[@]}" pi -e ./integrations/pi/index.ts \
  --provider "${PI_PROVIDER:-openai-codex}" --model "${PI_MODEL:-gpt-5.6-sol}" \
  --thinking "${PI_THINKING:-medium}" -p "$PROMPT"
