# 03 模块设计与分层架构

> 目录：`docs/generic-engine/`
> 本篇定位：**拆分模块**。从分层到每层模块的定位、职责、数据传递关系。
> 前置阅读：[01-ddd-strategic.md](./01-ddd-strategic.md) [02-entity-relations.md](./02-entity-relations.md) [00-config-design.md](./00-config-design.md)
> 调研参考：
> - [research-crewai-layering.md](./research-crewai-layering.md) — CrewAI 分层架构详解
> - [research-claudecode-architecture.md](./research-claudecode-architecture.md) — Claude Code 架构详解
> - [research-general-patterns.md](./research-general-patterns.md) — 通用 Agent 引擎分层模式

---

## 一、总分层图

引擎分为 **8 层**，从上到下依次是调用方向：

```
┌─────────────────────────────────────────────────────────┐
│  Layer 1: View / API                                    │
│  HTTP handlers, SSE streaming, CLI entry                │
│  → 输入验证 → 路由到 Orchestration                       │
├─────────────────────────────────────────────────────────┤
│  Layer 2: Orchestration                                 │
│  Flow 路由表, Team 调度, Signal 翻译                   │
│  → 纯代码，不调 LLM → 触发 Layer 3                     │
├─────────────────────────────────────────────────────────┤
│  Layer 3: Agent Runtime                                 │
│  Agent Turn (tool-use loop), prompt 渲染, Signal 解析   │
│  → 唯一调 LLM 的层 → 触发 Layer 4/5/6                  │
├──────────┬──────────┬──────────┬─────────────────────────┤
│  Layer 4 │ Layer 5  │ Layer 6  │  Layer 7: Context       │
│  Tool    │ Skill    │ Knowledge│  & Memory               │
│  工具执行 │  能力包   │  知识注入 │  Agent 私有状态 + Memory   │
├──────────┴──────────┴──────────┴─────────────────────────┤
│  工具注册/执行     │  Agent 私有状态读写, Memory 管理,     │
│  Schema 生成      │  Patch 增量更新, 视角隔离            │
│  MCP 接入         │                                      │
├──────────────────┴──────────────────────────────────────┤
│  Layer 7: Knowledge                                     │
│  KnowledgeEntry 触发注入, Scope 过滤, 上下文拼接         │
├─────────────────────────────────────────────────────────┤
│  Layer 8: Model / LLM                                   │
│  Provider 抽象, streaming, token 统计                   │
├─────────────────────────────────────────────────────────┤
│  Layer 9: Storage                                       │
│  文件读写, Run 状态持久化, Checkpoint                    │
├─────────────────────────────────────────────────────────┤
│  横切: Observability                                    │
│  Tracing, Logging, Metrics → 覆盖所有层                  │
└─────────────────────────────────────────────────────────┘
```

**数据流向**：`View → Orchestration → Agent Runtime → Tool/Context/Knowledge → Model → Storage`

---

## 二、各层模块详解

### Layer 1: View / API

**职责**：外部接口层。接收用户输入，返回引擎产出。不包含任何业务逻辑。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **TUI Handler** | 终端交互式界面（P0 优先）：`multirole run --flow xxx` | CLI args + 用户输入 | TUI 渲染 + SSE 事件 |
| **HTTP API Handler** | REST API 路由（P1 后续）：`POST /api/run` 启动，`GET /api/run/{id}` 查询，`POST /api/run/{id}/resume` 恢复，`POST /api/run/{id}/approve` 审批 | HTTP Request | HTTP Response (JSON + SSE) |
| **SSE Handler** | 流式输出：Agent Turn 的 streaming 结果通过 SSE 推送给前端/TUI | — | SSE Event Stream |
| **Input Validator** | 校验请求参数、Flow 引用完整性、Agent 必填字段 | API/CLI 参数 | 校验通过 / 错误信息 |
| **Config Loader** | 加载 .agents/ 配置（settings/models/mcp + Flow/Team/Agent 文件） | 入口参数 | 完整的 RunRequest（含 FlowDef + TeamDef[] + AgentDef[]）|
| **Streaming Output** | 将 Agent Turn 的流式输出转换为 SSE 事件流推送 | AgentResult stream | SSE events |

**Streaming Output 两种展示模式**：

| 模式 | 用户感知 | 前端行为 | 何时用 |
|------|---------|---------|--------|
| **单 Agent 模式（默认）** | 一个助手在回答 | 只展示内容，不区分 Agent | Flow 只有 1 个 Agent（问答模式）|
| **多 Agent 模式** | 多个角色在协作 | 展示每个 Agent 的名称 + 输出 | Flow 有多个 Agent（RP/协作模式）|

引擎输出统一带 `agent_id` 和 `agent_name`，由前端决定展示模式：

```
SSE event: { type: "agent_start",  agent_name: "研究员", agent_id: "..." }
SSE event: { type: "agent_output", agent_name: "研究员", content: "正在搜索..." }
SSE event: { type: "agent_end",    agent_name: "研究员" }
SSE event: { type: "round_end",    signal: "wait_input" }
```

**默认行为**：单 Agent 模式。当 Round 内有多个 Agent 时，自动切换为多 Agent 模式。前端也可通过 API 参数 `display_mode: "single" | "multi"` 显式指定。

**数据传递**：`API/CLI 参数 → Config Loader（加载 .agents/） → RunRequest → Orchestration`

---

### Layer 2: Orchestration

**职责**：编排层，纯代码状态机。不调 LLM。驱动 Run → Round 循环。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Flow Engine.*管理 Signal 路由表，Team 出 Signal 决定方向 | Flow 配置 + 用户输入 | 下一环节名 / 结束 |
| **Team Runner** | 执行一个 Team：按 Team 配方的 Process 编排 Agent 执行 | Team 配置 + input | TeamResult (内容 + Signal) |
| **Signal Router** | 读取 Round 产出的 Signal，决定 Run 的下一步 | Signal 枚举 | continue → 下一环节 / wait_input / goal_* → 结束 |
| **Team Scheduler.*并发调度 Team 内的 Agent 执行（parallel join / sequential chain）| Process 配置 + Agent 列表 | AgentResult[] |
| **Loop Guard** | 循环上限兜底：Flow 级（跨 Round）+ Turn 级（单 Agent tool-use）| 当前轮次 + 上限 | GuardContinue / GuardStop（详见 04S）|

**数据传递**：
```
Flow Engine
  ├── 当前环节 → Round Runner
  │     ├── Team 配置 → Round Scheduler
  │     │     └── 每个 Agent → Agent Runtime.Run()
  │     │           └── AgentResult → 下一个 Agent / sequential 阶段 Agent
  │     └── 收口 AgentResult → TeamResult (内容 + Signal)
  └── TeamResult.Signal → Signal Router → Flow Engine (下一环节 / 结束)
```

**不持有的东西**：Agent 内部状态、LLM 调用细节、记忆内容。

---

### Layer 3: Agent Runtime

**职责**：唯一调 LLM 的层。一个 Turn = 一次完整的 Agent 执行（prompt 渲染 → tool-use loop → 产出）。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Prompt Renderer** | 渲染 Agent 的 System Prompt + User Prompt（System 委托 04P 模板拼接）| Agent 配置 + 注入变量 | system + user messages |
| **Turn Loop** | tool-use loop：LLM.Chat → 有 tool_calls 则执行 → 追加结果 → 继续；无则最终答案 | messages + tools | AgentResult (raw + parsed) |
| **Signal Parser** | 从 Agent 最终输出中解析 Signal 标签 | AgentResult.raw | Signal 枚举 |
| **Structured Output** | 约束 LLM 输出为指定 JSON schema，校验失败触发重试 | raw text + schema | 结构化对象（详见 04R）|
| **Guardrail Checker** | 输入/输出校验护栏 | input / output text | 通过 / 拦截 |
| **Handoff Router** | Agent 间任务委派：将当前上下文转交给另一个 Agent | target_agent_id + context | 切换 Agent 继续执行 |
| **Human-in-Loop Gate** | 人工介入：暂停执行等待人工输入/审批 | pause_reason | 等待 → 人工输入 → 继续 |
| **Hooks 执行器** | 同步生命周期钩子（on_start/on_tool_start/on_handoff 等），可中断流程 | HookPayload | 通过 / 中断（详见 04Q）|

**数据传递**：
```
Agent Runtime.Run(agentConfig, input)
  │
  ├── Prompt Renderer
  │     agentConfig.persona → 渲染 role/goal/backstory
  │     agentConfig.rules → 注入约束文本
  │     agentConfig.tools → 生成 tool schemas
  │     input → user message
  │     → [system, user]
  │
  ├── Turn Loop (最多 loopMaxRounds 轮)
  │     ├── Guardrail Checker (input) → 通过
  │     ├── Model.Chat(messages, tools)
  │     │     ├── 有 tool_calls → Tool Executor → append → 继续
  │     │     └── 无 tool_calls → 最终答案 → break
  │     └── Guardrail Checker (output) → 通过
  │
  └── 产出
        ├── Structured Output (可选)
        └── Signal Parser → Signal
```

---

### Layer 4: Tool Execution

**职责**：工具注册、Schema 生成、执行调度。Agent Turn 调用工具的桥梁。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Tool Registry** | 注册所有可用工具（内置/MCP/自定义），提供名称索引 | Tool 定义列表 | Tool 实例 |
| **Schema Generator** | 将 Tool 的 params 转换为 LLM function calling schema | Tool.params | JSON Schema |
| **Tool Executor** | 执行单个工具调用，处理超时/错误/审批 | tool_name + args | ToolResult |
| **MCP Adapter** | 接入外部 MCP 服务，暴露为 Tool | MCPServer 配置 | Tool 列表 |
| **Approval Gate** | 工具调用前审批（`needs_approval`） | tool_name + args | 批准 / 拒绝 |

**数据传递**：
```
Turn Loop: LLM 返回 tool_calls
  │
  ├── Approval Gate (如果 needs_approval)
  │     ├── 批准 → 继续
  │     └── 拒绝 → 返回错误给 LLM
  │
  └── Tool Executor
        ├── Tool Registry.Lookup(tool_name)
        ├── Tool.Execute(args)
        └── ToolResult → 追加到 messages
```

**内置工具（最小集，对齐 Claude Code 命名，见 04D）**：
- `Read` — 读 Agent 私有文件（sessions/{team}-{agent}.md）
- `Edit` — 精确替换文件内容（必须先 Read）
- `Write` — 全量覆盖文件（必须先 Read）
- `Grep` — 搜索文件内容
- `Glob` — 按模式匹配文件行
- `SearchKnowledge` — 查 Knowledge（内部根据向量模式自动选择 FTS5 或向量检索）
- `AppendKnowledge` — 追加新知识（自动更新索引）
- `UpdateState` / `AppendMemory` — Agent 级 State/Memory
- `UpdateTeamState` / `AppendTeamMemory` — Team 级 State/Memory（见 04G）
- `TodoWrite` / `TodoRead` — 操作 `todos.md`

---

### Layer 5: Skill

**职责**：声明式能力包。不是原子工具，而是一组 Tool + prompt 片段 + Knowledge 引用的打包。Agent 按需发现和加载。

**与 Tool 的区别**：

| | Tool | Skill |
|---|------|-------|
| 粒度 | 一个动作 | 一项本领 |
| 给 LLM 的是 | function schema（可调用） | prompt 片段 + 工具列表（注入上下文） |
| 加载方式 | Agent Turn 中调 tool_call | Agent 声明式引用 `skills: [name]`，Turn 开始前注入 |
| 示例 | `Read` | "深度调查" = [Read, Grep] + 调查方法论 prompt |
| 文件 | 代码函数 | `skills/<name>/SKILL.md` |

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Skill Registry** | 注册所有可用 Skill，按名称索引 | Skill 定义列表 | Skill 实例 |
| **Skill Loader** | 从文件系统加载 Skill 目录（SKILL.md） | 目录路径 | Skill 定义 |
| **Skill Injector** | 将 Agent 声明的 Skill 注入到 prompt 上下文 | skill_names[] + agent_id | 增强的 system prompt |
| **Skill Discovery** | 渐进式发现：先展示 name/description，Agent 需要时才加载完整 body | skill_names[] | 完整 Skill 定义 |

**数据传递**：

```
Agent Turn: Prompt Renderer 构建上下文
  │
  ├── Agent 配置中有 skills: ["deep_research", "combat"]
  │
  ├── Skill Registry.Lookup("deep_research")
  │     └── 返回 Skill { tools, prompt, knowledge }
  │
  ├── Skill Injector
  │     ├── Skill.prompt → 注入 system prompt（告诉 LLM 何时/怎么用）
  │     ├── Skill.tools → 加入 tools 列表（扩展 Agent 能力）
  │     └── Skill.knowledge → 预加载相关 Knowledge
  │
  └── Skill Discovery（渐进式）
        ├── 初始：只注入 Skill.name + Skill.description
        └── Agent 需要时：注入完整 Skill.body
```

**Skill 文件格式（SKILL.md）**：

```markdown
---
name: deep_research
description: "深度调查：系统地搜索和分析信息"
tools:
  - Read
  - Grep
knowledge:
  - lore.md
---

# 深度调查方法论

当需要进行深度调查时，按以下步骤操作：
1. 先用 Read 了解当前已知信息
2. 用 Grep 搜索相关背景知识
3. 交叉验证多个来源的信息
4. 总结发现并标注置信度
```

---

### Layer 6: Context & Memory

**职责**：Agent 私有文件的存取 + Run 级全局对话。保证视角隔离。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Agent Memory** | Agent 私有文件读写（一个 .md 文件，Agent 自由组织），按 (team, agent) 组合键隔离 | (team, agent) | 文件内容（见 04G）|
| **Perspective Guard** | 视角隔离：读写必须带 (team, agent) 组合键，跨 Agent/Team 访问拒绝 | (team, agent) | 通过 / 拒绝 |
| **全局对话（run.jsonl）** | 管理 Run 级别的全局对话历史（messages），与 Agent 私有文件区分 | run_id | Message[]（见 04I）|
| **Context Compressor** | 上下文窗口管理：截断旧消息 + 摘要压缩，防止超出 token 限制 | messages + maxTokens | 压缩后的 messages（见 04J）|

**数据传递**：
```
Agent Turn: 调用 Read 工具
  │
  ├── Perspective Guard (校验 team_name + agent_name)
  ├── Agent Memory.Read(team, agent)
  └── 返回 .md 文件全量内容

Agent Turn: 调用 Edit 工具
  │
  ├── Perspective Guard (校验 team_name + agent_name)
  ├── Agent Memory.Edit(team, agent, old_str, new_str)
  │     ├── Read → 精确匹配 old_str → 替换 → Write 回
  │     └── old_str 不匹配 → 返回错误
  └── 返回 "已更新"
```

---

### Layer 7: Knowledge

**职责**：领域知识的关键词触发注入。Agent Turn 构建上下文时调用。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Knowledge Store** | 加载和索引 Knowledge 文件（.md） | 文件路径 | KnowledgeEntry[] |
| **Trigger Matcher** | 关键词匹配：input 中的词匹配 KnowledgeEntry.keys | input text | 匹配的 KnowledgeEntry[] |
| **Scope Filter** | 按可见范围过滤：all / owner_only / [agent_id...] | KnowledgeEntry[] + agent_id | 过滤后的条目 |
| **Context Injector** | 将匹配的 Knowledge 注入 Agent 的 user prompt | KnowledgeEntry[] + prompt | 增强的 user prompt |

**数据传递**：
```
Agent Turn: Prompt Renderer 构建上下文
  │
  ├── Trigger Matcher
  │     input 关键词 → 匹配 KnowledgeEntry.keys
  │
  ├── Scope Filter
  │     requesterAgentId → 过滤可见范围
  │
  └── Context Injector
        匹配的 content → 拼入 user prompt
```

---

### Layer 8: Model / LLM

**职责**：LLM Provider 抽象。统一 OpenAI / Anthropic / 本地模型的调用接口。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **Model Registry** | 管理已配置的 LLM Provider 列表 | models.json | Provider 实例 |
| **Provider Adapter** | 统一 Chat 接口：屏蔽 OpenAI/Anthropic 差异 | messages + tools + config | ChatResponse |
| **Stream Handler** | 处理流式响应（SSE 推送）| ChatResponse stream | SSE events |
| **Token Counter** | 统计 token 消耗 | messages + response | token usage |
| **Retry Handler** | 失败重试 + fallback | error + config | 重试 / 切换 provider |

**数据传递**：
```
Turn Loop: 需要调 LLM
  │
  ├── Model Registry.Get(agentConfig.model.provider)
  ├── Provider Adapter.Chat(messages, tools, config)
  │     ├── 成功 → ChatResponse (text + tool_calls + usage)
  │     └── 失败 ��� Retry Handler → 重试 / 切换 provider
  │
  └── Stream Handler (如果 stream=true)
        └── SSE events → View Layer
```

---

### Layer 9: Storage

**职责**：持久化。Run 状态、Agent 私有状态、全局对话的存取。

| 模块 | 职责 | 输入 | 输出 |
|------|------|------|------|
| **File Store** | 文件系统读写（JSON/YAML/Markdown） | 文件路径 | 文件内容 |
| **RunState Store** | Run 的状态持久化（轮次、Signal 历史、累积结果） | run_id | Run 状态 |
| **Chat Logger** | 聊天记录的 append-only 写入 | run_id + Message | — |
| **Checkpoint Manager** | 运行中断后的恢复点管理 | run_id | 最近的 checkpoint |

**数据传递**：
```
Run 开始时：RunState Store.Load(run_id) → 恢复状态
每个 Round 结束：RunState Store.Save(run_id, state)
每个 Turn 结束：Chat Logger.Append(run_id, message)
Checkpoint: Checkpoint Manager.Save(run_id, snapshot)
```

---

## 三、横切：Observability

**职责**：所有层的可观测性。日志、追踪、指标。

| 模块 | 职责 | 覆盖范围 |
|------|------|---------|
| **Logger** | 结构化日志输出（按层分级）| 全部 9 层 |
| **Tracer** | Span/Trace 生成：Run span → Round span → Turn span → Tool span → LLM span | 全部 9 层 |
| **Metrics** | 计数器：token 消耗、延迟、成功率 | Agent Runtime + Model |
| **Event Bus** | 发布-订阅事件总线：各层发布事件（AgentStarted/ToolCalled/RoundCompleted），其他模块订阅 | 全部 9 层 |
| **Prompt Templates** | 引擎级默认提示词模板，用户可覆盖（见 04P）| Agent Runtime |
| **Hooks 执行器** | 同步生命周期钩子，可中断流程（见 04Q）| Agent Runtime |

---

## 四、模块间数据传递总图

```
POST /api/run { flow, agents, teams, variables }
  │
  ▼
[View] Input Validator → 校验通过
  │
  ▼
[Orchestration] Flow Engine
  │ 加载 Flow 配置 → 环节序列
  │
  ├── Round 1
  │     │
  │     ▼
  │   [Orchestration] Round Runner
  │     │ Team 配置 + input
  │     │
  │     ├── [Orchestration] Round Scheduler (parallel)
  │     │     ├── Agent A
  │     │     │     │
  │     │     │     ▼
  │     │     │   [Agent Runtime] Turn Loop
  │     │     │     │ Prompt Renderer (→ [Knowledge] SearchKnowledge → 注入)
  │     │     │     │
  │     │     │     ├── Turn 1: [Model] LLM.Chat
  │     │     │     │     └── tool_call: Read
  │     │     │     │           └── [Tool] Executor → [Context] Agent Memory.Read
  │     │     │     │
  │     │     │     ├── Turn 2: [Model] LLM.Chat
  │     │     │     │     └── tool_call: Edit(old_str, new_str)
  │     │     │     │           └── [Tool] Executor → [Context] Agent Memory.Edit
  │     │     │     │
  │     │     │     └── Turn 3: [Model] LLM.Chat
  │     │     │           └── 最终答案 → AgentResult
  │     │     │
  │     │     └── Agent B (同上，并发)
  │     │
  │     └── [Orchestration] Round Scheduler (sequential)
  │           └── sequential 阶段 Agent
  │                 │ 读 Agent A/B 的 AgentResult
  │                 │ [Agent Runtime] Turn Loop (同上)
  │                 └── AgentResult → [Agent Runtime] Signal Parser → Signal
  │
  │   TeamResult { content, signal: continue }
  │     │
  │     ▼
  │   [Orchestration] Signal Router
  │     continue → Round 2
  │
  ├── Round 2 → ... → Signal: wait_input
  │     │
  │     ▼
  │   [Orchestration] Signal Router
  │     wait_input → 暂停，返回给 View
  │
  └── [View] SSE/JSON Response → 用户
        │
        └── [Storage] RunState Store.Save + Chat Logger.Append
```

---

## 五、Go 包结构映射

```
cmd/
└── server/              # CLI + HTTP 入口

internal/
├── view/                # Layer 1: TUI + HTTP + SSE
│   ├── tui.go           # TUI 终端界面（P0，见 042）
│   ├── handler.go       # HTTP API handlers（P1，见 042）
│   ├── sse.go           # SSE streaming + Streaming Output（见 043）
│   └── validator.go     # Input Validator

├── orchestration/       # Layer 2: 编排
│   ├── flow.go          # Flow Engine
│   ├── team.go          # Team Runner
│   ├── scheduler.go     # Team Scheduler (parallel/sequential)
│   ├── signal.go        # Signal Router
│   └── guard.go         # Loop Guard (见 04S)

├── agent/               # Layer 3: Agent Runtime
│   ├── runtime.go       # Turn Loop
│   ├── prompt.go        # Prompt Renderer (委托 04P BuildSystemPrompt)
│   ├── signal.go        # Signal Parser
│   ├── structured.go    # Structured Output (见 04R)
│   ├── guardrail.go     # Guardrail Checker
│   ├── handoff.go       # Handoff Router
│   ├── human_in_loop.go # Human-in-Loop Gate
│   └── hooks.go         # Hooks 执行器 (见 04Q)

├── tool/                # Layer 4: Tool Execution
│   ├── registry.go      # Tool Registry
│   ├── schema.go        # Schema Generator
│   ├── executor.go      # Tool Executor
│   ├── mcp.go           # MCP Adapter
│   └── approval.go      # Approval Gate

├── skill/               # Layer 5: Skill
│   ├── registry.go      # Skill Registry
│   ├── loader.go        # Skill Loader (SKILL.md 解析)
│   ├── injector.go      # Skill Injector (注入 prompt)
│   └── discovery.go     # Skill Discovery (渐进式加载)

├── context/             # Layer 6: Context & Memory
│   ├── agent_memory.go  # Agent Memory (见 04G)
│   ├── guard.go         # Perspective Guard
│   ├── history.go       # 全局对话（run.jsonl，见 04I）
│   └── compressor.go    # Context Compressor (见 04J)

├── knowledge/           # Layer 7: Knowledge + Memory Recall
│   ├── store.go         # Knowledge Store (Grep 搜索)
│   ├── vector.go        # MemoryIndex (向量索引，memory/sqlite-vec)
│   ├── embedder.go      # Embedder (local/openai，sqlite-vec 模式)
│   └── injector.go      # Context Injector

├── model/               # Layer 8: Model / LLM
│   ├── registry.go      # Model Registry
│   ├── openai.go        # OpenAI Adapter（统一接口）
│   ├── bridge.go        # Anthropic→OpenAI 桥接
│   ├── stream.go        # Stream Handler
│   ├── token.go         # Token Counter
│   └── retry.go         # Retry Handler

├── storage/             # Layer 9: Storage
│   ├── file.go          # File Store
│   ├── run_state.go     # RunState Store
│   ├── chat.go          # 全局对话（run.jsonl）
│   └── checkpoint.go    # Checkpoint Manager

├── prompt/              # 横切: Prompt Templates (见 04P)
│   ├── builtin.go       # 代码内置默认提示词模板
│   └── loader.go        # .agents/prompts/ 加载

└── observability/       # 横切
    ├── logger.go        # Logger
    ├── tracer.go        # Tracer
    ├── metrics.go       # Metrics
    └── event_bus.go     # Event Bus

pkg/
├── config/              # 配置加载（.agents/ 文件解析）
│   ├── loader.go        # 配置加载器
│   ├── agent.go         # Agent .md 解析
│   ├── team.go          # Team .yml 解析
│   ├── flow.go          # Flow .yml 解析
│   ├── knowledge.go     # Knowledge .md 解析
│   └── rule.go          # Rule .md 解析
│
└── types/               # 共享类型定义（domain 层）
    ├── agent.go          # Agent 类型
    ├── team.go           # Team / Task 类型
    ├── flow.go           # Flow 类型
    ├── signal.go         # Signal 枚举
    ├── context.go        # Agent 私有状态 / Memory / State 类型
    ├── knowledge.go      # KnowledgeEntry 类型
    ├── rule.go           # RuleItem 类型
    ├── tool.go           # Tool 接口
    └── model.go          # Model Provider 接口
```

---

## 六、模块原子性检查

| 模块 | 单一职责 | 可独立测试 | 依赖（接口） |
|------|---------|-----------|------------|
| Flow Engine | 管理环节序列和路由 | ✅ (mock Round Runner) | Round Runner, Signal Router |
| Team Runner.*执行一个 Team | ✅ (mock Agent Runtime) | Round Scheduler |
| Round Scheduler | 并发/串行调度 Agent | ✅ (mock Agent Runtime) | Agent Runtime |
| Loop Guard | 循环上限兜底 | ✅ (纯函数) | — |
| Turn Loop | tool-use loop | ✅ (mock Model + Tool) | Model, Tool Executor, Hooks, StructuredOutput |
| Prompt Renderer | 渲染 system+user prompt | ✅ (纯函数) | Prompt Templates (04P) |
| Signal Parser | 从文本解析 Signal | ✅ (纯函数) | — |
| Structured Output | 校验 LLM 输出符合 schema | ✅ (mock Model) | Model |
| Hooks 执行器 | 生命周期钩子执行 | ✅ (纯函数) | — |
| Tool Registry | 工具注册和查找 | ✅ (纯数据) | — |
| Tool Executor | 执行单个工具 | ✅ (mock tool) | Tool Registry |
| Agent Memory | 读写 Agent 私有文件 | ✅ (mock File Store) | File Store |
| Perspective Guard | 视角隔离校验 | ✅ (纯函数) | — |
| Knowledge Store | 加载和索引知识 | ✅ (mock File Store) | File Store |
| Trigger Matcher | 关键词匹配 | ✅ (纯函数) | — |
| Model Registry | Provider 管理 | ✅ (mock Provider) | Provider Adapter |
| Provider Adapter | LLM 调用 | ✅ (mock HTTP) | — |
| RunState Store | 状态持久化 | ✅ (mock File Store) | File Store |
| Logger | 日志输出 | ✅ (纯函数) | — |

---

## 七、实现顺序建议

| 阶段 | 模块 | 说明 |
|------|------|------|
| **Phase 1: 骨架** | types/ + config/ + storage/ | 类型定义 + 配置加载 + 文件存储 |
| **Phase 2: 核心** | model/ + agent/ + tool/ | LLM 调用 + Agent Turn + 工具执行 |
| **Phase 3: 编排** | orchestration/ + context/ | Flow 驱动 + Team 调度 + Agent 私有状态 |
| **Phase 4: 知识** | knowledge/ | 知识触发注入 |
| **Phase 5: 视图** | view/ | HTTP + SSE |
| **Phase 6: 可观测** | observability/ | 日志 + 追踪 |
