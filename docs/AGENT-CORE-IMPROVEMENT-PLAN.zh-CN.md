# Agent 核心能力改进计划

> 评估日期：2026-08-28  
> 范围：只讨论 Agent 本身的运行时能力。  
> 不讨论 Skill 的具体能力，也不讨论 RunCommand、GitDiff、GitStatus、Build、Test 等工程工具实现。

## 1. 目标边界

当前 Agent 的核心结构是：

```text
Agent
  └── TurnLoop
        ├── Prompt
        ├── Model
        ├── Tool
        └── Agent State
```

Agent 本身应该负责：

- 管理一次 AgentTurn 的状态
- 管理 Model / Tool 循环
- 管理 Tool 白名单和参数校验
- 管理上下文窗口
- 管理执行预算
- 管理取消、暂停、恢复
- 管理异步 Tool Task 的等待和继续
- 管理 Agent 生命周期 Hook
- 提供 Read / Write Policy 和 Tool 执行门禁接口
- 记录 AgentTurn 的执行检查点

以下内容不属于本次 Agent 核心建设范围：

```text
RunCommand
GitDiff
GitStatus
Build
Test
浏览器具体实现
网页测试脚本
业务领域 Skill
```

这些属于 Tool 或 Skill 的具体能力。Agent 核心只需要提供稳定的调用接口和生命周期管理。

## 2. 当前状态判断

### 2.1 Tool Allowlist

当前 Agent 配置中的：

```yaml
tools:
  builtin: []
  custom: []
  mcp: []
```

本质上就是 Agent 的 Tool 白名单。

这个设计是正确的，不需要增加新的配置概念。

但当前已完成运行时强制校验：

```text
模型返回 Tool Call
  → 检查 Tool 是否在 Agent Allowlist
  → 检查 Tool 是否已注册
  → 检查参数
  → 执行
```

不能只因为 Tool 存在于全局 Registry，就允许当前 Agent 调用。

### 2.2 Write 和 Workspace

当前 Workspace 以 `cwd.workspace` 为边界，Agent 在边界内可以读写文件。

本计划不把 Write Approval 作为当前优先事项。当前重点是：

- 路径不能逃出 Workspace
- Tool 参数要合法
- Agent Tool 白名单要生效
- 文件操作要有审计记录
- 后续可以利用 revision 做冲突检测

### 2.3 HITL

HITL 已接入 Agent 主链路，不再是“只有配置没有执行”的状态：

```text
Tool Policy / Tool NeedsApproval
  → waiting_approval
  → AgentCheckpoint.PendingApproval
  → Team / Flow waiting_approval
  → POST /api/approvals
  → 重新执行 Tool Policy
  → 批准后执行 Tool，拒绝则 TurnFailed
```

当前已覆盖：

- Agent Tool Policy 的 `allow / deny / require_approval`
- 审批等待的 Durable Checkpoint
- Flow `ResumeApproval`
- HTTP `/api/approvals?session_id=<id>`
- 审批后重新校验 Policy，防止“旧审批绕过新规则”
- 审批拒绝不会执行 Tool

当前策略：

- Durable Approval 永不过期，审批请求保存在 Checkpoint / Session 中；
- 审批响应记录 `ApproverID`、`Approver`、`Reason`、`Channel`、`DecidedAt`；
- 多个审批不做批量接口，按 `PendingApprovals` 顺序逐个处理。

### 2.4 Hook

当前已经有：

```text
HookExecutor
HookOnStart
HookOnEnd
HookOnToolStart
HookOnToolEnd
HookOnError
```

当前已经有 Hook 注册和执行器抽象，也有对应的单元测试。
当前已经接入 `TurnLoop` 生命周期：

```text
on_start
  AgentTurn 开始后、模型调用前
on_tool_start
  Tool 白名单检查后、Tool 执行前
on_tool_end
  Tool 执行完成后
on_error
  Model、Tool、参数、Context 等错误
on_end
  AgentTurn 最终结束时
```

因此状态是：

```text
Hook API / Executor：已有
TurnLoop 生命周期接入：已完成
```

### 2.4.1 长程稳定性补强

Agent Loop 已补齐以下长程控制：

- `CompletionPolicy`：Tool、Tool 成功、Workspace Read/Write、Structured Output 作为完成证据；证据不足时继续循环，达到上限后失败
- `StuckDetection`：重复 Tool 签名、无进展轮次、重复模型文本可配置触发 `fail` 或 `waiting_input`
- `Reactive Recovery`：Provider 报告上下文超限时执行一次强制 Compact；瞬态网络/限流/网关错误按 `max_model_retries` 重试
- `Checkpoint LoopState`：恢复时保留 Tool 成功证据和 stuck 计数，避免重启后忘记已经完成的工作
- `Checkpoint Compatibility`：保存 Agent 配置、模型、Tool Schema/Allowlist、Prompt 版本、Context Policy 指纹；拒绝未来版本 Checkpoint

这些能力属于 Agent Runtime，不增加 Flow / Team 层级。

### 2.5 Guardrail

当前已经有 `GuardrailChecker`，支持：

- regex
- contains
- input check
- output check

它更像一个通用的输入/输出规则检查器，不应在当前阶段扩展成
“脚本化门禁平台”。

当前优先增强的是 Read / Write 的文件操作能力：

```text
ReadPolicy
WritePolicy
Tool Parameter Validation
Workspace Revision
```

通用 Guardrail 可以保留为后续扩展能力。

## 3. Write 和局部编辑是否需要拆成两个 Tool

不建议增加独立的 `ApplyPatch` Tool。

`Write` 和局部编辑都属于文件修改能力，使用相同的 Workspace、revision、审计和恢复边界。对 Agent 暴露一个文件编辑 Tool 即可，避免模型在 `Write` 和 `ApplyPatch` 之间重复选择。

当前 Write 更接近：

```text
Write(file, full_content)
```

局部编辑模式更接近：

```text
Write(file, mode=edit, old_text, new_text, base_revision)
```

建议保留现有 `Write` 名称，并增加可选模式：

```text
create   创建文件
replace  全量替换文件内容
edit     基于 old_text / new_text 做局部编辑
```

示例：

```json
{
  "file": "src/App.tsx",
  "mode": "edit",
  "old_text": "const port = 3000",
  "new_text": "const port = 5173",
  "base_revision": "sha256:..."
}
```

内部可以使用 patch 算法，但不需要把 `ApplyPatch` 作为新的 Tool 名称暴露给 Agent。当前的 `Write(file, content)` 形式继续兼容。

这样可以保留局部修改的优点：

- 不需要模型重新生成整个文件
- 减少误覆盖
- 产生更清晰的 diff
- 可以检查 base revision
- 修改冲突时要求 Agent 重新读取
- 更适合多轮代码修改

这仍然属于 Tool 的内部实现，不是 Agent 核心需要增加的独立概念。

Agent 核心只需要支持：

```text
Tool Call
→ 工具执行
→ 返回成功、冲突或失败
→ 模型继续处理
```

## 4. Tool 参数校验

这是当前 Agent 核心需要优先补齐的能力。

当前 Tool 有 `Parameters()` 和 Schema，但执行前没有统一、严格的参数校验层。

建议在 `TurnLoop` 和 `ToolExecutor` 之间增加：

```text
ToolRequestValidator
```

执行流程：

```text
模型 Tool Call
  → Tool Allowlist 检查
  → Tool 是否存在检查
  → 参数 Schema 校验
  → 参数归一化
  → Tool Execute
```

最小校验范围：

- 参数是否存在
- 参数类型是否正确
- required 字段是否缺失
- 字符串是否为空
- 数组元素类型
- enum 值
- 数字范围
- 不允许的额外字段

错误应该作为 Tool Result 返回给模型，例如：

```json
{
  "success": false,
  "error": "parameter \"file\" is required"
}
```

不要因为模型参数错误直接崩溃整个 AgentTurn。

## 5. Durable Async Task

### 5.1 当前状态

当前已有 Tool 并发执行：

```text
一次模型响应中多个只读 Tool Call
  → goroutine 并发
  → 等待全部结束
  → 继续模型循环
```

这属于：

```text
同步 Loop 内的并行执行
```

还不是：

```text
可持久化、可恢复的异步任务
```

### 5.2 为什么 Agent 需要它

Agent 可能调用长时间 Tool：

- 长时间测试
- 大型构建
- 浏览器任务
- 远程 API
- 大规模扫描
- 人工等待
- 外部系统回调

这些任务不应该一直阻塞当前 Agent goroutine。

### 5.3 建议模型

```go
type ToolTaskStatus string

const (
    TaskQueued     ToolTaskStatus = "queued"
    TaskRunning    ToolTaskStatus = "running"
    TaskWaiting    ToolTaskStatus = "waiting"
    TaskCompleted  ToolTaskStatus = "completed"
    TaskFailed     ToolTaskStatus = "failed"
    TaskCancelled  ToolTaskStatus = "cancelled"
)

type ToolTask struct {
    ID          string
    AgentTurnID string
    ToolCallID  string
    ToolName    string
    Status      ToolTaskStatus
    Progress    float64
    Message     string
    Result      *types.ToolResult
    Error       string
    CreatedAt   time.Time
    StartedAt   *time.Time
    FinishedAt  *time.Time
}
```

Agent 侧需要的接口：

```text
StartTask
GetTaskStatus
GetTaskResult
CancelTask
ResumeAgentTurn
```

### 5.4 Agent 状态变化

当前 AgentTurn 不能只表示“执行中”或“完成”，需要支持：

```text
running
waiting_tool
waiting_input
waiting_approval
completed
failed
cancelled
recovering
```

异步 Tool 的基本流程：

```text
Agent 调用异步 Tool
  → 创建 ToolTask
  → 持久化 task_id
  → AgentTurn 进入 waiting_tool
  → 返回当前状态
  → Task 后台执行
  → Task 完成
  → 通过 ResumeAgentTurn 继续 Agent
```

### 5.5 必须满足的要求

- task_id 全局唯一
- 状态持久化
- 支持查询
- 支持取消
- 支持超时
- 支持失败原因
- 支持重启恢复
- 支持幂等继续
- 同一个 Tool Task 不能重复执行
- Agent 恢复时不能重复追加 Tool Call

## 6. 上下文治理

当前已增加 `MessageContextManager`，负责把完整 Transcript 和模型实际使用的
Active Context 分离，并提供上下文估算、Tool 输出截断和压缩。

这是 Agent 核心的主要缺口之一。

### 6.1 当前风险

- Read 可能返回整个大文件
- Grep 可能产生大量结果
- Tool Result 没有统一长度限制
- 历史消息持续增长
- 模型上下文可能被无关输出占满
- model profile 中的输入 token 上限没有形成 Agent 层预算
- 没有区分完整审计记录和当前工作上下文

### 6.2 建议分成两层

```text
Canonical Transcript
  完整保存原始消息、Tool Call、Tool Result、错误和事件

Active Context
  只保留当前模型调用真正需要的内容
```

完整记录用于审计和恢复，Active Context 用于控制模型输入大小。

### 6.3 ContextManager

当前实现：

```go
type ContextManager interface {
    AddMessage(message types.Message) error
    Messages() []types.Message
    EstimateTokens() int
    NeedsCompaction() bool
    Compact(ctx context.Context) error
    Reset() error
}
```

实现包含：

- `CanonicalMessages`：保留当前 AgentTurn 的完整消息
- `Messages`：返回模型实际使用的 Active Context
- Tool 结果只在 Active Context 中截断
- 保留 system 前缀、当前用户消息以及完整的 assistant/tool 调用组
- 超过阈值时压缩较旧消息
- 支持注入自定义 Token estimator
- Context 配置按模型能力比例计算
- `MaxInputTokens` 可从模型 profile 的 `maxInputTokens` 获取

### 6.4 最小治理规则

需要支持：

- 单个 Tool Result 最大字符数
- 单个文件最大读取大小
- 读取文件的行范围
- Grep 最大匹配数量
- 历史消息保留最近 N 轮
- 旧 Tool Result 摘要化
- 大输出转为 Artifact 引用
- 当前待处理 Tool Call 不得被压缩
- System Prompt 和任务输入始终保留

### 6.5 推荐配置

上下文容量不建议写死成某个 token 数。不同模型的上下文窗口、
输入上限和输出上限不同，应根据当前 Agent 使用的模型动态计算。

建议配置比例，而不是固定容量：

```yaml
context:
  target_ratio: 0.70
  compaction_threshold: 0.80
  hard_limit_ratio: 0.90
  output_reserve_ratio: 0.15
  tool_output_ratio: 0.10
  min_recent_rounds: 4
```

含义：

```text
有效输入容量 = 当前模型输入容量 × target_ratio
开始压缩     = 当前模型输入容量 × compaction_threshold
硬限制       = 当前模型输入容量 × hard_limit_ratio
单个 Tool 输出 ≤ 当前模型输入容量 × tool_output_ratio
```

其中当前模型输入容量由模型配置动态得到，优先使用模型声明的输入上限，
并结合本次请求的输出预留：

```text
effective_input_capacity
  = model_input_limit
  - reserved_output_tokens
  - system_prompt_reserve
```

如果模型没有声明精确的输入上限，则使用模型注册表提供的上下文容量作为
保守估计。固定的 `max_tool_output_chars` 或 `max_file_read_chars` 可以作为
最后的绝对保险，但不应该作为主要上下文策略。

比例配置的优点：

- 自动适配不同上下文窗口的模型
- 切换模型时不需要修改 Agent 配置
- 可以为输出预留空间
- 避免小模型使用过大的固定值
- 避免大模型被过度限制

## 7. Agent Budget

当前主要使用：

```text
max_rounds
max_parallel_tools
timeout
```

这些还不够。

建议把 Agent Budget 独立出来：

```go
type AgentBudget struct {
    MaxModelRounds  int
    MaxToolCalls    int
    MaxWallTime     time.Duration
    MaxInputTokens  int
    MaxOutputTokens int
    MaxFileChanges  int
}
```

每次执行前后都检查预算：

```text
模型调用前：检查轮数、时间、token
Tool 调用前：检查 Tool Call 数量、并发数、时间
Tool 完成后：累计输出和变更
Agent 结束时：记录实际使用量
```

### 7.1 当前默认值建议

```text
普通 QA：1～3 轮
普通 Agent 任务：10～30 轮
复杂 Agent 任务：30～80 轮
```

默认 200 轮不应单独作为安全边界。

## 8. Agent Recovery

当前 Recovery 主要在 Flow / Team / Call 层处理。

Agent 内部的以下状态还没有完整持久化：

- 当前模型轮次
- 已发出的 Tool Call
- 已完成的 Tool Call
- 当前消息上下文
- 待完成的异步 Task
- 当前预算消耗
- 当前 Agent 状态

### 8.1 AgentCheckpoint

`AgentCheckpoint` 不是新的编排层，也不是完整对话记录。

它是 AgentTurn 内部的“可恢复执行位置”或“恢复指针”，作用是让 Runtime 知道：

- 当前执行到第几轮
- 哪些 Model Call 已经完成
- 哪些 Tool Call 已经发出
- 哪些 Tool Call 已经完成
- 哪些异步任务仍在运行
- 当前上下文应该从哪里恢复
- 当前预算已经消耗多少
- Agent 是运行中、等待中还是失败中

如果进程在 Tool 执行中崩溃、网络断开或 Agent 因异步任务进入等待，
没有 Checkpoint 就无法判断：

```text
这个 Tool 是没有执行？
还是已经执行但结果没有返回？
```

Checkpoint 的核心作用是避免恢复时重复执行：

```text
已完成的只读 Tool：不要重复追加
已创建的异步 Task：查询原 task_id，不要重新创建
未完成的副作用 Tool：进入人工或显式恢复流程
```

它解决的是“执行位置恢复”问题，不解决：

```text
上下文压缩
Tool 参数校验
权限判断
业务结果正确性
```

因此，Checkpoint 不能替代：

```text
Canonical Transcript：保存完整消息和 Tool 结果
ContextManager：决定下一次模型调用携带什么上下文
ToolTaskStore：保存异步任务的真实状态
```

Checkpoint 更适合保存“引用和状态”，而不是复制完整上下文：

```go
type AgentCheckpoint struct {
    AgentTurnID      string
    TranscriptSeq    int64
    Round            int
    Status           types.TurnStatus
    PendingToolCalls []string
    CompletedTools   []string
    PendingTasks     []string
    ToolCalls        int
    Usage            types.TokenUsage
    UpdatedAt        time.Time
}
```

### 8.2 检查点时机

至少在以下时机保存：

```text
AgentTurn 开始
模型响应完成
Tool Call 开始前
Tool Call 完成后
创建异步 Task 后
AgentTurn 暂停前
AgentTurn 完成或失败时
```

### 8.3 恢复原则

- 只读 Tool 可以按策略自动重试。
- 写操作不能默认自动重放。
- 异步 Task 优先查询已有 task_id，不能重新创建。
- 已完成的 Tool Call 不能重复追加。
- 恢复后要保留原始错误和重试原因。
- 恢复必须继续使用原 Agent Budget。

## 9. Hook 接入计划

Hook API 和 `TurnLoop` 生命周期接入已完成：

```text
on_start
  AgentTurn 开始后、模型调用前

on_tool_start
  Tool 白名单检查后、真正执行前

on_tool_end
  Tool 返回结果后

on_error
  Model、参数校验、Tool、Context、Budget 错误

on_end
  AgentTurn 最终结束时
```

当前错误策略：

```text
on_start / on_tool_start 失败：阻止对应执行，并返回错误 Tool Result
on_tool_end / on_error / on_end 失败：观察型处理，不覆盖 Agent 原始结果
```

后续如果接入安全策略，可以为 Hook 增加显式的 blocking / observing 模式；
当前不把所有 Hook 错误都当成阻断错误。

## 10. Read / Write 能力增强与门禁

当前项目不需要优先建设一个泛化的“Guardrail 脚本系统”。

当前更重要的是把 Read / Write 做成安全、可控、可恢复的文件操作能力。
这属于 Workspace Tool 和 Agent Tool Runtime 的结合，不需要新建额外的
Agent 层级。

### 10.1 Read 能力

Read 至少需要支持：

- 文件大小限制
- 行范围读取
- 编码和二进制文件判断
- 输出截断
- revision 返回
- 路径边界检查
- 可选敏感信息脱敏
- 生成结构化文件元数据

推荐返回：

```json
{
  "success": true,
  "file": "src/App.tsx",
  "content": "...",
  "revision": "sha256:...",
  "truncated": false,
  "line_start": 1,
  "line_end": 120
}
```

### 10.2 Write 能力

Write 统一支持：

```text
create
replace
edit
```

其中 `edit` 使用 `old_text` / `new_text` 和可选 `base_revision`：

- old_text 必须匹配
- 默认只允许匹配一次
- revision 不一致时拒绝修改
- 修改失败不能写入半成品
- 返回新的 revision
- 返回变更摘要
- 保留 WorkspaceOperation 审计记录

### 10.3 文件操作策略

可以在 Tool 执行前增加轻量的文件操作策略：

```text
ReadPolicy
WritePolicy
```

它们负责：

- 路径是否允许
- 文件是否超过大小限制
- 是否允许创建目录
- 是否允许覆盖
- 是否需要 base_revision
- 是否需要脱敏
- 是否记录额外审计信息

这比当前阶段引入抽象的 Input / Output / Tool Guardrail 更直接。

### 10.4 Guardrail 的定位

当前不优先建设脚本化的通用 Guardrail 平台。

Read / Write 的主要问题不是“缺少 Guardrail 脚本”，而是文件操作本身需要
更强的能力和策略：

```text
Read / Write Policy
  处理文件操作本身的安全和一致性

Tool Parameter Validation
  处理 Tool 参数是否合法

Guardrail
  后续处理跨 Tool、跨输入输出的通用策略
```

当前已有的 regex / contains Checker 可以作为最小实现保留，
但不把它作为当前 Agent 核心建设的第一优先级。

## 11. 明确暂不做的内容

以下内容暂时不进入 Agent 核心改造：

### 11.1 HITL

基础审批链路、永不过期策略、审批审计字段和按顺序处理已经完成。
后续只考虑审计查询和审批策略展示，不增加批量审批协议。

### 11.2 RunCommand

作为 Tool 或 Skill 能力处理，不放入本次 Agent 核心。

### 11.3 GitDiff / GitStatus / Build / Test

作为 Skill 或工程 Tool 处理，不作为 Agent 核心逻辑。

### 11.4 浏览器实现

浏览器属于外部 Tool / Browser Harness。Agent 只需要能够调用同步或异步 Tool，并处理结果。

### 11.5 文件编辑 Tool

不新增独立的 `ApplyPatch` Tool。后续增强现有 `Write`，支持全量写入和局部编辑；局部编辑使用 revision 和明确的文本匹配，冲突时返回结构化错误。

## 12. 实施优先级

### P0：必须做

1. Tool 参数 Schema 校验。
2. Agent Tool Allowlist 的执行时强制检查。
3. Hook 接入 TurnLoop 生命周期。
4. Read / Write 基础策略和 revision 元数据。
5. Tool Result 和文件读取大小限制。
6. Agent Budget 基础结构。
7. AgentTurn 状态模型扩展。

### P1：核心能力

1. ContextManager。
2. 按模型能力比例计算上下文预算。
3. 上下文压缩。
4. AgentCheckpoint（已完成基础版本）。
5. Tool Task Store。
6. Durable Async Task。
7. Agent 暂停和恢复（已完成用户输入恢复基础链路）。
8. Write 局部编辑和冲突处理。

### P2：增强能力

1. Artifact 引用。
2. Tool retry / backoff。
3. 成本预算。
4. 更细的 Tool 权限策略。
5. 通用 Guardrail 扩展。
6. 大型 Tool Result 的摘要和索引。

## 14. 本轮已完成的 AgentBudget / Checkpoint / Resume

### AgentBudget

Agent 已支持独立预算：

```text
max_model_rounds
max_tool_calls
max_wall_time
max_input_tokens
max_output_tokens
max_file_changes
max_tool_output
```

预算超限时会停止当前 AgentTurn，并返回失败结果；如果配置了
CheckpointStore，会同时保存当前恢复指针。

### AgentCheckpoint

基础文件实现：

```text
internal/agent/checkpoint.go
.agents/data/agent-checkpoints/<checkpoint-id>.json
```

Checkpoint 保存：

```text
Agent / Call 标识
当前 round
Active Context
最近模型输出
Token Usage
Budget Usage
Workspace Operations
等待输入信息
```

Checkpoint 不是完整审计历史；完整历史仍由 Session Event 保存。

### Agent Resume

当前支持：

```text
AskUserQuestion
  → AgentTurn waiting_input
  → 保存 AgentCheckpoint
  → Team / Flow 传播 waiting_input
  → Flow.Resume(session_id, input)
  → 读取 checkpoint
  → 恢复 Active Context
  → 追加用户回答
  → 继续 Model / Tool Loop
```

恢复成功后会删除已消费的 Checkpoint，避免同一恢复点被重复执行。

当前尚未覆盖：

```text
复杂 Team 中多个并行等待调用的恢复
```

### Durable Async Tool Task 当前实现

当前已经增加：

```text
ToolTaskStore
AsyncToolExecutor
waiting_tool
Checkpoint.PendingTool
Flow / Team waiting_tool 传播
BuildRuntime 启动恢复扫描
```

异步 Tool 通过 Agent 配置显式启用：

```yaml
loop:
  async_tools:
    - Bash
```

同一模型响应中如果同时包含异步 Tool 和其他 Tool，当前会拒绝执行，
避免结果顺序和恢复语义不明确。

持久化位置：

```text
.agents/data/tool-tasks/<task-id>.json
.agents/data/agent-checkpoints/<checkpoint-id>.json
```

进程启动时：

```text
加载 queued / running ToolTask
  → 对可安全恢复的任务重新启动
  → 对不可安全重放的 running 任务标记 failed
扫描 waiting_tool AgentCheckpoint
  → 检查 PendingTool 是否存在
  → 标记可继续或孤儿 Checkpoint
```

进程重启不会自动继续 LLM 推理；必须通过显式 Resume 继续 Agent。

## 13. 推荐最终 Agent Runtime

```text
AgentTurn
  ↓
检查 Agent 输入
  ↓
Read / Write Policy 在文件 Tool 执行边界生效
  ↓
加载 Agent Tool Allowlist
  ↓
加载 Agent Budget
  ↓
ContextManager 组织上下文
  ↓
调用 Model
  ↓
校验结构化输出 / Tool Call
  ↓
检查 Tool Allowlist
  ↓
校验 Tool 参数
  ↓
触发 on_tool_start
  ↓
执行同步 Tool / 并行 Tool / 异步 Tool Task
  ├── 同步完成：追加 Tool Result，继续 Loop
  ├── 并行完成：汇总结果，继续 Loop
  └── 异步任务：保存 checkpoint，进入 waiting_tool
  ↓
ContextManager 压缩或截断
  ↓
检查 Budget
  ↓
继续 Model / 完成 AgentTurn
  ↓
保存 checkpoint
  ↓
触发 on_end
```

## 14. 最终结论

当前 Agent 的基础 Model / Tool Loop 是合理的，Tool Allowlist 的配置方向也是正确的。

本轮真正需要建设的不是更多工程 Tool，而是 Agent Runtime 本身：

```text
Tool 参数校验
→ Hook 生命周期接入
→ Read / Write 能力增强
→ ContextManager
→ Agent Budget
→ AgentCheckpoint
→ Durable Async Task
→ Agent 暂停 / 恢复
```

其中最重要的三个缺口是：

```text
Read / Write 能力增强
上下文治理
Durable Async Task
```

完成这些能力后，Agent 才能从“同步 Model / Tool 循环”升级为：

```text
可控、可暂停、可恢复、可审计的 Agent Runtime
```
