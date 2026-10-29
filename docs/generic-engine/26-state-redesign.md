# 26 · State 设计（当前实现）

> 状态：现行设计，取代 `design doc 26` 早期的"快照注入"方案。
> 代码基线：`master @ 5167d3d` + `fix/state-memory-redesign`。
> 本文只描述当前实现与取舍；历史事故作为设计依据保留。

## 1. 要解决的问题

早期 State 把快照（goal / confirmed / open_questions / decisions）拼进每轮 prompt，意图是"让 Agent 记住上一轮"。实测结果是相反的：

- 上一轮的结论被下一轮当成自己的结论。实际事故：问仓库 B 时答出仓库 A 的分支和 HEAD（s13 的 genie-agent→cls 日志）。
- `next_steps` 里存的不是待办，而是"上一轮答复"和"重派过程"，越积越多，读起来更像结论。
- 两个承载跨轮工作的 Team 只能把 `state.enabled` 关掉来止损（`default_team.yml`、`owner_team.yml`），等于放弃了 State。

根因不是提示词不够严厉，而是快照缺少四样让模型能判断的东西：**任务标识、状态、时间戳、作废判据**。提示词里的"unless the current task continues it"要求模型做一个它没有信息可做的判断。

## 2. 设计目标与非目标

目标：

- 跨会话的备忘/待办**可以查**，但不自动占上下文。
- 任何进入 prompt 的内容都可归因（谁写的、什么时候、属于哪个任务）。
- 写入由 Agent 自主，引擎只做它确定知道的事（记录引用）。
- 出问题时能定位到文件与条目，而不是"模型记错了"。

非目标（当前明确不做）：

- 向量检索或语义召回；
- 自动摘要、自动抽取"值得记住的事实"；
- TTL 与自动归档；
- 用 state 代替任务台账或 session 记录。

## 3. 三层模型

| 层 | 载体 | 生命周期 | 谁读 | 是否进 prompt |
|---|---|---|---|---|
| 结果与事实 | `sessions/<id>/*.jsonl`、SharedRecord | 会话内持久 | 引擎 / 恢复 / 审计 | 通过 records 块按需进入 |
| 会话状态 | `sessions/<id>/teams/<tid>/state.md`、`.../agents/<tid>/<call>/state.md` | 会话内 | 无读取工具 | **否** |
| 跨会话备忘 | `.agents/data/agents/<agentID>/state/`（index + 每 block 一文件） | 跨会话 | Agent 通过 `State` 工具 | 只进指针，不进条目 |

边界判据：

- 一次任务**发生了什么** → session 与 records，不写 state。
- 会话内**跑到哪一步** → Team/per-call state（仅供审计与恢复）。
- 跨会话**还没做什么、做过哪些判断** → Agent state（需要时自己读）。

任务台账（配置工程里的 `tasks/<task_id>.json`）是另一层：它是"用户要什么、当前 phase"的事实源，面向进度查询，不属于引擎的 State。

## 4. 数据模型

跨会话 state 是一个**目录**，一个 block 一个文件；`index.md` 是摘要。`pkg/types/state.go`：

```go
StateItem{
  ID, Text,
  Status,          // pending | in_progress | completed（仅 next_steps 有意义）
  AddedBy, HandledBy,   // "agent(instance)"
}

StateBlock{ Name, Revision, UpdatedAt, Goal, Items, Workspace, RecordIDs }
StateBlockMeta{ Name, Entries, InProgress, UpdatedAt, Revision }
StateIndex{ Revision, UpdatedAt, Goal, Blocks []StateBlockMeta }

StateSnapshot{ ... }   // 兼容视图：把 7 个 block 组装成一份，供旧调用方
StateList{ UpdatedAt, Revision, Goal, Items }  // 单块读取的返回
```

字段语义约束（写进 `state-management` 模板与指针）：

- `next_steps` 是唯一的 todo 列表，承载状态机：`pending → in_progress → completed`；任何时刻最多一条 `in_progress`。
- `confirmed` 是**已核实**的事实，带标识/数值，不写模糊印象；不参与状态机。
- `decisions` 必须带理由，否则下一轮会重新讨论同一个决定。
- `goal` 单值，回答"这份 state 属于哪个任务"；任务切换时覆盖并清理不再适用的 block。

## 5. 存储与生命周期

目录（跨会话）：

```
.agents/data/agents/<agentID>/state/
├── index.md            # 摘要，State(list) 无 field 时的返回
├── goal.md
├── next_steps.md
├── confirmed.md
├── decisions.md
├── open_questions.md
├── workspace.md
└── record_ids.md
```

每个块文件都是 frontmatter（`block` / `revision` / `updated_at`）+ Markdown body，条目形如：

```
- i-23: 给目录迁移补测试 [status=in_progress, added_by=coordinator(e-1)]
```

- 每次写块：`revision = index.revision + 1`，`updated_at = now(UTC)`，然后刷新 `index.md` 对应行。
- 并发：**锁键是 `agentID + "/" + block`**，不再整份 state 互斥。两个实例改不同块可以同时进行；同一块忙时返回 `state: agent "x" block "y" is busy`，不排队。
- 容量（按块独立计算，不再共享一个池）：
  - 每块 4000 字符；条目上限 40。
  - 超限顺序：丢 `completed` 条目 → 条目减到 10 → 截断条目文本 → （goal）截断文本 →（workspace/record_ids）减半引用 → 仍超则报错。
  - `team.state.max_items/max_chars` 只作用于会话级快照。
- 会话级路径（team / per-call）仍是单文件 `state.md`，不参与目录化；它们只落盘、不注入、无读取工具。

## 6. 读取路径（当前设计的核心）

### 6.1 只注入指针

`internal/state/pointer.go` 的 `PointerText(agentID)` 生成固定文本：

```
## Persistent State
Your cross-session state is a directory, not part of this prompt: .agents/data/agents/<id>/state/
Call the State tool with action="list" (no field) to read the index: which blocks
exist, how many entries each has, whether anything is in_progress, and how old each
block is. Load one block (field=<name>) only when you need its entries.
...
```

注入规则（`internal/runtime/team/runtime.go` 的 `runBatch`）：

- 条件：Agent 声明了内置工具 `State`（`ToolConfig.HasBuiltin("State")`）。**spawned 与静态 call 一致**——两者读的是同一份跨会话 state。
- 块属性：`Kind=state_pointer`、`Source=agent_state`、`Placement=system`、`Stability=stable`、`Priority=60`、不可压缩。
- 放 system 的原因：它是"怎么读"的策略，不是对话内容；`stable` 是因为同一 agent 的路径不变，可留在缓存前缀；不可压缩是因为压掉等于让 Agent 忘了有这个存储。
- 没有 `State` 工具的 Agent 不注入指针，也不注入 `state-management` 模板（`internal/prompt/builtin.go` 的 `BuildSystemPrompt`）。

### 6.2 工具读取

`internal/agent/state_tool.go`，参数 `action/field/text/id/status`，`field ∈ {goal, confirmed, open_questions, decisions, next_steps, workspace, record_ids}`。

- `list` **不带 field** → 返回 `StateIndex`：有哪些块、各自条目数、`in_progress` 数、`updated_at`、`revision`、当前 goal。这是默认读法。
- `list` 带 field → 返回 `StateList`：该块的 `{updated_at, revision, goal, items}`。`goal` 随每次读取返回，作为"是否同一任务"的标签。
- `get` 按 id 读单条；`add/remove/update` 维护条目；`status` 只在 `next_steps` 上生效。
- 写 `goal` 用 `add`（等价 `SetGoal`）；`update` 不支持 goal。
- agentID 从执行上下文解析，Agent 不能读写别的 Agent 的 state。

## 7. 写入路径

| 写入者 | 写什么 | 说明 |
|---|---|---|
| Agent（State 工具） | goal 与全部列表 | 唯一能写自然语言条目的路径 |
| Team runtime（Team 级） | `record_ids` | `team.state.enabled` 时在 TeamTurn 结束时追加 |
| Team runtime（per-call） | `record_ids`、`workspace` | 不写 goal、不写 reply |
| Spawn（内联与合成 call） | `goal = item JSON`（仅当 goal 为空）、`workspace` | 子任务即初始目标 |

明确**不再发生**的写入：

- 本轮 `reply` → `confirmed` / `next_steps`（三处已删）；
- `call.responsibility` → `goal`（职责不是任务）；
- Team 的整段答复 → team state 的 `next_steps`。

理由：这些写入把"结果"混进"记忆"，而结果已经在 session 与 records 里；重复一份只会被下一轮误读。

所有引擎侧写入都是**块级**的（`SetGoalIfEmpty` / `AppendWorkspace` / `AppendRecordIDs`），只碰目标块，不会全量重写 Agent 的 state——否则一次 spawn 结果就能覆盖模型刚写的待办。

`state.enabled` 现在的含义是"是否落盘会话级快照"，与"是否进 prompt"解耦。会话级快照当前没有读取工具，保留它是为了审计与恢复；本轮不改它。

## 8. 与 knowledge 的边界

两者结构同构（固定指针 + 按需读取），但职责不同：

| | knowledge | state |
|---|---|---|
| 内容 | 稳定结论、问题模式、仓库事实 | 当前任务的备忘与未完成动作 |
| 变化频率 | 低 | 高 |
| 写入 | 脚本/模型沉淀，带 meta，参与归档 | Agent 自主增删改 |
| 过期判据 | `last_seen` + 归档规则 | `goal` + `updated_at`，由模型判断 |
| 典型错误 | 拿旧结论当事实 | 拿旧任务当本轮 |

因此：**todolist 不写 knowledge**。knowledge 的条目要能长期成立，待办不是；且 knowledge 写入有格式与归档约束，活状态会被判成脏数据。

## 9. 失效判断

模型侧（唯一实际生效的判据）：

1. 读 `list`，先看 `goal` 与 `updated_at`；
2. 条目属于别的任务 → 视为过期，不引用；
3. 需要沿用 → 先核实（读文件/跑命令），再更新条目。

工程侧：有 `status`，但**没有** TTL、自动归档、以及"扫一遍未完成项"的入口。`status` 只是让模型能表达完成，不代表引擎会据此回收条目。这是当前最大的缺口，见 §11。

## 10. 兼容与迁移

- **Prompt 形态变化**：声明 `State` 的 Agent 的 system prompt 多一段固定指针；未声明的少一段 `## State Management`。这是一次性的缓存 epoch 变化，之后保持稳定。
- **行为变化**：跨会话记忆不再自动喂给子 Agent。依赖"不读也能看到上次结论"的 Agent，必须在其 AGENT.md 里加"开始前先 `State(list)`"。
- **旧数据自动迁移**：首次访问某个 Agent 的 state 时，若存在旧单文件 `state/state.md` 且没有 `index.md`，会按 block 拆分写入目录，把原文件改名为 `state.md.bak`（保留可回溯），然后删除旧文件。迁移只在读取/写入入口触发一次，不需要停机。
- **迁移后建议清理**：旧文件里的 `goal` / `next_steps` 可能是旧任务残留；迁移完成不等于内容有效，建议在新的 `goal` 上覆盖当前任务、清掉无关条目。
- `state.enabled: false` 的 Team 不需要改配置；开启只会多写会话级快照，不进 prompt。

## 11. 已知缺口

1. **无外部查询入口**：没有 CLI 子命令列 state，定时任务只能直接读 `state/index.md`。若要做"定时查询任务完成情况"，应扫任务台账（`phase != done`）而不是 state。
2. **无 TTL / 归档**：`completed` 条目只能靠 Agent 主动 `remove`；长期不清理会触发 40 条上限。截断会优先丢 `completed`，但仍是静默的。
3. **会话级 state 无读取方**：per-call/team 快照只写不读。当前明确**不处理**：保留写入，不做工具，也不注入。
4. **与 compaction 无联动**：state 不参与上下文压缩决策，也不在压缩前被提醒"把关键结论写进 state"（Anthropic memory tool 与 context editing 是配套设计的）。
5. **任务边界靠模型判断**：引擎不提供 task_id，`goal` 是自由文本；"这份 state 属于哪个任务"仍由模型读 `goal` + `updated_at` 判断。
6. **索引与块可能不一致**：`index.md` 是派生数据。若进程在写块后、写索引前崩溃，索引会滞后；`LoadIndex` 在索引缺失时会从块重建，但不做逐行校验。

## 12. 代码索引

| 关注点 | 位置 |
|---|---|
| 数据结构 | `pkg/types/state.go` |
| 目录布局、块/索引编解码、状态校验 | `internal/state/blocks.go` |
| 存储、路径、块级读写、迁移、容量与截断 | `internal/state/store.go` |
| CRUD、块级锁、`ListWithMeta`、status 规则 | `internal/state/crud.go` |
| 指针文本 | `internal/state/pointer.go` |
| State 工具（含无 field 的索引读取） | `internal/agent/state_tool.go` |
| 注入点（Team runtime） | `internal/runtime/team/runtime.go` `runBatch` |
| 注入点（内联 spawn） | `internal/agent/spawn.go` `runChild` |
| 合成 call 的 state 写入 | `internal/runtime/team/spawn.go` `saveSpawnedAgentState` |
| 系统提示模板 | `internal/prompt/builtin.go` `stateManagementTemplate` |
| 配置说明 | `docs/configuration/agent.md`（State Tool）、`docs/configuration/team.md`（state 字段） |
