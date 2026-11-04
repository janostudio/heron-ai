# Heron AI - Multi-Agent Generic Engine

Build, orchestrate, and evaluate AI agents. Written in pure Go, no framework dependencies.

## Features

- **Three-layer orchestration** — Flow routes, Team schedules, Call executes. An `Agent`,
  a shell `Command` or a `Webhook` is a Call; there is no fourth layer.
- **Turn-based agent runtime** — prompt assembly, tool loop, guardrails, HITL approvals,
  handoff, structured output.
- **Run modes** — interactive TUI, one-shot `--prompt`, long-lived JSON-RPC over
  stdin/stdout, HTTP server with SSE, and a `stream-json` client for external callers.
- **Team packages** — install, remove and export self-contained Teams
  (`heron teams`), with a manifest, dependency closure and script review.
- **Configuration as files** — everything lives in `.agents/`; no database, no daemon.
- **File storage** — append-only JSONL per session plus Markdown state snapshots.
- **MCP** — stdio and HTTP transports, connected at startup.
- **Multimedia input** — image/file content blocks over HTTP and CLI.
- **Zero framework dependencies** — Go standard library plus a small set of
  UI/parsing libraries.

## Quick Start

```bash
# Install
npm install -g @qinghuangniao/heron-ai

# Set API key (or declare api_key in .agents/models.json)
export OPENAI_API_KEY=sk-your-key

# Create .agents/ in the current directory, then validate before running
heron --check-config

# Run
heron                                   # interactive TUI
heron --prompt "Hello"                   # one turn, then exit
heron --list                             # pick a past session and resume
```

Heron resolves configuration from `.agents/` **relative to the current working
directory**, so run it from the workspace root. See
[docs/getting-started.md](./docs/getting-started.md) for the four files a working
setup needs.

## Examples

Each example is a complete `.agents/` tree you can copy as a starting point.

| Example | Description | Agents | Teams |
|---------|-------------|--------|-------|
| [simple-qa](./examples/simple-qa/) | Single agent Q&A | 1 | 1 |
| [code-review](./examples/code-review/) | Multi-agent code review | 3 | 1 |
| [blog-writer](./examples/blog-writer/) | 3-team content pipeline | 4 | 3 |
| [novel-rp](./examples/novel-rp/) | Multi-character roleplay | 3 | 3 |

Each example needs its own `.agents/models.json` — models and keys are local to
the receiving side and never shipped with a sample. See
[examples/README.md](./examples/README.md) for the one-file template and the
run command per example.

`examples_test.go` loads every example through the real config loader, so an
example that stops validating fails CI rather than rotting.

## Documentation

Start at [docs/INDEX.md](./docs/INDEX.md) — it routes by task and marks each
document's language. The most-used entries:

- [Getting Started](./docs/getting-started.md) - Four files from nothing to a working agent
- [Architecture](./docs/architecture.md) - How Heron works under the hood
- [Configuration](./docs/CONFIGURATION.md) - All config files, with per-file detail
- [CLI Usage](./docs/CLI.md) - Every flag, TUI, HTTP server, JSON-RPC, `stream-json`, `teams`
- [CLI JSON-RPC](./docs/CONNECT-CLI-JSONRPC.md) - JSON-RPC/JSONL integration for external CLI callers
- [Multimedia Input](./docs/MULTIMEDIA-INPUT.md) - Image and file content blocks
- [Context Management](./docs/context-management.md) - What one AgentTurn sends to the model
- [Team Sharing](./docs/generic-engine/30-team-sharing.md) - Package manifest and install rules

## Configuration

Heron uses a `.agents/` directory for all configuration:

```
.agents/
├── flows/         # Flow definitions (YAML)
├── teams/         # Team configurations (YAML)
├── agents/        # Agent directories (<id>/AGENT.md + knowledge/ + rules/)
├── skills/        # Skill definitions (SKILL.md + scripts/ + references/)
├── knowledge/     # Knowledge base files
├── rules/         # Global rules
├── _teams/        # Installed Team packages (managed by `heron teams`)
├── models.json    # Model registry
├── settings.json  # Engine settings
└── data/          # Runtime data: sessions/, logs/
```

### models.json

```json
{
  "model": "hy3-ioa",
  "models": [
    {
      "id": "hy3-ioa",
      "name": "hy3-ioa",
      "protocol": "openai_chat",
      "base_url": "https://api.openai.com/v1",
      "api_key": "${OPENAI_API_KEY}",
      "maxOutputTokens": 64000
    }
  ]
}
```

`model` selects the default profile by `name`. `api_key` expands `${ENV_VAR}`;
when omitted, the protocol's conventional variable (`OPENAI_API_KEY` /
`ANTHROPIC_API_KEY`) is used. A `${VAR}` that resolves to nothing is an error,
not a silent fallback to another provider's key.

### settings.json

Optional. It carries log policy, runtime safety backstops, the `WebSearch` /
`WebFetch` data source, MCP servers, extra Bash policy phrases and env vars for
subprocesses. Fields that no longer have a reader are listed explicitly in
[docs/configuration/settings.md](./docs/configuration/settings.md#inert-sections)
so nobody tunes a knob that does nothing.

## Development

```bash
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
go test ./...
```

The docs-check in CI catches broken relative links between documents, so a
renamed file fails the build instead of leaving a 404 in the README.

## License

MIT
