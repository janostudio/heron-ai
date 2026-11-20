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
| `lifecycle` | string | `resident`（默认，整个 FlowSession 一个 TeamSession）/ `per_task`（每个任务一个）。**entry team 禁止 `per_task`**；`per_task` 必须同时声明 `inputs.user_message: true`，否则加载期报错 |
| `inputs` | object/list | 用户消息 + SharedRecord 输入 |
| `on_proceed` | list/object | Team 以 `proceed` 结束后的固定路由 |

> 绑定里**不再有 `coordinator` 键**：Team 是平权的，谁是入口由 Flow 的 `entry` 决定，
> 结果回到激活它的 Team。老配置写了这个键会在加载期直接报错并给出替代写法，
> 不会被静默忽略（静默忽略会悄悄改变路由）。

---

## workspace（工作区，Flow / Team / Agent 三级继承）

| 字段 | 含义 |
|---|---|
| `type` | `local`（默认）/ `ssh` |
| `ssh` | 远程工作区配置（`host` / `user` / `key` 等） |
| `read_roots` | **只读扩展根**：工作区根之外的目录，文件工具放行读取，写仍然拒绝 |

- 工作区根是 heron 进程的 cwd，也就是 `.agents/` 的**上一级**。
- 继承：Agent 覆盖 Team，Team 覆盖 Flow；都没写则用进程 cwd 作为 local 根。
- `read_roots` 的相对路径按**进程 cwd** 解析，不是按配置文件所在目录。换部署位置后条目会失效，失效条目被**跳过**（不会导致启动失败），代价是那一刻读不到该目录。
- 只读根只对读取类工具有效（`Read` / `Grep` / `Glob` / `CodeNav` 与目录遍历）；`Write` / `Edit` 始终被限制在工作区根内，不放行。

典型用法——让 agent 读到工作区之外的代码库：

```yaml
workspace:
  type: local
  read_roots:
    - ../repos
    - ../shared-config
```

---

## Team（`.agents/teams/<name>.yml`）

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | string | Team 标识 |
| `goal` | string | Team 目标 |
| `calls` | map | 执行项集合 |
| `inputs` | object/list | Team 级输入（用户消息 / SharedRecord），语义同 Call 的 `inputs` |
| `output` / `outputs` | object | 对外发布的 SharedRecord（`from` + `record` + `scope`；`outputs.records[]` 是一次发多条） |
| `state` | object | 可选 Team State 扩展（`enabled`/`max_items`/`max_chars`/`inject_summary`） |
| `workspace` | object | Team 级工作区覆盖（见 workspace 节） |

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
| `webhook.method` / `webhook.headers` | string / map | （type=webhook）请求方法与请求头；`webhook.timeout` 同上 |
| `command.idempotency_key` / `webhook.idempotency_key` | string | 重放去重键。`replay_policy: idempotent` 时**必填**，缺了加载期报错 |
| `command.replay_policy` / `webhook.replay_policy` | string | 恢复/重跑时能否重放：`never` / `idempotent`（配合去重键）/ `allow` |

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
# ❌ 错：TaskCandidates 此刻还是 team scope，不在 Flow 级集合里
acquire_slot:
  depends_on: [search]
  inputs:
    flow_records: [TaskCandidates]
# → 报 `record "TaskCandidates" not found among call inputs`

# ✅ 对：用 from 指定同 team 内的产出方
acquire_slot:
  depends_on: [search]
  inputs:
    records:
      - from: search              # 同 team 内的 call 名
        record: TaskCandidates
```

`records` 形式会沿 `previous`（同 team 内前序 call 的结果）查找，
这正是"team 内顺序依赖"的正确表达。

### 完整示例

```yaml
acquire_slot:
  type: command
  depends_on: [search]
  command:
    command: "bash .agents/skills/xxx/scripts/build.sh"
    env:
      HERON_FLOW_SESSION_ID: "${flow_session_id}"
      BUILD_TARGET: "${records.TaskCandidates.Data.repo}"   # ← 从上游记录取
  inputs:
    records:                                 # ← 必须声明，否则找不到该 record
      - from: search
        record: TaskCandidates
  output:
    record: BuildResult
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

> **command 输出的 `Data` 是什么？** 视 executor 而定。以 shell 脚本 call 为例，
> `BuildResult.Data` 是 `{exit_code, passed, stderr, stdout}`——脚本的 JSON 结果在
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
| `model` | **模型 profile 的 `name`**（models.json 里按 `name` 查表，不是 `id`）；**勿用 `${ENV}` 占位符**，那里不展开 |
| `temperature` | 覆盖默认温度 |
| `top_p` / `top_k` / `repetition_penalty` | 覆盖采样参数 |
| `reasoning` | 覆盖推理配置 |
| `max_output_tokens` | 覆盖单次输出上限 |
| `api_key` / `base_url` | 特殊场景覆盖密钥/地址 |

### tools
| 字段 | 含义 |
|---|---|
| `builtin` | 内置工具：Read/Write/Grep/Glob/Bash/WebSearch/WebFetch/CodeNav/AskUserQuestion/TodoWrite/TodoRead/Spawn/Collect/ListTeams/Team/TeamSession/State/Define（另有 `REPL`、`ScheduleWakeup`/`CancelWakeup` 见下） |
| `custom` / `mcp` | 自定义 / MCP 工具 |

> 没有 `Edit` 工具：改文件用 `Read` → `Write`（整文件覆盖）。写错工具名**不会在加载期报错**，
> 只是那个工具对模型不可见 —— 排查"模型说没有某能力"时先核对这个名字。

> `Spawn`、`State`、`Define` 是**按需开启**的工具：不写进 `builtin` 就既不会
> 提供给模型，调用也会被允许清单拦下。`Define` 的语义见 `define.md`。
>
> `Collect` 是例外：**声明了 `Team` 或 `Spawn` 时，引擎自动授予 `Collect`**（放入
> 允许清单并暴露 schema）。派发了子任务就必须能收结果，不需要手写。
>
> 这个例外来自一个真实的失败模式：只声明 `Team` 时，引擎回流结果会以模型名义写下一对
> `Collect` 调用 + 结果；模型在转录里看到"自己调用过"，就跟着再调一次，而允许
> 清单拒绝了它，于是模型反复重试直到轮次耗尽，那一轮没有任何答复。手动写
> `Collect` 现在无害但不是必需。
>
> `WebSearch` / `WebFetch` 的数据源不在 Agent 层配，统一在 `.agents/settings.json`
> 的 `web` 段。默认 provider `duckduckgo` 免 key，空配置即可搜；常见写法是
> `{"web": {"api_key": "..."}}` —— **但这不会切换 provider**，`duckduckgo` 免 key，
> 这个 key 会被忽略。真要用 TinyFish 必须同时写 provider：
> `{"web": {"provider": "tinyfish", "api_key": "..."}}`。
>
> provider 对照：`duckduckgo`（默认，仅 search，免 key）、`tinyfish`（search+fetch，
> 要 key）、`brave`（仅 search，要 key）、`tavily`（search+fetch，要 key）、
> `direct`（仅 fetch，本地直连，免 key）。`provider` 管 search，fetch 默认跟随；
> `brave`/`duckduckgo` 没有 fetch API，自动落 `direct` 本地直连（不报错），
> 写 `fetch.provider: direct` 可强制本地抓取。
>
> api_key 三级来源：`web.api_key` 明文 > 环境变量 `WEB_SEARCH_API_KEY` > `env` 段
> 同名变量（变量名刻意不带厂商名，provider 和 key 配套整段换）。fail-closed 按
> provider 判定：只有 tinyfish/brave/tavily 缺 key 时报错且不发请求，duckduckgo
> 和 direct 免 key 放行。注意 DuckDuckGo 走的是 Instant Answer API，只答百科/实体
> 类查询，普通搜索词（如 `best web search api`）返回全空，要真搜索结果得配 key。
> 私网 URL 自动降级本地直连、不外发（降级与「是否允许抓私网」是两件事，后者看
> `AllowPrivateHosts`）。

### loop
| 字段 | 含义 |
|---|---|
| `max_rounds` | 单次执行最大模型轮次 |
| `tool_execution` | `sequential` / `parallel_safe` |
| `max_parallel_tools` | 最大并发只读工具数 |
| `async_tools` | 允许创建持久异步任务的工具 |
| `max_same_tool_calls` | 同一个工具同参数最多连续几次就判定卡住 |
| `max_no_progress_rounds` | 连续多少轮没有进展就判定卡住 |
| `max_same_model_texts` | 连续多少轮模型输出完全一致就判定卡住 |
| `max_repeated_reads` | 同一个只读调用（同参数）最多重复几次；**写操作之后的重复不计**（那是改-验循环，不是卡住） |
| `stuck_action` | 判定卡住后怎么办：`fail` / `ask_user` |
| `max_model_retries` | provider 瞬时失败的重试次数；上下文超限恢复另算，只尝试一次 |
| ~~`timeout`~~ | 已移除：agent turn 由轮次与工具调用数约束，不看墙钟；写了会加载报错 |

### context（上下文窗口控制）
| 字段 | 含义 |
|---|---|
| `target_ratio` | 压缩后目标 Active Context 比例（默认 0.45） |
| `compaction_threshold` | 触发压缩的比例（默认 0.90） |
| `output_reserve_ratio` | 为输出预留的比例（默认 0）。**硬限就是 `1 - 这个值`**，没有单独的配置项 |
| `tool_output_ratio` | 工具输出预算比例（默认 0.10） |
| `max_tool_output_chars` | 单条工具结果字符上限 |
| `max_input_tokens` | 显式覆盖模型上下文容量（不配则用 model profile 的预算） |
| `microcompact_threshold_chars` / `microcompact_max_chars` | 单条工具结果超阈值时截断到多少字符（默认 8192 / 4096） |
| `recent_message_groups` | 压缩时保留最近几组消息原文（默认 2） |
| `compactor` | 压缩器：`llm`（默认，摘要调用）/ `mechanical`（不调模型，纯裁剪） |

> 默认值刻意"晚压、压狠"（90% 才压，压到 45%）：每次压缩是一次摘要调用加一次
> 缓存重置，频繁小碎压的固定成本远高于一次压到位。

### completion（完成证据，默认全关）
| 字段 | 含义 |
|---|---|
| `require_tool` / `required_tools` | 必须调用过工具 / 必须调用过其中某个工具 |
| `require_tool_success` | 工具调用必须成功 |
| `require_workspace_read` / `require_workspace_change` | 必须读过工作区 / 必须改过工作区 |
| `require_structured_output` | 必须产出符合 `structured_output` 的结构化结果 |

### budget（资源预算，0 = 不限制）
| 字段 | 含义 |
|---|---|
| `max_model_rounds` | 最大模型调用数 |
| `max_tool_calls` | 最大工具调用数 |
| `max_input_tokens` / `max_output_tokens` | 累计 token 上限 |
| `max_file_changes` | 最大 workspace 写操作数 |
| `max_tool_output` | 累计工具输出字符上限 |

### 其他
| 字段 | 含义 |
|---|---|
| `structured_output` | `{type, schema, max_output_tokens, enforce_provider_format}` 结构化输出契约 |
| `hitl` | `{enabled, timeout}` 人工审批 |
| `hooks` | **列表**，每项 `{event, command, timeout}`；写成单个映射会解码失败 |
| `skills` / `rules` | 引用的 skill / 规则 |
| `knowledge` | 声明的知识条目名（加载期校验存在性；**不产生注入内容**，见下） |

> **`skills` 的实际效果**：声明后 system prompt 里只多一行**索引**
> （`- <name> — <description>` + `read: <SKILL.md 路径>`），正文**不进** prompt；
> 模型判断任务匹配时才用 `Read` 打开那个路径。所以正文改了**立即生效**，代价是模型
> 多一次读文件调用。但 `tools` / `allowed-tools` 是**声明即授予**——工具定义会常驻
> 上下文，只声明真正需要的 skill。正文长的拆到 `references/`，正文只留纲要与指向。
>
> **`knowledge` 的实际效果**：运行时只往提示词里注入一个**目录指路块**（该 agent
> 可读哪些知识目录），不注入条目内容。读知识就是 `Glob` 定位 + `Read` 打开。
> 因此 `knowledge:` 列表写错条目名不会让 agent 少读什么，它该看的是目录里有什么。
> 知识目录有三处：`.agents/knowledge/`（全 Flow 共享）、
> `.agents/teams/<team>/knowledge/`（Team 私有）、`.agents/agents/<agent>/knowledge/`
> （Agent 私有）。

---

## SharedRecord（跨层数据契约）

- `output.record` 声明产出名（如 `DiagnosisReport`）。
- 下游用 `inputs` 显式声明要读的 record：
  ```yaml
  inputs:
    - from: research          # 上游 call 名
      record: ResearchReport  # 上游产出的 record 名
  ```
- **record 名称是自由字符串，schema 里没有枚举**——拼写必须与上游 `output.record` 完全一致，否则下游静默收不到。
- 加载期会校验引用：`inputs` 里引用的 record 必须有人生产（存在对应的 `output.record`），`command` 里 `${records.X...}` 的 X 必须写在同一 call 的 `inputs` 里。写错在加载时就报错，不再等到运行时变成"缺字段"。
- 调用不会自动读全部历史；需要什么就在 `inputs` 里声明什么。

**作用域与可见性**

| scope | 含义 |
|---|---|
| `team`（默认） | 产出它的 Team 自己用 |
| `flow` | 整个 Flow 可见，下游按名就能匹配到 |

- 被派发（通过 `Team` 工具调用起来）的子 Team，产出的记录会以 **flow 作用域的副本**写入 `evidence.jsonl`。这样调用方的 `flow_records` 才能按名读到它——否则子 Team 的结论只能留在它自己那一层。
- 时间窗：被工具结果唤醒的回合只读**派发那一刻之后**产生的记录，上一轮问题的结论不会被当成本轮事实。窗口起点是派发，不是"收到结果"。
- 想确认某条记录到底被谁消费，读 `session.record.consumed` 事件（见下文运行时事件字段）。

## models.json（`.agents/models.json`）

**唯一必需文件**：缺它或 `models` 为空，启动直接失败（`load .agents/models.json` /
`models.json has no models`）。

```json
{
  "model": "gpt-4o-mini",
  "models": [
    {
      "id": "gpt-4o-mini",
      "name": "gpt-4o-mini",
      "protocol": "openai_chat",
      "base_url": "http://127.0.0.1:15721/v1",
      "api_key": "${OPENAI_API_KEY}",
      "maxInputTokens": 192000,
      "maxOutputTokens": 64000
    }
  ]
}
```

| 字段 | 含义 |
|---|---|
| `model` | 默认模型，按条目的 **`name`** 匹配；空则用第一条 |
| `name` | **匹配键、显示名，也是 Agent `model.model` 要写的值**。每个 profile 都写它 |
| `id` | 发给 API 的 wire model（`model` 字段）；空则用 `name` |
| `protocol` | `openai_chat`（默认）/ `anthropic_messages`；不认识的值按 openai 处理 |
| `base_url` | Provider 地址 |
| `models_url` | 可选，`/models` 探测地址（Web UI / 校验用） |
| `api_key` | 直接写值，或写 `${ENV_VAR}` 由引擎展开 |
| `maxInputTokens` | 上下文预算，压缩策略据此计算 |
| `maxOutputTokens` | 单次输出上限，缺省走协议默认 |
| `stream` | 该 provider 需要流式调用；为 `true` 时把 SSE 分片聚合成一个响应 |
| `temperature` / `top_p` / `top_k` / `repetition_penalty` | 采样默认值，缺省则不发送 |
| `reasoning` / `onlyReasoning` | Provider-neutral 推理配置 / 只要推理不要正文 |
| `supports*` | 能力声明（`supportsImages` / `supportsToolCall` / `supportsStructuredOutput` 等），按需显式声明 |
| `fallback` | 该模型遇到可重试错误（限流 / 5xx / 超时 / 网络）时按顺序尝试的备用模型名 |
| `cooldown_seconds` | 失败模型作为被动兜底被跳过的时长，默认 600 秒 |

> **`maxAllowedSize` 是无效字段**：结构体里有声明，但没有任何代码读取它。
> 上下文预算看 `maxInputTokens`。

`api_key` 的两级来源与陷阱：

- 写 `${VAR}` 时由引擎展开；**展开为空会直接报 `API key ... is not set`**，不会退回
  其他变量（防止把 A 厂商的 key 发给 B 厂商）。
- 整段省略时按协议读 `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`。
- Agent 的 `model.model` 写 profile 的 **`name`**（不是 `id`），**不要写 `${ENV}`** —— 那里不做展开。

## settings.json（`.agents/settings.json`）

可选，缺文件即全默认。**只有下面这些段有代码读取**：

| 段 | 作用 | 关键项 |
|---|---|---|
| `logging` | 执行日志 | `level`（`debug` 才有 `model request usage`）、`dir`、`max_file_size`、`max_backups`（0=不限）、`retention_days`；日志只写文件，没有 stdout 分支 |
| `runtime` | 编排循环安全上限 | `max_team_turns`(100，**每个 FlowTurn**)、`max_calls_per_team_turn`(100)、`max_agent_rounds`(200)、`max_parallel_*`(20)、`max_coordinate_retries`(20)、`max_active_team_sessions`(8)、`max_task_context_depth`(3) |
| `knowledge` | 知识总结模型 | `summary_model`（空 = 默认模型） |
| `web` | WebSearch / WebFetch 数据源 | 见上文 tools 节的 provider 对照 |
| `mcp` | MCP server 列表 | `name` / `transport`(`stdio`\|`http`) / `command`+`args` / `url`+`headers` / `env` |
| `tools` | 工具策略 | `restricted_bash_phrases`（只增不减）、`repl.*`（默认关闭；开启后 agent 还必须配 `hitl.enabled`——整个脚本是人工审批的粒度） |
| `env` | 注入所有子进程的环境变量 | **每个值都按路径解析**，相对**工作区根**（进程 cwd）：`"."` = 根，`".."` = 上一级。非路径值会被拼成路径（`"MODE": "prod"` → `<cwd>/prod`），这个段只适合放路径锚点 |

**不生效的段**（老配置里常见，调了也没用）：

| 段 | 为什么 |
|---|---|
| `logging.output` | `buildLogger` 只传 level/dir/轮转参数，`output` 被丢弃；日志永远写 `dir` |
| `paths.data` | 数据目录硬编码 `.agents/data/` |
| `agent.max_parallel` / `agent.tracing` / `agent.default_loop` | 没有全局 agent 段；单 agent 限制在 `AGENT.md` 的 `loop`，全局在 `runtime.max_agent_rounds` |
| `observability` | 包已删除；`logging.retention_days` 才是生效的那个 |

`runtime` 的上限是**防跑飞的兜底**，不是编排旋钮：合法的 Flow 会来回移交很多次，
默认值刻意远高于任何正常计数。要更早失败才调低。

## rules（`.agents/rules/<name>.md`）

```markdown
---
type: hard        # hard 强制 / soft 建议
scope:
  type: all       # all / team / agents
  # type: team 时必须配 teams: [<team 局部名>]
  # type: agents 时必须配 agents: [<agent id>]
priority: 10
paths:            # 可选：条件触发，命中这些 glob 才注入；不写=常驻
  - "internal/**/*.go"
---
规则正文。
```

- `scope.type` 写 `team` / `agents` 却不配对应的名单，规则**永远不命中**，且加载期
  不报错——排查"规则没生效"先看这里。注意是复数 `agents`，不是 `agent`。
- 正文是渲染时按需读的：改规则文件不需要重启（skill 正文现在也是按需读，见上）。
- `priority` 越大越靠前。

## 运行时事件字段（排查用）

配置字段是"你写进去的"，运行时字段是"引擎吐出来的"。后者落在
`.agents/data/sessions/<fs_id>/*.jsonl`，排查时按下列字段读取。
事件按 run 层级分流：**`flow.jsonl`（flow + team turn 层）、`team.jsonl`（call 层）、
`agent.jsonl`（agent 内部）**。读取方法见 `session-files.md`；串联用 `trace-ids.md`。

### 每行事件共有字段

| 字段 | 含义 |
|---|---|
| `seq` | 全局自增序号，判先后用它，不要用行号 |
| `type` | 事件类型，如 `session.turn.started`、`session.agent_turn.completed` |
| `flow_session_id` | 所属 Flow 会话，即目录名 `fs_xxx` |
| `flow_turn_id` / `team_turn_id` | 轮次 ID，跨三层关联用 |
| `team_session_id` | Team 会话 ID；`per_task` 生命周期下同一个 Team 会有多个 |
| `recovery_of` | 本事件是恢复哪次尝试（`attempt` 会随重试 +1） |
| `team_id` | **flow 局部名**（如 `default` / `diagnose`），不是 team 定义 id（`xxx_team`） |
| `call_id` / `call_turn_id` | call 名与其轮次 ID |
| `call_type` | `agent` / `command` / `webhook` |
| `attempt` | 第几次尝试（重试/恢复会 +1） |
| `created_at` | RFC3339 时间戳 |
| `payload` | 该事件的具体内容，字段见下 |

### 关键 payload 字段

| 位置 | 字段 | 含义 |
|---|---|---|
| `session.turn.started` | `payload.turn.input` | **本轮用户输入原文** |
| `session.turn.started` | `payload.turn.origin` | 这轮怎么被唤醒：`user`（用户提问）/ `mailbox` / `wakeup` / `resume` / `tool`。**只有 `user` 才是用户提问**，一条用户消息会派生多个 turn |
| `session.turn.ended` | `payload.stop_reason` | **怎么结束的**：`end_turn` / `max_rounds` / `max_tool_calls` / `max_tokens` / `cancelled` / `refusal` / `failed` |
| `session.turn.ended` | `payload.awaiting` | **在等谁**：`user` / `tool` / `approval`；空表示不再等任何人。等工具、等审批不是失败 |
| `session.turn.ended` | `payload.session.status` | 会话级状态：等用户 → `idle`，等工具/审批 → 对应等待态 |
| `flow_turn` 终态 | `payload.reply` | 这一轮给用户的答复（`intermediate=true` 时为空） |
| `flow_turn` 终态 | `payload.team` | **写这条答复的 Team**（`origin` 只说明这轮怎么被唤醒，不是归属） |
| `flow_turn` 终态 | `payload.contributors[]` | 这轮里每个 Team 说了什么、谁请求的——答复是复述时用它还原来源 |
| `flow_turn` 终态 | `payload.deliveries[]` | 必须作为独立消息送达用户的结果：`message_id` / `team_id` / `text` |
| `flow_turn` 终态 | `payload.intermediate` | `true` 表示这是内部进度而不是答案（`reply` 会被清空） |
| `flow_turn` 终态 | `payload.limit` | 撞到兜底上限时的结构化事实：`name`（哪条上限）/ `value` / `observed` / `top_pairs`（最频繁的激活边）/ `recent`。比解析 `error` 字符串可靠 |
| `session.turn.ended` | `payload.stop_reason=cancelled` + `payload.error` | 进程中断留下的悬空回合，在**下一个回合开始时**被补写为这个终态（`intermediate=true`），不属于本次运行的失败 |
| `session.team_turn.ended` | `payload.team_result.turn.next.action` | 路由：`proceed` / `activate` / `return` / `coordinate` / `fail` / `wait_tool` / `wait_approval`（后两个是"等工具或审批"，不是失败） |
| `session.team_turn.ended` | `payload.team_result.turn.stop_reason` / `.awaiting` | team turn 的终态两维，语义与 flow 层一致；同名字段也在 payload 顶层 |
| `session.team_turn.ended` | `payload.team_result.reply` | 该 team 的最终文本 |
| `session.team_turn.ended` | `payload.team_result.records[]` | 该 team 发布的 record |
| `session.team_turn.ended` | `payload.team_result.call_results[]` | 各 call 的结果 |
| `session.agent_turn.completed` | `payload.call_result.status` | call 状态（非 `completed` 即要看 `error`） |
| `session.agent_turn.completed` | `payload.call_result.requests[]` | **消耗事实源**，每个元素 = 一轮模型调用 |
| `session.agent_turn.completed` | `payload.call_result.requests[].usage` | `prompt_tokens` / `completion_tokens` / `total_tokens`；缓存看 `prompt_cache_hit_tokens` 与 `cache_read_input_tokens`（不同 provider 命名不同，**取两者较大值，不可相加**） |
| `session.agent_turn.completed` | `payload.spawn` | 子 agent 回合标记：`{agent, key, parent_call_id}`。以 `deliver=downstream` 派发的子任务会作为合成 call 计入父 `team_result.usage`；同步 inline spawn 不计 |
| `session.context.compacted` | `payload.summary` / `payload.dropped_count` | 这一轮发生了上下文压缩：摘要文本 + 丢掉了多少条 |
| `session.agent.model_response` | `payload.text` | **模型本轮回复原文**（不是 `content`） |
| `session.agent.model_response` | `payload.round` / `tool_calls` / `finish_reason` | 第几轮 / 工具调用 / 停止原因 |
| `session.tool_call.started` / `.completed` / `.failed` | `payload.tool_name` / `arguments` / `round`；`completed` 另有 `content`（结果）、`matches` 等 | 工具调用详情 |
| `session.agent.feedback` | `payload.content`（+ 结构化重试时的 `payload.raw_text`） | 引擎追加的内部提示（如结构化重试）；`raw_text` 为被丢弃的模型原文 |
| `session.record.published` | `payload.record` | 发布的 record 全文 |
| `session.record.published` | `payload.record.producer` | 产出者：`team_id` / `call_id` / `call_turn_id` / `team_turn_id`（**顶层 `team_id` 为空，归属看这里**） |
| `session.record.published` | `payload.record.scope` | `team`（默认，产出者自用）/ `flow`（整个 flow 可见） |
| `session.record.consumed` | `payload.record` + `payload.consumer_team` | **某条 record 被交给了谁**（Flow 侧的移交事实，比从文本推断可靠） |
| `evidence.jsonl` 每行 | `summary` / `basis[]` | 成品文本 / 依据的文件与行号 |

> **字段命名**：三层 jsonl 的字段名统一 snake_case，包括 `team_result` / `call_result`
> 这一层（`turn` / `reply` / `records` / `call_results` / `requests` / `usage`）。
> Go 代码里的标识符是大驼峰，但那只影响代码，不影响落盘格式。已发布契约，改动是破坏性变更。
>
> **轮次**：一次模型/工具循环 = `Requests[]` 里一个元素；`session.agent.model_response`
> 的 `round` 与之对应。
