# heron-ai 排查与诊断

当你需要定位一次 agent 执行的问题（为什么这样回答、卡在哪、报什么错、消耗多少）时，
用本文件的方法读取 heron-ai 的运行时数据。

## 1. 数据落点全景

所有运行时数据在 `.agents/data/` 下：

```
.agents/data/
├── logs/                         # 执行日志（全局，按日期分文件，非按 session）
│   └── 2026-09-04.log
├── sessions/<sessionID>/         # 会话级数据（核心排查入口）
│   ├── flow.jsonl                # flow 层事件
│   ├── team.jsonl                # team 层事件
│   ├── agent.jsonl               # agent 层事件
│   ├── evidence.jsonl            # 证据链（Flow-scope SharedRecord）
│   └── state/…/state.md          # 会话状态快照
├── agent-checkpoints/<id>.json   # checkpoint 快照（等待态）
├── tool-tasks/<id>.json          # 异步工具任务
├── agents/<agentID>/<key>/state/ # 实体状态
└── uploads/<hash>                # 媒体上传
```

## 2. 三层事件流（sessions/<sessionID>/）

三层 jsonl 按"产生者"分层，用 ID（flow_session_id / team_turn_id / call_id）串联。
每行是一个 JSON 对象，有 `type` 字段标记事件类型。

### flow.jsonl（flow 层：flow 调度 team）

| type | 含义 |
|---|---|
| `flow_session.created` / `flow_session.updated` | 会话创建/更新 |
| `flow_turn.started` / `flow_turn.completed` / `flow_turn.waiting_*` | 一轮 flow turn 生命周期 |
| `team_turn.started` / `team_turn.completed` / `team_turn.waiting_*` | team turn（flow 调度 team） |
| `shared_record.published` | 发布 SharedRecord（payload.record 含完整 record） |
| `recovery.requested` / `recovery.completed` | 恢复 |

### team.jsonl（team 层：team 调度 call）

| type | 含义 |
|---|---|
| `team_session.created` / `team_session.updated` | team 会话 |
| `agent_turn.started` / `agent_turn.completed` / `agent_turn.waiting_*` | agent turn |
| `command_turn.*` / `webhook_turn.*` | command/webhook call |
| `approval.requested` / `approval.resolved` | 审批 |

### agent.jsonl（agent 层：agent 内部行为）

| type | 含义 | 关键字段 |
|---|---|---|
| `agent.model_response` | 模型输出 | text / tool_calls / usage / finish_reason / model |
| `tool_call.started` / `tool_call.completed` / `tool_call.failed` | 工具调用 | tool_name / arguments / content / exit_code / stdout / stderr |
| `agent.feedback` | 内部 user 消息（completion feedback / structured retry 提示） | content |
| `context.compacted` | 上下文压缩 | summary / dropped_count |

### 证据链 evidence.jsonl

每行是一个 Flow-scope 的 SharedRecord，含 `basis`（指向支持它的文件/代码段）：
```
{ record_id, name, summary, producer, basis:[{path, revision, lines}], … }
```
用于回答"这个结论基于哪个文件、哪个代码段"。

## 3. 执行日志（logs/*.log）

JSON 行，字段：`ts`（时间戳）、`level`（debug/info/warn/error）、`msg`（消息），
外加业务字段（`flow_session_id` / `team_id` / `call_id` / `model` / `error` 等）。

**按 session 过滤**：日志是全局的，用 `flow_session_id` 字段过滤某个会话的日志。

## 4. 常用排查命令（用 Bash 执行 jq）

先确定 sessionID（从用户提供的会话信息，或 `ls .agents/data/sessions/` 找最新）。

```bash
# 看某个 session 的 flow 层 turn 序列
jq -r '.type' .agents/data/sessions/<sessionID>/flow.jsonl

# 看 agent 层的模型输出和工具调用
jq -r 'select(.type=="agent.model_response") | "\(.round) \(.model) \(.text)"' \
  .agents/data/sessions/<sessionID>/agent.jsonl

# 看工具调用的退出码（结构化字段）
jq -r 'select(.type=="tool_call.completed") | "\(.tool_name) exit=\(.exit_code // "?")"' \
  .agents/data/sessions/<sessionID>/agent.jsonl

# 看上下文压缩
jq -r 'select(.type=="context.compacted") | .summary' \
  .agents/data/sessions/<sessionID>/agent.jsonl

# 看证据链（哪些文件/代码段）
jq -r '.name, (.basis[]?.path)' .agents/data/sessions/<sessionID>/evidence.jsonl

# 看某个 session 的执行日志（按 flow_session_id 过滤）
grep '"flow_session_id":"<sessionID>"' .agents/data/logs/*.log
```

## 5. 排查思路

1. **agent 为什么这样回答**：读 agent.jsonl 的 model_response 序列 + context.compacted（看上下文怎么被压缩）。
2. **卡在哪/报什么错**：读三层 jsonl 的 `waiting_*` 事件（等待态）+ 执行日志的 error。
3. **消耗多少**：model_response 的 usage 字段（含缓存命中 cache_read_input_tokens）。
4. **结论基于什么**：evidence.jsonl 的 basis（文件路径 + 行号）。
5. **完整调用链**：用 flow_session_id → team_turn_id → call_id 跨三层关联。
