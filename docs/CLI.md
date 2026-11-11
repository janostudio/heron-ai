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

# Knowledge: learn one session (incremental). Flags must come BEFORE the id:
# `heron knowledge <id> --flow X` would silently ignore --flow.
heron knowledge --flow .agents/flows/default.yml <flow_session_id>

# Knowledge: learn all unlearned sessions (incremental).
# No flags accepted: it always uses .agents/flows/default.yml from the cwd.
heron knowledge

# Knowledge: archive expired knowledge (ExpiresAt in the past).
# `gc` takes no flags at all.
heron knowledge gc

# Teams: list local + installed teams / install / remove / export
heron teams
heron teams add <source>
heron teams remove <name>
heron teams export <team>

# Sessions: list and pick one to resume
heron --list

# Validate configuration without starting a turn
heron --check-config --flow .agents/flows/default.yml

# Version
heron --version
```

## Flag reference

| Flag | Type | Default | Meaning |
|------|------|---------|---------|
| `--flow` | string | `.agents/flows/default.yml` | Flow config path. `default.yaml` is also accepted. |
| `--prompt` | string | empty | Run one FlowTurn and exit (non-interactive). |
| `--session` | string | empty | Resume an existing FlowSession by id. **Only honoured together with `--prompt`**; the TUI branch ignores it |
| `--model` | string | empty | Override the default model from `models.json`. |
| `--log-level` | string | from `settings.json` | `debug`, `info`, `warn`, `error`. |
| `--max-rounds` | int | `0` (use config) | Override the per-Agent max rounds. |
| `--list` | bool | false | List sessions and pick one to resume. |
| `--check-config` | bool | false | Load flow/team/agent definitions, print each Team binding's lifecycle, exit 0 on valid. |
| `--json-rpc` | bool | false | Long-lived JSON-RPC 2.0 over stdin/stdout. |
| `--serve` | bool | false | Start HTTP server mode. |
| `--host` | string | `127.0.0.1` | Bind address for `--serve`. |
| `--port` | string | `8080` | Port for `--serve`; resolved as flag → `$PORT` → `8080`. |
| `--auth-token` | string | `$HERON_SERVE_TOKEN` | Bearer token required by `--serve`. |
| `--exit-with-parent` | bool | false | `--serve` only: exit when the parent process goes away (use it when a supervisor started you). |
| `--input-format` | string | empty | Machine input format; only `stream-json`. |
| `--output-format` | string | empty | Machine output format; only `stream-json`. |
| `--server` | string | empty | HTTP Heron server URL for `stream-json` client mode. |
| `--version` | bool | false | Print version and exit. |

Some combinations are refused at startup rather than resolved silently:

- `--list` cannot be combined with `--session` or `--prompt`.
- `--json-rpc` cannot be combined with `--prompt` or `--serve`.
- `--input-format` / `--output-format` cannot be combined with `--prompt`,
  `--serve` or `--json-rpc`; `stream-json` requires both flags, plus `--flow`
  and `--server`.

`--host`, `--port` and `--auth-token` are only meaningful with `--serve`.

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
| `/exit` (`/quit`) | Exit TUI |
| `/clear` | Clear message list |
| `/model` | Show current model |
| `/usage` | Show token usage |
| `/flow` | Show flow config |
| `/agents` | List agents |
| `/welcome` | Redraw the welcome banner |

### Keyboard Shortcuts

| Key | Action |
|-----|--------|
| Enter | Send message |
| Up / Down | Recall older / newer input (in a multi-line input, Up first moves the cursor to the top line) |
| Ctrl+P / Ctrl+N | Aliases for Up / Down |
| Alt+Up / Alt+Down | Scroll the transcript (plain Up/Down is reserved for history) |
| Ctrl+L | Clear the message list (same as `/clear`; the display is not recoverable) |
| Home / End | Jump to the top / bottom of the transcript |
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

### Bind address and authentication

The server binds `127.0.0.1` by default. Every route on this server can start
an Agent turn, and an Agent turn runs Bash and Write on the host, so exposing
it is a deliberate act:

| Flag | Default | Meaning |
|------|---------|---------|
| `--host` | `127.0.0.1` | Interface to bind. Any non-loopback value requires a token. |
| `--auth-token` | `$HERON_SERVE_TOKEN` | Bearer token every request must present. |

A non-loopback `--host` without a token is refused at startup
(`refusing to serve on a non-loopback interface without --auth-token`, exit 1)
rather than warned about. With a token, requests need
`Authorization: Bearer <token>`; a missing or wrong token returns 401.

```bash
# local only (no token needed)
heron --serve --port 8080 --flow .agents/flows/default.yml

# reachable from the network, token required
HERON_SERVE_TOKEN=$(openssl rand -hex 24) \
  heron --serve --host 0.0.0.0 --port 8080 --flow .agents/flows/default.yml
```

Endpoints (registered in `cmd/server/main.go`):

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/` | Web UI (static assets, served from the same process) |
| GET | `/api/health` | Readiness plus `pending_wakeups` and `sessions_loaded` |
| POST | `/api/run` | Start a run, or continue one when the body carries `flow_session_id` |
| POST | `/api/sessions` | Alias of `/api/run` |
| POST | `/api/sessions/turn` | Send another input to an existing session (`?session_id=`) — **synchronous**, answers when the turn finishes |
| POST | `/api/sessions/{id}/events` | Accept a message and answer **202**; the turn runs in the background and reports on the event stream — the Web UI uses this |
| GET | `/api/sessions/{id}/record` | Paged session records (`?after_seq=`, `?limit=`) — the Web UI reads this |
| GET | `/api/status` | Session status (`?session_id=`) |
| GET | `/api/stream` | SSE stream of session events (`?session_id=`, `?follow=1` keeps it open across turns, `Last-Event-ID` resumes after a sequence) |
| GET | `/api/recovery/status` | Recovery status (`?session_id=`) |
| POST | `/api/recovery` | Trigger recovery (`?session_id=`, body = `RecoveryRequest`) |
| POST | `/api/resume` | Resume a waiting session (`?session_id=`) |
| POST | `/api/approvals` | Answer a pending approval (`?session_id=`, body needs `approval_id`) |
| GET | `/api/result` | Durable current session state (`?session_id=`) |
| GET | `/api/media` | Serve an attachment/image referenced by a message (`?path=`); the Web UI needs it to render images |
| POST | `/api/cancel` | Cancel a session (`?session_id=`). Also stops the Teams this process dispatched for it and the durable Tool tasks it still owns, and keeps the cancelled status from being overwritten by the turn that was in flight |
| GET | `/api/tasks` | Async Tool task state (`?task_id=`) |
| POST | `/api/tasks/cancel` | Cancel an async Tool task (`?task_id=`) |
| GET | `/api/tasks/stream` | SSE stream for one Tool task (`?task_id=`) |

The API is query-parameter based: `session_id` and `task_id` are query values
(`/api/status?session_id=fs_001`), never `/api/run/{id}`. The single exception is
`/api/sessions/{id}/record`, which takes the session id in the path because it
exists for the bundled Web UI's paging; a client written against path-style URLs
gets 404 on every other route.

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
{"jsonrpc":"2.0","id":1,"result":{"session_id":"fs_001","flow_turn_id":"ft_001","status":"idle","reply":"已完成检查。"}}
```

Rules:

- one complete JSON object per line;
- stdout contains protocol messages only;
- logs go to stderr;
- the first `turn` without `session_id` creates a FlowSession;
- later turns send the returned `session_id`; a normally finished turn
  leaves the session in `idle`, so the same `session_id` can be
  reused as a permanent chat thread ID;
- `flow.jsonl`, `team.jsonl` and `agent.jsonl` remain internal storage
  formats and are not sent directly over stdout.

## stream-json Client Mode

`--input-format stream-json --output-format stream-json` turns the CLI into a
thin client of a running `--serve` instance. Both flags are required together,
and so are `--flow` and `--server`; the Flow itself runs on the server, the
client only translates stdin/stdout JSONL into HTTP calls.

```bash
heron --input-format stream-json --output-format stream-json \
      --flow .agents/flows/default.yml \
      --server http://127.0.0.1:8080
```

Input lines are objects discriminated by `type`:

| `type` | Payload | Effect |
|--------|---------|--------|
| `user` | `message`, `content` or `input` | Starts or continues a turn via `POST /api/run`. Text and content blocks (text/image/file) are both accepted. |
| `approval` / `permission_response` | `approval_id` + `approved` (`true`/`false`), optional `reason`, `approver`, `channel` | Answers a pending approval via `POST /api/approvals`. |
| `resume` / `poll` | `session_id` (carried from earlier turns by default) | Re-reads durable state via `GET /api/result`. |

Output lines are one of:

```json
{"type":"result","subtype":"input_required","session_id":"fs_001","status":"idle","reply":"...","error":"","pending_approvals":[],"pending_tool_tasks":[],"records":[]}
{"type":"error","subtype":"parse_error","error":"..."}
```

`subtype` on a `result` tells the caller how the turn ended:
`success`, `input_required`, `permission_required`, `tool_waiting`, `failure`.
Error subtypes are `parse_error`, `invalid_content`, `invalid_input`,
`request_failed`, `invalid_approval`, `approval_failed`, `invalid_resume` and
`unsupported_input`.

`session_id` is sticky: once a result carries one, later `user` lines continue
that session without repeating it.

## Configuration Check

```bash
heron --check-config --flow .agents/flows/default.yml
```

Loads the same definitions the runtime loads, without building a model
provider and without touching a session, then prints every Team binding's
resolved lifecycle:

```
default      team=qa_team      lifecycle=resident
flow default: 1 bindings ok (0 per_task)
```

Exit code 0 means the configuration is valid; 1 prints the loader error to
stderr. Use it as a pre-deployment gate: a binding that declares
`lifecycle: per_task` without `inputs.user_message` fails here instead of on
the first real turn.

## Teams Commands

`heron teams` is a definition-management command: it lives next to the Teams you
wrote yourself and the ones installed from packages, and it runs before any Flow
config is resolved, so it works in a directory with no `.agents/` at all.
Packages install under `.agents/_teams/<scope>/`, which `LoadDefinitions` scans
in addition to `.agents/teams/`. Design and trade-offs:
`docs/generic-engine/30-team-sharing.md`.

```bash
# List installed packages (also the default when no subcommand is given)
heron teams

# Install from a local path or a remote source
heron teams add ./my-team-package
heron teams add https://github.com/<owner>/<repo>
heron teams add https://github.com/<owner>/<repo>#<ref>
heron teams add https://example.com/team.tar.gz

# Preview the parse without writing anything
heron teams add ./my-team-package --dry-run

# Remove an installed package
heron teams remove <name>
heron teams remove <name> --keep-agents

# Export a team as a package (tar.gz by default)
heron teams export <team>
heron teams export <team> -o dist/ --format dir --version 1.2.0
```

| Usage | Flags | Notes |
|-------|-------|-------|
| `heron teams` | — | Lists every Team from both roots — `.agents/teams/*.yml` (`source=local`) and `.agents/_teams/<scope>/` (installed packages) — as `TEAM / SOURCE / VERSION / STATUS`. `heron teams list` is rejected on purpose: list is the bare command. An empty result prints `no teams`. |
| `heron teams add <source>` | `--dry-run`, `--yes`, `--as <name>`, `--refresh` | `<source>` is a local directory, `https://github.com/<owner>/<repo>` (optional `#ref`), or an `https` `.tar.gz` / `.tgz` URL. |
| `heron teams remove <name>` | `--keep-agents` | Deletes the package directory; `--keep-agents` keeps `agents/`, `skills/` and `rules/` and removes only `teams/`. |
| `heron teams export <team>` | `-o`, `--format tar.gz\|dir`, `--version` | `-o` is a directory or a full path ending in `.tar.gz`. |

Source rules are enforced, not guessed:

- `http://` is refused; remote sources must be `https`.
- A non-GitHub URL must end in `.tar.gz` or `.tgz`.
- A GitHub source must be exactly `https://github.com/<owner>/<repo>`. The
  optional `#fragment` is the git ref (branch, tag or commit); it defaults to
  `HEAD` and is part of the download cache key, so two branches of one
  repository do not overwrite each other.

A package declares itself in `heron-team.yml`:

```yaml
apiVersion: heron/v1
kind: team
name: review-team
version: 0.1.0
description: Multi-reviewer code review team
runtime:
  lifecycle: resident      # resident (default) | per_task
provides:
  teams: [review_team]
  agents: [lead-reviewer, security-reviewer]
  skills: []
  rules: [review-standards]
requires: {}
```

`models.json` is never packaged: models, keys and endpoints belong to the
receiving side. Skill scripts can execute arbitrary commands, so `add` shows
them for review — `--yes` is what skips that prompt, and `sandbox.scripts`
(`allow` / `deny` / `review`) is what the package itself declares.

## Session List

```bash
heron --list
```

Lists FlowSessions from `.agents/data/sessions/` and, on a terminal, opens a
picker; the selected session resumes in TUI with the current `--flow`. When
stdin or stdout is not a terminal — pipes, CI, `--prompt` wrappers — the same
command prints a text table instead of opening a picker.

`--list` runs before any Flow config is resolved, so it works in a directory
that has no `.agents/` yet. An empty store prints `暂无会话` and exits 0.

## Knowledge Commands

Knowledge 是"跨会话固化知识"（区别于 state 的"待办"）。生产 = 从 session 事件提炼，
消费 = 模型用 Read/Grep/Glob 自取（agentic search，没有注入器）。配置与语义见
`docs/configuration/knowledge.md`。

```bash
# 学一个会话（增量：只学上次之后的新事件）。flag 必须写在 id 之前
heron knowledge --flow .agents/flows/default.yml <flow_session_id>

# 学所有未学过的会话（增量，逐个处理）。不接受任何 flag，用 cwd 的默认 flow
heron knowledge

# 归档过期知识（只按 ExpiresAt 判断：已过期即降级 archived）。不接受任何 flag
heron knowledge gc
```

语义：

- **学习**：从 session 事件流提炼 SharedRecord → LLM 总结成知识条目 → 按归属落盘：
  flow 级落 `.agents/knowledge/<id>.md`（扁平），team/agent 私有条目落
  `.agents/{teams,agents}/<owner>/knowledge/<id>.md`。旧的 `<层>/<id>.md` 布局不再写入。
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
├── flow.jsonl                    # session.* / session.turn.* / session.team_turn.* / session.record.* / mailbox.* / recovery.*
├── team.jsonl                    # session.agent_turn.* / command_turn / webhook_turn / approval.*（call 层与 token 消耗的事实源）
├── agent.jsonl                   # session.agent.model_response / session.tool_call.* / session.agent.feedback / session.context.compacted
├── teams/<team_id>/state.md      # Team 状态快照
├── agents/<team_id>/<call_id>/state.md   # Agent 状态快照
└── evidence.jsonl                # Flow-scope SharedRecord（有 Flow 级记录时才出现）
```

The lists above are representative, not exhaustive: an event is routed by the
layer that produced it, so anything the Agent layer emits (`tool_call.*`,
`agent.feedback`, `context.compacted`) lands in `agent.jsonl`, and anything the
Team layer emits (`approval.*`, command/webhook turns) lands in `team.jsonl`.

The three jsonl files are one append-only log split by producing layer: each
event goes to the file of the layer that produced it, but sequence numbers come
from a single per-session counter, so the three can always be re-merged into
one monotonic timeline. `flow.jsonl` carries the Flow/Team orchestration
timeline including `session.record.*`; `team.jsonl` carries the per-Agent call
timeline (`session.agent_turn.completed` → `payload.call_result.requests[]` is where
token usage lives); `agent.jsonl` carries model responses.

`evidence.jsonl` is separate from the three event streams: it holds the
append-only history of Flow-scope `SharedRecord`s (`kind` / `name` / `basis`),
and only appears once a Flow-scope record is published. These storage files are
separate from the JSON-RPC/JSONL stdin/stdout transport.
