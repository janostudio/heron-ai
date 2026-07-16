# CLI Usage

## Commands

```bash
# TUI interactive mode (default)
heron
heron --flow .agents/flows/default.yml

# Non-interactive mode
heron --prompt "Hello" --flow .agents/flows/default.yml

# HTTP server mode
heron --serve --port 8080

# Resume a previous run
heron --run <run_id> --prompt "Continue..."

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

## Runtime Data

Each run produces data in `.agents/data/{runID}/`:

```
.agents/data/{runID}/
├── run.jsonl          # Conversation log
├── run_state.json     # Run metadata
└── sessions/
    └── {team}-{agent}/
        └── state.json # Agent state
```

Run IDs are auto-generated as `YYYYMMDD-HHMMSS-XXXXXX`.
