# Architecture

Heron is a multi-agent orchestration engine with a three-layer runtime model.

## Runtime Model

```
Run (session container)
  └── Flow (stage pipeline with signal routing)
        └── Stage (team execution)
              └── Team (agent scheduling)
                    ├── parallel: agents run concurrently
                    └── sequential: agents run in order
                          └── Agent (LLM turn loop)
                                └── Turn (LLM call + tool execution)
```

| Layer | Description | Has LLM? |
|-------|------------|----------|
| **Run** | Complete session, holds conversation history | No |
| **Round/Stage** | One stage of team execution | No |
| **Turn** | Single agent LLM call + tool loop | **Yes** |

## Package Structure

```
heron-ai/
├── cmd/server/          # CLI entry point
├── pkg/types/           # Shared types and interfaces
├── internal/
│   ├── orchestration/   # Flow engine, team runner, signal router
│   ├── agent/           # Turn loop, guardrail, signal parser, HITL
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

Agents produce signals that control flow execution:

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
