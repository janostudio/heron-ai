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

### 1.2 浏览器实际走的是「先订阅 + 同步提交」的组合

前端 `submit()`（`internal/view/webassets/app.js:934-989`）的顺序是：

```
1. GET /api/stream?follow=1        ← 先建订阅（Last-Event-ID = 屏幕上已有的最大 seq）
2. POST /api/sessions/turn         ← 再提交，同步等整轮结束
3. setTimeout(800)                 ← POST 返回后再猜 800ms 关流
4. 若流一帧都没收到 → renderRecord 整页重拉
```

源码注释把这个顺序的动机写得很清楚：

```js
/* Subscribe before posting. The turn starts the moment the request
   lands; a stream opened afterwards would miss its opening events. */
```

以及收尾逻辑：

```js
// The POST answers when the turn is over, so the stream has nothing left to add.
```

所以当前形态是 **「提前订阅（B 的一半）+ 同步阻塞（C）」的组合**，
既不是单请求单流（A），也不是轮询。

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

### 1.4 端到端不可靠：三个缺口

**缺口 1：刷新后不自动重连。**
`app.js` 只在 `submit()` 里建流。刷新后走 `renderRecord` 只渲染历史，
不检查会话是否还在跑，也不建流。正在执行的 turn 在页面上表现为静止，
要再发一条消息才恢复。

**缺口 2：历史接口不直接给会话状态。**
`GET /record` 返回 `{session, events, nodes, facts, latest_seq, truncated, after_seq}`，
其中 `session` 是**字符串 id** 而不是对象；`facts` 只有统计
（turns / usage / calls / models…），没有 status。要判断「还在跑」得遍历
`nodes` 找 `flow_session` 节点的 Status —— 可行，但没有文档说明该这么做。

**缺口 3：流异常断开后不重试。**
浏览器端用 fetch + ReadableStream 而不是 EventSource（因为要带 Authorization 头，
见 `app.js` 注释），`pump()` 在 `chunk.done` 时直接返回，不会自动重连。
只有 `onError` 把「流断开」写进标题栏，没有恢复动作。

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

当前实现是 **B 的一半 + C**。

### 3.2 推荐 A

理由不是 A 更常见，而是本仓库已经具备 A 成立的两个前提：

1. **「订阅必须先于提交」这个约束已经存在**（`app.js` 注释写明）。A 天然满足它：
   提交与订阅是同一个动作，不需要靠调用顺序和注释保证。
2. **流可以重放**（`Subscribe` 会先补 `seq > afterSeq` 的历史）。所以 A 不牺牲恢复能力：
   连接断掉后转 GET 续传，与 B 完全等价。

A 相比现状直接消掉问题 1、2、3：只有一条通道、turn 期间始终有事件或心跳、
不需要猜收尾时机。

### 3.3 A 的完整路径

```
首次进入 / 刷新
  ├── GET /record?after_seq=0&limit=500        → 历史 + latest_seq
  └── if status == running:
        GET /stream?follow=1
        Last-Event-ID: latest_seq              → 续上正在跑的 turn

发新消息
  └── POST /api/sessions/{id}/events
      Accept: text/event-stream
      Idempotency-Key: <客户端 UUID>
      → 200 OK, text/event-stream
        data: {"type":"turn.created","turn_id":"ft_x","session_id":"fs_x"}
        data: {"type":"turn.item.added", ...}
        data: {"type":"output_text.delta", "delta":"..."}
        data: {"type":"turn.completed","turn_id":"ft_x","reply":"..."}

新建会话
  └── POST /api/sessions
      Accept: text/event-stream
      → 首帧 data: {"type":"session.created","session_id":"fs_x"}

断线重连
  └── GET /stream?follow=1
      Last-Event-ID: <已看到的最大 seq>
```

三条硬约束：

1. **流的生命周期 = 一次 turn**。跨 turn 常驻就退化成 B 了；这个需求交给 GET 订阅。
2. **首帧必须在任何模型调用之前 flush**（`session.created` / `turn.created`），
   这是客户端拿到 id 做断线恢复的唯一时机。
3. **断线恢复走 GET，不重发 POST**。优先用 `Last-Event-ID` 续传。

### 3.4 为什么不选 B

B 把「先订阅再提交」的责任交给调用方，并且要求服务端遵守
「先完成订阅注册、再写出 200 响应头」这条不可观测的契约。A 在协议层面消除这个依赖，
调用方不需要理解它。B 的唯一优势是提交和接收可以分连接，本仓库目前没有这个需求。

### 3.5 A 不改变历史获取

历史一直是独立接口（`GET /record`），不是从流里读的。刷新后 A 与现状完全一致：
先查历史，再决定是否订阅。A 只影响「正在进行的 turn 的增量从哪来」。

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
| D1 | 是否采用 A 形态 | A / 保持现状 | 改契约语义；只影响浏览器端（JSON-RPC 与 CLI 不经过 HTTP SSE） |
| D2 | 是否保留同步端点 | 保留 `POST /api/run` / `/api/sessions/turn` / 还是替换 | 保留则两套语义并存，替换则 CLI/脚本需要改 |
| D3 | 事件是否改名 | ~~改名 / 保持~~ **已完成**（§4.3） | 所有消费方已同步：Go 常量、浏览器端、e2e 用例、Skill 排查命令 |
| D4 | 状态词表是否改 | `waiting_input`→`idle` **已完成**（§4.2）；`requires_action` 合并待评估 | 合并会改恢复路径分流，不是命名改动 |
| D5 | 是否引入 item 概念 | 引入 / 用 seq + call_turn_id 代替 | 引入需要维护 item_id / output_index / content_index |
| D6 | 历史接口是否返回会话状态 | 加 `status` 字段 / 保持 | 前端自动重连依赖它 |

## 6. 建议顺序

已完成（2026-11-10）：

- 字段命名统一 snake_case（§4.1）
- `waiting_input` → `idle`（§4.2）
- 事件名统一 `session.` 命名空间（§4.3）

未做，但不需要决策 —— 与 A/B 的选择无关：

1. 历史接口返回 `status`（D6）—— 刷新自动重连的前提
2. 断线窗口测试（§1.5）—— 用测试证明重连可靠
3. 会话列表分页（§4.7）—— 新增参数，默认行为不变

需要决策后做：

4. A 形态（D1/D2）—— 新增端点 + 改浏览器端
5. 刷新自动重连（依赖 1 和 4）
6. 增量/完结成对（§4.4）
7. 错误结构化（§4.5）
8. `requires_action` 状态合并（§4.2）—— 独立任务，需要恢复路径的测试覆盖
