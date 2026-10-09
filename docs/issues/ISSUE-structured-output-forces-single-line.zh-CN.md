# 发现：structured_output 的 response_format 约束导致 reply 永远是单行

> 状态：**已修复（根因更正）**（2026-10-09）
> 影响面：所有配置了 `structured_output` 的 agent 的可读长回复
> 关联：`internal/prompt/builtin.go`（`structuredOutputContract`）

## 修复记录 + 根因更正（2026-10-09）

**本文档原先的"根因"（`response_format` 强制单行）经实测被推翻。** 用真实网关
（`copilot.tencent.com/v2`，`claude-sonnet-5`，流式）对照实验：

| 条件 | `reply` 换行数 |
|---|---|
| 无 `response_format`，prompt 要求换行 | 4（分段） |
| `response_format: json_object`，prompt 要求换行 | 4（分段） |
| `response_format: json_schema`（当前引擎用的），prompt 要求换行 | 4（分段） |
| `json_schema` + **真实 coordinator 风格 prompt（全是"禁止…"，无一句允许换行）** | **0（单行）** |
| 同上 + 只加一句"reply 可用 `\n` 分段" | **3（分段）** |

结论：**单行不是 `response_format` 造成的**，而是**系统提示里全是"禁止 Markdown/禁止
解释/禁止第二个 JSON"，却从没说"允许换行"**——模型在这种全禁止约束下保守地把长回复写
成一行。只要 prompt 明确允许 `\n`，同一个模型、同样的 `response_format` 立刻分段。

**引擎侧通用修复**：`internal/prompt/builtin.go` 的 `structuredOutputContract`
（structured agent 系统提示里引擎自动追加的那段）新增一句：

> Inside string fields, use `\n` to separate points, steps, or paragraphs —
> newlines in string values are valid JSON and make long replies readable;
> do not collapse everything onto one line.

所有 structured agent 通用，无需逐个改 AGENT.md。测试：
`internal/prompt/builtin_test.go` 的 `TestStructuredOutputContractAllowsNewlines`。

**渲染侧无问题**（原文结论仍成立）：TUI 对真实换行渲染正常，问题一直在"输入就没有换行"。

## 现象

`coordinator`（以及其他带 `structured_output` 的 agent）的回复在 TUI 中显示为
**一整行无分段的长文本**。用户在截图中看到：

```
明白，你要的是方案 a：genie 仓库的 genie-1 槽位，切到 feat/134288-miniprogram-p0。
但这一棒我不能自己派出去——两个卡点：1) 重复激活门禁：... 2) 授权：genie-1 当前在 ...
```

`1)` 和 `2)` 挤在同一行，没有换行、没有缩进。

用户最初的疑问：「怎么看起来不换行，模型直接给的就不换行吗」
后续追问：「是不是代码里面过滤了」——**这个方向是对的**。

## 数据事实

对会话 `fs_7a3e3c95f9b3fbf9` 的 `agent.jsonl` 做全量统计：

```
共 68 条 model_response
长回复（>300 字符）16 条，其中无换行 13 条（81%）

 #     长度    换行数  首行预览
 1    373      0      '{"reply":"我是 auto-bugfix 的主协调员...
 5   1333      0      '{"reply":"知识库不是随便找个 md 写...
27    898      0      '{"reply":"你问的是「为什么切到了 genie-agent-1...
62    864      0      '{"reply":"明白，你要的是方案 a：...
68   1009      0      '{"reply":"收到，你选了方案 2...
```

**所有无换行的回复，首行都是 `{"reply":"`**（即带 JSON 外壳）。

**带换行的回复，都不带 JSON 外壳**：

```
26   3215     63      '用户：repos/.pool/genie-1 我期望切换...'
38   7911     99      '@decision\nstatus: done\n...'
39    736     16      '收到上游数据。检查输入：...'
```

**强相关**：有 `structured_output` → 单行；没有 → 正常换行。

单条精确核对（第 62 条，即截图那条）：

| 项 | 数量 |
|---|---|
| 长度 | 864 |
| 真实换行符 `\n` | **0** |
| 字面 `\n`（两个字符） | **0** |

**源头就没有换行符**，不是渲染丢失。

## 根因：引擎把 schema 作为 `response_format` 发给模型

### 链路

`internal/agent/structured.go:18-20`：

```go
func structuredModelConfig(agent types.AgentConfig) types.ModelConfig {
	config := agent.Model
	config.ResponseFormat = agent.Structured     // ← 关键
	...
```

`internal/model/openai.go:75`：

```go
type openAIRequest struct {
	...
	ResponseFormat      map[string]any  `json:"response_format,omitempty"`   // ← 发给模型
	...
}
```

即：agent 的 `structured_output.schema` 被作为 **`response_format`** 传给模型，
要求它输出**严格符合 schema 的 JSON**。

### 为什么会导致单行

`response_format`（OpenAI 的 `json_schema` / strict 模式）要求模型输出**严格合法**的
JSON。在这样的约束下，模型倾向于**避免在字符串字面量里放任何换行**——
即便是转义的 `\n` 也可能被它视为"可能破坏 JSON"的风险而主动省略。

实测数据支持这个解释：**换行数恰好为 0**（连一个转义 `\n` 都没有），
说明模型是**主动规避**而非随机遗漏。

对照证据：同样内容的 agent（如 `root-cause-analyst` 输出 `@decision\nstatus: done\...`）
**没有走 structured 路径时换行正常**。

## 这与 prompt 无关

`coordinator/AGENT.md` 的约束是：

```
- 禁止 Markdown、禁止代码块围栏、禁止解释、禁止第二个 JSON。
```

**只禁 Markdown 与代码块围栏，并未禁止换行**；`structured_output` 的 schema 里
`reply` 也只是 `type: string`。

所以问题不在 agent 定义（prompt 文案），而在**引擎层的 `response_format` 注入**。

> 注：先前曾建议"改 prompt 要求模型用 `\n` 分段"——**该建议不成立**，
> 因为约束来自 `response_format` 而非 prompt 文案；模型即使被 prompt 要求，
> 在 strict JSON 约束下仍可能规避换行。已在本文档修正。

## 渲染侧是正常的

已实测（独立复现 glamour 渲染）：

| 输入 | 输入换行数 | 渲染结果 |
|---|---|---|
| 含真实换行 | 3 | ✅ 正确渲染为列表（`1.` `2.` 各自成行） |
| 含字面 `\n` | 0 | ❌ 原样显示 `\n` |
| 单行（当前现状） | 0 | ❌ 一整行 |

**TUI 的 `renderMarkdown` 对换行的处理是正确的**，问题只在"输入就没有换行"。

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 |
|---|---|---|
| **A. 允许 reply 不用 strict** | 对 `structured_output` 中标注为"可读文本"的字段（如 `reply`）不进入 strict schema，或整体改用非 strict 的 `json_object` 模式 | 引擎改动；需确认网关对 `json_object` 的支持 |
| **B. prompt 明确允许 `\n`** | 在 agent 定义里写明 `reply` 可用 `\n` | **低成本但可能无效**——约束来自 `response_format`，模型未必遵循（见上） |
| **C. 渲染侧分段** | TUI 对超长单行按句号 / 分号 / `N)` 模式重排 | 治标；易切错语义，且把展示层的猜测当成修复 |
| **D. 不发 response_format** | `openai.go:208-218` 已有"提交失败则移除 response_format 重试"的兜底；可让配置显式关闭 | 会失去 schema 约束，模型可能输出非 JSON |

**建议先验证 A 的可行性**：确认网关对 `response_format: {type: "json_object"}`
（非 strict）的支持情况，以及是否能在 schema 中为 `reply` 类字段保留"允许换行"的宽松度。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 1. 找一个带 structured_output 的 agent 的回复
S=.agents/data/sessions/<fs_id>
python3 - <<'EOF'
import json
for l in open('.agents/data/sessions/<fs_id>/agent.jsonl', encoding='utf-8'):
    if not l.strip(): continue
    e = json.loads(l)
    if e.get('type') != 'agent.model_response': continue
    t = (e.get('payload') or {}).get('text') or ''
    if len(t) > 300:
        print(f"长度={len(t)} 换行数={t.count(chr(10))} 首行={t[:40]!r}")
EOF
# 观察：首行为 '{"reply":"...' 的，换行数基本为 0

# 2. 对照 agent 定义
grep -A3 "structured_output" .agents/agents/coordinator/AGENT.md
# 有 structured_output → 单行；没有 → 正常换行
```

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/agent/structured.go:20` | `config.ResponseFormat = agent.Structured`（注入点） |
| `internal/agent/structured.go:53` | `ParseAndValidate`（解析不碰字符串内容，`\n` 会正确还原） |
| `internal/model/openai.go:75` | `ResponseFormat` 字段（发给模型的请求体） |
| `internal/model/openai.go:208-218` | response_format 失败时的移除重试兜底 |
| `internal/agent/runtime.go:589` | `modelConfig := structuredModelConfig(agent)`（调用点） |
| `.agents/agents/coordinator/AGENT.md` | 仅禁 Markdown，**未禁换行**（排除 prompt 嫌疑） |

## 一句话

`structured_output` 的 schema 被引擎作为 `response_format` 发给模型，strict JSON
约束下模型主动规避字符串内的任何换行（实测 81% 的长回复换行数为 0，且首行均为
`{"reply":"`）；这是**引擎层注入 `response_format` 的结果，不是 prompt 问题、
也不是 TUI 渲染问题**——TUI 对真实换行的渲染已验证正常。
