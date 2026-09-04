# Agent 能力设计评估

> 评估日期：2026-08-26  
> 评估范围：Agent 内部执行、Tool、Loop、网页开发能力和异步任务能力。  
> 不评估 Flow、Team 的整体编排设计。

## 1. 结论

当前 Agent 已经具备一个清晰的基础执行闭环：

```text
Prompt
  → Model
  → Tool Call
  → Tool Result
  → Model
```

它适合：

- 单 Agent 问答
- 文件读取、搜索和简单文件写入
- 基础代码理解
- 有界的 Model / Tool 循环

但当前还不是完整的软件工程 Agent，也不能独立完成稳定的网页开发闭环。

当前更接近：

```text
文件型 Agent
```

目标形态应该是：

```text
软件工程 / 网页开发 Agent
```

## 2. 当前 Agent 内部结构

```text
Agent Call
  └── TurnLoop
        ├── PromptRenderer
        ├── ModelProvider
        ├── ToolExecutor
        ├── GuardrailChecker
        ├── RouteParser
        ├── HITLGate
        ├── HookExecutor
        └── StructuredOutputManager
```

`TurnLoop` 负责一次 Agent 执行。一次执行内部可以包含多轮：

```text
for round < max_rounds:
    调用模型
    如果没有 Tool Call:
        校验并返回结果
    如果有 Tool Call:
        执行工具
        把工具结果追加到消息
        继续调用模型
```

模型和工具调用属于 Agent 内部实现细节，不应继续向外暴露为新的编排层。

## 3. 当前 Tool 能力

当前内置 Tool 主要包括：

```text
Read
Write
Grep
Glob
TodoWrite
TodoRead
```

### 已有优势

- `ToolRegistry` 与 Agent 解耦，方便扩展。
- Tool 接口统一，支持参数 Schema。
- Workspace 有根目录约束，能够阻止路径逃逸。
- 文件读写会生成 `WorkspaceOperation`。
- Write 支持 revision 概念，可以继续发展为并发修改检测。
- Tool 支持只读和串行两种执行分类。
- 只读 Tool 可以在一次模型响应内并发执行。
- Tool 错误可以转换为模型可理解的 Tool Result。

### 主要问题

#### 3.1 缺少工程开发工具

Agent 当前没有直接使用以下能力：

```text
Write（支持全量写入和局部编辑）
GitDiff
GitStatus
Test
Build
StartDevServer
StopProcess
```

虽然系统存在 Command Call，但它属于 Team 层的 Call，不是 Agent Loop 内的 Tool。

因此当前无法自然完成：

```text
修改代码
→ 安装依赖
→ 构建
→ 测试
→ 读取错误
→ 修复
→ 再次验证
```

#### 3.2 文件编辑能力需要统一

当前 Write 主要是全量覆盖文件。对于代码开发，不建议再增加独立的 `ApplyPatch` Tool，而是统一增强 Write：

```text
Write(file, mode, content, old_text, new_text, base_revision)
```

其中 `mode` 可以是：

```text
create
replace
edit
```

它应该支持：

- 基于上下文的局部修改
- base revision 校验
- 修改失败返回明确错误
- 生成可审计 diff
- 原子写入
- 冲突后要求 Agent 重新读取文件

内部可以使用 patch 算法，但不需要把 `ApplyPatch` 作为新的 Tool 名称暴露给 Agent。现有 `Write(file, content)` 继续作为全量写入兼容形式。

#### 3.3 Grep 能力不完整

当前 Grep 的实现依赖 Workspace Read，主要适合读取单个文件，不是完整的递归代码搜索工具。

网页项目需要支持：

- 递归目录搜索
- 文件类型过滤
- 最大匹配数
- 最大输出字符数
- 行号
- 排除目录
- 跳过二进制文件
- 忽略 `node_modules`、`dist`、`.git` 等目录

#### 3.4 Todo 没有真实持久化状态

当前 Todo Tool 返回固定成功信息，TodoRead 也没有真正读取 TodoWrite 的内容。

因此 Prompt 中的“跟踪进度”与实际能力不一致。

Todo 至少应该具备：

```text
todo_id
title
status
priority
dependencies
created_at
updated_at
```

并持久化到当前 Agent Session。

#### 3.5 Custom Tool 和 MCP Tool 没有完整进入 Agent Loop

Agent 配置中存在：

```yaml
tools:
  builtin: []
  custom: []
  mcp: []
```

但当前 `TurnLoop` 的 Tool Schema 主要由内置工具名称硬编码生成。Custom Tool 和 MCP Tool 尚未形成完整的：

```text
注册
→ Schema 注入
→ Allowlist 校验
→ 执行
→ 结果审计
```

## 4. Loop 设计评估

### 4.1 合理的部分

- Model / Tool 循环结构正确。
- 有 `max_rounds`，可以阻止无限循环。
- 每轮都检查 Context 取消。
- Tool 结果会回到模型上下文。
- 支持一次模型响应中的多个 Tool Call。
- 只读 Tool 并发，写操作保持串行。
- Tool 返回顺序与模型请求顺序保持一致。

### 4.2 当前限制

当前主要只有一个循环预算：

```text
max_rounds
```

实际生产 Agent 还需要分别限制：

```text
max_model_rounds
max_tool_calls
max_wall_time
max_command_count
max_file_changes
max_output_tokens
max_cost
```

建议使用统一预算结构：

```go
type AgentBudget struct {
    MaxModelRounds   int
    MaxToolCalls     int
    MaxWallTime      time.Duration
    MaxCommandCalls  int
    MaxFileChanges   int
    MaxOutputTokens  int
    MaxCost          float64
}
```

### 4.3 默认 200 轮偏大

复杂网页任务可能确实需要多轮，但 200 轮不能单独作为安全边界。

建议：

```text
简单问答：1～3 轮
普通代码任务：10～30 轮
复杂网页任务：30～80 轮
```

超过 80 轮时，应同时要求：

- 明确的 wall-clock timeout
- Tool Call 数量限制
- token / cost 限制
- 变更文件数量限制
- 周期性状态摘要

### 4.4 路由协议需要统一

当前同时存在：

```text
<continue/>
<wait_input/>
<goal_achieved/>
```

以及 Structured Output 中的 JSON 路由。

建议将 JSON 作为主协议：

```json
{
  "reply": "任务结果",
  "next": {
    "action": "proceed",
    "reason": "验证通过"
  }
}
```

XML 标记只保留为旧配置兼容格式。

## 5. 当前是否支持异步 Tool

### 结论

当前有“并发执行”，但没有“持久化异步任务”。

当前 `ToolBatchExecutor` 的行为是：

```text
模型返回多个只读 Tool Call
  → 启动 goroutine
  → 并发执行
  → 等待全部完成
  → 继续 Agent Loop
```

因此它属于：

```text
同步 Agent Loop 内的并行 Tool 执行
```

而不是：

```text
可恢复的异步 Tool Task
```

### 当前不具备的能力

- Task ID
- 任务状态
- 进度
- 独立取消
- 持久化
- 重启恢复
- 断线后查询
- Poll
- 完成事件
- Webhook 回调
- Agent 等待后恢复

### 建议的异步任务模型

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
    ToolCallID  string
    ToolName    string
    Status      ToolTaskStatus
    Progress    float64
    Message     string
    Result      *ToolResult
    Error       string
    CreatedAt   time.Time
    StartedAt   *time.Time
    FinishedAt  *time.Time
}
```

异步 Tool 应支持：

```text
StartTask
GetTaskStatus
GetTaskResult
CancelTask
ResumeTask
```

适合异步化的任务：

- `npm install`
- `npm run build`
- 长时间测试
- Playwright 测试
- Docker 构建
- 大规模代码扫描
- Webhook 等待
- 人工审批
- 浏览器长任务

## 6. 当前网页开发能力评估

### 当前可以做

当前 Agent 可以通过 Read / Write / Grep / Glob：

- 创建 HTML、CSS、JavaScript 文件
- 创建 React、Vue、Svelte 基础组件
- 读取已有源码
- 搜索简单代码片段
- 修改小型静态页面

### 当前无法稳定做

当前缺少以下闭环：

```text
创建项目
→ 安装依赖
→ 启动开发服务器
→ 打开浏览器
→ 获取截图
→ 检查 DOM
→ 检查 Console
→ 检查 Network
→ 执行交互
→ 修复视觉和功能问题
→ 构建和测试
```

缺少的关键 Tool：

```text
工程类：
  Write（支持全量写入和局部编辑）
  RunCommand
  GitDiff
  GitStatus
  Build
  Test

浏览器类：
  BrowserOpen
  BrowserScreenshot
  BrowserDOM
  BrowserClick
  BrowserType
  BrowserConsole
  BrowserNetwork

进程类：
  StartProcess
  GetProcessStatus
  StopProcess
  ReadProcessOutput
```

浏览器能力应运行在隔离的 Browser Harness 或 Playwright 环境中，不应直接把宿主机浏览器控制权交给 Agent。

## 7. 安全能力现状

当前已经定义了：

- Guardrail
- HITL
- Hook
- Tool `NeedsApproval`
- Workspace 路径约束
- Workspace revision

但其中部分能力尚未完整接入主执行链：

### 需要重点确认和补齐

1. Agent Tool 执行前是否强制检查工具白名单。
2. `NeedsApproval` 是否自动进入 HITL 流程。
3. Write 是否会真正等待人工批准。
4. Hook 是否在 Agent Start、Tool Start、Tool End、Error、End 时触发。
5. Guardrail 是否只作为后续通用扩展，而不是承担 Read / Write Policy 的职责。
6. Tool 参数是否经过 Schema 校验。
7. Shell 是否有命令白名单和环境变量过滤。
8. Tool 输出是否有敏感信息脱敏。

当前这些能力不能只停留在配置和接口层，必须进入 `TurnLoop` 的执行路径。

## 8. 当前架构优势

### 8.1 Agent Loop 简单

核心执行路径短，容易调试和测试。

### 8.2 Tool Registry 解耦

Tool 可以独立注册和替换，不绑定具体 Agent 实现。

### 8.3 并发策略保守

只读 Tool 并发、写 Tool 串行，是相对安全的基础策略。

### 8.4 Workspace 有边界

文件访问限制在 Workspace 根目录内，具备基本安全边界。

### 8.5 结构化输出基础较好

已有 JSON 解析、必填字段检查和路由提取能力。

### 8.6 Agent 配置扩展方向正确

已经预留：

- Persona
- Model
- Tools
- Skills
- Knowledge
- Rules
- Structured Output
- HITL
- Hooks
- State

## 9. 主要缺点和功能缺失

按优先级排序：

### P0：Agent Tool 执行边界未完全接入

- Tool Allowlist
- Read / Write Policy
- Hook 生命周期接入
- Tool 参数校验

### P0：Read / Write 能力需要增强

- Read 行范围和大小限制
- Read 返回 revision 和元数据
- Write 支持 replace / edit
- Write 的 old_text 唯一匹配
- Write 的 base_revision 冲突检测
- 统一返回变更摘要

### P1：Agent Runtime 能力

- ContextManager
- Agent Budget
- AgentCheckpoint
- Durable Async Task
- Agent 暂停和恢复

### P2：通用 Guardrail

- Guardrail 作为可选的跨 Tool 规则扩展
- 不优先建设脚本化 Guardrail 平台
- 当前优先增强 Read / Write Policy，而不是扩展 Guardrail

### P1：缺少 Browser Harness

- Screenshot
- DOM
- Click
- Type
- Console
- Network
- 页面错误收集

### P1：缺少 Durable Async Task

- Task Store
- Task Status
- Progress
- Cancel
- Resume
- Restart Recovery

### P1：缺少工程验证闭环

网页开发 Agent 至少应支持：

```text
Edit
→ Build
→ Test
→ Start Dev Server
→ Browser Check
→ Collect Error
→ Patch
→ Re-test
```

### P2：上下文治理不足

- 文件大小限制
- 行范围读取
- Tool 输出截断
- 大文件分页
- 自动摘要
- 历史消息压缩
- Artifact 引用

### P2：预算和恢复能力不足

- 多维度 Budget
- Tool retry policy
- Backoff
- Idempotency Key
- Cost tracking
- Tool checkpoint
- Agent restart recovery

## 10. 建议实现路线

### 第一阶段：工程 Agent 基础

优先实现：

```text
Write（支持全量写入和局部编辑）
Tool Allowlist
Tool 参数校验
Read / Write Policy
ContextManager
Agent Budget
```

### 第二阶段：安全闭环

把以下能力接入 `TurnLoop`：

```text
Hooks
Schema Validation
Read / Write Policy
Context Compaction
```

### 第三阶段：网页开发闭环

增加隔离 Browser Harness：

```text
BrowserOpen
BrowserScreenshot
BrowserDOM
BrowserClick
BrowserType
BrowserConsole
BrowserNetwork
```

### 第四阶段：异步任务

增加：

```text
ToolTaskStore
ToolTaskExecutor
Task Status
Task Progress
Task Cancel
Task Resume
```

### 第五阶段：Loop 生产化

引入：

```text
AgentBudget
Context Compaction
Tool Output Limit
Checkpoint / Recovery
Durable Async Task
```

## 11. 建议验收标准

一个真正可用于网页开发的 Agent，至少应通过以下测试：

1. 能创建一个最小 React/Vue 项目。
2. 能使用 Patch 修改已有组件，而不是只能全量覆盖。
3. 能执行安装、构建和测试命令。
4. 能启动和停止开发服务器。
5. 能访问页面并取得截图和 DOM。
6. 能读取 Console 和 Network 错误。
7. 能根据浏览器错误自动修改代码。
8. 能在超时后取消长任务。
9. Agent 重启后能查询并恢复异步任务。
10. 写文件和执行命令可以按策略请求人工批准。
11. Tool 输出过大时不会直接撑爆上下文。
12. 整个任务有完整的 Tool、文件和测试审计记录。

## 12. 最终判断

当前 Agent 的基础设计方向是正确的：

```text
Agent
  └── Model / Tool Loop
```

当前最大的短板不是 Prompt，而是 Tool Runtime 和执行闭环。

优先级应当是：

```text
安全接入
→ 工程 Tool
→ 异步 Task
→ Browser Harness
→ 上下文和预算治理
```

在完成这些能力之前，当前 Agent 更适合作为单 Agent QA 和文件操作引擎，不适合作为完整的网页开发 Agent。
