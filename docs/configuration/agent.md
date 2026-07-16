# Agent Configuration

Agents are the core workers. Defined in Markdown with YAML frontmatter.

## File Forms

### Single file (simple agents)
```
.agents/agents/my_agent.md
```

### Directory form (self-contained agents)
```
.agents/agents/my_agent/
├── AGENT.md              # Agent definition
├── knowledge/            # Private knowledge
│   └── domain.md
└── rules/                # Private rules
    └── guidelines.md
```

## Full Configuration

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

Agent body text (Markdown). Template variables available:
{{.Persona.Role}} {{.Persona.Goal}} {{.Persona.Backstory}}
{{range .Rules}} - {{.Content}} {{end}}
```

## Fields

### persona

| Field | Type | Description |
|-------|------|-------------|
| `role` | string | Agent's role name |
| `goal` | string | Primary objective |
| `backstory` | string | Background context |

### model

| Field | Type | Description |
|-------|------|-------------|
| `model` | string | Model name. Supports `${VAR:-default}` |
| `temperature` | float | 0.0-1.0. Lower = more deterministic |
| `max_tokens` | integer | Max response tokens |
| `api_key` | string | API key. Supports `${ENV_VAR}` |
| `base_url` | string | API base URL |

### tools

| Field | Type | Description |
|-------|------|-------------|
| `builtin` | array | Built-in tool names |
| `custom` | array | Custom tool names |
| `mcp` | array | MCP server tool names |

Built-in tools: `Read`, `Write`, `Grep`, `Glob`, `TodoWrite`, `TodoRead`

### loop

| Field | Type | Description |
|-------|------|-------------|
| `max_rounds` | integer | Max LLM turns per execution |
| `tool_mode` | string | `sequential` or `parallel` |
| `timeout` | string | Execution timeout |

### structured_output

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Output format, usually `json` |
| `schema` | object | JSON Schema definition |

### hitl

| Field | Type | Description |
|-------|------|-------------|
| `enabled` | boolean | Enable human-in-the-loop |
| `timeout` | string | Approval timeout |

### hooks

| Field | Type | Description |
|-------|------|-------------|
| `event` | string | Hook event: `on_start`, `on_end`, `on_tool_start`, `on_error` |
| `command` | string | Shell command to execute |
| `timeout` | string | Command timeout |

### handoffs

List of agent names this agent can delegate tasks to.
