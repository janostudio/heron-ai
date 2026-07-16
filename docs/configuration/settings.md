# settings.json

Engine behavior configuration. Optional - defaults are used if not present.

## Structure

```json
{
  "logging": {
    "level": "info",
    "output": "stdout",
    "max_file_size": "50MB",
    "max_backups": 5
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
| `output` | string | `stdout` | Output: stdout or file path |
| `max_file_size` | string | `50MB` | Max log file size before rotation |
| `max_backups` | integer | `5` | Number of rotated files to keep |

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
