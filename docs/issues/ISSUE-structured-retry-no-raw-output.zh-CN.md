# 发现：结构化重试丢弃的模型原始输出不可回溯

> 状态：**已修复**（2026-10-09）
> 影响面：可观测性——无法从落库数据判断模型为何未通过 `structured_output` schema
> 关联：`internal/agent/runtime.go:722-732`、`internal/model/openai.go`

## 修复记录（2026-10-09）

按"复用 `agent.feedback`"的方向实施（改动最小、向后兼容）：

- `internal/agent/runtime.go` 两处重试分支（JSON 校验失败 `:739`、输出截断 `:677`）
  发射 `agent.feedback` 时，payload 在 `round`/`content` 之外新增 `raw_text`，
  值为被丢弃的 `resp.Text`。
- `internal/agent/replay.go` 的 `EventAgentFeedback` 分支：若 payload 带
  `raw_text`，先还原成一条 `assistant` 消息再追加 user 反馈——与重试时写进内存
  上下文的顺序一致，避免 replay 出的上下文与真实上下文发散。
- 同步更新 `builtin/skills/heron-ai-config/references/session-files.md` 与
  `fields.md`：`agent.feedback` 现包含 `raw_text`；旧会话该字段为空
  （`jq` 得 `null`）属正常，需与"模型输出为空"区分。
- 测试：新增 `TestReplayAgentContextFeedbackWithRawText`；原
  `TestReplayAgentContextFeedback`（无 `raw_text`）仍通过，确认向后兼容。

## 现象

agent 配置了 `structured_output` 但模型返回不合法 JSON 时，引擎会丢弃这一轮的
模型输出、追加一条固定提示、然后重试。**被丢弃的原始输出没有任何地方留存。**

落库数据只能看到"发生了重试"，看不到"模型当时到底输出了什么"：

```
fs_7a3e3c95f9b3fbf9/agent.jsonl

seq=21  agent.model_response  round=0  finish=stop  text=''  usage.completion_tokens=2
seq=22  agent.feedback        round=0  content='The previous response was not valid JSON for the required schema. Return one complete JSON object only; do not explain, use Markdown, or omit required fields.'
seq=23  agent.model_response  round=1  finish=tool_calls  text=''
```

`seq=21` 是那次失败的尝试，但 `text` 是空串。**原始内容不可见。**

## 根因

`internal/agent/runtime.go:722-732`：

```go
if agent.Structured != nil {
    parsed, err = NewStructuredOutputManager().ParseAndValidate(cleanText, agent.Structured)
    if err != nil {
        if structuredRetries < 1 && round+1 < maxRounds {
            structuredRetries++
            // 把 resp.Text 作为 assistant 消息加进内存上下文，供下一轮参考
            if addErr := contextManager.AddMessage(types.Message{
                Role: "assistant", Content: resp.Text,
            }); addErr != nil {
                return nil, addErr
            }
            invalidMsg := "The previous response was not valid JSON for the required schema. ..."
            // ... 追加 feedback 后 continue，进入下一轮
```

失败分支只做两件事：把 `resp.Text` 放进**内存上下文**、追加 `agent.feedback`。
**没有任何写盘动作携带 `resp.Text`。**

同时：

- `agent.model_response` 的 `payload.text` 由事件发射时的字段决定，而该轮
  `resp.Text` 从未被发射——落库的空串与实际持有的内容无关。
- `internal/model/openai.go` 不落 raw HTTP body（`result.Text = choice.Message.Content`
  之后 body 即丢弃），所以日志里也没有。

结论：**这是一条真实的可观测性缺口，不是落库抖动。**

## 影响

- 无法判断模型是"返回了空/截断 JSON"还是"返回了结构不同但语义正确的 JSON"。
- 无法据此决定该兼容（放宽 schema）还是不改（模型侧偶发）。
- 排查只能间接推断，见 `builtin/skills/heron-ai-config/references/debug-triage.md`
  「排查手法：区分"模型输出异常"与"schema 定义过严"」一节：
  实测 4 条丢弃记录 `completion_tokens` 全为 2，倾向"模型侧异常"，但这是推断，不是证据。

## 全库出现次数

`auto_bugfix/heron-ai/.agents/data/sessions/*/agent.jsonl` 中，被丢弃的轮共 **4** 条
（148 条 `agent.model_response` 中的 `text空 + finish=stop + 无tool_calls`）：

```
fs_2631a5a00c34c62d  round=0  completion=2
fs_7a3e3c95f9b3fbf9  round=0  completion=2
fs_e02b79923292cfbd  round=0  completion=2
fs_f741e51abd8bea19  round=0  completion=2
```

出现频率低，且 4 次均自愈（后续有 `agent.model_response`，turn 最终 `completed`），
所以不紧急——这也是"暂不修复"的依据。

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 |
|---|---|---|
| 重试分支落原文 | 在 `runtime.go` 失败分支额外发射一条事件（如 `agent.structured_retry`），payload 带 `raw_text` 与校验错误 | 小；新增一种事件类型，需同步文档 |
| 复用 `agent.feedback` | 给现有 `agent.feedback` 的 payload 增加 `raw_text` 字段 | 最小；不新增事件类型，但会改变已有事件的结构 |
| 模型层落 raw body | `internal/model/openai.go` 在 debug 级别记录原始响应 | 覆盖更广但体量大、可能含敏感内容 |

推荐第 2 种（改动最小、向后兼容）。

## 复现

```bash
cd auto_bugfix/heron-ai

# 1. 找出被丢弃的轮
for f in .agents/data/sessions/*/agent.jsonl; do
  jq -r 'select(.type=="agent.model_response"
                and ((.payload.text // "")|length)==0
                and .payload.finish_reason=="stop")
         | "\(.payload.round) completion=\(.payload.usage.completion_tokens)"' "$f"
done

# 2. 看紧随其后的反馈
jq -r 'select(.type=="agent.feedback") | .payload.content' \
  .agents/data/sessions/fs_7a3e3c95f9b3fbf9/agent.jsonl

# 3. 确认原始输出确实无处可查
grep -c 'completion_tokens\":2' .agents/data/sessions/fs_7a3e3c95f9b3fbf9/agent.jsonl
grep -ri 'raw\|original response' ../logs/*.log | head
```

## 一句话

结构化校验失败时引擎丢弃了模型原文且只把 `resp.Text` 留在内存上下文里，
落盘的事件和日志都不含它；出现频率低（4/148）且均自愈，暂不修复。
