# 看运行时：数据在哪、怎么读

排查时先确认自己读的是**哪一层**。三个错误最常见：按 Go 字段名 jq（得到 `null`）、
在 `team.jsonl` 找 team turn（分层不在那里）、按事件名找"失败"（终态统一叫 `ended`）。

## 1. 落点

```text
.agents/data/                       # 工作区根（进程 cwd）下
├── logs/<YYYY-MM-DD>.log           # 全局执行日志，JSON 行，不按会话分文件
├── sessions/<fs_id>/               # 一次对话一个目录，续聊继续追加
│   ├── flow.jsonl                  # flow 层 + team turn 层
│   ├── team.jsonl                  # call 层（agent / command / webhook）
│   ├── agent.jsonl                 # agent 内部（模型输出、工具调用）
│   ├── evidence.jsonl              # Flow-scope SharedRecord（成品与依据）
│   └── teams|agents/<...>/state.md # 状态快照
├── agent-transcripts/<call>.json   # 由 agent.jsonl 重建的对话视图（不是第二份数据）
├── agent-checkpoints/<id>.json     # 续跑检查点
├── tool-tasks/<id>.json            # 异步工具任务
└── uploads/<hash>
```

`agent-transcripts/` `agent-checkpoints/` `tool-tasks/` `uploads/` **按需生成**，不要假定存在。

## 2. 三层分流

| 文件 | 只包含 |
|---|---|
| `flow.jsonl` | `session.*`（会话与 turn）、`session.team_session.*`、`session.team_turn.*`、`session.record.*`、`session.recovery.*`、`session.mailbox.*`、`session.wakeup.*` |
| `team.jsonl` | `session.agent_session.*`、`session.agent_turn.*`、`session.command_turn.*`、`session.webhook_turn.*`、`session.approval.*` |
| `agent.jsonl` | `session.agent.model_response`、`session.tool_call.*`、`session.agent.feedback`、`session.context.compacted` |

两个高频误判：

- **`session.team_turn.ended` 在 `flow.jsonl`**，不在 `team.jsonl`。
- **`session.command_turn.*` 在 `team.jsonl`**（command 是 call），不在 `flow.jsonl`。

每行共有字段：`seq`（判先后用它，不要用行号）、`event_id`、`type`、`flow_session_id`、
`flow_turn_id`、`team_session_id`、`team_turn_id`、`team_id`（flow 局部名）、`call_id`、
`call_turn_id`、`call_type`、`attempt`、`recovery_of`、`created_at`、`payload`。
用不到的字段是 `null`，不代表数据不全。

## 3. 事件清单

**flow.jsonl**

| type | 含义 |
|---|---|
| `session.created` / `.updated` | 会话创建 / 更新 |
| `session.turn.started` / `.ended` | 一轮 flow turn 的生命周期。**终态只有一个事件**，怎么结束看 `stop_reason`，在等谁看 `awaiting` |
| `session.team_session.created` / `.updated` | team 会话（`per_task` 下同一 team 多个） |
| `session.team_turn.started` / `.ended` | team turn；**路由结果在这里** |
| `session.record.published` / `.consumed` | record 发布 / 被消费 |
| `session.recovery.*` / `session.mailbox.*` / `session.wakeup.*` | 恢复、信箱、自唤醒 |

**team.jsonl**

| type | 含义 |
|---|---|
| `session.agent_session.created` / `.updated` | agent call 的会话 |
| `session.agent_turn.started` / `.completed` | agent call 轮次；**消耗事实源在这里** |
| `session.command_turn.started` / `.completed` | command call |
| `session.approval.requested` / `.resolved` | 人工审批 |

**agent.jsonl**

| type | 关键字段 |
|---|---|
| `session.agent.model_response` | `payload.text`（**不是 `content`**）、`round`、`tool_calls`、`finish_reason`、`model`、`usage` |
| `session.tool_call.started` / `.completed` / `.failed` | `tool_name`、`arguments`、`content`、`exit_code` |
| `session.agent.feedback` | `content`（引擎追加的内部提示）、`round`、`raw_text`（结构化重试丢弃的原文） |
| `session.context.compacted` | `summary`、`dropped_count` |

## 4. 关键 payload

| 事件 | 字段 | 含义 |
|---|---|---|
| `session.turn.started` | `payload.turn.input` | 本轮输入原文 |
| `session.turn.started` | `payload.turn.origin` | `user`（用户提问）/ `mailbox` / `wakeup` / `resume` / `tool`。**只有 `user` 是用户提问** |
| `session.turn.ended` | `payload.stop_reason` | `end_turn` / `max_rounds` / `max_tool_calls` / `max_tokens` / `cancelled` / `refusal` / `failed` |
| `session.turn.ended` | `payload.awaiting` | `user` / `tool` / `approval` / 空（不再等任何人）。等工具、等审批**不是失败** |
| `session.turn.ended` | `payload.reply` | 给用户的答复；`intermediate=true` 时为空（那是进度） |
| `session.turn.ended` | `payload.team` | 写这条答复的 Team（`origin` 只说怎么被唤醒，不是归属） |
| `session.turn.ended` | `payload.limit` | 撞兜底上限时的结构化事实：`name` / `value` / `observed` / `top_pairs` / `recent` |
| `session.turn.ended` | `payload.deliveries[]` | 需独立送达用户的结果：`message_id` / `team_id` / `text` |
| `session.team_turn.ended` | `payload.team_result.turn.next.action` | `proceed` / `activate` / `return` / `coordinate` / `fail` / `wait_tool` / `wait_approval` |
| `session.team_turn.ended` | `payload.team_result.turn.stop_reason` / `.awaiting` | team turn 的两维终态（同名字段也在 payload 顶层） |
| `session.team_turn.ended` | `payload.team_result.reply` / `.records[]` / `.call_results[]` | team 的文本、产出、各 call 结果 |
| `session.agent_turn.completed` | `payload.call_result.status` | 非 `completed` 就看 `error` |
| `session.agent_turn.completed` | `payload.call_result.requests[]` | **消耗事实源**：一个元素 = 一轮模型调用 |
| `session.agent_turn.completed` | `payload.spawn` | 子 agent 回合：`{agent, key, parent_call_id}` |
| `session.record.published` | `payload.record.producer` | 归属（顶层 `team_id` 为空，看这里） |
| `session.record.consumed` | `payload.consumer_team` | 这条 record 交给了谁 |

字段一律 **snake_case**（`team_result` / `call_result` / `requests` / `usage` 也是）。
写成 Go 的 `Usage.PromptTokens` 会得到 `null` 且不报错。

## 5. 对话在哪看

| 你要看 | 去哪读 | 字段 |
|---|---|---|
| 用户提问 | `flow.jsonl` `session.turn.started` + `origin=="user"` | `payload.turn.input` |
| 模型每轮原始输出 | `agent.jsonl` `session.agent.model_response` | `payload.text` |
| 这一轮答了什么、谁写的 | `flow.jsonl` `session.turn.ended` | `payload.reply` + `payload.team` |
| 成品与依据 | `evidence.jsonl` | `summary` / `basis[]` |
| 工具过程 | `agent.jsonl` `session.tool_call.*` | `tool_name` / `arguments` / `content` |

```bash
S=.agents/data/sessions/<fs_id>

# 人真正问的（排除 mailbox / wakeup / resume / tool 唤醒）
jq -r 'select(.type=="session.turn.started" and .payload.turn.origin=="user") | .payload.turn.input' $S/flow.jsonl

# 每轮答复（intermediate 的进度行没有 reply）
jq -r 'select(.type=="session.turn.ended" and (.payload.reply // "") != "") | "\(.payload.team // "-")\t\(.payload.reply)"' $S/flow.jsonl

# 模型原始输出
jq -r 'select(.type=="session.agent.model_response") | "[r\(.payload.round)] \(.payload.text)"' $S/agent.jsonl

# 工具退出码
jq -r 'select(.type=="session.tool_call.completed") | "\(.payload.tool_name) exit=\(.payload.exit_code // "?")"' $S/agent.jsonl
```

一条用户消息会派生多个 turn（派发、结果唤醒、自唤醒各一轮）。按 `origin=user` 取提问，
按 `reply` 非空且 `intermediate=false` 取答复，中间那些是引擎自己接着跑。

## 6. `round` 与空 `text`

- `payload.round` 在**每个 `agent_turn` 内从 0 重数**。`round=0` 会反复出现，别当成数据重复；
  区分 turn 用 `team_turn_id`。从检查点续跑时首轮 `round` 不从 0 开始，那是 resume，不是错乱。
- 子 agent（`Spawn`）复用父调用的 `call_turn_id`，`round` 也从 0 起 → 必须再加
  `payload.spawn.parent_call_id` 才能区分父子回合。
- `text` 为空串有三种**正常**情况：

| 情况 | `finish_reason` | `tool_calls` |
|---|---|---|
| 纯工具调用轮 | `tool_calls` | 有 |
| 结构化重试丢弃原文 | `stop` | 无（原文在 `session.agent.feedback.payload.raw_text`） |
| 结构化输出已解析 | `stop` | 无（成品在 `evidence.jsonl`） |

结构化重试：agent 配了 `structured_output` 但模型没按 schema 输出 → 丢弃该轮文本、追加一条
`session.agent.feedback`（`raw_text` 是被丢弃的原文）、下一轮重试，最多 1 次。

## 7. Token

```text
requests[].usage  →  call_result.usage  →  team_result.usage  →  终端 Tokens 行（各 team 求和）
```

- flow 层**没有**聚合好的 usage；终端那个 `Tokens:` 是消费方对 `team_result.usage` 求和的结果。
- `team_result.usage` 已含本轮所有 call（含插入成合成 call 的子任务）→ **team 层与 call 层只能二选一，相加就是重复计**。
- 缓存命中量取 `prompt_cache_hit_tokens` 与 `cache_read_input_tokens` 的**较大值**，不能相加。

```bash
S=.agents/data/sessions/<fs_id>

# 总计（与终端 Tokens 行同口径）
jq -s '[.[]|select(.type=="session.team_turn.ended")|.payload.team_result.usage.total_tokens]|add' $S/flow.jsonl

# 按 team
jq -r -s '[.[]|select(.type=="session.team_turn.ended")]
  | group_by(.team_id)
  | .[] | "\(.[0].team_id)\ttotal=\([.[].payload.team_result.usage.total_tokens]|add)"' $S/flow.jsonl

# 按 agent（粒度在 call 层）
jq -r -s '[.[]|select(.type=="session.agent_turn.completed")]
  | group_by(.payload.call_result.agent_id)
  | .[] | "\(.[0].payload.call_result.agent_id)\ttotal=\([.[].payload.call_result.usage.total_tokens]|add)"' $S/team.jsonl

# 单 agent 逐轮（定位从哪一轮开始膨胀）
jq -r 'select(.type=="session.agent_turn.completed")
  | .payload.call_result.requests[] | "prompt=\(.usage.prompt_tokens) completion=\(.usage.completion_tokens)"' $S/team.jsonl
```

`prompt ≫ completion` 是常态（每轮重发前序内容）；命中率突然为 0 通常是前缀每轮都在变
（时间戳、随机 ID、每轮重排的列表）。

## 8. ID 串联

```text
flow_session_id  → sessions/<fs_id>/
  flow_turn_id (ft_…)   一次 flow 执行（一次提问会派生多个）
    team_session_id     某个 Team 的会话（per_task 下同一 team 多个）
      team_turn_id (tt_…)  某个 team 的一次执行
        call_turn_id       <tt>:<call_id>
```

| 文件 | 事件 | flow_session | flow_turn | team_id | team_turn | call_id | call_turn |
|---|---|---|---|---|---|---|---|
| flow | `session.turn.*` | ✓ | ✓ | | | | |
| flow | `session.team_turn.*` | ✓ | ✓ | ✓ | ✓ | | |
| flow | `session.record.published` | ✓ | ✓ | **✗** | ✓ | | |
| flow | `session.record.consumed` | ✓ | ✓ | ✓ | ✓ | | |
| team | `session.agent_turn.*` / `.command_turn.*` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| agent | `session.agent.model_response` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

两个反直觉点：`session.record.published` **顶层没有 `team_id`**（用 `payload.record.producer`，
那里八个 ID 最全）；**只有 agent call 写 `agent.jsonl`**，command 的输出在
`session.command_turn.completed.payload`。

```bash
S=.agents/data/sessions/<fs_id>

# ft → tt：一次提问激活了哪些 team
jq -r 'select(.type=="session.team_turn.started")|"\(.flow_turn_id) → \(.team_turn_id) [\(.team_id)]"' $S/flow.jsonl

# tt → call
TT=$(jq -r 'select(.type=="session.team_turn.started")|.team_turn_id' $S/flow.jsonl | head -1)
jq -r --arg tt "$TT" 'select(.team_turn_id==$tt and (.type|endswith(".started")))|"\(.call_type)\t\(.call_id)\t\(.call_turn_id)"' $S/team.jsonl

# record 归属
jq -r 'select(.type=="session.record.published")|"\(.payload.record.name) ← \(.payload.record.producer.team_id)/\(.payload.record.producer.call_id)"' $S/flow.jsonl

# 反查：某轮模型输出属于哪次提问
FT=$(jq -r 'select(.type=="session.agent.model_response")|.flow_turn_id' $S/agent.jsonl | head -1)
jq -r --arg ft "$FT" 'select(.type=="session.turn.started" and .payload.turn.id==$ft)|"\(.payload.turn.origin)\t\(.payload.turn.input)"' $S/flow.jsonl
```

## 9. 执行日志

`logs/<YYYY-MM-DD>.log`：JSON 行，固定 `ts` / `level` / `msg`，其余键由调用方附带
（`round` / `agent_id` / `call_id` / `team_id` / `model` / `error`，部分行带 `flow_session_id`）
——**不是固定 schema**，别假定某字段每行都有。

```bash
grep '"flow_session_id":"<fs_id>"' .agents/data/logs/*.log
```

没有 `flow_session_id` 的行用时间窗 + `team_id` / `call_id` 定位。`logging.level=debug`
才有 `model request usage`。启动期校验失败**不写日志**，只在 stderr。
