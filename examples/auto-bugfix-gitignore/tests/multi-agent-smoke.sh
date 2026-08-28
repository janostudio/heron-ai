#!/usr/bin/env bash
set -euo pipefail

# Low-complexity collaboration test:
#   diagnose Team: command + explorer Agent + root-cause Agent
#       ↓ Flow SharedRecords
#   review Team: challenger Agent
#
# This test is read-only. It verifies orchestration above one Agent without
# running the full repair/learning/audit pipeline.

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HERON_ROOT="$(cd "$ROOT/../.." && pwd)"
PORT="${HERON_TEST_PORT:-18088}"
SERVER_URL="http://127.0.0.1:${PORT}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/heron-multi-agent-smoke.XXXXXX")"
SERVER_LOG="$TMP_DIR/server.log"
RESULT="$TMP_DIR/result.json"
BIN="${HERON_BIN:-$TMP_DIR/heron}"
FLOW=".agents/flows/multi_agent_smoke.yml"
SESSION_ID=""

cleanup() {
  if [[ -n "${SERVER_PID:-}" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  if [[ -n "$SESSION_ID" ]]; then
    rm -rf "$ROOT/.agents/data/sessions/$SESSION_ID"
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

if [[ -z "${HERON_BIN:-}" ]]; then
  (
    cd "$HERON_ROOT"
    GOCACHE="${GOCACHE:-/tmp/heron-ai-go-cache}" go build -o "$BIN" ./cmd/server
  )
fi

before_hash="$(shasum "$ROOT/project/.gitignore" | awk '{print $1}')"

(
  cd "$ROOT"
  "$BIN" --serve --flow "$FLOW" --port "$PORT"
) >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 160); do
  if grep -q "FlowRuntime server listening on :${PORT}" "$SERVER_LOG" 2>/dev/null; then
    ready=1
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    cat "$SERVER_LOG"
    exit 1
  fi
  sleep 0.1
done
if [[ "$ready" -ne 1 ]]; then
  cat "$SERVER_LOG"
  echo "multi-agent smoke server did not become ready" >&2
  exit 1
fi

set +e
http_code="$(
  curl -sS -o "$RESULT" -w '%{http_code}' \
    -X POST "$SERVER_URL/api/run" \
    -H 'Content-Type: application/json' \
    --data '{"input":"请检查 project/.gitignore，给出最小修复建议，但不要修改文件。"}'
)"
curl_exit=$?
set -e
if [[ "$curl_exit" -ne 0 || "$http_code" != "200" ]]; then
  cat "$RESULT" 2>/dev/null || true
  cat "$SERVER_LOG"
  echo "multi-agent smoke request failed: curl_exit=$curl_exit http_code=$http_code" >&2
  exit 1
fi

cat "$RESULT"
SESSION_ID="$(python3 - "$RESULT" <<'PY'
import json
import sys
payload = json.load(open(sys.argv[1]))
print(payload.get("Session", {}).get("id", ""))
PY
)"

python3 - "$RESULT" <<'PY'
import json
import sys

payload = json.load(open(sys.argv[1]))
session = payload.get("Session", {})
teams = payload.get("TeamResults", [])
records = payload.get("Records", [])
team_ids = [item.get("Turn", {}).get("team_id") for item in teams]
record_names = [item.get("Name") for item in records]

assert session.get("status") == "completed", session
assert team_ids == ["diagnose", "review"], team_ids
assert "DiagnosisReport" in record_names, record_names
assert "ChallengeReport" in record_names, record_names
print("low-complexity multi-agent collaboration passed")
PY

after_hash="$(shasum "$ROOT/project/.gitignore" | awk '{print $1}')"
if [[ "$before_hash" != "$after_hash" ]]; then
  echo "read-only collaboration smoke modified project/.gitignore" >&2
  exit 1
fi
