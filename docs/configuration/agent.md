# Agent Configuration

Agents are reusable model and prompt definitions. Every Agent uses a
directory. The main configuration file is always `AGENT.md`; private
knowledge, rules, memory and future extensions live beside it.

## Standard Directory Form

```
.agents/agents/my_agent/
├── AGENT.md              # Required Agent definition
├── knowledge/            # Private knowledge
│   ├── index.md          # Knowledge index
│   └── domain.md
└── rules/                # Private rules
    └── guidelines.md
```

An Agent directory may also contain:

```text
.agents/agents/my_agent/
├── skills/               # Agent-local skills, when needed
├── memory/               # Optional persistent memory files
├── scripts/              # Agent-local helper scripts
└── extensions/           # Optional Agent-specific extensions
```

Only `AGENT.md` is required. Other directories are optional and are loaded by
the corresponding extension.

### Canonical path

```text
.agents/agents/<agent-id>/AGENT.md
```

The directory name is the Agent's storage identity. The `name` in frontmatter
must match the directory name. This keeps each Agent's knowledge and rules
isolated and makes the configuration easy to locate.

### Legacy compatibility

Older projects may still contain:

```text
.agents/agents/my_agent.md
```

Heron continues to read this path for migration compatibility. New projects
and new Agent definitions must use the directory form. A project should not
define both `my_agent.md` and `my_agent/AGENT.md` with the same Agent name.

Migration:

```text
my_agent.md
  → my_agent/AGENT.md
  → mkdir my_agent/knowledge
  → create my_agent/knowledge/index.md
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
  # 可选：未配置时从 models.json 继承
  # temperature: 0.3
  # max_output_tokens: 8192
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

The content above belongs in:

```text
.agents/agents/my_agent/AGENT.md
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
| `temperature` | float | 覆盖 models.json 的默认值 |
| `top_p` | float | 覆盖模型默认 top-p |
| `top_k` | integer | 覆盖模型默认 top-k |
| `repetition_penalty` | float | 覆盖模型默认重复惩罚 |
| `reasoning` | object | 覆盖模型默认推理配置 |
| `max_output_tokens` | integer | 覆盖模型的单次输出上限 |
| `max_tokens` | integer | 旧字段兼容；新配置使用 `max_output_tokens` |
| `api_key` | string | 特殊场景覆盖模型 API Key |
| `base_url` | string | 特殊场景覆盖模型 API 地址 |

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

## Private Knowledge

Private Agent knowledge belongs below the Agent directory:

```text
.agents/agents/my_agent/knowledge/
├── index.md
└── domain.md
```

`index.md` is the navigation/index file and is not itself treated as a
knowledge entry. Agent-private knowledge is loaded with the Agent's scope and
is not automatically exposed to other Agents.
