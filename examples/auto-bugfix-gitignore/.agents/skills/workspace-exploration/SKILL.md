---
name: workspace-exploration
description: "只读探索当前 Workspace 并整理路径事实"
tools:
  - Read
  - Glob
  - Grep
knowledge:
  - gitignore-basics
---
# Workspace 探索

先读取目标配置，再列出实际存在的文件。
不要用外层仓库的状态推断嵌套 project 的状态。
不要在探索阶段写入任何文件。
