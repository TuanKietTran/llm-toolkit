#!/usr/bin/env bash
# End-to-end check across processes: Go core + mock ATS + Python intelligence worker.
# Runs the first vertical slice against the MOCK ATS only; it never contacts a real platform.
#
#   Ruxt-shaped snapshot -> discovery (mock board) -> normalize/dedup -> match -> CV selection
#   -> documents + answers -> policy -> authorized API submission -> lookup verification
#   -> receipt + history, plus an email-intake handoff verified by a confirmation email.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d)"
CORE_PORT="${CORE_PORT:-18790}"; ATS_PORT="${ATS_PORT:-18791}"
API="http://127.0.0.1:$CORE_PORT"
export JCF_API_TOKEN="e2e-api-token-$(date +%s)-abcdefghij" JCF_WORKER_TOKEN="e2e-worker-token-$(date +%s)-abcdefg"
export JCF_SECRET_KEY="$(head -c 32 /dev/urandom | base64)"
ATS_KEY="e2e-ats-key-$RANDOM"
PIDS=()
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

echo "== build"
(cd "$HERE/core" && go build -o "$WORK/jcf" ./cmd/jcf && go build -o "$WORK/mockats" ./cmd/mockats)

echo "== start mock ATS and core"
MOCKATS_KEY="$ATS_KEY" MOCKATS_ADDR="127.0.0.1:$ATS_PORT" MOCKATS_BOARD=demo "$WORK/mockats" >"$WORK/ats.log" 2>&1 & PIDS+=($!)
JCF_ADDR="127.0.0.1:$CORE_PORT" JCF_DB="$WORK/jcf.db" JCF_MOCKATS_URL="http://127.0.0.1:$ATS_PORT" JCF_DISCOVER_EVERY=24h \
  "$WORK/jcf" >"$WORK/core.log" 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do curl -fsS "$API/readyz" >/dev/null 2>&1 && break; sleep 0.2; done

call() { curl -fsS -H "Authorization: Bearer $JCF_API_TOKEN" -H 'Content-Type: application/json' "$@"; }

echo "== start Python worker"
(cd "$HERE/intel" && uv sync -q && { PY=.venv/bin/python; [ -x "$PY" ] || PY=.venv/Scripts/python; JCF_CORE_URL="$API" exec "$PY" -m jcf_intel 2>"$WORK/worker.log"; }) & PIDS+=($!)

echo "== candidate snapshot from Ruxt (fixture), credential, discovery"
call -X POST "$API/api/v1/candidate/snapshots" --data-binary @"$HERE/scripts/fixtures/snapshot.json" >/dev/null
call -X PUT "$API/api/v1/accounts/mockats:demo/credential" -d "{\"credential\":\"$ATS_KEY\"}" >/dev/null
call -X POST "$API/api/v1/sources/mockats:demo/discover"; echo

echo "== email intake: LinkedIn alert (handoff path)"
call -X POST "$API/api/v1/intake/emails" --data-binary @"$HERE/scripts/fixtures/linkedin-alert.json"; echo

state_of() { call "$API/api/v1/applications" | python -c "import sys,json; a=json.load(sys.stdin)['applications']; print(' '.join(sorted(x['job']['external_job_id']+'='+x['state'] for x in a)))"; }
echo "== waiting for workflows to settle"
for i in $(seq 1 60); do
  s="$(state_of)"
  case "$s" in *MATCHING*|*PREPARING*|*SELECTED*|*PREPARED*|*POLICY_CHECK*|*EXECUTING*|*VERIFYING*|*MATCHED\ *|*MATCHED) sleep 1;; *) break;; esac
done
echo "$s"

echo "== LinkedIn confirmation email arrives"
call -X POST "$API/api/v1/intake/emails" --data-binary @"$HERE/scripts/fixtures/linkedin-confirmation.json"; echo
final="$(state_of)"; echo "$final"

echo "== mock ATS received"
curl -fsS "http://127.0.0.1:$ATS_PORT/_mock/applications"; echo

fail=0
expect() { case " $final " in *" $1 "*) echo "PASS $1";; *) echo "FAIL expected $1"; fail=1;; esac; }
expect "1001=SUBMITTED"
expect "1002=NOT_SELECTED"
expect "1003=BLOCKED_EXCEPTION"
expect "4486000001=SUBMITTED"
n="$(curl -fsS "http://127.0.0.1:$ATS_PORT/_mock/applications" | python -c 'import sys,json; print(len(json.load(sys.stdin)))')"
[ "$n" = "1" ] && echo "PASS exactly one ATS submission" || { echo "FAIL ATS submissions=$n"; fail=1; }
if grep -q "$ATS_KEY" "$WORK/core.log" "$WORK/worker.log"; then echo "FAIL credential in logs"; fail=1; else echo "PASS no credential in logs"; fi
[ $fail = 0 ] || { echo "--- core.log"; tail -40 "$WORK/core.log"; echo "--- worker.log"; tail -40 "$WORK/worker.log"; }
exit $fail
