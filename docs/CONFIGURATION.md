# Configuration

All configuration lives in the `.agents/` directory, resolved relative to the
**current working directory**. Heron does not search upward for a config root,
so run it from the workspace root.

## Quick Start

Four files stand between an empty directory and a working agent; the walk-through
is in [getting-started.md](./getting-started.md). The shortest version:

```bash
export OPENAI_API_KEY=sk-your-key   # or put api_key in models.json
heron --check-config                # validate before spending tokens
heron
```

## Directory Structure

```
.agents/
├── models.json         # Model registry (required)
├── settings.json       # Engine settings (optional)
├── flows/              # Flow definitions
├── teams/              # Team configurations
├── agents/             # Agent definitions (<id>/AGENT.md + knowledge/ + rules/)
├── skills/             # Skill definitions
├── knowledge/          # Knowledge base
├── rules/              # Global rules
├── _teams/             # Installed Team packages (managed by `heron teams`)
└── data/               # Runtime data: sessions/ and logs/ (do not commit)
```

## Configuration Files

| File | Required | Description |
|------|----------|-------------|
| [models.json](./configuration/models.md) | Yes | Model registry and API keys |
| [settings.json](./configuration/settings.md) | No | Engine behavior tuning; inert sections are called out explicitly |
| [Flow](./configuration/flow.md) | Yes | Entry Team, Team bindings and signal routing (no Stage concept) |
| [Team](./configuration/team.md) | Yes | Agent scheduling (parallel/sequential, per_task) |
| [Agent](./configuration/agent.md) | Yes | Persona, tools, loop, hooks |
| [Skill](./configuration/skill.md) | No | Packaged tool + prompt combinations |
| [Knowledge](./configuration/knowledge.md) | No | Markdown knowledge entries (agentic search, no index) |
| [Rules](./configuration/rules.md) | No | Prompt-level constraints (`type` is a label only; no guardrail config exists) |
| [Workspace](./configuration/workspace.md) | No | Execution backend (local / remote SSH) |

## How the pieces load

1. `--flow` (or `.agents/flows/default.yml`) is read first; it names the Teams
   to bind and how work is routed between them.
2. Each binding resolves its Team file, then the Agents / Commands / Webhooks
   that Team's Calls reference, then each Agent's skills, rules and knowledge.
3. `models.json` and `settings.json` are read from `.agents/` regardless of the
   Flow, and the default model is resolved last — an Agent that omits `model`
   inherits it.
4. Validation happens at load time: unknown record producers, unknown agent
   names and illegal bindings fail here, not on the first turn.

`heron --check-config` runs exactly this load and reports each Team binding's
lifecycle, which is the cheapest way to see whether a configuration change is
structurally sound.
