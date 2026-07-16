# Flow Configuration

A flow defines the execution pipeline - a sequence of stages with signal-based routing.

## Structure

```yaml
name: blog_writer_flow
loop_max_rounds: 0

stages:
  - name: research_stage
    team: research_team
    on_signal:
      continue: writing_stage
      wait_input: null
      goal_achieved: null
      goal_failed: null
      goal_impossible: null

  - name: writing_stage
    team: writing_team
    on_signal:
      continue: review_stage

  - name: review_stage
    team: review_team
    on_signal:
      continue: null  # Terminal stage
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Flow identifier |
| `loop_max_rounds` | integer | Max rounds (0 = unlimited) |
| `stages` | array | Ordered list of stages |

### Stage

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Stage identifier |
| `team` | string | Team name to execute |
| `on_signal` | object | Signal routing rules |

### on_signal

| Signal | Value | Effect |
|--------|-------|--------|
| `continue` | stage name | Route to this stage |
| `continue` | null | End pipeline |
| `wait_input` | stage name or null | Pause for user input |
| `goal_achieved` | stage name or null | End successfully |
| `goal_failed` | stage name or null | End with failure |
| `goal_impossible` | stage name or null | End, task impossible |

## File Location

Flows are YAML files in `.agents/flows/`:
```
.agents/flows/
├── default.yml
└── blog.yml
```
