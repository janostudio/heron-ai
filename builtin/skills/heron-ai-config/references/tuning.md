# 旋钮：什么时候才需要动

`../SKILL.md` §2 说这些键"别写"。这一份是例外手册：**先有症状，再来这里找一个键**。
每个键都给出默认值、触发条件、代价与验证方式——一次只改一个。

## 0. 先理解一个总规则：上限取 min

```
生效轮次 = min(
  loop.max_rounds,              # 不写 = 200
  settings.runtime.max_agent_rounds,   # 默认 200
  budget.max_model_rounds       # 0 = 不限
)
```

写两个不同的值，小的那个静默生效；事后从事件里看不出是谁在钳制。**只要一个上限够用，就别写第二个。**

## 1. 上下文与压缩

| 键 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `context.max_input_tokens` | 模型容量 | **永不主动写**。只有模型 profile 缺失容量时才对 | 写小了压缩提前、历史被折；这是最常见自伤 |
| `context.max_tool_output_chars` | 容量 × 0.10 × 4 | 单条工具输出过大（整读日志/大文件），压缩频繁但窗口没被覆盖 | 截断后模型看不到全文，只能重读或换手段 |
| `context.target_ratio` | 0.45 | 压缩后上下文仍太满，且压缩本身成为主要成本 | 压得越浅，压缩次数越多（每次都付摘要调用 + 缓存重置） |
| `context.compaction_threshold` | 0.90 | 需要更早压缩以留出长工具输出空间 | 同上；且必须低于硬限，否则加载报错 |
| `context.recent_message_groups` | 2 | 压缩把"当前任务"也折掉了（压完模型问"我刚才要干什么"） | 保留更多原文 = 压得更浅 |
| `context.compactor` | `llm` | 只想裁剪不想付摘要调用（廉价、质量低） | `mechanical` 不调模型，信息损失更大 |

**判断顺序**：先看 `session.context.compacted` 出现次数 → 再看窗口是否被覆盖 → 最后才调 `max_tool_output_chars`。

## 2. 判停

| 键 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `loop.max_repeated_reads` | 3 | 纯只读任务（取证、分析），第一次触顶提示后仍合理重读 | 放宽后真的原地打转时更晚被发现 |
| `loop.stuck_action` | `fail` | 希望判停时先问人而不是判死 | `ask_user` 需要有人能回答；Spawn 出来的子 agent 无人可答，会挂起 |
| `loop.max_same_tool_calls` / `max_no_progress_rounds` / `max_same_model_texts` | 内置 | 只在确认"模型在有效推进但被判停"时 | 与 `max_repeated_reads` 同 |

注意：判停前引擎已经会先给一次提示（"同一内容已返回 N 次…"）。看到提示不等于要改配置，
先确认是不是压缩导致重读同一文件——那种情况该改的是窗口，不是阈值。

## 3. 预算

| 键 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `budget.max_input_tokens` | 0（不限） | 有明确成本上限（如"这个 agent 每次最多 X token"） | 它是**累计** prompt token，不是窗口。按窗口 × 轮数估：24 轮 × 25K ≈ 600K；写 120000 会在第 5 轮左右中止 |
| `budget.max_tool_calls` | 0 | 防止失控地反复调工具 | 与 prompt 里写的"最多 N 次"要一致，否则模型以为自己还能调 |
| `budget.max_model_rounds` | 0 | 一般不用（见 §0，与 `max_rounds` 重复） | 与 `loop.max_rounds` 互相钳制 |
| `budget.max_file_changes` | 0 | 只读 agent 想硬性禁止写 | 读工具的"写"计数口径要确认（改文件才计） |

## 4. 工具与执行模式

| 键 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `tools.builtin` | 无 | 需要能力时加；只在**确认不需要**时减 | 工具定义常驻上下文；写错名字不报错，只是模型看不见 |
| `loop.tool_execution` | `sequential` | 需要同批只读工具并行时改 `parallel_safe` | 并行会一次产生多条大输出，上下文涨得快 |
| `loop.max_parallel_tools` | 引擎默认 | 并行度不够 | 同上 |
| `loop.async_tools` | 空 | 需要长任务脱离本轮（下载、构建） | 异步任务的生命周期要自己收 |
| `loop.max_model_retries` | 引擎默认 | provider 抖动导致失败 | 与"上下文超限恢复"是两套机制，后者只试一次 |

## 5. 结构化输出与完成证据

| 键 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `structured_output` | 无 | 下游要按字段取值（`${records.X.Data.<field>}`） | 模型必须输出合法 JSON；不合法会触发一次重试，仍失败则该轮 failed |
| `completion.require_tool` / `required_tools` | 关 | prompt 已明确"必须先查证"，模型仍直接作答 | 误伤：模型用了别的方式达成同样效果也会被判未完成 |
| `completion.require_workspace_change` | 关 | 任务必须改代码才算完成 | 只读任务开了会必然失败 |
| `hitl.enabled` | 关 | 危险动作要人工确认 | 无人审批时整轮挂起（子 agent 尤其注意） |

## 6. 编排兜底（`settings.json`）

| 段 | 默认 | 什么时候改 | 代价 |
|---|---|---|---|
| `runtime.max_team_turns` | 100 / 每 FlowTurn | 合法的长编排被截断（先确认不是路由成环） | 调高会掩盖成环问题 |
| `runtime.max_agent_rounds` | 200 | 同 §0，会与 `loop.max_rounds` 取 min | 同上 |
| `runtime.max_active_team_sessions` | 8 | `isolated` 实例不够用（未关闭的实例计入配额） | 太高会累积未关闭实例 |
| `logging.level` | `info` | 要 `model request usage` 之类细节 | 日志体积上涨 |
| `tools.restricted_bash_phrases` | 内置 5 条 | 需要额外禁用某类命令 | 命中是"需审批"而非拒绝，无人审批时挂起 |

## 7. 改完怎么验证

```bash
S=.agents/data/sessions/$(ls -1t .agents/data/sessions | head -1)

# 判停 / 预算 / 上限：看终态与 stop_reason
jq -r 'select(.type=="session.turn.ended") | "\(.payload.stop_reason) awaiting=\(.payload.awaiting) err=\(.payload.error // "-")"' $S/flow.jsonl

# 压缩：改了窗口/输出上限后，次数应下降
jq -r 'select(.type=="session.context.compacted") | .payload.dropped_count' $S/agent.jsonl | wc -l

# 轮次：改了上限后，是否仍在同一轮数上撞停
jq -r 'select(.type=="session.agent_turn.completed") | .payload.call_result.requests | length' $S/team.jsonl
```

一次只改一个键，跑一轮，对比上面三条。**改完没有变化，就回滚那个键**——
留下一个不生效的配置，比没有配置更难排查。
