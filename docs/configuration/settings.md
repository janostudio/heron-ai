# settings.json

Engine behavior configuration. Optional — every section is read independently,
and a missing file or missing section is a legal state that falls back to
defaults.

Settings live at `.agents/settings.json` under the workspace root, next to
`models.json`. The two files are not interchangeable: `models.json` is the
model registry (endpoints and keys), `settings.json` is engine behavior.

## Sections at a glance

| Section | Read by | Effect |
|---------|---------|--------|
| `logging` | `ConfigLoader.LoadLoggingSettings` | Execution log level, destination, rotation |
| `runtime` | `ConfigLoader.LoadRuntimeLimits` | Safety backstops on orchestration loops |
| `knowledge` | `ConfigLoader.LoadKnowledgeSettings` | Which model summarizes learned knowledge |
| `web` | `ConfigLoader.LoadWebSettings` | Data source for the `WebSearch` / `WebFetch` tools |
| `mcp` | `ConfigLoader.LoadMCPServers` | MCP servers connected at startup |
| `tools` | `LoadToolPolicyPhrases`, `LoadScriptSettings` | Extra Bash policy phrases, `REPL` tool (`tools.repl`) |
| `permissions` | `ConfigLoader.LoadPermissions` | Global posture: what a run on this machine may do without a human |
| `env` | `ConfigLoader.LoadGlobalEnv` | Env vars injected into every subprocess |

Sections that are **not** read by any code path are listed in
[Inert sections](#inert-sections). Writing one does not fail, but it is not
silent either: every key no section reads is reported at startup and by
`heron --check-config`, together with the closest accepted name when the key
looks like a typo. See [Unknown keys](#unknown-keys).

## logging

```json
{
  "logging": {
    "level": "info",
    "dir": ".agents/data/logs",
    "max_file_size": "50MB",
    "max_backups": 0,
    "retention_days": 7
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `info` | Log level: `debug`, `info`, `warn`, `error`. Overridable per process with `--log-level`. |
| `dir` | string | `.agents/data/logs` | Log directory, relative to the workspace root or absolute. |
| `max_file_size` | string | `50MB` | Size before rotation. Accepts B/KB/MB/GB. |
| `max_backups` | integer | `0` | Rotated files kept per day. `0` means unlimited. |
| `retention_days` | integer | `7` | Days to keep log files; negative disables cleanup. |

Logs always go to `dir`: there is no switch that sends them to stdout, which is
what keeps `--json-rpc` and `stream-json` stdout clean for protocol frames.
See `logging.output` under [Inert sections](#inert-sections).

Log files are named by date (`YYYY-MM-DD.log`) and gain a sequence suffix
(`YYYY-MM-DD.1.log`) once they exceed `max_file_size`. Files older than
`retention_days` are removed on rotation.

`model request usage` lines are written at `debug` only — the authoritative
token facts are in `team.jsonl`, not in the log.

## runtime

Safety backstops for Flow / Team / Agent turns. These are **not orchestration
knobs**: a legitimate Flow hands work back and forth many times and must not
trip them, so the defaults sit far above any plausible legitimate count. Lower
them only when you want a runaway loop to fail earlier.

```json
{
  "runtime": {
    "max_team_turns": 100,
    "max_calls_per_team_turn": 100,
    "max_agent_rounds": 200,
    "max_parallel_teams": 20,
    "max_parallel_calls": 20,
    "max_parallel_tools": 20,
    "max_coordinate_retries": 20,
    "max_active_team_sessions": 8,
    "max_task_context_depth": 3
  }
}
```

| Field | Scope | Default | Meaning |
|-------|-------|---------|---------|
| `max_team_turns` | one `FlowTurn` | `100` | Team turns one FlowTurn may run. Exceeding it fails that turn; the session can still start a new turn. |
| `max_calls_per_team_turn` | one `TeamTurn` | `100` | Agent / Command / Webhook calls one TeamTurn may execute. |
| `max_agent_rounds` | one `AgentTurn` | `200` | Model/tool loop iterations inside one AgentTurn. |
| `max_parallel_teams` | one batch | `20` | Teams running at once. Activations beyond it are deferred to the next batch, not dropped. |
| `max_parallel_calls` | one `TeamTurn` | `20` | Calls running at once. Calls beyond it are deferred to the next batch, not dropped. |
| `max_parallel_tools` | one `AgentTurn` | `20` | Tool calls running at once inside the agent loop. |
| `max_coordinate_retries` | per Team, per `FlowTurn` | `20` | How often one Team may hand a decision back to its activator. Over the limit that route is **skipped**; the turn itself does not fail. |
| `max_active_team_sessions` | one `FlowSession` | `8` | `isolated` Team instances kept open at once. Closed instances do not count. |
| `max_task_context_depth` | one `Flow` | `3` | How deeply `isolated` Team instances may nest. |

`max_team_turns` is deliberately per-turn, not a session total: a session-wide
counter would eventually make a long-running conversation stop accepting input,
which is a worse failure than a bounded loop.

## knowledge

```json
{
  "knowledge": {
    "summary_model": "gpt-4o-mini"
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `summary_model` | string | empty | Model name (from `models.json`) used by `heron knowledge` to summarize sessions into knowledge entries. Empty = the default model. |

## web

Configures the data source behind the builtin `WebSearch` and `WebFetch` tools.
An agent only gets these tools when its `AGENT.md` lists them under
`tools.builtin`.

```json
{
  "web": {
    "provider": "duckduckgo",
    "api_key": "${WEB_SEARCH_API_KEY}",
    "search": { "max_results": 10, "timeout_ms": 30000 },
    "fetch": { "provider": "direct", "format": "markdown", "timeout_ms": 45000 }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `provider` | string | `duckduckgo` | `duckduckgo`, `tinyfish`, `brave`, `tavily`. |
| `api_key` | string | `$WEB_SEARCH_API_KEY` | Key for the selected provider. Resolution order: `web.api_key` → `WEB_SEARCH_API_KEY` → the same name in the `env` section. |
| `search.url` | string | provider default | Override the search endpoint. |
| `search.max_results` | integer | `10` | Result cap. |
| `search.location` | string | empty | Country code, TinyFish only. |
| `search.language` | string | empty | Language code, TinyFish only. |
| `search.timeout_ms` | integer | `30000` | Search timeout. |
| `fetch.provider` | string | follows `provider` | `tinyfish`、`tavily` 或 `direct`。`direct` 在本地直连抓取，不经第三方 API。 |
| `fetch.url` | string | provider default | Override the fetch endpoint. |
| `fetch.format` | string | `markdown` | `markdown`, `html`, `json`. |
| `fetch.timeout_ms` | integer | `45000` | Fetch timeout. |

`duckduckgo` needs no key and is the default, but it is an Instant Answer API
rather than a web search: it answers entity/encyclopedia queries and returns
nothing for most others. `tinyfish`, `brave` and `tavily` need a key and fail
closed without one. A key for one provider is never reused for another.

`fetch.provider` empty means "follow `web.provider`". `brave` and `duckduckgo`
expose no fetch API, so fetch silently lands on `direct` — a local direct fetch
that never sends the URL to a third party — instead of failing. That fallback
is also the reason `fetch.provider: "direct"` is the usual pairing with a
search-only provider. Provider-by-provider capabilities are tabulated in
[agent.md](./agent.md#web-data-source-for-websearch--webfetch).

## mcp

MCP servers are connected once at startup. Each entry spawns a stdio process or
talks to an HTTP endpoint; the tools they expose become available to agents
that list the server under `tools.mcp`.

```json
{
  "mcp": [
    {
      "name": "github",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"],
      "env": { "GITHUB_TOKEN": "..." }
    },
    {
      "name": "issues",
      "transport": "http",
      "url": "https://mcp.example.internal/issues",
      "headers": { "Authorization": "Bearer ..." }
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Server name; this is what agents reference. |
| `transport` | string | `stdio` or `http`. `sse` is not supported and is rejected at startup. |
| `command` / `args` | string / array | stdio only: the process to spawn. |
| `url` | string | http only: endpoint. |
| `headers` | object | http only: request headers. |
| `env` | object | stdio only: extra environment for the process. |

A missing `mcp` section and an empty `mcp` array mean the same thing: no
servers. A malformed section is an error, not a silent skip.

## tools

### tools.restricted_bash_phrases

```json
{
  "tools": {
    "restricted_bash_phrases": ["prod-deploy", "drop database"]
  }
}
```

Extra phrases merged into the builtin Bash safety floor. The list only grows —
config cannot unblock a phrase the engine already refuses. It is a *per-command*
backstop; the broader question ("may runs on this machine use a shell at all") is
the `permissions` section [below](#permissions).

### tools.repl

The `REPL` tool lets a model orchestrate several synchronous tool calls in
one REPL call, keeping intermediate results out of the context. It is **off by
default**; the switch has to be explicit.

```json
{
  "tools": {
    "repl": {
      "enabled": false,
      "timeout_ms": 60000,
      "max_steps": 200,
      "max_output_bytes": 102400,
      "catalog_budget": 8000
    }
  }
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | boolean | `false` | Turn the tool on. |
| `timeout_ms` | integer | `60000` | Compute budget for one script, excluding tool call time. Cap 600000. |
| `max_steps` | integer | `200` | Tool calls allowed inside one script. |
| `max_output_bytes` | integer | `102400` | Cap on return value plus console output. |
| `catalog_budget` | integer | `8000` | Character budget for the tool catalog injected with the `code` parameter. |

### When it is worth a program

A program costs one parse and one extra hop; what it saves is round trips and
context. Measured on a 40-file aggregation: eight programs wrapped 118 tool calls,
622 KB of intermediate output never entered the context, and only 16 KB (the return
values) did — 10 model requests instead of one per call.

The rule that falls out of that, and that the tool description also states:

- **more than about three calls of the same kind, or intermediate output bigger
  than ~10 KB** → one program;
- **one or two calls with a small result** → call the tool directly; wrapping it
  costs more than it saves.

`REPL` has no permission settings of its own. A program runs under the same
posture as a direct call, taken from the `permissions` section of this file
([below](#permissions)):

- `allow`: that call runs unattended;
- `ask`: the program **pauses at that step** and the turn parks as
  `waiting_approval`. Approving re-issues the same program with the step marked
  approved: finished steps are replayed (no repeated side effects) and execution
  continues from there. A second `ask` later in the program pauses it again;
- `deny`: the tool is not bound inside the program at all.

Per-command rules still apply to each statement: the shell floor, the `.env` rule,
the path restriction and `PreToolUse` hooks all run at the inner call — an inner
`ask` from any of them pauses the program the same way.

## permissions

The one place that decides what a run may do without a human. It is global to the
runtime — every Flow and every Agent started from this `.agents/` inherits it —
because the question is about the machine ("may runs here execute commands"), not
about a task. A per-Flow or per-Agent answer would be the same answer written
several times.

```json
{
  "permissions": {
    "bash": "allow",
    "write": "ask",
    "websearch": "allow",
    "webfetch": "allow"
  }
}
```

| Field | Type | Default | Governs |
|-------|------|---------|---------|
| `bash` | string | `allow` | the `Bash` tool |
| `write` | string | `allow` | the `Write` tool |
| `websearch` | string | `allow` | the `WebSearch` tool |
| `webfetch` | string | `allow` | the `WebFetch` tool |

Each field is named after the tool it governs — there is no shared "network" or
"shell" bucket to decode. Each takes `allow`, `ask` or `deny`:

| Decision | Direct call | Inside a `REPL` program |
|----------|-------------|-------------------------|
| `allow` | runs | runs |
| `ask` | parks for one approval | the program **pauses at that step**; the turn parks as `waiting_approval`, and the resumed run replays the finished steps and executes the approved one |
| `deny` | refused, and the tool is withheld from the model | not bound at all |

### What this section does not cover

- **A value is per field, not a global switch.** There is no
  `"permissions": "allow"`. Each governed tool has its own line, and a tool with no
  line is not governed at all.
- **Only four tools are governed.** `Read`, `Grep`, `Glob`, `CodeNav`, `TodoWrite`,
  `TodoRead`, `State`, `AskUserQuestion`, `Spawn` and the Team tools cannot change
  anything outside the conversation, so they run without asking and without a knob.
  A tool that is not in the table above keeps its own behaviour.
- **`REPL` is not here, by design.** It is not a capability: it is a program that
  calls the tools above. Whether a program may do something is answered per step
  while it runs — a program that only reads runs free, and one that reaches `Bash`
  or `Write` inherits that tool's decision at the step where it happens. Adding a
  `repl` line would only create a second place to say the same thing.
- **Omitting the section means `allow` everywhere**, which is what every
  installation did before the section existed.
- **Two things are not configurable**, because a run that needs them needs a human:
  a shell command containing `rm -rf`, `sudo `, `shutdown`, `mkfs` or
  `git reset --hard`, and a write to `.env`. Inside a program they behave like any
  other `ask`: the program pauses at that step.
  `tools.restricted_bash_phrases` adds phrases to the first, never removes these.

One approval covers the whole program, and that includes the two floor rules above:
a program approved under `bash: ask` may run a command the engine would otherwise
ask about. The alternative — stopping at the first such statement — is the
half-finished program the design removes. The approval reason says so, so the human
agrees to the program, not to a list of statements they cannot see.

`--check-config` rejects a value that is not one of the three decisions: a
misspelled posture would otherwise fall back to `allow` and read as a restriction
that is not there.

## env

Environment variables injected into every subprocess the engine starts —
command calls, the `Bash` tool, MCP stdio servers, hooks and CodeNav.

```json
{
  "env": {
    "WORKSPACE_ROOT": ".",
    "PARENT_DIR": "..",
    "STATE_DIR": "/var/lib/heron/state"
  }
}
```

**Every value is resolved as a path**, relative to the workspace root (the
process cwd): `"."` is the workspace root itself, `".."` is its parent, an
absolute value is kept as-is. This is what makes the value usable by a
subprocess regardless of its own `cwd`.

The consequence is the trap: a non-path value gets mangled. `"MODE": "prod"`
becomes `<workspace>/prod`, and `"FLAG": "1"` becomes `<workspace>/1`. This
section is for path anchors, not for arbitrary settings — put other variables in
the process environment instead.

Explicitly configured values win over the process's existing environment.

## Unknown keys

Any key that no section reads is reported, not rejected:

```text
$ heron --check-config
warning: .agents/settings.json: unknown setting "runtme" (did you mean "runtime"?)
warning: .agents/settings.json: unknown setting "web.timeout_ms"
```

The same lines go to the execution log at startup (`warn` level), so a server
that is already running reports its configuration problems in the log it
already collects.

- Paths are exact: `mcp[0].arg` names an array element, `web.timeout_ms` names a
  nested key.
- A suggestion appears only when the name is close to an accepted one — an edit
  distance of at most two, or one for keys of four characters or fewer — and an
  adjacent-character swap counts as one edit (`nmae` → `name`).
- Names are matched the way `encoding/json` matches them, so a case difference
  (`Web`) is read rather than reported.
- Map keys are user data, not schema: `env` and `headers` accept any name.
- Reporting is not failure: a settings file written for a newer build still
  starts. It is only a statement that the key has no reader.

## Inert sections

These sections appear in older configurations. No code path reads them, and
[the unknown-key report](#unknown-keys) names them when they are present. They
stay documented so a reader can tell "this was removed" from "this was never
here". `configs/settings.json` in this repository contains none of them.

| Section | Status | Why |
|---------|--------|-----|
| `logging.output` | Not read | `buildLogger` passes level/dir/rotation to the rotating logger and drops `output`. Logs always go to the log directory; there is no stdout branch. |
| `paths.data` | Not read | The runtime data directory is fixed at `.agents/data/` (`internal/sessionlist/list.go`, `internal/tool/path_scope.go`). Changing it is not supported. |
| `agent.max_parallel`, `agent.tracing`, `agent.default_loop` | Not read | Per-agent limits live in the agent definition (`AGENT.md`) and in `runtime.max_agent_rounds`; there is no global agent section. |
| `observability` | Removed | The `internal/observability` package was deleted. `observability.retention_days` never had a reader; `logging.retention_days` is the live one. |
| `flow`, `team` | Not read | Per-team limits come from the Team config (`teams/*.yml`) and `runtime.*`. |

There is currently no configuration for run-data retention.

## Complete example

```json
{
  "logging": {
    "level": "info",
    "dir": ".agents/data/logs",
    "max_file_size": "50MB",
    "max_backups": 0,
    "retention_days": 7
  },
  "runtime": {
    "max_team_turns": 100,
    "max_calls_per_team_turn": 100,
    "max_agent_rounds": 200
  },
  "knowledge": {
    "summary_model": "gpt-4o-mini"
  },
  "web": {
    "provider": "brave",
    "api_key": "${WEB_SEARCH_API_KEY}",
    "fetch": { "provider": "direct" }
  },
  "mcp": [],
  "tools": {
    "restricted_bash_phrases": ["prod-deploy"],
    "repl": { "enabled": false }
  },
  "permissions": {
    "bash": "ask",
    "write": "allow",
    "websearch": "allow",
    "webfetch": "allow"
  },
  "env": {
    "WORKSPACE_ROOT": ".",
    "PARENT_DIR": ".."
  }
}
```
