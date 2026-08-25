#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

echo "Heron auto-bugfix fixture health check"
python3 .agents/scripts/validate_config.py
bash .agents/scripts/preflight.sh
test -d project/.git
test -f .agents/knowledge/gitignore.md
test -f .agents/scripts/analyze_session.py
echo "health: passed"
