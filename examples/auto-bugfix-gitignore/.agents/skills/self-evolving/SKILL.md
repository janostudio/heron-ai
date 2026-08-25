---
name: self-evolving
description: "从本次 Flow 的证据和验证结果沉淀长期 Knowledge"
allowed-tools: Read,Write,Grep,Glob
---

# 本地知识进化

这是 auto_bugfix self-evolving 能力在 Heron 中的适配版本。
本示例不使用 Issue、fix-history、`.codebuddy` 或外部知识库。

## 输入

只消费：

- `DiagnosisReport`
- `ChangeSet`
- `VerificationReport`
- `ReviewReport`

## 规则

1. 只记录被命令验证过的事实；
2. 知识写入 `.agents/knowledge/*.md`；
3. 使用 `index.md` 作为知识入口时，正文保持短小；
4. 不保存完整 `session.jsonl`，不保存密钥；
5. 写入前可以运行：

   ```bash
   python3 .agents/scripts/check_pattern_dup.py --text "<候选知识>"
   ```

本示例的实际写入命令是：

```bash
bash .agents/scripts/record_learning.sh
```
