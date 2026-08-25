#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

bash .agents/scripts/preflight.sh
bash .agents/scripts/verify_gitignore.sh > /tmp/heron-gitignore-verification.out
if grep -q '^RESULT passed$' /tmp/heron-gitignore-verification.out; then
  echo "fixture: already fixed"
else
  echo "fixture: initial failure is expected; this script only validates a fixed fixture"
fi
bash .agents/scripts/check_project_scope.sh
