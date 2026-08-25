#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

echo "GitStatusReport"
echo "--- project status ---"
git -C project status --short
echo
echo "--- project ignored status ---"
git -C project status --short --ignored
echo
echo "--- changed tracked files ---"
changed_count=0
while IFS= read -r path; do
  [[ -z "$path" ]] && continue
  changed_count=$((changed_count + 1))
  echo "$path"
  if [[ "$path" != ".gitignore" ]]; then
    echo "FAIL changed file outside project/.gitignore: $path"
    exit 1
  fi
done < <(git -C project diff --name-only)

if [[ "$changed_count" -eq 0 ]]; then
  echo "(none)"
fi

echo "RESULT passed"
