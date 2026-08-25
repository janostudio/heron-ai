---
id: fix-boundary
title: 修复边界
keys: [fix, boundary, minimal-change, gitignore]
scope:
  type: agents
  agents: [code-fixer]
---

本业务只允许修改 project/.gitignore。
保留已有规则，新增规则必须能被 VerificationReport 中的失败路径解释。
