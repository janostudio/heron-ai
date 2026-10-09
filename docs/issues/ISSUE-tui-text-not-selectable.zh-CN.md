# 发现：TUI 文字无法划取复制

> 状态：**已按方案 A 处理**（2026-10-09）——保留鼠标捕获 + 界面提示修饰键拖选
> 影响面：TUI 交互模式（`heron` 不带 `--prompt`）下的所有输出文本
> 关联：`cmd/server/main.go:858`、`internal/view/tui.go`（`tea.MouseMsg` 处理、欢迎页/`/help`）

## 处理结论（2026-10-09）

**这不是本项目的缺陷，而是所有全屏 TUI 的通用权衡。** 调研了 opencode、Claude Code、
Gemini CLI 的实现与社区讨论（opencode issue #7926、以及"TUI 鼠标捕获导致无法复制"
的通行分析）：

- 全屏 TUI 默认都开启**鼠标捕获**（`?1000/1002/1003h`），因为 AltScreen 下**关掉捕获
  滚轮就失效**（终端会把滚轮拿去滚并不存在的滚屏历史）。
- 开捕获 = 滚轮能滚，代价 = **终端原生拖选被程序接管**。两者在纯终端里天然互斥。
- 行业通用逃生口：**按住 Shift（部分终端 Option/⌥）拖选**，绕过捕获走终端原生选择。
- opencode 也只是额外提供了**显式 opt-out 配置**（`tui.mouse: false`），默认仍是开捕获。

**本项目采用方案 A**：保持鼠标捕获（滚轮可滚，符合"↑↓/滚轮都能滚"的诉求），
在**欢迎页与 `/help` 里明确提示**按住 Shift/⌥ 拖选。**没有**引入 `/copy` 命令、
也没有加 `/mouse` 之类的开关（真实用户不会为了复制去输命令）。

改动仅在文案：
- 欢迎卡提示行：`To copy text, hold Shift (⌥ on some terminals) and drag with the mouse.`
- `/help` 的 Mouse 段：说明滚轮滚动 + Shift/Option 拖选。

**未采用**的方案及原因（供后续参考）：
- 关闭鼠标捕获（原生直接拖选）：赌终端行为，多数终端会丢滚轮，**不划算**。
- 自实现 TUI 内选区 + 剪贴板：工作量大、收益低。
- `/copy` 命令：与"划词复制"的真实诉求不符，用户不会用。

## 现象

TUI 里显示的文本**无法用鼠标拖选、因而无法复制**。用户想复制 agent 的回复或
命令输出时，拖拽没有反应。

## 根因：两个机制叠加，各自独立地阻止了原生选择

### 机制 1：`WithAltScreen()` 让内容不在终端滚动缓冲区

`cmd/server/main.go:858`：

```go
if _, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
```

AltScreen（备用屏幕缓冲区）是 TUI 全屏应用的常规做法，但它意味着：
- 屏幕上显示的内容**不在终端的主滚动缓冲区里**
- 退出 TUI 后终端恢复原样，那些内容消失
- 终端自身的"拖选 + 复制"依赖缓冲区，因此对 AltScreen 内容不适用

### 机制 2：`WithMouseCellMotion()` 把拖拽事件从终端抢走

同一行开了鼠标追踪。开启后终端**不再执行原生文字选择**，而是把所有鼠标事件
（含拖拽）转发给程序。

而程序只处理滚轮，其余一律吞掉：

`internal/view/tui.go:308-319`：

```go
case tea.MouseMsg:
	// Wheel scrolls the transcript. Only button/wheel events matter here;
	// motion is ignored. Enabled via tea.WithMouseCellMotion in runTUI.
	switch msg.Type {
	case tea.MouseWheelUp:
		m.viewport.ScrollUp(scrollLines)
		return m, nil
	case tea.MouseWheelDown:
		m.viewport.ScrollDown(scrollLines)
		return m, nil
	}
	return m, nil          // ← 拖拽等其余鼠标事件被吞，无任何处理
```

**结果**：拖拽既没有被程序利用（没有实现自己的选区），又阻止了终端的原生选择。
两头落空。

## 现状核实

| 检查项 | 结果 |
|---|---|
| 是否启用鼠标追踪 | ✅ `tea.WithMouseCellMotion()`（`main.go:858`） |
| 是否 AltScreen | ✅ `tea.WithAltScreen()`（`main.go:858`） |
| 是否处理拖拽/选区 | ❌ 只处理 `MouseWheelUp` / `MouseWheelDown` |
| 是否提供复制/导出功能 | ❌ 无 |

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 | 评价 |
|---|---|---|---|
| **`/copy` 命令** | 提供 slash 命令，把最后一条消息（或全部）写入系统剪贴板 | 小；需按平台选 `pbcopy`(macOS) / `xclip`·`wl-copy`(Linux) / `clip`(Windows) | **推荐**。TUI 内容多为长报告，拖选本就不便；命令式复制可精确、可跨平台 |
| 允许临时关闭鼠标 | 提供 `/mouse` 切换 `WithMouseCellMotion` 的生效状态，关闭后终端可原生拖选 | 需重建 `tea.Program` 或用 `tea.DisableMouse` 指令 | 中；用户需主动切换 |
| 提示修饰键拖选 | 在欢迎语/`/help` 里提示"按住 Shift（iTerm2/Kitty）或 Option 拖选" | 零代码 | 最省，但依赖终端；Terminal.app 等不完全支持 |
| 输出到文件 | `/export <path>` 把 transcript 写入文件，用户再用编辑器复制 | 小 | 适合长会话存档，不适合"复制一小段" |

**建议组合**：先做"提示修饰键拖选"（零成本，立刻缓解），再做 `/copy`
（根本解法）。

## 边界

- **`--prompt` 一次性输出不受影响**：那种模式内容直接打到 stdout、无 AltScreen，
  本来就是终端普通输出，可以正常拖选复制（可用 `heron --prompt "..." > out.txt`）。
  本 issue 仅限 TUI。
- `/copy` 若实现，需明确复制范围：当前视口内容 / 最后一条消息 / 整个 transcript。
  建议默认"最后一条 assistant 消息"，并提供 `/copy all` 复制全部。
- Windows 的 `clip` 需处理 UTF-8 编码，且长文本可能截断（`clip` 有缓冲区限制）。

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `cmd/server/main.go:858` | `tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())` |
| `cmd/server/main.go:904` | `--list` 选择器也用了 `tea.NewProgram`（未开鼠标，不受本问题影响） |
| `internal/view/tui.go:308-319` | 鼠标事件处理（只认滚轮） |
| `internal/view/tui.go:722` | `handleCommand`，新增 `/copy` 的落点 |
| `internal/view/tui.go:77-78` | slash 命令清单（`/help` / `/exit`），新增 `/copy` 在此注册 |
| `internal/view/tui.go:730-757` | `handleCommand` 的各命令分支 |

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron                                   # 进 TUI，随便聊一轮产生输出
# 用鼠标拖拽 TUI 中的文字 → 无选区、无法复制
# 对照：--prompt 模式的输出可正常拖选
heron --prompt "你好"                    # 这段可拖选
```

## 一句话

`WithAltScreen()` 让内容离开终端滚动缓冲区，`WithMouseCellMotion()` 又把拖拽
事件从终端抢走而程序只处理滚轮——两者叠加导致 TUI 文字既选不了也复制不了；
建议提供 `/copy` 命令作为根本解法。
