---
id: session-layout
title: Heron 会话文件布局
keys: [session, evidence, observation]
scope:
  type: agents
  agents: [audit-agent]
---

session.jsonl 是完整事件事实；evidence.jsonl 是跨 Team 查询的 SharedRecord。
memory.md 是短期记忆，不替代 session 或 evidence。
