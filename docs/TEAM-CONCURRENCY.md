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
- `per_task`：每个任务一个独立实例。**可并行**，受 `max_active_team_sessions`（默认 8）限制。

`entry` 不能用 `per_task`：它没有派发者，没人能给它建实例。引擎在加载配置时就拒绝
（`pkg/types/domain.go:296-303`）。

## 3. 并行不是靠"类型"，靠 busy 标记的 key

派发前的互斥检查只有一行：

```go
// internal/runtime/flow/team_call.go:114
if !r.tryMarkTeamBusySeq(session.ID, spec.TeamID, seq) {
    return types.TeamCallOutcome{
        Status: "failed",
        Error:  fmt.Sprintf("team %q is already running in this session", spec.TeamID),
    }, nil
}
```

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

执行型团队按"是否会被同时派多个任务"决定 lifecycle：

- 会被并行派发 → `per_task`，同时要求调用方带 `task_key`
- 只在会话级协调 → 保持 `resident`

判定依据不是"它属于哪类角色"，而是"同一个 Flow session 里会不会同时有两个它的任务"。

### 引擎层

**统一撞车策略**：resident 撞车改为排队，等当前轮结束后接上，与 `activate` 路由一致。
`Team` 工具的调用方因此会得到一个"稍后完成"的结果，而不是一个它必须自己重试的错误。

**补上成功保护**：`pendingFailure` 分支加与 `failedTeamCalls` 相同的判断 —— 本回合有派发
成功时，把失败降级为可见记录，不判回合失败。

### 排队条件

把 drain 的触发条件从"上一个 turn 结束"改成"目标 team 空闲"，或者让 drain 在撞车时把
消息退回队列。前者需要引擎知道 team 级空闲；后者改动更小：撞车时不留失败，留 pending。

## 7. 不该改的部分

**入口团队保持 resident**。它承载会话级的协调状态，分裂实例会丢上下文。

**协调团队串行处理用户任务是合理的**。它的 state 与 records 是会话级的，同一时刻只应该
有一个"当前进度"。真正需要并行的是它派下去的执行团队，不是它自己。

所以正确的期待是：**用户发两个任务 → 第二个排队等待第一个真正做完**，而不是"两个协调
团队实例同时跑"。这需要的是排队语义的修正，不是让协调团队并发。
