# Flow Configuration

A flow binds Team definitions into one execution graph and identifies the
entry Team. Flow execution state belongs to FlowSession and is persisted in
`session.jsonl`.

## Structure

```yaml
id: blog_writer_flow
entry: research

teams:
  research:
    team: research_team
    coordinator: true
    inputs:
      user_message: true
    on_proceed: [writing]

  writing:
    team: writing_team
    depends_on: [research]
    inputs:
      - from: research
        record: ResearchReport
    on_proceed: [review]

  review:
    team: review_team
    depends_on: [writing]
    inputs:
      - from: writing
        record: BlogDraft
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Flow identifier |
| `entry` | string | Flow-local name of the entry Team |
| `teams` | map | Team bindings, keyed by Flow-local Team name |

### Team binding

| Field | Type | Description |
|-------|------|-------------|
| `team` | string | Team definition under `.agents/teams/` |
| `coordinator` | bool | Marks the single coordinator Team |
| `can_activate` | []string | Teams this binding may activate |
| `depends_on` | []string | Teams that must finish first |
| `inputs` | object/list | User message and SharedRecord inputs |
| `on_proceed` | list or object | Fixed routing when the Team ends with `proceed` |

### on_proceed

`on_proceed` selects the next orchestration step when a TeamTurn ends with
the default `proceed` route. The list form means "activate these Teams":

```yaml
on_proceed: [writing, review]
```

The mapping form may set `action` and `teams` explicitly. `action` only
allows orchestration actions: `proceed`, `return`, `coordinate`,
`activate`, `fail`.

Session lifecycle is not configurable: a normally finished turn always
leaves the FlowSession in `waiting_input` so the client can continue the
same `session_id`. Configuring `complete` or `wait_input` in `on_proceed`
is a load-time validation error.

## File Location

Flows are YAML files in `.agents/flows/`:
```
.agents/flows/
├── default.yml
└── blog.yml
```
