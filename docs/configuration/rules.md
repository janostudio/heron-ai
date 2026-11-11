# Rules Configuration

Rules are prompt-level constraints that agents are told to follow. `type` is a
label only: both `soft` and `hard` are injected the same way, and neither is
enforced or blocking. `priority` is currently **not read** — it does not change
ordering or strength.

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

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | 引用键（`rules:` 里写的就是它）；缺省取文件名 |
| `type` | string | `soft` 或 `hard`，只作为注入标题里的标签，不改变行为 |
| `scope` | object | Visibility control |
| `paths` | array | 可选的 glob 条件：只有当前 path context 命中时才注入这条规则 |
| `priority` | integer | 预留字段，**当前无读取方** |

### scope

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `all`, `team`, or `agents` |
| `teams` | array | Team names (when type=team) |
| `agents` | array | Agent names (when type=agents) |

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

## Usage

Rules are injected into the prompt as a `## rules` section, one `###` block per
referenced rule, titled `<id> (<type>)`:

```
## rules
### api-keys (hard)
Never include API keys or passwords in responses.
### objectivity (soft)
Be objective and cite sources.
```
