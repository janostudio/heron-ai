# heron-ai 配置字段速查

本文列出 Flow / Team / Call / Agent 各配置字段的含义。权威来源：
`docs/configuration/*.md`。

## 编排层次

```
Flow → Team → Call → Agent / Command / Webhook
```

`Call` 是执行项（不是层级），统一了「LLM Agent / 固定命令 / Webhook」三种调用。

---

## Flow（`.agents/flows/<name>.yml`）

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | string | Flow 标识 |
| `entry` | string | 入口 Team 的 Flow 局部名 |
| `teams` | map | Team 绑定表，key 为 Flow 局部名 |

### Team 绑定（teams 下每个条目）

| 字段 | 类型 | 含义 |
|---|---|---|
| `team` | string | 指向 `.agents/teams/` 下的真实 Team id |
| `can_activate` | []string | 本绑定可激活的 Team。任何 Team 都可声明；不声明则不能激活别的 Team。被激活的 Team 结束后回到激活它的 Team |
| `depends_on` | []string | 前置 Team（完成后本 Team 才执行） |
| `inputs` | object/list | 用户消息 + SharedRecord 输入 |
| `on_proceed` | list/object | Team 以 `proceed` 结束后的固定路由 |

---

## Team（`.agents/teams/<name>.yml`）

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | string | Team 标识 |
| `goal` | string | Team 目标 |
| `calls` | map | 执行项集合 |
| `output` | object | 对外发布的 SharedRecord（`from` + `record` + `scope`） |
| `memory` | object | 可选 Team Memory（`enabled`/`max_items`/`max_chars`/`inject_summary`） |

### Call（calls 下每个条目）

| 字段 | 类型 | 含义 |
|---|---|---|
| `type` | string | `agent` / `command` / `webhook` |
| `agent` | string | （type=agent）Agent 定义名 |
| `command` | string/object | （type=command）Shell 命令 |
| `webhook`/`url` | object/string | （type=webhook）URL |
| `depends_on` | []string | 依赖的同 Team 内其他 call |
| `inputs` | object/list | 显式输入与 SharedRecord |
| `output` | object | 本次调用发布的 SharedRecord |
| `responsibility` | string | Agent 职责说明 |
| `timeout` | string | 超时 |
| `command.env` | map | （type=command）注入的环境变量，值支持 `${...}` 模板 |
| `command.args` | []string | （type=command）位置参数，逐项支持 `${...}` 模板（见下节） |

### 调度规则

- 无依赖的 call → 自动并行
- 有依赖的 call → 依赖完成后执行
- 依赖多个 → 读取声明的 SharedRecord 后执行

### 把共享记录传进 command（`${records.*}` 模板）

command call **不能读对话内容**，但可以通过 `command.env` / `command.args` 的模板
从上游 SharedRecord 取值。这是让"确定性脚本"消费"agent 产出"的唯一通道。

**语法**（`internal/runtime/call/policy.go`）：

| 写法 | 取什么 |
|---|---|
| `${records.<Name>}` | 该 record 的 `Summary`（简写） |
| `${records.<Name>.Summary}` | `Summary` |
| `${records.<Name>.RecordID}` | `RecordID` |
| `${records.<Name>.Name}` | `Name` |
| `${records.<Name>.Data.<field>}` | `Data` 的**顶层**字段 |

固定占位符同样可用：`${flow_session_id}`、`${flow_turn_id}`、`${team_id}`、
`${team_turn_id}`、`${call_id}`、`${call_turn_id}`、`${attempt}`、`${recovery_of}`。

### 先分清 `inputs` 的三种写法（**最容易踩的坑**）

| 写法 | 匹配范围 | 什么时候用 |
|---|---|---|
| `user_message: true` | 用户本轮原始输入 | 需要用户输入的 call |
| `flow_records: [X]` | **Flow 级** record | 引用**其他 team** 的产出，或已被提升为 flow 的 record |
| **`records: [{from, record}]`** | **指定 call 的产出**（含同 team 内的） | **同 team 内的顺序依赖** |

**关键区别**：`flow_records` / `team_records` 都只在 **Flow 级 record 集合**里按名字匹配
（`internal/runtime/team/runtime.go` 的 `selectCallRecords`：两者合并进同一个
`allowedNames`，但都从 `flowRecords` 参数中查找）。

因此**同 team 内"前一个 call 刚产出、后一个 call 要引用"时必须用 `records` + `from`**：

```yaml
# ❌ 错：IssueCandidates 此刻还是 team scope，不在 Flow 级集合里
acquire_slot:
  depends_on: [search]
  inputs:
    flow_records: [IssueCandidates]
# → 报 `record "IssueCandidates" not found among call inputs`

# ✅ 对：用 from 指定同 team 内的产出方
acquire_slot:
  depends_on: [search]
  inputs:
    records:
      - from: search              # 同 team 内的 call 名
        record: IssueCandidates
```

`records` 形式会沿 `previous`（同 team 内前序 call 的结果）查找，
这正是"team 内顺序依赖"的正确表达。

### 完整示例

```yaml
acquire_slot:
  type: command
  depends_on: [search]
  command:
    command: "bash .agents/skills/xxx/scripts/acquire.sh"
    env:
      HERON_FLOW_SESSION_ID: "${flow_session_id}"
      AUTO_REPO: "${records.IssueCandidates.Data.repo}"   # ← 从上游记录取
  inputs:
    records:                                 # ← 必须声明，否则找不到该 record
      - from: search
        record: IssueCandidates
  output:
    record: WorktreeSlot
```

**三个必须注意的点**：

1. **`inputs` 必须声明被引用的 record**，否则报
   `record "X" not found among call inputs`（注意选对写法，见上）。
2. **取不存在的字段会让 call 直接失败**，不是静默传空：
   `record "X" has no data field "y"`。这是有意设计——传空值比报错更危险。
3. **只能取 `Data` 的顶层字段**。`${records.X.Data.a.b}` 会报
   `too deep (expected <Name>.Data.<field>)`。不同产出方的 `Data` 形态不同：

   | 产出方 | `Data` 顶层 | 取业务值的方式 |
   |---|---|---|
   | **agent** | agent 在 schema 里声明的字段（**已展开到顶层**） | 直接引 `${...Data.<schema字段>}` |
   | **command** | `{exit_code, stdout, stderr, passed}` | 业务 JSON 在 `stdout` 字符串里，引 `${...Data.stdout}` 后自行解析 |

   值是非标量（map / array）时，引擎以 **JSON** 形式传递
   （`stringifyRecordValue` 用 `json.Marshal`），消费方需自行 `jq` 解析。

> **command 输出的 `Data` 是什么？** 视 executor 而定。以 `acquire.sh` 为例，
> `WorktreeSlot.Data` 是 `{exit_code, passed, stderr, stdout}`——脚本的 JSON 结果在
> `stdout` **字符串**里，**没有**展开成 `Data.abs_path` 这样的字段。引用前先确认
> 该 record 的 `Data` 实际键，可用
> `jq -r 'select(...).payload.record.data | keys' flow.jsonl` 查。

---

## Agent（`.agents/agents/<id>/AGENT.md`）

frontmatter 分组：

### persona
| 字段 | 含义 |
|---|---|
| `role` | 角色名 |
| `goal` | 主要目标 |
| `backstory` | 背景 |

### model（未配置的字段从 models.json 继承）
| 字段 | 含义 |
|---|---|
| `model` | 模型名（**直接写模型 id，勿用 `${ENV}` 占位符**） |
| `temperature` | 覆盖默认温度 |
| `top_p` / `top_k` / `repetition_penalty` | 覆盖采样参数 |
| `reasoning` | 覆盖推理配置 |
| `max_output_tokens` | 覆盖单次输出上限 |
| `api_key` / `base_url` | 特殊场景覆盖密钥/地址 |

### tools
| 字段 | 含义 |
|---|---|
| `builtin` | 内置工具：Read/Write/Edit/Grep/Glob/Bash/WebSearch/WebFetch/CodeNav/AskUserQuestion/TodoWrite/TodoRead/Spawn/Collect/State/Define |
| `custom` / `mcp` | 自定义 / MCP 工具 |

> `Spawn`、`Collect`、`State`、`Define` 都是**按需开启**的工具：不写进
> `builtin` 就既不会提供给模型，调用也会被允许清单拦下。`Define` 的语义
> 见 `define.md`。

### loop
| 字段 | 含义 |
|---|---|
| `max_rounds` | 单次执行最大模型轮次 |
| `tool_execution` | `sequential` / `parallel_safe` |
| `max_parallel_tools` | 最大并发只读工具数 |
| `async_tools` | 允许创建持久异步任务的工具 |
| `timeout` | 执行超时 |

### context（上下文窗口控制）
| 字段 | 含义 |
|---|---|
| `target_ratio` | 目标 Active Context 比例（默认 0.70） |
| `compaction_threshold` | 触发压缩的比例（默认 0.80） |
| `hard_limit_ratio` | 硬上限比例（默认 0.90） |
| `output_reserve_ratio` | 为输出预留的比例（默认 0.15） |
| `tool_output_ratio` | 工具输出预算比例（默认 0.10） |
| `max_tool_output_chars` | 单条工具结果字符上限 |

### budget（资源预算，0 = 不限制）
| 字段 | 含义 |
|---|---|
| `max_model_rounds` | 最大模型调用数 |
| `max_tool_calls` | 最大工具调用数 |
| `max_wall_time` | 最大墙钟时间（如 `10m`） |
| `max_input_tokens` / `max_output_tokens` | 累计 token 上限 |
| `max_file_changes` | 最大 workspace 写操作数 |
| `max_tool_output` | 累计工具输出字符上限 |

### 其他
| 字段 | 含义 |
|---|---|
| `structured_output` | `{type, schema}` 结构化输出契约 |
| `hitl` | `{enabled, timeout}` 人工审批 |
| `hooks` | `{event, command, timeout}` 生命周期钩子 |
| `handoffs` | 可委派任务的其他 agent 列表 |
| `skills` / `knowledge` / `rules` | 引用的 skill / 知识 / 规则 |

---

## SharedRecord（跨层数据契约）

- `output.record` 声明产出名（如 `DiagnosisReport`）。
- 下游用 `inputs` 显式声明要读的 record：
  ```yaml
  inputs:
    - from: research          # 上游 call 名
      record: ResearchReport  # 上游产出的 record 名
  ```
- **record 名称是自由字符串，无 schema 校验**——拼写必须与上游 `output.record` 完全一致，否则下游静默收不到。
- 调用不会自动读全部历史；需要什么就在 `inputs` 里声明什么。

## rules（`.agents/rules/<name>.md`）

```markdown
---
type: hard        # hard 强制 / soft 建议
scope:
  type: all       # all / team / agents
priority: 10
---
规则正文。
```

## 运行时事件字段（排查用）

配置字段是"你写进去的"，运行时字段是"引擎吐出来的"。后者落在
`.agents/data/sessions/<fs_id>/*.jsonl`，排查时按下列字段读取。
事件按 run 层级分流：**`flow.jsonl`（flow + team turn 层）、`team.jsonl`（call 层）、
`agent.jsonl`（agent 内部）**。读取方法见 `session-files.md`；串联用 `trace-ids.md`。

### 每行事件共有字段

| 字段 | 含义 |
|---|---|
| `seq` | 全局自增序号，判先后用它，不要用行号 |
| `type` | 事件类型，如 `flow_turn.started`、`agent_turn.completed` |
| `flow_session_id` | 所属 Flow 会话，即目录名 `fs_xxx` |
| `flow_turn_id` / `team_turn_id` | 轮次 ID，跨三层关联用 |
| `team_id` | **flow 局部名**（如 `default` / `diagnose`），不是 team 定义 id（`xxx_team`） |
| `call_id` / `call_turn_id` | call 名与其轮次 ID |
| `call_type` | `agent` / `command` / `webhook` |
| `attempt` | 第几次尝试（重试/恢复会 +1） |
| `created_at` | RFC3339 时间戳 |
| `payload` | 该事件的具体内容，字段见下 |

### 关键 payload 字段

| 位置 | 字段 | 含义 |
|---|---|---|
| `flow_turn.started` | `payload.turn.input` | **本轮用户输入原文** |
| `flow_turn.*` | `payload.turn.status` | `running` / `waiting_input` / `failed` / `cancelled` |
| `flow_turn.*` | `payload.session.status` | 会话级状态，正常结束为 `waiting_input` |
| `team_turn.completed` | `payload.team_result.Turn.next.action` | 路由：`proceed` / `activate` / `return` / `coordinate` / `fail` |
| `team_turn.completed` | `payload.team_result.Turn.status` | team turn 状态 |
| `team_turn.completed` | `payload.team_result.Reply` | 该 team 的最终文本 |
| `team_turn.completed` | `payload.team_result.Records[]` | 该 team 发布的 record |
| `team_turn.completed` | `payload.team_result.CallResults[]` | 各 call 的结果 |
| `agent_turn.completed` | `payload.call_result.Status` | call 状态（非 `completed` 即要看 `Error`） |
| `agent_turn.completed` | `payload.call_result.Requests[]` | **消耗事实源**，每个元素 = 一轮模型调用 |
| `agent_turn.completed` | `payload.call_result.Requests[].usage` | `prompt_tokens` / `completion_tokens` |
| `agent.model_response` | `payload.text` | **模型本轮回复原文**（不是 `content`） |
| `agent.model_response` | `payload.round` / `tool_calls` / `finish_reason` | 第几轮 / 工具调用 / 停止原因 |
| `tool_call.started` / `.completed` / `.failed` | `payload.tool_name` / `arguments` / `round`；`completed` 另有 `content`（结果）、`matches` 等 | 工具调用详情 |
| `agent.feedback` | `payload.content`（+ 结构化重试时的 `payload.raw_text`） | 引擎追加的内部提示（如结构化重试）；`raw_text` 为被丢弃的模型原文 |
| `shared_record.published` | `payload.record` | 发布的 record 全文 |
| `evidence.jsonl` 每行 | `summary` / `basis[]` | 成品文本 / 依据的文件与行号 |

> **大小写**：`payload.team_result` 下，叶子类型用小写（`Turn.next`、
> `Turn.record_ids`、`Turn.team_id`），容器结构体用 Go 字段名大写（`Next`、
> `Records`、`CallResults`、`Requests`、`Usage`）。这是已发布契约，不是笔误。
>
> **轮次**：一次模型/工具循环 = `Requests[]` 里一个元素；`agent.model_response`
> 的 `round` 与之对应。
