# Architecture

Heron is a multi-agent orchestration engine with a three-layer orchestration
model. `Call` is a Team execution item, not an additional orchestration layer.

## Runtime Model

```
Flow (signal routing)
  └── Team (Call scheduling)
        ├── Call → Agent
        │          └── AgentTurn (LLM + tool loop)
        ├── Call → Shell Command
        └── Call → Webhook
```

| Layer | Description | Has LLM? |
|-------|------------|----------|
| **Flow** | Complete orchestration graph and routing | No |
| **Team** | Schedules dependent or parallel Calls | No |
| **Call** | One Agent, Command, or Webhook execution | Depends on type |
| **AgentTurn** | One Agent LLM/tool loop | **Yes** |

## Package Structure

`cmd/server` is the only binary. `pkg/types` is the shared vocabulary;
everything else is `internal/`, grouped by the job it does.

```
heron-ai/
├── cmd/server/          # CLI entry point: flags, subcommands, HTTP handlers
├── pkg/types/           # Shared types and interfaces (the import boundary)
├── builtin/skills/      # Skills shipped with the engine
├── examples/            # Complete .agents/ trees, loaded by CI
└── internal/
```

| Package | Responsibility |
|---|---|
| `runtime` | Flow / Team / Call runtimes: turn lifecycle, activation, record routing |
| `agent` | One AgentTurn: LLM/tool loop, prompt assembly hooks, guardrail, signal parser, HITL, structured output, spawn handling |
| `agentstore` | Inserts dynamically spawned child calls into the invoking Team's DAG |
| `prompt` | Builtin prompt fragments and prompt provenance |
| `model` | Provider abstraction (`openai_chat`, `anthropic_messages`), profile defaults, fallback router |
| `tool` | Tool registry, executor, builtin tools, Bash policy, path scope |
| `skill` | Skill registry, loader, progressive-disclosure injector |
| `knowledge` | Knowledge index, injector, summarizer |
| `state` | Short-term session state snapshots (`state.md`) |
| `storage` | File store and run-state persistence (append-only JSONL) |
| `logging` | Rotating execution log (one of the fact sources) |
| `config` | Config loader: flows / teams / agents / skills / rules / settings / models |
| `definitions` | Definition merge, templates, validation and writing (backs the `Define` tool) |
| `teamspkg` | Team package manifest, dependency closure and compatibility checks |
| `view` | TUI (bubbletea), HTTP handler, SSE, session picker |
| `app` | Runtime wiring: builds and connects everything above for a run |
| `mcp` | MCP client (stdio + http; `sse` is unsupported and rejected at startup) |
| `media` | Multimedia content blocks and attachment store |
| `workspace` | Execution backend: local process or remote SSH, with path restriction |
| `osenv` | Merges `settings.json` `env` into every subprocess environment |
| `sessionlist` | Reads `.agents/data/sessions/` for `--list` |
| `eval` | Correlation IDs and fact derivation (not yet surfaced in CLI/TUI) |
| `extension` | Extension registry (registration only; no script runtime is bundled) |

Two of these are easy to misread:

- `agentstore` does not store agents. It writes the synthetic call that makes a
  spawned child join the parent's Team DAG.
- `extension` is a registry, not a plugin runtime. Nothing in the engine loads
  Lua or WASM; a tool that is not builtin or MCP-backed has to be registered in
  `internal/app`.

## Where things live

| What | Path | Notes |
|---|---|---|
| Configuration | `.agents/` | Resolved relative to the process cwd, never upward |
| Session events | `.agents/data/sessions/<fs_id>/{flow,team,agent}.jsonl` | One append-only log split by producing layer |
| Flow-scope records | `.agents/data/sessions/<fs_id>/evidence.jsonl` | Appears once a Flow-scope record is published |
| State snapshots | `.agents/data/sessions/<fs_id>/teams/…/state.md` | Human-readable, injected next turn |
| Execution log | `.agents/data/logs/YYYY-MM-DD.log` | JSON lines; `debug` adds request usage |
| Installed Team packages | `.agents/_teams/<scope>/` | Scanned in addition to `.agents/` |

The data directory is fixed; `settings.json` has no key that moves it.

## Signal Routing

Agent Calls produce signals that control Flow execution:

| Signal | Effect |
|--------|--------|
| `continue` | Proceed with the next orchestration step |
| `wait_input` | End the turn; the session stays `waiting_input` and can be continued |
| `goal_achieved` | End the turn; the session stays `waiting_input` and can be continued |
| `goal_failed` | End the turn as failed; the session stays continuable |
| `goal_impossible` | End the turn as failed; the session stays continuable |

Session lifecycle is a runtime decision: every turn, including a failed one,
leaves the FlowSession continuable, and Flow configuration (`on_proceed`)
may only choose orchestration actions (`proceed` / `return` / `coordinate` /
`activate` / `fail`). A failure is reported on that turn's result (status
`failed` plus `error`) and never turns the conversation into a dead session.

## Agent Capabilities

Each agent can be configured with:

- **Persona**: Role, goal, backstory
- **Tools**: Builtin tool schemas are declared in `internal/agent/runtime.go`
  (`builtinSchemas`) — Read, Write, Bash, WebSearch, WebFetch, CodeNav,
  AskUserQuestion, Grep, Glob, TodoWrite, TodoRead, Spawn, ListTeams, Team,
  TeamSession, Collect, ScheduleWakeup, CancelWakeup, State, Define. An agent
  only gets the subset listed in its `tools.builtin`; a name that is registered
  but missing from `builtinSchemas` runs but stays invisible to the model.
  Two tools are conditional: `Script` only appears when
  `settings.json` sets `tools.script.enabled`, and `ScheduleWakeup` /
  `CancelWakeup` only exist under `--serve`, the one mode whose process
  outlives its turns.
  `WebSearch` and `WebFetch` additionally read their data source from the `web`
  section of `.agents/settings.json` — the provider defaults to `duckduckgo`
  (keyless, search only); `tinyfish`, `brave` and `tavily` need an `api_key`
  and fail closed without one, while `direct` fetches locally. See
  `docs/configuration/settings.md` and `docs/configuration/agent.md`.
- **Skills**: Packaged tool + prompt combinations
- **Knowledge**: Searchable knowledge base
- **Rules**: Soft/hard constraints
- **Guardrails**: Input/output validation
- **Hooks**: Lifecycle event handlers
- **Structured Output**: JSON schema enforcement
- **Handoff**: Cross-agent task delegation
- **HITL**: Human-in-the-loop approval gates
- **MCP**: Tools discovered from configured MCP servers, listed per agent under
  `tools.mcp`
