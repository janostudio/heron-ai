# heron-ai 提示词方案

> 本文是 heron-ai 提示词的**落地设计方案**。结合：
> - `BUILTIN-PROMPTS-ANALYSIS.zh-CN.md`（哪些提示词该内置）
> - 已实现的 LLM 摘要式压缩（`internal/agent/summarizer_llm.go`）
> - `docs/generic-engine/16-memory-knowledge-learning.md`（Knowledge 格式与自动学习约定）
>
> 范围：本次聚焦 **Knowledge 总结**（Knowledge Curator 提示词），去掉 session-title（非核心）。

## 0. 定位：提示词在引擎里的三个归属层

延续分析文档的框架，所有提示词分三层，本方案只落前两层：

| 层 | 载体 | 本次动作 |
|---|---|---|
| **引擎级强制策略** | `internal/prompt/builtin.go` | 激活 `knowledge-query`（教模型"如何用注入的知识"） |
| **能力级可选提示词** | 内置 Skill / 独立模块常量 | 新增 Knowledge Curator 提炼提示词 |
| **产品层政策** | 用户 `agent.body` | 不涉及 |

## 1. 问题：knowledge 总结缺什么

对照 16 号文档的「自动学习两阶段」，当前引擎的缺口：

```
阶段一：确定性候选收集（SharedRecord + Workspace diff + Memory Confirmed/Decisions）
  → 已有（候选来源在 Session 里）
阶段二：Knowledge Curator（候选 → 固定格式 Knowledge Markdown）
  → ❌ 缺：当前 KnowledgeExtractor.Extract 只是把 high/critical memory 的
     Content 原样搬成 entry，没有「提炼」，不产出 frontmatter 与固定 section
```

**核心缺口**：没有一条提示词驱动模型，把杂乱的候选来源**提炼**成 16 号文档 5.5 节约定的固定格式（frontmatter + Statement/Data/Basis/Usage/Notes）。

## 2. 两个提示词方案

### 2.1 引擎级：激活 `knowledge-query`（教模型如何用知识）

**触发机制（已澄清，非用户触发）**：知识检索是**引擎自动**的——TeamRuntime 分发 agent 调用前（`internal/runtime/team/runtime.go:372`），只要装配了 `KnowledgeInjector` 就自动用 `Responsibility + Input` 检索，`agent.Knowledge`（`[]string`）只是**作用域白名单**（过滤该 agent 可用哪些知识），不是触发开关。检索结果作为 `ContextBlock{Kind:"knowledge"}` 注入到 **user 上下文**（该块未设 `Placement:"system"`）。

**现状缺口**：知识内容已自动注入，但 `knowledgeQueryTemplate` 是死代码——模型拿到了知识，却没被告知「这是 guidance 不是 fact、当前事实优先、按 confidence 权衡」。

**方案（修正）**：不依赖 `agent.Knowledge` 字段判断（`BuildSystemPrompt` 拿不到"本次是否注入知识"的运行时信息，且知识块在 user 侧）。改为**让知识内容自带使用指令**：在 `KnowledgeInjector.formatEntries` 产出的知识文本头部，追加 `knowledge-query` 指令（或复用 `renderContextBlock` 的 knowledge 分支追加固定指令头）。这样指令与知识内容同生共灭——有知识注入才出现，无知识不出现。

**提示词文本**（增强版，替换死代码常量）：

```
## Knowledge Usage
When background knowledge is injected into your context:
- Treat injected knowledge as guidance, not fact. Current Workspace files and
  Session records always take precedence over knowledge.
- Weigh each entry by its confidence and basis. Lower-confidence or stale
  knowledge must not override what you can verify directly.
- Cross-reference multiple entries when they overlap; note when information is
  uncertain or conflicting.
- Use knowledge to avoid re-deriving known constraints, not to skip verification.
```

> 注意：当前注入格式是 `## Knowledge Context`（代码 `formatEntries`），与 16 号文档 6.3 的 `## Relevant Knowledge` 标题不一致。方案里保留现状标题 `## Knowledge Context`，避免本次改动牵扯注入格式；标题统一留待 Knowledge 注入重构时一并处理。

### 2.2 能力级：Knowledge Curator 提炼提示词

**对应 16 号文档 5.5 / 7.2**：把候选来源提炼成固定 frontmatter + section 的 Knowledge Markdown。

**归属**：这是「知识自动学习」的能力，不是所有 agent 的常规职责。应作为**独立模块的提示词常量**（类似 `summarizer_llm.go` 的摘要提示词），由 Knowledge Curator 调用，而非注入主 agent 系统提示。

**提示词文本**：

```
You are a knowledge curator. Your task is to distill candidate sources into a
single, precisely-formatted Knowledge entry.

## Input
You are given candidate sources: SharedRecords, Workspace diffs, test results,
and Confirmed/Decisions from Memory. Not every source is worth keeping.

## Selection rules
- Only distill durable, reusable knowledge (facts, rules, preferences,
  procedures, decisions, lessons).
- Drop raw natural-language chatter, private drafts, and unreleased tool output.
- If a candidate lacks a verifiable basis, discard it — never invent provenance.
- If a candidate conflicts with active knowledge, keep it as `proposed` and mark
  the conflict; do not silently rewrite history.

## Output format (STRICT)
Emit exactly one Markdown document with a YAML frontmatter and the following
sections. No preamble, no extra text outside the document.

---
schema_version: v1
kind: <fact|rule|preference|procedure|decision|lesson>
id: <stable-id>
scope: <flow|team|agent>
workspace_id: <workspace>
flow: <flow-id>
status: proposed
confidence: <high|medium|low>
keywords: [ ... ]
---

# <title — one sentence>

## Statement
<the distilled claim in 1-3 sentences>

## Data
<structured facts: identifiers, paths, values>

## Basis
- SharedRecord: <id>
- Workspace: <path:lines @ sha256>

## Usage
<when this knowledge applies>

## Notes
<precedence, supersedes, or open caveats>
```

**kind 枚举**（16 号文档 5.6）：`fact` / `rule` / `preference` / `procedure` / `decision` / `lesson`。
**status 枚举**（5.7）：默认 `proposed`，仅用户明确偏好可 `active`。

## 3. 与已实现「LLM 摘要压缩」的关系

| 维度 | 压缩摘要（已实现） | Knowledge Curator（本方案） |
|---|---|---|
| 目的 | 保住当前对话的续聊上下文 | 沉淀跨会话可复用知识 |
| 输入 | 被裁剪的消息组 | SharedRecord + Workspace diff + Memory |
| 输出 | `## Compacted Agent Context` 纯文本 | 固定 frontmatter + section 的 Markdown |
| 调用时机 | 压缩时同步 | 会话封存后异步 |
| 归属 | 引擎级（`summarizer_llm.go`） | 能力级（Knowledge Curator 模块） |

二者**共用**同一个技术模式：`ModelProvider.Chat()` 无 tools 轻量调用 + 严格输出约束 + 失败降级（压缩已实现降级回机械，Curator 应同样实现「提炼失败则丢弃候选或退回原始 memory，不产出脏知识」）。

## 4. 落地路径

### Phase 1：激活 knowledge-query（P0）
- 改 `internal/knowledge/injector.go` 的 `formatEntries`：在 `## Knowledge Context` 之后、知识条目之前，追加 `knowledge-query` 使用指令（指令常量可放 `internal/prompt/builtin.go` 导出，或 knowledge 包内定义；推荐后者保持 knowledge 自包含）。
- 替换 `internal/prompt/builtin.go` 的死代码 `knowledgeQueryTemplate` 常量文本为增强版（若指令放 knowledge 包，则同步删除或改由 knowledge 包引用）。
- 测试：新增断言「`InjectWithAllowlist` 命中知识时，输出含 `## Knowledge Usage` 指令 + 知识条目；未命中（空）时不含指令」。

### Phase 2：Knowledge Curator 提示词 + 调用骨架（P1）
- 新增 `internal/knowledge/curator.go`：`Curator` 结构体，持有 `types.ModelProvider` + `ModelConfig`，`Curate(ctx, sources) (types.KnowledgeEntry, error)`。
- 提示词常量放同文件（参考 `summarizer_llm.go` 的 `NewLLMSummarizer` 模式：覆盖 `MaxOutputTokens`/`Temperature`/`Reasoning=nil`）。
- 替换/增强现有 `KnowledgeExtractor.Extract`：不再机械搬运，改为调 `Curator.Curate` 提炼（或保留 `Extract` 作为无 LLM 的降级路径，Curator 作为 LLM 增强路径——与压缩的 mechanical/llm 双实现一致）。
- 失败降级：`Curate` 失败则丢弃候选（不产出脏知识），记录日志。

#### Curator 模型可配置（已确认）

- Curator 复用 `types.ModelProvider`（实际是 `ProviderRouter`）。`ProviderRouter.providerFor` 在 `config.Model == ""` 时自动回退到 `defaultModel`。
- 因此：**Curator 模型配置 = 给 `ModelConfig.Model` 赋值；不配置（空字符串）= 自动用默认模型**，引擎已天然支持，无需新增路由逻辑。
- 配置位置：`EngineConfig.Settings` 下新增 `Knowledge` 段：

```go
// pkg/types/config.go
type SettingsConfig struct {
    Logging       LoggingConfig       `json:"logging"`
    Observability ObservabilityConfig `json:"observability"`
    Paths         PathsConfig         `json:"paths"`
    Agent         AgentSettingsConfig `json:"agent"`
    Knowledge     KnowledgeConfig     `json:"knowledge,omitempty"`
}

type KnowledgeConfig struct {
    // CuratorModel 为 Curator 提炼指定的模型名；空 = 用默认模型。
    CuratorModel string `json:"curator_model,omitempty"`
}
```

- `NewCurator(provider types.ModelProvider, curatorModel string)`：`curatorModel` 为空时 `config.Model = ""`（走默认），否则 `config.Model = curatorModel`。

### Phase 3：接入触发时机（P2，依赖 16 号文档 7.3）
- 会话封存后异步调 Curator，产出 `proposed/` 候选。
- 此阶段依赖 Session 封存钩子与异步任务，不在本次提示词方案范围内，仅标注。

## 5. 明确不做

1. 不做 session-title（用户明确去除非核心）。
2. 不统一 `## Knowledge Context` → `## Relevant Knowledge` 标题（牵扯注入格式重构，另立任务）。
3. 不做向量检索/rerank（16 号文档 6.2 的规模扩展项）。
4. 不做 Curator 的发布门槛自动化（7.4 的人工确认策略，另立任务）。
5. 不把 Curator 提示词注入主 agent 系统提示（它是独立元任务，非 agent 常规职责）。

## 6. 已确认决策

1. **Curator 模型可配置**：`Settings.Knowledge.CuratorModel` 指定，空则用默认模型（`ProviderRouter` 原生支持，无需新路由）。
2. **Curator 与 KnowledgeExtractor 关系**：保留机械 `Extract` 作为降级、新增 `Curator` 作为 LLM 增强（对齐压缩的双实现模式）。—— 待最终确认。
3. **Phase 2 是否本次实现**：提示词方案文档已定稿，实现（Curator 代码）是否立即启动，待确认。
