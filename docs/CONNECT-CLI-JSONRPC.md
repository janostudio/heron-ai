# heron-connect CLI JSON-RPC 兼容设计

## 1. 目标

让 `heron-connect` 可以像启动 Claude Code、Codex 等 CLI 一样启动 Heron：

```text
heron-connect
  └── 启动 Heron CLI 子进程
        ├── stdin  发送 JSON-RPC 请求
        └── stdout 接收 JSON-RPC 响应
```

本方案只增加一层很薄的通信协议：

```text
stdin/stdout + JSON-RPC 2.0 + 一行一个 JSON 对象
```

不实现 ACP，也不要求 Heron 改造成 ACP Agent。

本方案的目标是：

- 复用当前 `FlowRuntime`；
- 复用当前 `.agents/data/sessions/<session_id>/session.jsonl`；
- 复用当前 Flow / Team / Agent / Command / Webhook 编排；
- 让 `heron-connect` 通过 CLI 子进程接入 Heron；
- 保留当前 `--prompt` 的人工使用方式；
- 第一期只解决一问一答，以及处于可继续状态的 FlowSession；
- 后续可以在不破坏第一期协议的情况下增加进度通知。

本方案不试图把 Heron 内部的编排概念暴露给 `heron-connect`。

外部只需要知道：

```text
session_id
turn_id
status
reply
error
```

## 1.1 格式边界

这里有三个不同层次，不能都叫作“JSONL 格式”：

```text
JSON
  = 数据编码格式

JSONL/NDJSON
  = 一行一个 JSON 对象的分帧方式

JSON-RPC 2.0
  = 请求、响应、通知和错误的消息协议
```

本方案的完整表述是：

```text
JSON-RPC 2.0 over JSONL/NDJSON stdin/stdout
```

其中：

- JSONL/NDJSON 不是 Heron 自创的格式；
- JSON-RPC 2.0 不是 Heron 自创的协议；
- Heron 只定义 JSON-RPC 的业务方法和 `turn` 的参数/结果；
- `session.jsonl` 和 `evidence.jsonl` 是 Heron 内部存储 Schema，不是 JSON-RPC 消息。

同样使用 JSONL，不代表 Schema 相同。

## 1.2 与 Agent CLI `stream-json` 的关系

Claude Code、CodeBuddy、Codex 等 CLI 都使用一行一个 JSON 事件的方式输出机器可读数据，但它们的事件 Schema 并不完全相同：

```text
Claude Code / CodeBuddy
  system / assistant / user / result

Codex
  thread.started / turn.started / item.started /
  item.completed / turn.completed
```

这些属于 Agent CLI 事件流，不是 JSON-RPC。

本方案当前选择：

```text
Heron 与 heron-connect：
JSON-RPC 2.0 over JSONL

Heron 内部：
SessionEvent over session.jsonl
```

这样做的原因是通信需要稳定的请求 ID、响应匹配和标准错误结构；内部存储需要完整的执行事件、顺序号和恢复信息。两者职责不同，因此使用两套 Schema。

未来如果需要直接兼容 Claude Code/CodeBuddy 的 `stream-json`，可以新增独立的 Transport Adapter，不修改当前 Session 存储格式，也不修改 JSON-RPC 的 `turn` 方法。

## 2. 为什么不直接使用当前 `--prompt`

当前命令：

```bash
heron --prompt "请检查项目中的 .gitignore"
```

适合人直接执行，但不适合被另一个程序长期驱动。

当前输出是人类可读文本，类似：

```text
Flow: auto_bugfix_gitignore
Model: hy3-ioa
FlowSession: fs_xxx
Status: waiting_input

已经完成检查。

[DiagnosisReport] ...
```

它存在几个问题：

1. stdout 同时包含元信息、最终回复和记录摘要，机器不容易稳定解析；
2. 每次只执行一个 prompt，执行结束后进程退出；
3. 没有请求 ID，无法把请求和响应稳定对应起来；
4. 没有统一的机器可读错误结构；
5. 不能在一个长期运行的 CLI 进程中连续处理多个 Session 请求；
6. 后续无法自然增加取消、进度通知等双向通信。

因此需要新增一个独立模式：

```bash
heron --json-rpc
```

`--prompt` 和 `--json-rpc` 是两种不同的入口：

| 模式 | 用途 | 进程生命周期 | stdout |
|---|---|---|---|
| 默认/TUI | 人工交互 | 持续运行 | TUI 内容 |
| `--prompt` | 单次人工或脚本执行 | 执行一次后退出 | 人类可读结果 |
| `--json-rpc` | 被 `heron-connect` 驱动 | 持续运行 | 纯 JSON-RPC |

## 3. 非目标

第一期明确不做以下内容：

- 不实现 ACP；
- 不实现 WebSocket；
- 不实现 `bridge-protocol.md`；
- 不把 Heron 伪装成 Claude Code 或 Codex；
- 不解析 Claude Code 或 Codex 的私有输出格式；
- 不把 Team、Agent、Command、Webhook 的内部事件作为外部强制协议；
- 不要求 `heron-connect` 了解 Heron 的 Flow 配置；
- 不要求 `heron-connect` 了解 SharedRecord、Evidence、State 等内部数据；
- 不在第一期实现完整的实时进度流；
- 不在第一期实现外部权限审批协议。

第一期只保证：

```text
发送一条 turn 请求
等待一次最终响应
在 Session 仍可继续时发送下一条 turn 请求
```

## 4. 进程启动方式

建议新增：

```bash
heron --json-rpc
```

完整示例：

```bash
heron \
  --json-rpc \
  --flow .agents/flows/auto_bugfix.yml
```

也可以由 `heron-connect` 配置启动：

```toml
[[projects]]
name = "auto-bugfix"

[projects.agent]
type = "heron"

[projects.agent.options]
work_dir = "/path/to/auto-bugfix"
command = "/path/to/heron"
args = [
  "--json-rpc",
  "--flow",
  ".agents/flows/auto_bugfix.yml"
]
```

建议 `work_dir` 作为子进程当前工作目录。

这样：

- `.agents/` 使用项目目录下的配置；
- `.agents/data/` 使用项目目录下的运行数据；
- Flow 的 Workspace 仍然是当前项目目录；
- Heron 不需要为 `heron-connect` 增加另一套配置加载逻辑。

## 5. 传输格式

JSON-RPC 2.0 本身只定义消息结构，不定义 stdin/stdout 的具体分帧方式。

这里约定使用：

```text
newline-delimited JSON
```

即：

- 一行一个完整 JSON-RPC 对象；
- 每行以 `\n` 结束；
- JSON 对象不能跨多行；
- stdout 不允许输出普通文本；
- 日志全部写入 stderr。

示例：

```json
{"jsonrpc":"2.0","id":1,"method":"turn","params":{"session_id":"","input":"请检查项目"}}
{"jsonrpc":"2.0","id":2,"method":"turn","params":{"session_id":"fs_123","input":"继续处理"}}
```

这和 Agent CLI 常见的 `stream-json` 一样使用 JSONL 分帧，但消息内容不同：

```text
JSONL：
  解决“如何把多条 JSON 消息放进管道”的问题

JSON-RPC：
  解决“这条消息是什么、响应对应哪个请求、错误如何表达”的问题
```

### 5.1 stdout 约束

stdout 只能出现：

```json
{"jsonrpc":"2.0","id":1,"result":{...}}
```

或：

```json
{"jsonrpc":"2.0","id":1,"error":{...}}
```

后续的异步通知也必须是 JSON-RPC JSON 对象。

以下内容不能写入 stdout：

```text
Flow: ...
Model: ...
starting runtime...
team started
runtime error
```

### 5.2 stderr 约束

stderr 可以写：

```text
loading definitions
starting flow session
team diagnose started
flow turn completed
```

stderr 是日志通道，不属于协议内容。

### 5.3 立即刷新

每次写完响应或通知后都应该立即 flush，避免 `heron-connect` 因缓冲等待。

## 6. JSON-RPC 请求格式

第一期只定义一个方法：

```text
turn
```

请求：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "turn",
  "params": {
    "session_id": "",
    "input": "请检查项目中的 .gitignore"
  }
}
```

字段：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `jsonrpc` | string | 是 | 固定为 `"2.0"` |
| `id` | string/number | 是 | 请求和响应的关联 ID |
| `method` | string | 是 | 第一版固定为 `"turn"` |
| `params` | object | 是 | 请求参数 |
| `params.session_id` | string | 否 | 为空表示创建新的 FlowSession |
| `params.input` | string | 是 | 本轮用户输入 |

### 6.1 新建 FlowSession

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "turn",
  "params": {
    "session_id": "",
    "input": "请检查项目中的 .gitignore"
  }
}
```

处理方式：

```go
bundle.Flow.Start(ctx, types.StartFlowRequest{
    FlowID: flowID,
    Input:  input,
})
```

### 6.2 继续已有 FlowSession

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "turn",
  "params": {
    "session_id": "fs_123456",
    "input": "继续修复并验证"
  }
}
```

处理方式：

```go
bundle.Flow.HandleInput(ctx, sessionID, input)
```

如果当前会话明确处于 `waiting_input`，可以使用：

```go
bundle.Flow.Resume(ctx, sessionID, input)
```

协议层不需要让调用方区分 `HandleInput` 和 `Resume`，由 Heron 根据当前 FlowSession 状态决定。

当前运行时的 Session 状态边界：

| Session 状态 | 含义 | `turn` 行为 |
|---|---|---|
| `waiting_input` | 轮次正常结束（可续聊），或 Team 中途暂停等待用户输入 | 继续同一个 FlowSession |
| `created` / `running` | 会话已建立或正在执行 | 调用 `HandleInput` |
| `waiting_approval` | 中途审批门禁 | 必须先响应审批（ResumeApproval），不能直接发送普通 `turn` |
| `failed` | 终态 | 修正输入后创建新 FlowSession，或使用显式 Recovery API |
| `cancelled` | 终态 | 创建新 FlowSession |
| `interrupted` | 存在未完成的执行 | 需要先执行 Recovery，不能直接发送普通 `turn` |
| `completed` | 仅出现在旧数据回放中 | 新引擎不再产生该状态；旧 `completed` 会话也可以继续同一个 FlowSession |

会话生命周期由运行时决定：轮次正常结束一律可续，`on_proceed` 不再提供
`complete` / `wait_input` 之类的生命周期动作。因此 `heron-connect` 可以把
同一个 `session_id` 当作永久聊天线程 ID 一直复用；只有会话进入
`failed` / `cancelled` / `interrupted` 之后，才需要按业务决定使用 Recovery
或省略 `session_id` 开启新 FlowSession。

## 7. JSON-RPC 响应格式

成功响应：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "session_id": "fs_123456",
    "flow_turn_id": "ft_123456",
    "status": "waiting_input",
    "reply": "已经完成 .gitignore 检查和修复。",
    "records": [
      {
        "name": "DiagnosisReport",
        "summary": "发现 .gitignore 缺少必要规则"
      }
    ]
  }
}
```

第一期建议保留的字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `session_id` | string | FlowSession ID |
| `flow_turn_id` | string | 本次 FlowTurn ID |
| `status` | string | `waiting_input`、`waiting_approval`、`failed`、`cancelled` 等；`completed` 仅存在于旧数据回放 |
| `reply` | string | 用户可见的最终回复 |
| `records` | array | 可选的记录摘要 |
| `error` | string | 可选的业务错误信息 |

### 7.1 Session ID 约定

直接使用 Heron 的：

```text
FlowSession.ID
```

例如：

```text
fs_123456
```

不额外创建一层：

```text
HeronAgentSessionID
```

这样可以保持：

```text
heron-connect session
    ↔ Heron FlowSession
```

### 7.2 Records 的边界

`records` 不是恢复数据，也不是完整证据链。

它只用于给调用方返回简短摘要，例如：

```json
{
  "name": "ChangeSet",
  "summary": "修改了 .gitignore，新增 dist/ 和 coverage/ 规则"
}
```

完整内容继续保存在 Heron 的：

```text
.agents/data/sessions/<session_id>/session.jsonl
.agents/data/sessions/<session_id>/evidence.jsonl
```

`heron-connect` 不需要解析这些文件。

### 7.3 第一版协议范围

第一版只定义一个请求方法：

```text
turn
```

不增加以下 ACP 风格的方法：

```text
initialize
session/new
session/load
session/update
```

当前使用一个更小的业务接口：

```text
turn(session_id, input) -> turn result
```

调用方不需要单独调用 `session/new`。第一次 `turn` 请求没有 `session_id` 时，Heron 创建 FlowSession 并在响应中返回 ID；后续请求只有在 Session 仍可继续时才带回这个 ID。

## 8. 错误格式

错误使用 JSON-RPC 2.0 标准 error 响应：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "error": {
    "code": -32001,
    "message": "flow turn exceeded max team turns",
    "data": {
      "session_id": "fs_123456",
      "flow_turn_id": "ft_123456",
      "retryable": false
    }
  }
}
```

建议错误码：

| 错误码 | 含义 |
|---:|---|
| `-32700` | JSON 解析失败 |
| `-32600` | 无效的 JSON-RPC 请求 |
| `-32601` | 不支持的方法 |
| `-32602` | 参数错误 |
| `-32001` | FlowTurn 执行失败 |
| `-32002` | Session 不存在或状态不允许 |
| `-32003` | Runtime 配置或初始化失败 |
| `-32004` | 请求被取消 |

### 8.1 单次错误不退出进程

以下错误只返回 JSON-RPC error，不退出整个 CLI 进程：

- 输入参数错误；
- Session 不存在；
- FlowTurn 执行失败；
- 达到运行限制；
- 当前 Session 状态不允许继续执行。

只有以下情况可以退出进程：

- 启动参数错误；
- Flow 配置加载失败；
- models 配置加载失败；
- 必要依赖初始化失败；
- stdin 已关闭；
- 进程收到终止信号。

这样 `heron-connect` 的 Agent Session 不会因为一轮业务错误而整体消失。

## 9. 进程生命周期

### 9.1 启动

`heron-connect` 启动：

```bash
heron --json-rpc --flow ...
```

Heron 完成配置加载后进入 stdin 读取循环。

### 9.2 请求循环

逻辑：

```text
读取一行
  → 解析 JSON-RPC
  → 校验 method 和 params
  → 执行一个 FlowTurn
  → 写入一行 response
  → 继续读取下一行
```

第一期建议按 Session 串行处理请求。

原因：

- 当前一个 FlowSession 同一时间不应执行多个 FlowTurn；
- 避免两个请求同时修改同一个 `session.jsonl`；
- 与 `heron-connect` 当前按 session 顺序处理消息的语义一致；
- 实现简单。

如果后续需要并行，可以按 `session_id` 分片，而不是全局并行。

### 9.3 stdin EOF

stdin EOF 表示调用方关闭了 CLI：

```text
关闭运行循环
关闭必要资源
正常退出
```

### 9.4 进程关闭

`heron-connect` 关闭 Agent Session 时：

1. 关闭 Heron 子进程 stdin；
2. 等待进程优雅退出；
3. 超时后终止进程；
4. 必要时杀掉整个进程组。

这部分可以复用 `heron-connect` 现有 Claude/Codex Adapter 的进程管理方式。

## 10. heron-connect Adapter

Heron 侧实现 JSON-RPC Server 后，`heron-connect` 仍需要一个轻量 Adapter。

建议新增：

```text
heron-connect/agent/heron/
├── agent.go
├── session.go
└── jsonrpc.go
```

### 10.1 Agent

实现：

```go
core.Agent
```

职责：

- 读取 `command`、`args`、`work_dir`；
- 校验命令是否存在；
- 创建 Heron CLI Session；
- 保存配置中的环境变量；
- 提供 `StartSession`、`ListSessions`、`Stop`。

### 10.2 AgentSession

实现：

```go
core.AgentSession
```

职责：

- 启动 Heron 子进程；
- 向 stdin 写 JSON-RPC 请求；
- 从 stdout 读取 JSON-RPC 响应；
- 根据 `id` 匹配请求和响应；
- 把成功结果转成 `core.EventResult`；
- 把错误结果转成 `core.EventError`；
- 返回 `CurrentSessionID()`；
- 支持 `Close()`；
- 第一版可以暂不实现真正的权限请求。

### 10.3 Send 映射

`heron-connect` 调用：

```go
session.Send(prompt, images, files)
```

发送：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "turn",
  "params": {
    "session_id": "fs_123456",
    "input": "继续处理"
  }
}
```

收到成功响应后，转换成：

```go
core.Event{
    Type:      core.EventResult,
    Content:   result.Reply,
    SessionID: result.SessionID,
    Done:      true,
}
```

收到错误响应后，转换成：

```go
core.Event{
    Type:  core.EventError,
    Error: err,
}
```

### 10.4 图片和文件

第一期可以采用与当前 Codex Adapter 类似的低成本方式：

1. `heron-connect` 把附件写入 Heron Workspace；
2. 将文件路径追加到 `input`；
3. Heron 通过 Workspace 工具读取文件。

例如：

```text
请分析附件。

文件已保存到：
/path/to/workspace/.heron-connect/attachments/report.pdf
```

第一期不需要把二进制内容直接塞进 JSON-RPC 请求。

## 11. 第一版不做实时流，但保留扩展点

第一期：

```text
一个 turn 请求
一个 turn 响应
```

不发送中间进度。

后续可以增加 JSON-RPC Notification：

```json
{
  "jsonrpc": "2.0",
  "method": "progress",
  "params": {
    "session_id": "fs_123456",
    "flow_turn_id": "ft_123456",
    "type": "text",
    "content": "正在检查 .gitignore..."
  }
}
```

Team 进度：

```json
{
  "jsonrpc": "2.0",
  "method": "progress",
  "params": {
    "session_id": "fs_123456",
    "flow_turn_id": "ft_123456",
    "type": "team",
    "team_id": "diagnose",
    "status": "started"
  }
}
```

后续通知类型可以包括：

```text
progress
tool_started
tool_completed
record_published
waiting_input
```

这些是 Heron 自定义扩展，不属于第一期必须协议。

建议扩展原则：

- 不改变已有 `turn` 请求；
- 不改变已有成功响应；
- 不要求调用方必须支持通知；
- 不把内部所有 SessionEvent 原样暴露；
- 只提供调用方真正需要展示的摘要。

## 12. 与现有数据文件的关系

通信协议和持久化文件分开：

```text
stdin/stdout JSON-RPC
    = 进程间实时通信

session.jsonl
    = 会话恢复和完整执行记录

evidence.jsonl
    = 跨 Team/Agent 查询的精简证据

state
    = Team/Agent 短期状态
```

JSON-RPC 响应只返回当前 FlowTurn 的结果，不返回完整 `session.jsonl`。

恢复时：

```text
heron-connect 保存 session_id
  → 如果 Session 仍可继续，下次发送 turn 时带回 session_id
  → Heron 从 session.jsonl 恢复 FlowSession
  → 继续处理新输入

轮次正常结束的会话（waiting_input）可以一直复用同一个 session_id 继续对话。
只有上一次响应的状态是 `failed` 或 `cancelled` 时，才不要把该 ID 当作
永久聊天线程 ID；下一次新任务应省略 `session_id`。
```

### 12.1 传输 Schema

传输消息示例：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "turn",
  "params": {
    "session_id": "fs_001",
    "input": "继续验证"
  }
}
```

它需要：

- `jsonrpc`；
- `id`；
- `method`；
- `params`。

### 12.2 存储 Schema

存储事件示例：

```json
{
  "seq": 12,
  "event_id": "fs_001-12",
  "type": "team_turn.completed",
  "flow_session_id": "fs_001",
  "flow_turn_id": "ft_001",
  "team_session_id": "ts_001",
  "team_turn_id": "tt_001",
  "team_id": "diagnose",
  "created_at": "2026-08-25T10:00:00Z",
  "payload": {
    "status": "completed"
  }
}
```

它需要：

- `seq`；
- `event_id`；
- 执行层级 ID；
- 事件类型；
- 时间；
- 可恢复的 Payload。

### 12.3 两套 Schema 的转换

转换关系是：

```text
JSON-RPC turn request
  → FlowRuntime.Start / HandleInput / Resume
  → SessionEvent 写入 session.jsonl
  → FlowTurnResult
  → JSON-RPC turn response
```

不是：

```text
直接把 session.jsonl 原样返回给 heron-connect
```

### 12.4 evidence.jsonl 的边界

`evidence.jsonl` 只保存 Flow 范围的 `SharedRecord`，用于跨 Team/Agent 查询。

它不是：

- JSON-RPC 通信日志；
- 完整的 Session Replay；
- Agent CLI 的输出副本。

因此三者保持独立：

```text
stdin/stdout JSONL
  = 外部进程通信

session.jsonl
  = 完整会话和执行恢复

evidence.jsonl
  = 精简的跨 Team/Agent 业务证据
```

## 13. 最小端到端示例

启动：

```bash
cd /path/to/project
heron --json-rpc --flow .agents/flows/auto_bugfix.yml
```

输入：

```json
{"jsonrpc":"2.0","id":1,"method":"turn","params":{"session_id":"","input":"请检查项目中的 .gitignore"}}
```

输出：

```json
{"jsonrpc":"2.0","id":1,"result":{"session_id":"fs_001","flow_turn_id":"ft_001","status":"waiting_input","reply":"已完成检查。"}}
```

新的输入（轮次正常结束，继续同一个 FlowSession）：

```json
{"jsonrpc":"2.0","id":2,"method":"turn","params":{"session_id":"fs_001","input":"继续修复并运行验证"}}
```

输出：

```json
{"jsonrpc":"2.0","id":2,"result":{"session_id":"fs_001","flow_turn_id":"ft_002","status":"waiting_input","reply":"已完成修复并通过验证。"}}
```

错误：

```json
{"jsonrpc":"2.0","id":3,"error":{"code":-32001,"message":"flow turn exceeded max team turns","data":{"session_id":"fs_001","flow_turn_id":"ft_003"}}}
```

## 14. 实现顺序

### Heron

1. 在 CLI 增加 `--json-rpc`；
2. 增加 stdin 行读取循环；
3. 增加 JSON-RPC 请求解析；
4. 实现 `turn` 方法；
5. 调用当前 `FlowRuntime.Start`；
6. 根据 `session_id` 调用 `HandleInput` 或 `Resume`；
7. 输出统一成功响应；
8. 输出统一错误响应；
9. 保证 stdout 只有 JSON；
10. 将日志统一写入 stderr；
11. 增加 stdin EOF 和信号退出处理；
12. 增加 JSON-RPC 单元测试。

### heron-connect

1. 新增 `agent/heron`；
2. 实现 Heron 子进程启动；
3. 实现 JSON-RPC request ID；
4. 实现 stdin 写入；
5. 实现 stdout JSONL 读取；
6. 将响应转换为 `core.Event`；
7. 保存和恢复 `FlowSession.ID`；
8. 实现优雅关闭和超时终止；
9. 增加 mock Heron CLI 测试；
10. 增加一次真实 auto-bugfix 集成测试。

## 15. 验收标准

### Heron CLI

```bash
heron --json-rpc --flow .agents/flows/default.yml
```

满足：

- 可以连续处理多条 JSON-RPC 请求；
- 每条请求都有对应的 `id`；
- stdout 每行都是合法 JSON；
- stdout 没有普通日志；
- stderr 可以输出日志；
- 新请求可以创建新的 FlowSession；
- 带 `session_id` 的请求可以继续旧 FlowSession；
- 单次 FlowTurn 失败时进程不退出；
- stdin EOF 时正常退出。

### heron-connect

满足：

- 可以通过配置启动 Heron CLI；
- 可以发送第一条用户消息；
- 可以收到最终回复；
- 可以保存 Heron 返回的 `session_id`；
- 可以继续发送下一条消息；
- Heron 单轮错误可以转换为用户可见错误；
- 关闭连接时不会遗留 Heron 子进程。

## 16. 最终结构

```text
heron-connect
    │
    │ 启动 Heron CLI 子进程
    │
    ├── stdin  ── JSON-RPC request: turn
    └── stdout ─ JSON-RPC response
                      │
                      ▼
                 Heron CLI
                      │
                      ▼
                FlowRuntime
                      │
                      ▼
             Flow → Team → Agent / Command / Webhook
```

核心原则：

```text
底层分帧使用业内 JSONL/NDJSON
消息协议使用标准 JSON-RPC 2.0
传输 Schema 与存储 Schema 分离
外部协议简单
内部编排不暴露
stdout 纯协议
stderr 只放日志
session_id 直接使用 FlowSession.ID
第一期只做 turn 请求和最终响应
```
