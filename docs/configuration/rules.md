# Rules Configuration

Rules are prompt-level constraints that agents are told to follow. `type` is a
label only: both `soft` and `hard` are injected the same way, and neither is
enforced or blocking. `priority` is currently **not read** — it does not change
ordering or strength.

A rule is either **resident** or **conditional**:

- Resident (no `paths`): rendered into the system prompt at the start of every
  turn. Stable, cacheable, and paid for on every call.
- Conditional (`paths` set): held back and injected only when the turn touches a
  matching path. For contracts that belong to one directory — "documents go
  here", "this repository is public" — so a turn that never goes near that
  directory does not carry them.

## Structure

```yaml
---
type: hard
scope:
  type: all
priority: 10
---

Never include API keys or passwords in responses.
```

A conditional rule adds `paths`:

```yaml
---
type: hard
scope:
  agents: [researcher]
  type: agents
paths:
  - docs/**
  - ../docs/**
---

Write explanatory documents under docs/, not at its root.
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | 引用键（`rules:` 里写的就是它）；缺省取文件名 |
| `type` | string | `soft` 或 `hard`，只作为注入标题里的标签，不改变行为 |
| `scope` | object | Visibility control |
| `paths` | array | 条件触发：命中本轮工具触及的路径时才注入；空=常驻 |
| `priority` | integer | 预留字段，**当前无读取方** |

### scope

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `all`, `team`, or `agents` |
| `teams` | array | Team names (when type=team) |
| `agents` | array | Agent names (when type=agents) |

### paths

Matched against the paths this turn's tools touch, never against the user's
text. Two sources, because they cover different halves:

- The tool arguments — what a call is about to do, including a shell command's
  redirection, `tee` and `-C`/`cd` targets.
- The operations the workspace reports — the resolved path, for tools whose
  argument names the Agent layer does not know.

Three pattern shapes:

- `docs/README.md` — that file.
- `docs/*` — one level under `docs/`.
- `docs/**` — everything under `docs/`, at any depth. A trailing `/**` is the
  only form of `**` that is understood; `docs/**/x.md` is not a pattern.

Patterns are workspace-relative, and a leading `../` is allowed: a deployment
whose `docs/` lives beside the workspace writes `../docs/**`. An absolute
pattern, a mid-path `..`, or a `**` anywhere but the end never matches, and
`--check-config` reports it.

A shell command's target is read only when it is static. A variable, a glob or a
command substitution is dropped rather than guessed — a contract injected for a
path the command may never write teaches the model that rule notes are noise.

## Injection

Resident rules are rendered into the system prompt as a `## rules` section, one
`###` block per referenced rule, titled `<id> (<type>)`:

```
## rules
### api-keys (hard)
Never include API keys or passwords in responses.
### objectivity (soft)
Be objective and cite sources.
```

A conditional rule is injected at the tail of the request as its own system
message, once per turn per rule, labelled with what triggered it:

```
[rule-notes] 本轮触及的路径适用以下规则，立即生效；这不是对话历史，也不需要回应。

[rule:docs-placement trigger=docs/self/x.md kind=write]
Write explanatory documents under docs/, not at its root.
```

Three properties, each of which is the reason the note is not simply appended to
the conversation:

- It is not part of the system prefix, so the cached prefix is unchanged.
- It is not in the conversation, so it is not replayed into the next turn (long
  after the work left that directory) and cannot be dropped by compaction.
- It is rebuilt from the rules triggered so far on every request, so it contains
  each triggered rule exactly once regardless of how many rounds followed.

Each trigger is recorded as `session.rule.injected` with `rule_id`,
`trigger_path`, `trigger_kind` (write/read/search) and `round`. A resumed turn
replays the identity from the checkpoint's operations instead of injecting a
second copy.

## File Location

### Global rules
```
.agents/rules/
├── quality.md
├── accuracy.md
└── safety.md
```

### Agent-private rules
```
.agents/agents/researcher/rules/
└── guidelines.md
```

## Checking a configuration

`heron --check-config` loads the tree, prints each conditional rule with its
patterns, and lints the patterns that can never match. A conditional rule is
absent from the prompt on every turn that does not touch its directory — that is
the design, and it is also why the list needs printing: "why did this rule never
apply" has no other answer.
