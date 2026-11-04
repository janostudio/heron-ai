# 设计探讨：服务化部署与 Web 页面

> 状态：设计稿，未实现
> 日期：2026-11-02
> 定位：heron-ai 从「前台 CLI 工具」变成「可部署的服务」，页面是它的基本能力

## 一、这份设计解决什么

### 1.1 现状：只能当前台 CLI 用

heron-ai 现在有两种用法：

```
heron-ai                      → TUI，一个进程一个会话流
heron-ai -prompt "..."        → 一次性，跑完退出
```

两种都是**前台进程**：关掉终端就没了，没有页面，没有别人能连的入口。`--serve` 虽然存在，但它被当成"定时唤醒的前置条件"来用（见 `DESIGN-schedule-wakeup` §11），而不是一个正式的部署形态。

要把它当服务用，缺的不是一个页面，是**一整层部署能力**：谁持有执行权、多前端怎么共享状态、后台怎么跑、怎么访问、怎么升级。

### 1.2 已经具备的底座

比想象的完整：

| 能力 | 现状 |
|---|---|
| HTTP 服务 | `--serve`，`--host`（默认 `127.0.0.1`）、`--port`（默认 `8080`） |
| 鉴权 | `--auth-token` 或 `$HERON_SERVE_TOKEN`；非 loopback 监听时强制 Bearer（`requireBearerToken`，`cmd/server/main.go:214`） |
| 优雅退出 | SIGTERM → 停收新请求 → `Shutdown`（上限 10s）→ 关 runtime → exit 0；端口由 `Serve` 返回释放（`cmd/server/serve_lifecycle.go`） |
| 父进程绑定 | `--exit-with-parent`：ppid 变化即自行退出，兜底 `kill -9` |
| 数据落盘 | `.agents/data/`：`sessions/`（`flow.jsonl` / `team.jsonl` / `agent.jsonl`）、`agent-transcripts/`、`logs/` |
| 日志 | 轮转 JSONL，按天分文件、按大小切段、`MaxBackups` 保留（`internal/logging/logging.go:128`） |
| 会话恢复 | 事件流 replay；`/api/recovery` 手动恢复中断回合 |
| 健康检查 | `/api/health` → `{ready, pending_wakeups, sessions_loaded}` |

也就是说：**常驻、鉴权、退出、落盘、恢复、观测都已经有了**。

### 1.3 缺的

| 缺口 | 影响 |
|---|---|
| **页面** | 只能开终端看。没有"别人也能看"的入口 |
| **执行权不唯一** | TUI 自己 `buildCurrentRuntime`（见 §1.4），两个进程会写坏同一个 session |
| **CLI 连不上远端** | `--server` 只支持 stream-json 机器协议，TUI 没法附加到运行中的 serve |
| TLS / 反代支持 | 无 `ListenAndServeTLS`、无 base path、无 `X-Forwarded-*` 处理；只能裸 HTTP 或外部反代且不能改路径 |
| 多实例 | 数据目录按 cwd 的 `.agents` 固定，同目录两个实例会撞 |
| 部署物料 | 没有 systemd unit 示例、没有容器镜像、没有升级/回滚说明 |

### 1.4 第一个约束：执行权必须收敛到 serve

`runTUIWithSession` 在自己的进程里 `buildCurrentRuntime`（`cmd/server/main.go:905`）。而 `JSONLSessionWriter.nextSeq` 是**进程内存**的。

```
heron-ai --serve   → 进程 A，持有 runtime，写 .agents/data/sessions/<id>/flow.jsonl
heron-ai           → 进程 B（TUI），又 build 了一个 runtime，也写同一个文件
```

两个进程各自从 1 开始分配 seq，事件流直接乱掉；页面看到的会话和终端里的会话还会是两份状态。

**所以"打开页面"绝不能是"再起一个进程"，CLI 也必须收敛到那个唯一的执行宿主。** 这是整份设计的地基，不是实现细节。

---

## 二、目标架构：一个执行宿主，多个前端

```
┌──────────────────────────────────────────────────────────┐
│ heron-ai serve（唯一执行宿主，唯一持有 runtime 与写权）  │
│                                                          │
│ Runtime ── WakeupScheduler ── SessionWriter              │
│                                                          │
│ HTTP + SSE   /api/*，机器消费者                          │
│ 静态页面     go:embed 进二进制                           │
└──────────────────────────────────────────────────────────┘

   ↑ TUI 客户端（本机进程内，或 attach 远端）
   ↑ 浏览器（本地 / 局域网 / 反代）
   ↑ 机器消费者（curl / SDK / IM 网关）
```

前端有三个：TUI、浏览器、机器消费者。**它们都是客户端**，都不持有 runtime。

### 2.1 启动方式

| 命令 | 执行宿主 | 前端 | 用途 |
|---|---|---|---|
| `heron-ai` | 本进程（后台起 HTTP） | 进程内 TUI + 浏览器 | 本机日常：终端干活，顺手开页面 |
| `heron-ai --serve --port 8080` | 本进程 | 无（只有 HTTP/SSE） | 部署：systemd / 容器常驻 |
| `heron-ai attach http://host:8080` | 远端 serve | 进程内 TUI | 连远端，本地不起 runtime |
| `heron-ai -prompt "..."` | 本进程（一次性） | 无 | 脚本 / CI，跑完退出 |

`heron-ai` 无参数时**不是在跑另一种模式**：它建一次 bundle，然后用同一份 bundle 起 HTTP server 并跑 TUI。所以进程内 TUI 就是"serve 的一个前端"，Ctrl-C 时两个前端一起退出、不留后台残留。

这需要把当前互斥的两个分支拆开：

```
现在：runTUIWithSession → buildCurrentRuntime → 跑 TUI     （main.go:905）
      startServer       → buildCurrentRuntime → 起 HTTP     （main.go 的 serve 分支）

改成：buildCurrentRuntime → bundle
      ├─ serveRuntime(bundle, host, port, token)   只起 HTTP
      ├─ runTUIOnBundle(bundle)                    只跑 TUI
      └─ 无参数时两者都调用，共享同一个 bundle
```

### 2.2 CLI 进入 serve 的两种形态

用户的诉求是"后续 CLI 执行也进入到存在的 serve 中"，落地上分两种：

| 形态 | 谁持 runtime | 何时用 |
|---|---|---|
| **同进程双前端** | 本进程 | 本机单用户。TUI 和页面共享内存里的同一份 runtime，零延迟 |
| **attach 远端** | 远端 serve | 远程开发、多人看同一个会话、页面在浏览器而终端在另一台机器 |

两者对 `flow.jsonl` 都只有一个写者。区别只是"TUI 与 HTTP 是不是同一个进程"。

---

## 三、部署形态

### 3.1 单机常驻（systemd）

```ini
[Unit]
Description=heron-ai
After=network.target

[Service]
Type=simple
WorkingDirectory=/srv/project          # .agents/ 所在目录
ExecStart=/usr/local/bin/heron-ai --serve --host 127.0.0.1 --port 8080
Environment=HERON_SERVE_TOKEN=...
Restart=always
RestartSec=2
KillSignal=SIGTERM
TimeoutStopSec=15                      # 引擎自己有 10s drain 上限

[Install]
WantedBy=multi-user.target
```

要点：
- `KillSignal=SIGTERM` + `TimeoutStopSec` 略大于引擎的 10s，让优雅关闭先跑完，systemd 的 SIGKILL 只作兜底；
- 监听 `127.0.0.1`，由 nginx/caddy 反代对外（§3.3）；
- `Restart=always` 之后，重启期间未触发的定时唤醒会由启动时的 `RecoverWakeups` 捡回来（事件流是事实源）。

### 3.2 容器

```dockerfile
FROM golang:1.24 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/heron-ai ./cmd/server

FROM gcr.io/distroless/static
COPY --from=build /out/heron-ai /usr/local/bin/heron-ai
WORKDIR /work
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/heron-ai", "--serve", "--host", "0.0.0.0", "--port", "8080"]
```

要点：
- 数据目录挂卷：`-v heron-data:/work/.agents/data`，否则容器重建会话全丢；
- 容器内必须 `--host 0.0.0.0`，**因此必须给 token**（非 loopback 强制鉴权）；
- 单副本。多副本会各自写同一份卷，就是 §1.4 的双写问题。

### 3.3 反代与 TLS

现状不支持 TLS 与 base path，建议保持"引擎只说 HTTP，TLS 交给反代"：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_buffering off;            # SSE 必须：否则事件被缓冲，页面看起来"卡住"
    proxy_read_timeout 3600s;       # 长连
}
```

`proxy_buffering off` 是最容易漏的一条：开着缓冲时 `/api/stream` 的事件会攒在 nginx 里，页面表现为"很久不动，然后一次刷出一堆"。

**不挂在子路径下**（`/heron/`）：页面和 API 都用了绝对路径，没有 base path 支持。要做的话是独立改动（统一的 `--base-path` + 前端相对路径化），V1 不做。

### 3.4 多实例与数据目录

数据目录固定为 cwd 下的 `.agents/data`，所以"多实例"只能是"多目录"：

```
/srv/proj-a/.agents/data   → 实例 A（端口 8081）
/srv/proj-b/.agents/data   → 实例 B（端口 8082）
```

同一目录起第二个实例必须失败，不能静默双写：启动时对 `.agents/data/serve.lock` 做 `flock`，抢不到就报错并提示"已有实例在跑，用 attach 连它"。

### 3.5 升级与回滚

| 步骤 | 说明 |
|---|---|
| 停旧 | SIGTERM，等它把当前回合写完（最长 10s） |
| 换二进制 | 数据全在 `.agents/data`，二进制可随时替换 |
| 起新 | 启动时 `RecoverWakeups` 把未触发的定时唤醒捡回来；中断的回合靠 `/api/recovery` 或下次进入会话时恢复 |
| 回滚 | 换回旧二进制即可；事件流格式向后兼容（新增事件类型，旧版本按未知事件忽略） |

升级窗口会打断正在跑的回合，这是可接受代价；不可接受的是"升级后会话消失"——那不会发生，因为状态不在内存里。

### 3.6 观测

| 看什么 | 怎么看 |
|---|---|
| 活着、欠多少定时器 | `GET /api/health` → `pending_wakeups` |
| 执行日志 | `.agents/data/logs/YYYY-MM-DD.log`（JSONL，按大小切段） |
| 某会话发生了什么 | `GET /api/stream?session_id=...&follow=1`，或直接读 `flow.jsonl` |
| 页面 | `/`，含状态页 `/status` |

---

## 四、Web 页面

### 4.1 技术选型：无 Node 构建，`go:embed` 内嵌

| 方案 | 代价 |
|---|---|
| **原生 HTML + CSS + 少量 JS，`go:embed` 进二进制**（选它） | 无前端工具链，`go build` 就是全部；页面复杂度上限低 |
| SPA（React/Preact + 构建产物 embed） | 需要 Node 与构建步骤，CI 变复杂；对"列表 + 时间线 + 审批"这类界面收益有限 |

形态是「列表 + 时间线 + 少量交互」，不是表单密集型应用。将来要做复杂视图（diff 比对、看板）可升级成 SPA，`go:embed` 的目录换掉即可，接口不变。

### 4.2 安全：禁止 `innerHTML`

agent 的输出包含任意文本、代码块，有时是模型生成的 HTML/JS。而这个页面背后是一个**能跑 Bash、能写文件的 agent**——XSS 等于把执行权交给网页里的任意脚本。

- 一律 `textContent` 或转义后插入；
- markdown 只渲染白名单子集，链接强制 `rel="noopener noreferrer"` 且只允许 `http/https`；
- CSP：`default-src 'self'; script-src 'self'; connect-src 'self'`，不写内联 `<script>`；
- review 时把"有没有 innerHTML"当硬门禁。

### 4.3 鉴权：默认 loopback 免 token，对外强制登录

浏览器加不了 `Authorization` 头。做法：

- 监听 loopback（默认）时不需要 token，页面直接可用；
- 监听 `0.0.0.0` 时**必须** token：`POST /api/login` 校验后换 `HttpOnly; SameSite=Strict` cookie，之后 `/api/*` 同时接受 Bearer 与 cookie；
- cookie 模式下写操作（发消息、审批、取消）带 `X-Requested-With` 做 CSRF 双提交校验；
- **不把 token 放 URL**（会进浏览器历史、Referer、代理日志）。

### 4.4 实时：复用 `/api/stream?follow=1`

不新造通道。turn 开始/结束、工具调用、delivery、wakeup 事件都在同一条流上，`Last-Event-ID` 支持续传（刷新/断网重连不丢）。

页面侧连接策略：先 `/api/sessions/{id}/history` 拉历史并记住最大 `seq`，再带 `Last-Event-ID` 连流只收增量；断线退避 1s → 30s。

**帧形状**（真机验证过的坑，见 `DESIGN-schedule-wakeup` §11.10）：`data:` 里是**扁平事件头 + 嵌套 payload**；`seq/type/flow_turn_id/created_at` 在顶层，`origin/reply/intermediate/deliveries/contributors` 在 `payload` 内。把 `flow_turn_id` 当 payload 字段读会拿到空串。

### 4.5 页面清单

**会话列表 `/`**

```
┌──────────────────────────────────────────────────────────┐
│ heron-ai    ● 待处理 2 个定时任务                        │
│                                                          │
│ ● 修复镜像构建失败                            │
│   3 轮 · 12.4k tok · 3 分钟前 · 定时检查 5 分钟后        │
│   （有未读主动汇报）                                     │
│ ○ 升级 workspace 模板                                    │
│   7 轮 · 28.1k tok · 1 小时前                            │
│ ○ 调研 ACP 协议                                          │
│   运行中 ●                                               │
└──────────────────────────────────────────────────────────┘
```

字段来自 `sessionlist.Session`：`Title / Turns / Tokens / UpdatedAt / Active`；未读徽标是页面本地状态。

**会话详情 `/s/{id}`**

```
┌──────────────────────────────────────────────────────────┐
│ ← 修复镜像构建失败    等待输入 · 5 分钟后定时 │
│                                                          │
│ 你 04:12                                                 │
│   升级沙箱镜像到最新的基础层                             │
│                                                          │
│ 助手 04:12  [已转给 team-sandbox]   ← intermediate，灰色 │
│                                                          │
│ ⏰ 定时检查 06:12                  ← origin=wakeup       │
│   下游已跑 2 小时，无新增产出，已催一次                  │
│   ▸ 3 条交付物                                           │
│                                                          │
│ [输入框]                [发送]  [取消当前回合]           │
└──────────────────────────────────────────────────────────┘
```

- 工具调用折成一行（`Bash (make build)` + 成败 + 耗时），点开看输出；
- `deliveries` 单独成块并标 `team_id`；
- `pending_approvals` 时输入框上方出现审批条；
- 标题栏显示当前 `origin=wakeup` 的下次触发时间。

**定时任务面板 `/wakeups`**：会话、登记时间、计划触发、状态（`scheduled/fired/skipped/expired`）、**skipped 的原因**（引擎已给出具体门禁）、note 原文。

**状态页 `/status`**：`/api/health` 内容 + 会话数 + 最近 20 条事件。

**追踪页 `/s/{id}/trace`**：执行树 + 节点详情（见 §4.8）。

**事件流页 `/s/{id}/events`**：全量事件的原始列表，按层/类型/失败过滤，可导出 JSONL。

### 4.6 主动回合必须显眼

这是页面与其他 agent 界面的产品差异：别的页面是「你问它答」，这里的页面要回答「**它背着你做了什么**」。

三处体现：时间线里 `origin=wakeup` 的回合用不同底色 + 标识 + 「定时检查（你不在时）」；定时任务面板显示下次何时醒、上次为什么被推延；列表页标出"有一条你还没看过的主动汇报"。

### 4.7 数据不在页面里聚合

列表的 `Active` 来自 `sessionlist` 按 `flow_turn.*` 事件的派生判断（不读 `session.status`——它在 `flow.jsonl` 里停在 `running` 不回写）。页面只显示，不重新推导。

### 4.8 会话记录：一份数据，三种呈现

引擎那边本来就是**一份只增不改的记录**：一个 `sessionID` 目录、三层 jsonl（flow/team/agent）、`seq` 全局递增、`EventHeader` 带完整 scope 链。`eval.SessionFacts` 的注释就写着「the merged three-layer timeline」。

所以界面不该把它切开。上一版拆成三个视图、三个接口（history / trace / events），等于在界面层复制了三份索引——用户想知道「第 8 条事件前后发生了什么」，却要在三个页面之间跳。

正确形态：**一份记录 → 一个接口 → 一个页面 → 三种渲染**。

| 呈现 | 看什么 | 切换时保留 |
|---|---|---|
| 对话 | 人能读的消息流（11 类事件） | 同一份事件数组、当前选中 seq、按 seq 还原的滚动位置 |
| 结构 | 执行树 + 节点详情（`eval.NewGraph`） | 同上 |
| 原始 | 合并后的完整事件表（47 类，含 payload） | 同上 |

顶部常驻的还有：

- **KPI**：回合、步骤、工具调用（含失败）、总耗时、token、压缩次数、模型；
- **时间轴 minimap**：色带按真实耗时占比，颜色区分用户/步骤/工具/压缩/错误/重试/定时检查，点色块跳站点；
- **回放**：`◀` `▶` 逐站、1x/2x/4x 播放、`/` 搜索、导出 Markdown 与 JSONL。

**三级粒度，因为数据量差四个数量级**：

| 粒度 | 一次多少 | 策略 |
|---|---|---|
| 树骨架 `nodes[]` | 几十~几百个节点（只跟回合/Team/Call 有关，跟轮数无关） | **全量返回**，可见性用折叠控制；深链 `?node=` 因此一定能定位 |
| 节点事件 `events[]` | 一个 Call 几十~几百条，全会话上万条 | **按节点区间** `first_seq..last_seq` 分页，默认 50 条 |
| 单条 payload | 几百字节 ~ 几 MB | **按需**，默认 512 字节摘要，点开单条才取 |

量级参照：一次问答约 10 条事件；一次构建排查约 180；跑一天的长任务约 7000（20 FlowTurn / 60 TeamTurn / 90 Call / 600 round）。

规模上限来自 `RuntimeLimits`，但三个数的计量窗口不同，不能笼统写「上限 100」：

| 限制 | 默认 | 计量窗口 | 超限后果 |
|---|---|---|---|
| `max_team_turns` | 100 | **按单个 FlowTurn**；会话级对应项已被刻意删除 | 该回合失败，会话可开始新回合 |
| `max_calls_per_team_turn` | 100 | 单个 TeamTurn | 不失败，转 `coordinate` 交激活方决策 |
| `max_agent_rounds` | 200 | 单个 AgentTurn（与 `loop.max_rounds`、`budget.max_model_rounds` 取最小） | 回合结束（截断） |

为什么没有会话级上限：曾有一条「会话累计 TeamTurn 数」的检查，但 mailbox 里跑的只有用户排队输入，这条限制实际等于「一个会话里用户能聊多少轮」——聊够 100 轮后不再接受输入。防 runaway 交给单回合上限与「同一对 Team 反复派活」的计数。

所以：树的节点数受前两者约束、量级可控；事件数还要乘轮数、量级不可控——这正是分层的依据。

**展开规则**：默认只展开到 FlowTurn；round 层（可能几十个）必须主动点；折叠态与选中节点记在 URL（`?open=ft_x,tt_y`）；节点状态点表示整棵子树的终态，折叠时也能看出哪支有问题。事件列表里连续 5 条以上同类事件折成一行（`tool_call.completed ×12`），失败工具默认展开。

**原始请求面板**是回溯里最有用的一块：`AgentTranscript` 已存在 `.agents/data/agent-transcripts/`，按 `flow_session_id + team_id + call_id + agent_id` 分文件，含累积的 `Messages` 与 `SystemHash`。展开任一 `agent.model_response` 能看到该轮的消息前缀、系统提示版本、工具目录、token 明细。

**一个精度限制**：transcript 是累积列表，不是每次请求的快照。要精确复现某一轮，需按 `agent_turn_id` 截取前缀并重建系统提示。后续可在 `agent.model_response` 里补一条 `request_digest`（message 数、system hash、工具名列表、token），成本低但能让重建免于猜测。

界面细节（三种呈现的布局、时间轴、节点详情字段、加载策略）见 HTML 版 §14。

---

## 五、API 缺口

| 接口 | 方法 | 数据源 | 说明 |
|---|---|---|---|
| `/api/sessions` | GET | `sessionlist.List`（`list.go:47`） | 列表，支持 `?active=1`、`?limit=` |
| `/api/sessions/{id}/history` | GET | `sessionlist.History`（`history.go:47`） | 历史，支持 `?after_seq=` |
| `/api/sessions/{id}/record` | GET | `storage.Replay` + `eval.NewGraph` + `eval.SessionFacts` | **唯一的记录接口**：合并三层事件 + 执行树 + KPI。分页 `?after_seq=&limit=`，payload 按需 `?include_payload=seq:7`；增量走 `/api/stream?follow=1` |
| `/api/wakeups` | GET | runtime 的 wakeup index | 支持 `?session_id=`、`?status=` |
| `/api/wakeups/{id}` | DELETE | `Runtime.CancelWakeup` | 页面取消定时 |
| `/api/login` | POST | token 校验 | 换 cookie（仅非 loopback） |
| `/`、`/s/{id}`、`/static/*` | GET | `go:embed` | 未命中的一律 404，不退化成文件系统读 |

`/api/wakeups` 需要一个只读访问器：现在 `Runtime.WakeupStats` 只返回计数，要补 `Wakeups(ctx, sessionID) []types.WakeupRecord`。

---

## 六、与 heron-connect 的关系

**heron-ai 不依赖 connect，页面也不经过 connect。**

| 场景 | 走什么 |
|---|---|
| 本地看进度、审批、发消息 | 页面（`/api/*` + SSE） |
| 后台常驻 + 远程看 | `--serve` + 页面（反代 + token） |
| 主动汇报到 IM | delivery 出口（webhook），或 connect 作为可选网关 |
| 在 IM 里对话 | connect（或任意网关）调 `/api/sessions/turn` |

connect 的价值收窄为「IM 平台接入 + 多用户/权限/展示」，不再是"让 agent 能被用起来"的必经环节。

---

## 七、阶段与验收

| 阶段 | 内容 | 验收（可复现） |
|---|---|---|
| **P0 只读可看** | 静态托管 + `/api/sessions` + `/api/sessions/{id}/history` + 列表页 + 详情页（历史 + SSE 实时） | `--serve` 起一个会话并让它跑，浏览器打开：会话出现在列表，点进去能看到历史与**正在产生**的输出（工具调用逐条出现） |
| **P1b 会话记录** | `/api/sessions/{id}/record`；记录页三呈现（对话/结构/原始）；KPI | 打开一个跑完的会话，三种呈现之间切换不重新请求；KPI 与 `eval.SessionFacts` 对得上 |
| **P2 时间轴** | minimap + 逐站 + 播放 + 搜索 + 原始请求面板 | 点色块跳到站点；播放按 1x 走完；展开任一 `model_response` 能看到该轮消息前缀与系统提示版本 |
| **P1 能操作** | 发消息、审批、取消；输入框与审批条 | 页面发一句 → CLI 侧同一会话看到；触发审批时页面出现审批条，允许后会话继续 |
| **P2 主动能力可见** | `/api/wakeups` + 取消；定时任务面板；`origin=wakeup` 样式与未读 | 登记 2 分钟唤醒 → 页面显示"2 分钟后"→ 到点出现带标识的检查回合，列表页出现未读徽标 |
| **P3 执行权收敛** | 拆 `buildCurrentRuntime` 与"起 HTTP / 跑 TUI"；无参数时同进程双前端；新增 `attach` | 两个终端（`--serve` + `attach`）共享一个引擎，页面实时同步；`flow.jsonl` 的 seq 连续无重复；Ctrl-C 后端口释放 |
| **P4 部署物料** | `serve.lock` 单实例保护、systemd unit、Dockerfile、反代示例、升级说明 | 按文档部署一遍：常驻、反代后页面可用、`kill -9` 后 systemd 拉起且定时器被 recover |
| **P5 外发** | delivery webhook（webhook / exec 两种 target） | 本地 echo 服务当 target，唤醒结论被 POST 出去，含 `turn_id/origin/deliveries` |

P0 单独可用：只读页面已经解决"看不到 agent 在干什么"。P3 动 CLI 主路径，风险最高，但它是"CLI 进入 serve"这条诉求的实质内容，不能省。

---

## 八、风险

| 风险 | 内容 | 应对 |
|---|---|---|
| **双写** | 有人仍同时开 TUI 和 `--serve`，两个进程写同一 session，seq 撞车 | P3 收敛后同进程；P4 加 `.agents/data/serve.lock` 的 `flock`，第二个实例直接报错并提示用 `attach` |
| **XSS** | agent 输出含 HTML/JS，一旦 `innerHTML` 就等于交出执行权 | §4.2；review 门禁 |
| **非 loopback 暴露** | `--serve --host 0.0.0.0` 裸奔 | 非 loopback 强制 token（已实现）；页面再要求登录换 cookie；启动时打印醒目警告 |
| **SSE 被反代缓冲** | 页面"卡住不动，然后一次刷出" | 部署文档写死 `proxy_buffering off`；`/status` 页显示最近事件时间戳，便于判断是否被缓冲 |
| **渲染卡顿** | 跑一天的会话几千条事件，全量渲染会卡 | 历史默认最近 200 条 + "加载更早"；单 DOM 节点超 2000 时折叠最早的 |
| **多项目** | `.agents` 按 cwd 解析，一个进程只服务一个 flow | 页面显示当前 flow 与 cwd；多项目 = 多实例（不同端口/目录），页面不做项目切换 |
| **前端可维护性** | 原生 JS 到 2000 行就难维护 | 单文件按"数据层 / 渲染层 / SSE 层"分工；超 1500 行再评估引入构建 |

---

## 九、不做什么

- **云端托管形态**：不 clone GitHub 到远端 VM，不做多租户。agent 必须在用户机器上、看得见未提交改动。
- **同一数据目录跑多副本**：单写者是有意约束，不是待优化项。
- **在 heron-ai 里写 IM 协议**：那是 connect 的 20.6k 行（13 个平台），搬进来只会让引擎变重。
- **V1 支持 base path 与 TLS**：TLS 交给反代；子路径部署需要前后端一起改路径，独立评估。
- **页面里做 diff 审查 / IDE 能力**：属于编辑器插件职责，页面只负责"看会话、发消息、审批"。
- **为页面单独维护一套数据模型**：列表和历史直接复用 `sessionlist`。
- **V1 上 WebSocket**：SSE + POST 已经够，多一条协议只增加状态。

---

## 十、一句话总结

**这不是"给引擎加个页面"，是把 heron-ai 变成一个可部署的服务。** 服务化的三个要件——常驻与优雅退出、唯一的执行权、可访问的入口——前两个仓库里已经七成具备，缺的是入口（页面）和一条规矩（**执行只在 serve 里发生，CLI 与浏览器都只是它的前端**）。
