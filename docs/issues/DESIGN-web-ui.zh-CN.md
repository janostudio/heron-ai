# 设计探讨：Web 页面（一个引擎，三个前端）

> 状态：设计稿，未实现
> 日期：2026-11-02
> 前置：`DESIGN-schedule-wakeup.zh-CN.md`（定时唤醒，已合入）

## 一、需求与现状

### 1.1 要什么

heron-ai 现在只有 CLI（TUI）。需要同时具备**页面**和 **CLI**，且两者看到的是同一批会话、同一份状态。

这不是"再做一个界面"那么简单：定时唤醒已经能用了，但唤醒回合的产出只有两个出口——事件流（要写客户端去读）和 IM（要装 heron-connect）。**没有页面，用户就看不到 agent 自己醒来做了什么**，这个能力的价值等于打了对折。

### 1.2 现在离页面有多远

比想象的近：页面要的三块数据，代码里都已经有实现，只是没暴露成 HTTP。

| 页面需要 | 现成实现 | 缺什么 |
|---|---|---|
| 会话列表 | `sessionlist.List(fs, dir)`（`internal/sessionlist/list.go:47`），返回 `ID/Title/Turns/Tokens/UpdatedAt/Active` | 一个 HTTP 接口 |
| 会话历史 | `sessionlist.History(fs, sessionDir)`（`internal/sessionlist/history.go:47`），重建 Turn → Messages → Operation | 一个 HTTP 接口 |
| 实时事件 | `/api/stream?follow=1`（`internal/view/handler.go:215`），支持 `Last-Event-ID` 续传 | 无 |
| 发消息 / 审批 / 取消 | `/api/sessions/turn`、`/api/approvals`、`/api/cancel`（`cmd/server/main.go:184-201`） | 无 |
| 健康与定时器 | `/api/health`（`internal/view/handler.go:277`），返回 `ready/pending_wakeups/sessions_loaded` | 无 |
| 静态页面 | **无**。全仓 `go:embed` 只用在 `internal/definitions/templates.go` | 前端资产 + 托管路由 |

所以缺的是：**两个只读接口 + 静态资源托管 + 前端**。

### 1.3 但有一个必须先解决的约束：单写者

`runTUIWithSession` 在自己的进程里 `buildCurrentRuntime`（`cmd/server/main.go:905`）。也就是说：

```
heron-ai --serve        → 进程 A，持有 runtime，写 session.jsonl
heron-ai               → 进程 B，TUI，又 build 了一个 runtime，也写同一个 session.jsonl
```

`JSONLSessionWriter` 的 `nextSeq` 是**进程内存**的（`internal/storage/session.go`）。两个进程写同一个 session 会各自从 1 开始分配 seq，事件流直接乱掉。

**所以"打开页面"绝不能是"再起一个进程"。** 这是本设计的第一个约束，不是实现细节：一个进程持 runtime，页面和 TUI 都是它的前端。

---

## 二、业界形态对照

| 产品 | 形态 | 关键点 |
|---|---|---|
| **Claude Code** | 一个引擎，五个前端（CLI / Desktop / Web / Mobile / IDE） | 真正的分野是「agent 进程在哪」：CLI 和 Desktop 跑在本机、能看到未提交改动；Web 跑在 Anthropic 的 VM 里、只能 clone GitHub。Web 端另有一个 `--remote-control` 模式：**执行留在本机，浏览器只是方向盘**（不开入站端口，只发出站 HTTPS） |
| **OpenCode** | `opencode web` 起本地 server（默认 `127.0.0.1` + 随机端口，自动开浏览器）；`opencode attach http://localhost:4096` 让 TUI **附加到运行中的 server**，页面和终端同时用、共享同一批会话 | 正是"单写者 + 多前端"的标准形态；另有 `--mdns`、CORS、`OPENCODE_SERVER_PASSWORD` |

**heron 该选哪一种**：本地引擎 + 浏览器窗口，也就是 Claude Code 的 Remote Control 形态 / OpenCode 的 `web` + `attach` 形态。理由：agent 要读写本地仓库、跑 Bash、看未提交改动，这些能力在"云端 VM"形态里全部丧失；而 heron 的定时唤醒需要进程常驻，正好与"本机常驻引擎"一致。

**不选**：云端托管形态（要 GitHub App、要 VM 生命周期、要租户隔离），那是另一个产品。

---

## 三、目标形态

```
┌──────────────────────────────────────────────────────────┐
│ heron-ai（一个进程，唯一持有 runtime 与 session 写权）   │
│                                                          │
│ Runtime ── WakeupScheduler ── SessionWriter（唯一写者）  │
│                                                          │
│ 前端一  HTTP + SSE   /api/*，机器消费者                  │
│ 前端二  静态页面     go:embed 进二进制                   │
│ 前端三  TUI          bubbletea，进程内                   │
└──────────────────────────────────────────────────────────┘
```

三种启动形态，同一个引擎：

| 命令 | 行为 |
|---|---|
| `heron-ai` | 起 runtime + HTTP server（随机端口）+ TUI。终端里干活，同时可以用浏览器打开同一会话 |
| `heron-ai --serve --port 8080` | 只起 HTTP server，无 TUI。后台常驻，给页面和机器消费者用 |
| `heron-ai attach http://host:8080` | 只起 TUI，连远端引擎，不起 runtime。远程/多人场景 |
| `heron-ai -prompt "..."` | 一次性，跑完退出（现状不变） |

`attach` 是让 CLI 在"远端引擎"下仍然可用的关键：它不持有 runtime，只消费 `/api/stream` 和 `/api/sessions/turn`，所以不违反单写者。

---

## 四、关键决策

### 4.1 前端：无 Node 构建，`go:embed` 内嵌

| 方案 | 代价 |
|---|---|
| **原生 HTML + CSS + 少量 JS，`go:embed` 进二进制**（选它） | 无前端工具链，`go build` 就是全部；页面复杂度上限低 |
| SPA（React/Preact + 构建产物 embed） | 需要 Node 与构建步骤，CI 变复杂；对"会话列表 + 时间线 + 审批"这类界面收益有限 |

理由：这个页面的形态是「列表 + 时间线 + 少量交互」，不是表单密集型应用。用 300 行原生 JS 能覆盖，换来"单二进制、零构建依赖、改一行刷一下就生效"。

如果将来要做复杂视图（diff 比对、看板），可以升级成 SPA，`go:embed` 的目录换掉即可，接口不变。

### 4.2 渲染：服务端只给 JSON，页面自己渲染；**禁止 innerHTML**

agent 的输出包含任意文本、代码块，有时是模型生成的 HTML/JS。全仓最容易出安全事故的地方就是这里：把 `reply` 直接塞进 `innerHTML` 就是一个 XSS，而**这个页面背后是一个能跑 Bash、能写文件的 agent**——XSS 等于把执行权交给网页里的任意脚本。

做法：
- 一律 `textContent` 或转义后再插入；
- markdown 只渲染白名单子集（代码块、粗体、列表、链接），链接强制 `rel="noopener noreferrer"` 且只允许 `http/https`；
- 代码块不高亮或只在客户端本地高亮，不引入会执行 DOM 的第三方渲染器；
- CSP 头：`default-src 'self'; script-src 'self'; connect-src 'self'`，页面里不写内联 `<script>`。

### 4.3 实时：复用 `/api/stream?follow=1`，不新造通道

已有的 SSE 已经能覆盖页面的全部实时需求：turn 开始/结束、工具调用、delivery、wakeup 事件都在同一条流上，且 `Last-Event-ID` 支持续传（页面刷新/断网重连不丢）。

页面侧的连接策略：
- 打开会话 → 先用 `/api/sessions/{id}/history` 拉历史，记住最大的 `seq`；
- 再用 `Last-Event-ID: <seq>` 连 `/api/stream?follow=1`，只收增量；
- 断线自动重连，退避 1s → 30s。

**注意 SSE 帧的形状**（真机验证过的坑，见 `DESIGN-schedule-wakeup` §11.10）：`data:` 里是**扁平的事件头 + 嵌套 payload**；`seq/type/flow_turn_id/created_at` 在顶层，`origin/reply/intermediate/deliveries/contributors` 在 `payload` 内。页面解析要按这个分层写，不能把 `flow_turn_id` 当 payload 字段。

### 4.4 鉴权：默认 loopback，非 loopback 强制 token

现状：`requireBearerToken`（`cmd/server/main.go:214`）在非 loopback 监听时要求 Bearer token。

页面要新增一条路径：浏览器没法方便地加 `Authorization` 头。做法：
- `--serve` 监听 loopback（默认）时**不需要 token**，页面直接可用；
- 监听 `0.0.0.0` 时**必须** token，页面走登录：`POST /api/login` 换取 `HttpOnly; SameSite=Strict` 的 cookie，之后所有 `/api/*` 同时接受 Bearer 与 cookie；
- cookie 模式下的写操作（发消息、审批、取消）要带 `X-Requested-With` 头做 CSRF 双提交校验。

明确不做：把 token 放进 URL query（会进浏览器历史、Referer、代理日志）。

### 4.5 页面必须让"agent 自己醒来做的事"显眼

这是 heron 和其他 agent 页面最大的产品差异。别的产品页面是「你问它答」，heron 的页面要回答「**它背着你做了什么**」。

三处体现：

1. **时间线里区分 origin**：`origin=wakeup` 的回合用不同底色 + 左侧标识 + 标题「定时检查（你不在时）」；`intermediate=true` 的回合折成一行灰色进度，不占正片。
2. **定时任务面板**：列出 `status=scheduled` 的唤醒（何时触发、note 原文），以及最近的 `fired/skipped/expired` 及**被推延的原因**（quiet 窗口 / 下游 Team 在跑 / 会话忙）。这个面板是"agent 有没有在按计划干活"的唯一可视图。
3. **未读与总览**：会话列表标出"有一条你还没看过的主动汇报"，而不是让用户自己翻。

### 4.6 关键数据不在页面里做聚合

列表页的 `Active`、`Turns`、`Tokens` 全部来自 `sessionlist.List` 的既有派生逻辑（它按 `flow_turn.*` 事件判断 Active，而不是读 `session.status`——后者在 `flow.jsonl` 里停在 `running` 不会回写）。页面只负责显示，不重新推导。

---

## 五、页面清单

### 5.1 会话列表 `/`

```
┌──────────────────────────────────────────────────────────┐
│ heron-ai    ● 待处理 2 个定时任务                        │
│                                                          │
│ ● 修复 miora 沙箱镜像构建失败                            │
│   3 轮 · 12.4k tok · 3 分钟前 · 定时检查 5 分钟后        │
│   （有未读主动汇报）                                     │
│ ○ 升级 workspace 模板                                    │
│   7 轮 · 28.1k tok · 1 小时前                            │
│ ○ 调研 ACP 协议                                          │
│   运行中 ●                                               │
└──────────────────────────────────────────────────────────┘
```

字段来自 `sessionlist.Session`：`Title`、`Turns`、`Tokens`、`UpdatedAt`、`Active`；未读徽标来自页面本地状态。

### 5.2 会话详情 `/s/{session_id}`

```
┌──────────────────────────────────────────────────────────┐
│ ← 修复 miora 沙箱镜像构建失败    等待输入 · 5 分钟后定时 │
│                                                          │
│ 你 04:12                                                 │
│   升级沙箱镜像到最新的基础层                             │
│                                                          │
│ 助手 04:12  [已转给 team-sandbox]   ← intermediate，灰色 │
│                                                          │
│ ⏰ 定时检查 06:12                   ← origin=wakeup      │
│   下游已跑 2 小时，无新增产出，已催一次                  │
│   ▸ 3 条交付物                                           │
│                                                          │
│ [输入框]                [发送]  [取消当前回合]           │
└──────────────────────────────────────────────────────────┘
```

特性：
- 工具调用折叠为一行（`Bash (make build)` + 成功/失败 + 耗时），点开看输出；
- `deliveries` 单独成块，标 `team_id`；
- `pending_approvals` 时输入框上方出现审批条（允许 / 拒绝 + 原因）；
- 页面标题栏显示当前 `origin=wakeup` 的下次触发时间。

### 5.3 定时任务面板 `/wakeups`（或详情页右侧抽屉）

| 列 | 内容 |
|---|---|
| 会话 | 链接到详情 |
| 登记时间 | `created_at` |
| 计划触发 | `fire_at` |
| 状态 | `scheduled / fired / skipped / expired` |
| 原因 | skipped 时显示 `reason`（引擎已给出具体门禁） |
| note | 模型当初写下的话（前 N 字） |

### 5.4 状态页 `/status`

`/api/health` 的内容 + 会话数 + 最近 20 条事件的时间线（排查"定时器还在不在"）。

---

## 六、API 缺口

| 接口 | 方法 | 数据源 | 说明 |
|---|---|---|---|
| `/api/sessions` | GET | `sessionlist.List` | 列表，支持 `?active=1` 过滤、`?limit=` |
| `/api/sessions/{id}/history` | GET | `sessionlist.History` | 完整历史（Turn → Messages），支持 `?after_seq=` |
| `/api/wakeups` | GET | `Runtime` 的 wakeup index | 列表，支持 `?session_id=`、`?status=` |
| `/api/wakeups/{id}` | DELETE | `Runtime.CancelWakeup` | 页面取消定时 |
| `/api/login` | POST | token 校验 | 换 cookie（仅非 loopback 模式） |
| `/`、`/s/{id}`、`/static/*` | GET | `go:embed` | 静态资源；不存在的一律 404，不退化成文件系统读 |

`/api/wakeups` 需要一个只读访问器：现在 `Runtime.WakeupStats` 只返回计数（`pending, sessions`），要补一个返回记录切片的（`Wakeups(ctx, sessionID) []types.WakeupRecord`），仍然只读。

---

## 七、与 heron-connect 的关系

**heron-ai 不依赖 connect，页面也不经过 connect。**

| 场景 | 走什么 |
|---|---|
| 本地看进度、审批、发消息 | 页面（`/api/*` + SSE） |
| 后台常驻 + 远程看 | `--serve` + 页面（局域网/反代 + token） |
| 主动汇报到 IM | delivery 出口（webhook），或 connect 作为可选网关 |
| 在 IM 里对话 | connect（或任意网关）调 `/api/sessions/turn` |

connect 的价值收窄为「IM 平台接入 + 多用户/权限/展示」，不再是"让 agent 能被用起来"的必经环节。这是本设计对上一轮的收敛：**heron-ai 自闭环，IM 是可插拔出口。**

---

## 八、阶段与验收

| 阶段 | 内容 | 验收（可复现） |
|---|---|---|
| **P0 只读可看** | 静态托管 + `/api/sessions` + `/api/sessions/{id}/history` + 列表页 + 详情页（历史 + SSE 实时） | `heron-ai --serve`，CLI 起一个会话并让它跑起来，浏览器打开页面：能看到该会话出现在列表、点进去能看到历史与**正在产生**的输出（工具调用逐条出现） |
| **P1 能操作** | 发消息、审批、取消；输入框与审批条 | 页面发一句 → CLI 侧同一会话看到该消息；触发审批时页面出现审批条，点允许后会话继续 |
| **P2 主动能力可见** | `/api/wakeups` + 取消；定时任务面板；`origin=wakeup` 回合的样式与未读 | 让模型登记 2 分钟后的唤醒 → 页面显示"2 分钟后"→ 到点后时间线出现带标识的检查回合，列表页出现未读徽标 |
| **P3 CLI 对齐** | `attach` 子命令；`heron-ai` 无参数时进程内同时起 HTTP + TUI | 两个终端：一个 `--serve`，一个 `attach`；在 attach 里发消息，页面实时出现；反过来亦然，且 `flow.jsonl` 的 seq 连续无重复 |
| **P4 外发** | delivery webhook（webhook / exec 两种 target） | 配一个本地 echo 服务当 target，唤醒回合的结论被 POST 出去，含 `turn_id/origin/deliveries` |

P0 单独可用：只读页面已经能解决"看不到 agent 在干什么"的核心痛点。P2 是差异化，P3 才动 CLI 结构（风险最高，放后面）。

---

## 九、风险

| 风险 | 内容 | 应对 |
|---|---|---|
| **双写** | 有人仍按老习惯同时开 TUI 和 `--serve`，两个进程写同一 session，seq 撞车 | `attach` 之外，`--serve` 启动时对 sessions 目录加锁（复用 `resume_lock` 思路的跨进程版：`flock` 一个 `serve.lock`），第二个实例直接报错并提示用 `attach` |
| **XSS** | agent 输出含 HTML/JS，页面一旦 `innerHTML` 就等于交出执行权 | 见 §4.2；review 时把"有没有 innerHTML"当硬门禁 |
| **非 loopback 暴露** | 有人 `--serve --host 0.0.0.0` 裸奔 | 非 loopback 强制 token（已实现），页面再要求登录换 cookie；启动时打印醒目警告 |
| **页面卡顿** | 一个跑一天的会话有几千条事件，全量渲染会卡 | 历史默认只渲染最近 200 条 + "加载更早"；实时增量按帧追加，单 DOM 节点超过 2000 时折叠最早的 |
| **多项目** | `.agents` 按 cwd 解析，一个进程只服务一个 flow | 页面显示当前 flow 与 cwd；多项目要起多个实例（不同端口），页面不做项目切换 |
| **前端无构建会劣化可维护性** | 原生 JS 到 2000 行就难维护 | 单文件按"数据层 / 渲染层 / SSE 层"分工，超过 1500 行再评估引入构建 |

---

## 十、不做什么

- **云端托管形态**：不 clone GitHub 到远端 VM，不做多租户。agent 必须在用户机器上、看得见未提交改动。
- **在 heron-ai 里写 IM 协议**：那是 connect 的 20.6k 行（13 个平台），搬进来只会让引擎变重。
- **页面里做 diff 审查 / IDE 能力**：属于编辑器插件的职责，页面只负责"看会话、发消息、审批"。
- **为页面单独维护一套数据模型**：列表和历史直接复用 `sessionlist` 的两个函数，页面不重新推导 `Active`/`Tokens`。
- **把 token 放进 URL**：见 §4.4。
- **V1 做 WebSocket**：SSE 已经够（服务端单向推送 + POST 上行），多一条协议只增加状态。

---

## 十一、一句话总结

**页面不是新能力，是已有能力的出口。** 列表、历史、实时流、审批接口都已经在仓库里；缺的是两个只读接口、一份静态资源和一条"谁写 session"的规矩。而这条规矩——一个进程持 runtime，页面与 TUI 都只是它的前端——才是这个设计真正的约束。
