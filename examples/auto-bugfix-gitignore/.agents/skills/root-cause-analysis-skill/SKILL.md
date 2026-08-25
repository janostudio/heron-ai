---
name: root-cause-analysis-skill
description: "四阶段根因分析：现象、假设、证据、根因"
allowed-tools: Read,Grep,Glob
---

# 四阶段根因分析

这是 auto_bugfix 根因分析能力在 Heron 本地 Flow 中的适配版本。
本示例不使用 Issue、DAG Scheduler、Worktree、`.codebuddy` 或外部仓库。

## 步骤

1. **现象定位**：描述真实文件和确定性命令输出。
2. **假设生成**：至少列出两个可能原因。
3. **假设验证**：逐项引用 `GitSnapshot`、`ExplorationReport` 和 Workspace 文件。
4. **根因确认**：输出最小修复范围、影响路径和验收标准。

## 输出要求

不要把“规则未命中”写成“文件已被跟踪”。
不要凭文件名猜测生成物；必须使用真实路径和命令结果。
信息不足时输出 `next.action=wait_input`，不要强行激活 Fix Team。
