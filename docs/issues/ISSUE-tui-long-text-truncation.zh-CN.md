# 发现：TUI 中较长文本显示不全（硬截断）

> 状态：**已定位并修复**（2026-10-09）
> 报告人：用户（截图 + 澄清"被截断的是对话消息正文"）
> 影响面：`heron` 不带 `--prompt` 进入的 TUI 交互界面，任何长消息正文
> 关联：`internal/view/tui.go`（`renderMessages`、`FlowResult` 渲染）

## 追加修复：TUI 无法上下滚动，历史消息看不到（2026-10-09）

**现象**：消息多了以后，↑↓ 按了没反应，看不到上面的历史。

**根因**：`Update` 的 `KeyMsg` 分支里，`↑`/`↓` 被**输入历史导航**独占并在末尾
`return m, nil`，永远到不了第 459 行的 `m.viewport.Update(msg)`；同时没有
PgUp/PgDn/Home/End 处理，也没开鼠标。于是 viewport 根本无法滚动。

**修复**（按用户要求，对齐"业内 CLI 都用 ↑↓ 滚动"）：

- `↑`/`↓` 改为**无条件滚动消息区**（`viewport.LineUp/LineDown(scrollLines)`，
  `scrollLines=3`）。
- 输入历史改到 **`Ctrl+P` / `Ctrl+N`**（readline 风）。
- 新增 `PgUp`/`PgDn` → `HalfViewUp/Down`，`Home`/`End` → `GotoTop/Bottom`。
- 新增 `tea.MouseMsg` 分支：滚轮上/下滚动；`runTUIWithSession` 启用
  `tea.WithMouseCellMotion()`。
- 同步更新欢迎页、状态栏、`/help` 的键位说明。

**测试**：`tui_scroll_test.go` 的 `TestTUIModel_ArrowKeysScrollTranscript` /
`TestTUIModel_WheelScrollsTranscript` / `TestTUIModel_CtrlPNInputHistory`；
把 ↑↓ 改为早退后前者必失败（证明有效）。受影响的旧测试（`TestTUIModel_InputHistory`、
`TestSelfTest_History_Navigation`）已改用 `Ctrl+P/N`。

## 根因（已定位）

`bubbles/viewport v1.0.0` **不做软换行**：`SetContent` 只按 `\n` 拆行，
`visibleLines()` 对宽度超过 viewport 的行调用 `ansi.Cut(lines[i], xOffset, xOffset+w)`
**水平切掉**超出部分（见
`…/bubbles@v1.0.0/viewport/viewport.go:125-162`）。v0.18 时代的 `SoftWrap` 字段在
v1.0.0 已移除。

而 `internal/view/tui.go` 的 `renderMessages` 用
`assistantMsgStyle.Render(msg.Content)` 渲染消息——这些样式只有 `Padding(0,1)`，
**没有 `Width`**，所以整段消息被 lipgloss 渲染成**一行**。这一行交给 viewport 后，
超过 viewport 宽度的部分被 `ansi.Cut` 直接丢弃。**这就是"后半句看不到"**：
不是换行失败，是超出部分被硬切掉。

之前 issue 里"消息区正常换行"的结论是错的——那次最小复现用的是**带 `Width` 的
lipgloss 样式**（`lipgloss.NewStyle().Width(w)` 会自己换行），与真实 `renderMessages`
用的**不带 `Width` 的样式**不是同一条路径，所以没复现。

最小复现（已在修复前的代码上验证）：

```go
vp := viewport.New(28, 27)
vp.SetContent(assistantMsgStyle.Render(longCN)) // 样式无 Width → 单行
// 实测 vp.View() 只剩 " 先确认当前分支，然后需要切 "，其余全部丢失
```

## 修复

`renderMessages` 给每条消息渲染时显式加上 `.Width(...)`，让 lipgloss 在进入
viewport **之前**就按可视宽度换行：

- 带 `Padding(0,1)` 的样式（user / assistant / agent / usage）用 `width-2`
- `systemMsgStyle` 无 padding，用 `width`
- `width` 取 `m.viewport.Width`（≤0 时回退 80）

同时保留 resize 后重新换行的处理（见下），二者缺一都会在收窄终端时丢字。

## 修复记录（2026-10-09）

- `internal/view/tui.go` 的 `renderMessages`：消息样式改用 `.Width(...)` 渲染
  （`paddedWidth = viewport.Width - 2`；system 用整宽）。
- `internal/view/tui.go` 的 `WindowSizeMsg` 分支：resize 后重新 `renderMessages()`
  （有消息时）或重设欢迎页，避免旧宽度下换好行的内容被更窄的 viewport 切掉。
- 回归测试 `TestTUIModel_ResizeRewrapsMessages`：收窄终端后断言消息**头部与尾部
  关键词都还在**。把两处 `.Width(...)` 注掉后该测试必失败（只剩首段），证明有效。

---

## 现象

用户报告：TUI 里 agent 回复的较长文本，后半截看不到、也不换行到下一行，像被硬截断。用户在 TUI 界面看到一段文字被**硬截断**——后面的内容看不到，也没有换行到下一行。
截图（局部）显示的内容是：

```
...要切到  feat/134
```

用户描述："如果文字很长就看不到后面的内容"。

## 环境

| 项 | 值 |
|---|---|
| 入口 | `heron`（无 `--prompt`）→ `runTUI` → `tea.NewProgram(model, tea.WithAltScreen())` |
| 渲染代码 | `internal/view/tui.go` |
| 触发路径 | agent/assistant 消息正文（用户 2026-10-09 澄清） |
| viewport 版本 | `github.com/charmbracelet/bubbles v1.0.0` |

## 早期误判与更正（保留以免重复踩坑）

本 issue 起初标注"未复现"，并"排除"了几项；其中**第 3 项是错的**：

| # | 早期假设 | 早期结论 | 更正 |
|---|---|---|---|
| 1 | header 的 `.Width()` 硬截断 | ✗ 会换行 | 成立（header 确实换行，与此 bug 无关） |
| 2 | 中文宽度算错 | ✗ 算得对 | 成立 |
| 3 | 消息样式缺 `Width` 导致不换行 | ✗ "viewport 自身会补换行" | **错**。v0.18 的 viewport 会补换行，但项目已升到 **v1.0.0，不再补换行**，改为 `ansi.Cut` 硬切。这正是根因 |
| 4 | 高度账目错误 | ✗ 未超出 | 成立 |
| 5 | viewport 宽字符换行异常 | ✗ 换行正常 | 早期复现用了带 `Width` 的样式，与真实路径不同，结论无效 |

教训：最小复现必须**逐字复刻真实渲染路径**（`renderMessages` 用的样式没有 `Width`），
不能用"功能相近"的样式代替，否则会得出相反结论。

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `internal/view/tui.go` `renderMessages` | 消息渲染（已加 `.Width(...)`） |
| `internal/view/tui.go` `WindowSizeMsg` 分支 | resize 后重新 `renderMessages()`（已加） |
| `internal/view/tui.go` 消息样式定义 | 均带 `Padding(0,1)`、无 `Width`（宽度现由渲染时传入） |
| `bubbles@v1.0.0/viewport/viewport.go:125-162` | `SetContent` 只按 `\n` 拆行；`visibleLines` 用 `ansi.Cut` 切宽 |

## 一句话

`bubbles/viewport v1.0.0` 不再软换行而是把超宽行 `ansi.Cut` 硬切，而
`renderMessages` 的消息样式没有 `Width`、整段渲染成一行，于是长消息超出
viewport 的部分被直接丢弃；修复方式是渲染时显式给样式加 `.Width(...)`
让 lipgloss 先换行，并在 resize 后重新渲染。
