# 缺陷：replay 漏掉 tool_call.failed，重复激活同一 call 时 400

> 状态：**未复现，待补证据**（2026-10-09）
> 已确认的事实：400 报错真实发生；`replay.go` 确实漏处理 `tool_call.failed`
> **未确认**：二者是否因果相关。手工构造的对照实验**未能复现**该 400（见文末）。
> 故本文只记录事实与线索，**不宣称根因已定位**。
> 关联：`internal/agent/replay.go:39`、`internal/agent/runtime.go:1460-1535`

## 现象

解决"协调员拒绝分发"后，再次要求 intake 执行，报错：

```
失败 team：intake（第 2 次激活，team_turn_id=tt_0ef164c1c0edad27）
失败 call：search
失败原因：llm chat 返回 HTTP 400（code=11148）
         tool calls and tool results do not match, please start a new conversation and retry
```

服务端明确要求 `start a new conversation`——即**当前对话历史的 tool 配对本身就坏了**。

## 定位过程

### 1. 失败发生在第 1 次模型调用，不是长对话后期

日志：

```
[error] model chat failed
  team_id: intake   call_id: search   model: deepseek-v4.1-flash
  error: openai chat: bad_request (http 400): {"code":11148,
         "msg":"tool calls and tool results do not match, ..."}
```

`deepseek-v4.1-flash` 是 `issue-searcher` 的模型，说明是它这轮的首个请求。

### 2. 该 turn 内的工具调用出现"2 个 call、1 个 result"

`agent.jsonl`（第二次 intake 的 `tt_0ef164c1c0edad27`，seq 461-468）：

```
461  agent.model_response   tool_calls=0
462  agent.feedback         结构化校验失败，引擎要求重试
463  agent.model_response   tool_calls=2      ← 模型一次返回两个并行调用
       ├─ id=chatcmpl-tool-9fd8fb4e757dd43d  State action=add
       └─ id=chatcmpl-tool-92594c90f70c89fd  State action=update
464  tool_call.started      State
465  tool_call.completed    "added"           ← 第 1 个成功
466  tool_call.started      State
467  tool_call.failed       "state: item \"next_steps\" not found"   ← 第 2 个失败
468  agent.model_response   → 引擎据此发下一次请求 → HTTP 400
```

**assistant 消息声明了 2 个 `tool_calls`，但重建出的 `tool` 消息只有 1 条。**

OpenAI 协议要求**每个 `tool_call` 都必须有对应的 `role=tool` 消息**（无论成败），
否则服务端判定 `tool calls and tool results do not match`。

## 线索：`replay.go` 只处理 completed，漏了 failed（**仅为嫌疑，未证实**）

`internal/agent/replay.go:33-55` 的事件分发：

```go
switch event.Type {
case types.EventAgentModelResponse:
	current = append(current, modelResponseMessage(event.Payload))
case types.EventToolCallCompleted:          // ← 只有 completed
	current = append(current, toolResultMessage(event.Payload))
case types.EventAgentFeedback:
	...
case types.EventContextCompacted:
	...
}
```

**没有 `case types.EventToolCallFailed`。**

而引擎**确实会发出该事件**（`internal/agent/runtime.go:1473` 与 `:1524`）：

```go
t.emitAgentEvent(ctx, req, round, types.EventToolCallFailed, map[string]any{
	"round":     round,
	"tool_name": parallelCalls[i].Name,
	"error":     errMsg,
})
```

于是 replay 时：assistant 的 `tool_calls` 有 2 个，但只补了 1 条 `tool` 消息 →
数量不匹配 → 服务端 400。

### 为什么 replay 会触发

`replay.go:12-14` 的注释点明用途：

> It is used so a normally-completed agent turn can be **continued in a later session**
> without losing its compacted context.

即：**同一个 call 在后续再次执行时**，会重建上次的消息历史。
本次正是 `intake/search` 第二次被激活 → 触发 replay → 撞上漏掉的 failed 事件。

> 注：**同一轮内**的失败处理是正确的（`runtime.go:951` 会把失败结果也
> `AddMessage`，`toolResultContent` 会回退到 `result.Error`）。
> 问题只在 **replay 重建路径**。

## 影响面（推测，未证实）

**若**该漏处理确实是 400 的原因，则影响：任何"同一 call 被 ≥2 次执行
且首次有工具失败"的场景都会失败。但这只是推测——对照实验未复现，见文末。

## 四个待查方向

由于未能复现，以下都只是线索，需逐个排除：

1. **replay 漏 `tool_call.failed`**（本文首要嫌疑）
2. **`context.compacted` 折叠点**：replay 遇到 compacted 事件会用 summary 替换累积消息
   （`replay.go:51`）。若折叠恰好切在 tool_calls 与 tool 结果之间，会产生孤立 tool_call。
   本次会话启用了 context 压缩（`compaction_threshold`），**这条嫌疑同样大**。
3. **failed 事件 payload 缺 `content` / `tool_call_id`**：`replay.go:127-135` 取 `content`，
   而 failed 事件只有 `round`/`tool_name`/`error`。若 replay 补了 failed 但 content 为空，
   可能产生"空 tool 消息"这一异常形态。
4. **本网关对某种特定不匹配才报 11148**：手工构造的 3 种不匹配均返回 200（见文末），
   说明触发条件比"数量不等"更特殊。

## 需要的证据（复现所必需）

- **引擎实际发出的请求体**。当前日志只记错误，不记 request body。
  建议在 `internal/model/openai.go` 的 `post` 失败路径把 request body（脱敏后）
  落到 debug 日志——这是定位此类问题的关键缺口。
- 或：`--log-level debug` 重跑同一步骤，看是否已有更详细输出。

## 修复方向

### A. replay 补齐 failed 事件（根因修复，推荐）

`replay.go` 增加：

```go
case types.EventToolCallFailed:
	current = append(current, toolResultMessage(event.Payload))
```

但要注意：**`EventToolCallFailed` 的 payload 没有 `content` 字段**
（只有 `round`/`tool_name`/`error`），而 `toolResultMessage` 取的是 `content`：

```go
func toolResultMessage(payload map[string]any) types.Message {
	return types.Message{
		Role:    "tool",
		Content: payloadString(payload, "content"),   // failed 事件里为空
		// ToolCallID is not currently recorded in the event; ...
	}
}
```

所以要么：

- **A1**：改 `emitAgentEvent` 的 failed payload，带上 `content`（与 completed 一致），
  replay 端就能直接复用 `toolResultMessage`；
- **A2**：replay 端对 failed 事件用法 `error` 字段兜底为 content。

同时 **`ToolCallID` 在 failed 事件的 payload 里也没有**——而 OpenAI 要求
`role=tool` 消息带 `tool_call_id`。当前 `toolResultMessage` 的注释说
"role/content 配对足够了"，但**对严格的服务端不成立**（与 `completed` 路径同样存在这个隐患，
只是本网关对单条缺失容忍，见下）。

### B. 顺带：`toolResultMessage` 应带上 ToolCallID

`replay.go:127-135`：

```go
// ToolCallID is not currently recorded in the event; the role/content
// pairing is sufficient to rebuild a usable context.
```

实测该网关**对单条缺失容忍**（手工构造缺失 `tool_call_id` 的请求返回 200），
所以它不是本次 400 的直接原因。但**数量不匹配会 400**。

建议一并修正：在 `tool_call.completed` / `tool_call.failed` 事件里记录
`tool_call_id`，replay 时填入 `types.Message.ToolCallID`——这样协议上更严谨，
不再依赖某个网关的宽容度。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
S=.agents/data/sessions/fs_7a3e3c95f9b3fbf9

# 1. 看第二次 intake 的 tool 配对失败
python3 - <<'EOF'
import json
for l in open('.agents/data/sessions/fs_7a3e3c95f9b3fbf9/agent.jsonl', encoding='utf-8'):
    if not l.strip(): continue
    e = json.loads(l)
    if not (461 <= e.get('seq', 0) <= 468): continue
    pl = e.get('payload') or {}
    n = len(pl.get('tool_calls') or []) if e['type'] == 'agent.model_response' else ''
    print(e['seq'], e['type'], ('tool_calls=' + str(n)) if n != '' else pl.get('tool_name',''))
EOF
# → 463 有 tool_calls=2；464-465 一对 completed；466-467 一对 failed

# 2. 看引擎确实发出 failed 事件
grep -n "EventToolCallFailed" ../heron-ai/internal/agent/runtime.go 2>/dev/null || \
  grep -n "EventToolCallFailed" /Users/jahweijiang/Documents/github/multirole-agent/heron-ai/internal/agent/runtime.go

# 3. 看 replay 是否处理
grep -n "case types.Event" /Users/jahweijiang/Documents/github/multirole-agent/heron-ai/internal/agent/replay.go
# → 只有 ModelResponse / ToolCallCompleted / AgentFeedback / ContextCompacted

# 4. 手工验证"数量不匹配"会 400（缺 tool_call_id 单条不会）
#    见下方对照实验
```

### 对照实验（**均未复现 400**）

对同一网关手工构造了三种 tool 消息形态，**全部返回 200**：

| 场景 | 构造 | 结果 |
|---|---|---|
| 数量匹配 | `tool_calls=1`，`tool` 消息 1 条 | HTTP 200 |
| 数量不足 | `tool_calls=2`，`tool` 消息 1 条（带 `tool_call_id`） | HTTP 200 |
| 数量不足 + 无 id | `tool_calls=2`，`tool` 消息 1 条（**无** `tool_call_id`） | HTTP 200 |
| 全部缺失 | `tool_calls=2`，`tool` 消息 0 条 | HTTP 200 |
| id 重复 | 2 条 `tool` 消息共用同一个 `tool_call_id` | HTTP 200 |

**结论：本网关接受这些"不匹配"形态**，因此"数量不等"不是 11148 的触发条件。
真实触发条件仍未知——需要上节的请求体证据。

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/agent/replay.go:33-55` | 事件分发（**缺 `EventToolCallFailed`**） |
| `internal/agent/replay.go:127-135` | `toolResultMessage`（取 `content`；注释称 ToolCallID 缺失无妨） |
| `internal/agent/runtime.go:1473` | 并行分支发出 `EventToolCallFailed` |
| `internal/agent/runtime.go:1524` | 串行分支发出 `EventToolCallFailed` |
| `internal/agent/runtime.go:951` | **同轮内**失败结果正确入栈（对照：此处无问题） |
| `internal/agent/runtime.go:1682` | `toolResultContent`（会回退到 `result.Error`） |

## 一句话

`intake/search` 第二次执行时，引擎发给模型的历史上下文被服务端判定为
"工具记录不完整"（11148），但**手工构造的多种 tool 消息不匹配形态均未复现该错误**；
已确认 `replay.go` 漏处理 `tool_call.failed`（客观事实），但它是否为本次 400 的原因
**尚未证实**——下一步应让引擎在请求失败时记录 request body，或先用 debug 日志重跑定位。
