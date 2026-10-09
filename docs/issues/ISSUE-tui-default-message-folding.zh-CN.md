# 缺陷：TUI 默认折叠长消息（未经要求新增的功能，破坏完整展示）

> 状态：**已修复**（2026-10-09）——按方案 A 直接移除折叠
> 影响面：所有超过 12 行的 assistant / agent 回复在 TUI 中被截断展示
> 关联：`internal/view/tui.go`

## 修复记录（2026-10-09）

按 issue 方案 A（直接移除，代码净减少）删除全部折叠相关代码：

- 删 `const foldMaxLines = 12`
- 删 `TUIModel.expanded` 字段
- 删 `renderBody()` / `foldBody()`（`renderMessages` 回到直接 `renderMarkdown(...)`）
- 删 `foldNoteStyle` 样式
- 删 `Update()` 的 `case tea.KeyCtrlO:` 分支
- `/help` 去掉 `Ctrl+O - Expand / fold long messages` 一行

长消息恢复**完整展示**；浏览长内容用 `PgUp/PgDn`、`Alt+↑/↓`、`Home/End`、滚轮。

测试：删掉 fold 相关用例，改为 `TestLongMessageIsNotFolded`（断言正文不含
`more lines` 且内容在）。

## 现象

TUI 里较长的回复**默认只显示前 12 行**，后面被替换成一行提示：

```
… 37 more lines (Ctrl+O to expand)
```

要按 `Ctrl+O` 才能看到全文。

用户反馈：

> "目前把消息收起来了，需要 ctrl+O 才能展示全部，**不要增加没有提到的功能**"

## 根因：新增了自动折叠机制，默认开启

`internal/view/tui.go` 顶部：

```go
// foldMaxLines bounds a collapsed message body. A reply longer than this is
// folded to this many lines with a "… N more lines" note until the user
// expands it (Ctrl+O).
const foldMaxLines = 12
```

`TUIModel` 新增字段（默认 `false` = 折叠）：

```go
// expanded shows every message in full. When false, long bodies are folded
// to foldMaxLines so one huge reply cannot bury the rest of the transcript.
expanded bool
```

渲染时无条件套用折叠：

```go
func (m *TUIModel) renderBody(content string, bodyWidth int) string {
	rendered := renderMarkdown(content, bodyWidth-2)
	if m.expanded {
		return rendered
	}
	return foldBody(rendered, foldMaxLines)     // ← 默认即折叠
}
```

`Ctrl+O` 用于切换（并追加一条系统提示）：

```go
case tea.KeyCtrlO:
	m.expanded = !m.expanded
	note := "Folded long messages."
	if m.expanded {
		note = "Expanded all messages."
	}
	m.addMessage(DisplayMessage{Role: RoleSystem, Content: note, Timestamp: time.Now()})
```

## 为什么这是问题

1. **未经要求新增功能**。原始需求里没有"折叠长消息"这一项。
2. **默认行为变了**：以前长回复完整展示，现在默认截断。用户需要主动发现
   并记住 `Ctrl+O` 才能看到全文——对不熟悉的用户等于**内容丢失**。
3. **与既有反馈方向相反**：此前用户反馈过「文字看不到后面」（见
   `ISSUE-tui-long-text-truncation`，已修换行问题），折叠又把内容藏起来了。
4. **`Ctrl+O` 的帮助文案不足以发现该状态**。`/help` 里只有一行：
   ```
   Ctrl+O       - Expand / fold long messages
   ```
   它**没有说明默认就是折叠态**，用户看不到「我现在看到的是被截断的」这一事实——
   只有当某条回复恰好超过 12 行、且用户注意到末尾那行灰色提示时，才可能察觉。
5. **折叠按行数硬切**（`foldMaxLines = 12`），不看内容语义——
  可能切在表格、代码块、列表中间，已渲染的 Markdown 结构被破坏。

## 期望行为

**默认展示完整内容**（回到折叠引入之前的行为）。

若确实需要控制超长消息，应作为**可选增强**而非默认：

- 要么默认 `expanded = true`，让折叠成为用户主动开启的选项；
- 要么不做折叠，用 `PgUp/PgDn`、滚轮、`Home/End` 浏览长内容（这些都已存在）。

**但按用户明确要求：先不加这个功能。** 回归"不增加未提及的功能"这一原则。

## 修复方向（未实施）

| 方案 | 做法 | 评价 |
|---|---|---|
| **A. 直接移除折叠** | 删掉 `foldMaxLines`、`renderBody` 的折叠调用、`foldBody`、`Ctrl+O` 分支、`expanded` 字段 | **推荐**。符合"不加未提及功能"，代码净减少 |
| B. 默认展开 | `expanded` 初值改 `true` | 保留功能但默认不影响行为；仍属于"多了没要求的功能" |
| C. 保留但换开关方式 | 用 `/fold` 命令替代 `Ctrl+O` 快捷键 | 同上，仍是多余功能 |

注意：`Ctrl+O` 若被移除，需检查它是否在别处也有绑定；当前仅此一处。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron
# 提一个会产生长回复的问题（如"介绍一下知识库结构"）
# 观察：回复是否只显示前 12 行 + "… N more lines (Ctrl+O to expand)"
```

## 相关代码位置

> 行号易漂移（该文件正在频繁改动），以符号定位。

| 定位 | 内容 |
|---|---|
| `const foldMaxLines = 12` | 折叠阈值 |
| `TUIModel.expanded` 字段 | 折叠状态（默认 `false`） |
| `renderBody()` | 折叠调用点：`if m.expanded { return rendered }` |
| `foldBody()` | 截断实现：`lines[:maxLines]` + `"… %d more lines (Ctrl+O to expand)"` |
| `Update()` 的 `case tea.KeyCtrlO:` | 切换开关 |
| `renderMessages()` | 调用 `renderBody` 的地方 |

## 一句话

新增的自动折叠（`foldMaxLines = 12`）让长回复默认只显示 12 行、须 `Ctrl+O` 展开，
属未经要求的功能且改变了默认行为；建议直接移除，回归完整展示。
