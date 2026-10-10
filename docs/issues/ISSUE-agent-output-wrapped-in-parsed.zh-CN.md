# 缺陷：agent 产出被引擎多套一层 `parsed` 壳，导致下游无法按声明使用字段

> 状态：**已修复**（2026-10-10）
> 影响面：所有由 agent 产出、需要被下游 command/agent 消费的 SharedRecord
> 关联：`internal/runtime/call/agent.go:121-135`
>
> ## 修复记录
>
> 按 A 方案改了 `internal/runtime/call/agent.go` 的 record 构造：
> 把 `result.Parsed`（引擎已强制为 `map[string]any`，见 `structured.go`）的键
> **展开到 `Data` 顶层**，不再包 `parsed` 壳。`reply` 仅在 agent 未声明同名键时写入
> （agent 显式声明优先）。
>
> 端到端实测（agent schema 声明 `repo`/`candidates`/`notes`，下游 command 引用）：
> - `Data` 顶层键 = `[candidates, notes, reply, repo]` ✅
> - `parsed` 壳不再存在 ✅
> - `reply` 与 `parsed` 的内容重复消失 ✅
> - `${records.X.Data.candidates}` / `.repo` / `.notes` 全部直接命中 ✅
>
> 测试（`execute_coverage_test.go`）覆盖 issue 列出的 5 个边界：
> 展开、reply 冲突（agent 优先）、`Parsed=nil`、非 map 回退、无 record 不产出。

## 现象

agent 在自己的 schema 里声明了字段（如 `issue-searcher` 声明 `candidates` / `notes`），
但下游**无法直接引用**：

```yaml
${records.IssueCandidates.Data.candidates}          # ❌ 不存在该键
${records.IssueCandidates.Data.parsed.candidates}   # ❌ too deep（引擎不支持深路径）
```

只能：

```yaml
${records.IssueCandidates.Data.parsed}              # ✅ 但拿到的是整段嵌套 JSON
```

**下游被迫先拿整段、再自己解析**——而 agent 明明已经声明清楚了字段。

## 根因：引擎在 agent 输出外多套了一层 `{reply, parsed}`

`internal/runtime/call/agent.go:121-135`：

```go
if recordName := req.Call.Output.Record; recordName != "" {
	data := map[string]any{"reply": result.Reply}      // ← 引擎自建
	if result.Parsed != nil {
		data["parsed"] = result.Parsed                 // ← 再套一层
	}
	callResult.Records = []types.SharedRecord{newCallRecord(
		req, recordName, "agent_result", result.Reply, data,
	)}
	...
}
```

**agent 按 schema 输出的对象被整体塞进 `data["parsed"]`**，
而 agent 声明的字段名（`candidates` / `notes` / …）**一个都没提升到 `Data` 顶层**。

### 实测：`parsed` 与 `reply` 内容完全重复

对 `IssueCandidates` 的 Data 做验证：

```json
{
  "reply":  "{\"candidates\":[{\"number\":134288,...}],\"notes\":\"...\"}",
  "parsed": {"candidates":[{"number":134288,...}],"notes":"..."}
}
```

```bash
# 实测：parsed 就是 reply 的 JSON 解析结果
jq '... | (.reply | fromjson) == .parsed'   →  true
```

**即 `Data` 里存了同一份数据的两遍**（一遍原文本、一遍解析后），且业务字段仍在第二层。

## 为什么这是问题

按职责界定——**agent 声明清楚自己的产出，下游直接用**——当前设计违背了它：

| 层 | 谁定义 | 下游能否直接用 |
|---|---|---|
| agent 的 schema（`candidates` / `notes` / …） | **配置** | — |
| agent 的输出对象 | 模型按 schema 填 | — |
| **`Data = {reply, parsed}`** | **引擎**（额外包装） | ❌ 字段被压在 `parsed` 下 |

后果：

1. **下游无法按声明使用字段**——必须知道"引擎会套 `parsed`"这个实现细节
2. **配置被迫在上游 agent 重复声明**——例如为了给 `acquire_slot` 传 `repo`，
   不得不在 `issue-searcher` 的 schema 里**再加一个顶层 `repo`**，
   使它同时存在于 `repo`（顶层）和 `parsed.candidates[].repo`（原位置）。
   **这是在配置层打补丁，修引擎的包装问题。**
3. **两份数据冗余**：`reply` 是 `parsed` 的 JSON 文本形式，占双倍空间

## 与另一条 issue 的关系

| issue | 问题 |
|---|---|
| `record-template-cannot-pass-structures` | 值被 `fmt.Sprint` 加工成 `map[...]` → **格式错** |
| **本条** | 值被套 `parsed` 壳 → **层级错** |

**两者叠加**才是"配置成本高"的完整原因：

```
agent 声明 candidates
   ↓ 引擎套壳 → Data.parsed.candidates
      ↓ 深路径不支持 → 只能引 Data.parsed
         ↓ stringify 加工 → map[...]
            ↓ 完全不可用
```

## 修复方向

### A. 把 agent 的结构化输出展开到 `Data`（推荐）

**关键前提已被引擎自身保证**：`internal/agent/structured.go:78-80` 强制
"structured output must be a JSON object"——**所以 `result.Parsed` 一定是 `map[string]any`**，
不存在数组/标量形态，无需为它们保留 `parsed` 分支。

```go
data := map[string]any{"reply": result.Reply}
if parsed, ok := result.Parsed.(map[string]any); ok {
	for k, v := range parsed {
		data[k] = v          // ← 展开到顶层
	}
}
```

**效果**：agent 声明的字段名**原样出现在 `Data` 顶层**，下游可直接引用：

```yaml
${records.IssueCandidates.Data.candidates}    # ✅ 直接命中 schema 声明
${records.IssueCandidates.Data.notes}         # ✅
${records.IssueCandidates.Data.repo}          # ✅（若 schema 里有该字段）
```

**符合职责界定**：引擎不解释语义，只是**不再额外加壳**——agent 声明什么，下游就拿到什么。

#### 影响面极小

全仓 `result.Parsed` 的读取点**只有 1 处**（即套壳处）：

```bash
$ grep -rn '\.Parsed\b' internal/ pkg/ cmd/ | grep -v _test
internal/runtime/call/agent.go:124:		if result.Parsed != nil {
internal/runtime/call/agent.go:125:			data["parsed"] = result.Parsed
```

改动集中在这两行，无其他消费方需要同步。

#### 需处理的边界

| # | 情况 | 处理 |
|---|---|---|
| 1 | **键冲突**：agent schema 里也有 `reply` | 定义优先级。建议：**agent 的声明优先**（它是显式契约），引擎的 `reply` 仅在 agent 未声明时写入 |
| 2 | `result.Parsed == nil`（无 schema 或未产出结构化结果） | 保持现状：`Data = {reply}` |
| 3 | `Parsed` 非 map（理论上被 schema 校验排除） | 走 `ok=false` 分支，保持 `Data = {reply}`，不 panic |
| 4 | **配置兼容**：现有引用 `Data.parsed` 的地方 | 本项目尚未真正使用 `Data.parsed`（`acquire_slot` 引的是顶层 `repo`），迁移影响可控 |

#### 需要覆盖的测试点

| # | 输入 | 期望 `Data` |
|---|---|---|
| 1 | `Parsed = {"a":1,"b":"x"}` | `{"reply":..., "a":1, "b":"x"}` |
| 2 | `Parsed = nil` | `{"reply":...}`（无多余键） |
| 3 | `Parsed` 含 `reply` 键 | 按选定优先级（建议 agent 优先） |
| 4 | `Parsed` 含嵌套对象 | 原样保留（嵌套不展开，交给模板/脚本处理） |
| 5 | 无 `output.record` 的 call | 不产出 record（现有行为不变） |

### B. 保留 `parsed` 但支持深路径（不推荐单独做）

若只做"支持 `${...Data.parsed.candidates.0.repo}`"，仍要求下游知道 `parsed` 这层存在，
**没有解决"声明与使用不一致"的根本问题**。

### C. 让 agent 自己声明包装形态（不推荐）

要求每个 agent 在 prompt 里说明"我的输出会进 `parsed`"——把引擎的实现细节
写进配置文档，**本末倒置**。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
S=.agents/data/sessions/fs_7a3e3c95f9b3fbf9

# 1. 看 agent 产出的 Data 结构（只有 reply/parsed）
jq -r 'select(.type=="shared_record.published" and .payload.record.name=="IssueCandidates")
       | .payload.record.data | keys' $S/flow.jsonl | tail -6

# 2. 验证 parsed 是 reply 的解析结果（内容重复）
jq -r 'select(.type=="shared_record.published" and .payload.record.name=="IssueCandidates")
       | .payload.record.data | (.reply|fromjson) == .parsed' $S/flow.jsonl | tail -2

# 3. 对比：agent 声明的字段 vs Data 里实际有的
grep -A4 "^structured_output:" .agents/agents/issue-searcher/AGENT.md | head -8
# 声明了 repo / candidates / notes，但 Data 顶层只有 reply / parsed
```

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/runtime/call/agent.go:121-135` | **套壳点**：`data["parsed"] = result.Parsed` |
| `internal/runtime/call/policy.go:139-152` | 模板只支持 `Data.<field>` 一层（深路径报 `too deep`） |
| `internal/runtime/call/command.go:96-108` | command 产出对照：`Data = {exit_code, stdout, ...}`（同样未展开业务数据） |
| `internal/agent/structured.go` | `result.Parsed` 的来源（结构化输出解析结果） |

## 一句话

agent 已按 schema 声明清楚产出字段，但引擎把整个结构化结果塞进 `Data["parsed"]`
（且 `reply` 与 `parsed` 内容重复），导致下游无法按声明直接引用、
只能被迫在上游 agent 重复声明扁平字段来绕过；应**把 agent 的结构化输出键展开到 `Data` 顶层**（`reply` 保留为原始文本，
冲突键建议 agent 声明优先）——引擎已强制 `Parsed` 必为 JSON 对象，
展开方案无形态歧义；且全仓仅 1 处消费 `Parsed`，改动安全。
