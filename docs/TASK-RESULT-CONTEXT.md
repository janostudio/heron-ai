# 子任务结果如何进入上下文

一个问题：一个 agent 派出 100 个子任务，它们的结论该不该全部进入它的对话？

现在的答案是"全部进来，每条单独跑一个回合"。这个文档说明它为什么贵、业界怎么做、
以及改法。

## 1. 现状：一条结果从派发到回到对话

### 1.1 派发

```
模型 → Team(subagent_type="probe", prompt="查 CLS 请求量")
     → 建 ToolTask，落盘 .agents/data/tool-tasks/tt_xxx:receptionist:team:probe:2.json
     ← "Handed to probe… (handle: task_key=… team_session_id=…)"
     本回合结束，模型继续做别的事
```

派发是异步的：工具立即返回 handle，模型不等结果（`internal/agent/team_tool.go:144`）。

### 1.2 执行与回投

任务在后台跑完，`onTaskDone`（`internal/app/runtime.go:548`）把它交给
`ResumeForToolResult`（`internal/runtime/flow/resume_tool_result.go:55`）：

```
1. 按 (caller team, call_id) 定位调用方的 transcript
2. 去重：transcriptHasToolResult(transcript, taskID)      :80   用 task_id，不是 task_key
3. 写一对消息进 transcript：
     assistant: Collect(handles=["tt_xxx:probe:2"])        :89
     tool:      <回复全文>                                   :114
                (records: ExplorationReport)                :121
                (original task: 派发时 prompt 的前 800 字)    :124
4. waitForIdle，最多 45s                                    :150  toolResultWaitLimit
5. runTurnWithOrigin → 跑一个完整回合                         :206
```

### 1.3 上下文压缩

`MessageContextManager` 有两级压缩（`internal/agent/context.go`）：

| 机制 | 触发 | 行为 | 默认值 |
|---|---|---|---|
| microcompact | 消息滑出"最近 N 组" | 超过 8 KB 的 tool 输出截成头尾各几行，压到 4 KB | `MicrocompactThresholdChars=8192`、`MicrocompactMaxChars=4096`、`RecentMessageGroups=2` |
| compact | 超过上下文阈值 | 整段对话摘要成一条 | 按模型窗口估算 |

microcompact 的产物长这样（`context.go:559`）：

```
[Collect result microcompacted]
<head 约 2 KB>
...
<tail 约 2 KB>
```

## 2. 成本：两层，且叠乘

### 2.1 第一层：一条结果一个回合

`ResumeForToolResult` 末尾无条件跑 `runTurnWithOrigin`。100 个任务 → 100 次模型调用，
串行（`resumeLock` + `waitForIdle`）。

第 k 次回合的输入 = 历史 + 前 k−1 条结果 + 本条。总量 O(n²)。

### 2.2 第二层：结果全文长期驻留

`content` 直接取 `signal.Reply` 全文，只有 `original task` 截了 800 字。

microcompact 在这里几乎不起作用，两个具体原因：

- **中等长度的结果永远不触发**：阈值是 8192 字符，而一个 team 的结论常在 1–3 KB，
  100 条这样的结果全部原样保留。
- **触发之后丢掉引用**：截断保留头尾，但**不保留 task_id**。模型看到
  `[Collect result microcompacted]` 加两截文本，想再要全文已经不知道找谁要。

这是与业界做法偏离最明显的一处：清理可以，但清理后必须留下可重取的指针。

### 2.3 结果

| 任务数 | 回合数 | 上下文体量 |
|---|---|---|
| 5 | 5 | 可接受 |
| 20 | 20 | 开始变慢，microcompact 勉强介入 |
| 100 | 100 | O(n²) 回合 + 全文驻留，实际不可用 |

## 3. 业界怎么做

### 3.1 Anthropic：结果落外部存储，只回传引用

生产的多 agent research 系统（orchestrator + 3–5 个 worker）附录里有一节，
标题就是 **"Subagent output to a filesystem to minimize the 'game of telephone'"**：

> Subagents call tools to store their work in external systems, then **pass lightweight
> references back to the coordinator**. This prevents information loss during multi-stage
> processing and **reduces token overhead from copying large outputs through conversation
> history**.

同一篇还写了他们早期犯的错：**为简单问题派出 50 个 subagent**。

### 3.2 Claude Code：子代理只回最终消息

> intermediate tool calls and results stay inside the subagent; **only its final message
> returns to the parent**. The parent receives a concise summary, not every file the
> subagent read.

并解释了成本逻辑：

> every message re-sends the full conversation. **A turn is priced on the whole transcript,
> not just your latest instruction**.

### 3.3 LangGraph supervisor：默认只回最后一条

`create_supervisor` 的默认 `output_mode="last_message"`，要完整过程必须显式选
`full_history`。它把状态分成四类：shared state / worker-private context / runtime
context / long-term memory —— **工具原始输出属于 worker-private，不进共享上下文**。

### 3.4 OpenAI Agents SDK：给出可复用的两个机制

它的 handoff 默认带上全部历史（含所有工具结果），官方把这称为陷阱：
一条 3000 token 的工具输出被重读 20 轮 = 6 万 token。

现成手段有两个：

```python
# 交接时按类型剥掉工具项
handoff(agent=writer, input_filter=handoff_filters.remove_all_tools)

# 每次模型调用前裁剪旧工具输出
ToolOutputTrimmer(recent_turns=2, max_output_chars=500,
                  preview_chars=200, trimmable_tools={"web_search"})
```

### 3.5 共同原则

> **A tool should return the smallest result that lets the model choose its next safe action.**

允许的返回形态：identifier、count、status、short summary、selected fields、bounded
sample，或 **a pointer to a stored artifact**。

并且明确区分两件事：

- **clearing**：删掉能按 ID 重取的 payload，留一行小记录
- **compaction**：把早前对话摘要掉

两者解决不同的增长，不能互相替代。heron 现在只有 compaction，clearing 只做了半截
（见 §2.2）。

## 4. 方案

分三步，前两步是核心，第三步可选。三步都不改派发接口、不让模型多声明任何参数。

### 4.1 第一步：按结果大小自动决定"全文"还是"引用"

**不需要模型传任何参数**，由引擎按结果体量决定：

| 结果体量 | 进上下文的东西 |
|---|---|
| ≤ 800 字 | 全文 + records（小结论不值得多一次工具调用） |
| > 800 字 | 一行摘要 + task_id 引用，全文留在账本 |

摘要行的格式（必须含"是什么事"，否则模型无法判断要不要拉）：

```
probe finished: 查 CLS 最近一个自然日的请求总数与均值 QPS
(status: completed, records: ExplorationReport)
(full answer: Collect(handles=["tt_xxx:probe:2"]))
```

改动点：`internal/runtime/flow/resume_tool_result.go:114` 附近的 content 构造，
加一个体量分支。`task_id` 已经在 `signal.TaskID` 里，不用新字段。

**为什么不用"派发时声明 delivery"**：那需要模型多判断一次"这个结果我以后要不要用"，
而这正是它不擅长的（它连 task_key 都写不稳）。按体量自动分档不需要任何模型判断，
且规则可预测。

### 4.2 第二步：microcompact 保留引用，而不是截断头尾

现在截断后模型无法取回全文。改成：

```
[probe result compacted: 3.2 KB]
查 CLS 最近一个自然日的请求总数与均值 QPS
(full answer: Collect(handles=["tt_xxx:probe:2"]))
```

改动点：`internal/agent/context.go:535` 的 `microcompactToolContent`。它需要知道
这条消息的 task_id —— 从同一组消息里前面那条 `Collect(handles=[…])` 的 assistant
消息取，或者给 `types.Message` 加一个 `TaskID` 字段由回投时写入（更稳）。

这一条是"clearing 留指针"的落地。做完之后，无论结果大小、无论滑出多远，
模型永远能按 task_id 取回原文。

### 4.3 第三步（可选）：同批结果合并成一次回合

前两步之后，100 条结果在上下文里是 100 行摘要、总共几 KB，不再撑爆上下文。
但仍然是 100 次回合。

合并方式：任务完成先进"待回投队列"，由一个 drain 循环攒批（例如 500 ms 窗口或
累积到 8 条），合成一条 tool 消息跑一次回合。

**这一步优先级最低**，因为它只影响回合数量，不影响上下文体量。而且它有一个真实
风险：合并会延迟单条结果的呈现（用户可能等 500 ms 才看到"某个任务完成了"）。
前两步做完后，如果回合数量仍然是瓶颈，再考虑。

### 4.4 依赖：Collect 的语义要扩

`Collect` 现在的描述只写了 Spawn：

> Block until spawned children from asynchronous Spawn calls (wait=false) finish…

它技术上接受任意 task id（从 `TaskStore` 读），所以复用可行，但模型不会想到用它读
team 结果。需要把描述扩成"读任意异步任务的结论（Spawn 子任务或 Team 派发）"。

同时 `Collect` 的返回值应当支持 `limit`：拉全文时一次给 3 KB 还是 30 KB，应该由
模型决定，而不是默认不设上限（`MaxToolOutputChars` 默认 0 = 不限）。

## 5. 为什么不是别的方案

| 方案 | 为什么不选 |
|---|---|
| **只做"批量 drain"** | 治标。回合数降了，但 100 条全文仍在上下文里，每次回合都要重读。O(n²) 变成 O(n)，常数还是很大 |
| **派发时声明 `delivery=wait/notify/silent`** | 让模型多判断一次"这个结果以后要不要用"，等于把 task_key 的错误重演一遍。按体量自动分档不需要模型参与 |
| **结果完全不进上下文（纯 pull）** | 模型不拉就漏结论。这套代码里已有先例：曾出现"写了已派 X 但从未调用工具"，用户等了 4 分钟。小结果必须直接给 |
| **改 task_key 为 UUID** | 与本文无关，但同样是把引擎该做的事推给模型。见 `TEAM-CONCURRENCY.md` 的讨论 |

## 6. 风险

**① 摘要行不够，模型不知道要不要拉。** 这是第一步唯一的失败模式。缓解：摘要行必须
带原文任务的前 60 字（不是 team 名，不是 records 名），且带 status，让"失败了"和
"完成了但有 3 条结论"在摘要层面就能区分。

**② 模型不主动拉。** 对需要拿它做决策的结果（check 的 TestDecision、dev 的 ChangeSet），
多一次工具调用就是多一次失败机会。缓解：800 字的阈值定得偏大，让"要用来做决策的结论"
基本都落在全文档。真正的长文本（probe 的取证过程、audit 的报告）才是引用档。

**③ microcompact 拿不到 task_id。** 第二步需要把 task_id 和 tool 消息关联起来。
最稳的做法是回投时给 `types.Message` 写一个 `TaskID` 字段，而不是从相邻消息反推
（反推在 compaction 之后会失效）。

**④ 阈值需要观测数据校准。** 800 字是估计值。上线后应该统计"被引用化的结果里，
模型随后调用 Collect 的比例"：
- 比例很高 → 阈值定小了，说明这些结果本来就该给全文
- 比例很低 → 引用化是对的，甚至可以把阈值调小

## 7. 落地顺序

| 步骤 | 改动 | 风险 | 收益 |
|---|---|---|---|
| 1 | 回投按体量分档（`resume_tool_result.go`） | 低，纯新增分支 | 上下文体量从 O(全文) 降到 O(摘要) |
| 2 | microcompact 留引用（`context.go` + `Message.TaskID`） | 中，动压缩路径 | 结果永远不会因压缩而不可取回 |
| 3 | 扩 `Collect` 描述与 `limit` 参数 | 低 | 让"按需拉"真的可用 |
| 4 | 同批结果合并回合 | 中，动触发时机 | 回合数下降，但延迟上升 |

第 1、3 步可以一起做，第 2 步单独验证（压缩路径有既有测试，改动要带回归）。

## 8. 一句话

问题不是"引擎管不住 100 个任务"——任务记录、去重、归属、查询都已经用引擎生成的 ID
管得很好。问题是**结果回到对话时抄了全文，且每条都触发一个回合**。业界的一致做法是
结果留在账本、上下文里只放"一句摘要 + 一个能取回全文的指针"，heron 已经有账本
（`tool-tasks/*.json`）和取回工具（`Collect`），缺的只是回投路径上那两个动作。
