# Knowledge Configuration

Knowledge entries are searchable reference documents injected into agent context.

## Structure

Knowledge files are Markdown with YAML frontmatter:

```yaml
---
keys: ["SEO", "博客", "写作", "标题优化"]
scope:
  type: all
---

# SEO Writing Guide

## Title Optimization
- Title length: 20-70 characters
- Include primary keyword in first 30%
- Use numbers and power words

## Content Structure
- Paragraphs: 2-4 sentences max
- Use H2/H3 for section breaks
- Include keyword in first 100 words after first H2
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `keys` | array | Keywords for search matching |
| `scope` | object | Visibility control |

### scope

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | `all` (everyone), `team` (specific teams), `agents` (specific agents) |
| `teams` | array | Team names (when type=team) |
| `agents` | array | Agent names (when type=agents) |

## File Location

### Global knowledge
```
.agents/knowledge/
├── seo-guide.md
└── writing-style.md
```

### Agent-private knowledge
```
.agents/agents/researcher/knowledge/
└── research-methods.md
```

Agent-private knowledge is automatically loaded when the agent runs.
