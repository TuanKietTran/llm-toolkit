#!/usr/bin/env bash
# Interactive smoke environment: starts gorouter plus a mock upstream and leaves them running.
# Usage: scripts/smoke.sh        (Ctrl-C to stop)
set -euo pipefail
cd "$(dirname "$0")/.."
PORT="${PORT:-20150}"
MOCK_PORT="${MOCK_PORT:-9911}"
KEY="${KEY:-smoke-key}"
DIR="$(mktemp -d)"
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$DIR"' EXIT

go build -o "$DIR/gorouter" ./cmd/gorouter
go build -o "$DIR/mock" ./cmd/mockupstream

cat > "$DIR/config.json" <<JSON
{"listen":"127.0.0.1:$PORT","api_keys":["$KEY"],
 "providers":[
  {"id":"flaky","type":"openai","base_url":"http://127.0.0.1:$MOCK_PORT/bad","models":["a"],"tier":1},
  {"id":"good","type":"openai","base_url":"http://127.0.0.1:$MOCK_PORT","models":["b"],"tier":2,"price_in":1,"price_out":2}],
 "combos":[{"name":"main","models":["flaky/a","good/b"]}],
 "aliases":{"default":"main"}}
JSON

"$DIR/mock" -addr "127.0.0.1:$MOCK_PORT" 2>/dev/null &
"$DIR/gorouter" -config "$DIR/config.json" -db "$DIR/gorouter.db" 2>&1 | sed 's/^/[gorouter] /' &
sleep 1

cat <<TXT

gorouter smoke environment is up.
  Dashboard : http://127.0.0.1:$PORT/dashboard/   (API key: $KEY)
  API key   : $KEY

Try:
  curl -s -H 'Authorization: Bearer $KEY' localhost:$PORT/v1/chat/completions \\
    -d '{"model":"main","messages":[{"role":"user","content":"hello"}]}'      # flaky 429 -> falls back to good
  curl -sN -H 'Authorization: Bearer $KEY' localhost:$PORT/v1/chat/completions \\
    -d '{"model":"default","stream":true,"messages":[{"role":"user","content":"stream me"}]}'
  curl -sN -H 'x-api-key: $KEY' localhost:$PORT/v1/messages \\
    -d '{"model":"main","max_tokens":50,"stream":true,"messages":[{"role":"user","content":"anthropic client"}]}'
  curl -s -H 'Authorization: Bearer $KEY' localhost:$PORT/admin/status   # watch the flaky cooldown

Ctrl-C to stop.
TXT
wait
