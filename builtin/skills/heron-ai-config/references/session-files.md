# 会话排查 · 数据落在哪、怎么读

heron 的运行时数据按 run 层级分流在 `.agents/data/` 下。本文件讲**数据落点、
三层文件各装什么、聊天记录在哪**。

排查流程与判定标准见 `debug-triage.md`；token 统计见 `token-usage.md`；
ID 串联见 `trace-ids.md`。

## 1. 数据落点全景

所有运行时数据在**工作区根**（即 heron 启动时的 cwd）的 `.agents/data/` 下。
以本工作区为例，cwd 是 `auto_bugfix/heron-ai`，所以是
`auto_bugfix/heron-ai/.agents/data/`：

```
.agents/data/
├── logs/<YYYY-MM-DD>.log         # 全局执行日志（按日期分文件，不按 session）
├── sessions/<flow_session_id>/   # 会话级数据（核心排查入口）
│   ├── flow.jsonl                # flow 层 + team turn 层事件
│   ├── team.jsonl                # agent call 层 + command/webhook call 层事件
│   ├── agent.jsonl               # agent 内部行为（模型输出、工具调用）
│   ├── evidence.jsonl            # 证据链（Flow-scope SharedRecord）
│   ├── teams/<team>/state.md     # team 状态快照
│   └── agents/<team>/<agent>/state.md
└── agents/<agentID>/…            # 实体状态（跨会话）
```

`.agents/data/` 下**实际只有 `logs/`、`sessions/`、`agents/` 三个子目录**。
checkpoint / 异步工具任务按需生成，不保证存在，不要假定其路径。
## 2. 三层事件流：按 run 层级分流

关键规则：**事件按产生它的 run 层级写入不同文件**，不是同级混放。
这是排查时最容易搞错的一点。

| 文件 | 只包含 | 事件 |
|---|---|---|
| `flow.jsonl` | flow 会话级 + **team turn 级** | `flow_session.*`、`flow_turn.*`、`team_session.*`、`team_turn.*`、`shared_record.published`、`recovery.*` |
| `team.jsonl` | **call 层**：agent / command / webhook | `agent_session.*`、`agent_turn.*`、`command_turn.*`、`webhook_turn.*`、`approval.*` |
| `agent.jsonl` | agent **内部**行为 | `agent.model_response`、`tool_call.*`、`agent.feedback`、`context.compacted` |

由此得出两个高频误判：

- **`team_turn.completed` 在 `flow.jsonl`，不在 `team.jsonl`。** 在 `team.jsonl`
  里搜它必然一条都找不到——这是正常的分层，不是数据缺失。
- **`command_turn.*` 在 `team.jsonl`，不在 `flow.jsonl`。** command 是 team 的
  一个 call，所以和 `agent_turn.*` 同级。

每行是一个 JSON 对象。共有字段：`seq`、`event_id`、`type`、`flow_session_id`、
`team_turn_id`、`team_id`、`call_id`、`call_turn_id`、`call_type`、`attempt`、
`created_at`、`payload`。

### flow.jsonl

| type | 含义 |
|---|---|
| `flow_session.created` / `flow_session.updated` | 会话创建/更新 |
| `flow_turn.started` / `flow_turn.completed` / `flow_turn.waiting_input` / `flow_turn.failed` | 一轮 flow turn 生命周期 |
| `team_session.created` / `team_session.updated` | team 会话 |
| `team_turn.started` / `team_turn.completed` | team turn（**路由结果在这里**） |
| `shared_record.published` | 发布 SharedRecord |
| `recovery.requested` / `recovery.completed` | 恢复 |

### team.jsonl

| type | 含义 |
|---|---|
| `agent_session.created` / `agent_session.updated` | agent call 会话 |
| `agent_turn.started` / `agent_turn.completed` | agent turn（**消耗在这里**） |
| `command_turn.started` / `command_turn.completed` | command call |
| `approval.requested` / `approval.resolved` | HITL 审批 |

### agent.jsonl

| type | 含义 | 关键字段 |
|---|---|---|
| `agent.model_response` | 模型单次输出 | **`text`**（不是 `content`）/ `tool_calls` / `usage` / `finish_reason` / `model` / `round` |
| `tool_call.started` / `tool_call.completed` / `tool_call.failed` | 工具调用 | `tool_name` / `arguments` / `content` / `exit_code` |
| `agent.feedback` | 内部 user 消息（结构化重试提示等） | `content` / `round` / `raw_text`（结构化重试时携带被丢弃的模型原文） |
| `context.compacted` | 上下文压缩 | `summary` / `dropped_count` |

> `agent.model_response` 的回复原文在 **`payload.text`**。写成 `payload.content`
> 会读到空值，从而误判成"模型没输出"。

### `round` 是 turn 内局部序号

`agent.model_response.payload.round` **在每个 `agent_turn` 内从 0 重新计数**，
不是整个会话递增。所以同一个会话的多轮里，`round=0` 会反复出现。

排查时不要用「`round` 重复」推断数据重复——要用 `team_turn_id` 区分。
判据：`team_turn_id` 不同就是不同的 turn，`round` 各自从 0 开始。

```bash
# 按 turn 分组看各自的轮次
jq -r 'select(.type=="agent.model_response")
       | "\(.team_turn_id) round=\(.payload.round) finish=\(.payload.finish_reason) text_len=\((.payload.text // "")|length)"' \
  $S/agent.jsonl
```

### `agent.model_response` 的 `text` 可能为空（正常，不是数据丢失）

三种情况 `text` 是空串，都**不是**落库故障：

| 情况 | `finish_reason` | `tool_calls` | 说明 |
|---|---|---|---|
| 纯工具调用轮 | `tool_calls` | 有 | 模型这轮只调工具、没说话，内容在 `tool_calls[]` |
| 结构化重试丢弃 | `stop` | 无 | 见下（原始输出被丢弃） |
| 结构化输出已解析 | `stop` | 无 | 成品在 `evidence.jsonl` 的 `summary` |

全库统计（`sessions/*/agent.jsonl` 共 148 条 `agent.model_response`）印证了这三类：

```
(text空, tool_calls, 有工具)   -> 84   纯工具调用轮
(text非空, stop, 无工具)       -> 37   正常文本回复
(text非空, tool_calls, 有工具) -> 23   边说话边调工具
(text空, stop, 无工具)         -> 4    结构化重试丢弃
```

**结构化重试场景**（会看到 `agent.feedback`）：agent 配了 `structured_output`
但模型没按 schema 输出时，引擎会：

1. 丢弃该轮的 `resp.Text`
2. 追加一条 `agent.feedback`，`content` 为固定的重试提示，
   **`raw_text` 为被丢弃的模型原始输出**（便于回溯）
3. 下一轮重试

真实样例（`auto_bugfix` 的 `fs_7a3e3c95f9b3fbf9`）：

```
seq=21  agent.model_response  round=0  finish=stop  text=''  completion_tokens=2
seq=22  agent.feedback        round=0  content='The previous response was not valid JSON for the required schema...'  raw_text='<被丢弃的原文>'
seq=23  agent.model_response  round=1  finish=tool_calls  text=''   ← 重试后改为调工具
```

> ⚠️ `raw_text` 是在 `2026-10-09` 修复
> `ISSUE-structured-retry-no-raw-output` 之后才落盘的。**修复前**的历史会话
> （含上面的 `fs_7a3e3c95f9b3fbf9`）该字段不存在，`jq` 会得到 `null`——这是
> 旧数据，不是"模型输出为空"。

### 排查手法：区分"模型输出异常"与"schema 定义过严"

丢弃轮的原始输出可从 `agent.feedback.payload.raw_text` 直接读取：

```bash
# 查看结构化重试时模型到底输出了什么
jq -r 'select(.type=="agent.feedback")
       | "round=\(.payload.round) raw=\(.payload.raw_text // "<旧数据，无 raw_text>")"' \
  $S/agent.jsonl
```

若为旧数据（无 `raw_text`），用 `completion_tokens` 间接判断性质：

```bash
# 列出所有被丢弃的轮及其 completion_tokens
jq -r 'select(.type=="agent.model_response"
              and ((.payload.text // "")|length)==0
              and .payload.finish_reason=="stop")
       | "round=\(.payload.round) completion=\(.payload.usage.completion_tokens)"' \
  $S/agent.jsonl
```

判据：

| `completion_tokens` | 结论 | 处理 |
|---|---|---|
| 很小（实测 4 条全是 **2**） | 模型侧输出异常（只吐 2 个 token 就 stop） | 属正常自愈，**不需要兼容** |
| 正常量级（几百）却仍不合法 | schema 定义与实际输出形态不匹配 | 考虑放宽 schema |

**判断是否自愈**：`agent.feedback` 之后紧接有 `agent.model_response`，且该 turn
最终 `status=completed` → 重试成功（正常）；若重试耗尽
（`structuredRetries` 上限 1 次）后仍失败 → 该 turn 会 `failed`。

### 证据链 evidence.jsonl

每行是一个 Flow-scope 的 SharedRecord，含 `basis`（指向支持它的文件/代码段）：

```
{ record_id, name, summary, producer, basis:[{path, revision, lines}], … }
```

用于回答"这个结论基于哪个文件、哪个代码段"。
## 3. 聊天记录在哪看（用户问了什么 / 模型答了什么）

这是最高频的排查需求。**对话不是存在一个"聊天记录文件"里，而是按层分散在三个位置**：

| 你要看的 | 去哪读 | 字段 |
|---|---|---|
| **用户输入**（每一轮的提问原文） | `flow.jsonl` 的 `flow_turn.started` | `payload.turn.input` |
| **模型每轮的原始回复** | `agent.jsonl` 的 `agent.model_response` | `payload.text`（含思考/JSON）、`payload.round` 标第几轮 |
| **最终对用户的答复** | `evidence.jsonl` 的 SharedRecord | `summary`（已解析的成品文本） |
| 工具调用过程 | `agent.jsonl` 的 `tool_call.*` | `payload.tool_name` / `arguments` / `content` |
| 内部提示（结构化重试等） | `agent.jsonl` 的 `agent.feedback` | `payload.content` |

```bash
S=.agents/data/sessions/<fs_id>

# 用户提问时间线
jq -r 'select(.type=="flow_turn.started") | .payload.turn.input' $S/flow.jsonl

# 模型每轮回复（text 里可能含 <think> 或待解析的 JSON）
jq -r 'select(.type=="agent.model_response") | "[round \(.payload.round)] \(.payload.text)"' $S/agent.jsonl

# 最终答复（成品）
jq -r '.summary' $S/evidence.jsonl
```

一个会话可以续聊多轮：**同一 `sessions/<fs_id>/` 目录会持续追加**，
所以 `flow_turn.started` 会出现多次，每次的 `payload.turn.input` 就是那一轮的用户输入。
## 4. 执行日志（logs/<日期>.log）

JSON 行，字段：`ts`、`level`（debug/info/warn/error）、`msg`、`round`、
`agent_id`、`call_id`、`team_id`、`model`、`error`，部分行带 `flow_session_id`。

日志是**全局**的（不按 session 分文件），用 `flow_session_id` / `team_id` /
`call_id` 做过滤。注意并非每行都有 `flow_session_id`，缺失时用时间窗 + 其它字段定位。
