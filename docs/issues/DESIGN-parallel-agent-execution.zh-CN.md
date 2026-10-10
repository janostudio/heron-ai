# 设计探讨：多 agent 并行执行（想要的模型 vs 现状）

> 状态：**P0 已实现**（2026-10-09 分析，2026-10-10 修订 + P0 落地）
> 性质：记录"用户想要的并发模型"与"引擎现状"的差异；§一~§六 为分析，§七~§九 含改造点/P0 实现记录
> 关联：`internal/runtime/flow/runtime.go`、`internal/runtime/flow/mailbox.go`、`internal/runtime/flow/dispatch.go`、`internal/runtime/flow/resume_mailbox.go`、`pkg/types/mailbox.go`、`internal/app/runtime.go`
> **要点（见 §五）**：team/agent **各自已有独立、持久、可 replay 的执行状态**，
> 空闲判据 = `Status != running` 已可直接成立；缺的是**按它调度的调度器**。
>
> **P0 落地状态（2026-10-10）**：mailbox 调度已实现并端到端验证——
> coordinator 派活后**立即返回 `waiting_input`**，后台执行被派发的 team，
> 完成后投 result 消息到 coordinator inbox 并**唤醒**它开新 turn 汇总。
> 实现见 §九。**context 归属（话题隔离）仍按 §1.5 搁置**（P0 不做）。


## 一、想要的设计（用户意图）

### 1.1 一句话：把"并发"从会话层下沉到 agent 层

CLI 现在的并发方式是**开多个会话**——一件任务一个窗口，天然隔离，但彼此不知道对方在干嘛，
要用户自己切窗口、自己拼结果。用户想要的是**在同一个会话里，由多 agent 协作实现并发**：
用户只面对一个入口，背后多 agent 并行推进多件事，结果由入口 agent 汇总回来。

| | CLI 多会话（现状做法） | 想要：单会话多 agent |
|---|---|---|
| 并发单位 | 会话（进程级） | **agent**（会话内） |
| 谁协调 | 用户自己切窗口 | **default agent 自动协调** |
| 结果汇总 | 用户自己拼 | default agent 汇总后回给用户 |
| 状态归属 | 各会话各写一份 | **一份会话账本，多 agent 并行** |

即：把 CLI 那种**物理并发**（多进程）变成引擎里的**逻辑并发**（多 agent），
用户不再需要自己开好几个窗口。

### 1.2 目标图景

```
用户输入 query A
      │
      ▼
default agent（协调者 / 用户与引擎的唯一入口）
      │  派活（投消息）
      ├──────────────▶ other agent X   ← 并行执行 A
      │                      │
      │   （default agent 派完即"空闲"，
用户输入 query B ──▶  可以立刻接 B 并直接回复你 ）   ← 不卡住
      │                      │
      ◀──────────────────────┘  X 干完 A，把结果"投消息"给 default agent
      │
default agent 汇总 ──▶ 汇报 A 的结果给用户
```

核心诉求：

1. **default agent 派活后立刻空闲、可接新任务**。它派出 other team/agent 后**不应被阻塞**，
   用户可以马上再输入下一件事。
2. **each team/agent 独立**。派出的 other team/agent 与 default agent **并行**，
   而不是"等它跑完 default 才能动"。
3. **结果异步回流**。other team/agent 完成后把结果**作为消息投给** default agent，
   default agent 再决策、再汇报给用户。
4. **并行无上限（或高上限）**。default agent 可以派发多个执行体同时跑。

一句话：这是 **actor / mailbox 式并发**——每个 agent 独立、靠消息驱动，
**没有"一个会话串行推进"的假设**。

### 1.3 default agent 的定位：理念上"只协调不执行"，引擎层无特权

**必须把"引擎层规则"和"设计理念"分开说**：

| 视角 | default agent 是什么 |
|---|---|
| **引擎层** | 就是**一个普通的 team/agent**，没有任何调度特权。引擎只认一条判据——**它在不在执行**；不在执行即空闲、即可接消息。引擎**不关心**它是不是"协调者"。 |
| **设计理念层** | 约定它**只协调、不执行**（派活 + 汇总 + 回复用户）。其他 agent 是"助理/秘书"。这是**最佳实践/默认用法**，不是引擎强制。 |

换句话说：引擎**允许** default agent 自己执行，但**期望**它不执行——这个"允许但不期望"
的分层保证了引擎的通用性（不因角色特殊化），同时把方法论收敛在约定里。

### 1.4 统一抽象：每个 team/agent 都有一根消息队列（mailbox）

"空闲即可接活"的**执行机制就是消息队列**：

```
每个 team / agent  ←── 拥有一个 mailbox（消息队列）
     ▲
     │ 投递者可以是：
     │   · 用户（对 default agent / 入口 agent）
     │   · 其他 agent（派活 / 汇报结果）
```

- **消息来源**：用户 / 其他 agent。
- **谁空闲谁能取**：agent 不执行时，从自己 mailbox 取下一条消息开始执行；执行中则消息排队等候。
- **派活 = 投消息**（不是同步调用等返回值）；**汇报 = 投消息**（不是 return）。
- 于是 §三 的冲突 1（派活后卡住）、冲突 3（返回值 vs 消息）**被一次性统一**——全是消息。

**"唤醒"不是特殊机制**：other agent 完成时往 default agent 的 mailbox 投一条消息，
default agent **从空闲（无消息可处理）自然转为执行（有消息可处理）**——这就是唤醒。

### 1.5 三件事必须拆开：mailbox / context 归属 / thread_id（**关键**）

用户提出一个尖锐问题：**"我和 default agent 同时聊 2~N 个话题会不会混乱？"**
这看似是"并发"问题，其实**是另一个正交的问题**，必须先把三件事解耦，否则复杂度会被
错误地叠加。

```
① mailbox（调度机制）   ── 派活 / 汇报 / 唤醒。跟"话题"无关，只负责消息投递
② context 归属（话题）   ── 谁的历史和谁的历史串。跟"并发"无关，**串行下也存在**
③ thread_id（IM 模型）   ── 一种"归属信号"的载体。**单窗口下不可用**（见 1.5.2）
```

**关键结论**：**① 和 ② 不强依赖**。

- 完全可以**只做 ①（mailbox）**，历史仍用一个（话题串了就串了）——mailbox 不关心历史分桶。
- 也可以只做 ②（话题隔离）而**不动调度**——串行下多话题一样会串。
- 因此"多话题混乱"**不是 mailbox 的锅**，`mailbox ≠ 话题隔离`。

#### 1.5.1 P0 切分：把 ② 整体搁置，只做 ①

由于 ① 与 ② 正交，本次改造可以**分级落地**，把最容易翻车的 ② 砍出去：

| 阶段 | 做什么 | 话题能力 | 依赖 |
|---|---|---|---|
| **P0（本次目标）** | default agent 派活后**不卡住** + 结果回流（= ① mailbox + §七 C2/C3/C4） | 用户**自己守规矩**（固定单话题） | 只需 ① |
| **P1** | mailbox 消息带**归属信号**，历史按信号分桶 | 同一 default agent 并行多话题 | 需要 ② |
| **P2** | 共享记忆 / 黑板，跨话题传递全局事实 | 话题间"记得彼此" | 需要 ② + 共享层 |

**P0 是自洽的**：单话题时它就是"派活后不卡住"，**没有隔离问题**；用户"固定话题就行"
正是 P0 的用法。**② 的复杂度与 P0 无强依赖，可以在 P0 之后单独谈。**

#### 1.5.2 为什么 thread_id（IM 模型）**不可用**

IM（Slack / Discord / OpenAI Assistants）处理多话题靠 **thread**，但**它的前提是有多窗口 /
显式 thread 归属**：

- IM 天然**多窗口**：用户在**哪个窗口**打字，就是显式告诉系统"这是哪个话题"——
  **UI 承载了话题归属信号**。
- 同一 assistant 可并发跑多个 thread，正是 OpenAI Assistants API 的一等设计
  （`Thread` → `Run` 附着在 thread 上）。

**但 heron 这里是"单窗口"**：**用户都从同一个窗口输入，没有 thread_id**。
即——

> **话题归属没有外部信号**（不像 IM 有 window / thread_id），
> 若要求隔离，归属只能靠**模型自己从内容里推**。

这就是用户说的"**本质还是和 IM 不一样，需要模型自己管理上下文**"。因此 **thread_id 模型
不能照搬**。

#### 1.5.3 单窗口下"模型自己管理多话题"≈伪解

既然没有显式信号，②（context 归属）的候选做法只剩两种：

| 做法 | 机制 | 现实 |
|---|---|---|
| **模型隐式分段**（topic segmentation / dialogue state tracking） | 模型每轮自己判断"这句接着哪个话题" | **不可靠**：指代消解是老大难（"把那个改成红色"——"那个"指哪话题？），猜错即串话。学术界有，产品里基本没做好。 |
| **用户显式切换**（命令 / 前缀 / 话术） | 用户用语法标记话题边界：`/new`、`[2] …`、"对了，另一件事…" | **可行**：归属由**用户显式动作**保证，引擎只维护"话题 → 历史快照"表，切回即恢复。|

**没有第三条路**：单窗口下让模型自由管理多话题，会得到不可预期的串话。
真要多话题，必须让**话题切换成为用户的显式动作**（斜杠命令 / 序号前缀 / "换个话题"话术）。
注意：这**不是新窗口**，仍是同一窗口，只是用户多一个**明确的**信号，而非让模型猜。

#### 1.5.4 小结

- **mailbox（①）与 context 归属（②）正交**：mailbox ≠ 话题隔离，二者可分别落地。
- **本次（P0）只做 ①**，② 整体搁置为"用户自律（固定单话题）"——所以 P0 复杂度**回到中等**，
  用户担心的"复杂度很高"**很大一部分来自 ②，而 ② 可以砍开**。
- **thread_id（③）不可用**：单窗口无显式信号，IM 模型不适用。

## 二、现状（引擎当前模型）

### 2.1 三层编排 + turn 独占

引擎是 Flow → Team → Agent（call）三层。**一个 FlowSession 同一时刻只推进一个 FlowTurn**，
且这个 turn **独占整个会话**直到结束：

- `flow/runtime.go:549`：turn 一开始就 `session.Status = SessionRunning`。
- `flow/runtime.go:630`：`for len(queue) > 0` —— turn 内所有 activation（team）在**同一个队列**
  里顺序消费，直到队列空才 `finishTurn`（`:860`/`:862`/`:870`）。
- 即：default agent（coordinator）派出 other team 后，**它的这个 FlowTurn 不会结束**，
  一直挂在队列循环里等 other team 回来。**它没有"空闲"这个状态**——"空闲"= turn 结束 =
  `waiting_input`。

### 2.2 turn 内确实是并行的（但收口靠同步等待）

- **同一 Team 内无依赖的 call**：并行。`team/runtime.go:353-355` 每个 ready call
  `wg.Add(1); go func(...)`，`:641` `wg.Wait()` 收口。
- **同一 Flow 内无依赖的 team**：并行。`flow/runtime.go:667-694` 同样 goroutine + `wg.Wait`。
- **spawn 子 agent**：并行（`internal/agent/spawn.go`，同步 semaphore / 异步 durable task）。

**关键**：并行只发生在"**同一 turn 内**"，且**收口是同步的**（`wg.Wait()`）——
必须等这一批全部跑完，turn 才继续/结束。这与你想要的"派出去就不管、结果回来了再说"
**方向相反**。

### 2.3 "一个 session 一个 turn"是**架构假设**，不是硬锁

- **没有 session 级 mutex**：`HandleInputWithContext`（`flow/runtime.go:133-170`）不加锁，
  只有 `Resume*`/`ResumeApproval*` 用 `r.resumeMu`（`:177`、`:270`）。
- **没有显式拒绝 `running`**：拒绝列表是 `Failed/Cancelled/Interrupted/WaitingApproval`
  （`:142-147`），`SessionRunning` **不在其中**。
- 所以严格讲，第二个 `HandleInput` 不会被挡住，会**叠加**一个 turn。但——
  **事件账本、状态机、记账都假设单链**：`flow.jsonl` 的 turn 归属、`completedTeams`/
  `attempts` 账本（`:713`/`:792`）、`waiting_tool` 的 park-then-resume 路径，
  叠加第二个 turn 会**各写各的事件、互不知晓**，语义立刻错乱。

### 2.4 现有 async 是"挂起会话"，不是"后台跑 + 主线程继续"

- `agent/task.go:416`：async tool 起独立 goroutine（`context.Background()`）。
- 但主 turn 一旦产出 `waiting_tool` **就结束了**（`flow/runtime.go:860`），
  整个 session 停下；完成回调 `app/runtime.go:424` `wakeFlowOnToolTaskDone` 轮询等
  session 进 `waiting_tool` 再 `Resume`（`:443-449`）。
- 即：**"后台跑"确实有，但"主线程继续接新活"没有**——主线程已经 park 了。

## 三、冲突在哪里（核心）

| # | 用户想要的 | 现状 | 冲突本质 |
|---|---|---|---|
| **1** | default agent 派活后**立刻空闲**、可接新任务 | 派活即进入 turn 内队列等待，**turn 不结束** | **"turn 独占会话" vs "agent 无状态可空闲"** |
| **2** | 多个执行体**并行且互不等** | 并行只在 turn 内，且 `wg.Wait()` **同步收口** | **"turn 内 fan-out + 等齐" vs "fire-and-forget + 异步回流"** |
| **3** | other team 完成后**发消息给** default agent | 结果是 `teamResult` **返回给同一个 turn 的队列循环** | **"返回值" vs "消息/邮箱"** |
| **4** | 同一 agent **fork 多实例**并行 | 一个 agent 定义在一次 turn 里是**单条 call 链**；spawn 是子 agent，不是"同一 agent 多实例接活" | **"agent 可多实例无状态" vs "call 是 turn 内的一个节点"** |
| **5** | 一次输入 = 一次 flow（并行推进） | **一个 session 同时只有一个 FlowTurn**（假设，非硬锁） | **"多 turn 并存" vs "单会话单 turn 顺序推进"** |

### 3.1 最根本的冲突：**"等"的语义**

现状里"default agent 等 other team" = **同步阻塞在 turn 内队列**；
你想要的是"default agent 派活后**状态回到空闲**，other team 完成时**唤醒**它"。

这一个差异决定了另外 4 条：只要"等"是同步的，agent 就永远不空闲（冲突 1）、
就不需要消息机制（冲突 3）、就无所谓多实例（冲突 4）、就天然单 turn（冲突 5）。

### 3.2 状态机视角（**修正**：粒度其实已分层，缺的是"被调度器使用"）

> ⚠️ 初稿这里写"状态粒度是会话不是 agent 实例"——**不准确**。核实后见 §五：
> `TeamSession`/`AgentSession` **各自就有 `Status` 且独立持久化**。

真正的问题是：**Flow 层的调度器（`for len(queue)>0` 的 turn 队列）不看 team/agent 的
status**，只按自己的 activation 队列串行推进。于是"team 空闲"这个事实**存在但无人消费**。

（详见 §五：空闲判据 = `TeamSession.Status != running`，数据已就位。）

### 3.3 事件账本视角

`flow.jsonl` 按 `FlowTurnID` 归属事件。要"多 turn 并存"，事件需要能按**并发的 turn**
隔离与重放；而且"other team 完成 → 唤醒 default agent"要落成**一条可重放的唤醒事件**，
否则崩溃恢复无法重建并发关系。现在的账本是**单链 turn**的，无法表达并发 DAG。

> 📌 现状能并行的粒度对照见 **§7.4**（合并到改造点章节，避免重复）。

## 四、"是不是硬约束"的结论

- **"一个 session 一个 turn"不是硬锁**——没有 mutex、没有拒绝 `running`。
- 但它是**架构假设**：状态粒度（会话级）、事件账本（单链 turn）、async 语义（park-then-resume）
  三层都建立在"单会话单 turn 顺序推进"上。**直接并发会各写各的事件、语义错乱**。
- 因此你的设计**不是"加把锁放开"就能实现**，而是要把"等"从**同步**改成**异步消息**，
  并把状态粒度从**会话**下沉到**team/agent**。

## 五、关键补充：team/agent 其实**已经有各自独立的执行状态**（脚手架已具备）

用户补充了一个通用判据：**"team 的 default agent 没在执行 → team 空闲 → 可承接任务"**。
核实代码后：**这个判据的数据基础已经存在**，只是没人拿它当调度入口。

| 事实 | 位置 | 说明 |
|---|---|---|
| **TeamSession 有独立 `Status`** | `pkg/types/session.go:31-38` | 与 FlowSession 同级，独立字段 |
| **AgentSession 有独立 `Status`** | `pkg/types/session.go:42-50` | 每个 agent call 一份 |
| **team session 是"每 team 一个、持久"的** | `flow/runtime.go:994-1001` | `ID = "<flowSessionID>:<teamID>"`，跨 turn 复用 |
| **team turn 结束会更新 team status** | `flow/runtime.go:1179` | `teamSession.Status = sessionStatusForTurn(turn.Status)` → 不再 `running` |
| **agent turn 结束会更新 agent status** | `team/runtime.go:831/891/894` | `appendAgentSessionUpdated` |
| **状态有专门的事件持久化** | `flow/runtime.go:1183`（`team_session.updated`）、`team/runtime.go:914`（`agent_session.updated`） | 可 replay、可恢复 |

### 空闲判据（现状可直接推导）

```
team 空闲  ⇔  TeamSession.Status ∉ {running}
agent 空闲 ⇔  AgentSession.Status ∉ {running}
```

`sessionStatusForTurn`（`flow/runtime.go:1405-1421`）已经把 turn 终态映射为
`completed` / `waiting_input` / `waiting_tool` / `waiting_approval` / `failed` /
`cancelled`——**只要不等于 `running` 就是"没在执行"= 空闲**。

### 与 §三 冲突的关系

这**削弱了冲突 4/5 的严重性**（"agent 实例级状态"不需要**新建**，只需要**启用**）：

- 之前说"状态粒度是会话不是 agent 实例"——**不准确**：粒度其实**已经有 team/agent 层**，
  问题是 **Flow 层的调度器不看它**，仍然按 turn 队列串行推进。
- 所以缺的不是"新状态字段"，而是**一个"看 team/agent 空闲状态来分派任务"的调度器**
  （mailbox/dispatcher），以及在**不结束 FlowTurn**的前提下把任务投给空闲 team。

**结论修正**：冲突的根从"状态粒度不够"收窄为——
① **"等"是同步的**（`wg.Wait`）＋ ② **Flow 调度器不看 team/agent status** ＋
③ **事件账本是单链 turn**。第 ② 条比原先估计的**轻**，因为状态已就位。

## 六、关键补充：**"round" 概念缺失（并行的记账基础）**——但**降级为辅助，非调度主线**

> ⚠️ **2026-10-10 修订**：初稿（原 5.3.4）把 round 说成"**必须先有 round，才能并行**"，
> 这个定位**偏重了**。用户澄清后修正为：**round 仍然存在**（"一问一答"这个粒度没变），
> 只是它**不该被当成调度阶段门禁（stage）**，而是并行的**记账锚点**。
> 调度主线是 §1.4 的 **mailbox + 空闲即可接活**，round **不驱动调度**。

用户的观察：**引擎里没有"会话交互轮次（round）"这个概念**。核实后确认——
**"round" 被两个语义相反的概念同名占用**，且**引擎侧没有权威的"会话轮次"字段**。
（以下 6.1–6.3 是事实核对，仍然成立；只有结论 6.4 被修正。）

### 6.1 同名不同义：两个 "round"

| 层的 "round" | 实际语义 | 承载 | 落盘 |
|---|---|---|---|
| **agent 层** `round`（`agent/runtime.go:555` `for round := startRound; round < maxRounds; round++`） | **一次 AgentTurn 内的 model/tool 循环圈数**，每个 AgentTurn 从 0（或 checkpoint 的 `NextRound`）重数 | 有字段；写进 `ModelRequestStats.Round`（`execution.go:154`）与 `agent.jsonl` payload 的 `"round"` | ✅ 落 agent.jsonl |
| **TUI** `roundNum`（`view/tui.go:259/518`） | **用户交互轮次**（每次 submit `m.roundNum++`，`flowResultMsg` 明确**不**递增） | 只是**内存计数**，引擎无对应字段 | ❌ 不落盘 |

**两者语义正好相反**：一个是"turn 内第几圈循环"，一个是"会话第几次用户输入"。`view/tui.go`
里的 `Round: %d`（状态栏 `:693`）、`Round %d · tokens this step`（`:599`）、
`Round %d` 分隔线（`:934`）用的都是**用户交互轮次**，与 agent.jsonl 里的 `round`
（内循环）**同名却是两回事**。

### 6.2 引擎**没有**权威的"会话交互轮次"字段

| 结构 | 有没有轮次字段 | 位置 |
|---|---|---|
| `FlowSession` / `TeamSession` / `AgentSession` | ❌ 只有 ID/Status/时间戳 | `session.go:22/31/42` |
| `EventHeader` | ❌ 只有 `Attempt`/`RecoveryOf`，无 round | `event.go:9-24` |
| `FlowTurn` | ❌ 只有 `Attempt`，无 round | `turn.go:79-91` |
| `TeamTurn` | ❌ 只有 `Attempt`，无 round | `turn.go:94-107` |
| `ModelRequestStats.Round` | ✅ 但这是**agent 内循环**轮次 | `execution.go:154` |
| `StateObservation.Round`（`pkg/types/state.go:12`） | ✅ 但这是 **State 学习输入**里的"第几轮观测"，与会话轮次无关 | `pkg/types/state.go` |

**唯一能表达"会话第 N 次交互"的办法**：**数 `flow.jsonl` 里 `flow_turn.started` 事件数**——
事后推断，不是事实源。TUI 的 `SeedHistory` 正是这么干的（`tui.go:1177`
`m.roundNum = len(turns)`），所以它**易失**：`--list` 重开会靠数事件重算，而非读引擎字段。

### 6.3 `Attempt` 是**正交的第三根轴**，别和 round 混

| 轴 | 语义 | 递增时机 | 位置 |
|---|---|---|---|
| **round（agent 层）** | turn 内循环圈数 | 每圈 model 调用 | agent.jsonl payload |
| **round（用户语义，缺字段）** | 会话第几次输入 | 每次用户 submit | 无（TUI 内存） |
| **Attempt** | 该 turn/call 的**第几次执行**（retry/恢复代次） | retry/恢复时 `+1`，配 `RecoveryOf` | `FlowTurn`/`TeamTurn`/各 `CallTurn`/`EventHeader`/`AgentCheckpoint` |

`Attempt` 与 round **无关**：它是"重试了几次"，不是"第几轮对话"。eval 侧
（`internal/eval/correlation.go:60`）也只把 `Attempt` 当恢复代次用，**不依赖 round**。

### 6.4 为什么 round 与"并行"相关，但**不是并行的前提**

在**串行**假设下，"第几轮"可以糊弄——只有一条链，数事件即可推。
一旦**并行 / 交错**（§一、§三），"第几轮"就变成**账本归属问题**：

- 哪几个并发 turn 属于**同一次用户输入**？
- 谁等谁、结果投给**哪一轮**的发起者？
- 事件不再能靠"顺序"判定归属。

**修正后的结论**：round 是并行的**记账锚点（辅助）**，**不是并行的前提条件**。
理由是——**"一问一答"这个粒度并没有消失**（用户澄清）：用户问、default agent 答，
这个交互节奏天然存在，只是 default agent 答的时候**可以派活、派完即空闲、继续答下一问**。
所以 round 更像一个**自然发生的会话事件**（第 N 次用户输入），而不是引擎必须强管的"阶段"
（stage）。引擎要造它，是为了**事后把并发的结果归位到发起它的那一问**，
而不是**靠它来驱动调度**。

调度的主线是 §1.4：**mailbox + "非执行即可接消息"**。round 缺不影响"能不能并行"，
只影响"**并发的结果能不能干净地归属到某次用户输入**"——即记账完整性，而非调度可行性。

### 6.5 命名建议（避免继续同名）

| 概念 | 建议名 | 语义 | 落哪 |
|---|---|---|---|
| agent 内循环 | **step**（或保留 `round` 但限定在 agent 作用域） | 一次 AgentTurn 内第 N 圈 model/tool | 已有，`agent.jsonl` |
| 会话交互 | **round**（新定义） | 第 N 次用户输入，会话级单调递增 | **新增字段**，落 `FlowTurn`（+ `FlowSession.RoundCount`） |

有了 `FlowTurn.round`，并发的 turn 各自带轮次，事件归属不再依赖顺序。

## 七、改造点 / 收益 / 影响面

> 本节只列**范围与判断**，不含实施方案（方案另开文档）。

### 7.1 改造点（按层）

| # | 层 | 现状 | 需要变成 | 关键位置 |
|---|---|---|---|---|
| **C1** | **mailbox** | 无。agent/team 只能被"turn 内队列"同步调用 | 每个 team/agent 一根**可持久、可 replay** 的消息队列；投递源=用户/其他 agent | 新增（`pkg/types` + flow/team runtime） |
| **C2** | **调度器** | Flow 层 `for len(queue)>0` 串行消费 turn 队列，**不看 team/agent status** | 改为**看 `TeamSession.Status`/`AgentSession.Status`** 派发：非 `running` 即可投消息 | `flow/runtime.go:630`（队列循环） |
| **C3** | **"等"的语义** | 同步阻塞：`wg.Wait()` 收口（`flow/runtime.go:667`、`team/runtime.go:641`） | 改成**异步消息**：派活后不 park，完成时投消息唤醒 | `flow/runtime.go:667-694`、`team/runtime.go:353` |
| **C4** | **Flow turn 独占** | turn 一开始 `Status=SessionRunning`，直到队列空才 `finishTurn` | turn 可**并存/交错**；default agent 派活那一轮**立即结束**回到空闲 | `flow/runtime.go:549/630/860` |
| **C5** | **事件账本** | 单链 `FlowTurnID` 归属，无法表达并发 DAG | 并发 turn 的**归属结构** + 可重放的**唤醒/投递事件** | `flow.jsonl` 事件模型 |
| **C6** | **round（辅助）** | 无权威"会话轮次"字段；TUI 靠数事件易失 | 新增 `FlowTurn.round`（+ `FlowSession.RoundCount`），作为**记账锚点**（**非调度主线**，见 §6.4） | `pkg/types/turn.go` |
| **C7** | **理念约定** | 无 | 文档约定 **default agent 只协调不执行**（引擎不强制，见 §1.3） | 配置/文档层 |

**改造依赖顺序**：C1/C2 是地基（mailbox + 按 status 派发）→ C3/C4 是语义变更（异步"等"、
turn 不独占）→ C5 是记账跟上（并发事件）→ C6 是记账完善（round 归位）→ C7 是约定层，**可最后做**。

### 7.2 收益

| # | 收益 | 说明 |
|---|---|---|
| **B1** | **单会话多任务并行** | 用户不再需要"一个任务开一个窗口"，一个入口推进多件事 |
| **B2** | **入口 agent 不卡住** | 派活后立刻可接下一问，交互体感从"等结果"变成"随时提问" |
| **B3** | **自动协调 + 汇总** | 谁派活、谁汇总由 default agent 承担，用户不手工拼结果 |
| **B4** | **通用、非特化** | 规则是"任一 team/agent 非执行即可接消息"，三层一体；default agent 只是恰好承担协调职责的实例，**引擎不因角色特殊化** |
| **B5** | **状态底座已就位** | team/agent 的独立 `Status`（§五）**已存在且已持久化**，本改造**复用它**，不是从零造状态——这是最大的成本利好 |
| **B6** | **可 replay 可恢复** | 消息队列 + 唤醒事件落账后，崩溃恢复能重建并发关系（相比现状单链 turn，恢复语义更完整） |

### 7.3 影响面

| # | 影响面 | 程度 | 说明 |
|---|---|---|---|
| **I1** | **事件契约** | **高** | `flow.jsonl`/`team.jsonl` 事件模型要能表达"并发 turn + 消息投递"，是**已发布契约**（见 CODEBUDDY.md「事件流字段命名」），改动需谨慎、需版本化 |
| **I2** | **消费端（TUI/HTTP/SSE）** | **高** | 现在假设"单链 turn 顺序渲染"，并发的 turn/消息需要新的展示与排序逻辑（参照 `ISSUE-tui-conversation-display-research`） |
| **I3** | **状态机语义** | **高** | `SessionStatus` 现值描述"会话在推进"，并发后需区分"会话里哪些 agent 在跑"；会话级 status 要**退回派生视角**（§七 C4） |
| **I4** | **记账/消耗模型** | **中** | 消耗事实源仍是 `agent.jsonl` 的 `agent_turn.completed.payload.call_result.Requests[]`；并发后 team/flow 级加总需能**按 round 归位**，否则"这次问了花了多少"算不清 |
| **I5** | **eval（`internal/eval`）** | **中** | 现有 correlation 基于 `Attempt` 恢复代次（`correlation.go:60`），并发后需要能按 round/发起者**关联并发的 turn** |
| **I6** | **async tool / park-then-resume** | **中** | 现有"挂起会话"路径（`task.go:416` + `app/runtime.go:424`）与新的"fire-and-forget + 消息唤醒"是**两套异步**，需统一或明确边界 |
| **I7** | **配置/约定层** | **低** | "default agent 只协调不执行"是约定（C7），不改引擎行为，风险低；但需文档与示例同步 |
| **I8** | **回放/恢复正确性** | **高** | 并发 + 消息投递的 replay 需要**确定性**（谁先谁后不影响最终账本），否则恢复不一致——这是硬骨头 |
| **I9** | **context 归属（话题）** | **暂不涉及** | 多话题隔离与 P0 **正交**（§1.5），本次**搁置**；P0 用"固定单话题"由用户自律。真要多话题再单独谈（需用户显式切换信号，thread_id 不可用） |

**影响面小结**：契约层（I1/I2/I8）与状态机（I3）是**高风险区**；
记账（I4/I5）与异步统一（I6）是**中风险**；约定层（I7）低风险可先落地；
**context 归属（I9）与 P0 正交，本次不涉及**。

### 7.4 P0 / P1 / P2 阶段切分与"context 归属"的边界

> 详细论证见 §1.5（三件事拆开：mailbox / context 归属 / thread_id）。

**P0（本次目标，可开发）**——只做 mailbox 调度（§七 C1/C2/C3/C4）：

- default agent 派活后**不卡住**，可接下一问；other agent 完成后**投消息唤醒**它。
- **话题能力 = 单话题**：由**用户自己守规矩**（固定一个话题）。
- **复杂度回到中等**——不含 ②（context 归属），故不含多话题的串话风险。

**P1（暂缓，需先讨论）**——mailbox 消息带**归属信号**，历史分桶：

- 前提是**先定"归属信号"形态**（§1.5.3）：用户显式切换（命令 / 前缀 / 话术），
  **不能**靠模型自由猜。
- **thread_id（IM 模型）不可用**（§1.5.2）：单窗口无显式信号。

**P2（更远）**——共享记忆 / 黑板，跨话题传递全局事实。

**边界声明**：**P0 与 ②（context 归属）无强依赖**。用户"固定话题就行"正是 P0 的用法；
② 的复杂度**不进入 P0**。本次先做 P0，② 留待专门讨论（用户明确"不急着开发"）。

### 7.5 现状能并行的粒度（对照）

| 粒度 | 现状 | 与你的设计 |
|---|---|---|
| tool call 内 | ✅ 只读段并行（`tool_batch.go`） | 已经支持 |
| 同一 team 内 call | ✅ 无依赖并行（`team/runtime.go:353`） | 已经支持 |
| 同一 flow 内 team | ✅ 无依赖并行（`flow/runtime.go:667`） | 已经支持 |
| spawn 子 agent | ✅ 并行（`spawn.go`） | 已有，但语义是"父等子" |
| **turn 级（派活后不阻塞）** | ❌ 顺序 / 同步收口 | 你的核心诉求，**缺口**（C3/C4） |
| **session 级（多输入并行）** | ❌ 假设单 turn | 你的核心诉求，**缺口**（C4） |
| **mailbox / 消息驱动** | ❌ 无 | 统一抽象，**缺口**（C1/C2） |

## 八、一句话总结

**想要**：把 CLI 那种"多开会话"的**物理并发**，变成引擎里的**逻辑并发**——
用户只面对一个入口（default agent），背后多 agent 并行推进多件事。

**统一抽象**：每个 team/agent 都有**一根 mailbox**；**"非执行即可接消息"是贯穿三层的调度律**；
派活 = 投消息，汇报 = 投消息，唤醒 = 有消息可取。default agent 在**理念上**只协调不执行，
在**引擎层**不带任何特权——引擎只认"在不在执行"。

**现状**：**"单会话单 turn 串行推进 + turn 内 fan-out + `wg.Wait` 同步收口"**。
冲突的根是 **①"等"是同步的**（C3）、**②Flow 调度不看 team/agent status**（C2）、
**③事件账本是单链 turn**（C5）。

**好消息**：team/agent 各自独立、可持久、可 replay 的 `Status` **已经就位**（§五），
**round 也从"前提"降级为"记账辅助"**（§六）——所以差的不是"造状态/造轮次"，
而是 **mailbox + 一个按 status 分派的调度器 + 把"等"改成异步消息**。

**风险**：契约层（事件模型 I1、消费端 I2、replay 确定性 I8）与状态机（I3）是**高风险区**，
需版本化、需配套改 TUI/HTTP/SSE 与 eval。

**切分（§1.5 / §7.4）**：`mailbox` 与 `context 归属（话题）` **正交**，可分别落地。
**本次 P0 只做 mailbox**（派活不卡住 + 结果回流），**话题固定单话题、由用户自律**——
② 的复杂度**不进入 P0**。多话题隔离留待 P1/P2 单独讨论
（thread_id 的 IM 模型**不可用**：单窗口无显式归属信号，需用户显式切换）。

## 九、P0 实现记录（2026-10-10）

### 9.1 实现内容

| 文件 | 内容 |
|---|---|
| `pkg/types/mailbox.go`（新） | `MailboxMessage` / `MailboxAddress` / `MailboxMessageStatus`（`pending`/`claimed`/`delivered`/`dead`），叶子字段 snake_case |
| `pkg/types/event.go` | 新增 5 常量：`mailbox.message.enqueued/claimed/delivered/dead` + `mailbox.batch.dispatched` |
| `internal/runtime/flow/mailbox.go`（新） | 投递原语 `enqueueWorkMessage`/`enqueueResultMessage`/`claimMessage`/`deliverMessage`/`markMessageDead`；`loadMailbox`/`listInbox`（事件流折叠，**按 Revision 决胜、Seq 平手**，确定性） |
| `internal/runtime/flow/dispatch.go`（新） | `launchAsyncDispatch`（后台 goroutine，`context.Background()` + `recover` 兜底）；`wakeIfIdle`（**coordinator running 时不唤醒**，result 留 inbox） |
| `internal/runtime/flow/resume_mailbox.go`（新） | `ResumeForMailbox`（专用唤醒入口，**不走** pendingTeamResume/AgentResume）；`renderMailboxInbox` |
| `internal/runtime/flow/runtime.go` | `asyncDispatch`/`wakeFn` 字段 + `SetAsyncDispatch`/`SetWaker`；`NextActivate` 分支 async 化；收尾写 `mailbox.batch.dispatched`（幂等守卫） |
| `internal/app/runtime.go` | 接线：`SetAsyncDispatch(true)` + `SetWaker(go ResumeForMailbox)` |

### 9.2 关键设计决策（相对 §七 的收敛）

- **不做并发 turn**（C4 收敛）：派活轮 `finishTurn(waiting_input)` 结束，后台跑完后 `ResumeForMailbox` 开**新** turn 唤醒。任何时刻只有一个 flow turn running——`interruptedExecutions`/`pendingTeamResume`/`listInbox` 的单链假设**全部保持成立**。
- **mailbox 落 `flow.jsonl`**（不新建第四份文件），复用 `storage.SessionWriter` 契约。
- **开关控制**：`SetAsyncDispatch(bool)`，默认 false = 原同步路径；app 层 P0 硬编码 true。
- **只对 coordinator 的 `NextActivate` 走 async**（`binding.Coordinator` 保证）。
- **归属锚点用 `CauseFlowTurnID`**，P0 **不引入** `FlowTurn.round`（§6.4 的降级）。
- **与 async tool / park-then-resume 共存**：mailbox 是 team/flow 级，`wakeFlowOnToolTaskDone` 是 agent/tool 级，正交。

### 9.3 端到端验证（真实配置，2026-10-10）

配置：coordinator(`main`) `can_activate: [worker]`，`--json-rpc` 常驻进程跑一轮。

观察到的完整闭环：
```
flow_turn.started (输入="请…派活")  → 立即 flow_turn.waiting_input（reply "已派活"）
mailbox.message.enqueued (work→worker, cause_flow_turn_id=ft_…)
mailbox.message.claimed (后台领取)
team_turn.started/completed (worker 后台执行)
mailbox.message.delivered + mailbox.message.enqueued (result→main)
flow_turn.started (输入=""，被唤醒) → main 读 inbox 汇总 → flow_turn.waiting_input
mailbox.batch.dispatched ×2（每 turn 各 1，已修重复 bug）
```

**结论**：派活不阻塞 ✓、结果回流 ✓、唤醒开新 turn ✓。

### 9.4 已知限制 / 后续

- **`--prompt` 单轮模式**：进程跑完一轮即退出，后台 goroutine 随进程结束——异步派发需要**进程存活**（json-rpc / TUI / serve 场景）。这是 `--prompt` 语义决定的，非缺陷。
- **后台 team 崩溃不可自动恢复**（§7.5 已声明）：P0 接受；`claimed` 但未 `delivered` 的 team 在重启后不被 `interruptedExecutions` 发现。
- **P1/P2 未做**：context 归属（话题隔离）、round 字段、跨话题共享记忆。
- **SSE**：async 轮为 `waiting_input` 不触发 `EventFlowTurnCompleted`，流保持连接等 result 回流（§七 I2）。


