# Context（上下文）管理

> 本文梳理 heron-ai 中单个 Agent 的上下文（Context）组成、各级处理与拼接逻辑，
> 以及上下文压缩的锚定机制（anchor 层 vs Claude Code 式摘要）对比。

## 1. 概念定位

在 heron-ai 中，Context 特指**单个 Agent 在某一轮 AgentTurn 中发给模型的对话上下文**。
它与其他两个相邻概念严格区分：

| 概念 | 对象 | 生命周期 | 机制 |
|---|---|---|---|
| **Context**（本文） | 对话消息 `[]types.Message` | 单次 AgentTurn | 压缩（Compactor） |
| **State** | 会话内工作状态快照 | 会话内 | 机械 append，注入下一轮 |
| **Knowledge** | 跨会话长期知识 | 跨会话持久 | KnowledgeSummarizer 提炼 |

本文只讨论 Context。

## 2. Context 的组成

### 2.1 双层结构

`MessageContextManager`（`internal/agent/context.go`）维护两个并列的消息列表：

| 列表 | 字段 | 含义 |
|---|---|---|
| **canonical** | `[]types.Message` | 完整对话记录，**永不压缩**，用于 checkpoint 恢复与审计 |
| **active** | `[]types.Message` | 实际发给模型的消息，受压缩管理，token 预算内 |

每次 `AddMessage` 都同时追加到两个列表，但只有 `active` 会被压缩。

### 2.2 active 的分层（splitContextLayers）

`splitContextLayers` 把 `active` 拆成四层：

```
active 消息列表
├── system 层        # 开头连续的 system 消息（系统提示词，不压缩）
├── anchor 层        # 第一条非 system 消息（通常是最初的 user 输入，不压缩）
├── [已有压缩摘要]    # 之前压缩累积的摘要，以 "## Compacted Agent Context" 标记
└── groups 层        # 后续对话消息组
    ├── group 1      # 一个消息组 = assistant(tool_calls) + 紧跟着的所有 tool 消息
    ├── group 2
    └── ...
```

**关键**：一个 group 是「一次 assistant 回复（含 tool_calls）+ 紧跟着的所有 tool 结果」的原子单元。
这样「提问 → 工具调用 → 工具结果」作为一个整体被压缩，不会把 tool 结果和它的调用拆开。

### 2.3 ContextBlock（拼接单元）

在消息进入 `MessageContextManager` 之前，跨层信息（State、Knowledge、Records 等）以
`ContextBlock` 形式注入。`ContextBlock`（`pkg/types/execution.go`）字段：

| 字段 | 含义 |
|---|---|
| `Kind` | 块类型（`team_state` / `agent_state` / `entity_state` / `knowledge` / `records` 等） |
| `Text` | 文本内容 |
| `Parts` | 多媒体内容（图片等） |
| `Placement` | `system` 或 `user`，决定拼进哪条消息 |
| `Stability` | `stable` / `semi_stable` / `dynamic` |
| `Priority` | 优先级（影响拼接顺序） |
| `Compressible` | 是否可被压缩 |

## 3. 各级处理逻辑（四层压缩 + 一层拼接）

### 第 0 层：拼接（PromptRenderer）

`PromptRenderer.Render`（`internal/prompt/builtin.go`）把 `ContextBlock` 和系统/用户提示词拼成最终消息：

```
最终消息 = [ system 消息 ] + [ user 消息 ]
  system 消息 = BuildSystemPrompt(agent) + ContextBlocks(Placement=system)
  user 消息   = BuildUserPrompt(agent, req) + ContextBlocks(Placement=user 或空)
```

`BuildSystemPrompt` 的拼接顺序（固定策略）：
1. Persona（Role / Goal / Backstory）
2. Agent Body（额外指令）
3. ContextBlocks（Placement=system，如 knowledge、rules）
4. Tool usage 指令
5. State 管理指令（`state-management` 模板）
6. Execution 管理指令
7. Perspective isolation
8. Output format（如有 structured output）

### 第 1 层：tool 输出截断（即时，每轮触发）

`AddMessage` 时，任何 `role == "tool"` 的消息内容立即被 `truncateContextText`
截断到 `toolOutputLimitChars()`：

```
toolOutputLimitChars =
  MaxToolOutputChars（若 > 0）
  否则 MaxInputTokens × ToolOutputRatio × 4（默认 ToolOutputRatio 0.10）
  否则 64KB
```

**目的**：单个工具输出（grep 全文件、日志）可能非常大，立即截断，不占预算。

### 第 2 层：microcompact（微压缩，每轮触发）

`applyMicrocompactLocked`：对超出 `RecentMessageGroups`（默认 2）的**旧消息组**里的
tool 消息，若超过 `MicrocompactThresholdChars`（默认 8192 字符），用
`microcompactToolContent` 压缩成「头部 N 行 + 尾部 N 行 + 中间省略」形式，
限制到 `MicrocompactMaxChars`（默认 4096）。

**目的**：保留最近 2 组完整，更早的 tool 输出只保留头尾，去掉中间冗长内容。

### 第 3 层：compaction（压缩，超阈值触发）

`compactLocked` 触发条件：`estimateMessagesLocked() > CompactionThreshold`
（默认 0.80 × 输入容量）。

流程：
1. `splitContextLayers` 分层
2. 从**尾部往前**选消息组，保留最近的消息，超出 `target`（TargetRatio 0.70 × 输入容量）的组丢进 `dropped`
3. `compactor.Compact(ctx, dropped)` 把被丢弃的组压成摘要
4. 摘要合并进 `existingSummary`，以 `## Compacted Agent Context` marker 塞回上下文（作为一条 user 消息）
5. 多层兜底：trim tool 消息 → trim 摘要 → 实在不行删摘要

**强制压缩**：`CompactForce` 在 provider 报"请求太大"（本地估算没超阈值）时强制压缩一次。

### 第 4 层：hard limit（硬限制，报错）

`exceedsHardLimitLocked`：`HardLimitRatio`（默认 0.90）是硬上限。
若压缩后仍超硬限，返回 `ErrContextLimit` 错误（不静默丢弃，让上层决定）。

### ratio 计算规则

所有 ratio 都是 `MaxInputTokens` 的比例，并减去 `OutputReserveRatio`（默认 0.15）预留输出空间：

```
effectiveRatioLimit(ratio) = MaxInputTokens × min(ratio, 1 - OutputReserveRatio)
```

| 配置 | 默认值 | 含义 |
|---|---|---|
| `TargetRatio` | 0.70 | 压缩后目标大小 |
| `CompactionThreshold` | 0.80 | 触发压缩的阈值 |
| `HardLimitRatio` | 0.90 | 硬上限 |
| `OutputReserveRatio` | 0.15 | 预留输出空间 |

## 4. Compactor（压缩器）

`Compactor` 接口（`internal/agent/compactor.go`）：

```go
type Compactor interface {
    Compact(ctx context.Context, groups [][]types.Message) (string, error)
}
```

两种实现：

| 实现 | 触发条件 | 机制 |
|---|---|---|
| `llmCompactor` | **默认**（空值，或 `"llm"`/`"model"`） | 用 agent 自己的模型生成 9 段结构化摘要 |
| `mechanicalCompactor` | `Compactor: "mechanical"` | 机械拼接，无 LLM，`buildContextSummary` |

### 4.1 机械摘要 buildContextSummary

遍历 dropped groups 的每条消息：
- `assistant` 带 tool_calls → `"assistant requested tools: <names>"`
- `user` 消息 → `"user: <content>"`，**原文不截断**（防止目标漂移）
- 其他（assistant/tool）→ `"<role>: <content>"`，content 被 `truncateContextText(content, 500)` 截断到 500 字符

### 4.2 LLM 摘要（9 段结构化）

`llmCompactor` 的 prompt 强制输出 `<summary>` 包裹的 9 段固定结构：

| 序号 | 章节 |
|---|---|
| 1 | Primary Request and Intent |
| 2 | Key Technical Concepts |
| 3 | Files and Code Sections |
| 4 | Errors and fixes |
| 5 | Problem Solving |
| 6 | **All user messages**（所有非工具 user 消息原文） |
| 7 | Pending Tasks |
| 8 | Current Work |
| 9 | Optional Next Step |

### 4.3 摘要前缀

压缩摘要注入回上下文时，会加固定前缀（`compactedContextPreamble`）：

> This session is being continued from a previous conversation that ran out of
> context. The summary below covers the earlier portion of the conversation.

使模型理解这是跨上下文重置后的续聊。

## 5. 锚定机制对比：anchor 层 vs Claude Code 式摘要

### 5.1 方案 A：当前 anchor 层

**机制**：`splitContextLayers` 把第一条非 system 消息单独抽出，放 `prefix`，
**完全不参与压缩**（连 `compactor.Compact` 都看不到它）。

**数据流**：
```
active 消息
├── system（不压缩）
├── anchor = 第一条非 system（不压缩）  ← 唯一锚点
├── 摘要（压缩产物，用户消息被 500 字符截断 / LLM 自由提炼）
└── 最近 N 组（不压缩）
```

### 5.2 方案 B：Claude Code 式摘要锚定

**机制**：取消独立 anchor 层，第一条消息也参与压缩。但压缩摘要采用**结构化固定格式**，
强制包含所有用户消息原文。

Claude Code 的摘要（9 段固定结构）：

| 序号 | 章节 | 内容 |
|---|---|---|
| 1 | Primary Request and Intent | 任务目标 |
| 2 | Key Technical Concepts | 技术概念、框架、API |
| 3 | Files and Code Sections | 涉及文件、代码段、函数签名 |
| 4 | Errors and fixes | 错误与修复 |
| 5 | Problem Solving | 已解决/正在排查的问题 |
| 6 | **All user messages** | **所有非工具的用户消息原文** |
| 7 | Pending Tasks | 待办 |
| 8 | Current Work | 压缩前正在做什么 |
| 9 | Optional Next Step | 下一步 |

**数据流**：
```
active 消息
├── system（不压缩）
├── 结构化摘要（含所有用户消息原文 + 任务意图 + 技术状态）
└── 最近 N 组（不压缩）
```

Claude Code 还附带**冷路径**：摘要里引用完整 transcript 的文件路径，需要精确细节时可回溯。

### 5.3 收益对比

| 维度 | 方案 A（anchor 层） | 方案 B（CC 式摘要） | 收益归属 |
|---|---|---|---|
| 目标漂移防护 | 只保第一条，中途改口丢失 | 保留所有用户消息原文 | **B 显著优** |
| token 效率 | anchor 永不压缩，长首条占大额预算 | 摘要统一受控 | **B 优** |
| 信息完整性 | 用户消息被 500 字符截断 | 用户消息原文保留 | **B 优** |
| 可恢复工作状态 | 摘要偏对话流水账 | 结构化保留文件/错误/待办/进度 | **B 显著优** |
| 实现复杂度 | 已实现，简单 | 需扩展 Compactor + 摘要格式 + 冷路径 | **A 优** |
| 冷路径回溯 | canonical 内存态，会话结束即失 | 摘要引用 transcript 文件 | **B 优** |
| 依赖 LLM | 机械模式不依赖 LLM | 结构化质量依赖 LLM | 持平 |

### 5.4 关键差异总结

1. **最核心差异**：Claude Code 保留的是**所有用户消息原文**（防中途改口导致漂移），
   而 anchor 层只保留**第一条非 system 消息**，范围更窄。

2. **次核心差异**：Claude Code 摘要结构化保留了「文件状态、错误、待办、进度」，
   使 agent 压缩后能**真正恢复工作状态**；anchor 方案的摘要偏「对话记录」。

3. **实现代价**：方案 B 需要扩展 `Compactor.Compact` 返回结构（从 `string` 到结构化摘要）、
   重写 compactor prompt、落盘 transcript 冷路径。

### 5.5 已落地结论

最终落地策略是**分层架构 + 第 3 层默认 LLM**，吸收各方优点：

1. **第 1/2 层 masking 每轮必做（零 LLM 成本）**：tool 截断 + microcompact（头尾截断），
   这是 Claude Code Layer 1 的做法，与第 3 层模式无关。
2. **第 3 层 compaction 默认 LLM**：空值走 `llmCompactor`，用 9 段结构化摘要
   （吸收 Claude Code 的 9 段结构，尤其 "All user messages" 防目标漂移）。
3. **机械模式作 opt-out 降级**：显式 `Compactor: "mechanical"` 时用机械拼接，
   保留 user 消息原文（吸收 Codex「保留所有 user 消息」），零 LLM 成本。
4. **摘要加固定前缀**：吸收 Claude Code 的拼接风格。

**masking 与摘要的关系**：masking（1/2 层）和摘要（3 层）是**正交**的两件事。
masking 是"原地裁剪 tool 结果"，一直在跑；摘要是"超阈值折叠旧消息"，默认 LLM、
可选机械。无论第 3 层用哪种模式，masking 都在前面两层执行。

## 6. 相关文件索引

| 文件 | 职责 |
|---|---|
| `internal/agent/context.go` | MessageContextManager、分层、压缩、microcompact |
| `internal/agent/compactor.go` | Compactor 接口 + 机械实现 |
| `internal/agent/compactor_llm.go` | LLM 压缩实现 |
| `internal/prompt/builtin.go` | PromptRenderer、系统/用户提示词拼接、ContextBlock 渲染 |
| `pkg/types/agent.go` | ContextConfig 配置 |
| `pkg/types/execution.go` | ContextBlock 定义 |
