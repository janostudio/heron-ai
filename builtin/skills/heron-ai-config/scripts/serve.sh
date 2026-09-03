#!/bin/sh
# 后台启动 / 停止 heron-ai 服务（HTTP 或 JSON-RPC），带 PID 管理。
#
# 用法：
#   serve.sh start [--flow <path>] [--mode http|jsonrpc] [--port <port>]
#   serve.sh stop
#   serve.sh status
#
# 环境变量：
#   HERON_BIN     heron 二进制路径（默认 PATH 里的 heron）
#   HERON_FLOW    flow 配置路径（默认 .agents/flows/default.yml）
#   HERON_MODE    运行模式 http|jsonrpc（默认 http）
#   HERON_PORT    端口（默认 8080）
#   HERON_LOG     日志文件（默认 ./heron-service.log）
#   HERON_PID     PID 文件（默认 ./heron-service.pid）

set -eu

ROOT="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
HERON_BIN="${HERON_BIN:-heron}"
HERON_FLOW="${HERON_FLOW:-.agents/flows/default.yml}"
HERON_MODE="${HERON_MODE:-http}"
HERON_PORT="${HERON_PORT:-8080}"
HERON_LOG="${HERON_LOG:-$ROOT/heron-service.log}"
HERON_PID="${HERON_PID:-$ROOT/heron-service.pid}"

log() { echo "$@"; }

running_pid() {
  if [ -f "$HERON_PID" ]; then
    pid="$(cat "$HERON_PID" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      echo "$pid"
      return 0
    fi
  fi
  return 1
}

cmd_start() {
  if running_pid >/dev/null 2>&1; then
    log "ALREADY_RUNNING pid=$(running_pid)"
    exit 0
  fi

  rm -f "$HERON_LOG"
  case "$HERON_MODE" in
    jsonrpc)
      nohup "$HERON_BIN" --json-rpc --flow "$HERON_FLOW" >"$HERON_LOG" 2>&1 &
      ;;
    *)
      nohup "$HERON_BIN" --serve --port "$HERON_PORT" --flow "$HERON_FLOW" >"$HERON_LOG" 2>&1 &
      ;;
  esac
  pid=$!
  echo "$pid" >"$HERON_PID"
  log "STARTED pid=$pid mode=$HERON_MODE"
}

cmd_stop() {
  if ! running_pid >/dev/null 2>&1; then
    rm -f "$HERON_PID"
    log "STOPPED"
    exit 0
  fi
  pid="$(running_pid)"
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 1 100); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$HERON_PID"
      log "STOPPED"
      exit 0
    fi
    sleep 0.1
  done
  kill -9 "$pid" 2>/dev/null || true
  rm -f "$HERON_PID"
  log "STOPPED (forced)"
}

cmd_status() {
  if running_pid >/dev/null 2>&1; then
    log "RUNNING pid=$(running_pid)"
  else
    log "STOPPED"
  fi
}

case "${1:-}" in
  start)  cmd_start ;;
  stop)   cmd_stop ;;
  status) cmd_status ;;
  *) log "用法: $0 {start|stop|status}"; exit 1 ;;
esac
