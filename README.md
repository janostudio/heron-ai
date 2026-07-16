# Heron AI

<p align="center">
  <img src="assets/icon.png" alt="Heron AI" width="128" height="128">
</p>

[![npm version](https://badge.fury.io/js/heron-ai.svg)](https://www.npmjs.com/package/heron-ai)
[![Go Report Card](https://goreportcard.com/badge/github.com/heron-ai/heron-engine)](https://goreportcard.com/report/github.com/heron-ai/heron-engine)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> **English** | [中文](README.zh-CN.md)

**Heron AI** is a generic multi-agent engine built in Go. No framework dependencies.
Design, orchestrate, and evaluate AI agent workflows.

## Quick Start

```bash
npm install -g heron-ai
heron
```

## Features

- Multi-agent orchestration (Flow + Team + Signal)
- Turn-based agent runtime (Prompt + Tool + Guardrail + HITL + Handoff)
- Extensible (Lua / WASM / external scripts)
- TUI interface (bubbletea)
- File-based storage (JSONL + Markdown)
- MCP protocol support

## Documentation

- [Why Heron AI](docs/why-heron.md) - Problem, philosophy, comparison
- [Getting Started](docs/getting-started.md) - Installation and configuration
- [CLI Reference](docs/cli-reference.md) - All available commands
- [Configuration](docs/configuration.md) - Settings, models, providers
- [Flow Guide](docs/flow-guide.md) - How to design and run flows
- [Agent Guide](docs/agent-guide.md) - Agent configuration and behavior
- [Extension Guide](docs/extension-guide.md) - Lua / WASM / script extensions
- [Examples](examples/) - Sample flow configurations

## Development

```bash
git clone https://github.com/heron-ai/heron-engine.git
cd heron-engine
go build ./cmd/server/
```

## License

MIT - see [LICENSE](LICENSE)
