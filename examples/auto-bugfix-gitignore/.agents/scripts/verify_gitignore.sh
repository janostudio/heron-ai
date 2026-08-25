#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

ignored=(
  ".env"
  ".env.local"
  ".pytest_cache/cache.db"
  "__pycache__/app.cpython-312.pyc"
  "dist/app.js"
  ".idea/workspace.xml"
)

kept=(
  "README.md"
  "app.py"
  "requirements.txt"
  ".gitignore"
)

failed=0
echo "VerificationReport"
echo "--- expected ignored ---"
for path in "${ignored[@]}"; do
  if output="$(git -C project check-ignore -v "$path" 2>&1)"; then
    echo "PASS ignored project/$path :: $output"
  else
    echo "FAIL not_ignored project/$path"
    failed=1
  fi
done

echo "--- expected tracked/visible ---"
for path in "${kept[@]}"; do
  if git -C project check-ignore -q "$path"; then
    echo "FAIL unexpectedly_ignored project/$path"
    failed=1
  else
    echo "PASS visible project/$path"
  fi
done

if [[ "$failed" -ne 0 ]]; then
  echo "RESULT failed"
  # Keep the command turn successful so the failure is published as a
  # VerificationReport and TestDecision can route back to Fix Team.
  exit 0
fi

echo "RESULT passed"
