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
heron --serve --port 8080

# Resume a waiting FlowSession
heron --prompt "Continue..." --session <flow_session_id> --flow .agents/flows/default.yml

# Knowledge: learn one session (incremental)
heron knowledge <flow_session_id> --flow .agents/flows/default.yml

# Knowledge: learn all unlearned sessions (incremental)
heron knowledge --flow .agents/flows/default.yml

# Knowledge: archive stale knowledge (default 15-day window)
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
heron --serve --port 8080
```

Endpoints:
- `POST /api/run` - Start a run
- `GET /api/run/{id}` - Query run status
- `GET /api/run/{id}/stream` - SSE streaming
- `POST /api/run/{id}/resume` - Resume after wait_input
- `POST /api/run/{id}/cancel` - Cancel run

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
- `session.jsonl` and `evidence.jsonl` remain internal storage formats and
  are not sent directly over stdout.

## Knowledge Commands

Knowledge 是"跨会话固化知识"（区别于 state 的"待办"）。生产 = 从 session 事件提炼，消费 = agent 检索注入。详见 `docs/generic-engine/24-knowledge-trigger-design.md`。

```bash
# 学一个会话（增量：只学上次之后的新事件）
heron knowledge <flow_session_id> --flow .agents/flows/default.yml

# 学所有未学过的会话（增量，逐个处理）
heron knowledge --flow .agents/flows/default.yml

# 归档过期知识（默认 15 天窗口：过期 且 0 命中的知识降级 archived）
heron knowledge gc [--window 15] --flow .agents/flows/default.yml
```

语义：

- **学习**：从 session 事件流提炼 SharedRecord → LLM 总结成知识条目 → 按事件来源层（flow/team/agent）落到 `.agents/knowledge/<层>/<id>.md`。
- **增量**：记录每个 session 学到哪个 seq（`.agents/knowledge/learn-progress.jsonl`），续聊后只学新增部分。
- **gc**：归档不是删除。知识降级为 `archived`，文件保留；命中数据（`.agents/knowledge/stats.jsonl`）保留，供后续分析。

## Runtime Data

Each FlowSession produces data in `.agents/data/sessions/<flow_session_id>/`:

```
.agents/data/sessions/<flow_session_id>/
├── session.jsonl      # Complete append-only session event log
└── evidence.jsonl     # Flow-scope SharedRecord history
```

`session.jsonl` is used for replay and recovery. `evidence.jsonl` is a
queryable summary of Flow-scope records. These storage files are separate from
the JSON-RPC/JSONL stdin/stdout transport.
