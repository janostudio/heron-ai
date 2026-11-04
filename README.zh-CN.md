# Heron AI

<p align="center">
  <img src="assets/icon.png" alt="Heron AI" width="128" height="128">
</p>

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

> [English](README.md) | **中文**

**Heron AI** 是一个纯 Go 实现的多 Agent 通用引擎，不依赖任何 Agent 框架。

## 能力

- **三层编排** —— Flow 负责路由，Team 负责调度，Call 负责执行。Agent、shell
  Command、Webhook 都是 Call，没有第四层。
- **回合制 Agent 运行时** —— 提示词组装、工具循环、guardrail、HITL 审批、handoff、
  结构化输出。
- **多种运行方式** —— 交互 TUI、`--prompt` 单轮、stdin/stdout 上的常驻 JSON-RPC、
  带 SSE 的 HTTP server，以及给外部调用方用的 `stream-json` 客户端。
- **Team 分享包** —— `heron teams` 安装 / 卸载 / 导出自包含的 Team，带 manifest、
  依赖闭包与脚本复核。
- **配置即文件** —— 全部在 `.agents/` 下，没有数据库，没有常驻守护进程。
- **文件存储** —— 每个会话一份 append-only JSONL，加 Markdown 状态快照。
- **MCP** —— 支持 stdio 与 HTTP 两种 transport，启动时连接。
- **多媒体输入** —— HTTP 与 CLI 都接受图片/文件内容块。
- **无框架依赖** —— Go 标准库，加少量 UI 与解析库。

## 快速开始

```bash
# 安装
npm install -g @qinghuangniao/heron-ai

# 配置 key（也可以写在 .agents/models.json 的 api_key 里）
export OPENAI_API_KEY=sk-your-key

# 在当前目录建好 .agents/ 后，先验证再运行
heron --check-config

# 运行
heron                                    # 交互式 TUI
heron --prompt "你好"                     # 跑一轮就退出
heron --list                             # 选一个历史会话继续聊
```

Heron 从**当前工作目录**下的 `.agents/` 读配置，请在工作区根执行。
一个能跑起来的最小配置需要哪四个文件，见
[快速开始](./docs/getting-started.md)。

## 示例

每个示例都是一份完整的 `.agents/` 目录，可以直接复制当起点。

| 示例 | 说明 | Agent 数 | Team 数 |
|------|------|---------|--------|
| [simple-qa](./examples/simple-qa/) | 单 Agent 问答 | 1 | 1 |
| [code-review](./examples/code-review/) | 多 Agent 代码评审 | 3 | 1 |
| [blog-writer](./examples/blog-writer/) | 三 Team 内容流水线 | 4 | 3 |
| [novel-rp](./examples/novel-rp/) | 多角色扮演 | 3 | 3 |

每个示例都要自己补一份 `.agents/models.json` —— 模型与 key 属于使用方本地环境，
不随示例分发。通用模板与逐个示例的启动命令见
[examples/README.md](./examples/README.md)。

`examples_test.go` 会用真正的加载器逐个加载示例：哪个示例改坏了，CI 就会红，
不会悄悄过期。

## 文档

从 [docs/INDEX.md](./docs/INDEX.md) 进 —— 它按"你要做什么"分流，并标注每篇的语言。
最常用的几篇：

- [快速开始](./docs/getting-started.md) —— 四个文件从零跑通第一轮
- [架构](./docs/architecture.md) —— 引擎内部是怎么组织的
- [配置总览](./docs/CONFIGURATION.md) —— 所有配置文件，逐项链接到细节
- [CLI 命令](./docs/CLI.md) —— 全部 flag、TUI、HTTP server、JSON-RPC、`stream-json`、`teams`
- [JSON-RPC 接入](./docs/CONNECT-CLI-JSONRPC.md) —— 外部 CLI 以子进程方式接入
- [多媒体输入](./docs/MULTIMEDIA-INPUT.md) —— 图片与文件内容块
- [上下文管理](./docs/context-management.md) —— 一次 AgentTurn 到底发什么给模型
- [Team 分享](./docs/generic-engine/30-team-sharing.md) —— 包 manifest 与安装规则

## 配置

全部配置都在 `.agents/` 下：

```
.agents/
├── flows/         # Flow 定义（YAML）
├── teams/         # Team 定义（YAML）
├── agents/        # Agent 目录（<id>/AGENT.md + knowledge/ + rules/）
├── skills/        # Skill 定义（SKILL.md + scripts/ + references/）
├── knowledge/     # 知识库文件
├── rules/         # 全局规则
├── _teams/        # 已安装的 Team 包（由 `heron teams` 管理）
├── models.json    # 模型注册表
├── settings.json  # 引擎行为配置
└── data/          # 运行数据：sessions/、logs/
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

`model` 按 `name` 选默认模型。`api_key` 支持 `${ENV_VAR}` 展开；整段省略时按协议读
`OPENAI_API_KEY` / `ANTHROPIC_API_KEY`。`${VAR}` 展开为空**会直接报错**，不会退回
另一个厂商的 key。

### settings.json

可选。承载日志策略、runtime 安全上限、`WebSearch` / `WebFetch` 的数据源、MCP
服务、额外的 Bash 策略短语，以及注入子进程的环境变量。已经没有任何代码读取的
配置段在
[settings.md 的 Inert sections](./docs/configuration/settings.md#inert-sections)
里明确列出，免得有人去调一个不起作用的开关。

## 开发

```bash
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
go test ./...
```

CI 会跑 `scripts/check-docs-links.py` 校验文档之间的相对链接：改了文件名却没改
引用，会直接构建失败，而不是留一个 404 给下一个读者。

## 许可证

MIT —— 详见 [LICENSE](LICENSE)
