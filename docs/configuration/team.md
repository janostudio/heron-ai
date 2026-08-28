# Team Configuration

`Flow` 负责调度 Team，Team 负责组织自己的 Call。
Call 是 Team 内部的一次执行定义，目标可以是 Agent、Shell Command 或 Webhook。
Call 不是额外的编排层，用户需要理解的主层级仍然是：

```text
Flow → Team → Call → Agent / Command / Webhook
```

## Structure

```yaml
id: diagnose_team
goal: 通过快照、探索和分析形成诊断结论。

calls:
  snapshot:
    type: command
    command:
      command: bash .agents/skills/<skill-id>/scripts/git_snapshot.sh
      timeout: 30s
    output:
      record: GitSnapshot

  explorer:
    type: agent
    agent: explorer
    responsibility: 只读检查 Workspace，补充诊断事实。
    output:
      record: ExplorationReport

  diagnose:
    type: agent
    agent: root-cause-analyst
    depends_on: [snapshot, explorer]
    inputs:
      flow_records: [GitSnapshot, ExplorationReport]
    output:
      record: DiagnosisReport

output:
  from: diagnose
  record: DiagnosisReport
  scope: flow
```

`calls` 只是 Team 内配置的调用集合，不是一个额外的协作层级。
调用的类型由 `type` 决定：

```text
agent → 调用本地 Agent，内部运行 Model / Tool Loop
command  → 调用固定 Shell
webhook  → 调用固定 URL
```

Command 脚本可以由 Skill 打包。推荐不要使用全局
`.agents/scripts/`：

```text
.agents/skills/<skill-id>/
├── SKILL.md
└── scripts/
    └── check.sh
```

Team 调用：

```yaml
command:
  command: bash .agents/skills/<skill-id>/scripts/check.sh
```

这样 Skill 可以作为一个完整目录复制到另一个 Flow 中复用。

## Fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Team identifier |
| `goal` | string | Team 目标 |
| `calls` | object | Team 内的 Agent、Command、Webhook 配置 |
| `output` | object | Team 对外发布的 SharedRecord |
| `memory` | object | 可选 Team Memory 配置 |

### Call

| Field | Type | Description |
|---|---|---|
| `type` | string | `agent`、`command` 或 `webhook` |
| `agent` | string | `agent` 使用的 Agent 定义 |
| `command` | string/object | `command` 类型使用的 Shell |
| `webhook` / `url` | object/string | `webhook` 类型使用的 URL |
| `depends_on` | array | Team 内其他调用的名称 |
| `inputs` | object/array | 显式输入和 SharedRecord |
| `output` | object | 本次调用发布的 SharedRecord |
| `responsibility` | string | Agent 的职责说明 |
| `timeout` | string | 本次调用超时时间 |

## 调度规则

```text
没有依赖的调用 → 自动并行
有依赖的调用   → 依赖完成后执行
依赖多个调用   → 读取声明的 SharedRecord 后执行
```

Team 不要求必须有默认 Agent，也不要求必须有汇总 Agent：

```text
Review Team
  ├── Security Agent
  ├── Performance Agent
  └── Maintainability Agent
```

如果需要汇总，配置一个普通的 Agent 即可，不需要特殊的
`default_agent`、`worker` 或 `aggregator` 类型。

## SharedRecord

Team 内外的信息通过 SharedRecord 传递：

```text
Diagnose Team
  → DiagnosisReport

Fix Team
  ← DiagnosisReport
  → ChangeSet

Test Team
  ← ChangeSet
  → VerificationReport
```

调用不会自动读取全部历史。需要什么信息，就在 `inputs` 中声明什么信息。

## Terminology

`Call` 是 Team 的执行项，不是独立的协作实体：

```text
Team
├── Call: Agent
├── Call: Shell Command
└── Call: Webhook
```

运行时不再使用 `Member` 作为核心概念。历史 session 中的
`member_id`、`member_turn_id` 等字段只作为旧数据迁移输入，不作为当前
配置或运行时 API。
