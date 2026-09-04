# settings.json

Engine behavior configuration. Optional - defaults are used if not present.

## Structure

```json
{
  "logging": {
    "level": "info",
    "output": "file",
    "dir": ".agents/data/logs",
    "max_file_size": "50MB",
    "max_backups": 5,
    "retention_days": 7
  },
  "observability": {
    "retention_days": 30,
    "event_bus_size": 256
  },
  "paths": {
    "data": ".agents/data/"
  },
  "agent": {
    "max_parallel": 10,
    "tracing": {
      "enabled": true,
      "sample_rate": 1.0
    },
    "default_loop": {
      "max_rounds": 10,
      "timeout": "120s",
      "tool_mode": "sequential"
    }
  }
}
```

## Fields

### logging

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `level` | string | `info` | Log level: debug, info, warn, error |
| `output` | string | `file` | Output: stdout or file |
| `dir` | string | `.agents/data/logs` | Log directory (relative to workspace root or absolute) |
| `max_file_size` | string | `50MB` | Max log file size before rotation (supports B/KB/MB/GB) |
| `max_backups` | integer | `5` | Number of rotated files to keep per day |
| `retention_days` | integer | `7` | Days to keep log files; negative disables cleanup |

Log files are named by date (`YYYY-MM-DD.log`) and split with a sequence suffix
(`YYYY-MM-DD.1.log`) once they exceed `max_file_size`. Files older than
`retention_days` are removed on rotation.

### observability

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `retention_days` | integer | `30` | Days to keep run data before cleanup |
| `event_bus_size` | integer | `256` | Event bus channel buffer size |

### paths

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `data` | string | `.agents/data/` | Runtime data directory |

### agent

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `max_parallel` | integer | `10` | Max parallel agents |
| `tracing.enabled` | boolean | `true` | Enable tracing |
| `tracing.sample_rate` | float | `1.0` | Trace sampling rate (0.0-1.0) |
| `default_loop.max_rounds` | integer | `10` | Default max LLM turns per agent |
| `default_loop.timeout` | string | `120s` | Default agent timeout |
| `default_loop.tool_mode` | string | `sequential` | Tool execution mode |

## Flow / Team / AgentTurn limits

```json
{
  "runtime": {
    "max_team_turns": 20,
    "max_calls_per_team_turn": 20,
    "max_agent_rounds": 200,
    "max_parallel_teams": 20,
    "max_parallel_calls": 20,
    "max_parallel_tools": 20
  }
}
```

These limits have different scopes:

| Field | Scope | Meaning |
|---|---|---|
| `max_team_turns` | one `FlowTurn` | Maximum TeamTurns scheduled by this FlowTurn |
| `max_calls_per_team_turn` | one `TeamTurn` | Maximum Agent/Command/Webhook calls executed by this TeamTurn |
| `max_agent_rounds` | one `AgentTurn` | Maximum Model/Tool loop iterations inside one AgentTurn |

`max_parallel_teams`, `max_parallel_calls`, and `max_parallel_tools` only limit concurrency. They do
not change the counted units. The default values are `20 / 20 / 200`.
