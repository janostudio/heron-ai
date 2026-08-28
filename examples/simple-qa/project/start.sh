#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PID_FILE="$ROOT/service.pid"
LOG_FILE="$ROOT/service.log"

if [ -f "$PID_FILE" ]; then
  pid="$(cat "$PID_FILE" 2>/dev/null || true)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    echo "SERVICE_ALREADY_RUNNING pid=$pid"
    exit 0
  fi
  rm -f "$PID_FILE"
fi

rm -f "$LOG_FILE"
PORT="${PORT:-0}" nohup node "$ROOT/server.mjs" >"$LOG_FILE" 2>&1 &
pid=$!
echo "$pid" >"$PID_FILE"

ready=0
for _ in $(seq 1 100); do
  if grep -q "SERVICE_READY" "$LOG_FILE"; then
    ready=1
    break
  fi
  if ! kill -0 "$pid" 2>/dev/null; then
    cat "$LOG_FILE"
    rm -f "$PID_FILE"
    exit 1
  fi
  sleep 0.1
done

if [ "$ready" -ne 1 ]; then
  cat "$LOG_FILE"
  kill "$pid" 2>/dev/null || true
  rm -f "$PID_FILE"
  exit 1
fi

cat "$LOG_FILE"
echo "SERVICE_RUNNING pid=$pid"
