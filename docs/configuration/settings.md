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
| `tools` | `LoadToolPolicyPhrases`, `LoadScriptSettings` | Extra Bash policy phrases, `Script` tool |
| `env` | `ConfigLoader.LoadGlobalEnv` | Env vars injected into every subprocess |

Sections that are **not** read by any code path are listed in
[Inert sections](#inert-sections). Writing them does not fail — the engine
ignores them — so they are documented here to stop readers from tuning a knob
that does nothing.

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
| `max_active_team_sessions` | one `FlowSession` | `8` | `per_task` Team instances kept open at once. Closed instances do not count. |
| `max_task_context_depth` | one `Flow` | `3` | How deeply `per_task` Team instances may nest. |

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
| `fetch.provider` | string | follows `provider` | `tinyfish` or `direct`. `direct` fetches locally without a third-party API. |
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
config cannot unblock a phrase the engine already refuses.

### tools.script

The `Script` tool lets a model orchestrate several synchronous tool calls in
one REPL call, keeping intermediate results out of the context. It is **off by
default**; the switch has to be explicit.

```json
{
  "tools": {
    "script": {
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

Write operations inside a script still pass the policy, Bash gate and approval
checks one by one.

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

## Inert sections

These sections appear in older configurations and in `configs/settings.json`.
No code path reads them; they are documented here rather than silently
interpreted.

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
    "script": { "enabled": false }
  },
  "env": {
    "WORKSPACE_ROOT": ".",
    "PARENT_DIR": ".."
  }
}
```
