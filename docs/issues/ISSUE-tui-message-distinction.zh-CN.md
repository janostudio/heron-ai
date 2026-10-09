# 发现：TUI 用户消息与机器人消息难以区分

> 状态：**已修复**（2026-10-09）
> 影响面：`heron` TUI 交互界面，用户看不出哪条是自己说的、哪条是 agent 回复
> 关联：`internal/view/tui.go`（`renderMessages`、消息样式）、`internal/view/markdown.go`

## 现象

用户消息与助手消息**颜色几乎一样**（`252` 灰 vs `255` 近白），主要只靠用户行前面的
`> ` 前缀区分；助手正文没有任何前缀标识，多 agent 时只有一行 `[teamID]`。长对话里
很难快速分辨谁说了什么。

## 调研：主流 CLI 怎么区分

查了 Gemini CLI 源码（`UserMessage.tsx` / `GeminiMessage.tsx` / `theme.ts`）、
Claude Code、Aider。共性做法是**三重区分**，颜色反而是次要的：

| 维度 | 用户消息 | 助手消息 |
|---|---|---|
| 前缀符号 | `> `（accent 色） | `✦ `（Gemini）/ `⏺`（Claude Code） |
| 内容渲染 | 纯文本 | **Markdown 渲染**（代码块/列表/粗体各有样式） |
| 背景/边框 | 可选浅背景块 | 消息区分隔 |

Gemini CLI 实测（源码）：
- `UserMessage`：`prefix = '> '`，`color={theme.text.accent}`，可选 `background.message`。
- `GeminiMessage`：`prefix = '✦ '`，正文走 `MarkdownDisplay`。
- **两边正文颜色相同**——区分靠前缀 + Markdown，不是靠色相。

## 修复

`internal/view/tui.go` + 新增 `internal/view/markdown.go`：

1. **前缀 gutter**：用户 `>`（青 `39`），助手 `⏺`（绿 `42`），各占 2 列固定宽度，
   续行缩进对齐在正文下（`renderGutterMessage` + `lipgloss.JoinHorizontal`）。
2. **助手正文 Markdown 渲染**：引入 `charmbracelet/glamour`，固定 `dark` 样式
   （不用 `WithAutoStyle`——非 tty 下它会退化到 `notty`，把 `**`、`##` 原样露出）。
   渲染失败回退原文，绝不吞掉模型回答。
3. **padding 清理**（`trimMarkdownPadding`）：glamour 会把每行右侧用
   **ANSI 包裹的空格**补齐到 wrap 宽度，直接进 viewport 会撑宽行、和 gutter 打架；
   需要按"转义序列 / 可见 rune"逐字节扫描，丢掉末尾空格及其后的转义序列，
   但**不能按可见宽度 truncate**——那会把开头的转义序列从中间切断，漏出
   `8;5;252m` 这种残片（实测踩过）。收尾补 `\x1b[0m` 防止样式串行。
4. 多 agent 消息保持 `[teamID]` 头，正文套用助手样式（按用户选择"保持现状"）。

## 测试

- `render_test.go`：`TestRenderMessagesDistinguishesUserAndAssistant`（用户行 `>`、
  助手行 `⏺`、助手正文 `**` 被消费）、`TestTrimLineTrailingPadding*`
  （无残片、保留样式、补 reset、无 padding 行不动）。
- 受影响的旧测试断言加 `ansi.Strip`（markdown 会把词切进独立转义段）。

## 一句话

用户/助手颜色太近导致分不清；对齐主流 CLI 的做法——**前缀符号（`>` vs `⏺`）+
助手正文 Markdown 渲染**，颜色作辅助而非唯一手段。
