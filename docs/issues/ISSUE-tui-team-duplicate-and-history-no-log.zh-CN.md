# 缺陷：team 名重复展示；输入历史回填疑似无效（缺日志难定位）

> 状态：**两个 bug 均已修复**（2026-10-09）
> 触发：用户实测
> 关联：`internal/view/tui.go`、`internal/view/tui_history.go`、`internal/view/tui_debug.go`

## 修复记录（2026-10-09）

### Bug 2 根因（用户补充后确认）：输入历史没有持久化

用户澄清：↑/↓ **能切换本次输入，但 Ctrl+C 重开后历史就没了**。所以根因不是按键、
不是 slashMode，而是 **`m.history` 只是内存 slice，从不落盘**——进程一退全丢。

修复：

- 新增 `internal/view/tui_history.go`：把输入历史持久化到
  `.agents/data/tui-history`（按工作区，像 shell 的 history；可用
  `HERON_TUI_HISTORY_FILE` 覆盖）。`appendHistory` 提交时写盘（**相邻重复去重、
  多行压成一行、上限 500 条**），`loadHistory` 启动时读回。
- **构造器保持无副作用**：`NewTUIModel` 不再读盘；改由入口
  `runTUIWithSession` 调 `model.LoadHistory()`。这样测试构造模型不会碰文件系统。
- 测试用 `TestMain` 把历史重定向到临时文件，避免污染仓库/互相干扰。

端到端验证：提交 "remember me" → 新建模型 `LoadHistory()` 读回 → ↑ 回填成功。

### （早前记录）Bug 2 的定位手段：TUI 调试日志

在确认根因前，按 issue 要求新增了可开关的 TUI 调试日志（保留，仍有价值）：
`HERON_TUI_DEBUG=1` 开启，写 `$HERON_TUI_DEBUG_FILE` 或
`.agents/data/logs/tui-debug.log`；`Update` 的 KeyMsg 入口记录
`key=… slashMode=… historyLen=… historyIdx=… line=… value=…`，
`recallHistory` 记录结果。

### Bug 1：team 重复 —— 已修（方案 A）

删掉 `renderTeamMarker`（`▸ team`）及其调用、`teamMarkerStyle`、`prevTeam` 状态：
`agentHeader` 输出的 `team › agent` 已含 team 名，再画一行 `▸ team` 属冗余。

两个 bug 合并在本文档，因为都在 TUI 展示/交互层，且第二个卡在"缺日志"上。

---

## Bug 1：team 名被重复展示（确认是 bug）

### 现象

相邻两行出现同一个 team：

```
▸ default                    ← team 边界标记
default › coordinator        ← agent header（本身就含 team 名）
```

用户反馈："在我看来重复展示 team 了。"

**确认是 bug。**

### 根因：两条独立机制都在画 team

`renderMessages()` 里有两个互不感知的输出点：

**① team 边界标记**（`renderMessages` 内）

```go
// Team boundary inside a round.
if msg.TeamName != "" && msg.TeamName != prevTeam {
	lines = append(lines, renderTeamMarker(msg.TeamName, width))   // → "▸ default"
	prevTeam = msg.TeamName
}
```

`renderTeamMarker`（`tui.go:951`）：

```go
func renderTeamMarker(team string, width int) string {
	label := fmt.Sprintf("▸ %s", team)
	return teamMarkerStyle.Width(width).Render(ansi.Truncate(label, maxInt(width, 1), ""))
}
```

**② `RoleAgent` 的 header**（同一个循环内）

```go
case RoleAgent:
	header := agentHeader(msg)                                       // → "default › coordinator"
	lines = append(lines, agentStyle(msg.AgentIndex).Width(paddedWidth).Render(header))
```

`agentHeader`（`tui.go:965`）：

```go
func agentHeader(msg DisplayMessage) string {
	switch {
	case msg.TeamName != "" && msg.AgentName != "":
		return fmt.Sprintf("%s › %s", msg.TeamName, msg.AgentName)   // ← 已含 team 名
	case msg.AgentName != "":
		return msg.AgentName
	case msg.TeamName != "":
		return msg.TeamName
	default:
		return fmt.Sprintf("[%s]", msg.CallID)
	}
}
```

**`agentHeader` 的输出已经包含 team 名**，再叠一行 `▸ team` 就是重复。

### 作者已意识到一半

`renderMessages` 的注释：

```go
// A team marker is suppressed for the very first team of a round when the
// round separator already names it; this keeps the transcript from being
// more chrome than content.
```

讨论中提到"round separator 已含 team 名"——但**当前 `renderRoundSeparator` 并不含 team**：

```go
func renderRoundSeparator(round, width int) string {
	label := fmt.Sprintf(" Round %d ", round)      // ← 只有轮次号，无 team
	...
}
```

所以这个"抑制"逻辑的注释与实际不符，且**没有处理"team 标记 vs agent header"这一真正的重复**。

### 修复方向

| 方案 | 做法 | 评价 |
|---|---|---|
| A. 去掉 team 标记 | 删 `renderTeamMarker` 调用；身份由 `agentHeader` 的 `team › agent` 承担 | **推荐**。team 信息不丢，少一行 chrome |
| B. `agentHeader` 去掉 team | header 只留 agent 名，team 由 `▸ team` 标记承担 | 也可；但 `team › agent` 的可读性更好 |
| C. 标记只在"无 call 明细"时画 | 有 `Calls` 时靠 header，无 `Calls` 时才画 `▸ team` | 折中，逻辑更复杂 |

方案 A 最简：`agentHeader` 已含 team，标记属冗余。

### 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron
# 提一个会跨 team 的问题（如"占用 genie-1 ..."）
# 观察：team 切换处出现 "▸ default" 紧接 "default › coordinator"
```

---

## Bug 2：输入历史回填疑似无效（代码正确，但实测不生效）

### 现象

- 状态栏显示 `↑↓ history`（说明快捷键已绑定）
- 但按 ↑ 后**输入框没有回填历史输入**

用户："还是无法获得历史输入，目前有没有日志，看起来一直没解决。"

### 已核实的部分（代码层面全部正确）

| 检查项 | 结果 |
|---|---|
| `KeyUp` 是否调用回填 | ✅ `m.recallHistory(1)` |
| `bin/heron` 是否为最新 | ✅ 构建于 20:33，晚于 `tui.go` 的 20:21 |
| 是否被多行判断挡住 | ✅ `if m.textarea.Line() > 0 { break }` —— 实测空输入与有值时 `Line()` 都返回 **0**，不会挡住 |
| `recallHistory` 逻辑 | ✅ 索引推进、`SetValue` 调用均正确 |
| `m.history` 是否被填充 | ✅ `KeyEnter` 分支：非空且非 slash 时 `m.history = append(m.history, input)` |

| 测试是否覆盖 | ✅ 有 `TestTUIModel_ArrowKeysRecallInputHistory` 等（`tui_scroll_test.go`） |

**结论**：静态检查找不到原因。可能是：

1. **终端把 ↑ 发成了别的序列**（部分终端/多路复用器下 `KeyUp` 不触发）
2. **焦点问题**：textarea 未聚焦时按键未进入该分支
3. **测试通过但真实交互路径不同**（如 `slashMode` 残留为 true，走 `:401` 的 slash 分支）
4. 其它运行期状态

### 卡点：TUI 没有日志

```bash
$ grep -n "logging\.\|log\." internal/view/tui.go
（无结果）
```

**`internal/view` 完全没有日志输出**，无法观察：

- 按键是否到达 `KeyUp` 分支
- `m.history` 的长度与内容
- `m.historyIdx` 的实际状态
- `m.slashMode` 是否为 true（会走错分支）
- `m.textarea.Line()` 的实际值

### 需要的日志（建议）

在 TUI 中加入**可开关的 debug 日志**（写文件，不污染 AltScreen）：

```
[tui] key=up slashMode=false historyLen=3 historyIdx=-1 textareaLine=0
[tui] recall -> idx=0 value="上一条输入"
```

落点：`~/.heron-connect/logs/` 或 `.agents/data/logs/tui-debug.log`，
由环境变量（如 `HERON_TUI_DEBUG=1`）开启。

这是定位 Bug 2 的**前提**——否则只能靠猜。

### 用户可先自查的两点

1. **确认按键真的触发**：`Ctrl+P` 是否有效？
   - `Ctrl+P` 与 ↑ 是**同一实现**（`:390`、`:400` 都调 `recallHistory(1)`）。
   - 若 `Ctrl+P` 有效而 ↑ 无效 → 是**终端按键序列**问题，不是代码。
   - 若两者都无效 → 是 TUI 内部状态问题（更可能是 `slashMode` 残留）。
2. **确认曾经提交过消息**：只有成功发送过（非 slash 命令）的消息才进历史。
   若全程只发过 `/help` 之类，历史是空的。

---

## 相关代码位置

> 行号易漂移，以符号定位。

| 定位 | 内容 |
|---|---|
| `renderMessages()` 的 team 边界判断 | `if msg.TeamName != "" && msg.TeamName != prevTeam`（Bug 1） |
| `renderTeamMarker()` | 画 `▸ team`（Bug 1 的重复源之一） |
| `agentHeader()` | 画 `team › agent`（Bug 1 的重复源之二） |
| `renderRoundSeparator()` | 只含 `Round N`，不含 team（与注释不符） |
| `Update()` 的 `case tea.KeyUp:` / `case tea.KeyCtrlP:` | 均调 `m.recallHistory(1)`（Bug 2） |
| `recallHistory()` | 索引推进与 `SetValue` |
| `Update()` 的 `case tea.KeyEnter:` 内 `m.history = append(...)` | 历史写入点 |
| `internal/view/tui.go` 全文 | **无日志**（Bug 2 的定位障碍） |

## 一句话

Bug 1 确认：`renderTeamMarker` 与 `agentHeader`（输出来自 `TeamName › AgentName`）各自都画 team，
team 切换时必然重复两行，建议删掉 team 标记；Bug 2 的代码静态检查全部正确、
二进制也是最新的，但 `internal/view` **完全无日志**，无法观察按键与历史状态，
需先加可开关的 TUI 调试日志才能定位。
