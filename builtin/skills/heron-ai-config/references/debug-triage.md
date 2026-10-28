# 会话排查 · 判定标准与排查流程

第一次排查从这里开始；需要细节时再按需读同目录的专项文件
（`session-files.md` / `token-usage.md` / `trace-ids.md`）。

## 1. 常用排查命令

先确定 `flow_session_id`：用户提供，或取最新会话目录。

> **跨平台提示**：下文用 `jq`（Linux / macOS / Windows 均可安装）+ 少量 shell。
> `jq` 语法三平台完全一致，差异只在外层 shell：
> - Linux / macOS：命令可原样粘贴到 `bash` / `zsh`。
> - Windows：建议用 Git Bash 或 WSL；PowerShell 里 `$S`、`\` 续行、`head` 不通用，需改写。
> - 每条命令里 `$S/xxx.jsonl` 换成完整路径即可，jq 表达式不变。
>
> jq 安装：`apt install jq` / `brew install jq` / `winget install jqlang.jq`（或 `scoop install jq`）。
> 不想装 jq 时，用本节末尾的 node 等价写法（node 在本项目里总是可用）。

```bash
cd <工作区>                                   # 本例：auto_bugfix/heron-ai
ls -1dt .agents/data/sessions/fs_* | head -5  # 按 mtime 倒序，最新在最前
```

```bash
# 会话目录（bash/zsh；Windows 直接写完整路径）
S=.agents/data/sessions/<fs_id>

# 事件序列（注意分层：team_turn 在 flow.jsonl）
jq -r '.type' $S/flow.jsonl
jq -r '.type' $S/team.jsonl

# 路由结果：每个 team turn 结束时的 next.action
jq -r 'select(.type=="team_turn.completed") | .payload.team_result.Turn
       | "\(.team_id) \(.status) next=\(.next.action) teams=\(.next.teams // [])"' \
  $S/flow.jsonl

# 轮次 + 消耗（事实源；一轮 = Requests 数组里的一个元素）
jq -r 'select(.type=="agent_turn.completed")
       | .payload.call_result
       | "\(.AgentID) 轮次=\(.Requests|length) status=\(.Status)"' \
  $S/team.jsonl

# 某个 agent 的模型输出（字段是 text；负索引在 jq 1.6 不生效，用切片）
jq -r 'select(.type=="agent.model_response")
       | "\(.payload.round) \(.payload.model) \(.payload.finish_reason) \((.payload.text // "")[0:120])"' \
  $S/agent.jsonl

# 工具调用退出码
jq -r 'select(.type=="tool_call.completed")
       | "\(.payload.tool_name) exit=\(.payload.exit_code // "?")"' $S/agent.jsonl

# 结构化重试提示（模型没按 schema 输出时会追加）
jq -r 'select(.type=="agent.feedback") | .payload.content' $S/agent.jsonl

# 上下文压缩
jq -r 'select(.type=="context.compacted") | .payload.summary' $S/agent.jsonl

# 证据链
jq -r 'select(.type=="shared_record.published") | .payload.record.name' $S/flow.jsonl
jq -r '.name, (.basis[]?.path)' $S/evidence.jsonl

# 执行日志按会话过滤
grep '"flow_session_id":"<fs_id>"' .agents/data/logs/*.log
```

> **字段大小写**：`payload.team_result.Turn` 内层是小写（`next` / `record_ids` /
> `team_id`，叶子类型），而它的兄弟 `team_result.Next` / `team_result.Records` /
> `team_result.CallResults` 是大写（容器结构体用 Go 字段名）。取错层级得到 `null`
> 且不报错，排查时会误以为是数据缺失。

**没有 jq 时**用 node（三平台通用，无需额外装 jq）：

```bash
node -e '
const fs = require("fs");
const path = ".agents/data/sessions/<fs_id>/team.jsonl";
for (const line of fs.readFileSync(path, "utf8").split("\n")) {
  if (!line.trim()) continue;
  let e; try { e = JSON.parse(line); } catch { continue; }
  if (e.type !== "agent_turn.completed") continue;
  const cr = e.payload.call_result || {};
  console.log(cr.AgentID, "轮次=", (cr.Requests || []).length);
}
'
```

> node 在本项目里总是可用（heron 的 skill 脚本统一用 node）。
> 上面的单引号写法在 Linux / macOS / Git Bash / PowerShell 7 均可直接执行；
> 老版 PowerShell 需改用双引号并把内部 `"` 转义。
## 2. 怎么判定"符合预期"

| 检查项 | 预期 |
|---|---|
| 会话定位 | 一次 `--prompt` 或 `--session` 续聊 → 一个 `sessions/<fs_id>/`，续聊是**追加**写入同一目录，按 `seq` / `created_at` 判断先后，不要按行号 |
| 事件顺序 | `flow_session.created` → `flow_turn.started` → `team_*` → `flow_turn.waiting_input` |
| **轮次** | 一轮 = `agent_turn.completed` 的 `call_result.Requests` 里一个元素；同一 agent 多个 Request 说明它多轮调用了模型 |
| **route** | `team_turn.completed.payload.team_result.Turn.next.action` ∈ `proceed` / `activate` / `return` / `coordinate` / `fail` |
| **终态** | 正常结束必然是 `flow_turn.waiting_input`（会话可续聊）；`failed` 是这一轮失败。`cancelled` 有两种来源：进程中断留下的悬空回合（见下），或外部取消——看 `payload.error` 是否写着 interrupted |
| **team 范围** | 寒暄类应只激活 `entry` team；出现 specialist team 说明路由把它当成任务了 |
| **消耗** | 事实源是 `team.jsonl` 的 `agent_turn.completed → payload.call_result.Requests[].usage`；flow 级加总 = 各 agent 之和 |
| **证据** | 发布的 SharedRecord 同时出现在 `flow.jsonl` 的 `shared_record.published` 与 `evidence.jsonl`。被派发的子 Team 产出的记录会以 flow 作用域副本进入 `evidence.jsonl`，所以那里出现"不是入口 Team 写的"记录是正常的 |

### 异常模式对照

| 现象 | 去看什么 |
|---|---|
| `flow_turn.failed` | 它的 `error`；常见 `repeated Flow activation "A->B" exceeded retry limit`（某个 team 反复激活同一组目标——多半是路由成环或 agent 没识别"这一步已完成"。先修路由，只有在确属合法的多次转交时才调 `settings.json` 的 `runtime.max_activation_retries`） |
| `call_result.Status` 非 completed | `call_result.Error` |
| 某 agent 的 `Requests` 数暴涨 | 上下文膨胀。每轮把前序工具输出重新带给模型；查 `context.compacted`，并考虑收窄工具输出或减少读取量 |
| `flow_turn` 迟迟不结束 | `runtime.max_team_turns` 兜底上限（默认很宽松，正常编排不会碰到）与路由是否成环 |
| 会话尾巴上有一个 `running` 的回合，之后再没动过 | 进程在回合中途退出。**下一次在这个会话里发消息时**，引擎会先给那个回合补写 `flow_turn.completed` + `status=cancelled` + `payload.error=interrupted…`，然后才开始新回合。看到这条 `cancelled` 说明"上次是被中断的"，不是这次新出的错 |
| 某条 record 明明产出了，下游 `flow_records` 却读不到 | 先看 `shared_record.consumed` 有没有对应事件：没有，说明下游根本没被移交这条记录（检查 `inputs` 名字与时间窗）；有事件却没内容，才去看下游的注入 |
| 模型输出为空 / 解析失败 | 先确认读的是 `payload.text` 而非 `payload.content`；若有 `agent.feedback`，说明模型没按 schema 输出，引擎已追加重试提示 |
| command call 无输出 | `command_turn.*` 在 `team.jsonl`；看 `payload` 的退出码与 stderr |
## 3. 从配置预测"该发生什么"

排查的前提是知道**正常长什么样**。运行时的形态直接由配置决定，所以先读配置、
再拿运行时数据对照。三条映射：

| 配置位置 | 决定运行时什么 | 对照方式 |
|---|---|---|
| `flows/default.yml` | 有哪些 team、`entry` 是谁、`can_activate` 允许谁被激活 | `flow.jsonl` 里出现的 `team_id` 必须是 `can_activate` 的子集 |
| `teams/*.yml` 的 `calls` | 每个 team 内跑哪些 call（agent/command）、依赖顺序 | `team.jsonl` 的 `agent_turn.*` / `command_turn.*` 应与 calls 一一对应 |
| `teams/*.yml` 的 `output.record` | 该 team 该发布什么 record | `flow.jsonl` 的 `shared_record.published` 的 `name` |
| `agents/*/AGENT.md` 的 `loop` / `budget` | 轮数上限 | `Requests` 元素个数不应超过 `max_rounds` |
| `agents/*/AGENT.md` 的 `structured_output` | 是否会有 `agent.feedback` 重试 | 有则说明模型没按 schema 输出 |
| `settings.json` 的 `runtime` | `max_team_turns` / `max_activation_retries` 等 | 触发上限时 `flow_turn.failed` |

```bash
# 该 flow 允许激活哪些 team
jq -r '.teams.default.can_activate[]' .agents/flows/default.yml 2>/dev/null \
  || yq '.teams.default.can_activate[]' .agents/flows/default.yml

# 实际激活了哪些（应与上面是子集关系）
jq -r 'select(.type=="team_turn.started") | .team_id' $S/flow.jsonl | sort -u
```

**典型对照结论**：

- 实际激活的 team 不在 `can_activate` 里 → 配置与运行不一致，查是否加载了旧定义
- 配了 agent call 却只有 `command_turn.*` → 检查 call 的 `type` 字段
- 没有任何 `agent.feedback` 但 agent 有 `structured_output` → 模型每次都合规，正常
- `Requests` 数接近 `max_rounds` → 该 agent 快撞上限，考虑放宽或收敛 prompt
## 4. 排查思路

1. **agent 为什么这样回答**：`agent.jsonl` 的 `model_response` 序列 + `agent.feedback`
   （结构化重试）+ `context.compacted`（上下文怎么被压缩）。
2. **卡在哪 / 报什么错**：三层 jsonl 的等待态事件 + 执行日志的 error。
3. **消耗多少**：见第 6 节（按 team / agent / 总计）。
4. **为什么走到这一步**：`flow.jsonl` 的 `team_turn.completed` 路由链
   （谁 activate 了谁、第几次回退）。
5. **结论基于什么**：`evidence.jsonl` 的 basis（文件路径 + 行号）。
6. **完整调用链**：用第 9 节的四个 ID 跨三层关联。

### 一个通用的排查顺序

```text
1. 定位会话        ls -1dt .agents/data/sessions/fs_* | head
2. 看整体形态      flow.jsonl 的 type 序列（是不是正常走完）
3. 看轮次与路由    第 5 节「快速体检」三条命令
4. 看消耗          第 6 节三条统计命令
5. 定位异常点      第 7 节「异常模式对照」按现象查
6. 下钻细节        第 9 节四个 ID 串到具体 call / 具体轮
7. 对照配置        第 8 节确认"该发生什么"，判断是配置问题还是运行问题
```
