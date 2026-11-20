# 会话排查 · Token 消耗统计

按 flow / team / agent / 轮次统计 token。

数据来源与字段位置见 `session-files.md`；ID 串联见 `trace-ids.md`。

## 数据链路（三级累加）

```text
requests[]（一轮模型调用：usage.prompt_tokens / completion_tokens / total_tokens）
  → call_result.usage           单个 call 内所有轮次的加总（agent.jsonl 事件里叫 payload.call_result）
    → team_result.usage         该 team 内所有 call 的加总（flow.jsonl 的 session.team_turn.ended）
      → 终端 Tokens 行          对本次 FlowTurn 里所有 team_result.usage 求和
```

两个要点：

- **flow 层没有聚合好的 usage 字段**。`FlowTurnResult` 里没有 `Usage`，终端那个
  `Tokens:` 值是消费方对 `TeamTurnResult.Usage` 求和得到的。所以自己也照这个口径算。
- **`team_result.usage` 是"本轮所有 call 之和"，别和 call 层相加**。以
  `deliver=downstream` 派发的子任务会被插入成合成 call（`<parent>/<key>`），由 team
  循环当普通 call 执行，所以它**已经计入** `team_result.usage`。不在里面的是两类：
  同步 inline spawn（在父 call 内部跑完，不进 team 循环），以及要到**下一个 TeamTurn**
  才完成的 call（异步工具任务、审批恢复——算在完成它的那一轮）。
  因此 team 层与 call 层只能二选一读，相加就是重复计。

终端一次 `--prompt` 的输出形如：

```text
Tokens: 59340 (prompt 55980, completion 3360)
Cache: 50120 of 55980 prompt tokens (89.5%)
```

第二行只在命中缓存时出现。

## 三条统计命令

```bash
S=.agents/data/sessions/<fs_id>

# 1) 总计：对每个 team turn 的 usage 求和（与终端 Tokens 行同口径）
jq -s '[.[] | select(.type=="session.team_turn.ended")
        | .payload.team_result.usage.total_tokens] | add' $S/flow.jsonl

# 2) 按 team 汇总
jq -r -s '[.[] | select(.type=="session.team_turn.ended")]
          | group_by(.team_id)
          | map({team: .[0].team_id,
                 prompt: ([.[].payload.team_result.usage.prompt_tokens] | add),
                 completion: ([.[].payload.team_result.usage.completion_tokens] | add),
                 total: ([.[].payload.team_result.usage.total_tokens] | add)})
          | .[] | "\(.team)\tprompt=\(.prompt)\tcompletion=\(.completion)\ttotal=\(.total)"' \
  $S/flow.jsonl

# 3) 按 agent 汇总：agent 粒度在 call 层，所以读 team.jsonl
jq -r -s '[.[] | select(.type=="session.agent_turn.completed")]
          | group_by(.payload.call_result.agent_id)
          | map({agent: .[0].payload.call_result.agent_id,
                 team: ([.[].team_id] | unique | join(",")),
                 prompt: ([.[].payload.call_result.usage.prompt_tokens] | add),
                 completion: ([.[].payload.call_result.usage.completion_tokens] | add),
                 total: ([.[].payload.call_result.usage.total_tokens] | add)})
          | .[] | "\(.agent)\tteam=\(.team)\tprompt=\(.prompt)\tcompletion=\(.completion)\ttotal=\(.total)"' \
  $S/team.jsonl
```

> 按 agent 汇总时，**子 agent 回合也在 `team.jsonl` 里**（带 `payload.spawn`），所以
> 它会单独出现在结果中。这是对的：子任务确实花了钱。注意这类子任务的消耗**已经包含**
> 在 `team_result.usage` 里（合成 call 口径），不要再和 team 层相加。

## 缓存命中怎么看

缓存相关字段在 `usage` 里，不同 provider 命名不同：

| 字段 | 谁报 |
|---|---|
| `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` | OpenAI 兼容网关 |
| `cache_read_input_tokens` / `cache_creation_input_tokens` | Anthropic 兼容 |
| 两者同时出现 | 某些网关一次请求两套都报 |

**命中量取 `prompt_cache_hit_tokens` 与 `cache_read_input_tokens` 中较大的那个，不能相加**
——同时报两套的网关会被加两遍，命中率就假了。终端 `Cache:` 行用的就是取大值的口径。

```bash
# 每个 team turn 的缓存命中率
jq -r 'select(.type=="session.team_turn.ended")
       | .payload.team_result.usage as $u
       | "\($u.prompt_tokens)\t\([$u.prompt_cache_hit_tokens, $u.cache_read_input_tokens] | max)\t\($u.cache_creation_input_tokens // 0)"' \
  $S/flow.jsonl
```

命中率高说明稳定前缀（system prompt、skill listing、rule、知识指路块）被复用了；
命中率突然掉到 0，通常是前缀每轮都在变——比如某段内容里带时间戳、随机 ID 或
每轮重排的列表。

## 读这些数字时要注意三点

1. **同一 agent 被多次调用会分别累计**。`group_by` 会把它们合并，但轮次数是累加的。
   这是"本次 FlowTurn 总消耗"，不是"某个 agent 的固定开销"。
2. **`prompt` 会因上下文重复发送而膨胀**。同一 agent 多轮时，每轮都把前序工具输出
   重新发给模型，`prompt_tokens` 逐轮累加。所以 `prompt ≫ completion` 是常态，
   不能只看 `total` 判断"某个 agent 很贵"——要看 `prompt / completion` 比值。
3. **team 层与 call 层不能相加**。`team_result.usage` 已经含本 TeamTurn 执行过的每个
   call（包括插入成合成 call 的子任务）；再加上 `call_results[].usage` 就是重复计。
   用命令 1 得到的数与终端 `Tokens:` 一致。想知道"某个子 agent 单独花了多少"，看命令 3
   里它那一行，但不要再把它加回总量。
4. **字段命名**：`team_result` / `call_result` 这一层也是 snake_case（`usage` /
   `agent_id` / `requests` / `team_result` / `call_result`），叶子是 `prompt_tokens` /
   `completion_tokens` / `total_tokens`。写成 `Usage.PromptTokens` 会得到 `null`。

## 单 agent 的逐轮消耗（定位消息膨胀）

```bash
# 看某 agent 每一轮各自的 usage，找出哪一轮开始膨胀
jq -r 'select(.type=="session.agent_turn.completed" and .payload.call_result.agent_id=="researcher")
       | .payload.call_result.requests[]
       | "prompt=\(.usage.prompt_tokens) completion=\(.usage.completion_tokens) cached=\(([.usage.prompt_cache_hit_tokens, .usage.cache_read_input_tokens] | max))"' \
  $S/team.jsonl
```

## node 等价写法（无 jq 时）

```bash
node -e '
const fs = require("fs");
const [flowPath, teamPath] = process.argv.slice(1);
const read = (p) => fs.readFileSync(p, "utf8").split("\n").filter(Boolean)
  .map((l) => { try { return JSON.parse(l); } catch { return null; } })
  .filter(Boolean);

let total = 0;
const byTeam = {};
for (const e of read(flowPath)) {
  if (e.type !== "session.team_turn.ended") continue;
  const u = e.payload.team_result.usage || {};
  total += u.total_tokens || 0;
  const t = (byTeam[e.team_id] ||= { prompt: 0, completion: 0, total: 0 });
  t.prompt += u.prompt_tokens || 0;
  t.completion += u.completion_tokens || 0;
  t.total += u.total_tokens || 0;
}

const byAgent = {};
for (const e of read(teamPath)) {
  if (e.type !== "session.agent_turn.completed") continue;
  const r = e.payload.call_result || {};
  const u = r.usage || {};
  const a = (byAgent[r.agent_id || "?"] ||= { prompt: 0, completion: 0, total: 0 });
  a.prompt += u.prompt_tokens || 0;
  a.completion += u.completion_tokens || 0;
  a.total += u.total_tokens || 0;
}

const show = (label, bag) => {
  for (const [k, v] of Object.entries(bag)) {
    console.log(`${label}=${k}\tprompt=${v.prompt}\tcompletion=${v.completion}\ttotal=${v.total}`);
  }
};
show("team", byTeam);
show("agent", byAgent);
console.log("总计(team 口径)\t" + total);
' .agents/data/sessions/<fs_id>/flow.jsonl .agents/data/sessions/<fs_id>/team.jsonl
```
