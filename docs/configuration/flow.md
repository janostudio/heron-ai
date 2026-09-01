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

### Team failure and coordination

The Flow is collaborative, so a failed intermediate Team is not an automatic
replay request:

```text
Agent/model retry
  → AgentTurn failed
  → CallResult failed
  → TeamTurn failed
  → Flow coordinator
  → user-visible summary
```

`Agent` may retry a transient model request inside the current AgentTurn.
After those retries are exhausted, the containing Team reports the failure to
the single Flow coordinator with `next.action=coordinate`. The Flow runtime
publishes a Flow-scope `TeamFailureReport` containing the failed Team, failed
Calls, and reason. It also preserves successful sibling Team results.

The coordinator is activated once for the batch and can aggregate:

- successful sibling SharedRecords;
- `TeamFailureReport` records;
- records already persisted for the Flow.

The failed Team is not automatically executed again. A dependent Team whose
inputs require the failed Team is skipped because its prerequisites are not
valid. Independent sibling Teams may complete normally. A later retry requires
an explicit new coordinator decision, normally from a new user turn.

For coordinator prompts, treat `TeamFailureReport` as an execution result:

```text
- summarize the failed Team and reason;
- include successful sibling results;
- do not activate the failed Team again automatically;
- only activate it when the user explicitly asks to retry or there is a
  concrete, non-replay recovery plan.
```

## File Location

Flows are YAML files in `.agents/flows/`:
```
.agents/flows/
├── default.yml
└── blog.yml
```
