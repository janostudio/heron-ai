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

## 待补充

> 后续新想法继续添加到这里## 待补充

> 后续新想法继续添加到这里
