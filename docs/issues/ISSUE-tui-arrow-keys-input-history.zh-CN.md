# 需求：上下箭头应切换输入历史，而非滚动正文

> 状态：**已实现**（2026-10-09）
> 影响面：TUI 输入体验——与主流 CLI 的肌肉记忆相反
> 关联：`internal/view/tui.go`

## 实现记录（2026-10-09）

按键按 issue 建议重新分配：

| 键 | 现在行为 |
|---|---|
| **↑ / ↓** | 输入历史回填（**多行时先移动光标**：光标不在首行(↑)/末行(↓) 先在 textarea 内移动；在首/末行才回填） |
| **Ctrl+P / Ctrl+N** | 保留，等价输入历史（与 ↑/↓ 共用 `recallHistory()`） |
| **Alt+↑ / Alt+↓** | 滚动一行（新增） |
| **PgUp / PgDn** | 滚动半页 |
| **Home / End** | 跳顶 / 底 |
| **滚轮** | 滚动 |

- slash 下拉激活时 ↑/↓ 仍用于选命令（优先级最高，未动）。
- 抽了 `recallHistory(dir)` 供 ↑/↓/Ctrl+P/N 共用。
- 同步更新 `/help` 与欢迎页文案（`↑↓ history`、`Alt+Up/Down - Scroll transcript`）
  与状态栏右侧提示。

测试：`tui_scroll_test.go` 的 `TestTUIModel_ArrowKeysRecallInputHistory`、
`...AltArrowKeysScrollTranscript`、`...ArrowKeyHistoryRespectsMultiLineCursor`。

## 现象

TUI 里按 ↑ / ↓ 是**滚动对话正文**，而不是**回填之前输入过的消息**。

```go
// internal/view/tui.go（Update 的 KeyUp 分支）
case tea.KeyUp:
    ...
    // Arrow keys scroll the transcript, as in every other CLI pager.
    // Input history moved to ctrl+p/ctrl+n so the arrows are never
    // swallowed and long transcripts stay reachable.
    m.viewport.LineUp(scrollLines)          // ← 滚动正文
    return m, nil
```

历史输入被挪到了 `Ctrl+P` / `Ctrl+N`（`:402-419`）。

**这是有意改动**（注释写明了理由），但与用户预期和主流 CLI 习惯相反。

## 用户期望

> "有滚动以后，不应该有上下箭头，上下箭头应该是往输入框里填入之前用户输入的消息，
> 其他 cli 都是这么做的"

即：**↑/↓ 用于输入历史回填**，滚动交给别的键。

## 主流 CLI 的实际行为

| CLI | ↑ / ↓ | 滚动方式 |
|---|---|---|
| **bash / zsh** | 输入历史回填 | 无（终端自身滚动） |
| **Python REPL / IPython** | 输入历史回填 | 终端自身滚动 |
| **Node REPL** | 输入历史回填 | 终端自身滚动 |
| **Claude Code** | 输入历史回填 | 终端自身滚动（不用 AltScreen 接管） |
| **Aider** | 输入历史回填 | 终端自身滚动 |
| **Gemini CLI** | 输入历史回填 | `Ctrl+U/D` 或终端滚动 |
| **vim / less**（分页器） | 滚动 | `j/k`、`PgUp/PgDn` |

**结论**：↑/↓ = 输入历史，是 **REPL 类工具**的压倒性惯例；
把 ↑/↓ 用作滚动是**分页器（pager）**的惯例。

heron 的 TUI 本质是 **REPL**（有输入框、在对话），不是 pager——
用户敲命令的地方，↑ 就该调出上一条命令。用户判断正确。

### 作者改动的初衷也成立，但解法不当

原注释的理由：

> "arrows are never swallowed and long transcripts stay reachable"

即：担心 ↑/↓ 被 textarea 吞掉、长对话无法回看。这个担忧**真实存在**，
但解决方式不该是**占用 REPL 最核心的快捷键**，而应另配滚动键。

## 建议方案

### 按键重新分配

| 键 | 建议行为 | 说明 |
|---|---|---|
| **↑ / ↓** | **输入历史回填**（↑ 上一条，↓ 下一条） | 恢复 REPL 惯例 |
| **Ctrl+P / Ctrl+N** | 保持 = 输入历史（与 ↑/↓ 等价） | 兼容已习惯的用户，零成本 |
| **PgUp / PgDn** | 滚动半页 | 已有 |
| **Alt+↑ / Alt+↓** | 滚动一行 | 新增；`Alt+方向` 是不少 TUI 的滚动选择 |
| **Ctrl+U / Ctrl+D** | 滚动半页（备选） | 若 Alt 组合在部分终端不可靠 |
| **Home / End** | 跳到顶 / 底 | 已有 |
| **鼠标滚轮** | 滚动 | 已有（`WithMouseCellMotion`） |

关键点：**滚动不止一种方式**（滚轮、PgUp/PgDn、Home/End 都已存在），
所以释放 ↑/↓ 不会让"长对话不可达"。

### 需要一并处理

1. **`slashMode` 的优先级**：当前 ↑/↓ 在 slash 下拉激活时用于选择命令
   （`:422-428`、`:435-441`）。改成输入历史后，需保留"slashMode 时 ↑/↓ 选命令"
   的分支——这个**不能动**。
2. **多行 textarea 的光标优先**：**已确认 textarea 是多行**
   （`newTextarea()` 里的 `ta.SetHeight(3)`）。
   所以 ↑/↓ 需要两级判断：
   - 光标**不在**首行（↑）/ 末行（↓）→ 先在输入框内移动光标；
   - 光标**已在**首行（↑）/ 末行（↓）→ 才触发输入历史回填。

   这是 readline / PSReadLine 的标准行为（multi-line editing），
   需检查 bubbles textarea 是否已内建该逻辑，避免自行实现时与它的光标移动冲突。
3. **help 文案同步**（`:1006-1011`）：
   ```
   Up/Down      - Scroll transcript        →  Up/Down - Previous / next input history
   Ctrl+P/Ctrl+N- Previous / next input history  →  保留或去掉
   Alt+Up/Down  - Scroll transcript        →  新增
   ```
4. **欢迎语 banner**（`welcomeBanner`，含 `Up/Down scroll`）也要同步。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron
# 输入两条消息："第一条" / "第二条"
# 按 ↑ → 观察是滚动正文（当前行为）还是回填"第二条"（期望行为）
```

## 相关代码位置

> 行号易漂移（该文件正在被频繁改动），故以**函数 / 代码片段**定位。

| 定位 | 内容 |
|---|---|
| `Update()` 的 `case tea.KeyUp:` 分支 | 当前行为：`m.viewport.LineUp(scrollLines)`（滚动正文） |
| `Update()` 的 `case tea.KeyDown:` 分支 | 当前行为：`m.viewport.LineDown(scrollLines)` |
| `Update()` 的 `case tea.KeyCtrlO:` / `KeyCtrlP:` / `KeyCtrlN:` | 输入历史（`m.historyIdx` + `m.textarea.SetValue`），**应保留** |
| `KeyUp` / `KeyDown` 分支内的 `if m.slashMode {` | slash 下拉选择命令（**必须保留**） |
| `Update()` 的 `KeyPgUp` / `KeyPgDown` | 滚动半页（已有） |
| `Update()` 的 `KeyHome` / `KeyEnd` | 跳顶 / 底（已有） |
| `handleCommand` 的 `/help` 分支 | 快捷键文案（含 `Up/Down - Scroll transcript`，需同步） |
| `welcomeBanner` 常量 | 欢迎语里的 `Up/Down scroll`（需同步） |
| `newTextarea()` 附近的 `ta.SetHeight(3)` | 确认 textarea 是多行（决定光标优先级实现） |

## 一句话

当前 ↑/↓ 滚动正文、输入历史挪到 `Ctrl+P/N`，是把 REPL 当 pager 用；
主流 REPL（bash/zsh/Python/Node/Claude Code/Aider）都是 ↑/↓ = 输入历史，
应释放 ↑/↓ 用于历史回填，滚动交给 `PgUp/PgDn`、`Alt+↑/↓`、滚轮与 `Home/End`，
同时保留 slashMode 下 ↑/↓ 选命令的分支。
