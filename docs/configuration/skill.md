# Skill Configuration

Skills package tools, prompts, and knowledge into reusable bundles.

## Structure

```
.agents/skills/
└── deep_research/
    ├── SKILL.md
    ├── scripts/          # Optional deterministic helpers
    └── references/       # Optional reference material
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
scripts:
  - scripts/repo_map.sh
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
| `scripts` | array | Package-relative paths of optional scripts shipped with the Skill |

## Usage

Agents reference skills in their config:

```yaml
skills:
  - deep_research
```

Skills are injected into the agent's system prompt at runtime. What gets injected
is the **index entry**, not the body:

```text
- deep_research — Deep research: systematically search and analyze information
  read: .agents/skills/deep_research/SKILL.md
```

Three consequences worth knowing before relying on it:

- **Body is on demand.** The model reads the named `SKILL.md` when the task
  matches it, so editing a body takes effect immediately — no restart, unlike
  a model definition. Keep the body short and push detail into `references/`
  (the model reads those files too, when the body points at them).
- **Tools are granted on declaration.** `tools` / `allowed-tools` join the
  agent's allowlist as soon as the skill is declared, independent of whether the
  body is ever read. Declaring ten skills to get two tools' worth of work still
  costs ten tool sets in context.
- **No path, no index.** A skill whose `Path` is unknown would have nowhere to
  be read from, so its body falls back to being inlined. That is a degraded
  mode, not the intended one.

`description` is the line the model sees on every call: write it as "when to use
this", not as a title.

## Skill scripts

A Skill may package deterministic scripts:

```text
.agents/skills/<skill-id>/
├── SKILL.md
├── scripts/
│   └── verify.sh
└── references/
    └── output-contract.md
```

When `scripts` is declared in the frontmatter, use paths relative to the Skill
directory:

```yaml
scripts:
  - scripts/verify.sh
```

The file should exist at:

```text
.agents/skills/<skill-id>/scripts/verify.sh
```

The script is part of the Skill package and can be copied together with the
Skill. The Skill body should document the command using a project-relative
path:

```bash
bash .agents/skills/<skill-id>/scripts/verify.sh
```

Scripts are not automatically executed just because the Skill is referenced.
They run only when a Team `command` call or a user explicitly invokes them.
This keeps deterministic work separate from the Agent's model loop. The core
does not decide when a script should run and does not turn a script into an
Agent call.

## Copying a Skill

A Skill is a self-contained package. To reuse it in another Flow, copy the
whole directory, not only `SKILL.md`:

```text
.agents/skills/<skill-id>/
├── SKILL.md
├── scripts/
└── references/
```

After copying, update Team `command` paths only when the Skill ID or the
workspace-relative location changes. The recommended path is:

```bash
bash .agents/skills/<skill-id>/scripts/<script>
```

This makes a Flow portable: its Skill prompt, deterministic helpers and
references can be copied together without relying on a global
`.agents/scripts` directory.

## What can be copied from another project

An existing project's script can be copied directly only when it uses the
Skill package contract:

1. It finds the workspace from the Skill location or `HERON_WORKSPACE`;
2. it does not depend on the source project's private directory layout;
3. it receives input through arguments, environment variables or stdin;
4. it writes only to the declared workspace/output location;
5. it does not require the source project's credentials, internal scheduler or
   external service unless those dependencies are packaged too.

Otherwise copy the script into the Skill and adapt its path and dependency
boundary first. A Skill is the reusable unit; a host project's whole script
directory is not a reusable framework API.
