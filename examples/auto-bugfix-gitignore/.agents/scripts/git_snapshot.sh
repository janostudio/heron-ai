#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

targets=(
  ".env"
  ".env.local"
  ".pytest_cache/cache.db"
  "__pycache__/app.cpython-312.pyc"
  "dist/app.js"
  ".idea/workspace.xml"
)

echo "GitSnapshot"
echo "workspace=$ROOT"
echo "gitignore=project/.gitignore"
echo
echo "--- project status ---"
git -C project status --short
echo
echo "--- ignored status ---"
git -C project status --short --ignored
echo
echo "--- check-ignore ---"
for path in "${targets[@]}"; do
  if output="$(git -C project check-ignore -v "$path" 2>&1)"; then
    echo "project/$path :: $output"
  else
    echo "NOT_IGNORED project/$path"
  fi
done
echo
echo "--- tracked files ---"
git -C project ls-files
