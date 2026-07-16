# Rules Configuration

Rules are constraints that agents must follow. Two types: soft (guidelines) and hard (requirements).

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
| `type` | string | `soft` (guideline) or `hard` (requirement) |
| `scope` | object | Visibility control |
| `priority` | integer | Priority (higher = more important) |

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

Rules are injected into agent system prompts:

```
## Rules
- Never include API keys or passwords in responses. (hard)
- Be objective and cite sources. (soft)
```
