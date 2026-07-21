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

## 待补充

> 后续新想法继续添加到这里
