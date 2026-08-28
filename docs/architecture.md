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
│   ├── context/         # Agent memory, history, compressor
│   ├── knowledge/       # Knowledge index, injector
│   ├── model/           # LLM provider abstraction
│   ├── config/          # Config loader (flows/teams/agents)
│   ├── storage/         # File store, run state persistence
│   ├── observability/   # Logger, event bus, metrics
│   ├── view/            # TUI (bubbletea), HTTP handler, SSE
│   ├── eval/            # Evaluation engine
│   ├── mcp/             # MCP adapter
│   ├── extension/       # Extension registry
│   └── consolidation/   # Consolidation agent
```

## Signal Routing

Agent Calls produce signals that control Flow execution:

| Signal | Effect |
|--------|--------|
| `continue` | Move to next stage |
| `wait_input` | Pause, wait for user input |
| `goal_achieved` | End run successfully |
| `goal_failed` | End run with failure |
| `goal_impossible` | End run, task impossible |

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
