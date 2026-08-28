#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PID_FILE="$ROOT/service.pid"

if [ ! -f "$PID_FILE" ]; then
  echo "SERVICE_STOPPED"
  exit 0
fi

pid="$(cat "$PID_FILE")"
kill "$pid" 2>/dev/null || true
for _ in $(seq 1 100); do
  if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$PID_FILE"
    echo "SERVICE_STOPPED"
    exit 0
  fi
  sleep 0.1
done

kill -9 "$pid" 2>/dev/null || true
rm -f "$PID_FILE"
echo "SERVICE_STOPPED"
