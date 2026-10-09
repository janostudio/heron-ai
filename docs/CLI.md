# CLI Usage

## Commands

```bash
# TUI interactive mode (default)
heron
heron --flow .agents/flows/default.yml

# Non-interactive mode
heron --prompt "Hello" --flow .agents/flows/default.yml

# Long-lived JSON-RPC over stdin/stdout
heron --json-rpc --flow .agents/flows/default.yml

# HTTP server mode
heron --serve --port 8080 --flow .agents/flows/default.yml

# Resume a waiting FlowSession
heron --prompt "Continue..." --session <flow_session_id> --flow .agents/flows/default.yml

# Knowledge: learn one session (incremental)
heron knowledge <flow_session_id> --flow .agents/flows/default.yml

# Knowledge: learn all unlearned sessions (incremental)
heron knowledge --flow .agents/flows/default.yml

# Knowledge: archive expired knowledge (ExpiresAt in the past)
heron knowledge gc --flow .agents/flows/default.yml

# Version
heron --version
```

## TUI Mode

```
+----------------------------------------------------------+
|  Heron AI - blog_writer_flow         Tokens: 1,234       |
+----------------------------------------------------------+
|                                                          |
|  [research_stage]                                        |
|  researcher: Found 7 key facts...                        |
|  planner: Outline complete...                            |
|  [writing_stage]                                         |
|  writer: Blog post ready (1320 words)...                 |
|  [review_stage]                                          |
|  editor: Quality score: 8/10...                          |
|                                                          |
+----------------------------------------------------------+
|  Model: deepseek-v4-flash | Round: 3 | Ctrl+C: quit      |
+----------------------------------------------------------+
|  >                                                       |
+----------------------------------------------------------+
```

### Slash Commands

| Command | Description |
|---------|-------------|
| `/help` | Show available commands |
| `/exit` | Exit TUI |
| `/clear` | Clear message list |
| `/model` | Show current model |
| `/usage` | Show token usage |
| `/flow` | Show flow config |
| `/agents` | List agents |

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| Enter | Send message |
| Up/Down | Navigate input history |
| Ctrl+L | Clear screen |
| Ctrl+C | Exit |

## Non-Interactive Mode

```bash
# Single prompt
heron --prompt "Write a Hello World in Go" --flow .agents/flows/default.yml

# Pipe output to file
heron --prompt "Review this code..." --flow .agents/code_review.yml > review.md
```

## HTTP Server Mode

```bash
heron --serve --port 8080 --flow .agents/flows/default.yml
```

`--serve` requires a Flow config: without `--flow` (and without a
`.agents/flows/default.yml` in the working directory) the process prints
`--serve requires a new-format Flow config` and exits 1.

Endpoints (registered in `cmd/server/main.go`):

| Method | Path | Purpose |
|--------|------|---------|
| POST | `/api/run` | Start a run, or continue one when the body carries `flow_session_id` |
| POST | `/api/sessions` | Alias of `/api/run` |
| POST | `/api/sessions/turn` | Send another input to an existing session (`?session_id=`) |
| GET | `/api/status` | Session status (`?session_id=`) |
| GET | `/api/stream` | SSE stream of session events (`?session_id=`) |
| GET | `/api/recovery/status` | Recovery status (`?session_id=`) |
| POST | `/api/recovery` | Trigger recovery (`?session_id=`, body = `RecoveryRequest`) |
| POST | `/api/resume` | Resume a waiting session (`?session_id=`) |
| POST | `/api/approvals` | Answer a pending approval (`?session_id=`, body needs `approval_id`) |
| GET | `/api/result` | Durable current session state (`?session_id=`) |
| POST | `/api/cancel` | Cancel a session (`?session_id=`) |
| GET | `/api/tasks` | Async Tool task state (`?task_id=`) |
| POST | `/api/tasks/cancel` | Cancel an async Tool task (`?task_id=`) |
| GET | `/api/tasks/stream` | SSE stream for one Tool task (`?task_id=`) |

**There are no path-parameter routes.** `session_id` and `task_id` are always
query parameters (`/api/status?session_id=fs_001`), never `/api/run/{id}`.
A client written against path-style URLs gets 404 on every call.

`/api/run` and `/api/sessions/turn` take `input` (or `message`, or `content`/
`attachments`) in the JSON body; `/api/run` additionally accepts `flow_id` and
`flow_session_id` in the body.

## JSON-RPC CLI Mode

`--json-rpc` starts a long-lived CLI process for external callers such as
`heron-connect`. It uses JSON-RPC 2.0 messages framed as JSONL/NDJSON:

```bash
heron --json-rpc --flow .agents/flows/default.yml
```

stdin:

```json
{"jsonrpc":"2.0","id":1,"method":"turn","params":{"input":"检查项目"}}
```

stdout:

```json
{"jsonrpc":"2.0","id":1,"result":{"session_id":"fs_001","flow_turn_id":"ft_001","status":"waiting_input","reply":"已完成检查。"}}
```

Rules:

- one complete JSON object per line;
- stdout contains protocol messages only;
- logs go to stderr;
- the first `turn` without `session_id` creates a FlowSession;
- later turns send the returned `session_id`; a normally finished turn
  leaves the session in `waiting_input`, so the same `session_id` can be
  reused as a permanent chat thread ID;
- `flow.jsonl`, `team.jsonl` and `agent.jsonl` remain internal storage
  formats and are not sent directly over stdout.

## Knowledge Commands

Knowledge 是"跨会话固化知识"（区别于 state 的"待办"）。生产 = 从 session 事件提炼，消费 = agent 检索注入。详见 `docs/generic-engine/24-knowledge-trigger-design.md`。

```bash
# 学一个会话（增量：只学上次之后的新事件）
heron knowledge <flow_session_id> --flow .agents/flows/default.yml

# 学所有未学过的会话（增量，逐个处理）
heron knowledge --flow .agents/flows/default.yml

# 归档过期知识（只按 ExpiresAt 判断：已过期即降级 archived）
heron knowledge gc --flow .agents/flows/default.yml
```

语义：

- **学习**：从 session 事件流提炼 SharedRecord → LLM 总结成知识条目 → 按事件来源层（flow/team/agent）落到 `.agents/knowledge/<层>/<id>.md`。
- **增量**：记录每个 session 学到哪个 seq（`.agents/knowledge/learn-progress.jsonl`），续聊后只学新增部分。
- **gc**：归档不是删除。知识降级为 `archived`，文件保留。

**gc 只有一个判据**：条目自身的 `ExpiresAt` 已过（RFC3339、UTC now 之后）。
没有 `--window` 参数，也不再有"年龄 + 0 命中"规则 —— 检索已改为 agentic
search（模型用 Read/Grep/Glob 直接读知识文件），没有 injector 能观察到"命中"，
命中统计（`stats.jsonl`）连同旧检索路径一起删除了。**年龄本身不再触发归档**：
没有 `ExpiresAt` 的条目会被永久保留，这是刻意取的保守方向。

## Runtime Data

Each FlowSession produces data in `.agents/data/sessions/<flow_session_id>/`:

```
.agents/data/sessions/<flow_session_id>/
├── flow.jsonl                    # flow_session.* / flow_turn.* / team_session.* / team_turn.* / shared_record.*
├── team.jsonl                    # agent_session.* / agent_turn.*（token 消耗的事实源）
├── agent.jsonl                   # agent.model_response
├── teams/<team_id>/state.md      # Team 状态快照
├── agents/<team_id>/<call_id>/state.md   # Agent 状态快照
└── evidence.jsonl                # Flow-scope SharedRecord（有 Flow 级记录时才出现）
```

The three jsonl files are one append-only log split by producing layer: each
event goes to the file of the layer that produced it, but sequence numbers come
from a single per-session counter, so the three can always be re-merged into
one monotonic timeline. `flow.jsonl` carries the Flow/Team orchestration
timeline including `shared_record.*`; `team.jsonl` carries the per-Agent call
timeline (`agent_turn.completed` → `payload.call_result.Requests[]` is where
token usage lives); `agent.jsonl` carries model responses.

`evidence.jsonl` is separate from the three event streams: it holds the
append-only history of Flow-scope `SharedRecord`s (`kind` / `name` / `basis`),
and only appears once a Flow-scope record is published. These storage files are
separate from the JSON-RPC/JSONL stdin/stdout transport.
