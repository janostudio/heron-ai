#!/bin/sh

# Real integration test:
#   stream-json CLI → HTTP FlowRuntime → hy3-ioa Agent → Tool Policy/HITL
#   → HTTP approval → Agent Resume → Bash execution.
#
# It intentionally uses the existing local .agents/models.json and never
# edits that file. The test is destructive only inside its temporary
# project/approval-sentinel directory.

set -eux

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
HERON_ROOT="$(CDPATH= cd -- "$ROOT/../.." && pwd)"
PORT="${HERON_TEST_PORT:-18081}"
SERVER_URL="http://127.0.0.1:${PORT}"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/heron-approval-stream-json.XXXXXX")"
SERVER_LOG="$TMP_DIR/server.log"
FIRST_OUTPUT="$TMP_DIR/first.jsonl"
SECOND_OUTPUT="$TMP_DIR/second.jsonl"
BIN="${HERON_BIN:-$TMP_DIR/heron}"

cleanup() {
  if [ -n "${SERVER_PID:-}" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  echo "DEBUG_TMP_DIR=$TMP_DIR" >&2
  rm -rf "$ROOT/project/approval-sentinel"
  if [ -n "${session_id:-}" ]; then
    rm -rf "$ROOT/.agents/data/sessions/$session_id"
    for job in "$ROOT"/.agents/data/consolidations/*.json; do
      [ -f "$job" ] || continue
      if grep -q "\"flow_session_id\": \"$session_id\"" "$job"; then
        rm -f "$job"
      fi
    done
  fi
}
trap cleanup EXIT INT TERM

if [ -z "${HERON_BIN:-}" ]; then
  (
    cd "$HERON_ROOT"
    GOCACHE="${GOCACHE:-/tmp/heron-ai-go-cache}" go build -o "$BIN" ./cmd/server
  )
fi

rm -rf "$ROOT/project/approval-sentinel"
mkdir -p "$ROOT/project/approval-sentinel"
printf '%s\n' 'must be removed only after approval' >"$ROOT/project/approval-sentinel/marker.txt"

(
  cd "$ROOT"
  "$BIN" --serve --flow .agents/flows/default.yml --port "$PORT"
) >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in $(seq 1 160); do
  if curl -fsS "$SERVER_URL/api/status?session_id=probe" >/dev/null 2>&1 ||
    grep -q 'FlowRuntime server listening' "$SERVER_LOG" 2>/dev/null; then
    ready=1
    break
  fi
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    cat "$SERVER_LOG"
    exit 1
  fi
  sleep 0.1
done
if [ "$ready" -ne 1 ]; then
  cat "$SERVER_LOG"
  echo "server did not become ready" >&2
  exit 1
fi

prompt='必须使用 Bash 执行命令 rm -rf project/approval-sentinel。这个危险命令必须先等待人工审批，不要改用其他工具或只给出建议。'
printf '%s\n' \
  "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$prompt")}}" |
  (
    cd "$ROOT"
    "$BIN" --flow .agents/flows/default.yml --server "$SERVER_URL" \
      --input-format stream-json --output-format stream-json
  ) >"$FIRST_OUTPUT"

first_line="$(sed -n '1p' "$FIRST_OUTPUT")"
if [ -z "$first_line" ]; then
  cat "$SERVER_LOG"
  echo "stream-json CLI returned no response" >&2
  exit 1
fi

session_id="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d.get("session_id",""))' "$first_line")"
approval_id="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); p=d.get("pending_approvals") or []; print((p[0] if p else {}).get("request_id",""))' "$first_line")"
subtype="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d.get("subtype",""))' "$first_line")"

if [ "$subtype" != "permission_required" ] || [ -z "$session_id" ] || [ -z "$approval_id" ]; then
  cat "$FIRST_OUTPUT"
  cat "$SERVER_LOG"
  echo "expected permission_required with session_id and approval_id" >&2
  exit 1
fi

# Durable approvals intentionally do not expire. Wait before approving to
# verify this is not an in-memory timeout path.
sleep 1

printf '%s\n' \
  "{\"type\":\"permission_response\",\"session_id\":\"$session_id\",\"approval_id\":\"$approval_id\",\"approved\":true,\"reason\":\"approved by integration test\",\"approver_id\":\"qa-user\",\"approver\":\"QA User\",\"channel\":\"stream-json\"}" |
  (
    cd "$ROOT"
    "$BIN" --flow .agents/flows/default.yml --server "$SERVER_URL" \
      --input-format stream-json --output-format stream-json
  ) >"$SECOND_OUTPUT"

second_line="$(sed -n '1p' "$SECOND_OUTPUT")"
second_subtype="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d.get("subtype",""))' "$second_line")"
second_status="$(python3 -c 'import json,sys; d=json.loads(sys.argv[1]); print(d.get("status",""))' "$second_line")"

if [ "$second_subtype" != "success" ] || [ "$second_status" != "completed" ]; then
  cat "$SECOND_OUTPUT"
  cat "$SERVER_LOG"
  echo "approval resume did not complete the Flow" >&2
  exit 1
fi

if [ -e "$ROOT/project/approval-sentinel/marker.txt" ]; then
  cat "$FIRST_OUTPUT"
  cat "$SECOND_OUTPUT"
  echo "dangerous Bash command was not executed after approval" >&2
  exit 1
fi

session_file="$ROOT/.agents/data/sessions/$session_id/session.jsonl"
if ! grep -q '"type":"approval.resolved"' "$session_file" ||
  ! grep -q '"approver_id":"qa-user"' "$session_file" ||
  ! grep -q '"channel":"stream-json"' "$session_file"; then
  cat "$FIRST_OUTPUT"
  cat "$SECOND_OUTPUT"
  echo "approval audit event is missing approver/channel fields" >&2
  exit 1
fi

printf '%s\n' "dangerous Bash HTTP approval stream-json test passed"
printf '%s\n' "session_id=$session_id approval_id=$approval_id"
