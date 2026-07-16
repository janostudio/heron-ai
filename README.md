# Heron AI - Multi-Agent Generic Engine

Build, orchestrate, and evaluate AI agents. Written in pure Go, no framework dependencies.

## Quick Start

```bash
# Install
npm install -g @qinghuangniao/heron-ai

# Set API key
export OPENAI_API_KEY=sk-your-key

# Run with builtin default agent
heron

# Run with a specific flow
heron --flow .agents/flows/blog.yml

# Non-interactive mode
heron --prompt "Hello" --flow .agents/flows/default.yml
```

## Examples

See the [examples](./examples/) directory for complete configurations:

| Example | Description | Agents |
|---------|-------------|--------|
| [simple-qa](./examples/simple-qa/) | Single agent Q&A | 1 |
| [code-review](./examples/code-review/) | Multi-agent code review | 3 |
| [blog-writer](./examples/blog-writer/) | 3-team content pipeline | 4 |

## Configuration

Heron uses a `.agents/` directory for all configuration:

```
.agents/
├── flows/         # Flow definitions (YAML)
├── teams/         # Team configurations (YAML)
├── agents/        # Agent definitions (Markdown + YAML frontmatter)
├── skills/        # Skill definitions
├── knowledge/     # Knowledge base files
├── rules/         # Global rules
├── models.json    # Model registry
└── settings.json  # Engine settings
```

### models.json

```json
{
  "model": "deepseek-v4-flash",
  "models": [
    {
      "name": "deepseek-v4-flash",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "${OPENAI_API_KEY}",
      "max_tokens": 64000
    }
  ]
}
```

## Architecture

```
Flow (orchestration)
  └── Stage (team execution)
        └── Team (agent scheduling)
              ├── parallel: agents run concurrently
              └── sequential: agents run in order, passing context
                    └── Agent (LLM turn loop)
                          └── Turn (LLM call + tool execution)
```

## CLI Usage

```
heron                          # TUI interactive mode
heron --flow <path>            # TUI with specific flow
heron --prompt <text> --flow <path>  # Non-interactive
heron --serve                   # HTTP API server
heron --version                 # Print version
```

## Development

```bash
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
go test ./...
```

## License

MIT
