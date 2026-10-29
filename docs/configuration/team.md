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

### Command 的参数与模板

`command` 支持 `args`（位置参数）与 `env`（环境变量），两者的值都支持
`${...}` 占位符。占位符分两类：

**会话标识**（固定字段）：

| 占位符 | 取值 |
|---|---|
| `${flow_session_id}` | 当前 FlowSession ID |
| `${flow_turn_id}` | 当前 FlowTurn ID |
| `${team_id}` / `${team_turn_id}` | 当前 Team / TeamTurn ID |
| `${call_id}` / `${call_turn_id}` | 当前 Call / CallTurn ID |
| `${attempt}` | 当前尝试次数 |
| `${recovery_of}` | 被恢复的 CallTurn ID（无则为空） |

**SharedRecord 字段**（把上游记录的值传进确定性命令）：

| 占位符 | 取值 |
|---|---|
| `${records.<Name>.Summary}` | 名为 `<Name>` 的记录的 `summary` |
| `${records.<Name>.Data.<field>}` | 该记录 `data` 里的 `<field>` |
| `${records.<Name>}` | 简写，等价于 `.Summary` |
| `${records.<Name>.RecordID}` / `.Name` | 记录 ID / 名称 |

`<Name>` 必须是该 call 在 `inputs` 里声明为输入的记录（`flow_records` /
`team_records` / `records`）。示例——把"目标仓库"记录的值传给占槽脚本：

```yaml
acquire_slot:
  type: command
  command:
    command: "bash .agents/skills/worktree-guard/scripts/acquire.sh"
    env:
      AUTO_REPO: "${records.WorktreeTarget.Data.repo}"
      HERON_FLOW_SESSION_ID: "${flow_session_id}"
  inputs:
    flow_records: [WorktreeTarget]
```

> **缺字段 = 报错**。若 `${records.X.Y}` 引用的记录或字段不存在，该 command call
> 以 `failed` 结束，**不会**替换成空字符串。这是有意为之：静默传空会让命令带着
> 错误的值继续跑（正是这个议题要消灭的失败模式）。请确保 `inputs` 声明了需要的记录。

## Fields

| Field | Type | Description |
|---|---|---|
| `id` | string | Team identifier |
| `goal` | string | Team 目标 |
| `calls` | object | Team 内的 Agent、Command、Webhook 配置 |
| `output` | object | Team 对外发布的 SharedRecord |
| `state` | object | 可选 Team State 配置（会话级快照，落盘不注入） |
| `workspace` | object | 执行后端（local / 远程 SSH），见 [workspace](./workspace.md) |

> Team State 与 per-call Agent State 只落盘，**不再拼进 prompt**：把上一轮的答复当作
> `next_steps` 注入，会让它成为下一轮任务的"结论"。Agent 的跨会话 state 是一个目录
> （`state/index.md` + 每 block 一个文件），走 `State` 工具按需读取（见
> [agent.md](./agent.md#state-tool)）；Team/per-call state 没有读取
> 工具，仅作为审计与恢复记录保留 `record_ids` / `workspace` 引用。

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
