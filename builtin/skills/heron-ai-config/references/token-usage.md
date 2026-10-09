# 会话排查 · Token 消耗统计

按 team / 按 agent / 按轮次统计 token。只读 `team.jsonl` 一个文件即可
（`team_id` 与 `AgentID` 都在事件上）。

数据来源与字段位置见 `session-files.md`；ID 串联见 `trace-ids.md`。

终端 `--prompt` 输出的 `Tokens: 59340` 就是**本次 FlowTurn 的总额**。要拆到 team 和
agent，只读 `team.jsonl` 一个文件即可（`team_id` 与 `AgentID` 都在事件上）。

### 数据链路（三级累加）

```text
Request（一轮模型调用，usage.prompt_tokens / completion_tokens）
  → call_result.Usage                      ← 单个 call 内所有轮次的加总
    → team_turn.Usage                      ← 该 team 内所有 call 的加总
      → flow 级 Tokens（终端显示值）        ← 所有 team 的加总
```

### 三条统计命令

```bash
S=.agents/data/sessions/<fs_id>

# 1) 总计（应等于终端 Tokens: 行）
jq -s '[.[] | select(.type=="agent_turn.completed")
        | .payload.call_result.Usage.total_tokens] | add' $S/team.jsonl

# 2) 按 team 汇总
jq -r -s '[.[] | select(.type=="agent_turn.completed")]
          | group_by(.team_id)
          | map({team: .[0].team_id,
                 prompt: ([.[].payload.call_result.Usage.prompt_tokens] | add),
                 completion: ([.[].payload.call_result.Usage.completion_tokens] | add),
                 total: ([.[].payload.call_result.Usage.total_tokens] | add)})
          | .[] | "\(.team)\tprompt=\(.prompt)\tcompletion=\(.completion)\ttotal=\(.total)"' \
  $S/team.jsonl

# 3) 按 agent 汇总
jq -r -s '[.[] | select(.type=="agent_turn.completed")]
          | group_by(.payload.call_result.AgentID)
          | map({agent: .[0].payload.call_result.AgentID,
                 team: ([.[].team_id] | unique | join(",")),
                 prompt: ([.[].payload.call_result.Usage.prompt_tokens] | add),
                 completion: ([.[].payload.call_result.Usage.completion_tokens] | add),
                 total: ([.[].payload.call_result.Usage.total_tokens] | add)})
          | .[] | "\(.agent)\tteam=\(.team)\tprompt=\(.prompt)\tcompletion=\(.completion)\ttotal=\(.total)"' \
  $S/team.jsonl
```

实测输出（`fs_63f041b056487198`，一个跨 team 的会话）：

```text
总计            628464

team=default    prompt=38528   completion=5767    total=44295
team=intake     prompt=566563  completion=17606   total=584169

agent=coordinator      team=default  prompt=38528   completion=5767   total=44295
agent=issue-searcher   team=intake   prompt=566563  completion=17606  total=584169
```

### 读这些数字时要注意三点

1. **同一 agent 被多次调用会分别累计**。上例 `default` team 的 coordinator 在
   intake 前后各被调用一次，两次都计入（`group_by` 会把它们合并，但轮次数是累加的）。
   这是"本 FlowTurn 总消耗"，不是"某个 agent 的固定开销"。
2. **`prompt` 会因上下文重复发送而膨胀**。同一 agent 多轮时，每轮都把前序工具输出
   重新发给模型，`prompt_tokens` 逐轮累加。所以 `prompt ≫ completion` 是常态，
   不能只看 `total` 判断"某个 agent 很贵"——要看 `prompt / completion` 比值。
3. **字段大小写**：`call_result.Usage` 内是小写 `prompt_tokens` / `completion_tokens` /
   `total_tokens`（叶子结构）；而同级兄弟 `AgentID` / `Requests` / `Records` 是大写
   （容器用 Go 字段名）。写成 `Usage.PromptTokens` 会得到 `null`。

### 单 agent 的逐轮消耗（定位消息膨胀）

```bash
# 看某 agent 每一轮各自的 usage，找出哪一轮开始膨胀
jq -r 'select(.type=="agent_turn.completed" and .payload.call_result.AgentID=="issue-searcher")
       | .payload.call_result.Requests[]
       | "prompt=\(.usage.prompt_tokens) completion=\(.usage.completion_tokens)"' \
  $S/team.jsonl
```

### node 等价写法（无 jq 时）

```bash
node -e '
const fs = require("fs");
const team = process.argv[1];
const rows = fs.readFileSync(team, "utf8").split("\n").filter(Boolean)
  .map((l) => { try { return JSON.parse(l); } catch { return null; } })
  .filter((e) => e && e.type === "agent_turn.completed");
const byTeam = {}, byAgent = {};
let total = 0;
for (const e of rows) {
  const u = (e.payload.call_result || {}).Usage || {};
  const agent = (e.payload.call_result || {}).AgentID || "?";
  total += u.total_tokens || 0;
  for (const [bag, key] of [[byTeam, e.team_id], [byAgent, agent]]) {
    bag[key] = bag[key] || { prompt: 0, completion: 0, total: 0 };
    bag[key].prompt += u.prompt_tokens || 0;
    bag[key].completion += u.completion_tokens || 0;
    bag[key].total += u.total_tokens || 0;
  }
}
const show = (label, bag) => {
  for (const [k, v] of Object.entries(bag)) {
    console.log(`${label}=${k}\tprompt=${v.prompt}\tcompletion=${v.completion}\ttotal=${v.total}`);
  }
};
show("team", byTeam);
show("agent", byAgent);
console.log("总计\t" + total);
' .agents/data/sessions/<fs_id>/team.jsonl
```
