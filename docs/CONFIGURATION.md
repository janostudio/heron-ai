# Configuration

All configuration lives in the `.agents/` directory.

## Quick Start

Minimum configuration: create `.agents/models.json` with your API key and model.

```bash
export OPENAI_API_KEY=sk-your-key
heron
```

## Directory Structure

```
.agents/
├── models.json         # Model registry (required)
├── settings.json       # Engine settings (optional)
├── flows/              # Flow definitions
├── teams/              # Team configurations
├── agents/             # Agent definitions
├── skills/             # Skill definitions
├── knowledge/          # Knowledge base
└── rules/              # Global rules
```

## Configuration Files

| File | Required | Description |
|------|----------|-------------|
| [models.json](./configuration/models.md) | Yes | Model registry and API keys |
| [settings.json](./configuration/settings.md) | No | Engine behavior tuning |
| [Flow](./configuration/flow.md) | Yes | Stage pipeline with signal routing |
| [Team](./configuration/team.md) | Yes | Agent scheduling (parallel/sequential) |
| [Agent](./configuration/agent.md) | Yes | Persona, tools, loop, hooks |
| [Skill](./configuration/skill.md) | No | Packaged tool + prompt combinations |
| [Knowledge](./configuration/knowledge.md) | No | Searchable knowledge base entries |
| [Rules](./configuration/rules.md) | No | Soft/hard constraints and guardrails |
