---
name: heron-ai-config
description: "编写、运维与排查 heron-ai 配置（Agent/Team/Flow/models/settings/rules/knowledge/skill），含字段速查、启停脚本与运行排查"
tools:
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Bash
scripts:
  - scripts/build.sh
  - scripts/serve.sh
---

# heron-ai 配置与运维

你在为一个基于 heron-ai 多 Agent 引擎的项目编写或修改配置，执行
下载/启动/关闭等运维操作，或排查一次运行的问题。

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
| 用户每一轮的提问 | `flow.jsonl` → `flow_turn.started` | `payload.turn.input` |
| 模型每轮原始回复 | `agent.jsonl` → `agent.model_response` | **`payload.text`**、`payload.round` |
| 最终对用户的答复 | `evidence.jsonl` 的 SharedRecord | `summary` |

```bash
jq -r 'select(.type=="flow_turn.started") | .payload.turn.input' $S/flow.jsonl
jq -r 'select(.type=="agent.model_response") | "[round \(.payload.round)] \(.payload.text)"' $S/agent.jsonl
```

**三份 jsonl 按 run 层级分流，不是同级混放**——这是排查时最容易错的一点：

| 文件 | 只包含 |
|---|---|
| `flow.jsonl` | flow 会话级 + **team turn 级**（`team_turn.*` 在这里） |
| `team.jsonl` | **call 层**：`agent_turn.*`、`command_turn.*`（**消耗在这里**） |
| `agent.jsonl` | agent 内部：`agent.model_response`、`tool_call.*`（回复原文在 **`payload.text`**） |

所以：在 `team.jsonl` 里找不到 `team_turn.completed` 是正常的，不是缺数据。
同一 `fs_id` 目录**追加式**记录，续聊会继续往里写，所以 `flow_turn.started` 会出现多次，每次对应一轮。

**快速体检**（轮次 / 路由 / 失败 / 终态 / 消耗）：

```bash
S=.agents/data/sessions/<fs_id>
jq -r 'select(.type=="team_turn.completed") | .payload.team_result.Turn
       | "\(.team_id) \(.status) next=\(.next.action)"' $S/flow.jsonl
jq -r 'select(.type=="agent_turn.completed") | .payload.call_result
       | "\(.AgentID) 轮次=\(.Requests|length) tokens=\([.Requests[].usage.prompt_tokens]|add)"' $S/team.jsonl
jq -r 'select(.type|startswith("flow_turn.")) | .type' $S/flow.jsonl
```

> 字段大小写要留意：`payload.team_result.Turn` 里是**小写** `next` / `record_ids`
> （内层叶子类型），而它的兄弟 `team_result.Next` / `team_result.Records` 是**大写**
> （容器结构体用 Go 字段名）。写错取到 `null`，不报错。

**排查时的三个高频陷阱**（都验证过真实数据）：

1. **分层**：`team_turn.*` 在 `flow.jsonl`；`command_turn.*` 在 `team.jsonl`。搜不到不等于缺数据。
2. **字段名**：模型回复原文是 `agent.model_response` 的 **`payload.text`**，不是 `content`。
3. **`round` 是 turn 内局部序号**，每轮从 0 重数；用 `team_turn_id` 区分是否同一轮，
   不要因 `round` 重复就以为数据重复。

**`text` 为空不是数据丢失**：纯工具调用轮（`finish_reason=tool_calls`）、
结构化重试被丢弃的轮、输出已解析进 `evidence.jsonl` 的轮——都是正常情况。
用 `completion_tokens` 区分"模型输出异常"与"schema 定义过严"，手法见
`references/debug-triage.md`。

判定标准、异常模式对照、配置对照见 `references/debug-triage.md`。

**Token 消耗统计**（终端 `Tokens:` 行 = 本 FlowTurn 总额；拆到 team / agent 只读 `team.jsonl`）：

```bash
# 总计（应等于终端 Tokens: 行）
jq -s '[.[]|select(.type=="agent_turn.completed")|.payload.call_result.Usage.total_tokens]|add' $S/team.jsonl

# 按 team 汇总
jq -r -s '[.[]|select(.type=="agent_turn.completed")]|group_by(.team_id)
          |map({t:.[0].team_id,total:([.[].payload.call_result.Usage.total_tokens]|add)})
          |.[]|"\(.t)\ttotal=\(.total)"' $S/team.jsonl

# 按 agent 汇总
jq -r -s '[.[]|select(.type=="agent_turn.completed")]|group_by(.payload.call_result.AgentID)
          |map({a:.[0].payload.call_result.AgentID,total:([.[].payload.call_result.Usage.total_tokens]|add)})
          |.[]|"\(.a)\ttotal=\(.total)"' $S/team.jsonl
```

三级累加：`Request.usage` → `call_result.Usage` → `team_turn.Usage` → flow `Tokens`。
注意 `Usage` 内是小写字段名（`total_tokens`），同级 `AgentID` 是大写。
`prompt ≫ completion` 是常态（上下文每轮重发），判定"贵不贵"要看比值而非 total。
完整算式与 node 写法见 `references/token-usage.md`。

## 参考资料（先按需读取）

排查相关按用途拆成 4 个文件，**按需只读需要的那份**：

| 参考文件 | 什么时候读 | 内容 |
|---|---|---|
| `references/debug-triage.md` | **排查入口** | 常用命令速查、判定标准、异常模式对照、从配置预测行为、排查流程 |
| `references/session-files.md` | 看数据在哪 | 数据落点、三层 jsonl 各装什么、聊天记录在哪、执行日志 |
| `references/token-usage.md` | 看消耗 | 按 team / agent / 轮次统计 token，含三级累加链路 |
| `references/trace-ids.md` | 串链路 | 每类事件带哪些 ID（串联地图）、正向/反向查询 |

配置与运维：

| 参考文件 | 内容 |
|---|---|
| `references/fields.md` | **每个配置字段的含义**（Flow/Team/Call/Agent/record/rules）+ 运行时事件字段 + **`${records.*}` 模板**（把共享记录传进 command） |
| `references/organization.md` | 目录组织、三层编排、设计优势 |
| `references/operations.md` | 下载/使用/启动/关闭的详细命令 |
| `references/define.md` | **Define 工具**：让 Agent 在对话中自建 Agent/Team 定义 |

读取方式：用 `Read` 打开 `references/<name>.md`（本 skill 目录下）。

## 核心要点

1. 三层编排：`Flow → Team → Call → Agent/Command/Webhook`，`Call` 是执行项不是层级。
2. 配置全部在 `.agents/` 目录下，按类型分目录。
3. `model.model` 直接写模型 id（来自 models.json），**勿用 `${ENV}` 占位符**（引擎不展开环境变量）。
4. record 靠 `output.record` + 下游 `inputs` 的 `{from, record}` 按名精确匹配，
   名称是自由字符串、无校验，拼写必须一致。
5. command call 不读对话内容；要把上游记录的值传给它，用 `command.env` 的
   `${records.<Name>.Data.<field>}` 模板（仅在 `Data` 顶层，字段缺失会让 call 失败）。
   详见 `references/fields.md`。
6. `models.json` 含 api_key，永不提交。
7. 想让 Agent 在对话中自建 Agent/Team，需在其 `AGENT.md` 里声明
   `tools.builtin: [..., Define]`；未声明则该工具对模型不可见。
   详见 `references/define.md`。

## 脚本

| 脚本 | 用法 |
|---|---|
| `scripts/build.sh` | `bash build.sh [输出路径]` 从源码构建二进制 |
| `scripts/serve.sh` | `bash serve.sh {start\|stop\|status}` 后台启停服务（PID 管理） |

## 修改配置的原则

1. 修改前先 `Read` 目标文件，保留已有字段与结构。
2. 只改必要的最小差异，不重构无关配置。
3. 新增 agent/team 时，确保 model、tools、call 引用的 agent 名、record 名前后一致。
4. 修改后运行引擎加载确认无 validate 错误。
