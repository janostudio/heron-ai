# Team Configuration

A team defines how agents collaborate. Supports parallel and sequential execution.

## Structure

```yaml
name: research_team

stages:
  - process: parallel
    tasks:
      - name: research_topic
        agent: researcher
        description: "Research: {{.Input}}"

      - name: plan_outline
        agent: planner
        description: "Plan outline: {{.Input}}"

  - process: sequential
    tasks:
      - name: write_blog
        agent: writer
        description: "Write blog based on research and outline"
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Team identifier |
| `stages` | array | Ordered stages (parallel then sequential) |

### Stage

| Field | Type | Description |
|-------|------|-------------|
| `process` | string | `parallel` or `sequential` |
| `tasks` | array | Tasks to execute |

### Task

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Task identifier |
| `agent` | string | Agent name to assign |
| `description` | string | Task description. Supports `{{.Input}}` template |

## Process Types

### parallel
All tasks run concurrently. Agents have **zero communication** during execution.

```
researcher ─┐
            ├─ concurrent, independent
planner ────┘
```

### sequential
Tasks run in order. Each task receives the previous task's output as context.

```
researcher → planner → writer
```

## File Location

Teams are YAML files in `.agents/teams/`:
```
.agents/teams/
├── research_team.yml
├── writing_team.yml
└── review_team.yml
```
