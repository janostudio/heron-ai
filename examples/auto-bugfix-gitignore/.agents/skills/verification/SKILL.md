---
name: verification
description: "根据确定性命令输出验证规则是否生效"
tools:
  - Read
knowledge:
  - gitignore-basics
---
# 验证

验证必须以命令输出为准：

- 目标本地生成物都能被 `git check-ignore -v` 命中；
- 项目源码、README、依赖声明和 `.gitignore` 不应被忽略；
- 对失败项给出精确路径和修复建议。
