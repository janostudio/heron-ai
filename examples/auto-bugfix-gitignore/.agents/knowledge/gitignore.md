---
id: gitignore-basics
title: .gitignore 基础规则
summary: 忽略本地密钥、缓存、构建产物和 IDE 文件，但不要忽略源码与项目文档。
keys: [gitignore, env, cache, dist, ide]
scope:
  type: all
status: active
---

常见需要忽略的内容包括：.env、.env.local、缓存目录、编译产物、IDE 私有配置。
修改前必须确认文件是否属于本地生成内容，不能仅凭文件名忽略整个源码目录。

## 本次验证记录

- 触发：项目存在本地环境文件、缓存、构建产物或 IDE 私有文件未被忽略。
- 最小规则：优先按具体目录或文件类型补充规则，保留源码、README、依赖声明和 `.gitignore`。
- 验证：使用 `git -C project check-ignore -v` 检查目标文件，使用 `git -C project status --short --ignored` 检查范围。
