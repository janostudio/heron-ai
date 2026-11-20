# 配置字段字典

查名字、类型、默认值、档位用这一份；**不要按这份逐项配**。档位含义与"为什么不配"见
`../SKILL.md` §2，具体旋钮的触发条件见 `tuning.md`。

| 档 | 含义 |
|---|---|
| 必 | 不写跑不起来或跑错对象 |
| 选 | 默认够用；有明确目标才写 |
| 别 | 默认即设计；写了会缩小能力 |
| 禁 | 已移除或不存在；写已移除的加载报错，自造键静默丢弃 |

权威来源是 `docs/configuration/*.md`；本文件与它冲突时以代码为准并回报。

---

## 编排层次

```text
Flow → Team 绑定 → Call(agent | command | webhook) → Agent
```

`Call` 是执行项，不是层级。Team 平权：没有 coordinator，入口由 `entry` 决定。

## Flow（`.agents/flows/<name>.yml`）

| 档 | 字段 | 类型 | 含义 |
|---|---|---|---|
| 必 | `id` | string | Flow 标识 |
| 必 | `entry` | string | 入口 Team 的 Flow 局部名；写错 → `entry team "x" not found` |
| 必 | `teams` | map | Team 绑定表，key 为 Flow 局部名 |
| 选 | `workspace` | object | 工作区覆盖（见 workspace 节） |

### Team 绑定（`teams` 下每个条目）

| 档 | 字段 | 含义 |
|---|---|---|
| 必 | `team` | 指向 `.agents/teams/` 下的真实 Team id |
| 选 | `can_activate` | 本绑定可激活的 Team。**不写 = 谁也不能激活**；被激活者结束后回到激活者 |
| 选 | `depends_on` | 前置 Team（完成后本 Team 才执行） |
| 选 | `lifecycle` | `shared`（默认，一个 FlowSession 一个 TeamSession）/ `isolated`（每任务一个）。**entry 禁止 isolated**；`isolated` 必须同时 `inputs.user_message: true`，否则加载报错 |
| 选 | `inputs` | 用户消息 + SharedRecord 输入 |
| 选 | `on_proceed` | Team 以 `proceed` 结束后的固定路由 |
| 禁 | `coordinator` | 已移除；写了加载报错并给出替代写法 |

## workspace（Flow / Team / Agent 三级继承）

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `type` | `local`（默认）/ `ssh` |
| 选 | `ssh` | 远程工作区（`host` / `user` / `key` …） |
| 选 | `read_roots` | 只读扩展根：根外目录对 `Read`/`Grep`/`Glob`/`CodeNav` 放行，写仍拒绝 |

- 工作区根 = 进程 cwd = `.agents/` 的上一级；继承顺序 Agent > Team > Flow。
- `read_roots` 相对**进程 cwd** 解析，不按配置文件位置；条目失效被**跳过**（不报错），代价是读不到。
- **Bash 不受路径限制**（它是 `shell -c`，无法约束）——所以"放行读取"不等于"限制执行"。

```yaml
workspace:
  type: local
  read_roots:
    - ../repos
    - ../shared-config
```

## Team（`.agents/teams/<name>.yml`）

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `id` | Team 标识；不写用文件名 |
| 选 | `goal` | Team 目标（进提示词） |
| 必 | `calls` | 执行项集合；没有 call 的 team 跑不起来 |
| 选 | `inputs` | Team 级输入，语义同 Call |
| 选 | `output` / `outputs` | 对外发布 SharedRecord（`from` + `record` + `scope`） |
| 选 | `state` | Team State 扩展（`enabled`/`max_items`/`max_chars`/`inject_summary`） |
| 选 | `workspace` | Team 级工作区覆盖 |

### Call（`calls` 下每个条目）

| 档 | 字段 | 含义 |
|---|---|---|
| 必 | `type` | `agent` / `command` / `webhook` |
| 必 | `agent` | （agent）Agent 定义名；引用不存在 → 加载报错 |
| 必 | `command` | （command）shell 命令，字符串或 `{command, env, args, ...}` |
| 必 | `webhook` / `url` | （webhook）URL 与方法等 |
| 选 | `depends_on` | 同 Team 内依赖的 call |
| 选 | `inputs` | 显式输入与 SharedRecord（见下节，写错是高频坑） |
| 选 | `output` | 本次调用发布的 SharedRecord |
| 选 | `responsibility` | 职责说明（进提示词） |
| 选 | `timeout` | command/webhook 的超时；agent call 不看墙钟 |
| 选 | `command.env` / `command.args` | 注入环境变量 / 位置参数，值支持 `${...}` 模板 |
| 选 | `command.replay_policy` / `webhook.replay_policy` | `never` / `idempotent` / `allow`；`idempotent` 时 `idempotency_key` **必填** |

调度：无依赖的 call 自动并行；有依赖的等依赖完成；依赖多个时先读声明的 record。

### 把上游记录传进 command（`${records.*}`）

command 不读对话，只能靠 `inputs` 声明 + 模板取值（`internal/runtime/call/policy.go`）：

| 写法 | 取什么 |
|---|---|
| `${records.<Name>}` / `${records.<Name>.Summary}` | Summary |
| `${records.<Name>.RecordID}` / `.Name` | 对应字段 |
| `${records.<Name>.Data.<field>}` | `Data` 的**顶层**字段 |

固定占位符：`${flow_session_id}` / `${flow_turn_id}` / `${team_id}` / `${team_turn_id}` / `${call_id}` / `${call_turn_id}` / `${attempt}` / `${recovery_of}`。

三条硬事实：
1. 被引用的 record **必须在该 call 的 `inputs` 里声明**，否则 `record "X" not found among call inputs`。
2. 取不存在的字段**直接失败**：`record "X" has no data field "y"`（有意设计，传空更危险）。
3. 只能取 `Data` 顶层；`${records.X.Data.a.b}` → `too deep`。非标量以 JSON 字符串传递。

`Data` 形状按产出方不同：

| 产出方 | `Data` 顶层 | 取业务值 |
|---|---|---|
| agent | schema 字段（已展开到顶层） | `${...Data.<schema字段>}` |
| command | `{exit_code, stdout, stderr, passed}` | 业务 JSON 在 `stdout` **字符串**里，取出来再解析 |

### `inputs` 的三种写法（高频坑）

| 写法 | 匹配范围 | 什么时候用 |
|---|---|---|
| `user_message: true` | 用户本轮原始输入 | 需要用户输入的 call |
| `flow_records: [X]` | **Flow 级** record 集合 | 引用其他 team 的产出，或已提升为 flow 的 record |
| `records: [{from, record}]` | **指定 call 的产出**（含同 team 内） | 同 team 内的顺序依赖 |

`flow_records` 与 `team_records` 都只在 Flow 级集合里按名匹配，所以**同 team 内"前一个 call 刚产出、后一个要引用"必须用 `records` + `from`**：

```yaml
# ❌ TaskCandidates 此刻还是 team scope
acquire:
  depends_on: [search]
  inputs: { flow_records: [TaskCandidates] }
# → record "TaskCandidates" not found among call inputs

# ✅ 用 from 指到同 team 的产出方
acquire:
  depends_on: [search]
  inputs:
    records:
      - from: search
        record: TaskCandidates
```

## Agent（`.agents/agents/<id>/AGENT.md`）

frontmatter 之后是正文：**原样拼进 system prompt**，不做模板渲染。

| 档 | 字段 | 说明 |
|---|---|---|
| 必 | `name` | 空 → `agent definition name is required`；Team/Spawn 按它引用 |
| 选 | `persona` | `{role, goal, backstory}`，进 system prompt |
| 必 | `model.model` | models.json 里 profile 的 **`name`**（不是 `id`）；**不支持 `${ENV}`** |
| 选 | `tools.builtin` | 见 tools 节 |
| 选 | `skills` / `rules` | 引用不存在的 → 加载报错 |
| 选 | `knowledge` | 条目名；只注入目录指路块，不注入内容 |
| 选 | `loop` / `context` / `budget` / `completion` | 默认够用，见 `tuning.md` |
| 选 | `structured_output` / `hitl` / `hooks` / `workspace` | 有明确目标才写 |
| 禁 | `loop.timeout` / `budget.max_wall_time` | 已移除，写了加载报错 |
| 禁 | 任何自造键（`handoffs` 等） | 静默丢弃 |

### model（未写的字段从 models.json 继承）

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `provider` | 覆盖 protocol |
| 选 | `temperature` / `top_p` / `top_k` / `repetition_penalty` | 覆盖采样参数；**不写就不发送给网关** |
| 选 | `reasoning` | 推理配置 |
| 选 | `max_output_tokens` | 单次输出上限 |
| 选 | `api_key` / `base_url` | 特殊场景覆盖 |

### tools

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `builtin` | `Read` `Write` `Grep` `Glob` `Bash` `WebSearch` `WebFetch` `CodeNav` `AskUserQuestion` `TodoWrite` `TodoRead` `Spawn` `Collect` `ListTeams` `Team` `TeamSession` `State` `Define` `REPL` `ScheduleWakeup` `CancelWakeup` |
| 选 | `custom` / `mcp` | 自定义 / MCP 工具 |

- **没有 `Edit`**：改文件用 `Read` → `Write`（整文件覆盖）。
- 写错工具名**不报错**，只是模型看不见 → "模型说没有某能力"先核名字。
- `Spawn` / `State` / `Define` 按需开启；不写进 `builtin` 就没有。
- **`Collect` 自动授予**：声明了 `Team` 或 `Spawn` 时引擎自动加入允许清单，不用手写。
- `WebSearch` / `WebFetch` 的数据源在 `settings.json` 的 `web` 段：默认 `duckduckgo`（免 key，仅 search）；换 provider 必须显式写 `{"web": {"provider": "tinyfish", "api_key": "..."}}`——只写 api_key **不会**切换 provider。key 三级来源：`web.api_key` > `WEB_SEARCH_API_KEY` > `env` 段。

### loop

| 档 | 字段 | 默认 | 含义 |
|---|---|---|---|
| 选 | `max_rounds` | 200 | 单次执行最大模型轮次；与 `budget.max_model_rounds`、`runtime.max_agent_rounds` 取 **min** |
| 选 | `tool_execution` | `sequential` | `sequential` / `parallel_safe` |
| 选 | `max_parallel_tools` | 引擎默认 | 并发只读工具数 |
| 选 | `async_tools` | 空 | 允许创建持久异步任务的工具 |
| 别 | `max_same_tool_calls` / `max_no_progress_rounds` / `max_same_model_texts` | 内置 | 判停守卫 |
| 选 | `max_repeated_reads` | 3 | 同一只读调用（同参数）重复几次判停；**写操作后的重复不计** |
| 选 | `stuck_action` | `fail` | `fail` / `ask_user`（无人可答的子 agent 会挂起） |
| 别 | `max_model_retries` | 引擎默认 | provider 瞬时失败重试 |
| 禁 | `timeout` | — | 已移除，写了加载报错 |

### context

| 档 | 字段 | 默认 | 含义 |
|---|---|---|---|
| 别 | `max_input_tokens` | 模型容量 | 容量**覆盖**；写小 = 窗口变小、压缩提前 |
| 别 | `target_ratio` | 0.45 | 压缩后目标比例 |
| 别 | `compaction_threshold` | 0.90 | 触发压缩的比例；必须低于硬限 |
| 别 | `output_reserve_ratio` | 0 | 为输出预留；**硬限 = 1 - 它** |
| 别 | `tool_output_ratio` | 0.10 | 工具输出预算比例（与容量相乘得字符上限） |
| 选 | `max_tool_output_chars` | 容量 × 0.10 × 4 | 单条工具结果字符上限 |
| 别 | `microcompact_threshold_chars` / `microcompact_max_chars` | 8192 / 4096 | 单条工具结果超阈值时截断到多少 |
| 别 | `recent_message_groups` | 2 | 压缩时保留最近几组原文 |
| 别 | `compactor` | `llm` | `llm` / `mechanical` |

### completion（默认全关）

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `require_tool` / `required_tools` | 必须调用过工具 / 其中某个 |
| 选 | `require_tool_success` | 工具调用必须成功 |
| 选 | `require_workspace_read` / `require_workspace_change` | 必须读过 / 改过工作区 |
| 选 | `require_structured_output` | 必须产出符合 schema 的结果 |

### budget（0 = 不限）

| 档 | 字段 | 含义 |
|---|---|---|
| 别 | `max_model_rounds` | 最大模型调用数（与 `loop.max_rounds` 取 min） |
| 选 | `max_tool_calls` | 最大工具调用数 |
| 选 | `max_input_tokens` / `max_output_tokens` | **累计** token 上限（不是窗口） |
| 选 | `max_file_changes` | 最大写操作数 |
| 选 | `max_tool_output` | 累计工具输出字符上限 |

### 其他

| 档 | 字段 | 含义 |
|---|---|---|
| 选 | `structured_output` | `{type, schema, max_output_tokens, enforce_provider_format}` |
| 选 | `hitl` | `{enabled, timeout}` 人工审批 |
| 选 | `hooks` | **列表**，每项 `{event, command, timeout}`；写成映射会解码失败 |
| 选 | `skills` / `rules` | 引用的清单 |

- **`skills`**：system prompt 只多一行索引（名字 + description + `SKILL.md` 路径），正文按需 `Read`；但 `tools`/`allowed-tools` 是**声明即授予**，只声明真正需要的。
- **`knowledge`**：只注入"可读哪些知识目录"的指路块，不注入条目内容；读法是 `Glob` + `Read`。目录三处：`.agents/knowledge/`（Flow 共享）、`.agents/teams/<team>/knowledge/`、`.agents/agents/<agent>/knowledge/`。

## SharedRecord（跨层数据契约）

- 产出：`output.record` 声明名字；消费：`inputs` 里按名声明。
- **名字是自由字符串**，拼错在加载期报"没有生产者"。
- scope：`team`（默认，产出者自用）/ `flow`（整个 Flow 可见）。
- 被派发的子 Team 产出的记录会以 **flow 作用域副本**写进 `evidence.jsonl`，调用方 `flow_records` 才读得到。
- 被工具结果唤醒的回合只读**派发那一刻之后**产生的记录（上一轮结论不会被当成本轮事实）。

## models.json（`.agents/models.json`）

**唯一必需文件**：缺它或 `models` 为空，启动直接失败。

```json
{
  "model": "gpt-4o-mini",
  "models": [
    { "name": "gpt-4o-mini", "id": "gpt-4o-mini", "protocol": "openai_chat",
      "base_url": "http://127.0.0.1:15721/v1", "api_key": "${OPENAI_API_KEY}",
      "maxInputTokens": 128000, "maxOutputTokens": 64000 }
  ]
}
```

| 档 | 字段 | 含义 |
|---|---|---|
| 必 | `models[]` | 至少一条 |
| 选 | `model` | 默认模型，按 **`name`** 匹配；空则用第一条 |
| 必 | `name` | 匹配键、显示名，也是 Agent `model.model` 要写的值 |
| 选 | `id` | 发给 API 的 wire model；空则用 `name` |
| 选 | `protocol` | `openai_chat`（默认）/ `anthropic_messages`；不认识按 openai |
| 必 | `base_url` | Provider 地址 |
| 选 | `models_url` | `/models` 探测地址 |
| 选 | `api_key` | 直接写值或 `${ENV_VAR}`；`${VAR}` 展开为空会报 `API key ... is not set`，不回退 |
| 选 | `maxInputTokens` | **上下文预算**，`context.*` 各比率的**分母** |
| 选 | `maxOutputTokens` | 单次输出上限 |
| 选 | `stream` | 需要把 SSE 聚合成一个响应时 |
| 选 | `temperature` / `top_p` / `top_k` / `repetition_penalty` | 缺省**不发送** |
| 选 | `reasoning` / `onlyReasoning` | 推理配置 |
| 选 | `supports*` | 能力声明（`supportsImages` / `supportsToolCall` 等） |
| 选 | `fallback` | 可重试错误时按序尝试的备用模型名 |
| 选 | `cooldown_seconds` | 失败模型被跳过的时长，默认 600 |
| 禁 | `maxAllowedSize` | 结构体有声明但**无人读取**；预算看 `maxInputTokens` |

## settings.json（`.agents/settings.json`）

可选，缺文件即全默认。**只有这些段有代码读取**：

| 段 | 关键项 |
|---|---|
| `logging` | `level`（`debug` 才有 `model request usage`）、`dir`、`max_file_size`、`max_backups`、`retention_days` |
| `runtime` | `max_team_turns`(100/每 FlowTurn)、`max_calls_per_team_turn`(100)、`max_agent_rounds`(200)、`max_parallel_*`(20)、`max_coordinate_retries`(20)、`max_active_team_sessions`(8)、`max_task_context_depth`(3) |
| `knowledge` | `summary_model`（空 = 默认模型） |
| `web` | `provider` / `api_key` / `fetch.provider` |
| `mcp` | `name` / `transport`(`stdio`\|`http`) / `command`+`args` / `url`+`headers` / `env` |
| `tools` | `restricted_bash_phrases`（只增不减）、`repl.*`（默认关；开了还要 `hitl.enabled`） |
| `env` | 注入子进程的环境变量；**每个值按路径解析**（相对工作区根），非路径值会被拼成路径，只适合放路径锚点 |

**不生效的段**（老配置常见）：`logging.output`（被丢弃）、`paths.data`（硬编码 `.agents/data/`）、`agent.*`（没有全局 agent 段，单 agent 限制在 `AGENT.md`，全局在 `runtime.max_agent_rounds`）、`observability`（包已删）。

`runtime` 的上限是**防跑飞的兜底**，不是编排旋钮：正常 Flow 会来回移交很多次，默认值刻意远高于任何正常计数。

## rules（`.agents/rules/<name>.md`）

```markdown
---
type: hard          # hard 强制 / soft 建议
scope:
  type: all         # all / team / agents
  # team 时必配 teams: [<team 局部名>]；agents 时必配 agents: [<agent id>]
priority: 10
paths:              # 可选：命中这些 glob 才注入；不写=常驻
  - "internal/**/*.go"
---
规则正文。
```

- `scope.type` 写 `team`/`agents` 却不给名单 → **永远不命中且不报错**（注意是复数 `agents`）。
- 正文按需读，改规则不需要重启。
- `priority` 越大越靠前。
