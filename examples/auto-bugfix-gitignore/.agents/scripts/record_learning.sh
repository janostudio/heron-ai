#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

knowledge=".agents/knowledge/gitignore.md"
if [[ ! -f "$knowledge" ]]; then
  echo "KnowledgeWrite failed: $knowledge not found" >&2
  exit 1
fi

if grep -q "本次验证记录" "$knowledge"; then
  echo "KnowledgeWrite"
  echo "status=already_recorded"
  echo "path=$knowledge"
  exit 0
fi

cat >> "$knowledge" <<'EOF'

## 本次验证记录

- 触发：项目存在本地环境文件、缓存、构建产物或 IDE 私有文件未被忽略。
- 最小规则：优先按具体目录或文件类型补充规则，保留源码、README、依赖声明和 `.gitignore`。
- 验证：使用 `git -C project check-ignore -v` 检查目标文件，使用 `git -C project status --short --ignored` 检查范围。
EOF

echo "KnowledgeWrite"
echo "status=updated"
echo "path=$knowledge"
