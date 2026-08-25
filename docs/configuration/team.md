# Team Configuration

`Flow` 负责调度 Team，Team 负责组织自己的 Agent、Command 和 Webhook。
不再引入 `Member`、`Worker` 或 `Aggregator` 作为用户需要理解的领域概念。

## Structure

```yaml
id: diagnose_team
goal: 通过快照、探索和分析形成诊断结论。

calls:
  snapshot:
    type: command
    command:
      command: bash .agents/scripts/git_snapshot.sh
      timeout: 30s
    output:
      record: GitSnapshot

  explorer:
    type: subagent
    agent: explorer
    responsibility: 只读检查 Workspace，补充诊断事实。
    output:
      record: ExplorationReport

  diagnose:
    type: subagent
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
subagent → 调用本地 Agent，内部运行 Model / Tool Loop
command  → 调用固定 Shell
webhook  → 调用固定 URL
```

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
| `type` | string | `subagent`、`command` 或 `webhook` |
| `agent` | string | `subagent` 使用的 Agent 定义 |
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

## Compatibility

当前运行时内部仍保留旧的 `Member` 类型和 `members` 配置读取能力，用于兼容
旧配置和事件恢复；新配置和文档统一使用 `calls`，用户只需要理解
`Flow → Team → Agent / Command / Webhook`。
