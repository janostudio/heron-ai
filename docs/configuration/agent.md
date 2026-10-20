# Agent Configuration

Agents are reusable model and prompt definitions. Every Agent uses a
directory. The main configuration file is always `AGENT.md`; private
knowledge, rules, state and future extensions live beside it.

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
├── state/               # Optional persistent state files
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
  tool_execution: sequential
  max_parallel_tools: 5
  async_tools: []
  timeout: 120s
context:
  # 按当前模型能力比例控制 Active Context
  target_ratio: 0.70
  compaction_threshold: 0.80
  hard_limit_ratio: 0.90
  output_reserve_ratio: 0.15
  tool_output_ratio: 0.10
  max_tool_output_chars: 65536
budget:
  max_model_rounds: 30
  max_tool_calls: 100
  max_wall_time: 10m
  max_input_tokens: 100000
  max_output_tokens: 20000
  max_file_changes: 100
  max_tool_output: 1000000
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

Built-in tools: `Read`, `Write`, `Grep`, `Glob`, `Bash`, `WebSearch`,
`WebFetch`, `CodeNav`, `AskUserQuestion`, `TodoWrite`, `TodoRead`, `State`

### web (data source for `WebSearch` / `WebFetch`)

The web tools' data source is **not** configured on the agent: it lives in the
`web` section of `.agents/settings.json` and is shared by every agent. The
default provider is keyless, so `WebSearch` works out of the box with an empty
`web` section; the smallest configuration that sets anything is a single key:

```json
{ "web": { "api_key": "..." } }
```

Note the trap: the snippet above does **not** switch providers — `provider` is
still `duckduckgo`, which is keyless, so the key is simply ignored. To use a
keyed provider you have to name it explicitly:

```json
{ "web": { "provider": "tinyfish", "api_key": "..." } }
```

| provider | WebSearch | WebFetch | key | built-in endpoint |
|----------|-----------|----------|-----|-------------------|
| `duckduckgo` | yes (**default**) | no | keyless | `https://api.duckduckgo.com/` |
| `tinyfish` | yes | yes | required | `api.search.tinyfish.ai` / `api.fetch.tinyfish.ai` |
| `brave` | yes | no | required | `https://api.search.brave.com/res/v1/web/search` |
| `tavily` | yes | yes | required | `api.tavily.com/search` / `api.tavily.com/extract` |
| `direct` | — | yes | keyless | local direct fetch, no external API |

`provider` governs search. Fetch follows it by default, and since `brave` and
`duckduckgo` expose no fetch API they fall back to `direct` (a local direct
fetch) instead of erroring. Write `fetch.provider: direct` to force local
fetching regardless of the search provider.

| Field | Type | Description |
|-------|------|-------------|
| `provider` | string | `duckduckgo` (default) / `tinyfish` / `brave` / `tavily` |
| `api_key` | string | Provider API key; required by `tinyfish`, `brave`, `tavily` |
| `search.url` | string | Override the search endpoint; empty = provider built-in |
| `search.max_results` | integer | Result cap, default `10` |
| `search.location` | string | Country code, `tinyfish` only |
| `search.language` | string | Language code, `tinyfish` only |
| `search.timeout_ms` | integer | Per-request timeout, default `30000` |
| `fetch.provider` | string | `tinyfish` / `tavily` / `direct`; empty = follow `provider` |
| `fetch.url` | string | Override the fetch endpoint; empty = built-in for the effective provider |
| `fetch.format` | string | `markdown` (default) / `html` / `json` |
| `fetch.timeout_ms` | integer | Per-request timeout, default `45000` |

`api_key` has three sources, highest priority first: `web.api_key` in
plaintext > the `WEB_SEARCH_API_KEY` environment variable >
`env.WEB_SEARCH_API_KEY` in `settings.json`. The `env` section means "inject
into child processes", which the in-process web tools cannot see, so the engine
lifts it up explicitly as the last resort. The variable name is deliberately
vendor-neutral: the provider and its key are swapped as a pair.

Behaviour worth knowing:

- **Fail-closed is per provider**: only providers that need a key (`tinyfish`,
  `brave`, `tavily`) report an error when the key is missing, and they never
  fire the request. `duckduckgo` and `direct` are keyless and pass through.
- **DuckDuckGo is not a web search engine**: the default hits the Instant
  Answer API, which only answers encyclopedic / entity-style queries. Ordinary
  search terms return nothing (`best web search api` yields an empty result
  set), so for real search results configure a key for `tinyfish`, `brave` or
  `tavily`.
- **Private URLs never leave the machine**: `localhost` / intranet targets are
  downgraded to a local direct fetch instead of being sent to a third-party
  provider. Downgrading and *allowing* are separate decisions — whether the
  direct fetch is permitted is still governed by `AllowPrivateHosts`.

### loop

| Field | Type | Description |
|-------|------|-------------|
| `max_rounds` | integer | Max LLM turns per execution |
| `tool_execution` | string | `sequential` or `parallel_safe` |
| `max_parallel_tools` | integer | Max concurrent read-only tools |
| `async_tools` | array | Tool names allowed to create durable async tasks |
| `timeout` | string | Execution timeout |

### context

`context` controls the bounded messages sent to the model. The complete
transcript remains available to the current AgentTurn, while the Active Context
may be compacted before the next model call.

| Field | Type | Description |
|-------|------|-------------|
| `max_input_tokens` | integer | Optional explicit model input capacity |
| `target_ratio` | number | Target Active Context ratio |
| `compaction_threshold` | number | Ratio that triggers compaction |
| `hard_limit_ratio` | number | Hard input limit ratio |
| `output_reserve_ratio` | number | Ratio reserved for model output |
| `tool_output_ratio` | number | Default Tool output budget ratio |
| `max_tool_output_chars` | integer | Optional per-tool-result character limit |

If `max_input_tokens` is omitted, the runtime uses the selected model profile's
`maxInputTokens` when available. Recommended defaults are:

```yaml
target_ratio: 0.70
compaction_threshold: 0.80
hard_limit_ratio: 0.90
output_reserve_ratio: 0.15
tool_output_ratio: 0.10
```

### budget

`budget` limits one AgentTurn independently from the context window:

| Field | Type | Description |
|-------|------|-------------|
| `max_model_rounds` | integer | Maximum model calls |
| `max_tool_calls` | integer | Maximum Tool calls |
| `max_wall_time` | duration | Maximum wall-clock time, e.g. `10m` |
| `max_input_tokens` | integer | Maximum cumulative model input tokens |
| `max_output_tokens` | integer | Maximum cumulative model output tokens |
| `max_file_changes` | integer | Maximum Workspace write operations |
| `max_tool_output` | integer | Maximum cumulative Tool output characters |

Zero means no explicit limit for that dimension. `loop.max_rounds` remains the
backwards-compatible loop boundary; `budget.max_model_rounds` can make it
stricter.

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
| `event` | string | Hook event: `on_start`, `on_tool_start`, `on_tool_end`, `on_error`, `on_end` |
| `command` | string | Shell command to execute |
| `timeout` | string | Command timeout |

### handoffs

List of agent names this agent can delegate tasks to.

## State Tool

`State` 是 agent 的"跨会话待办白板"（区别于 knowledge 的固化知识）。agent 在 TurnLoop 里增删改查自己的待办，做完一件事就划掉。同一 agent 的多个并发实例共享一份 state。

在 `tools.builtin` 声明 `State` 后，agent 可调用：

```text
State(action="add",    field=<field>, text=<内容>)        → 追加条目（id 自动生成）
State(action="remove", field=<field>, id=<条目id>)         → 删除条目
State(action="update", field=<field>, id=<条目id>, text=)  → 更新条目内容
State(action="list",   field=<field>)                     → 列出某字段全部条目
State(action="get",    field=<field>, id=<条目id>)         → 读单条目
```

- `field` 取值：`goal` / `confirmed` / `open_questions` / `decisions` / `next_steps` / `workspace` / `record_ids`。
- agent 只能操作**自己的** state，无法越权访问其他 agent。
- 每条目记录 `added_by`（谁加的）和 `handled_by`（谁处理的），格式 `agent名(实例号)`，如 `role-actor(e-1)`——共享白板可追溯，但所有实例都能处理任意条目。
- 跨会话持久，落盘 `agents/<agentID>/state/state.md`。

详见 `docs/generic-engine/26-state-redesign.md`。

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
