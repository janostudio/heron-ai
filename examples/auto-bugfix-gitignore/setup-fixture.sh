#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
PROJECT="$ROOT/project"

# The fixture is a nested Git repository so git check-ignore uses the
# project's .gitignore rather than the parent heron-ai repository rules.
if [ ! -d "$PROJECT/.git" ]; then
  git -C "$PROJECT" init -q
  git -C "$PROJECT" config user.email "fixture@example.invalid"
  git -C "$PROJECT" config user.name "Heron Fixture"
fi

mkdir -p "$PROJECT/.pytest_cache" "$PROJECT/__pycache__" "$PROJECT/dist" "$PROJECT/.idea"
printf 'APP_ENV=local\nAPI_TOKEN=fixture-secret\n' > "$PROJECT/.env"
printf 'APP_ENV=local\n' > "$PROJECT/.env.local"
printf 'cache\n' > "$PROJECT/.pytest_cache/cache.db"
printf 'bytecode\n' > "$PROJECT/__pycache__/app.cpython-312.pyc"
printf 'bundle\n' > "$PROJECT/dist/app.js"
printf '<project/>' > "$PROJECT/.idea/workspace.xml"

git -C "$PROJECT" add .gitignore README.md app.py requirements.txt
git -C "$PROJECT" diff --cached --quiet || git -C "$PROJECT" commit -q -m "fixture: initial project"

echo "Fixture ready: $PROJECT"
git -C "$PROJECT" status --short --ignored
