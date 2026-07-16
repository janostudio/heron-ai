# Skill Configuration

Skills package tools, prompts, and knowledge into reusable bundles.

## Structure

```
.agents/skills/
└── deep_research/
    └── SKILL.md
```

## SKILL.md

```yaml
---
name: deep_research
description: "Deep research: systematically search and analyze information"
tools:
  - Read
  - Grep
  - Glob
knowledge:
  - research-methods
---

# Deep Research Methodology

When conducting deep research:
1. Use Grep to search the knowledge base
2. Use Glob to find reference files
3. Use Read to review materials
4. Cross-validate multiple sources
5. Summarize findings with confidence levels
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Skill identifier |
| `description` | string | Brief description for discovery |
| `tools` | array | Tool names this skill uses |
| `knowledge` | array | Knowledge entries this skill depends on |

## Usage

Agents reference skills in their config:

```yaml
skills:
  - deep_research
```

Skills are injected into the agent's system prompt at runtime.
