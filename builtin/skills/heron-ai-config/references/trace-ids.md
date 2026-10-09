# 会话排查 · 事件串联（ID 与跨层关联）

三层 jsonl 靠 ID 串联。本文件给出**每类事件实际带哪些 ID**（串联地图）与
正向/反向查询命令。数据落点见 `session-files.md`。

有**三个反直觉点**会让 jq 取到 `null`，链路就断在这里。先看字段归属表，再照命令查。

### 9.1 ID 层级

```text
flow_session_id           → sessions/<fs_id>/ 目录（一次对话）
  flow_turn_id (ft_…)     → 一轮用户输入（用户每问一次一个）
    team_turn_id (tt_…)   → 某个 team 的一次执行
      call_turn_id        → 某次 call（格式 "<tt>:<call_id>"）
```

### 9.2 每类事件实际带哪些 ID（串联地图）

**这张表是本节的核心。** 打勾 = 该字段在事件顶层有值；空 = `null` 或缺失。

| 文件 | 事件 | flow_session_id | flow_turn_id | team_id | team_turn_id | call_id | call_turn_id |
|---|---|---|---|---|---|---|---|
| flow | `flow_turn.started` | ✓ | ✓ | | | | |
| flow | `team_turn.started` | ✓ | ✓ | ✓ | ✓ | | |
| flow | `team_turn.completed` | ✓ | ✓ | ✓ | ✓ | | |
| flow | `shared_record.published` | ✓ | ✓ | **✗** | ✓ | | |
| team | `agent_turn.*` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| team | `command_turn.*` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| agent | `agent.model_response` | ✓ | **✗** | ✓ | ✓ | ✓ | ✓ |

三个反直觉点：

1. **`shared_record.published` 顶层没有 `team_id`**（取到 `null`）。
   要串 record 的归属，读 `payload.record.producer`——那里**六个 ID 全有**，
   是链路最完整的入口。
2. **`agent.model_response` 没有 `flow_turn_id`**。想从某一轮模型输出回溯到
   "是用户的哪次提问"，必须先拿 `team_turn_id` 去 `flow.jsonl` 查 `flow_turn_id`，
   不能一步到位。
3. **只有 agent call 会写 `agent.jsonl`**。command call（`command_turn.*`）只出现在
   `team.jsonl`，没有对应的 `agent.model_response`；它的输出在
   `command_turn.completed.payload` 里。

### 9.3 串联查询

```bash
S=.agents/data/sessions/<fs_id>

# ① ft → tt：一次会话里，每个用户轮次激活了哪些 team
jq -r 'select(.type=="team_turn.started") | "\(.flow_turn_id) → \(.team_turn_id) [\(.team_id)]"' $S/flow.jsonl

# ② tt → call：某个 team turn 内跑了哪些 call（含 agent 与 command）
TT=$(jq -r 'select(.type=="team_turn.started")|.team_turn_id' $S/flow.jsonl | head -1)
jq -r --arg tt "$TT" 'select(.team_turn_id==$tt and (.type|endswith(".started")))
       | "\(.call_type)\t\(.call_id)\t→ \(.call_turn_id)"' $S/team.jsonl

# ③ call → 轮次：某个 agent call 的每一轮模型输出
CT="tt_a30e5880acfb5afe:search"        # 换成 ② 查到的 call_turn_id
jq -r --arg ct "$CT" 'select(.call_turn_id==$ct and .type=="agent.model_response")
       | "round=\(.payload.round) finish=\(.payload.finish_reason) len=\((.payload.text//"")|length)"' $S/agent.jsonl

# ④ record → 产地（team 在 producer 里，不在事件顶层）
jq -r 'select(.type=="shared_record.published")
       | "\(.payload.record.name) ← \(.payload.record.producer.team_id)/\(.payload.record.producer.call_id)"' \
  $S/flow.jsonl

# ⑤ 反查：已知 agent.model_response，回溯到用户提问
#    因 model_response 无 flow_turn_id，需经 team_turn_id 中转
TT=$(jq -r 'select(.type=="agent.model_response")|.team_turn_id' $S/agent.jsonl | head -1)
FT=$(jq -r --arg tt "$TT" 'select(.type=="team_turn.started" and .team_turn_id==$tt)|.flow_turn_id' $S/flow.jsonl)
jq -r --arg ft "$FT" 'select(.type=="flow_turn.started" and .payload.turn.id==$ft)|.payload.turn.input' $S/flow.jsonl

# record 的完整溯源（六 ID 齐全）
jq -c 'select(.type=="shared_record.published") | .payload.record.producer' $S/flow.jsonl
```

### 9.4 一条完整链路长什么样

以 `fs_7a3e3c95f9b3fbf9` 第 2 轮为例，从提问串到答复：

```text
flow_turn.started        ft_e6212632…  input='提问：知识应该写在那个目录下'
  team_turn.started      tt_4757a023…  team=default
    agent_turn.started   call=coordinator  call_turn=tt_4757a023…:coordinator
      agent.model_response round=0  finish=stop  text=''      ← 结构化校验失败
      agent.feedback       round=0  重试提示
      agent.model_response round=1  finish=tool_calls  → Glob ×2
      agent.model_response round=2  finish=tool_calls  → Read
      agent.model_response round=3  finish=stop  text='{"reply":…}'
    agent_turn.completed Requests=4  Usage.total_tokens=29218
  team_turn.completed    next.action=proceed
  shared_record.published CoordinatorReply
flow_turn.waiting_input
```

**注意 `round` 在 `agent_turn` 内从 0 重数**，跨 `agent_turn` 不连续——用
`call_turn_id` + `round` 组合定位唯一的某一轮。
