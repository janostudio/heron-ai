#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

query="${*:-gitignore}"
echo "KnowledgeSearch query=$query"
grep -inE "$query|gitignore|ignore|缓存|构建|环境" .agents/knowledge/*.md || true
