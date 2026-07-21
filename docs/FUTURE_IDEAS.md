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

**三层并发（重新定义）**：

### 层级 1: Team 间并发（主 Agent 决策）

**场景**：用户有多个**不相关**的 topic，主 Agent 并发 dispatch 给不同的 Team。

```
用户："帮我做三件事：写博客、审查代码、做市场调研"
  → 主 Agent 识别 3 个独立 topic
  → 并发 dispatch：
      ├── blog_team（写博客）
      ├── code_review_team（审查代码）
      └── research_team（市场调研）
  → 3 个 Team 同时执行，互不关联
  → 全部完成后主 Agent 汇总交付
```

**关键**：topic 之间无关联，各自独立。主 Agent 只负责汇总，不需要中间协调。

### 层级 2: Team 内多实例并发（相同 Team，不同内容）

**场景**：同一个 Team 处理**相同 topic 但不同内容**的多个子任务。

```
用户："审查这 5 个文件的代码"
  → 主 Agent dispatch 给 code_review_team
  → Team 入口 Agent 收到 5 个文件
  → 入口 Agent 决定：5 个文件无关联，并发处理
  → 同一个 code_review_team 实例 × 5，并发执行
      ├── 实例1: 审查 auth.go
      ├── 实例2: 审查 model.go
      ├── 实例3: 审查 handler.go
      ├── 实例4: 审查 storage.go
      └── 实例5: 审查 config.go
  → 入口 Agent 汇总 5 份审查报告
  → 返回主 Agent
```

**关键**：用的是同一个 Team 配置，但内容不同。入口 Agent 负责拆解和汇总。

### 层级 3: Team 内 Agent 并发（入口 Agent 决策）

**场景**：Team 入口 Agent 根据任务特点，决定 Team **内部**的 Agent 怎么协作。

```
用户："写一篇 AI 安全博客"
  → 主 Agent dispatch 给 blog_team
  → blog_team 入口 Agent 收到任务
  → 入口 Agent 决策：
      "调研和写大纲可以并发，写作必须在后面"
      → Stage 1: parallel (researcher + planner 同时)
      → Stage 2: sequential (writer 等前面完成)
  → 入口 Agent 监控执行，汇总结果
  → 返回主 Agent
```

**关键**：入口 Agent 自己决定 Team 内的并发/串行策略，不写死在配置里。

### 三层对比

| 层级 | 决策者 | 并发对象 | 关联性 | 示例 |
|------|--------|---------|--------|------|
| Team 间 | 主 Agent | 不同 Team | topic 无关联 | 写博客 + 审查代码 + 调研 |
| Team 内多实例 | Team 入口 Agent | 同一 Team 的多个实例 | topic 相同，内容不同 | 审查 5 个不同文件 |
| Team 内 Agent | Team 入口 Agent | Team 内的 Worker Agent | 同一任务的不同阶段 | 调研 + 大纲 → 写作 |

### 并发控制

```go
type ConcurrencyConfig struct {
    MaxConcurrentTeams     int  // 主 Agent 同时调度的 Team 数（层级1）
    MaxTeamInstances       int  // 同一 Team 的并发实例数（层级2）
    MaxConcurrentAgents    int  // Team 内并发 Agent 数（层级3）
}
```

| 层级 | 默认上限 | 超限行为 | 决策者 |
|------|---------|---------|--------|
| Team 间 | 5 | 排队等待 | 主 Agent |
| Team 内多实例 | 5 | 入口 Agent 排队 | Team 入口 Agent |
| Team 内 Agent | 10 | 排队等待 | Team 入口 Agent |

### 配置示例

Team 配置声明并发能力，入口 Agent 动态决定怎么用：

```yaml
name: code_review_team
entry: review_coordinator
concurrency:
  max_instances: 5        # 最多 5 个并发实例（层级2）
  max_internal_parallel: 3 # Team 内最多 3 个 Agent 并发（层级3）

stages:
  - process: parallel      # 默认策略，入口 Agent 可以覆盖
    tasks:
      - name: security_review
        agent: security_reviewer
      - name: performance_review
        agent: performance_reviewer
  - process: sequential
    tasks:
      - name: aggregate
        agent: lead_reviewer
```

入口 Agent 可以根据实际任务**覆盖**默认策略——比如 5 个文件就启动 5 个实例，1 个文件就只启动 1 个实例。

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

---

## 架构决策（修正版）：分层调度是合理的，Team 自己的主 A 有内部调度权

> 本节修正上一版"主 Agent 独家决策"的过度简化。分层调度（Idea 7/8 的核心思想）是对的，重新明确分层边界。

### 修正

**每一层都有自己的"主 A"，但每个主 A 只管自己的作用域：**

```
全局主 A（对话入口，用户直接聊天的对象）
  ↓ 决定 dispatch 哪些 Team、并发几次
Team 自己的主 A（Team 内部的调度者，用户/HITL 也可以直接找它聊天）
  ↓ 决定 Team 内部的 Agent 怎么协作
Worker Agent（纯执行单元，处理具体任务）
```

**关键澄清**：
1. Team 自己的主 A（原 Idea 7/8 里叫"入口 Agent"）**确实有调度权**——它决定 Team 内部怎么分工、要不要并发、结果怎么汇总。这不是"纯执行单元"。
2. 每一层的主 A 都可以被**外部直接对话**（Idea 1 的自由聊天 + 本次讨论的 HITL）。用户可以跳过全局主 A，直接找某个 Team 的主 A 聊天、审批。
3. 这不是"取消分层"，而是**分层但职责边界清晰**：Team 主 A 的调度范围只在自己 Team 内部，不能跨 Team 调度别的 Team。

### HITL 分层的正确理解

之前说"HITL 统一由全局主 A 管理"是错的。正确的是：

```
用户 ←── 可以直接对话 ──→ 全局主 A
用户 ←── 可以直接对话 ──→ Team 的主 A（跳过全局主 A，直接审批/沟通）
Team 的主 A ←── 内部审批 ──→ Worker Agent
```

- **Worker Agent** 发起的 HITL 请求，先到自己 Team 的主 A
- Team 主 A 可以自己按策略批准，也可以**转发给用户或全局主 A**
- 用户可以**直接找 Team 的主 A** 聊天/审批，不需要总是经过全局主 A 中转

这才是合理的分层：**决策权分层下放，但每一层都保留可被外部直达的接口**。

### Team 主 A 的调度权范围

Team 主 A（原"入口 Agent"）可以决定的：
- ✅ 内部 Worker Agent 之间 parallel 还是 sequential（可以是配置默认值，Team 主 A 可以在运行时调整，比如识别到 5 个独立文件就转成 5 路并发）
- ✅ 内部 HITL 审批策略（比如"这个 Team 内部的 Grep 操作不需要用户批准"）
- ✅ 结果怎么汇总后返回给上层（全局主 A 或直接返回用户）

Team 主 A **不能**决定的：
- ❌ dispatch 别的 Team（跨 Team 调度是全局主 A 的职责）
- ❌ 覆盖全局主 A 的顶层策略（比如全局强制"删除操作必须用户批准"，Team 主 A 不能自己豁免）

### 修正后的三层并发决策归属

| 层级 | 决策者 | 说明 |
|------|--------|------|
| Team 间并发 | 全局主 A | 决定要 dispatch 哪些 Team，并发跑 |
| Team 内多实例并发 | 全局主 A 或 Team 主 A | 全局主 A 可以直接并发 dispatch 同一 Team 多次；也可以只 dispatch 一次，让 **Team 主 A 自己决定内部要不要拆成多个实例处理**（比如一次性把 5 个文件路径都传给 Team，Team 主 A 自己决定并发审查） |
| Team 内 Agent 并发 | Team 主 A | Team 主 A 动态决定内部 parallel/sequential，不是纯静态配置 |

**两种模式都支持，看场景选择**：
- 全局主 A 知道要拆几份 → 直接并发 dispatch N 次（Idea 6 原方案）
- 全局主 A 不关心怎么拆，只给 Team 一个大任务 → Team 主 A 自己拆分调度（Idea 7/8 原方案）

### 最终结论

Idea 7、Idea 8 的**核心思想是对的**：
- ✅ 每个 Team 有自己的主 A（决策者），不是纯执行单元
- ✅ 递归结构成立：全局主 A 和 Team 主 A 用同一套 Coordinator 代码，只是作用域不同
- ✅ HITL 分层：每一层都能处理自己范围内的审批，也能上报

需要补充的澄清：
- 🔧 每一层的主 A 都要支持被**外部直接对话**（不是只能通过上层中转）
- 🔧 决策权边界清晰：Team 主 A 管自己内部，不能跨 Team

之前"主 Agent 独家决策"的说法**已废弃**，以本节为准。

---

## Idea 11: 主 Agent 内置化——用户只需配置 Flow/Team/Agent

### 问题

Idea 4 提出主 Agent 模式，Idea 10 提出状态机，但一个关键问题没回答：**主 Agent 需要用户配置吗？**

如果主 Agent 是用户配置的，那和普通 Agent 没区别——用户还是得写 YAML。这没有解决"零配置开箱即用"的问题。

### 设计

**主 Agent 是引擎内置的运行时组件，用户不需要配置它。**

```
用户视角（只需配置这些）：
  Flow（可选，高级用法）
  Team（核心配置，用户定义"怎么协作"）
  Agent（核心配置，用户定义"谁能干活"）

引擎内部（自动创建，用户无感知）：
  全局主 Agent（内置调度器）
    ↓ 自动管理
  Team 自己的主 Agent（内置调度器，每个 Team 一个）
    ↓ 自动管理
  Worker Agent（用户配置的，处理具体任务）
```

**主 Agent 的职责**：
1. **接收用户输入**——对话入口
2. **理解意图**——调用 LLM 分析用户要什么
3. **调度 Team**——dispatch 到合适的 Team
4. **接收 Team 结果**——每个 Team 执行完毕，结果汇总到主 Agent
5. **ReAct 决策**——评估结果质量，决定：
   - 结果满意 → 直接交付给用户
   - 结果不满意 → 再 dispatch 一轮（换 Team、重试、补充）
   - 需要澄清 → 向用户提问
6. **交付**——把最终结果给用户

### 主 Agent 是固定逻辑，不是用户配置的 Agent

```go
// 引擎内置，不需要用户写 YAML
type BuiltinMasterAgent struct {
    state    MasterState          // 状态机（Idea 10）
    teams    map[string]TeamConfig // 用户配置的 Team
    llm      ModelProvider         // 使用默认模型
    chain    *ReasoningChain      // 推理链路（Idea 9）
    history  []Message             // 对话历史
}

// 固定的状态机，用户不需要配置
func (m *BuiltinMasterAgent) Run(ctx context.Context, input string) (string, error) {
    switch m.state {
    case StateUnderstand:
        // 调用 LLM 理解意图，匹配 Team
        intent := m.llm.Analyze(input, m.teams)
        m.planDispatch(intent)
    case StateDispatch:
        // 执行 dispatch
        results := m.dispatchTeams()
        m.state = StateReAct
    case StateReAct:
        // 评估结果，决定交付还是再调度
        if m.isSatisfied(results) {
            return m.deliver(results), nil
        }
        m.state = StateDispatch // 再调度
    }
}
```

### 用户配置的只有 Team 和 Agent

用户视角的配置结构不变：

```
.agents/
├── models.json         # 模型配置
├── settings.json       # 引擎设置
├── teams/              # 用户定义的 Team
│   ├── blog_team.yml
│   └── code_review_team.yml
└── agents/             # 用户定义的 Agent
    ├── researcher/
    ├── writer/
    └── reviewer/
```

**不需要** `flows/`（除非高级用户要用固定管线）和 `master_agent.md`（引擎内置）。

### Team 自己的主 Agent 同理内置

每个 Team 内部，引擎自动创建一个 Team 主 Agent：

```yaml
# 用户配置的 Team（不需要指定 entry）
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

引擎内部自动处理：
1. 为 blog_team 创建一个内置 Team 主 Agent
2. Team 主 Agent 接收全局主 Agent 的 dispatch
3. Team 主 Agent 按 stages 配置调度 Worker Agent
4. 调度逻辑是固定的（状态机），不依赖 LLM 动态决策
5. 结果汇总后返回全局主 Agent

### ReAct 循环：全局主 Agent 的核心逻辑

```
用户输入
  ↓
全局主 Agent 理解意图 → dispatch Team
  ↓
Team 执行完毕 → 结果返回全局主 Agent
  ↓
全局主 Agent ReAct 判断：
  ├── 满意 → 交付用户
  ├── 不满意 → 重新 dispatch（可能换 Team、补充信息）
  └── 需要澄清 → 问用户
```

**ReAct 不是无限循环**——有最大轮次限制（`max_react_rounds: 3`），防止死循环。

### 和现有设计的关系

| 现有组件 | 变化 |
|---------|------|
| Flow | 变为可选（高级用法），默认不需要 |
| Team | 不变，用户继续配置 |
| Agent | 不变，用户继续配置 |
| 主 Agent | **新组件，引擎内置，用户无感知** |
| Team 主 Agent | **新组件，引擎内置，每个 Team 自动创建一个** |

### 配置量对比

| 模式 | 用户需要配置 |
|------|-----------|
| 当前 | Flow + Team + Agent（3 层） |
| 新设计 | Team + Agent（2 层） |
| 零配置 | 无（引擎内置默认 Team + Agent） |

### 优先级
**最高 — 直接决定用户体验是"写配置"还是"直接对话"**

### 澄清：编排配置不变，变的是调度决策层

主 Agent 内置化**不是取消编排**，而是调度决策从 YAML 静态规则变成 Agent 动态判断：

| 层次 | 当前 | 未来 | 变化？ |
|------|------|------|--------|
| Agent 内部 Turn Loop | 不变 | 不变 | - |
| Team 内 Agent 编排（parallel/sequential） | YAML 配置 | YAML 配置 | **不变** |
| Flow 内 Team 编排（stages 顺序） | YAML 配置 | YAML 配置 | **不变** |
| 谁决定 dispatch 哪个 Team | `on_signal` 静态路由 | **主 A 动态决策** | 变化 |
| 谁决定并发几个 Team | 配置写死 | **主 A 动态决策** | 变化 |
| 谁决定要不要 ReAct | 不支持 | **主 A 动态决策** | 新增 |
| 谁决定执行中追加/取消 | 不支持 | **Team 主 A 动态决策** | 新增 |

核心变化：**编排配置还在，但调度决策从代码/YAML 静态规则变成了 Agent 动态判断**。

---

## 架构决策：执行中用户输入的路由

> 用户输入时，根据当前引擎状态自动路由到正确的目标。

### 决策

| 引擎状态 | 用户输入路由 | 行为 |
|---------|------------|------|
| **空闲** | 全局主 A | 理解意图 → dispatch Team |
| **执行中** | 当前 **Team 主 A**（不是 Worker Agent） | Team 主 A 决定怎么处理 |
| **Ctrl+C** | 全局主 A | 中断当前执行 → 回到主 A |

### 执行中输入为什么给 Team 主 A（而不是 Worker Agent）

Team 主 A 收到用户追加输入后，**自己决定**：
- 追加给当前正在执行的 Worker Agent（"加点案例" → 直接传给 writer）
- 取消当前 Worker，另外起一个（"不对，换一个角度写" → 停掉 writer，重新 dispatch 新任务）
- 并发再起一个 Worker（"同时查下竞品" → 保持 writer 运行，另外起一个 researcher）

用户看到 "writer 正在写作..."，输入 "加点例子"：
```
用户输入 "加点例子"
  → Team 主 A 收到
  → Team 主 A 判断：writer 正在写，追加指令即可
  → writer 收到 "加点例子"，继续写作
```

用户看到 "researcher 正在搜索..."，输入 "换个方向，查下竞品"：
```
用户输入 "换个方向，查下竞品"
  → Team 主 A 收到
  → Team 主 A 判断：和当前方向冲突，停掉 researcher
  → 重新 dispatch researcher 新任务
```

### 不需要前缀

```
空闲：  "帮我写博客"     → 全局主 A → dispatch blog_team
执行中："加个案例"       → Team 主 A → 追加指令给 writer
执行中："换个角度写"     → Team 主 A → 停掉 writer，重新 dispatch
Ctrl+C："算了，查天气"   → 全局主 A → 中断 → 重新决策
```

