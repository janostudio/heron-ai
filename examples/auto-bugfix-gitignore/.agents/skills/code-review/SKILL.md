---
name: code-review
description: "审查实际变更的正确性和边界"
tools:
  - Read
  - Glob
  - Grep
knowledge:
  - gitignore-basics
---
# 变更审查

只审查本次变更：

- 文件范围；
- 规则差异；
- 误忽略风险；
- 验证证据。

没有证据时标记为 needs_review，不替用户修改。
