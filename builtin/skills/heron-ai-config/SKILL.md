---
name: heron-ai-config
description: "编写、运维与排查 heron-ai 配置（Agent/Team/Flow/models/settings/rules/knowledge/skill），含字段速查、启停脚本与运行排查。当需要新增或修改 .agents/ 下的配置、启动/关闭 heron 服务、或某次运行的结果不符合预期时使用"
tools:
  - Read
  - Write
  - Glob
  - Grep
  - Bash
scripts:
  - scripts/build.sh
  - scripts/serve.sh
---

# heron-ai 配置与运维

你在为一个基于 heron-ai 多 Agent 引擎的项目编写或修改配置，执行下载/启动/关闭等
运维操作，或排查一次运行的问题。

## 怎么用这个 skill

正文是入口与高频结论；细节在 `references/` 下，**按需只读需要的那份**（用 `Read`
打开 `<本 skill 目录>/references/<name>.md`）。不要一次全读——每份都是独立主题。

| 你现在的处境 | 读哪份 |
|---|---|
| 要写/改配置，但不确定某个字段叫什么、什么类型、默认值多少 | `references/fields.md` |
| 配置加载报错，或怀疑某个字段根本没生效 | `references/fields.md`（末尾有"不生效的段"清单） |
| 要装/启动/关闭服务，或不确定某个 CLI 参数 | `references/operations.md` |
| 跑完了但结果不对、卡住、报错、答非所问 | `references/debug-triage.md`（排查入口） |
| 要确认聊天记录/工具调用/事件落在哪 | `references/session-files.md` |
| 要算 token、看缓存命中、定位上下文膨胀 | `references/token-usage.md` |
| 要把事件串成一条链路（谁触发谁） | `references/trace-ids.md` |
| 想让 Agent 在对话里自建 Agent/Team | `references/define.md` |
| 要理解目录组织与三层编排为什么这样设计 | `references/organization.md` |

## 改完配置先自检（比跑一轮快）

```bash
heron --check-config --flow .agents/flows/default.yml
```

只做加载与校验，不建模型客户端、不产生会话，秒级返回；退出码 0 = 合法。
它拦得住：agent 名对不上、call 的 key 与 `id` 不一致、`depends_on` 未知或成环、
引用了没人生产的 record、`${records.X}` 的 X 没写进同一 call 的 `inputs`、
`per_task` 绑定缺 `inputs.user_message: true`、entry team 写了 `per_task`、
skill 的 `scripts` 路径不存在或跑到 skill 目录外。
它拦不住：模型名、API key、MCP 连接、工具被策略拒绝——它**不读 models.json**。
所以顺序是「先 `--check-config`，再 `--prompt` 试一轮」。

## 排查一次运行（高频，先看这里）

**定位会话**：数据在工作区根（heron 启动的 cwd）的 `.agents/data/sessions/<flow_session_id>/`。
一次 `--prompt` 或 `--session` 续聊对应一个目录，取最新的：

```bash
ls -1dt .agents/data/sessions/fs_* | head -5    # 按 mtime 倒序
S=.agents/data/sessions/<fs_id>                  # 下文命令沿用这个变量
```

> 跨平台：`jq` 三平台一致（`apt/brew/winget install jq`）；示例里的 `$S`、
> `ls -t`、`head` 属 POSIX shell，Windows 请用 Git Bash / WSL，或把
> `$S/xxx.jsonl` 换成完整路径。不想装 jq 时用 `references/token-usage.md` 里的 node 写法。

**聊天记录在哪看**（用户问了什么 / 模型答了什么）——对话不在一份"聊天文件"里，按层分散：

| 你要看的 | 位置 | 字段 |
|---|---|---|
| 用户每一轮的提问 | `flow.jsonl` → `session.turn.started` 且 `payload.turn.origin=="user"` | `payload.turn.input` |
| 模型每轮原始回复 | `agent.jsonl` → `session.agent.model_response` | **`payload.text`**、`payload.round` |
| 这一轮对用户答了什么、谁写的 | `flow.jsonl` → `flow_turn` 终态 | `payload.reply`、`payload.team`（`intermediate=true` 时没有答复） |
| 成品文本与依据 | `evidence.jsonl` 的 SharedRecord | `summary`、`basis[]` |

```bash
jq -r 'select(.type=="session.turn.started" and .payload.turn.origin=="user") | .payload.turn.input' $S/flow.jsonl
jq -r 'select(.type=="session.agent.model_response") | "[round \(.payload.round)] \(.payload.text)"' $S/agent.jsonl
```

**三份 jsonl 按 run 层级分流，不是同级混放**——这是排查时最容易错的一点：

| 文件 | 只包含 |
|---|---|
| `flow.jsonl` | flow 会话级 + **team turn 级**（`session.team_turn.*` 在这里），以及 `session.mailbox.*` / `session.wakeup.*` / `session.record.*` |
| `team.jsonl` | **call 层**：`session.agent_turn.*`、`session.command_turn.*`、`session.webhook_turn.*`、`session.approval.*`（**消耗在这里**） |
| `agent.jsonl` | agent 内部：`session.agent.model_response`、`session.tool_call.*`、`session.agent.feedback`、`session.context.compacted`（回复原文在 **`payload.text`**） |

所以：在 `team.jsonl` 里找不到 `session.team_turn.ended` 是正常的，不是缺数据。
同一 `fs_id` 目录**追加式**记录，续聊会继续往里写，所以 `session.turn.started` 会出现多次，
每次对应一轮。

**快速体检**（轮次 / 路由 / 失败 / 终态 / 消耗）：

```bash
S=.agents/data/sessions/<fs_id>
jq -r 'select(.type=="session.team_turn.ended") | .payload.team_result.turn
       | "\(.team_id) \(.status) next=\(.next.action)"' $S/flow.jsonl
jq -r 'select(.type=="session.agent_turn.completed") | .payload.call_result
       | "\(.agent_id) 轮次=\(.requests|length) tokens=\([.requests[].usage.prompt_tokens]|add)"' $S/team.jsonl
jq -r 'select(.type|startswith("session.turn.")) | .type' $S/flow.jsonl
```

> 字段名统一 snake_case，`team_result` / `call_result` 这一层也是（`turn` / `reply` /
> `records` / `call_results`，再往里是 `next` / `team_id`）。Go 代码里的标识符是大驼峰，
> 但那只影响代码，不影响落盘格式。写错取到 `null`，不报错。

**排查时的五个高频陷阱**（按当前实现核对，每条都能在代码里找到生产者）：

1. **分层**：`session.team_turn.*` 在 `flow.jsonl`；`session.command_turn.*` 在 `team.jsonl`。搜不到不等于缺数据。
2. **字段名**：模型回复原文是 `session.agent.model_response` 的 **`payload.text`**，不是 `content`。
3. **终态只有一个事件 `session.turn.ended`**：`payload.stop_reason` 说怎么结束（`end_turn` / `failed` /
   `cancelled` / `max_rounds` …），`payload.awaiting` 说在等谁。旧事件名（`.completed` / `.idle` /
   `.waiting_*`）已移除，不再发出。
4. **`turn.started` 不等于一次提问**：一条用户消息会派生多个 turn（派发、工具唤醒、自唤醒各一轮），
   只有 `payload.turn.origin=="user"` 才是人问的；其余是引擎自己接着跑。
5. **`round` 是 turn 内局部序号**，每轮从 0 重数（从检查点续跑例外）；用 `team_turn_id` 区分是否同一轮，
   不要因 `round` 重复就以为数据重复。子 agent 回合复用父的 `call_turn_id`，要靠 `payload.spawn` 区分。

**`text` 为空不是数据丢失**：纯工具调用轮（`finish_reason=tool_calls`）、
结构化重试被丢弃的轮、输出已解析进 `evidence.jsonl` 的轮——都是正常情况。
用 `completion_tokens` 区分"模型输出异常"与"schema 定义过严"，手法见
`references/debug-triage.md`。

判定标准、异常模式对照、配置对照见 `references/debug-triage.md`。

**Token 消耗统计**：终端 `Tokens: N (prompt X, completion Y)` 是本次 FlowTurn 总额，
等于各 `session.team_turn.ended` 的 `payload.team_result.usage` 之和
（team 层已含本 TeamTurn 执行过的 call，**不要**再与 call 层相加；缓存命中信息也在里面）。
按 team / agent 拆分、缓存命中率口径见 `references/token-usage.md`。

```bash
# 总计（与终端 Tokens: 行同口径）
jq -s '[.[]|select(.type=="session.team_turn.ended")|.payload.team_result.usage.total_tokens]|add' $S/flow.jsonl

# 按 agent 汇总（agent 粒度在 call 层）
jq -r -s '[.[]|select(.type=="session.agent_turn.completed")]|group_by(.payload.call_result.agent_id)
          |map({a:.[0].payload.call_result.agent_id,total:([.[].payload.call_result.usage.total_tokens]|add)})
          |.[]|"\(.a)\ttotal=\(.total)"' $S/team.jsonl
```

`prompt ≫ completion` 是常态（上下文每轮重发），判定"贵不贵"要看比值而非 total。
完整算式、缓存字段（取大值不相加）与 node 写法见 `references/token-usage.md`。

## 参考资料（先按需读取）

排查相关按用途拆成 4 个文件，**按需只读需要的那份**：

| 参考文件 | 什么时候读 | 内容 |
|---|---|---|
| `references/debug-triage.md` | **排查入口** | 常用命令速查、判定标准、异常模式对照、从配置预测行为、排查流程 |
| `references/session-files.md` | 看数据在哪 | 数据落点、三层 jsonl 各装什么、聊天记录在哪、执行日志 |
| `references/token-usage.md` | 看消耗 | 按 flow / team / agent / 轮次统计 token，缓存命中率 |
| `references/trace-ids.md` | 串链路 | 每类事件带哪些 ID（串联地图）、正向/反向查询 |

配置与运维：

| 参考文件 | 内容 |
|---|---|
| `references/fields.md` | **每个配置字段的含义**（Flow 绑定含 `lifecycle`、Team、Call、Agent 的 `loop`/`context`/`completion`、record、rules、**workspace**，以及 **models.json / settings.json 的生效段与不生效段**）+ 运行时事件字段 + **`${records.*}` 模板**（把共享记录传进 command） |
| `references/organization.md` | 目录组织、三层编排、设计优势 |
| `references/operations.md` | 全部 CLI 参数与子命令、下载/启动/关闭、Web UI 入口、配置自检范围 |
| `references/define.md` | **Define 工具**：让 Agent 在对话中自建 Agent/Team 定义 |

读取方式：用 `Read` 打开 `references/<name>.md`（本 skill 目录下）。

## 脚本

| 脚本 | 用法 |
|---|---|
| `scripts/build.sh` | `bash build.sh [输出路径]` 从源码构建二进制（自动定位仓库根，可用 `HERON_REPO` 指定） |
| `scripts/serve.sh` | `bash serve.sh {start\|stop\|status} [--flow <path>] [--mode http\|jsonrpc] [--port <port>]` 后台启停服务（PID 管理） |

两个脚本都**在项目根目录运行**（含 `.agents/` 的那一层）：heron 按 cwd 找配置，
`serve.sh` 的 PID 与日志也落在 cwd。

## 核心要点

1. 三层编排：`Flow → Team → Call → Agent/Command/Webhook`，`Call` 是执行项不是层级。
2. Team 之间平权：没有 coordinator，只有 `entry` 指定的入口；一个 Team 能激活谁由绑定的
   `can_activate` 决定，结果回到激活它的 Team。老配置里的 `coordinator` 键会让加载失败。
3. 配置全部在 `.agents/` 目录下，按类型分目录；`--check-config` 以 cwd 的 `.agents` 为根。
4. Agent 的 `model.model` 写 models.json 里 profile 的 **`name`**（不是 `id`），
   **勿用 `${ENV}` 占位符**（那里不展开）；`${ENV}` 展开只发生在 models.json 的 `api_key` 等字段。
5. record 靠 `output.record` + 下游 `inputs` 的 `{from, record}` 按名精确匹配，
   名称是自由字符串；拼错在**加载期**就报"没有生产者"，不会拖到运行时。
   被派发的子 Team 产出的记录会以 flow 作用域副本落进 `evidence.jsonl`，调用方按名就能读到。
6. command call 不读对话内容；要把上游记录的值传给它，用 `command.env` / `command.args` 的
   `${records.<Name>.Data.<field>}` 模板（仅在 `Data` 顶层，字段缺失会让 call 失败）。
   详见 `references/fields.md`。
7. `models.json` 含 api_key，永不提交；`maxAllowedSize` 之类的无效字段不会报错也不会生效，
   以 `references/fields.md` 的生效清单为准。
8. Agent 要读工作区根之外的目录（如仓库外的代码库），在 `workspace.read_roots`
   里列出来：读取放行、写入仍拒绝。相对路径按**进程 cwd** 解析，换部署位置会失效
   （失效条目被跳过，不会导致启动失败）。详见 `references/fields.md` 的 workspace 节。
9. 想让 Agent 在对话中自建 Agent/Team，需在其 `AGENT.md` 里声明
   `tools.builtin: [..., Define]`；未声明则该工具对模型不可见。
   详见 `references/define.md`。
10. Agent 声明了 `Team` 或 `Spawn` 时不必再手写 `Collect`——引擎会自动授予并暴露
    schema。反过来只声明 `Team` 而不给 `Collect`，历史上会造成"模型反复调一个被
    拒绝的工具直到轮次耗尽"。
11. `settings.json` 里**真正被读的段**只有 `logging` / `runtime` / `knowledge` /
    `web` / `mcp` / `tools` / `env`。`paths`、`agent`、`observability` 没有任何代码
    读取 —— 在里面调参数不会有任何效果。`tools` 段下是 `restricted_bash_phrases` 与
    `repl`（不是 `script`）。详细字段与不生效清单见 `references/fields.md`。
12. Team 分享包装在 `.agents/_teams/<scope>/`，由 `heron teams` 管理；手写的
    `heron-team.yml` 需要 `apiVersion: heron/v1` 与 `kind: team`。装包前先
    `--dry-run` 看解析结果，`--yes` 会跳过 skill 脚本复核（等于让它跑别人的 shell）。
13. **skill 是"索引常驻、正文按需读"**：Agent 声明了某个 skill，system prompt 里只出现
    一行索引（名字 + `description` + `SKILL.md` 的路径），正文不进 prompt；模型判断任务
    匹配时才用 `Read` 打开那个路径。所以正文改了**立即生效**（不像 rule 那样按轮读、
    也不像早期版本那样要重启），代价是模型多一次读文件的调用。但 skill 的 `tools` /
    `allowed-tools` 仍然是**声明即授予**——工具会在上下文里常驻，只声明真正需要的。
    （没有 `Path` 的 skill 例外：无处可读时引擎退回内联正文，否则等于静默丢能力。）

## 修改配置的原则

1. 修改前先 `Read` 目标文件，保留已有字段与结构。
2. 只改必要的最小差异，不重构无关配置。
3. 新增 agent/team 时，确保 model、tools、call 引用的 agent 名、record 名前后一致。
4. 修改后运行 `heron --check-config --flow <flow>` 确认无 validate 错误。
