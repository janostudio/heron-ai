# Configuration

All configuration lives in the `.agents/` directory.

## Directory Structure

```
.agents/
├── flows/              # Flow definitions
│   └── default.yml
├── teams/              # Team configurations
│   └── my_team.yml
├── agents/             # Agent definitions (Markdown + YAML frontmatter)
│   └── my_agent.md     # Single file form
│   └── my_agent/       # Directory form (self-contained)
│       ├── AGENT.md
│       ├── knowledge/  # Private knowledge
│       └── rules/      # Private rules
├── skills/             # Skill definitions
│   └── my_skill/
│       └── SKILL.md
├── knowledge/          # Global knowledge base
│   └── domain.md
├── rules/              # Global rules
│   └── safety.md
├── models.json         # Model registry
└── settings.json       # Engine settings
```

## Flow Config (YAML)

```yaml
name: my_flow
loop_max_rounds: 0  # 0 = unlimited

stages:
  - name: stage1
    team: team1
    on_signal:
      continue: stage2       # Route to next stage
      wait_input: null       # Pause for user
      goal_achieved: null    # End run
```

## Team Config (YAML)

```yaml
name: my_team

stages:
  - process: parallel        # parallel | sequential
    tasks:
      - name: task1
        agent: agent1
        description: "Do something: {{.Input}}"

  - process: sequential
    tasks:
      - name: task2
        agent: agent2
        description: "Aggregate results"
```

## Agent Config (Markdown + YAML)

```yaml
---
name: my_agent
persona:
  role: "Researcher"
  goal: "Find relevant information"
  backstory: "Experienced researcher with 10 years of expertise"
model:
  model: ${LLM_MODEL:-deepseek-v4-flash}
  temperature: 0.3
  max_tokens: 2048
  api_key: ${OPENAI_API_KEY}
  base_url: ${OPENAI_BASE_URL:-https://api.deepseek.com/v1}
tools:
  builtin:
    - Read
    - Write
    - Grep
    - Glob
    - TodoWrite
    - TodoRead
  custom: []
  mcp: []
skills:
  - deep_research
knowledge:
  - seo-guide
rules:
  - quality
loop:
  max_rounds: 5
  tool_mode: sequential
  timeout: 120s
structured_output:
  type: json
  schema:
    title:
      type: string
      required: true
hitl:
  enabled: false
  timeout: 5m
hooks:
  - event: on_start
    command: "echo 'Starting...'"
    timeout: 10s
handoffs:
  - other_agent
---

Agent instructions go here. Use {{.Persona.Role}} for template variables.
```

## models.json

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

- `model`: Default model name
- `models[]`: Available models with base_url, api_key, max_tokens
- `api_key` supports `${ENV_VAR}` syntax for environment variables

## settings.json

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

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `OPENAI_API_KEY` | API key for LLM provider | Required |
| `LLM_MODEL` | Override default model | From models.json |
| `OPENAI_BASE_URL` | Override API base URL | From models.json |
