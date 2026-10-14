# Flow Configuration

A flow binds Team definitions into one collaboration network and identifies the
entry Team. Teams are peers: no Team has a special role, and the only
structural difference between them is which one the Flow enters first. Flow execution state belongs to FlowSession and is persisted in
`.agents/data/sessions/<flow_session_id>/{flow,team,agent}.jsonl` (three
per-layer event streams; `evidence.jsonl` holds Flow-scope SharedRecords).

## Structure

```yaml
id: blog_writer_flow
entry: research

teams:
  research:
    team: research_team
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
| `entry` | string | Flow-local name of the Team every FlowTurn starts at. It is the only Team nobody activated, so it has no one to return to |
| `teams` | map | Team bindings, keyed by Flow-local Team name |
| `workspace` | object | Execution backend (local / remote SSH), 见 [workspace](./workspace.md) |

### Team binding

| Field | Type | Description |
|-------|------|-------------|
| `team` | string | Team definition under `.agents/teams/` |
| `can_activate` | []string | Teams this binding may activate. Any Team may declare it; a Team that declares nothing cannot activate |
| `depends_on` | []string | Teams that must finish first |
| `inputs` | object/list | User message and SharedRecord inputs |
| `on_proceed` | list or object | Fixed routing when the Team ends with `proceed` |

### How a result is routed

Routing follows from who activated whom. There is no designated coordinator.

```text
entry ──activate──▶ A ──activate──▶ B
                    ▲                │
                    └──── result ────┘   B returns to A, the Team that activated it
```

- **activate**: a Team names Teams in `next.teams`. They must be listed in its
  `can_activate`; naming anything else fails the FlowTurn. Several targets run
  in parallel.
- **Return**: when a Team finishes with `proceed` and its binding has no
  `on_proceed`, its result goes back to the Team that activated it. The Team it
  returns to is woken once per batch, however many of its siblings finish
  together, and sees all of their records.
- **Entry**: nothing activated the entry, so when it finishes there is no one to
  return to. The FlowTurn ends and the FlowSession waits for the next user
  message.
- **Same Team, different activators**: a Team returns to whoever activated it
  this time, so one Team can be activated by the entry directly and by another
  Team in the same Flow.

Repeated activation of the same target is bounded by
`runtime.max_activation_retries`, for every Team alike.

### on_proceed

`on_proceed` fixes the next step when a TeamTurn ends with the default
`proceed` route, instead of returning to the activator. The list form means
"activate these Teams":

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

### Team failure

A failed intermediate Team is not an automatic replay request:

```text
Agent/model retry
  → AgentTurn failed
  → CallResult failed
  → TeamTurn failed
  → the Team that activated it
```

`Agent` may retry a transient model request inside the current AgentTurn.
After those retries are exhausted, the containing Team reports the failure to
the Team that activated it with `next.action=coordinate`. The Flow runtime
publishes a Flow-scope `TeamFailureReport` containing the failed Team, failed
Calls, and reason, and preserves successful sibling Team results.

`TeamFailureReport` is an ordinary record. A Team sees it only if it lists it in
its `inputs`, like any other record; the engine does not inject it. A Team that
activates others and wants to react to their failures should list it.

The activator is woken once for the batch and can aggregate:

- successful sibling SharedRecords;
- `TeamFailureReport` records, if listed in its inputs;
- records already persisted for the Flow.

If the entry itself fails there is no activator to report to, so the FlowTurn
fails. The failed Team is not automatically executed again. A dependent Team
whose inputs require the failed Team is skipped because its prerequisites are
not valid. Independent sibling Teams may complete normally. Running it again
requires an explicit new activation, normally from a new user turn.

A Team that reacts to a `TeamFailureReport` should treat it as an execution
result:

```text
- summarize the failed Team and reason;
- include successful sibling results;
- do not activate the failed Team again automatically;
- only activate it when the user explicitly asks to retry or there is a
  concrete, non-replay recovery plan.
```

### Background dispatch

When the runtime enables mailbox dispatch, the entry Team's `activate` may hand
a target to a background goroutine and end the FlowTurn at once; the result is
posted to the entry Team's inbox and the session is woken. It applies only to
targets that finish on their own: a target that declares `can_activate` runs
inside the current turn, because its own activations and their returns must be
handled.

## File Location

Flows are YAML files in `.agents/flows/`:
```
.agents/flows/
├── default.yml
└── blog.yml
```
