#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
cd "$ROOT"

bash .agents/scripts/preflight.sh

if [[ -z "${OPENAI_API_KEY:-}" ]]; then
  echo "OPENAI_API_KEY is required; fill it in your shell, not in models.json" >&2
  exit 1
fi

HERON_BIN="${HERON_BIN:-../../bin/heron}"
if [[ -x "$HERON_BIN" ]]; then
  command=("$HERON_BIN")
else
  command=(go run ../../cmd/server)
fi

exec "${command[@]}" \
  --flow .agents/flows/auto_bugfix.yml \
  --prompt "${*:-请检查 project 的 .gitignore，补齐必要规则并验证。}"
