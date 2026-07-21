# Future Ideas - Heron AI

> 本文档记录未来要实现的新想法，持续补充，批量处理。

---

## Idea 1: 自由 Agent/Team 聊天 + HITL 协调

### 问题
用户只能通过预定义 Flow 交互，无法随时和某个 Agent/Team 临时聊天。HITL 审批也是 per-instance，无法跨 Flow 协调。

### 需求
- 用户可以 `heron chat --agent researcher` 直接和指定 Agent 对话
- 用户可以 `heron chat --team research_team` 直接和指定 Team 对话
- 运行中的 Flow 可以被中断，用户和 Agent 聊完后恢复
- HITL 审批集中管理，用户可以看到所有待审批请求

### 架构影响

| 组件 | 改动 |
|------|------|
| FlowEngine | 加 `Interrupt()` / `Status()`，ctx 取消传播 |
| SignalRouter | 加 `SignalInterrupted` |
| HITLGate | 改为单例，加 `ListPending()` / `BatchApprove()` |
| HTTP Handler | 从 stub 改为真正对接，加 6 个新端点 |
| 新包 `session/` | SessionManager 管理运行中 flow + 临时聊天 |

### 关键接口

```go
// FlowEngine 新增
func (e *FlowEngine) Interrupt() error
func (e *FlowEngine) Status() FlowStatus

// HITLManager（单例）
type HITLManager interface {
    RequestApproval(ctx context.Context, req HITLRequest) (HITLResponse, error)
    SubmitResponse(resp HITLResponse) error
    ListPending() []HITLRequest
    GetRequest(id string) (*HITLRequest, error)
    BatchApprove(ids []string) error
}

// SessionManager（新包）
type SessionManager interface {
    StartFlow(flow FlowConfig, input string) (runID string, err error)
    InterruptFlow(runID string) error
    ResumeFlow(runID string, input string) error
    ChatWithAgent(agentName string, message string) (response string, err error)
    ChatWithTeam(teamName string, message string) (response string, err error)
    ListActiveRuns() []RunState
    GetHITLManager() *HITLManager
}
```

### 优先级
**最高 — 阻塞所有后续功能**

---

## Idea 2: 模型决定并发 + Agent 复制

### 问题
当前 Team 并发是静态的（配置写死几个 Agent 并行），LLM 无法根据任务复杂度自己决定要不要并发。比如 Agent 发现任务可以拆成 3 个子主题，应该能自己 spawn 3 个副本并行处理。

### 需求
- LLM 输出 `<spawn>` 指令触发复制
- Team 配置加 `model_parallel` 模式
- 并发数有上限，防止失控
- 复制结果用 LLM 合成

### 架构影响

| 组件 | 改动 |
|------|------|
| TurnLoop | 解析 `<spawn>` 指令，生成 SpawnRequest |
| TeamScheduler | 加 `model_parallel` 模式，动态创建 agent 实例 |
| SignalParser | 加 `SignalSpawn` + 结构化解析 |
| ConsolidationAgent | LLM 合成复制结果 |
| ConcurrencyGuard | 全局并发限制 |

### Spawn 指令格式

```
<spawn agent="researcher" count="3">
<subtopic>Topic A: ...</subtopic>
<subtopic>Topic B: ...</subtopic>
<subtopic>Topic C: ...</subtopic>
</spawn>
```

### 关键接口

```go
type SpawnRequest struct {
    AgentName  string
    Count      int
    Subtopics  []string
    ParentTask TaskConfig
}

func (s *TeamScheduler) ScheduleWithSpawn(
    ctx context.Context,
    stages []StageConfig,
    agents map[string]AgentConfig,
    input string,
    spawnCh <-chan SpawnRequest,
) ([]AgentResult, error)
```

### 对比：Team 并行 vs Agent 复制

| 维度 | Team 并行 | Agent 复制 |
|------|----------|-----------|
| 发起方 | 配置（静态） | LLM（动态） |
| 任务分配 | 预定义 TaskConfig | LLM 生成子主题 |
| Agent 身份 | 不同 Agent | 同一 Agent 的副本 |
| 结果合并 | 字符串拼接 | LLM 合成 |

### 优先级
**中 — 依赖 Idea 1 的 SessionManager**

---

## Idea 3: Agent 独立测试 + 进化

### 问题
当前 EvalEngine 只做 Flow 级别的信号准确率和错误率统计。无法对单个 Agent 做单元测试、版本对比、回归检测。

### 需求
- 每个 Agent 有独立测试用例（输入 → 期望输出/信号/工具调用）
- Agent 版本管理（v1 → v2 对比）
- A/B 测试（生产环境分流对比）
- 失败模式分析（哪些输入总是失败）

### 架构影响

| 组件 | 改动 |
|------|------|
| EvalEngine | 大幅扩展：prompt eval / behavioral eval / regression |
| 新包 `agent/testing/` | TestHarness + mock LLM |
| 新包 `agent/evolution/` | 版本对比 + A/B 测试 |
| AgentConfig | 加 `Version` / `ParentVersion` |

### 关键接口

```go
type AgentTestCase struct {
    Name           string
    Input          string
    ExpectedSignal types.Signal
    ExpectedTools  []string
    ForbiddenTools []string
    ExpectedOutput string  // regex
}

type AgentTestHarness struct {
    agentConfig types.AgentConfig
    mockLLM     types.ModelProvider
    testCases   []AgentTestCase
}

func (h *AgentTestHarness) RunAll() (*TestReport, error)

type EvolutionEngine struct {
    agentStore  AgentVersionStore
    evalEngine  *eval.EvalEngine
    testHarness *AgentTestHarness
}

func (e *EvolutionEngine) CompareVersions(v1, v2 string, inputs []string) (*VersionComparison, error)
func (e *EvolutionEngine) SuggestImprovements(agentName string) (*ImprovementReport, error)
```

### 优先级
**低 — 可与 Idea 2 并行，最独立**

---

## 交互关系矩阵

| | Idea 1 自由聊天 | Idea 2 模型并发 | Idea 3 Agent 测试 |
|---|---|---|---|
| **Idea 1** | — | Session 管理副本 ID；临时聊天要识别副本 | 测试用临时聊天验证 Agent 行为 |
| **Idea 2** | 临时聊天遵守并发限制 | — | 测试覆盖复制场景 |
| **Idea 3** | 临时聊天作为调试工具 | Agent 版本追踪复制实例 | — |

---

## 实施顺序

```
Week 1-3:  Idea 1 自由聊天 + HITL    ← 阻塞所有后续功能
Week 4-6:  Idea 2 模型并发            ← 最大差异化能力
Week 5-8:  Idea 3 Agent 测试（可并行） ← 质量基础设施
```

---

## Idea 4: 主 Agent 驱动模式（而非纯流程编排）

### 问题
当前 Heron 从 Flow 编排起步——用户预定义 Stage → Team → Agent 的固定管线。但模型越来越强（Claude Sonnet 4、GPT-4o、DeepSeek V4），纯编排模式过于僵化：

- 用户要写 YAML 定义每个 Stage 怎么走
- Agent 无法根据任务复杂度自己决定调用谁、怎么协作
- 真实场景中，"做什么"应该由模型决定，而不是配置文件写死

Claude Code、Codex CLI 的成功证明：**主 Agent 自主决策 + 按需调度子 Agent** 才是正确方向。

### 核心设计

```
用户输入 → 主 Agent（秘书/分发者）
              │
              ├── 自己处理（简单问题直接回答）
              ├── 调用子 Agent A（"我需要调研，交给 researcher"）
              ├── 调用子 Agent B（"需要写代码，交给 coder"）
              ├── 并发调用多个子 Agent（Idea 2 的 spawn 能力）
              └── 汇总子 Agent 结果，返回给用户
```

**主 Agent 的角色**：
- **秘书**：理解用户意图，决定自己处理还是分发
- **分发者**：把任务拆解，调度合适的子 Agent
- **汇总者**：收集子 Agent 结果，综合后返回
- **决策者**：根据子 Agent 的 signal 决定下一步

### 与现有 Flow 的关系

| 模式 | 触发方式 | 适用场景 |
|------|---------|---------|
| **主 Agent 模式**（新） | `heron` 不带 `--flow` | 日常对话、探索性任务、复杂决策 |
| **Flow 编排模式**（现有） | `heron --flow xxx.yml` | 固定流程、CI/CD、批处理 |

两种模式共存，不互斥。主 Agent 模式是默认入口，Flow 是高级选项。

### 分发目标：统一为 Team（不分发 Agent）

**决策：主 Agent 只 dispatch Team，不直接 dispatch Agent。**

理由：
1. **统一抽象**：主 Agent 不需要区分"这是个单 Agent 任务"还是"多 Agent 协作任务"，统一 dispatch Team 即可
2. **Team 可以只有一个 Agent**：单 Agent 任务就是一个只有 1 个 sequential task 的 Team，层级清晰
3. **Team 内部协作逻辑自包含**：parallel/sequential 由 Team 配置决定，主 Agent 不关心
4. **扩展性**：今天 1 个 Agent 的任务，明天可以加 Agent 变成协作任务，主 Agent 代码不用改

```json
// 所有 dispatch 都指向 Team，不指向 Agent
{"name": "dispatch", "arguments": {"team": "qa_team", "input": "什么是 AI Agent"}}
{"name": "dispatch", "arguments": {"team": "blog_team", "input": "写 AI 安全博客"}}
{"name": "dispatch", "arguments": {"team": "research_team", "input": "调研竞品"}}
```

Team 定义示例（单 Agent 的简单 Team）：
```yaml
name: qa_team
stages:
  - process: sequential
    tasks:
      - name: answer
        agent: assistant
        description: "{{.Input}}"
```

Team 定义示例（多 Agent 协作的复杂 Team）：
```yaml
name: blog_team
stages:
  - process: parallel
    tasks:
      - name: research
        agent: researcher
      - name: plan
        agent: planner
  - process: sequential
    tasks:
      - name: write
        agent: writer
```

主 Agent 看到的都是 Team，不关心内部几个 Agent。

### 架构影响

| 组件 | 改动 |
|------|------|
| 主 Agent | 新增 `master` agent 配置，有特殊的 `dispatch` 工具 |
| TurnLoop | 主 Agent 的 loop 需要支持"调用子 Agent"作为一种特殊 tool call |
| 子 Agent 调度 | 类似 Idea 2 的 spawn，但由主 Agent 主动发起 |
| 无 Flow 时的执行 | main.go 默认进入主 Agent 模式，不经过 FlowEngine |
| 上下文管理 | 主 Agent 持有全局上下文，子 Agent 拿到的是主 Agent 分发的片段 |

### 关键接口

```go
// 主 Agent 的 dispatch 工具
type DispatchTool struct {
    agents map[string]types.AgentConfig  // 可调度的子 Agent
}

// 主 Agent 调用子 Agent 时的参数
type DispatchParams struct {
    AgentName string  // 要调用的子 Agent 名
    Input     string  // 传给子 Agent 的任务描述
    Wait      bool    // true=同步等待结果，false=异步 fire-and-forget
}

// 主 Agent 的 TurnLoop 扩展
type MasterTurnLoop struct {
    agentRuntime AgentRuntime
    subAgents    map[string]types.AgentConfig
    results      map[string]*types.AgentResult  // 异步结果收集
}

func (l *MasterTurnLoop) Run(ctx context.Context, input string) (*AgentResult, error) {
    // 1. 主 Agent LLM 调用
    // 2. 如果返回 dispatch tool call → 启动子 Agent
    // 3. 收集子 Agent 结果，喂回主 Agent
    // 4. 主 Agent 决定：继续 dispatch / 返回最终答案
}
```

### 主 Agent 配置

```yaml
---
name: master
persona:
  role: "首席助手"
  goal: "理解用户意图，自主决策处理方式，必要时调度专家 Agent"
  backstory: "你是用户的首席助手，负责理解需求、分配任务、汇总结果"
model:
  model: ${LLM_MODEL:-deepseek-v4-pro}
  temperature: 0.5
  max_tokens: 4096
tools:
  builtin:
    - Read
    - Write
    - Grep
    - Glob
    - TodoWrite
    - TodoRead
  custom:
    - dispatch    # 特殊工具：调度子 Agent
loop:
  max_rounds: 20  # 主 Agent 需要更多轮次
  tool_mode: sequential
  timeout: 600s
handoffs:
  - researcher
  - coder
  - reviewer
  - writer
---

你是用户的首席助手。当用户提出问题时：

1. **简单问题**：自己直接回答（知识问答、简单计算、翻译等）
2. **需要调研**：dispatch 给 researcher，等待结果后汇总
3. **需要写代码**：dispatch 给 coder，审查后返回
4. **需要审查**：dispatch 给 reviewer，根据反馈修改
5. **复杂任务**：拆解成多个子任务，并发 dispatch 给多个子 Agent

决策原则：
- 能自己做的不要分发
- 分发时给出清晰的任务描述
- 汇总时保持连贯性，不要简单拼接
```

### dispatch 工具

主 Agent 通过 `dispatch` 工具调用子 Agent，就像调用普通工具一样：

```json
{
  "name": "dispatch",
  "arguments": {
    "agent": "researcher",
    "input": "调研 AI Agent 安全最佳实践",
    "wait": true
  }
}
```

主 Agent 可以在一轮内多次 dispatch（串行或并发）：

```json
// 并发 dispatch
[
  {"name": "dispatch", "arguments": {"agent": "researcher", "input": "调研安全", "wait": true}},
  {"name": "dispatch", "arguments": {"agent": "researcher", "input": "调研性能", "wait": true}}
]
```

### 与其他 Idea 的关系

| Idea | 关系 |
|------|------|
| Idea 1（自由聊天） | 主 Agent 模式本身就是自由聊天的核心——用户和主 Agent 对话 |
| Idea 2（模型并发） | 主 Agent 的 dispatch 天然支持并发——一次 dispatch 多个子 Agent |
| Idea 3（Agent 测试） | 主 Agent 模式下更容易测试——给定输入，验证主 Agent 的分发决策 |

### 优先级
**最高 — 应该是 Idea 1-3 的前置**

主 Agent 模式改变的是用户入口体验：从"写 YAML 编排"变成"直接对话"。这是和 Claude Code 对标的核心能力。建议调整实施顺序：

```
Week 1-2:  Idea 4 主 Agent 模式         ← 默认入口，最高优先
Week 3-4:  Idea 1 自由聊天 + HITL       ← 基于主 Agent 扩展
Week 5-6:  Idea 2 模型并发              ← 主 Agent 的 dispatch 并发
Week 7-8:  Idea 3 Agent 测试            ← 质量保障
```

---

## Idea 5: 主 Agent 分发 Team（已合并到 Idea 4）

Idea 4 讨论后确定：**主 Agent 统一 dispatch Team，不直接 dispatch Agent**。单 Agent 任务就是一个只含 1 个 task 的 Team。此 Idea 已合并到 Idea 4 的"分发目标"章节。

保留此条目作为决策记录。

---

## Idea 6: 并发多个 Team

### 问题
Idea 5 只能一次 dispatch 一个 Team。但现实中用户可能同时要处理多件事：

```
用户："帮我做三件事：1. 写博客 2. 审查代码 3. 做市场调研"
  → 主 Agent 拆解成 3 个独立任务
  → 并发 dispatch 3 个 Team：blog_team / code_review_team / research_team
  → 3 个 Team 同时执行
  → 全部完成后主 Agent 汇总
```

### 设计

主 Agent 在一轮内可以发起多个 dispatch（并发）：

```json
[
  {"name": "dispatch", "arguments": {"team": "blog_team", "input": "写 AI 安全博客", "wait": true}},
  {"name": "dispatch", "arguments": {"team": "code_review_team", "input": "审查 auth.go", "wait": true}},
  {"name": "dispatch", "arguments": {"team": "research_team", "input": "市场调研竞品", "wait": true}}
]
```

三个 Team 并发执行，主 Agent 等待全部完成后汇总。

### 并发层次

```
主 Agent
  ├── Team A (blog_team)        ─┐
  │   ├── Agent: researcher       │ Team 内部并发
  │   ├── Agent: planner          │
  │   └── Agent: writer          ─┘
  ├── Team B (code_review_team)  ─┐
  │   ├── Agent: security          │ Team 间并发
  │   └── Agent: performance      ─┘
  └── Team C (research_team)     ─┐
      └── Agent: analyst          ─┘
```

**三层并发**：
1. **Team 间并发**：主 Agent 同时 dispatch 多个 Team
2. **Team 内并发**：Team 的 parallel stage 内多个 Agent 同时执行
3. **Agent 复制并发**（Idea 2）：单个 Agent spawn 多个副本

### 并发控制

```go
type ConcurrencyConfig struct {
    MaxConcurrentTeams   int  // 主 Agent 同时调度的 Team 数上限
    MaxConcurrentAgents  int  // 全局 Agent 并发上限
    MaxSpawnPerAgent     int  // 单个 Agent 最多 spawn 副本数
}
```

| 层级 | 默认上限 | 超限行为 |
|------|---------|---------|
| Team 间 | 5 | 排队等待 |
| Team 内 parallel | 10 | 排队等待 |
| Agent 复制 | 3 | 拒绝并提示主 Agent |

### 与其他 Idea 的关系

| Idea | 关系 |
|------|------|
| Idea 2（模型并发） | Agent 复制是最细粒度的并发，Team 并发是最粗粒度 |
| Idea 4（主 Agent） | 主 Agent 是并发的发起者 |
| Idea 5（dispatch Team） | Team 并发是 dispatch Team 的扩展 |

### 优先级
**中 — 依赖 Idea 4 和 Idea 5**

---

## Idea 7: Team 入口 Agent + HITL 分层

### 问题
当前 Team 内所有 Agent 平等，没有明确入口。HITL 审批散落在各个 Agent，没有统一协调点。

### 设计

**每个 Team 有一个入口 Agent（Entry Agent）**，负责：
1. 接收主 Agent 的 dispatch 请求
2. 在 Team 内部分发任务
3. 收集 Team 内其他 Agent 的结果
4. 汇总后返回给主 Agent
5. HITL 请求统一由入口 Agent 处理

```
主 Agent
  └── dispatch → Team (入口 Agent: coordinator)
                    ├── coordinator 接收任务
                    ├── coordinator 分发给 worker_A
                    ├── coordinator 分发给 worker_B
                    ├── worker_A 需要审批 → 请求 coordinator
                    ├── coordinator 决定是否需要 HITL
                    │   ├── 需要 → 向主 Agent 请求 HITL
                    │   └── 不需要 → 直接批准/拒绝
                    └── coordinator 汇总结果返回主 Agent
```

### HITL 分层

```
用户
  ↓ 审批
主 Agent（全局 HITL 管理者）
  ↓ 授权
Team 入口 Agent（Team 级 HITL 把关）
  ↓ 执行
Worker Agent（发起审批请求）
```

| 层级 | 审批权限 | 示例 |
|------|---------|------|
| 用户 | 最终决策权 | "删除文件"必须用户批准 |
| 主 Agent | 全局策略 | 读文件自动批准，写文件需要用户 |
| Team 入口 Agent | Team 级策略 | 本 Team 内的搜索自动批准 |

### Team 配置变更

```yaml
name: blog_team
entry: coordinator  # 新增：指定入口 Agent

stages:
  - process: parallel
    tasks:
      - name: research
        agent: researcher
      - name: plan
        agent: planner
  - process: sequential
    tasks:
      - name: write
        agent: writer
```

如果不指定 `entry`，默认用 sequential 阶段的最后一个 Agent（即汇总者）作为入口。

### 与 Idea 4 的关系

主 Agent dispatch Team 时，实际对话的是 Team 的入口 Agent：

```
主 Agent → dispatch(team=blog_team, input="写博客")
  → Team 入口 Agent (coordinator) 接收
  → coordinator 内部调度 researcher/planner/writer
  → coordinator 返回汇总结果
主 Agent ← 收到结果
```

### 优先级
**高 — Team 架构的核心设计**

---

## Idea 8: Team 入口 Agent = Team 内的主 Agent

### 问题
Idea 7 提出 Team 有入口 Agent，但更深层的问题是：**Team 入口 Agent 的角色和主 Agent 是同构的**。

### 设计

主 Agent 和 Team 入口 Agent 的职责完全一样，只是作用域不同：

| 角色 | 作用域 | 职责 |
|------|--------|------|
| 主 Agent | 全局 | 理解用户意图 → dispatch Team → 汇总 → 返回用户 |
| Team 入口 Agent | Team 内 | 理解任务 → 分发 Worker → 汇总 → 返回主 Agent |

**递归结构**：

```
主 Agent（全局协调者）
  ├── dispatch → Team A
  │   └── 入口 Agent（Team A 协调者）
  │       ├── dispatch → Worker A1
  │       └── dispatch → Worker A2
  └── dispatch → Team B
      └── 入口 Agent（Team B 协调者）
          └── dispatch → Worker B1
```

主 Agent 和 Team 入口 Agent 用同一套代码，只是配置不同：
- 主 Agent：`loop.max_rounds: 20`，`handoffs: [所有 Team]`
- Team 入口 Agent：`loop.max_rounds: 10`，`handoffs: [Team 内 Worker]`

### 优势

1. **统一抽象**：不区分"主 Agent"和"协调者"，都是 Coordinator 角色
2. **可嵌套**：Team 内可以再嵌套 Team（如果需要）
3. **复用代码**：dispatch 工具、汇总逻辑、HITL 处理只写一套
4. **简化心智模型**：用户只理解一种角色——Coordinator

### 配置示例

主 Agent（全局 Coordinator）：
```yaml
name: master
persona:
  role: "首席助手"
model:
  model: deepseek-v4-pro
loop:
  max_rounds: 20
# dispatch 目标是 Team
```

Team 入口 Agent（Team 级 Coordinator）：
```yaml
name: blog_coordinator
persona:
  role: "博客写作协调者"
model:
  model: deepseek-v4-flash
loop:
  max_rounds: 10
# dispatch 目标是 Team 内 Worker
```

### 优先级
**高 — 统一架构的核心决策**

---

## Idea 9: 推理链路维护 + 跨 Agent 可见性

### 问题
当前并行 Agent 之间零通信。但真实场景中，Agent 需要看到"其他 Agent 做了什么"的部分信息：
- researcher 发现了关键数据，writer 需要知道
- security-reviewer 发现漏洞，performance-reviewer 的结论需要调整
- 主 Agent 需要知道所有 Team 的进展

### 设计

**推理链路（Reasoning Chain）**：一个全局可读的执行日志，记录每个 Agent 的关键决策和产出。

```go
type ReasoningChain struct {
    Entries []ChainEntry
}

type ChainEntry struct {
    RunID       string
    TeamName    string
    AgentName   string
    Round       int
    Timestamp   time.Time
    Type        string  // decision / finding / tool_call / signal
    Content     string  // 摘要（不是全文）
    Visibility  string  // all / team / agent / coordinator
}
```

### 可见性层级

```
公开（all）       → 所有 Agent 可读
Team 级（team）   → 同 Team 内可读
Agent 级（agent） → 只有自己可读
协调者（coordinator）→ 只有主 Agent 和 Team 入口 Agent 可读
```

### Agent 查询推理链

Agent 通过工具查询推理链：

```json
{"name": "query_chain", "arguments": {"scope": "team", "type": "finding"}}
```

返回当前 Team 内其他 Agent 的发现摘要：

```
[researcher] Finding: SQL 注入漏洞在 GetUser 函数 (round 1)
[planner] Decision: 建议优先修复安全漏洞 (round 2)
```

### 并行 Agent 的"间接通信"

```
researcher ──→ 写入推理链 ──┐
                            │ 共享区域（Team 级可见性）
planner ────→ 写入推理链 ──┤
                            │
writer ←──── 查询推理链 ←──┘
```

并行 Agent 仍然不直接通信，但通过推理链实现**异步信息共享**：
- researcher 写入发现
- writer 在开始工作前查询推理链，获取 researcher 的发现摘要
- 不影响并行执行（writer 查询的是已写入的内容，不等待 researcher 完成）

### 推理链存储

```
.agents/data/{runID}/
└── reasoning_chain.jsonl   # 全局推理链（追加写入）
```

每条记录一行 JSON，支持按 scope/type/agent 过滤查询。

### 与其他 Idea 的关系

| Idea | 关系 |
|------|------|
| Idea 6（并发 Team） | 并发 Team 之间通过推理链共享进展 |
| Idea 2（Agent 复制） | 复制的 Agent 共享同一个推理链 |
| Idea 4（主 Agent） | 主 Agent 查询推理链了解所有 Team 进展 |

### 优先级
**中 — 并发场景的关键基础设施**

---

## Idea 10: 主 Agent 是状态机，不是纯 LLM

### 问题
Idea 4 提出主 Agent 模式，但如果主 Agent 纯靠 LLM 决策，会有问题：
- LLM 可能忘记当前状态（上下文过长）
- LLM 可能做出不一致的决策（同样的输入不同决策）
- LLM 无法被测试和复现

### 设计

**主 Agent 是一个状态机 + LLM 的混合体**：

```
状态流转（确定性）          LLM 决策（非确定性）
─────────────────          ──────────────────
INIT → UNDERSTANDING       "用户想要什么？"
  ↓                          
UNDERSTANDING → PLANNING    "需要哪些 Team？"
  ↓                          
PLANNING → DISPATCHING      "先 dispatch blog_team"
  ↓                          
DISPATCHING → WAITING       等待 Team 完成
  ↓                          
WAITING → REVIEWING         "结果质量如何？"
  ↓                          
REVIEWING → DISPATCHING     "需要再 dispatch reviewer"
  ↓ 或
REVIEWING → DELIVERING      "可以交付了"
  ↓
DELIVERING → DONE
```

### 状态机定义

```go
type MasterState string

const (
    StateInit        MasterState = "init"
    StateUnderstand  MasterState = "understanding"
    StatePlan        MasterState = "planning"
    StateDispatch    MasterState = "dispatching"
    StateWait        MasterState = "waiting"
    StateReview      MasterState = "reviewing"
    StateDeliver     MasterState = "delivering"
    StateDone        MasterState = "done"
)

type MasterAgent struct {
    state     MasterState
    teams     map[string]types.TeamConfig
    chain     *ReasoningChain  // Idea 9
    results   map[string]*types.TeamResult
    llm       types.ModelProvider
}
```

### 每个状态的 LLM 职责

| 状态 | LLM 做什么 | 状态机做什么 |
|------|-----------|------------|
| understanding | 分析用户意图 | 等待 LLM 输出意图分类 |
| planning | 决定需要哪些 Team | 验证 Team 存在，检查并发限制 |
| dispatching | 生成 dispatch 参数 | 执行 dispatch，记录到推理链 |
| waiting | 无（等 Team 完成） | 监控 Team 进度，处理超时 |
| reviewing | 评估结果质量 | 记录评估结果到推理链 |
| delivering | 生成最终回复 | 保存运行数据 |

### 优势

1. **可测试**：状态流转是确定性的，可以单元测试
2. **可复现**：相同输入 + 相同状态 → 相同流转路径
3. **可观测**：当前状态明确，日志清晰
4. **可控制**：状态机可以强制约束（比如不能从 init 直接跳到 delivering）
5. **防幻觉**：LLM 只在指定状态做指定决策，不能越权

### 与纯 LLM Agent 的对比

| 维度 | 纯 LLM Agent | 状态机 + LLM |
|------|-------------|-------------|
| 决策一致性 | 低（受上下文影响） | 高（状态约束） |
| 可测试性 | 低 | 高（状态流转可测） |
| 可观测性 | 低（全在 LLM 脑子里） | 高（状态明确） |
| 灵活性 | 高 | 中（状态约束） |
| 成本 | 高（每个决策都调 LLM） | 低（状态流转不调 LLM） |

### 配置示例

```yaml
name: master
persona:
  role: "首席助手"
state_machine:
  initial: init
  states:
    - name: understanding
      llm_action: "analyze_intent"
      next: planning
    - name: planning
      llm_action: "select_teams"
      next: dispatching
    - name: dispatching
      action: "dispatch_teams"
      next: waiting
    - name: waiting
      action: "await_results"
      next: reviewing
    - name: reviewing
      llm_action: "evaluate_quality"
      next_on_pass: delivering
      next_on_fail: dispatching
    - name: delivering
      llm_action: "generate_response"
      next: done
```

### 优先级
**高 — 主 Agent 的正确实现方式**

---

## 待补充

> 后续新想法继续添加到这里
