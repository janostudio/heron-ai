---
name: code-fix
description: "以最小差异安全修改配置文件"
tools:
  - Read
  - Write
  - Glob
  - Grep
knowledge:
  - gitignore-basics
---
# 最小修复

- 修改前读取目标文件。
- 保留已有规则。
- 只增加被 VerificationReport 需要的规则。
- 不修改源码、Git 索引或其他配置。
- 修改后把每条新规则和理由写入 ChangeSet。
