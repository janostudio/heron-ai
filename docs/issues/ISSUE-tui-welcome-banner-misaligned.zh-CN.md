# 发现：TUI 欢迎页框线错位，整体观感简陋

> 状态：**框线问题已修复**（2026-10-09）；"观感增强"部分仅落地了状态栏/agent 展示之外的范围
> 影响面：TUI 首次进入时的欢迎页；以及整体视觉观感
> 关联：`internal/view/tui.go`（`buildWelcome`、欢迎卡样式）

## 修复记录（2026-10-09）

**框线错位（P0）已修**：删掉硬编码的 ASCII 艺术 `welcomeBanner`（手工数空格 +
制表区块字符宽度不一致，6 行艺术比框宽 3 列、且进度行 `hist║` 粘连），改用
**lipgloss 自动绘框**（`welcomeCardStyle`，`lipgloss.RoundedBorder()`）：

- 框线宽度由库按真实显示宽度计算，**结构上不可能再错位**。
- 卡片随终端宽自适应：`inner = clamp(viewport.Width - frame, 20, 72)`，
  窄终端换行、宽终端不撑满（实测 40→34 列、60→54、100→74，逐行等宽）。
- 内容重排为轻量信息卡：`Heron AI` 标题 + 副标题 + `Flow/Model/Agents/Teams`
  一行 + 快捷键 + 复制提示。

**未做的部分**（issue 里列为 P1/P2 的"观感增强"，非缺陷）：
状态栏补 token/team、工具调用紧凑展示、team 边界分隔等——这些是增强项，
本次未纳入（消息区分已由 `ISSUE-tui-message-distinction` 的前缀+Markdown 解决）。

## 现象

用户反馈 TUI "太简陋，效果不太高级友好"。

其中**可客观验证的部分**：欢迎页的 ASCII 边框是**破的**——竖线不对齐。

## 实锤：框线错位 3 列

用真实显示宽度（`east_asian_width`，全角算 2 列）逐行测量 `welcomeBanner`：

| 行 | 显示宽 | 内容 |
|---|---|---|
| 2（顶边） | 48 | `╔══...══╗` |
| **4-9（ASCII 艺术）** | **51** | `║ ██╗ ██╗...  ║` ← **比框宽 3 列** |
| 10-15（文字区） | 48 | `║   ...   ║` |
| 16（底边） | 48 | `╚══...══╝` |
| 14 | 48 | `║   ...  ⌃P/⌃N hist║` ← **文字与右边框粘连** |

右边框 `║` 的实际列位置：

```
第 3 行:  列 47      ← 正常
第 4-9 行: 列 50      ← 错位 3 列（ASCII 艺术撑出去了）
第10-15 行: 列 47      ← 正常
```

**结果**：占 6 行的 "HERON" ASCII 艺术把右边框顶出去了，整个框看起来是断的。
第 14 行还有一处 `hist║` —— 少一个空格，文字直接粘在边框上。

## 根因

`welcomeBanner` 是硬编码的多行字符串常量（`tui.go:169-185`），
每行的字符数**靠手工数空格对齐**。

- 英文 + 制表符还好对齐
- 但 ASCII 艺术用了 `█` `╔` `═` 等**制表区块字符**，其显示宽度与普通字符不同
  （部分终端算 1 列、部分算 2 列），手工对齐必然出错
- 而且**完全不随终端宽度自适应**——终端窄了就换行错乱，宽了则框很小

## 其他"简陋"的具体表现（待确认，非缺陷）

以下未必是 bug，但同样影响观感：

| 项 | 现状 | 可改进 |
|---|---|---|
| 欢迎页 | 静态 ASCII banner + 一行 `Flow: x \| Model: y \| Agents: n \| Teams: m` | 去掉 ASCII 艺术，改为轻量的信息卡（框线用 lipgloss 自动撑宽） |
| 状态栏 | 左侧 `Model: x \| Round: n`，右侧 `Ctrl+C: quit \| /help: commands` | 加 token 累计、当前 team、耗时 |
| agent 输出 | 只显示 `[CallID]` 头 + 正文 | agent 名/角色、颜色区分（已有 `agentStyle(index)`，可扩展） |
| team 边界 | 无视觉区分 | 下一个 team 开始时加分隔线或标题 |
| 上下文压缩 | 仅事件，UI 无提示 | `context.compacted` 时提示"上下文已压缩" |
| 工具调用 | 完全不展示 | 可选展示工具名 + 结果摘要（现在只能事后看 jsonl） |

## 参考：成熟 TUI 的常见做法

| 项目 | 做法 |
|---|---|
| **Claude Code** | 问句用 `>` 前缀 + 浅色背景块；回复用 `⏺` 前缀；工具调用折叠显示一行摘要；下方状态栏常驻 model/token/成本 |
| **Aider** | 无全屏 TUI，直接流式打印 + 语法高亮；用分隔线标记轮次 |
| **Gemini CLI / Codex CLI** | 同上：行内前缀 + 颜色，不用大框；工具调用单独一行 `[tool] name ✓` |
| **lazygit / k9s** | 全屏 TUI，但框线**由库自动绘制**（lipgloss/bubbletea 的 `Border`），绝不手写空格 |
| **ollama / mods** | 极简：只用颜色与缩进区分角色 |

**共同点**：① 框线交给库画，不手写；② 前缀 + 颜色区分角色，而非大段 ASCII；
③ 状态信息常驻底栏；④ 工具调用有专门的紧凑展示形态。

## 建议方向（均未实施）

| 优先级 | 改动 | 说明 |
|---|---|---|
| P0 | **修 banner 错位** | 最省事是**删掉 ASCII 艺术**，改用 `lipgloss.Border` 自动撑宽的欢迎卡；或至少把 6 行艺术改成等宽的 |
| P0 | 欢迎信息随终端宽自适应 | 用 `m.width` 计算，而非硬编码 |
| P1 | 状态栏补 token / team | 数据已有（`m.totalUsage`、`m.roundNum`） |
| P1 | 工具调用紧凑展示 | agent 输出里插一行 `⏺ Bash ✓` 之类 |
| P2 | team 边界分隔 | 多 agent 协作时能看清谁在答 |

## 验证方式

```bash
# 复现框线错位：直接看 welcomeBanner 各行宽度
# 用 heredoc 避免 shell 引号与反引号冲突
python3 - <<'EOF'
import unicodedata
def w(s):
    return sum(2 if unicodedata.east_asian_width(c) in 'WF' else 1 for c in s)
src = open('internal/view/tui.go', encoding='utf-8').read()
lines = src.split('\n')
start = next(i for i, l in enumerate(lines) if l.startswith('const welcomeBanner'))
end = next(i for i in range(start + 1, len(lines)) if lines[i].strip() == '\`')
for i, l in enumerate(lines[start + 1:end], 1):
    print(f'{i:>3} {w(l):>6}  {l}')
EOF

# 观察实际渲染
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai && heron
```

实测：第 4-9 行宽度 51，其余 48 —— 差 3 列。

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `internal/view/tui.go:169-185` | `welcomeBanner` 常量（错位根源） |
| `internal/view/tui.go:635-639` | `buildWelcome()` 欢迎页拼装（banner + 一行 info） |
| `internal/view/tui.go:90-93` | `titleStyle`（header，未自适应宽度问题已由 `.Width(m.width)` 兜底） |
| `internal/view/tui.go:560` | `View()` 布局：header / viewport / status / input |
| `internal/view/tui.go:651` | `renderMessages()` 消息渲染（已有 gutter + Markdown，这部分做得不错） |

## 一句话

欢迎页的 ASCII 边框因手工对齐 + 制表符宽度不一致，6 行艺术比框宽 3 列导致右边框断裂；
建议改用 lipgloss 自动绘框并去掉硬编码 ASCII 艺术，同时把状态栏和工具调用展示补上。
