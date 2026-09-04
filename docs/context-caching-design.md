# 一个 Agent 对标 Claude Code 的上下文优化全景

> 核心前提：heron 下是一个个 agent，**一个 agent 就是对标 Claude Code 的一个运行单元**。
> 因此每个 agent 应具备 Claude Code 同级的上下文管理能力：分层压缩、结构化摘要、缓存意识。
> 本文把之前讨论的"三个点"（默认 LLM、LLM 学 CC、拼接学 CC）与缓存机制统一成一份
> **可执行优化清单**，每项标注收益，让读者明确"做什么事情有优化"。

## 0. 一个 Agent 的完整上下文闭环

```
TurnLoop（一个 agent，绑定 AgentConfig.Model.Provider）
  ├── ContextManager  —— 分层压缩（masking + 摘要）
  ├── Compactor       —— 摘要生成（默认 LLM / 可选机械）
  ├── PromptRenderer  —— 拼接（system + context blocks + user）
  └── ProviderRouter  —— 路由到 AnthropicProvider / OpenAIProvider
```

现状：`TurnLoop` 的 `t.model` 是 `ProviderRouter`（不是具体 provider），
`ContextManager` 构造时只收 `ContextConfig`，**不感知 provider 缓存机制**。
但 provider 类型信息在 `agent.Model.Provider` 里是有的，只是没往下传。

## 1. 已完成的优化（三个点，已落地）

### 1.1 默认 LLM 摘要

- **改动**：`Compactor` 默认走 `llmCompactor`（9 段结构化摘要），`mechanical` 作 opt-out。
- **收益**：压缩摘要有语义理解，能提炼任务意图、技术决策、文件状态（对齐 Claude Code Layer 3）。
- **状态**：✅ 已实现（`internal/agent/runtime.go`）。

### 1.2 LLM 摘要结构化 9 段

- **改动**：`llmCompactor` 的 prompt 强制输出 9 段（Primary Request / Key Concepts /
  Files / Errors / Problem Solving / **All user messages** / Pending / Current Work / Next Step）。
- **收益**：摘要结构化，"All user messages" 段保留所有用户消息原文，防目标漂移（对齐 Claude Code）。
- **状态**：✅ 已实现（`internal/agent/compactor_llm.go`）。

### 1.3 拼接学 Claude Code

- **改动**：摘要注入加固定前缀（`compactedContextPreamble`），提示模型"这是跨上下文重置的续聊"。
- **收益**：模型正确理解摘要语义。
- **状态**：✅ 已实现（`internal/agent/context.go`）。

### 1.4 masking 分层（第 1/2 层）

- **改动**：tool 截断 + microcompact（头尾截断）每轮必做，零 LLM 成本。
- **收益**：对齐 Claude Code Layer 1（observation masking），JetBrains 数据显示 +2.6% solve rate。
- **状态**：✅ 已实现（`internal/agent/context.go`）。

## 2. 待做的优化（缓存意识，未落地）

以下是对标 Claude Code 但 heron **尚未实现**的部分，集中在缓存。

### 2.1 缓存命中统计（已有，但未利用）

- **现状**：`TokenUsage` 已统计 `CacheReadInputTokens` / `CacheCreationInputTokens`，但**没人消费**。
- **优化**：把缓存命中率暴露到 `logging`（之前做的执行日志），用于监控。

### 2.2 ContextManager 感知 provider（关键缺口）

- **现状**：`ContextManager` 不感知 provider，压缩方向固定"丢头保尾"。
- **优化**：抽象 `CacheProfile`，让 ContextManager 知道 provider 缓存特性。

```go
type CacheProfile struct {
    SupportsExplicitBreakpoints bool   // Anthropic cache_control
    AutoPrefixCaching           bool   // OpenAI 前缀自动缓存
    PrefixCacheDiscount         float64 // 缓存读折扣（Anthropic 0.1，OpenAI 无）
}
```

### 2.3 压缩方向按缓存特性（tail trim）

- **现状**：`compactLocked` 从尾部往前选，保留尾部最近的组（`for i := len(groups)-1; ...`）。
- **优化**：Anthropic 下改为 tail trim（保留开头稳定前缀，丢弃尾部），命中缓存省 90%。
- **收益**：Anthropic agent 长会话下，输入成本大幅下降（Claude Code 的核心经济优化）。

| Provider | 压缩方向 | 理由 |
|---|---|---|
| Anthropic | tail trim 保前缀 | 显式断点 + 0.1 折缓存读，省 90% |
| OpenAI | 保尾部（现状） | 自动前缀缓存，方向影响小 |
| 其他 | 保尾部（现状） | 最近消息最相关 |

### 2.4 显式 cache breakpoint（Anthropic 专属）

- **现状**：`anthropic.go` 没有 `cache_control` 断点标记。
- **优化**：对稳定前缀（system prompt、tool 定义）设置 `cache_control: {type: "ephemeral"}`。
- **收益**：让缓存精确命中稳定前缀，而非依赖自动前缀匹配。

### 2.5 缓存命中率监控

- **现状**：无缓存监控。
- **优化**：缓存命中率 < 70% 告警（对齐业内 canary metric）。

## 3. 收益优先级排序

| 优先级 | 优化 | 收益 | 复杂度 |
|---|---|---|---|
| **P0** | 2.3 tail trim（Anthropic 保前缀） | 省 90% 输入成本 | 中 |
| **P0** | 2.2 ContextManager 感知 provider | 是 2.3 的前提 | 中 |
| **P1** | 2.4 cache breakpoint | 精确控制缓存 | 中（Anthropic 专属） |
| **P1** | 2.1 缓存命中率入日志 | 可观测 | 低 |
| **P2** | 2.5 缓存告警 | 运维 | 低 |

## 4. 关键权衡

### tail trim（保前缀）vs 保尾部（最近优先）

| 维度 | tail trim 保前缀 | 保尾部最近 |
|---|---|---|
| 缓存收益 | 省 90%（Anthropic） | 无 |
| 语义 | 丢最近消息，需摘要补偿 | 最近消息最相关 |
| 对标 | Claude Code 的做法 | heron 现状 / 通用做法 |

**结论**：Anthropic agent 应采用 tail trim（对标 Claude Code），代价是最近消息可能被摘要，
需 Layer 3 摘要把最近的也总结进去。OpenAI/其他 agent 保持保尾部。

## 5. 实施建议

1. **先做 P0（2.2 + 2.3）**：抽象 CacheProfile + Anthropic tail trim，这是缓存优化的核心。
2. **再做 P1（2.1 + 2.4）**：缓存命中率入日志 + Anthropic cache breakpoint。
3. **P2（2.5）**：告警，运维层面。

## 6. 与 context-management.md 的关系

- `context-management.md`：压缩本身（masking + 摘要分层，第 1 节已落地部分）
- 本文：压缩的**缓存意识**（第 2 节待做部分）+ 三个点的全景整合
