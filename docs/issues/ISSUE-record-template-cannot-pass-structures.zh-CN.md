# 缺陷：`${records.*}` 模板无法传递复杂结构（注释与实现不符，输出 Go map 格式）

> 状态：**已修复**（2026-10-10）
> **修复归属：改引擎代码**（配置侧无可改之处——引用语法正确，是引擎输出被加工）
> 影响面：所有想用 `${records.X.Data.<field>}` 把结构化数据传给 command 的配置
> 关联：`internal/runtime/call/policy.go:168-192`（单点改动）
>
> ## 修复记录
>
> 按 A 方案改了 `stringifyRecordValue` 的 `default` 分支，
> 由 `fmt.Sprint(typed)` 改为 `json.Marshal(typed)`（失败回退 `fmt.Sprint`），
> 使实现与其注释承诺一致：非标量值以**原始 JSON 形态**传递。
> 新增测试覆盖 issue 中列出的全部 8 个测试点
> （`policy_test.go`：`TestStringifyRecordValue{Scalars,Structures,EscapesSpecialChars,FallsBackOnMarshalError}`）。
>
> B（深层路径 `Data.parsed.candidates.0.repo`）**未做**——按 issue 结论它是可选优化项，
> 有 A 之后配置可把整个 `parsed` 传给脚本、由脚本内 `jq` 取字段，不阻塞。

## 职责界定（判定本 issue 是否成立的前提）

| 职责 | 归属 | 说明 |
|---|---|---|
| **按配置传递「配置指定的那段值」** | **引擎** | 忠实搬运，**不拆解、不转换、不判断内容** |
| 定义值的形态与解释 | 配置 | 值是什么、能否被消费，**引擎不关心** |

**推论**：

- 配置引用的值是 `string` → 传 string
- 配置引用的值是**嵌套对象** → **传它的原始形态**，引擎不做编码/美化/展平
- 内容能否被 shell/jq 消费 → **不是引擎的判断范围**

**因此判定标准只有一条：引擎传的，是不是那个值的原始形态？**

### 本 issue 的判定

`Data.parsed` 的**原始形态是 JSON**（落盘即为 JSON 对象）：

```json
{"candidates":[{"number":134288,"repo":"genie/genie"}],"notes":"x"}
```

而引擎输出的是：

```
map[candidates:[map[number:134288 repo:genie/genie]] notes:x]
```

**这是 Go 的 `fmt.Sprint` 调试格式，不是原始值** —— 说明引擎**加工了它**。

| | 内容 |
|---|---|
| 原始（落盘） | `{"candidates":[...]}` ← JSON |
| `json.Unmarshal` 后 | `map[string]interface{}` |
| **引擎传给 command 的** | `map[candidates:[map[number:...]]]` ← **Go 格式** |

**结论：这是引擎的加工行为，违反"忠实传递"原则。** ✅ 本 issue 成立。

（若引擎传的是原始 JSON 字符串、或原样透传对象，则属"已忠实传递"，
配置能否消费是配置的事——那才不构成 issue。但事实并非如此。）

## 现象

按文档写模板引用嵌套字段：

```yaml
command:
  env:
    AUTO_QUERY: "${records.DiagnosisReport.Data.parsed}"
```

得到的**不是 JSON**，而是 Go 的内部 map 格式：

```
map[candidates:[map[number:134288 repo:genie/genie]]]
```

**这个字符串无法被 shell 命令或 `jq` 解析**——传了等于没传。

## 根因：注释声称"JSON 编码"，实现却是 `fmt.Sprint`

`internal/runtime/call/policy.go:168-192`：

```go
// argv element. Non-scalar values are rendered as their JSON encoding so the
// result is still a single shell-safe token.          ← 注释承诺 JSON
func stringifyRecordValue(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return fmt.Sprint(typed)      // ← 实际是 Go 默认格式，不是 JSON
	}
}
```

**标量类型（string/bool/数字）正常**，但 `default` 分支用 `fmt.Sprint` ——
对 `map[string]any` / `[]any` 会输出 Go 的调试格式。

### 实测复现

复刻该函数，输入一个典型的 `Data.parsed`（JSON 解码后的嵌套 map）：

```go
parsed := map[string]any{
	"candidates": []any{
		map[string]any{"number": float64(134288), "repo": "genie/genie"},
	},
}
fmt.Println(stringify(parsed))
```

输出：

```
map[candidates:[map[number:134288 repo:genie/genie]]]     ← 实际
{"candidates":[{"number":134288,"repo":"genie/genie"}]}   ← 注释承诺的
```

**两者完全不同**。前者不是合法 JSON，无法被任何工具消费。

## 影响链条

```
record.Data 只暴露包装结构（见下）
        +
嵌套值被格式化成 Go map 串
        ↓
${records.X.Data.parsed} 拿到的值不可用
        ↓
只能用"上游 agent 主动输出的扁平标量字段"（如 IssueCandidates.Data.repo）
        ↓
每个 command 需要的参数都要为它单独加一个 agent schema 字段
        ↓
配置成本极高：6 个 command 需要改 4 个 agent 的 schema + prompt
```

## 背景：Data 的形态（**非缺陷**，仅说明为何容易踩坑）

模板只能取 `Data` 的**顶层字段**。两类产出方的 `Data` 形态由引擎如实生成——
**这是合理的**（引擎只负责搬运，不理解业务），但配置作者需要知道：

### ① command call 产出的 record（**独立议题，非本 issue**）

```json
Data = {
  "exit_code": 0,
  "passed":    true,
  "stdout":    "{\"slot_id\":\"genie-1\",\"abs_path\":\"/...\"}",   ← 业务数据在字符串里
  "stderr":    ""
}
```

`stdout` 是**字符串**，引擎如实传递——**不违反忠实传递原则**。

配置若需要 `abs_path`，可引 `${records.WorktreeSlot.Data.stdout}` 得到那段 JSON 字符串，
再由脚本 `jq` 解析。**但"是否该把 stdout 展开成 Data 顶层字段"是可优化项**，
属独立议题，与本 issue（形态被加工）无关。

### ② agent call 产出的 record

```json
Data = {
  "reply":  "整段文本",
  "parsed": { "candidates": [...], "notes": "..." }    ← 结构化结果，嵌套
}
```

`parsed` 是嵌套对象 → 走 `default` 分支 → **输出 Go map 格式**（本缺陷）。

### ③ 引擎自建的 record（形态扁平，故最省事）

```json
TeamFailureReport.Data = {
  "status": "failed", "reason": "...", "team_id": "intake",
  "failed_calls": [...], "call_errors": [...]      ← 扁平，可直接取
}
```

引擎自己拼的 Data 是扁平的，所以 `TeamFailureReport` 的字段**能正常引用**。

## 当前配置的实际后果

以 `auto_bugfix` 的 heron 配置为例，8 个 command call 里：

| call | 需要的参数 | 现状 |
|---|---|---|
| `intake/acquire_slot` | `repo` | ✅ 可用（`issue-searcher` 特意加了顶层 `repo` 标量） |
| `fix/verify_write` | `<file_path>` | ❌ 无来源 → 无参 → **exit 2 → `passed=false`（写门禁恒"未通过"）** |
| `fix/similar_fixes` | `--query` | ❌ 无来源 |
| `verify/run_tests` | `--repo` `--path` | ❌ 无来源（`WorktreeSlot.Data` 只有 `stdout`） |
| `learn/dup_check` | `--text` | ❌ 无来源 |
| `deliver/pr_diff` | `--repo` `--number` | ❌ 无来源 |
| `diagnose/sandbox_debug` | 沙箱目标 | ✅ skipped 是设计行为 |
| `verify/git_status` | 无 | ✅ 无参正确 |

**只有 1 个是特意配对的**，其余都靠"无参兜底"运行——其中 `verify_write` 会
持续产出 `passed=false`，**写门禁形同虚设**。

> 注意归因：这些**不是**"引擎该替配置决定传什么"，而是**配置想传却传不过去**。
> 若引擎能忠实传递 `${...Data.parsed}`（合法 JSON），配置即可自行 `jq` 取所需字段，
> 无需为每个参数去改上游 agent 的 schema。

## 结论：改引擎，不是改配置

**配置侧无改动空间**：

- 引用语法 `${records.X.Data.<field>}` 本身正确（`policy.go:109-152` 支持）
- 值也确实存在（`Data.parsed` 就在 record 里）
- **问题只在引擎输出的形态**——它把 JSON 对象转成了 Go 调试格式

所以无论配置怎么写，都拿不到原始值。**必须改引擎**（见下 A 方案，单点改动）。

### 顺带澄清：两个独立问题，不要混

| # | 问题 | 归属 | 处理 |
|---|---|---|---|
| **①** | 引擎把值加工成 `map[...]` | **引擎** | **本 issue**，改 `stringifyRecordValue` |
| ② | command 产出的 `Data` 只展开 `exit_code/stdout/...`，业务 JSON 埋在 `stdout` 字符串里 | 引擎（设计选择） | **另议**，见下方"背景"节 |

**①改了之后**：配置可引 `${records.X.Data.parsed}` 拿到原始 JSON，脚本自行 `jq` 解析。
**②仍在**：`WorktreeSlot.Data.abs_path` 依然不存在——但那不是"加工"问题
（`stdout` 是 string，引擎如实传递），而是"command 产出的 Data 是否该展开"，
属**独立议题**，不阻塞 ① 的修复。

## 修复方向

> **总原则**：只修"传递"，不碰"解释"。引擎不做字段语义推断、不强制配置产出某种结构，
> 只保证"配置引用的值，以消费方可用的形式到达"。

### A. 修 `stringifyRecordValue` 的 `default` 分支（**唯一必做项**）

让实现与它自己的注释一致：

```go
default:
	encoded, err := json.Marshal(typed)
	if err != nil {
		return fmt.Sprint(typed)
	}
	return string(encoded)
```

**效果**：`${records.X.Data.parsed}` 到达时是**原始值的形态**（JSON），
而非 Go 的 `fmt.Sprint` 调试格式。配置侧自行决定如何解析。

**这是唯一必须改的**：它让"值以原始形态到达"。至于值里有什么、怎么用——
**完全是配置的事**，引擎不拆解、不判断、不解释。

**代价**：几行。已实测当前输出 `map[...]`（见"复现"）。

**附带注意**：值可能较长（整个 `parsed` 对象）。若担心 env 体积，
应由**配置**决定只引子字段（见 B），而非引擎擅自截断。

#### 需要覆盖的测试点

| # | 输入 | 期望输出 |
|---|---|---|
| 1 | `string` | 原样 |
| 2 | `bool` / 整数 / 浮点 | 现有行为不变（`true` / `134288` / `1.5`） |
| 3 | `map[string]any` | **合法 JSON**（`{"a":1}`），不是 `map[a:1]` |
| 4 | `[]any` | **合法 JSON**（`[1,2]`），不是 `[1 2]` |
| 5 | 嵌套（`map` 内含 `[]map`） | **合法 JSON**，且顺序/转义正确 |
| 6 | 含特殊字符的字符串（引号、换行、中文） | 作为 JSON 值时正确转义 |
| 7 | `nil` | 空串（现有行为） |
| 8 | `json.Marshal` 失败的类型（如 `chan`） | 回退 `fmt.Sprint`，不 panic |

> 测试 3-5 是本次修复的核心；测试 1-2、7 是**回归保护**（确保没有改变既有标量行为）。

### B. （可选）让模板支持更深路径，便于配置精确取值

支持 `${records.X.Data.parsed.candidates.0.repo}` 这类路径，让配置**能精确指定**要哪个字段。

这仍然符合职责界定——**是"配置声明要哪段"，引擎照取**，不是引擎理解语义。

当前实现明确拒绝深层路径（`policy.go:149` 报
`too deep (expected <Name>.Data.<field>)`）。开放它需要定义：
数组下标语法（`.0` 还是 `[0]`）、路径不存在时的行为（报错还是空串）。

> 建议：**A 先做**（立刻解套）；B 视需要再评估，它不是阻塞项——
> 有了 A，配置可以把整个 `parsed` 传给脚本，由脚本里 `jq` 取字段。

### C. ✗ 不建议：让引擎"理解"业务结构

例如让引擎自动把 `parsed.candidates` 展平成 `Data.candidates`。
**这违反职责界定**：引擎会绑死在某种业务结构上，换一份配置就失效。
**不做。**

### D. 另一种"配置侧零成本"的形态（供讨论，非必需）

若希望连"写模板"都省掉，可考虑让 command call 也能收到上游 record
（当前只收 `env` + 原始用户输入，见 `team/runtime.go:1146-1154`）。
但那会改变 stdin 语义，属**引擎行为变更**，且与 A 不冲突——
**A 已足以让配置自主取值**，D 只是进一步简化。

## 复现

```bash
cd /Users/jahweijiang/Documents/github/multirole-agent/heron-ai

# 1. 看实现（注释 vs default 分支）
sed -n '168,192p' internal/runtime/call/policy.go

# 2. 实测嵌套结构的输出
cat > /tmp/s.go <<'EOF'
package main
import ("fmt";"strconv")
func stringify(v any) string {
	switch t := v.(type) {
	case nil: return ""
	case string: return t
	case bool: return strconv.FormatBool(t)
	case float64:
		if t == float64(int64(t)) { return strconv.FormatInt(int64(t),10) }
		return strconv.FormatFloat(t,'f',-1,64)
	default: return fmt.Sprint(t)
	}
}
func main() {
	parsed := map[string]any{"candidates": []any{map[string]any{"number": float64(134288), "repo": "genie/genie"}}}
	fmt.Printf("实际: %s\n", stringify(parsed))
	fmt.Printf("期望: %s\n", `{"candidates":[{"number":134288,"repo":"genie/genie"}]}`)
}
EOF
go run /tmp/s.go
# 实际: map[candidates:[map[number:134288 repo:genie/genie]]]
# 期望: {"candidates":[{"number":134288,"repo":"genie/genie"}]}

# 3. 在一处真实配置里试（会得到不可用的值）
grep -rn 'records\.' .agents/teams/*.yml
```

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/runtime/call/policy.go:168-192` | `stringifyRecordValue`（**default 分支与注释不符**） |
| `internal/runtime/call/policy.go:109-152` | `resolveRecordPlaceholder`（只支持 `Data.<field>` 一层） |
| `internal/runtime/call/policy.go:149` | 深层路径报错点（`too deep`） |
| `internal/runtime/call/agent.go:123-125` | agent 产出 `Data = {reply, parsed}`（嵌套来源） |
| `internal/runtime/call/command.go:96-108` | command 产出 `Data = {exit_code, stdout, ...}`（字符串来源） |
| `internal/runtime/team/runtime.go:1146-1154` | command 的 input 只含原始用户输入，不含 record |

## 一句话

按职责界定——**引擎负责"按配置忠实传递"，配置负责"定义与解释业务结构"**——
`stringifyRecordValue` 的注释承诺"非标量按 JSON 编码"，实现却用 `fmt.Sprint` 输出
`map[...]` 这类 Go 调试格式，**属传递失效（引擎问题）**；
应把 `default` 分支改为**原样传递**（`json.Marshal` 即还原其 JSON 形态），
使 `${records.X.Data.parsed}` 到达消费方时仍是原始值——
至于是 string 还是嵌套结构、怎么解析，**全部由配置决定，引擎不关心**。
