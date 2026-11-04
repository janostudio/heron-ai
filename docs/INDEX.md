# 文档索引

按"你要做什么"进入，不要从头遍历。每篇文档标注语言：
**中** = 中文，**EN** = 英文。

## 我要先把引擎跑起来

| 文档 | 内容 |
|---|---|
| [getting-started.md](./getting-started.md)（中） | 安装 → 最小可运行配置 → 跑通第一轮 → 确认数据落点 |
| [../examples/README.md](../examples/README.md)（中） | 四个完整示例怎么补 `models.json` 并跑起来 |
| [configuration/flow.md](./configuration/flow.md)（EN） | Flow 配置：阶段、信号路由、团队绑定 |
| [configuration/team.md](./configuration/team.md)（EN） | Team 配置：并行/串行、per_task、策略 |
| [configuration/models.md](./configuration/models.md)（EN） | `models.json`：模型注册表、协议、key 与环境变量展开 |
| [configuration/settings.md](./configuration/settings.md)（EN） | `settings.json`：日志、runtime 上限、web、mcp、env |

## 我要用 CLI 或把 Heron 接进别的系统

| 文档 | 内容 |
|---|---|
| [CLI.md](./CLI.md)（EN） | 全部 flag、TUI、slash 命令、HTTP server、JSON-RPC、`stream-json`、`heron teams`、`heron knowledge` |
| [CONNECT-CLI-JSONRPC.md](./CONNECT-CLI-JSONRPC.md)（中） | `heron-connect` 以子进程方式接入的 JSON-RPC 协议设计与边界 |
| [MULTIMEDIA-INPUT.md](./MULTIMEDIA-INPUT.md)（EN） | 图片/文件等多媒体输入的传输格式（HTTP 与 CLI） |

## 我要理解引擎怎么设计的

| 文档 | 内容 |
|---|---|
| [architecture.md](./architecture.md)（EN） | 三层编排模型、包结构、信号路由、Agent 能力全景 |
| [context-management.md](./context-management.md)（中） | 单个 AgentTurn 的上下文组成、压缩锚定、与 State/Knowledge 的边界 |
| [context-caching-design.md](./context-caching-design.md)（中） | 对标 Claude Code 的上下文优化全景（缓存、裁剪、注入策略） |
| [skill-progressive-disclosure.md](./skill-progressive-disclosure.md)（EN） | Skill / Knowledge 的渐进式披露：先给目录，再按需读正文 |
| [PROMPT-SCHEME.zh-CN.md](./PROMPT-SCHEME.zh-CN.md)（中） | 提示词分几层、每层现有哪些内容、知识如何进入上下文 |
| [generic-engine/26-state-redesign.md](./generic-engine/26-state-redesign.md)（中） | State 的当前实现：会话内工作状态如何落盘与注入 |
| [generic-engine/30-team-sharing.md](./generic-engine/30-team-sharing.md)（中） | Team 分享包：manifest、依赖闭包、安装与导出 |

## 我要配置某个具体文件

| 文档 | 对象 |
|---|---|
| [CONFIGURATION.md](./CONFIGURATION.md)（EN） | 配置总览与加载顺序，从这里跳到具体文件 |
| [configuration/agent.md](./configuration/agent.md)（EN） | `AGENT.md`：persona、tools、loop、hooks、guardrails |
| [configuration/skill.md](./configuration/skill.md)（EN） | `SKILL.md`：frontmatter、scripts、references |
| [configuration/knowledge.md](./configuration/knowledge.md)（EN） | `knowledge/`：条目格式、检索、`heron knowledge` 的产出 |
| [configuration/rules.md](./configuration/rules.md)（EN） | `rules/`：软/硬约束的写法与优先级 |
| [configuration/workspace.md](./configuration/workspace.md)（EN） | 执行后端：本地或远程 SSH |

## 我要排查一次运行

配置工程的排查手册内置在引擎里，随 Skill 一起分发：

| 文件 | 什么时候读 |
|---|---|
| [../builtin/skills/heron-ai-config/SKILL.md](../builtin/skills/heron-ai-config/SKILL.md) | 排查入口：会话在哪、聊天记录在哪、三层 jsonl 各装什么 |
| [../builtin/skills/heron-ai-config/references/debug-triage.md](../builtin/skills/heron-ai-config/references/debug-triage.md) | 判定标准、异常模式对照、从配置预测行为 |
| [../builtin/skills/heron-ai-config/references/session-files.md](../builtin/skills/heron-ai-config/references/session-files.md) | 数据落点、执行日志位置 |
| [../builtin/skills/heron-ai-config/references/token-usage.md](../builtin/skills/heron-ai-config/references/token-usage.md) | 按 team / agent / 轮次统计 token |
| [../builtin/skills/heron-ai-config/references/trace-ids.md](../builtin/skills/heron-ai-config/references/trace-ids.md) | 用 ID 把一次跨层调用串起来 |

CLI 侧的现象与命令见 [CLI.md](./CLI.md)；`heron-ai` Skill 的安装方式见
[../builtin/skills/README.md](../builtin/skills/README.md)。

## 历史档案

这些文档记录的是**当时**的判断与计划，不代表当前实现。读它们是为了理解
"为什么变成现在这样"，不要当成现状说明。

| 文档 | 性质 |
|---|---|
| [AGENT-CAPABILITY-REVIEW.zh-CN.md](./AGENT-CAPABILITY-REVIEW.zh-CN.md)（中） | Agent 能力设计评估 |
| [AGENT-CORE-IMPROVEMENT-PLAN.zh-CN.md](./AGENT-CORE-IMPROVEMENT-PLAN.zh-CN.md)（中） | Agent 核心能力改进计划 |
| [issues/INDEX.md](./issues/INDEX.md)（中） | 已修/在修问题与设计单的索引，逐条带状态 |

## 维护约定

- 新增 `docs/` 根目录文档时，在本索引对应分类下补一行，写清"什么时候读"。
- 文档里的相对链接必须指向真实文件；改文件名时同时改引用方。提交前跑一次：

  ```bash
  python3 scripts/check-docs-links.py --verbose
  ```

  CI 的 `Docs links` 步骤跑的是同一个脚本，链接断了会直接构建失败。

- 配置字段说明以源码为准：字段名改动后，同步 `docs/configuration/`。
  不生效的配置段应当明确标注，而不是留着让人以为能调（见
  [configuration/settings.md](./configuration/settings.md) 的 Inert sections）。
