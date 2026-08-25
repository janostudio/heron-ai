---
name: code-review-skill
description: "审查当前 Workspace 的实际变更、边界和验证证据"
allowed-tools: Read,Grep,Glob
---

# 本地变更审查

这是 auto_bugfix code-review 能力在 Heron 本地 Flow 中的适配版本。
本示例不创建 PR，也不调用外部评论 API。

## 审查顺序

1. 实际修改文件是否只有 `project/.gitignore`；
2. 原有 `*.log` 是否保留；
3. 新规则是否只覆盖本地环境文件、缓存、构建产物和 IDE 文件；
4. `README.md`、`app.py`、`requirements.txt` 和 `.gitignore` 是否仍可提交；
5. `VerificationReport` 是否包含确定性命令证据。

输出 `ReviewReport`：

```json
{
  "verdict": "approved | needs_modification",
  "findings": [],
  "evidence": []
}
```
