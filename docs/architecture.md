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
│   ├── logging/         # Rotating execution log
│   ├── observability/   # Logger, event bus, metrics
│   ├── view/            # TUI (bubbletea), HTTP handler, SSE
│   ├── eval/            # Evaluation engine
│   ├── mcp/             # MCP adapter
│   └── extension/       # Extension registry
```

## Signal Routing

Agent Calls produce signals that control Flow execution:

| Signal | Effect |
|--------|--------|
| `continue` | Proceed with the next orchestration step |
| `wait_input` | End the turn; the session stays `waiting_input` and can be continued |
| `goal_achieved` | End the turn; the session stays `waiting_input` and can be continued |
| `goal_failed` | End the run with failure |
| `goal_impossible` | End the run with failure |

Session lifecycle is a runtime decision: a normally finished turn always
leaves the FlowSession continuable, and Flow configuration (`on_proceed`)
may only choose orchestration actions (`proceed` / `return` / `coordinate` /
`activate` / `fail`).

## Agent Capabilities

Each agent can be configured with:

- **Persona**: Role, goal, backstory
- **Tools**: Read, Write, Grep, Glob, TodoWrite, TodoRead
- **Skills**: Packaged tool + prompt combinations
- **Knowledge**: Searchable knowledge base
- **Rules**: Soft/hard constraints
- **Guardrails**: Input/output validation
- **Hooks**: Lifecycle event handlers
- **Structured Output**: JSON schema enforcement
- **Handoff**: Cross-agent task delegation
- **HITL**: Human-in-the-loop approval gates
