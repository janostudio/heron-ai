---
name: session-observation
description: "读取 Heron session.jsonl 和 evidence.jsonl，检查流程是否真实执行"
tools:
  - Read
  - Grep
  - Glob
knowledge:
  - gitignore-basics
---
# Session 观测

观测使用 Heron 自己的持久化数据：

- `session.jsonl`：完整 Flow、Team、Member 生命周期和工具执行；
- `evidence.jsonl`：跨 Team 可查询的 SharedRecord 小抄。

不要把 session.jsonl 当作 CodeBuddy task-context，也不要自行创建
`checkpoint.json` 或配置快照。先读 AuditSnapshot，再根据事件和记录给出
AuditReport。
