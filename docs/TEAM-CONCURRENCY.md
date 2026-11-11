# Team 并发：为什么两个任务会互相挡住

> 状态：现象与机制已核实（2026-11-10），方案待定
>
> 范围：`internal/runtime/flow` 的 Team 派发与 lifecycle。不涉及 Agent 内部的模型循环。

## 1. 现象

用户连续发两条消息，分别问两个不同仓库的事情。第二条消息没有报错，但整轮以失败结束：

```
The request could not be completed:
dispatched team "coordinator" could not complete the task:
coordinator could not complete the task: team "coordinator" is already running in this session
```

而实际上，两条问题后来都得到了回答 —— 模型自己重试了，重试成功。

这就是最让人困惑的地方：**任务做完了，界面说失败**。

## 2. 三个容易混在一起的概念

问题看起来复杂，是因为三件事都叫"team"，但层级不同：

| 概念 | 是什么 | 谁决定数量 |
|---|---|---|
| team **定义** | `.agents/teams/<id>.yml` 里的一份配置 | 静态，一个 id 一份 |
| team **会话**（TeamSession） | 某个 Flow session 里，这个 team 的运行实例 | 由 lifecycle 决定 |
| team **执行轮**（TeamTurn） | 实例每被叫一次，跑一轮 | 每轮一个，串行或并行由 busy 标记决定 |

`lifecycle` 就是"定义 → 会话"这一步的策略：

```yaml
# .agents/flows/<flow>.yml
entry: entry-team          # 入口团队，强制 resident
teams:
  - team: coordinator
    # 未写 lifecycle → resident
  - team: worker
    lifecycle: per_task    # 每个任务一个实例
```

- `resident`（默认）：一个共享会话。**串行** —— 同一时刻只跑一轮。
- `per_task`：每个任务一个独立实例。**可并行**，受 `max_active_team_sessions` 限制
  （默认 8；本仓库设为 32，见 §6）。

`entry` 不能用 `per_task`：它没有派发者，没人能给它建实例。引擎在加载配置时就拒绝
（`pkg/types/domain.go:296-303`）。

## 3. 并行不是靠"类型"，靠 busy 标记的 key

派发时不再是「检查失败就返回」，而是取执行槽、取不到就等：

```go
// internal/runtime/flow/team_call.go:111
if err := r.acquireTeamSlot(ctx, session.ID, spec.TeamID, seq); err != nil {
    return types.TeamCallOutcome{
        Status: "failed",
        Error:  err.Error(),
    }, nil
}
```

`acquireTeamSlot` 在槽被占时轮询等待（上限 30 分钟），只有 ctx 取消才返回错误。
`team "..." is already running in this session` 这条直接失败路径已经不存在 ——
那是撞车修复前的写法，见 §6「撞车改为排队」。

而 `seq` 从哪来，决定了两个 call 会不会撞：

```go
// internal/runtime/flow/team_call.go:49
seq := 0                       // resident：永远是 0
if lifecycle.IsPerTask() {
    seq = plan.sessionSeq      // per_task：每个任务一个序号
}
```

busy 标记的 key 是 `{sessionID, teamID, seq}`（`dispatch.go:168`）。于是：

| lifecycle | key | 同一 team 的两个 call |
|---|---|---|
| resident | `{会话, team, 0}` | **撞**，第二个被拒 |
| per_task | `{会话, team, 任务序号}` | 不撞，各自一个实例 |

所以"能不能并发"不是团队的类型属性，而是**配置项 + 派发时是否带了 task_key** 的结果。

## 4. 一次真实的撞车

下面是改配置之前的现场（协调团队当时是 resident）。按 §6 把协调团队也改成 `per_task`
之后，这个场景不再发生 —— 保留它是因为撞车时的**后果**（回合判失败、任务却做完了）
仍然成立，而那条路径没有随配置消失。

假设入口团队已经把两个任务都交给同一个协调团队，后者是 resident：

```
t+0s    turn-1 开始（任务 A）
t+20s   turn-1 把任务 A 派给 coordinator（异步，占住 team）
t+22s   turn-1 结束 —— 不等 coordinator 跑完，只记录"派出去了"
t+22s   队列里的任务 B 被 drain，turn-2 开始
t+32s   turn-2 把任务 B 派给 coordinator → 撞车
t+144s  coordinator 跑完任务 A
t+153s  重试任务 B → 成功
t+164s  turn-2 仍然以 failed 收尾
```

三个时点错位，才是问题的全貌：

1. **turn-1 结束时 coordinator 还没跑完** —— 派发是异步的，turn 不等待
2. **turn-2 一开就跑** —— 队列的 drain 条件是"上一个 turn 结束"，不是"目标 team 空闲"
3. **撞车发生在 turn-2 里** —— 而失败已经写进了 turn-2 的终态

## 5. 由此暴露的四个问题

### ① 配置承诺与配置事实不一致

协调团队的职责说明常常写着"可以并行派发给多个执行团队"。但执行团队里只有显式配了
`per_task` 的才真的能并行。**并行派同一个 resident team 两次，第二次必然被拒。**

本仓库原先只有 `dev` 一个 `per_task`，其余 8 个执行团队和协调团队都是 resident ——
职责说明承诺的并行能力只兑现了 1/9。现已按 §6 改为除入口外全部 `per_task`。

### ② 同一件事有两条路径、两种策略

| 派发方式 | 撞车时 |
|---|---|
| flow 的 `activate` 路由 | 排队：消息留 pending，等目标 team 空闲再跑 |
| Agent 的 `Team` 工具（同步 `RunTeam`） | 立即失败 |

前者是"排队等"，后者是"报错回"。模型看到的是后者，于是它只能自己重试。

### ③ 排队的触发条件选错了对象

`queueUserInput` 保证消息不丢，但 drain 的时机是"上一个 FlowTurn 结束"。而 FlowTurn
结束不代表它派出去的后台工作结束 —— 恰恰相反，派发类 turn 会**故意**提前结束。

于是"用户连续发两条"这件事，几乎必然触发撞车：第二条一定在第一条派出的工作还没回来时开始。

### ④ 一次失败把整轮判死，即使后来成功了

```go
// internal/runtime/flow/runtime.go:1346
if pendingFailure != nil && !hasWaitApproval && !hasWaitTool {
    result.Reply = failureReply(pendingFailure)
    return r.finishTurn(..., types.SessionFailed, pendingFailure)
}
```

紧邻的另一个分支做了保护 —— 只有在"本回合没有任何派发成功"时才判失败：

```go
// runtime.go:1366
// Only when nothing at all succeeded: a retry that worked is a success
// with a failed attempt behind it.
if reasons, noneSucceeded := failedTeamCalls(result.TeamResults); noneSucceeded && len(reasons) > 0 {
```

`pendingFailure` 分支没有这个判断。所以「第一次派发失败、模型重试成功、用户的问题已经答上」
的情况下，回合依旧显示失败。

## 6. 建议

### 配置层

判定依据不是"它属于哪类角色"，而是"同一个 Flow session 里会不会同时有两个它的任务"。

本仓库的实际选择是**除入口外全部 `per_task`**：

| team | lifecycle | 理由 |
|---|---|---|
| frontdesk | resident | 入口，引擎强制；且它是唯一用户出口，必须单实例 |
| dev-lead 及其余 9 个 | per_task | 用户可能连续发多个任务，任何一个 team 成为串行点都会把后面的任务堵住 |

两个前提必须同时满足，否则引擎拒绝加载：

- 入口不能 `per_task` —— 没有派发者，没人给它建实例
- 每个 `per_task` team 必须 `inputs.user_message: true` —— 否则派发的任务文本会被丢掉

`per_task` 的实例不会自动关闭，未关闭的实例计入 `runtime.max_active_team_sessions`。
默认值是 8，而本配置有 10 个 `per_task` team，一次完整任务链路就可能超限
（`session already has 8 active task instances`）。因此 `.agents/settings.json` 把它设为 32。

代价要说清：协调团队也 `per_task` 之后，**两个任务各有一个协调实例，进度不再汇入同一处**。
并发度因此下移到了 frontdesk —— 它仍是 resident，两个协调实例的结果都要回到它。
它是新的串行点，只是它只做转述，不参与编排，所以堵住的时长远小于协调团队。

### 引擎层

**撞车改为排队**（已实现）：`acquireTeamSlot` 拿不到执行槽时等待，而不是返回
`team X is already running`。上限 30 分钟，调用方取消时立即退出。调用方本来都是异步的
（durable task 或 mailbox 唤醒），等待不挡住任何人。

**排队槽位的 key 按 lifecycle 分槽**（已实现）：这是这次撞车的直接原因。执行队列原本用
`(会话, team, task_key)` 当槽位 key —— 但 `task_key` 的有无不能说明目标是哪种 lifecycle。
模型给一个 resident team 写了两个不同的 `task_key`（当时的文档说"只有 per_task 才需要"，
它写了也无害），队列把这当成两个实例、两个槽位，两次派发同时放行；而它们背后只有一个
执行槽，第二个当场被拒。现在 `Team` 工具把目标的 lifecycle 写进任务参数，队列只在
`per_task` 时才用 `task_key` 分槽，未知时按 resident 串行 —— 宁可慢，不会撞。

**补上成功保护**（待做）：`pendingFailure` 分支加与 `failedTeamCalls` 相同的判断 ——
本回合有派发成功时，把失败降级为可见记录，不判回合失败。撞车不再是失败来源，但模型调用
失败、工具失败仍会走到这里。

### 排队条件

drain 的触发条件是"上一个 turn 结束"，不是"目标 team 空闲"。撞车改成排队之后，这条不再
致命：任务会在执行槽上等，而不是在 turn 上报错。但用户仍然要等一个 turn 结束才看到
"开始排队"的反馈。

## 7. 已经改掉的与仍然要改的

**已经改掉的三件：**

1. **除入口外全部 `per_task`**（§6 配置层）—— 让同一 team 的不同任务各拿实例。
2. **撞车改为排队**（§6 引擎层）—— resident 的第二个任务等第一轮跑完，不再失败。
3. **排队槽位按 lifecycle 分槽**（§6 引擎层）—— 修掉"两个 task_key 骗过队列、挤进一个
   执行槽"这个直接原因。

**入口团队必须保持 resident**。引擎在加载配置时就拒绝入口用 `per_task`，理由是它没有派发者。

**仍然要改的一件**：`pendingFailure` 一次失败判死整轮。撞车不再产生失败，但模型调用失败、
工具失败仍走这条路径，而相邻分支已有"有派发成功就不算失败"的保护。这是独立问题。

## 8. 三种派发落点的最终语义

| 派发情形 | 现在 |
|---|---|
| 目标是 resident，它正在跑 | **排队**，等当前轮结束再执行 |
| 目标是 `per_task`，`task_key` 与在跑的不同 | **新实例**，立即并发 |
| 目标是 `per_task`，`task_key` 与在跑的相同 | **排队**，等同一实例跑完 |
| 排队超过 30 分钟，或调用方取消 | 返回失败，错误写明是等待超时/取消，不是冲突 |
