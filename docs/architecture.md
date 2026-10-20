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

```
heron-ai/
├── cmd/server/          # CLI entry point
├── pkg/types/           # Shared types and interfaces
├── internal/
│   ├── runtime/         # Flow, Team, and Call runtimes
│   ├── agent/           # AgentTurn loop, guardrail, signal parser, HITL
│   ├── tool/            # Tool registry, executor, builtin tools
│   ├── skill/           # Skill registry, loader, injector
│   ├── knowledge/       # Knowledge index, injector, summarizer
│   ├── model/           # LLM provider abstraction
│   ├── config/          # Config loader (flows/teams/agents)
│   ├── storage/         # File store, run state persistence
│   ├── state/           # Short-term session state snapshots
│   ├── logging/         # Rotating execution log（事实源之一：轮转 JSONL）
│   ├── view/            # TUI (bubbletea), HTTP handler, SSE
│   ├── eval/            # Evaluation: 关联 ID 体系 + 事实派生（Phase 1 已落地；尚未接入 CLI/TUI 查看）
│   ├── mcp/             # MCP client（stdio + http 两种 transport；sse 不支持，配置写 sse 会明确报错）
│   └── extension/       # Extension registry
```

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
  AskUserQuestion, Grep, Glob, TodoWrite, TodoRead, Spawn, Collect, State,
  Define. An agent only gets the subset listed in its `tools.builtin`.
  `WebSearch` and `WebFetch` additionally read their data source from the `web`
  section of `.agents/settings.json` — the provider defaults to `duckduckgo`
  (keyless, search only); `tinyfish`, `brave` and `tavily` need an `api_key`
  and fail closed without one, while `direct` fetches locally. Fetch follows
  the search provider but falls back to `direct` for providers that expose no
  fetch API; see `docs/configuration/agent.md`.
- **Skills**: Packaged tool + prompt combinations
- **Knowledge**: Searchable knowledge base
- **Rules**: Soft/hard constraints
- **Guardrails**: Input/output validation
- **Hooks**: Lifecycle event handlers
- **Structured Output**: JSON schema enforcement
- **Handoff**: Cross-agent task delegation
- **HITL**: Human-in-the-loop approval gates
