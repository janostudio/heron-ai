# 设计探讨：ScheduleWakeup（模型定时自唤醒）

> 状态：**提案，未实现**（2026-10-29 分析）
> 性质：需求是"派出去的长任务，过一段时间我要回来看一眼"，落点**只给模型一个异步工具**；引擎不判断该不该看、该不该催、该不该汇报
> 关联：`internal/agent/team_tool.go`、`internal/runtime/flow/{runtime.go,dispatch.go,resume_mailbox.go,resume_tool_result.go}`、`internal/app/runtime.go`、`pkg/types/{agent_runtime.go,turn.go,event.go,execution.go,domain.go}`
> 基线：`master @ 2afcff2`（2026-10-29）
>
> **一句话**：模型在自己回合里留下"我什么时候再来"的便签，引擎把它存进事件流并挂起，到点从留便签的那个 Team 重新起跑，把便签和这段时间的事实一起交回去。等待期间不占执行槽、不烧 token。

---

## 一、需求与现状

### 1.1 场景

入口 agent（下称 coordinator）把一个任务交给下游 Team，下游要跑很久（小时到天级）。coordinator 应该能：

1. 过一段时间回来看看**是否走错路**，而不只是"跑完没有"；
2. 判断是否需要介入（补约束、催一次、叫停）；
3. 一切正常时**顺带**给用户一个进展反馈。

这三件事共同的前提是：coordinator 得有"过一会儿再来"的能力。现在没有。

### 1.2 现状：所有推进入口都是外部事件

| 入口 | 触发者 | 位置 |
|---|---|---|
| `Start` | 用户首次输入 | `internal/runtime/flow/runtime.go:158` |
| `HandleInputWithContext` | 用户说话 | `runtime.go:194` |
| `ResumeWithContext` | 工具结果 / 中断恢复 | `runtime.go:254` |
| `ResumeApproval*` | 人点审批 | `runtime.go:349` |
| `ResumeForToolResult` | 下游 Team 完成回流 | `resume_tool_result.go:48` |
| `ResumeForMailbox` | 邮箱里有消息，被唤醒器拉起 | `resume_mailbox.go:28` |

前五个都是"外面已经发生了什么"。唯一看起来"自动"的 `ResumeForMailbox`，开头先查邮箱（`resume_mailbox.go:58`），没消息直接返回；而消息只有三个来源——用户输入、Team 的 `activate`、Team 的结果回投，**三个都要求有人先动过**。

于是形成闭环：要唤醒得有消息 → 要消息得有人动 → 要人动得先被唤醒。

### 1.3 四个具体断点

**断点 1：`Team` 工具派完就断，且明确禁止回头。**

`internal/agent/team_tool.go:147-148` 返回给模型的原话是：

```
Handed to %s. It runs in the background; you will be notified when it finishes.
Do not wait for it and do not re-dispatch the same task.
```

注释写明了原因：早期模型会 `sleep` 循环等结果，所以引擎把"等待"这条路主动堵死了。堵得对，但没给替代品。

**断点 2：唯一能查状态的 `Collect` 是阻塞到底的，且只认 `Spawn`。**

`internal/agent/collect.go:95` 走 `waitTerminal`（`:128`），语义是"阻塞到终态"。没有 peek 模式，也不解析 `Team` 产生的 `team_run` 任务。HTTP 侧有 `HandleTaskStatus`（`internal/view/handler.go:477`）能查单个 task，但**那是给外部调用者的，模型看不到**。

**断点 3：下游的执行细节在事件流里，但没有任何通道把它交给 coordinator。**

`ToolTask` 有 `Progress` / `Message` 字段（`pkg/types/agent_runtime.go:151`），但 `team_run` 这条路径从不写——对 coordinator 而言它们是空的。

**但数据本身不缺**：事件流已经按工具调用粒度记录了执行过程。`EventToolCallStarted` / `Completed` / `Failed`（`internal/agent/runtime.go:1653` / `:1671` / `:1677`）带 `tool_name`、`arguments`、`round`、结果正文；事件头带 `TeamID` / `CallID` / `CallTurnID`（`pkg/types/event.go:9-24`）。"下游刚才调了什么、成功没有"完全查得到。

缺的是**注入通道**：没有任何代码把这些事件整理成一段事实交给模型。coordinator 想知道下游在做什么，只能自己读会话 jsonl——而 `Read`/`Grep` 面对几千行事件流，成本高且容易读错。

**断点 4：单回合装不下"一天"。**

`RestartSafe: false`（`team_tool.go:137`）意味着进程重启后该任务直接判失败：

```go
// internal/agent/task.go:551-563
if task.Status == types.ToolTaskRunning && !task.RestartSafe {
    task.Status = types.ToolTaskFailed
    task.Error = "task was interrupted by process restart; explicit retry is required"
    ...
    e.notifyDone(task)
```

再加上 `MaxAgentRounds` 默认 200（`pkg/types/execution.go:379`）。**推断**（未实测时长）：一天级的下游工作无法由单个 TeamTurn 承载，必然跨回合；而当前跨回合的唯一方式是"重启一个新任务"，上下文靠 coordinator 重新拼。

### 1.4 因此需要的不是"引擎定时巡检"

把"该不该看/是不是跑偏/要不要汇报"放进引擎，要做的事是：算停滞信号、比对 scope、生成巡检单。这条路的问题不在实现，在于：

- 引擎不掌握派单时的完整意图，判断依据不足；
- 阈值一旦写死就是黑魔法，误判了没人调得动；
- 拍平的固定节奏不匹配真实场景——等 CI 是几分钟，等别的团队是一天。

正确切分是：**引擎提供能力（唤醒原语、调度执行、期间事实、出站通道），节奏与判断归模型。**

---

## 二、目标与非目标

### 2.1 目标

给模型一个异步工具 `ScheduleWakeup`：登记"我在某个时间点回来"，本轮正常结束；到点由引擎起一个新 FlowTurn，把模型自己写的便签 + 这段时间的事实变化交给它。

### 2.2 引擎负责的四件事

| 能力 | 内容 |
|---|---|
| 唤醒原语 | 一个可登记、可撤销、可持久化的 timer |
| 调度执行 | 到点判定 + 串行化 + 起 turn |
| 期间事实 | 任务状态、record 增量、未读消息数、用户是否说过话 |
| 出站通道 | 唤醒回合的结论能作为独立交付项送到用户 |

引擎**不产**结论性描述（"已停滞""已偏离""建议叫停"）。

### 2.3 非目标

- 不做周期唤醒（`every: 2h`）——见 §8.3。
- 不做引擎侧停滞/偏离判定——见 §1.4。
- 不做 peek 查询工具——facts 在唤醒时主动注入，模型不需要自己查，见 §8.6。
- 不做 `noop` 自报标记——见 §8.5。
- 不做跨进程分布式定时器——单进程内存索引 + 事件流恢复即可，见 §4.3。
- 不给 Team 增加 `timer` 类型的 Call——见 §8.1。

---

## 三、设计

### 3.1 工具形态

```
ScheduleWakeup(when, note)
  → { wakeup_id, fire_at, team_id }

CancelWakeup(wakeup_id)
  → { cancelled: bool }
```

| 参数 | 类型 | 约束 | 说明 |
|---|---|---|---|
| `when` | string | 必填 | 相对时长（`30m` / `6h` / `2d`，Go duration 语法 + `d`）或绝对时间（RFC3339，**必须带时区**）。相对时长按登记时刻 + 时长解析，不受后续时钟漂移影响。引擎内部统一按 UTC 存储与比较（与 `time.Now().UTC()` 一致）。小于 `min_after` 直接拒绝 |
| `note` | string | 必填，≤ 2000 字符 | 写给自己的检查项：**到时候判断什么、什么情况下做什么**。超长拒绝而不是截断——截断会悄悄改掉模型的原意 |

语义三条：

1. 登记即返回，模型可以继续本回合，也可以直接结束；**回合结束后会话落 `waiting_input`，不占执行槽、不烧 token**。
2. 到点由引擎起一个新 FlowTurn，`origin = "wakeup"`（现有只有 `user`/`mailbox`/`resume`/`tool`，见 `runtime.go:641-646`、`team_call.go:18`）。
3. 唤醒的 input 是**模型当初自己写的 `note`** + 期间事实。模型写什么，醒来就看到什么。

**不预设"什么时候该叫醒我"的条件**：这条检查点有没有内容、要不要打扰用户、要不要再登记下一次，都由模型在醒来那一轮结束时自己判断。醒来发现没事就什么都不做（§3.7）。

**注册位置**：`internal/agent/runtime.go:2459` 的 `builtinSchemas`，与 `Team`（`:2572`）、`Collect`（`:2583`）同一层。

### 3.2 数据与事件

```go
// pkg/types/wakeup.go（新增）
type WakeupRecord struct {
    ID            string    // wk_xxx
    FlowSessionID string
    TeamID        string    // 登记者：唤醒后从这里起跑
    CallID        string    // 登记者所在的 call，用于重建 activation
    FireAt        time.Time
    Note          string
    LastSeq       int64     // 上次唤醒时的事件序号；下次只处理 seq 之后的事件，避免全量 replay
    Status        string    // scheduled | fired | skipped | cancelled | expired
    Fires         int       // 该会话累计自发唤醒次数
    CreatedAt     time.Time
    FiredAt       *time.Time
}
```

事件（`pkg/types/event.go`，与 mailbox 事件同层）：

| 事件 | 用途 |
|---|---|
| `wakeup.created` | 登记，恢复的唯一依据 |
| `wakeup.fired` | 真正起了 turn |
| `wakeup.skipped` | 被门禁推掉（会话忙 / 等工具 / 用户刚说完话 / 会话已终止） |
| `wakeup.cancelled` | 模型主动撤销 |
| `wakeup.expired` | 超出 `max_fires` / `max_chain` 或绝对时限 |

`skipped` 必须记，因为它决定模型下次醒来时知不知道"我上次被推掉了"。

### 3.3 调度器

新增 `internal/runtime/flow/wakeup_scheduler.go`：

```
WakeupScheduler
├─ pending map[sessionID]minHeap[WakeupRecord]   // 内存索引，非事实源
├─ loop: 等最近 fire_at（timer 或 新登记信号）
└─ fire(record):
     ├─ wakeIfIdle 判据（复用 dispatch.go:251）
     └─ ResumeForWakeup(ctx, sessionID, record)
```

挂在现有注入点旁边（`internal/app/runtime.go:388` 的 `SetWaker` 之后），不新造生命周期。

### 3.4 唤醒执行

新增 `ResumeForWakeup`，与 `ResumeForMailbox` 平行：

```
ResumeForWakeup(ctx, sessionID, record)
  ├─ resumeMu.Lock()                       ← 与 HandleInput / ResumeForMailbox 同一把
  ├─ 读 session，过四道门（§4.2）
  ├─ 写 wakeup.fired
  ├─ 构造 activation{teamID: record.TeamID, callerTeam: ""}
  ├─ 组装 ContextBlock：
  │    [automated] 这是你 04:12 自己登记的检查点，不是用户说的话
  │    [note]      看 sandbox 镜像构建是否跑偏
  │    [facts]     task running 3h12m；最近 10 个动作；新增 record 2 条；
  ├─ runTurnWithOrigin(..., turnOriginWakeup)
  └─ finishTurn(...)
```

#### facts 块从哪来

**唤醒的价值全在这段文字上。** 没有它，叫醒一个 agent 只会得到"还在跑"，这个工具就没有意义。

好在数据全都有——**来自已有的事件流与任务记录，不新增工具、不改 Team 执行路径**：

| 事实 | 来源 | 说明 |
|---|---|---|
| 下游还在跑吗 | `ToolTask.Status` | queued / running / completed / failed / cancelled |
| 跑了多久 | `ToolTask.StartedAt` → now | |
| 最近做了什么 | `tool_call.started` / `completed` / `failed`，按事件头 `TeamID` 过滤 | 取最近 N 条（默认 10）：工具名 + 参数摘要（截 120 字符）+ ok / failed |
| 最后一次动作距今多久 | 同上，最近一条事件的时间戳 | 只给时长，不给"卡住了"的结论 |
| 是不是在反复失败 | `tool_call.failed` 按工具名连续计数 | 只给次数，不下判断 |
| 有产出吗 | `shared_record.published`（`TeamID` 匹配） | 标题 + id，不搬正文 |
| 下游完成了吗 | `team_turn.completed` / `ToolTask.Status` | |
| 用户这段时间说过话吗 | `flow_turn.started` 且 `origin == "user"` | 次数 + 最近一次时间 |
| 上次想来看却被推掉了没有 | `wakeup.skipped` | 让模型知道"我上次没看成"，不是没来过 |

**增量读取**：`WakeupRecord.LastSeq` 记下上次唤醒时的事件序号，下次只处理 `seq > LastSeq` 的事件，避免每次唤醒 replay 整个 jsonl。

**截断**：facts 块整体上限 2000 字符（与 `note` 同级）。超出时先丢粗粒度项（用户发言次数、最后一次动作距今），保留"最近动作"与"record 增量"——前者是判断依据，后者是产出证据。

**关键：从 `record.TeamID` 起跑，不是 entry team。** 理由同 `resume_mailbox.go` 里"谁有邮件从谁起"（`:58-114`）：唤醒的目标是"当初登记这件事的那个 Team"，从 entry team 起会让它拿到一张自己没派过的便签。

### 3.5 流程

```
 模型回合内
 ┌─────────────────────────────────────────────────────────────┐
 │ ScheduleWakeup(when="6h", note="看 sandbox 跑偏")           │
 └───────────────────────────┬─────────────────────────────────┘
                             ▼ wakeup_tool.go Execute()
                ┌────────────────────────────┐
                │ 前置校验（拒绝即返错）     │
                │ · 终端态会话               │
                │ · 正在等工具/审批          │
                │ · pending 数 / 当日次数    │
                └────────────┬───────────────┘
                             ▼
                ┌────────────────────────────────┐
                │ 写 wakeup.created      │ ← 事件流 = 唯一事实源
                └────────────┬───────────────────┘
                             ▼ 返回 {wakeup_id, fire_at}
                finishTurn() → 会话落 waiting_input
                             │
        ┌────────────────────┴────────────────────┐
        │ 模型不在场：不占槽、不烧 token          │
        │ 调度器 min-heap 等最小 fire_at          │
        └────────────────────┬────────────────────┘
                             ▼
                        到点，四道门（§4.2）
                             ▼
                        ResumeForWakeup
                             ▼
                  从登记者 Team 起新 turn
                             ▼
              ┌──────────────────────────────┐
              │ 模型决策                     │
              └──┬────────┬────────┬─────────┘
                 ▼        ▼        ▼
              继续推进  需要介入     无事
                 │        │           │
                 ▼        ▼           ▼
              再登记   催/叫停    什么都不做
             （或不登记=停）      turn 自然结束
```

### 3.6 状态机

**会话**（唤醒只在这两个位置动）：

```
waiting_input ──[到点且空闲]──► running ──[turn 结束]──► waiting_input
     ▲                                                    │
     └──────[用户说话 / mailbox / wakeup]───────────────────┘
              ※ 用户消息永远优先；定时任务让位并顺延
```

**单条 timer**：

```
scheduled ──┬─[到点且空闲]──────────► fired ──► (模型决定是否再登记)
            ├─[忙 / 等工具 / 用户刚说话]─► skipped ──► 顺延回 scheduled
            ├─[CancelWakeup]───────────► cancelled
            ├─[超出 max_fires / 时限]──► expired
            └─[进程重启]─────────────► replay 重算 fire_at，回 scheduled
```

重启那一条是分水岭：timer 存在事件流里，不和 session 同生共死。

### 3.7 边界情形

这些行为不定义清楚，实现时必然各写各的。

**醒来发现没事**

模型什么都不做即可：不产生 Delivery，不再登记下一次，turn 正常结束。**不需要任何"本轮无事"的上报**——理由见 §8.5。

**多条同时到点**

同一会话的多条 wakeup 到期时**合并成一次唤醒**：一个 turn，facts 块里列出全部到期的 note。拆成多个 turn 会让模型对同一批事实重复判断，且每次都要付全量上下文成本。

**唤醒链深度**

`醒 → 再登记 → 醒` 是合法用法（§8.3），但要有硬停：单条链最多 `max_chain`（默认 20）次连续唤醒，或 `max_fires_per_day` 先到。超限时注销该会话全部待触发 wakeup，写一条 `wakeup.expired` 注明原因。

**会话已终止**

到点时若会话处于 `failed` / `cancelled` / `interrupted`：不唤醒，记 `wakeup.skipped`（payload 注明终态）。**不主动恢复会话**——恢复是 `Recover` 的职责，不是定时器的。

**登记者 Team 已不存在**

Flow 定义可能在等待期间变更（team 删除、改名）。到点时若 `record.TeamID` 在定义快照里找不到：降级到 entry team 起跑，并在 facts 块里写明"原登记 Team 已不存在"。**不静默丢弃**——模型需要知道自己的便签指向了一个不存在的对象。

**唤醒回合正在进行时又到点**

同一会话同一时刻只允许一个 wakeup turn，后续到期的顺延，与 §4.2 第一道门同一处理。

---

## 四、关键约束

每条都对应一个具体的坑，不是风格要求。

### 4.1 从登记者起跑

见 §3.4。补充：`activation.callerTeam` 留空，与"被自己的派发结果唤醒"是同一种"无激活者"情形（`resume_mailbox.go:114` 的注释）。

### 4.2 四道门

| 门 | 判据 | 动作 | 依据 |
|---|---|---|---|
| 会话是否 running | `Status == running` | 顺延重试（复用 `scheduleWakeRetry`，`dispatch.go:303`） | 可能有 turn 正在跑 |
| 是否在等工具/审批 | `Status ∈ {waiting_tool, waiting_approval}` | 跳过并顺延 | `HandleInputWithContext` 在这两个状态明确拒绝起新 turn（`runtime.go:210-231`），原因写在注释里：会叠出无人收的 turn |
| 用户是否刚说过话 | `quiet_after_user_turn` 窗口内 | 顺延 | 别插话 |
| 配额 | pending 数 / 当日次数 / 链深度 | 登记时拒绝（返回错误）；已登记的到期时置 `expired` | 见 §4.6 |

**注意**：不能直接照抄 `wakeIfIdle` 的 switch。它对 `waiting_tool`/`waiting_approval` 是**调 wakeFn** 的（`dispatch.go:280-289`），因为信箱里可能有结果要投递；timer 到点在这两个状态下必须跳过，两者的判据不同。

### 4.3 必须 durable

`wakeup.created` 落 session 事件流。恢复方式与 `pendingTeamResume`（`runtime.go:2065`）同一套 replay，进程重启后重算下次触发时间。

内存堆只是索引：堆丢了从事件流重建，不是丢事实。

### 4.4 唤醒 input 不是 user 消息

这一条容易被当成实现细节，实际是模型行为的分水岭：只要让模型觉得"有人在跟我说话"，它就会按"响应请求"的方式行动——重读需求、重新规划、甚至重派任务。而唤醒的含义是"我自己留的便签，时间到了"。

四个层面都要表达这件事：

| 层 | 位置 | 取值 |
|---|---|---|
| mailbox kind | `mailbox.go:26-27`、`queue_input.go:19` | **不进 mailbox**。`user` kind 专指"用户在会话忙时排队的消息"（`queue_input.go:12-20`），唤醒不写信箱 |
| turn origin | `runtime.go:641-646` 的 `turnOrigin*` | 新增 `turnOriginWakeup = "wakeup"`，不是 `user` |
| 上下文位置 | `ContextBlock.Placement`（`pkg/types/execution.go:80`，取值 `system \| user`） | **`system`**。`resume_mailbox.go:102` 把用户原始任务放 `user` 是对的（那确实是用户说的），唤醒的 note 不是 |
| 协议帧 | `cmd/server/progress.go:26` 的 `progressType*` | `assistant`（模型自己的输出），带 `origin=wakeup` 与 `intermediate` |

`Placement: "system"` 这一条最容易写错。把 note 放进 `user` 位置，模型会读成"用户在催我检查"，于是重新解释需求、重新派活——正是 §六 里"重派"那条失败模式。同理，`finishTurn` 的 payload 里 `origin` 已经单独暴露（`runtime.go:1663` 的 `"origin": result.Turn.Origin`），消费方据此区分，不需要从文案里猜。

这条与 `resume_mailbox.go` 里两个已踩过的坑同源：`taskChainSince`（`:164`）和 `continuedTaskStatement`（`:327`）都是为了解决"下游结果回来，模型当成新请求又重新派一遍"。`ScheduleWakeup` 会以完全相同的方式踩。

### 4.5 结论走 Deliveries，不走 Reply

`finishTurn` 里只要还有 pending Team call，就把这轮标 `Intermediate`（`runtime.go:1633-1634`），随后**清空 `Reply` 与 `FinalReply`**（`:1640` 起）。所以巡检时说的话如果走 Reply，会被引擎自己吞掉。

正确通路是 `Deliveries`（`pkg/types/execution.go:300` 的 `UserDelivery`），它在 turn 事件 payload 里独立字段（`runtime.go` 的 `deliveryPayload`）。现有机制，不用新建。

### 4.6 配额

```yaml
wakeup:
  enabled: true
  min_after: 1m               # 登记下限，防死循环空转
  max_pending_per_session: 8  # 同时挂着的 timer
  max_fires_per_day: 20       # 单会话每天自发唤醒上限
  max_chain: 20               # 单条唤醒链的连续次数上限（§3.7）
  max_age: 7d                 # 单条 timer 绝对时限
  quiet_after_user_turn: 5m   # 用户刚说完话的静默窗口（§4.2 第三道门）
```

超限时工具**返回错误并说明原因**，不静默截断——模型要能据此调整。

配额是资源边界，不是判断，不违背 §2.2 的切分。

---

## 五、外部参考

### 5.1 Claude Code 的 `schedule_wakeup`（形态最接近）

工具：`schedule_wakeup(delay)`，延迟硬夹在 **1 分钟 ~ 1 小时**；`cron_create`/`cron_delete` 走固定节奏。

自定节奏 `/loop` 的工程细节：

| 行为 | 做法 |
|---|---|
| 醒来跑什么 | 同一份 `/loop` input 重跑 |
| 模型忘了排下一次 | 引擎**兜底排一次 20 分钟**；那轮再没排，循环终止 |
| 空转 | 模型报 `noop: true` → 累计 streak，**安静的 tick 从上下文移除**，只留一行状态 |
| 上限 | 自定节奏 loop 7 天自动结束 |
| 子 agent | 可排自己的，只看得到/取消自己的；agent 结束 job 丢弃 |
| 排不了的情况 | 非交互运行（无人接收）→ 直接返回错误 |
| 唤醒呈现 | 通知框成 "automated event"，不冒充用户消息 |

来源：One Code 的 `tasks-and-background-work.md`（Claude Code 行为镜像文档，非官方）：<https://github.com/IsuruMaduranga/one-code/blob/master/docs/guide/tasks-and-background-work.md>

**要抄**：`fallback 一次`、`排不了就报错`、`automated event` 框定、绝对时限。
**不抄**：① 1 小时上限——它的 timer 与 session 同生共死（§5.3），本设计的 timer 落事件流，可以支持到天级；② `noop` 折叠——它是**周期 loop** 的产物，本设计是 one-shot，见 §8.5。

### 5.2 pi-scheduler：把"执行"和"叫醒"拆开

```json
{ "action": "shell", "type": "interval", "schedule": "5m",
  "command": "glab pipeline view 123",
  "wakeOn": "failure",     // always | failure | success | never
  "maxRuns": 10, "scope": "cwd" }
```

`wakeOn: failure` = 动作照跑，结果正常时**根本不叫醒** agent，只存输出。这比引擎算停滞信号干净：引擎不需要判断，只需按模型自己声明的条件决定吵不吵。

来源：<https://pi.dev/packages/@jl1990/pi-scheduler>

### 5.3 durable sleep：timer 不能和 session 同生共死

Temporal/Inngest/Restate 的 `await sleep('24 hours')`：timer 存在引擎侧，进程崩溃、重启、迁移都不影响，醒来是"过了 24 小时"而不是"立刻"。

反面论证（"闹钟被锁在房间里"）：如果下一次唤醒由 session 自己持有，session 卡住/关闭/崩溃时闹钟一起消失，**没有人会发现它没醒**。该文据此主张"排程不能交给 agent"。

来源：<https://ithelp.ithome.com.tw/articles/10421699>

它的两条论据在本设计里都不成立：间隔不做夹取；timer 落事件流，重启可 replay。**所以它反对的不是"模型决定间隔"，而是"闹钟存在模型手里且不可恢复"。**

本设计不需要引入工作流引擎，也不需要 Temporal 的确定性 replay 约束——heron 的 turn 本来就是独立 turn + 事件流。

### 5.4 两个事故：这套能力的失败模式

**事故 A：无界自循环，9.5 天 $285**

agent 开完 draft PR 自己调 `send_later(60min)`，醒来发现"Nothing changed; re-arming silently"，原地重排，89 次。60 分钟间隔正好落在 60 分钟 prompt cache TTL 之外 → 每次冷 cache 重建全部历史（100k → 784k token），cache write 占 $248（87%）。更讽刺的是该 PR 已订阅 push 事件，89 次轮询 100% 冗余；auto-compact 又续了一周。

来源：<https://claudeissues.com/issue/104736-bug-unbounded-recursive-send-later-loop-silently-burned-285-on-an-idle-draft-pr>

**事故 B：20 小时 1297 轮，97.7% token 在重读自己的历史**

508M token 重读 vs 11.9M 实际工作。根因三条叠加：API 无状态 + cache 已冷 + 上下文单调增长 → 每轮成本无界上升。

来源：<https://dev.to/wartzarbee/i-put-an-ai-agent-on-a-timer-overnight-it-burned-136m-tokens-doing-almost-nothing-2ae2>

由此得出两条硬约束：

1. **永远不要 poll 运行时已经会通知你的事。** 本设计里"下游完成"已经有通知路径（`onTaskDone` → `ResumeForToolResult`，`internal/app/runtime.go:412`）。`ScheduleWakeup` 的正当用途只有"看方向对不对"——这个答案没有任何事件会来通知。这句话必须写进工具描述，否则模型一定会拿它 poll 完成。
2. **间隔避开 cache 死区。** 270 秒 TTL 下，5 分钟（300s）是最坏取值：差 30 秒错过缓存，又没换来延迟收益。要么 <270s 吃缓存，要么 ≥1200s 让 miss 摊到真实工作上。

### 5.5 多 agent 监督场景的实践

| 来源 | 做法 | 对本设计的意义 |
|---|---|---|
| Codex subagents | 子 agent prompt 强制打 `=== CHECKPOINT 1/N ===`，orchestrator poll 日志 tail 看进度 | 进度靠被派发方主动打点，不靠引擎猜。属下游可观测性，本文不做（§8.6） |
| OpenClaw 实战 | "永不 kill"：不因"看起来慢"就杀，卡住了通知人决定；每轮输出 MD 报告，下轮 prompt 带上——"报告就是记忆" | 对应 `SharedRecord`；支持"引擎不判定卡死" |
| OpenAI Agents SDK + Modal | `list_subagents` 查状态、`set_status` 让子 agent 一句话上报、子 agent 池加配额 | 同样是查询/上报能力，不在本设计范围（§8.6）；配额思路已用于 §4.6 |

---

## 六、失败模式

| 失败 | 表现 | 防护 |
|---|---|---|
| 静默重排 | 醒来无事，原地再登记，无限循环 | `max_chain`（单链 20 次）+ `max_fires_per_day` 硬停；空转次数由引擎从事件流统计，不靠模型上报（§8.5） |
| 用 ScheduleWakeup 等完成 | 明明有完成通知还在轮询 | 工具描述写死禁止 + 事故 A 的结论 |
| 上下文膨胀 | 每次唤醒都全量重读，成本随轮次上升 | 压缩器已有（`internal/agent/compactor.go`）；`max_chain` 限制总轮次；登记间隔避开 cache 死区（§5.4） |
| facts 太长 | 最近 10 个动作 + 参数摘要把上下文撑大 | 2000 字符上限；超出按 §3.4 的优先级先丢粗粒度项 |
| 压缩吃掉便签 | note 在无人值守链里被压掉 | note 本就持久化在 `WakeupRecord`（§3.2），唤醒时**从记录重注入**，不依赖它留在 transcript；facts 块用 `Placement: system` + `Stability: stable` + `Priority: 90`，与 mailbox 的 `user_task` 同等级 |
| 用户插话 | 主动 turn 撞上用户消息 | 同一把 `resumeMu` + `quiet_after_user_turn` |
| 唤醒到错的 Team | coordinator 拿到自己没派过的便签，重派一遍 | 从 `record.TeamID` 起跑（§4.1） |
| 重启丢闹钟 | session 挂了 timer 一起丢 | 事件流 durable（§4.3） |

---

## 七、落地顺序

**完整方案由五件构成，缺任何一件都不闭环**：

| # | 组件 | 缺了会怎样 |
|---|---|---|
| 1 | `ScheduleWakeup` / `CancelWakeup` 工具 | 模型无法表达"我什么时候回来" |
| 2 | `WakeupScheduler` + durable 事件 | 到点没人叫；或进程重启后叫不醒 |
| 3 | **facts 块（事件流派生）** | **叫醒后只看到"还在跑"，无法判断，工具等于没用** |
| 4 | Deliveries 出站 | 判断出结论也送不到用户 |
| 5 | 配额与四道门 | 与用户消息抢道，或无限自唤醒 |

下面是**落地顺序**，不是分期交付不同能力。P0 单独上线只是半成品（能叫醒，但没有信息），**闭环在第 3 件完成时才成立**。这样切是因为每一步都能独立验证。

### P0：原语 + 调度（验证"能不能被叫醒"）

- `pkg/types/wakeup.go` + 5 个事件（`created` / `fired` / `skipped` / `cancelled` / `expired`）
- `internal/agent/wakeup_tool.go`（`ScheduleWakeup` / `CancelWakeup`）+ `builtinSchemas` 注册
- `internal/runtime/flow/wakeup_scheduler.go`
- `ResumeForWakeup` + `turnOriginWakeup`
- 重启恢复：replay `wakeup.created`

**验收**（每步都要能独立复现）：

1. 新会话第一轮，模型调 `ScheduleWakeup(when="2m", note="检查 X")`，turn 正常结束、会话落 `waiting_input`，事件流有 `wakeup.created`。
2. 2 分钟后出现 `wakeup.fired`，紧跟一条 `flow_turn.started`，其 `origin = "wakeup"`。
3. 该 turn 的上下文里能找到 note 原文，`Placement = system`，且不含"用户说了什么"的痕迹。
4. 等待期间 `kill -9` 进程并重启，`wakeup.created` 能被 replay 出来，到点照常触发（把 `fire_at` 调到 1 分钟内验证）。
5. `CancelWakeup` 后到点不再触发，事件流有 `wakeup.cancelled`。

### P1：facts 块（闭环的另一半）

- 从事件流派生 §3.4 表格里的 9 类事实
- `WakeupRecord.LastSeq` 增量读取，只处理上次唤醒之后的事件
- 超长截断规则

**验收**：唤醒 turn 的上下文里能看到下游最近 10 个工具动作（工具名 + 参数摘要 + 成败）、新增 record 标题、已运行时长；与直接 `grep` 会话 jsonl 的结果一致。

### P2：出站

- 巡检结论走 `Deliveries`
- JSON-RPC session 级常驻订阅（当前 pump 按请求建立，`cmd/server/jsonrpc.go:359` 的 `startTurnPump`；主动 turn 没有请求载体，事件只落 jsonl）

**验收**：用户在没有发消息的情况下收到一条主动进展。

### P3：配额与 `CancelWakeup` 的完整边界

**验收**：超配额时工具返回可读错误并说明原因；链深到顶后该会话不再被唤醒，事件流有 `wakeup.expired`。

---

## 八、不做什么

### 8.1 不给 Team 加 `timer` 类型 Call

`Call` 的语义是"一次执行项"（`pkg/types/call.go:15-20`）。定时是会话级关注点，塞进 Call 会让"Team 一轮 = 一次请求"的语义崩掉，回放、恢复、预算计算都要跟着改。

### 8.2 不做引擎侧停滞/偏离判定

见 §1.4。引擎给事实，模型做决策。

### 8.3 V1 不做周期唤醒（`every`）

周期任务要处理漂移、取消、错过、背压，而且模型不再参与，方向偏了没人纠。替代是"醒来后由模型决定是否再登记"——每次醒来都有模型参与、都有日志、模型能自己停。

一天的活自然变成一条链：

```
07:00 派活 → 登记 12:00
12:00 醒 → 正常 → 再登记 18:00
18:00 醒 → 卡住 → 催一次 → 再登记 20:00
20:00 醒 → 完成 → 不登记（结束）
```

这也顺带回答了"一天跑不完"（§1.3 断点 4）：模型会把长任务自然切成有观察点的片段。

### 8.4 不做分布式定时器

单进程内存索引 + 事件流恢复足够。跨进程需要的是调度服务，不是本次目标。

### 8.5 不做 noop 标记（模型自报"本轮无事"）

Claude Code 的 `/loop` 里有个 `noop: true` 机制：模型声明本轮无事，引擎据此折叠上下文、累计连续次数、触发终止。**本设计不做。**

不做的理由：

| noop 在周期 loop 里的作用 | 本设计为什么不需要 |
|---|---|
| 空转 tick 不进上下文 | 每次唤醒是**独立 turn**，不是同一 loop 反复 tick，不存在 tick 记录堆在同一上下文里 |
| 不产生用户可见消息 | 模型什么都不做就没有 Delivery，这是默认行为，不需要声明 |
| 连续空转后自动终止 | 链本来就会自然终止（不再登记即无下一次）；兜底用 `max_chain` / `max_fires_per_day` / `max_age` |

更根本的一条：**noop 让模型告诉引擎一件引擎自己能观察到的事**（这一轮有没有产出）。这与 §2.2 的分工相反——引擎给事实，模型做判断。真要看空转率，引擎从事件流自己统计即可：`wakeup.fired` 之后没有 Delivery、没有新登记、没有 record，就是空转。

代价：没有"连续空转自动熔断"。接受这个代价，因为空转在 one-shot 模型下是**合法用法**（"还没好，再等等"本来就该重复登记），需要的只是上限，不是模型自证。

### 8.6 不做 peek 查询工具

facts 块已经由引擎在唤醒时**主动注入**（§3.4），模型不需要一个"我自己去查一下下游状态"的工具。

不做的理由：

| 如果做 peek | 后果 |
|---|---|
| 新增 `TeamStatus` 工具 | 多一个 schema 位；模型要在"什么时候查"上做额外决策，而这个决策点已经被 `ScheduleWakeup` 覆盖 |
| 改 `Collect` 支持 `wait=false` | `Collect` 的语义是"阻塞收敛子任务结果"（`collect.go:12-17`），加非阻塞模式会让同一工具承担两种时序语义，调用方难以预期 |
| 需要模型主动调用才有信息 | 而唤醒场景是"到点自动获得信息"，让模型再发一次查询是多余的一跳 |

**不做的代价**：模型不能在两次唤醒之间主动查进度。这个代价可以接受——如果它需要更频繁的信息，正确做法是登记更短的唤醒间隔，而不是加一个查询工具。

---

## 九、工具描述（决定模型用得对不对）

调研下来，这段文字比实现更关键：

```
Schedule a one-off task for yourself at a later time. When it fires you get back your own
note, plus what changed in the meantime.

It fires once. It is not a cron and does not repeat — if you need to look again later,
call it again from the wake-up turn.

Use it ONLY to come back and re-check something nothing will notify you about — for
example whether a long-running task is still on the right track.

Do NOT use it to poll for completion: if you delegated work with Team or Spawn, you WILL
be notified when it finishes. Polling for it burns tokens and is always redundant.

If a check finds nothing that needs your attention, do nothing: do not report, do not
schedule again.
```

---

## 十、一句话总结

**`ScheduleWakeup` 是给模型的闹钟，但闹钟必须带信息。** 引擎保证四件事，缺一件这套能力就不成立：

1. 闹钟不会因为进程重启而消失；
2. 到点不会和用户消息抢道；
3. **醒来时把这段时间发生了什么摆在它面前**——任务状态、最近 10 个动作、新增产出、有没有被推掉过；
4. 它说的话能送到用户面前。

至于什么时候看、看了怎么想、要不要说话，全归模型。

---

## 十一、部署形态：进程生命周期与接入契约

**这一节是前面所有设计的前提。** 没有它，`ScheduleWakeup` 在 CLI 形态下登记得再正确也不会响。

### 11.1 问题：timer 活在进程里

`WakeupScheduler` 是进程内的 goroutine。事件流 durable 解决的是"重启后不丢"，**不解决"谁来重启"**。进程退出后，磁盘上留着 `wakeup.scheduled` 记录，没有任何东西会去触发它。

三种形态的实际情况：

| 形态 | 进程何时退出 | timer 能否触发 |
|---|---|---|
| `--prompt`（一次性） | turn 结束即退出 | **不能**。登记了也永远不会响 |
| TUI / JSON-RPC stdio | 用户关闭 / 父进程退出 | 存活期间能；关闭后不能 |
| `--serve`（HTTP 常驻） | 显式停止 / 崩溃 | **能**。`BuildRuntime` 里已启动调度器 |

### 11.2 业界怎么处理这件事

不是"CLI 直接不支持"，而是**按宿主的有无分了三类**：

| 做法 | 代表 | 机制 | 代价 |
|---|---|---|---|
| **只在会话存活期内有效** | Claude Code `/loop` + `schedule_wakeup` | 进程内的定时器；官方明确 `-p` 模式下"don't fire"，因为进程随 turn 结束退出 | 关掉终端就没了 |
| | Google Antigravity `/schedule` | 官方写明"对话级作用域……需要跨会话长期运行的常驻任务，请改用持久化的 sidecar 或 Automation" | 同上，官方直接建议换形态 |
| | DSH `dsh-delay-tools` | 文档自述"Reminders exist only in process memory. Restarting the DSH host also cancels all pending reminders." | 无持久化 |
| **外部 cron / systemd 起新进程** | Agent Defer | "let cron wake a runner"、"No daemon. No database. No state held in memory. If the machine reboots, cron picks up where it left off." | 每次新进程，system prompt 与工具 schema 重新计费；prompt cache 失效 |
| | Claude Code / Codex / Gemini 的无人值守用法 | `claude -p` / `codex exec` / `gemini -p` 由 cron 或 systemd timer 触发，业内视为官方特性而非变通 | 同上；且不同 tick 之间不共享会话 |
| **常驻宿主进程** | OpenClaw Gateway | "Cron 在闸道程序内部执行，而非模型内部。闸道必须保持运行，排程才会触发。" 状态存 SQLite，重启不丢 | 需要一个常驻进程 |
| | Ability/Trinity、Azure Foundry reminder | 云端 hosted agent，服务端本来常驻，timer 天然有效 | 托管依赖 |
| **折中：把 CLI 进程挂住** | cron-mcp | MCP server 与 CLI 同进程；cron 到点时作为 `cron_wait_next` 阻塞工具调用的返回值交回，复用同一会话与 prompt cache | 进程必须一直开着，等于自己实现常驻 |

**结论**：没有哪个 CLI 靠"自己的 daemon"解决跨进程唤醒——要么承认只在会话存活期内有效，要么把进程常驻起来（Gateway / 云端 / 桌面应用），要么交给外部调度器每次起新进程。

heron 的约束是"不依赖其他软件"，所以第三条（外部 cron）排除；而用户不会一直开着终端，第一条也不成立。**只剩常驻一条路。**

### 11.3 目标架构

```
┌──────────────────────────────────────────────────────┐
│ heron-ai --serve（常驻，一个进程管所有会话）           │
│  Runtime + WakeupScheduler + SessionWriter            │
│                                                       │
│  POST /api/run                新建 flow session       │
│  POST /api/sessions/turn      续会话（session_id）    │
│  GET  /api/sessions/stream    事件流（新增 follow=1）  │
│  GET  /api/health             健康检查（新增）         │
└───────────────────────┬──────────────────────────────┘
                        │ HTTP + SSE（长连，Last-Event-ID 续传）
┌───────────────────────┴──────────────────────────────┐
│ heron-connect（不再起子进程，纯客户端）                │
│  IM 会话 ↔ flow_session_id（持久化映射）              │
│  origin=wakeup 的完成事件 → 主动发到 IM               │
└──────────────────────────────────────────────────────┘
```

关键点：**wakeup 的产出不需要新通道**。它本来就写在 session 事件流里（`finishTurn` 的 payload 带 `origin`、`deliveries`）。只要客户端的长连不随 turn 关闭，主动消息自然流出去。

### 11.4 heron-ai 侧改动

| # | 改动 | 位置 | 说明 |
|---|---|---|---|
| 1 | `/api/sessions/stream` 支持 `follow=1` | `internal/view/handler.go:246` | 现在在 `EventFlowTurnCompleted` 时主动 return。带 `follow=1` 时不因 turn 结束而关闭，只在客户端断开时退出；不带参数行为完全不变 |
| 2 | `/api/health` | `internal/view/handler.go` | 返回 `{ready, pending_wakeups, sessions_loaded}`，供接入方探测与运维排查 |
| 3 | 只在 serve 注册该工具 | `app.WithWakeupTools` + `BuildRuntime` | 其它形态（`--prompt` / TUI / `--json-rpc`）**不注册** `ScheduleWakeup` / `CancelWakeup`。模型看不到，就不会去安排一个送不到人的检查；这比"注册了再拒绝"干净 |
| 4 | wakeup turn 的 reply 转 delivery | `internal/runtime/flow/runtime.go` 的 `finishTurn` | 只要下游还在跑，wakeup turn 就被标 `intermediate` 并清空 `reply`（`runtime.go:1633-1640`）。对 `origin=wakeup` 的 intermediate turn，应把 reply 转成 delivery 而不是丢弃（设计见 §4.5） |

第 1 项是 P2 出站的全部内容；第 3、4 项是让能力自洽。第 3 项刻意做成"不注册"而不是"注册后再拒绝"：一个模型看不见的工具不会被误用，也不需要额外的运行时解释。

**阶段 1 实现状态（2026-10-29）：四项全部落地。**

- `/api/sessions/stream?follow=1`：`EventHandler.HandleStream` 增参，不带参数行为不变（`internal/view/handler.go`）
- `/api/health`：新增 `Handler.HandleHealth` + `Runtime.WakeupStats`，路由挂在 `cmd/server/main.go`
- 只在 serve 注册：`app.WithWakeupTools(true)` 由 `startServer` 传入，其余路径不传即不注册（`TestWakeupToolsAreOnlyRegisteredInServeMode` 用 `ToolSchema` 钉住可见性）
- reply 转 delivery：`finishTurn` 对 `origin=wakeup` 的 intermediate turn 追加 `UserDelivery`

**实现时补的一条**：调度器改为**按需启动**（`StartWakeupSchedulerAsync` + `sync.Once`）。原来在 `BuildRuntime` 里无条件 `go RunWakeupScheduler`，导致没有定时器的进程也挂一个空转 goroutine；测试里它跨用例存活，与 `logging.SetDefault` 竞争（`-race` 可复现）。现在只有"恢复出待触发记录"或"发生一次登记"才启动。

### 11.5 heron-connect 接入契约

| # | 改动 | 现有实现 | 改成 |
|---|---|---|---|
| 1 | 适配器 | `exec.CommandContext(... "--json-rpc")`（`agent/heron/session.go:202`） | HTTP 客户端；`StartSession` 变成"探测 `/api/health` + 持有 `session_id`" |
| 2 | 发消息 | 写 stdout（JSON-RPC `turn`） | `POST /api/sessions/turn` |
| 3 | 收事件 | 读 stdout 行 | 读 SSE；映射到现有 `EventText` / `EventToolResult` / `EventResult` |
| 4 | 关闭 | 杀子进程 | 关 SSE 连接，**不杀引擎** |

会话映射从"子进程存活"改为持久化：IM 会话（`interactiveKey`）↔ `flow_session_id`，存在 connect 的 `Session.AgentSessionID` 字段里（已存在）。

断线重连带 `Last-Event-ID`，补齐错过的事件——**这正好解决"connect 重启期间的主动消息丢失"**。

主动消息识别沿用现有 `runUnsolicitedReader` 路径（`core/engine_turn.go`），只是数据源从 stdout 换成 SSE。

### 11.6 协议

```http
GET /api/sessions/stream?session_id=fs_abc&follow=1
Last-Event-ID: 122

id: 123
data: {"type":"flow_turn.waiting_input","seq":123,"origin":"wakeup",
       "reply":"下游已跑 5 小时，无新增产出，已催一次",
       "deliveries":[{"message_id":"...","team_id":"default","text":"..."}],
       "intermediate":false}
```

判据：`type=flow_turn.waiting_input` + `origin=wakeup` + `intermediate != true` + `deliveries` 非空。

### 11.7 四个必须正视的风险

| 风险 | 内容 | 应对 |
|---|---|---|
| **单点** | 一个进程管所有会话，挂了全停 | 需要进程守护。systemd `Restart=always` 算不算"依赖其他软件"要明确；彻底自足的话 heron 自己做 `--supervise`（父进程 fork + 崩溃重启） |
| ~~**全局锁**~~ **已修复** | `resumeMu` 原是 Runtime 级，6 个 resume 入口（工具结果 / mailbox / 审批 / wakeup / 排队输入）会持锁跑完整个 turn，跨会话串行 | 改为**按会话锁**（`internal/runtime/flow/resume_lock.go`，`sync.Map`）。正常对话本来就不拿这把锁，只影响 resume 路径；`TestResumeLockIsPerSession` 钉住"不同会话不互斥、同会话串行" |
| **内存** | transcript 缓存、eventPump、wakeup index 全在一个进程 | 设会话数上限或做 LRU |
| **升级** | 重启常驻进程 = 所有会话短暂中断，进行中的 turn 变 interrupted | 靠 `Recover` 恢复；升级窗口避开活跃 turn |

### 11.8 落地顺序

| 阶段 | 内容 | 可独立验证 |
|---|---|---|
| 1 | heron 侧 §11.4 四项 | curl 起常驻 + 登记 wakeup，SSE 里能看到主动消息 |
| 2 | connect 侧 §11.5 的 1–4（只跑对话，不做主动消息） | 一个 IM 会话正常对话 |
| 3 | connect 侧长连 + 主动消息转发 | 不发消息也能收到进展 |
| 4 | 并发改造（`resumeMu` 按会话化）或会话数上限 | 多会话并发不互相阻塞 |

阶段 1 不依赖 connect，做完就能端到端验证。

### 11.9 进程生命周期与退出保护

serve 进程必须能"干净地死"。一个退不掉的进程比一个起不来的进程更难排查：端口被占，下一次启动直接失败，而错误只说"address already in use"。

三个停止入口收敛到一条退出路径（`cmd/server/serve_lifecycle.go`）：

| 触发 | 行为 |
|---|---|
| SIGTERM / SIGINT | 停止接收新请求 → `server.Shutdown`（上限 10s）→ 关闭 runtime → exit 0 |
| 父进程消失（`--exit-with-parent`） | 同上。ppid 变化即触发，2 秒轮询 |
| 监听失败（端口被占） | 立即 exit 1，stderr 指明"已有实例在监听" |

四条实现约束：

1. **超时强制退出**：10 秒没 drain 完就走 `os.Exit`。宁可断一个 SSE 连接，也不让端口卡住——一个断掉的客户端会重连（`Last-Event-ID`），一个占着端口的僵尸进程不会自己消失。
2. **端口释放由 `Serve` 返回保证**，不是靠杀进程。`TestServeLifecycleShutsDownAndExits` 在退出后重新绑定同一端口，把这一点钉死。
3. **`--exit-with-parent` 默认关闭**：systemd 起的进程 ppid 是 1，本来就不存在"父进程会消失"，不该为它付轮询成本。
4. **父进程检测用 ppid 轮询，不用 `PR_SET_PDEATHSIG`**：后者只能在 `exec` 前武装，子进程自己做不到。ppid 变化是可观测的等价信号，延迟 2 秒在这个路径上无所谓。

**connect 侧对应的责任**：正常退出时向 `/api/health` 拿到的实例发 SIGTERM 并等待；被 SIGKILL 时无法执行任何清理，由 `--exit-with-parent` 兜底。两侧都要有，缺一个就会留下孤儿进程。

### 11.10 真机联动验证记录（2026-10-30）

阶段 1 完成后做了一次真实链路验证：`heron-ai --serve`（源码构建）+ 一个 OpenAI 兼容的 mock 端点（让模型真的调用 `ScheduleWakeup`）+ heron-connect 的 `httpSession` 订阅同一个 session。

**跑通的链路**：

```
POST /api/run                                  → turn(origin=user)，模型调用 ScheduleWakeup(when="70s")
wakeup.created                                  → 会话落 waiting_input，pending_wakeups=1
70s 后到点                                     → wakeup.skipped，reason 指向 quiet 窗口，顺延 5 分钟
5 分钟后重试                                   → wakeup.fired
flow_turn.started                               → origin=wakeup，从登记者 Team 起跑
flow_turn.waiting_input                         → reply = 巡检结论
/api/stream?follow=1                            → connect 收到并投递（68.8s 后，误差 1s）
```

**这一步抓到两个只在真机上暴露的问题**（都是客户端侧，单测用自造 fixture 时看不出来）：

| 问题 | 后果 | 修法 |
|---|---|---|
| 订阅路径写成 `/api/sessions/stream` | 真实引擎注册的是 `/api/stream`，连上去 404，主动消息永远收不到 | 改路径，并加 `TestHTTPSessionSubscribesToTheEngineStreamPath` 钉住 |
| 从 event payload 读 `flow_turn_id` | 真实帧是"扁平事件头 + 嵌套 payload"，`flow_turn_id` 在**头**上；读 payload 得到空串，去重与日志同时失效 | 头/载荷分开解，fixture 改成真实形状（`sessionEventWire`） |

**顺带修的一处**：`wakeup.skipped` 的 `reason` 原来固定写 "session was busy"，四道门共用一句话。现在每道门报自己的原因（`the session was waiting_tool` / `teams were still running: X` / `the user spoke 70s ago, inside the 5m quiet window`）——模型下次醒来才能知道"我为什么被推掉"。

**退出保护也验了**：`SIGTERM` → 进程 exit 0、端口立即释放、可重新 bind；这正是"防止无法重启"要的效果。

**没验的**：真实 IM 平台投递、多会话并发、`serve_manage` 起停真实二进制的完整回路（只验了 health 探测与 SIGTERM 语义）。
