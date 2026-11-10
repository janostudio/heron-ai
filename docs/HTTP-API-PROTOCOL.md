# HTTP API 协议：现状、问题与方案

> 状态：**现状已核实，方案待定**（2026-11-10）
>
> 范围：`--serve` 暴露的 HTTP/SSE 接口，即浏览器与外部客户端看到的那一层。
> 不含 JSON-RPC 子进程协议（见 [CONNECT-CLI-JSONRPC.md](./CONNECT-CLI-JSONRPC.md)），
> 也不含 CLI/TUI 的进程内调用。
>
> 本文只描述代码当前的样子和可选方向，不代表已经排期。

## 1. 现在怎么工作的

### 1.1 三条路径，各管一件事

| 用途 | 端点 | 形态 |
|---|---|---|
| 会话列表 | `GET /api/sessions` | 一次性 JSON，全量返回，无分页 |
| 历史记录 | `GET /api/sessions/{id}/record?after_seq=&limit=` | 一次性 JSON，游标分页（默认 500） |
| 过程事件 | `GET /api/stream?session_id=&follow=1` | SSE，带 `Last-Event-ID` 续传 |
| 提交一轮 | `POST /api/sessions/turn` 或 `POST /api/run` | **同步阻塞**，等整轮结束返回 `FlowTurnResult` |

代码位置：路由在 `cmd/server/main.go`，处理在 `internal/view/{handler.go,web.go}`，
事件订阅在 `internal/storage/session.go`。

### 1.2 浏览器实际走的是「常驻跟随 + 同步提交」

页面打开后先取记录、再建立一条**常驻订阅**，之后所有 turn 的事件都从这条流进来
（`internal/view/webassets/app.js` 的 `connect()` / `sessionStream`）。断线按指数退避
重连（1s → 2s → … → 15s 封顶）。

```
进入会话
  ├── GET /api/sessions/{id}/record        ← 历史 + latest_seq
  └── GET /api/stream?follow=1             ← 常驻订阅，Last-Event-ID = latest_seq
        断线 → 退避重连，重连仍带 Last-Event-ID

发消息
  └── POST /api/sessions/{id}/events       ← 202：受理即返回
        事件不来自这个响应，而来自上面那条常驻流
```

提交与接收是两条通道：POST 只负责把消息交给引擎并拿到受理确认，事件全部走常驻流。
**POST 的返回值没有信息量** —— 页面渲染完全依赖流。

> 2026-11-10 之前这里是同步的：`POST /api/sessions/turn` 要等整轮结束才返回，
> 收尾靠 `setTimeout(800)` 猜最后一帧有没有到。改造后这个猜测已删除。

### 1.3 后端重连机制：逻辑成立

`Subscribe(ctx, sessionID, afterSeq)`（`internal/storage/session.go:142-190`）
在**同一把锁内**完成三件事：

```
读事件快照 → 注册 subscriber → 把 seq > afterSeq 的历史推进 channel → 解锁
```

由此得到三个保证：

1. 没有「重放完了但订阅还没生效」的窗口 —— 解锁后的实时事件必然排在重放之后
2. `Seq > afterSeq` 严格比较，不重复不遗漏
3. channel 满时主动 `delete + close`（`session.go:196-205`），不是静默丢事件；
   客户端能从关闭感知，并用 `Last-Event-ID` 补齐

另外，非 follow 模式的重连路径用 `streamReplayBoundary` 算了
「历史结束在哪、是否已 settle」，避免重连时只看到旧 turn 就关流。

### 1.4 端到端缺口：两个已解决，两个仍在

**已解决（2026-11-10，随 `fix/web-interactivity` 合入）：**

- 刷新后自动跟随：页面常驻订阅，不再只在发送时建流
- 断线自动重连：指数退避，重连带 `Last-Event-ID`

**仍存在：**

两个都已在 2026-11-10 的改造中解决（§3.3 / §3.6）：提交改为 202 受理，
`record` 的 `session` 改为 `{id, status}` 对象。

### 1.5 测试盲区

`TestJSONLSessionWriterSubscribeReplaysAndPublishes`
（`internal/storage/session_test.go:106`）覆盖了两件事：订阅后能收到实时事件、
取消后 channel 关闭。

**没有覆盖**：断开期间产生的事件能否在重连时补齐（这正是刷新场景）、
多订阅者并发、慢消费者被踢后重连补历史。

即：重连机制看起来对，但没有用例证明它真的对。

## 2. 问题清单

| # | 问题 | 影响 | 证据 |
|---|---|---|---|
| 1 | 过程与终态来自两条通道 | 客户端解析两套 schema；两路可能不一致，需 `missed` 回退 | `app.js:941-980` |
| 2 | 提交同步阻塞，期间无数据 | turn 跑十分钟，POST 挂十分钟；最容易被代理/网关杀掉，且客户端无法区分「turn 失败」与「连接失败」 | `handler.go` `HandleRun`/`HandleTurn` |
| 3 | 收尾靠 `setTimeout(800)` | 最后一帧可能被截断，也可能白等 | `app.js:971` |
| 4 | 刷新后不重连 | 执行中的 turn 在页面上静止 | `app.js` 仅 `submit()` 建流 |
| 5 | 历史接口无会话状态 | 前端无法判断是否该重连 | `web.go:216-219`、`projectFacts` |
| 6 | 流断开不重试 | 慢消费者被踢后停在「流断开」 | `app.js:1339-1341` |
| 7 | 重连无测试 | 机制正确性没有证据 | `session_test.go:106` |
| 8 | ~~字段命名两套风格~~ | **已修复**（2026-11-10）：三个执行结果结构的 tag 统一为 snake_case | §4.1 |
| 9 | 会话列表无分页 | 会话数增长后接口先失效 | `web.go:51-73` |

## 3. 方案

### 3.1 三种形态

| 形态 | 提交 | 接收 | 丢事件窗口 | 连接数 |
|---|---|---|---|---|
| **A** 单请求单流 | `POST` 带 `Accept: text/event-stream`，连接本身即流 | 同一连接 | 无（提交与订阅原子） | 1 |
| **B** 提交/订阅分离 | `POST .../events` → 202 | `GET .../events` 长连接 | 有，需「先注册订阅再写响应头」契约消除 | 2 |
| **C** 同步阻塞 | `POST` 等整轮 → JSON | 无 | 无，但客户端干等 | 1 |

当前实现是 **B 的一半 + C**：接收侧已经是常驻订阅（B 的形态），提交仍是同步阻塞（C）。

### 3.2 A 不再必要

A（单请求单流）成立的前提是「客户端没有常驻流，需要靠提交这个动作顺便建立订阅」。
页面改成常驻跟随后这个前提消失了：已经有一条覆盖所有 turn 的流，再让 POST 返回一条流
只会多出第二条通道，而它只覆盖这一次 turn，用完即废。

所以剩下要动的只有提交语义，不是推进方式。

### 3.3 提交改为 202 受理

```
POST /api/sessions/{id}/events
Idempotency-Key: <客户端 UUID>
Content-Type: application/json

{ "input": "...", "attachments": [...] }

→ 202 Accepted
{ "session_id": "fs_abc", "status": "accepted", "queued": false }
```

- **不返回 turn_id**：turn 在受理之后才创建，号由常驻流里的 `session.turn.started`
  携带（客户端本来就用事件里的 `flow_turn_id` 分组）。提前发号需要一个占位 turn，
  代价大于收益。
- **202 只表示受理**，不表示完成，也不保证一定能跑完 —— 结果和失败都在流里。
- **幂等键是 `(session_id, idempotency_key)`**。首轮没有 session id，所以键由客户端生成；
  重复提交返回同一个受理结果，不重复执行。

现有 `POST /api/sessions/turn` / `POST /api/run` 保留同步语义，给 CLI 和脚本用：
它们没有常驻流，需要一次调用拿到结果。

### 3.4 runtime 侧要补的能力

`HandleInput` 现在分两步看会话：忙则入队（`queueUserInput`），闲则同步跑完整轮。
202 端点需要第三种组合：**总是入队，然后异步排空**。

```
SubmitInput(ctx, sessionID, input, blocks):
  1. 复用 HandleInput 的前置校验（waiting_approval / interrupted / waiting_tool / 有 running team 都拒绝）
  2. queueUserInput(...)         —— 入队，返回 Queued
  3. 若会话空闲 → go drainQueuedInput(ctx, sessionID)   —— 主动排空
     若会话忙   → 什么也不做，当前回合结束时会自己排空
```

`drainQueuedInput` 已经存在且有 `resumeLock` 保护，重复触发是幂等的（队列空就退出）。

### 3.5 客户端路径

```
进入会话 / 刷新
  ├── GET /record                             → 历史 + latest_seq + status
  ├── if status == running: 显示"进行中"
  └── GET /stream?follow=1                    → 常驻订阅（终态会话可以不建）
        断线 → 退避重连，带 Last-Event-ID

发消息
  └── POST /api/sessions/{id}/events → 202
        事件全部来自上面那条流；不再需要 setTimeout(800) 猜收尾
```

`setTimeout(800)` 那段 fallback 可以删除：它存在的唯一理由是「提交是同步的，所以
流里应该没有新东西了，但万一有呢」。提交改成 202 之后，流就是唯一通道，收尾由
`session.turn.idle` / `session.turn.completed` 事件决定。

### 3.6 顺带把 record 补上 status

`GET /record` 的 `session` 字段目前是字符串 id。改成对象（`{id, status, ...}`）之后，
页面能判断「这个会话已经终态，不必常驻订阅」，避免给已完结的会话挂空流。
数据源已存在（事件流里的 `session.created` / `session.updated`），只是没投影出来。

## 4. 命名规范

### 4.1 字段命名：已统一为 snake_case

Go 字段标识符保持大驼峰（导出标识符的惯例），json tag 承载 wire 格式。两层解耦，
Go 的命名习惯不会漏到对外格式里。

2026-11-10 把 `CallResult` / `TeamTurnResult` / `FlowTurnResult` 的 tag 从
Go 字段名（PascalCase）统一成 snake_case。字段集合由
`pkg/types/execution_json_test.go` 的三个 `*WireFields` 列表锁死，改 tag 会直接测试失败。
完整约束见 [context-management.md §6.4 / §6.5](./context-management.md)。

**这次能直接改 tag 的前提是仓库没有需要兼容的历史 jsonl。** 一旦对外发布过带旧字段名的
数据，同样的改动必须走三步迁移：`UnmarshalJSON` 双读 → 新数据写新名 → 一版后删旧分支。
直接改 tag 会让旧数据静默丢字段（`encoding/json` 只在“仅大小写不同”时兜底，`CallTurnID`
与 `call_turn_id` 差了下划线就不匹配，且不报错）。

### 4.2 状态词表（已完成）

`SessionStatus` 的 `waiting_input` 改成 `idle`（`pkg/types/session.go`）。差别是视角：
前者描述引擎在等什么，后者描述客户端可以再发一条。

| 状态 | 变化 |
|---|---|
| `idle`（原 `waiting_input`） | 已改名 |
| `waiting_tool` / `waiting_approval` | **未合并**，理由见下 |
| `created` / `running` / `completed` / `cancelled` / `failed` | 不变，与主流一致 |
| `interrupted` | 保留：语义是「进程崩溃留下的」，与 `failed`（执行失败）不同 |

`waiting_tool` + `waiting_approval` 合并成 `requires_action` + 明细数组**这一项没做**。
它不是改名，是状态机重构：两者在代码里驱动不同的恢复路径
（`ResumeForToolResult` 走 task，`ResumeApproval` 走审批），
`internal/runtime/flow/runtime.go` 有多处 switch 靠 status 分流，
合计 109 处引用。合并后必须改成按「`pending_approvals` / `pending_tool_tasks` 里有什么」
分流，改错会让会话停在无法恢复的状态。它应当作为独立任务评估与验证。

`TurnStatus` 的 `waiting_input` 也保留：turn 是「跨多轮的会话段」，等用户回复是它的正常主循环；
`SessionStatus` 描述的是整个会话能否接收下一条输入。两者不在同一层语义上，
强行对齐反而会让 turn 多出一个它不该有的终态。

### 4.3 事件命名（已完成）

所有事件类型统一到 `session.` 命名空间（`pkg/types/event.go`）：

| 之前 | 现在 |
|---|---|
| `session.created` / `.updated` | `session.created` / `session.updated` |
| `flow_turn.*` | `session.turn.*` |
| `team_turn.*` | `session.team_turn.*` |
| `team_session.*` | `session.team_session.*` |
| `agent_session.*` | `session.agent_session.*` |
| `agent_turn.*` | `session.agent_turn.*` |
| `command_turn.*` / `webhook_turn.*` | `session.command_turn.*` / `session.webhook_turn.*` |
| `shared_record.*` | `session.record.*` |
| `mailbox.*` | `session.mailbox.*` |
| `wakeup.*` | `session.wakeup.*` |
| `approval.*` | `session.approval.*` |
| `recovery.*` | `session.recovery.*` |
| `session.agent.model_response` / `session.agent.feedback` | `session.agent.model_response` / `session.agent.feedback` |
| `tool_call.*` | `session.tool_call.*` |
| `session.context.compacted` | `session.context.compacted` |

动作词保持 `started` / `completed` / `failed` / `cancelled`：事件记录的是已经发生的事，
过去时是对的。`in_progress` 是状态名，不是事件名，所以没有采用。

一个事件名跟着状态走：`session.turn.idle` → `session.turn.idle`，与 `SessionIdle` 对齐。
`session.team_turn.idle` / `session.agent_turn.idle` 同理，但它们对应的 `TurnStatus` 值没变，
所以事件名与状态名在这两处并不一一对应——这是 TurnStatus 保留 `waiting_input` 的直接后果。

`EventHeader` 的 id 字段（`flow_session_id` / `flow_turn_id`）**没有改**：它们标识的是
Go 类型 `FlowSession` / `FlowTurn`，改名要动 502 处生产代码，而 wire 上换来的只是措辞。
字段名与事件名的取舍不同，因为一个标识对象、一个构成词汇表。

**一个必须一起改的副作用**：`cmd/server/main.go` 的 `eventLayer` 原本靠前缀判断事件属于
flow / team / agent 哪一层（`strings.HasPrefix(eventType, "flow_")` 之类）。统一到
`session.` 后前缀不再区分层级，已改成基于常量的显式映射。这类「从命名反推结构」的代码
是改名时最容易静默失效的地方——它不会编译报错，只会返回错误的分类。

### 4.4 增量与完结成对

```
现状：  data: {"type":"session.agent.model_response","payload":{"reply":"完整的一段话"}}
建议：  data: {"type":"output_text.delta","delta":"逐"}
        data: {"type":"output_text.delta","delta":"字"}
        data: {"type":"output_text.done","text":"完整的一段话"}
```

影响不止命名：

- 客户端现在要 diff 前后两次 `model_response` 或整块替换，做不到 token 级流式
- `done` 事件携带完整文本，可以作为「这条已完结」的明确信号，覆盖临时缓冲

这是行为改进，优先级高于纯改名。

### 4.5 错误结构

现状是裸字符串：`Error string`（`team_call.go:60`、`execution.go` 多处）。
客户端无法按错误类型分支，只能做字符串匹配。

建议改为结构化：`{"code": "...", "message": "..."}`，`code` 用稳定枚举，
`message` 只给人读。

### 4.6 ID 前缀

现状 `fs_` / `ft_` / `tt_` / `mb_`。前缀本身是好实践（便于日志检索）。
建议对外暴露的只保留 `sess_` / `turn_`，其余内部沿用。

### 4.7 分页

会话列表 `GET /api/sessions` 目前全量返回，无分页。
建议统一 `after`（游标）+ `limit`（默认 20，上限 100）+ `order`（默认 desc），
与历史接口的 `after_seq` / `limit` 保持一致。

## 5. 待定决策

| # | 决策 | 选项 | 影响 |
|---|---|---|---|
| D1 | 是否采用 A 形态 | ~~A / 保持现状~~ **已否定**（§3.2） | 页面已有常驻流，A 会多出第二条通道 |
| D2 | 同步端点如何处理 | 保留同步 + 新增 202 端点 | CLI 与脚本没有常驻流，需要一次调用拿到结果 |
| D3 | 事件是否改名 | ~~改名 / 保持~~ **已完成**（§4.3） | 所有消费方已同步：Go 常量、浏览器端、e2e 用例、Skill 排查命令 |
| D4 | 状态词表是否改 | `waiting_input`→`idle` **已完成**（§4.2）；`requires_action` 合并待评估 | 合并会改恢复路径分流，不是命名改动 |
| D5 | 是否引入 item 概念 | 引入 / 用 seq + call_turn_id 代替 | 引入需要维护 item_id / output_index / content_index |
| D6 | 历史接口是否返回会话状态 | 加 `status` 字段 / 保持 | 前端自动重连依赖它 |

## 6. 建议顺序

已完成（2026-11-10）：

- 字段命名统一 snake_case（§4.1）
- `waiting_input` → `idle`（§4.2）
- 事件名统一 `session.` 命名空间（§4.3）
- 页面常驻跟随 + 断线退避重连（随 `fix/web-interactivity` 合入）
- 提交改 202 受理 + `record` 返回会话状态（§3.3 / §3.6）

接下来按收益排序：

1. **断线窗口测试**（§1.5）—— 用测试证明重连补事件可靠
2. **增量/完结成对**（§4.4）—— 真正的流式渲染
3. **错误结构化**（§4.5）
4. **会话列表分页**（§4.7）
5. **`requires_action` 状态合并**（§4.2）—— 独立任务，需要恢复路径的测试覆盖

## 7. 改造前后对照

> 这是 2026-11-10 那次改造的记录。左列是改造前的行为，右列是改造后的行为；
> 改造已实施，代码与右列一致。

用四个场景说明两种行为的差别。四个场景合起来覆盖了这条链路上
全部的用户可见问题。

### 7.1 场景一：打开一个刚结束的会话

```
现在                                    改造后
────────────────────────────────────    ────────────────────────────────────
GET /record                             GET /record
  → { session: "fs_abc", events,          → { session: { id, status: "idle" },
      latest_seq }                            events, latest_seq }
GET /stream?follow=1                    status 是终态 → 不订阅
Last-Event-ID: latest_seq               直接渲染历史，页面不再持有连接
  → 常驻流挂着，直到用户离开页面
```

差别：现在每个打开过的会话都挂一条流；改造后只给还会产生事件的会话建流。

### 7.2 场景二：发一条短消息

```
现在                                    改造后
────────────────────────────────────    ────────────────────────────────────
POST /sessions/turn                     POST /sessions/{id}/events
  → 等 turn 结束                          → 202（毫秒级）
  → 200 FlowTurnResult                    事件从常驻流进来：
                                            session.turn.started
事件同时从常驻流进来                          session.agent.model_response
                                            session.turn.idle        ← 结束
POST 返回后 setTimeout(800)
  判断"流收到过帧吗"
  没收到 → 整页重拉
```

差别：现在同一轮的结果有两个来源（POST 响应 + 流），要判断哪个更可信；改造后只有流
一个来源。

### 7.3 场景三：发一条派发型任务（跑几分钟）

这是问题最集中的场景。

```
现在                                    改造后
────────────────────────────────────    ────────────────────────────────────
POST /sessions/turn                     POST /sessions/{id}/events
  │                                       → 202，连接立刻释放
  ├─ 0s   连接建立                        │
  │       （此后几分钟无任何数据）          ├─ 0s   流：turn.started
  │                                       ├─ 3s   流：team_turn.started
  │   ⚠ 代理 60s 空闲超时会掐断这条连接    ├─ 90s  流：agent.model_response
  │   ⚠ 掐断后客户端看到的是"发送失败"     ├─ ...
  │      但服务端仍在跑                    ├─ 240s 流：turn.idle
  │                                       │
  ├─ 240s 才返回 200                      └─ 全程只有一条流，且一直有事件或心跳
  │
  └─ 若连接已被掐断 → 用户看到失败，
     实际 turn 成功，状态不一致
```

差别：现在这条 POST 是整条链路里唯一「长时间存在但没有任何数据」的连接，也是最容易
被中间设备杀掉的；改造后它只活几毫秒。

### 7.4 场景四：长任务期间刷新页面

```
现在                                    改造后
────────────────────────────────────    ────────────────────────────────────
刷新 → GET /record                     刷新 → GET /record
     → 渲染历史                             → 渲染历史 + status = running
     → GET /stream?follow=1                → GET /stream?follow=1
     → Last-Event-ID: 最新 seq              → Last-Event-ID: 最新 seq
     → 继续收到后续事件                      → 继续收到后续事件

（这条 2026-11-10 之前是坏的：刷新后不订阅，页面静止到用户再发一条消息）
```

差别：这一场景**已经修好**，改造不改变它。列在这里是为了说明：断线恢复能力与提交
语义是两件独立的事，前者已经具备，后者才是剩下的问题。

### 7.5 四个场景的效果汇总

| 维度 | 现在 | 改造后 |
|---|---|---|
| 提交请求占用连接 | turn 全程（可能几分钟） | 毫秒级 |
| 长任务期间「无数据的长连接」 | 一条（POST） | 零条 |
| 一轮结束的判定依据 | POST 返回 + `setTimeout(800)` 猜测 | 流里的 `session.turn.idle` 事件 |
| 结果的来源 | 两处（POST JSON + 流事件） | 一处（流） |
| turn 失败与连接失败 | 客户端无法区分 | 流里都有明确事件 |
| 已终态会话 | 仍挂一条流 | 不订阅 |
| 刷新后恢复 | 已具备（常驻流 + Last-Event-ID） | 不变 |

一句话：改造不是加功能，是**把「提交」从一条承担结果的连接，降级成一次受理动作**，
让结果只从流里来。
