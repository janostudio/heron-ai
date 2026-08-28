#!/bin/sh

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PID_FILE="$ROOT/service.pid"
LOG_FILE="$ROOT/service.log"

if [ ! -f "$PID_FILE" ]; then
  echo "SERVICE_STOPPED"
  exit 1
fi

pid="$(cat "$PID_FILE")"
if ! kill -0 "$pid" 2>/dev/null; then
  echo "SERVICE_STOPPED pid=$pid"
  exit 1
fi

echo "SERVICE_RUNNING pid=$pid"
cat "$LOG_FILE"
