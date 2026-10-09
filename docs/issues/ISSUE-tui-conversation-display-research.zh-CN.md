# 调研：TUI 会话展示优化（角色区分 / 工具折叠 / 信息密度）

> 状态：**全部完成**（2026-10-09）——2 个真缺陷 + P1/P2 增强项均已实现
> 触发：用户反馈"看不出哪个 team/agent 在回复，用户和模型回复区别不大"
> 关联：`internal/view/tui.go`、`internal/view/markdown.go`、`cmd/server/main.go`（TeamOutput 组装）

## 全部落地项（2026-10-09）

| 项 | 内容 | 落点 |
|---|---|---|
| P0-#1 | agent/team 身份：每 call 一条消息，头渲染 `team › agent` | `main.go` `callOutputs()`、`tui.go` `agentHeader()` |
| P0-#3 | 无空格长串硬换行（`ansi.Hardwrap`） | `markdown.go` `hardWrapLongLines()` |
| P1-① | 工具/命令输出折叠为一行摘要（`kind subject ✓/✗ exit N`） | `tui.go` `renderOpLine()`、`main.go` `opOutputs()` |
| P1-② | 状态栏补 team / 轮次耗时 / tokens | `tui.go` `View()`、`teamTurnDuration()` |
| P2-③ | 轮次分隔线 `──── Round N ────` | `tui.go` `renderRoundSeparator()` |
| P2-④ | team 切换标记 `▸ team` | `tui.go` `renderTeamMarker()` |
| P2-⑤ | 长消息折叠（>12 行）+ `Ctrl+O` 展开 | `tui.go` `foldBody()`、`foldMaxLines` |

### 实现要点

- **P1-① 工具摘要**：`CallResult.WorkspaceOps`（`Kind/Path/Command/ExitCode`）投影为
  `view.OpOutput`，每个操作一行；command 的 stdout 不再整段糊进正文。
- **P1-② 状态栏**：`Model | Team | Round | 耗时 | tokens`；耗时取
  `TeamTurn.FinishedAt - StartedAt`（`FinishedAt` 是指针，未结束则 0）。
- **P2-③④**：`renderMessages` 里按 `RoundNum` 变化插分隔线、按 `TeamName` 变化插
  `▸ team`（新一轮总是重新宣告 team）。
- **P2-⑤**：`foldMaxLines=12`，超出折叠并附 `… N more lines (Ctrl+O to expand)`；
  `Ctrl+O` 切 `m.expanded` 并重渲染。

### 测试

`internal/view/display_enhancements_test.go`：工具摘要（含截断）、状态栏字段、
轮次分隔+team 标记、折叠与 `Ctrl+O` 展开、短消息不折叠。
另见 `agent_identity_test.go`（P0）。

## 修复记录（2026-10-09，P0 部分）

本文档 4 点里，**2 个是真缺陷、已修**；1 个此前已修；1 个作者已自我更正为假象。

### 已修 P0-#1：agent/team 身份不可见

- `view.TeamOutput` 增 `Calls []CallOutput`（`{CallID, AgentID, Reply}`），
  `view.DisplayMessage` 增 `AgentName`/`TeamName`。
- `cmd/server/main.go` 新增 `callOutputs()`：从 `TeamTurnResult.CallResults`（map，
  按 call 名排序保证稳定）投影出每个 call 的 agent 与回复。
- `internal/view/tui.go` 的 `flowResultMsg`：**有 per-call 数据时每个 call 发一条消息**
  （原来是把 `team_result.Reply`——所有 call 回复 `\n\n` 拼接——当一条糊上去）；
  `renderMessages` 的 `RoleAgent` 头渲染为 **`team › agent`**（如
  `diagnose › root-cause-analyst`），缺哪边退化成哪边、都没有则回退 `[callID]`，
  复用已有 `agentHeaderStyles` 上色（`agentHeader()`）。

### 已修 P0-#3：无空格长串不换行（真缺陷）

`glamour.WithWordWrap` 只在**空格**处断行，`intake/diagnose/…/audit`、文件路径、
URL 这类无空格长串会渲染成超宽行，被 viewport（v1.0.0 不软换行）硬切。
`internal/view/markdown.go` 新增 `hardWrapLongLines()`：glamour 渲染后逐行检查，
超宽行用 `ansi.Hardwrap`（按显示列断、ANSI 安全）硬断。实测 72 列 → 断成
`…deliver` / `/learn/audit` 两行，内容不丢。

### 其余两点

- **#2 用户/助手同色**：已由 `ISSUE-tui-message-distinction` 修复（`>`青 vs `⏺`绿 +
  助手走 Markdown）。
- **#4 右边界被切**：作者已自我更正为"截图裁剪假象"，非缺陷。
- **P1/P2（工具输出折叠为一行摘要、状态栏补 token/team、team 分隔线）**：本次未做，
  属增强项。工具原始输出仍会进正文（但 command 的输出本就是确定性脚本 stdout，
  折叠会损失可读性，需单独设计）。

### 测试

- `agent_identity_test.go`：身份头渲染、`agentHeader` 退化、per-call 拆分
  （有 Calls 时不再显示聚合 Reply）、无 Calls 回退 team reply、
  `TestRenderMarkdownHardWrapsLongTokens`（注掉 `hardWrapLongLines` 必失败）。

## 问题（来自真实截图）

### 补充：完整截图确认的现象（2026-10-09 更新）

用户提供了**未裁剪的完整截图**，修正了先前基于局部截图的判断：

**已排除**：先前认为"右边界被硬切"——那是截图裁剪所致，**不是渲染缺陷**。
`renderMarkdown` 用 `glamour.WithWordWrap(width)`，普通文本按宽度正常换行。

**完整截图确认的真实表现**：

```
>  你是谁                                                  ← 用户：`>` 青色
我是 auto-bugfix 的主协调员（编排人格 wx-78）。我只负责调度：识别你的目标属于 Bug 还是 Feature，决定激活
intake/diagnose/design/challenge/fix/verify/review/deliver/learn/audit
中的哪一个团队，处理回退与重试预算，或在缺授权/有歧义时向你求证。…
>  提问：知识应该写在那个目录下                            ← 用户
知识库不是随便找个 md 写，按「作用域」选目录（依据 knowledge-usage.md 第三节）：
  1. 跨仓库/通用方法论的 pattern → knowledge/_shared/patterns-<主题>.md ，8 个主题按「根因所在层」选：
     sandbox-lifecycle / acp-prompt-pipeline / sse-streaming / session-state / llm-gateway-auth /
     observability-diagnosis（兜底）/ build-config-deploy / agent-tooling-workflow。注意 _shared/patterns.md
```

暴露四个问题：

| # | 现象 | 性质 |
|---|---|---|
| 1 | 用户与模型回复**正文同色**，仅行首 `>`(青) 与 `⏺`(绿) 不同 | 设计缺失 |
| 2 | 用户问句**无视觉容器**，长问句与正文混排 | 设计缺失 |
| 3 | 模型回复里**超长无空格串不换行**（见下） | **真实缺陷** |
| 4 | 无 team/agent 标识（此图恰好都是 `coordinator`，故未暴露） | 设计缺失 |

### 实锤：`glamour.WithWordWrap` 对超长无空格串失效

第 3 条是**可复现的渲染缺陷**。`internal/view/markdown.go:19`（`WithWordWrap` 在 `:33`）：

```go
renderer, err := glamour.NewTermRenderer(
	glamour.WithStandardStyle("dark"),
	glamour.WithWordWrap(width),        // ← 只按"空格"断行
	glamour.WithPreservedNewLines(),
)
```

实测（`wordWrap=60`，正文含 `intake/diagnose/design/challenge/fix/verify/review/deliver/learn/audit`）：

```
--- wordWrap=60 ---
  我是 auto-bugfix
  的主协调员。我只负责调度：识别你的目标属于 Bug 还是
  Feature，决定激活
  intake/diagnose/design/challenge/fix/verify/review/deliver/learn/audit    ← 76 列，未断
  中的哪一个团队。
```

**该串 76 列，远超设定的 60 列**——`glamour` 的 word wrap 只在**空格**处断行，
对 `a/b/c/d` 这类无空格长串不处理。

这在 heron 的场景下**必然触发**，因为 agent 的回复里常见：

- 枚举团队/agent：`intake/diagnose/design/challenge/fix/verify/review/deliver/learn/audit`
- 文件路径：`knowledge/_shared/patterns-<主题>.md`
- 命令：`python3 .codebuddy/scripts/check_pattern_dup.py --text "…" --validate-meta`
- URL、hash、长标识符

截图里正是这一类内容撑满整行。

### 以下四类问题（先前的分析，仍然成立）

一次 `diagnose` team 的运行，截图暴露四类问题：

### 1. agent / team 身份完全不可见

界面上只有 `[search]`、`[root_cause]` 这类 **call id**，看不出属于哪个 team、
由哪个 agent 产生。多 agent 协作时无法分辨"谁在答"。

**根因**：`DisplayMessage` 结构缺 agent/team 字段（`tui.go:58-67`）：

```go
type DisplayMessage struct {
	Role       MessageRole
	CallID     string       // ← 只有 call id
	Content    string
	RoundNum   int
	Timestamp  time.Time
	AgentIndex int
}
```

渲染时也只用它（`renderMessages`）：

```go
case RoleAgent:
	header := fmt.Sprintf("[%s]", msg.CallID)    // ← 只能显示 call id
	lines = append(lines, agentStyle(msg.AgentIndex).Render(header))
```

**而数据其实已在链路里**：`cmd/server/main.go:996` 组装 `view.TeamOutput` 时有 `team.TeamID`，
只是**没往 agent 级下传**：

```go
for _, teamResult := range result.TeamResults {
	outputs = append(outputs, view.TeamOutput{
		TeamID:  teamResult.Turn.TeamID,     // ← team 有了
		Reply:   teamResult.Reply,
		Records: teamResult.Records,
		// 缺：agent 名/角色（call_result.AgentID 存在但未传）
	})
}
```

有意思的是 `agentHeaderStyles`（5 种颜色，`tui.go:152`）**已经定义好了**，却因为拿不到 agent 名而只能给 call id 上色。

### 2. 用户与模型回复视觉差异过小

| 样式 | 前景色 | 实际效果 |
|---|---|---|
| `userMsgStyle` | 252 | 灰 |
| `assistantMsgStyle` | 255 | 灰白 |

**仅差 3 个色阶**（252 vs 255），多数终端上肉眼不可分。gutter 也不同
（`>` 青色 vs `⏺` 绿色），但截图中用户问句整行被正文淹没。

### 3. 工具/命令原始输出直接糊进正文

截图里大段内容是这样的：

```
{"slot_id": "genie-agent-1", "repo": "genie-agent", "path": "repos/...
"slot_snapshot": {"schema_version": 1, "slot_id": "genie-agent-1", ...
```

command call 的 **stdout 原样进入消息体**，几十行 JSON 挤占屏幕，
真正的结论（"intake 已跑完，但结果与你的要求不一致…"）反而被埋掉。

### 4. 右边界被硬切（**已修正**：局部截图的假象）

先前基于裁剪过的截图判断"右侧被切"。看到完整截图后确认：
`repos/.pool/geni`、`"consistency": "consi` 这类是**截图边缘切断**，非渲染缺陷。

**但仍有一处真实的行宽问题**：`glamour.WithWordWrap` 不处理无空格长串
（见上方"补充"一节的实测），会在视觉上造成"行宽不均"。

## 根因分析

### 宽度计算连续减 2（`renderMessages`）

```go
width := m.viewport.Width          // 已是 m.width - 2
paddedWidth := width - 2           // 减 Padding(0,1)
bodyWidth := paddedWidth - 2       // 再减 gutter "⏺ "（2 列）
... renderMarkdown(msg.Content, bodyWidth-2)   // 又减 2
```

**连续 4 次减 2**，每次都可能因取整/边框叠加而偏差。而 `RoleAgent` 的 header
（`fmt.Sprintf("[%s]", msg.CallID)`）**完全没走 `bodyWidth` 约束**，
长度取决于 call id，长 call id 必然溢出。

## 对标调研

| 项目 | 角色区分手段 | 工具调用展示 | 状态/上下文信息 |
|---|---|---|---|
| **Claude Code** | `>` 用户（青色块）+ `⏺` 助手；缩进层级 | 折叠一行 `⏺ Read(path) ✓`，可展开 | 底栏常驻 model / tokens / 成本 |
| **Aider** | `####` 分隔线 + 显式标注 `user:` / `model:` | 命令与输出逐行、有色区分 | 每轮尾部汇总 token/成本 |
| **Gemini CLI** | 彩色边框盒装用户输入；助手用 gutter | `└─ name ✓` 树形缩进 | 底部状态行 |
| **Codex CLI** | `›` 用户 + 助手首行缩进 | `• Ran command` + 缩进结果 | 顶部 header |
| **Cline** | 头像 + 名字 + 角色色块 | 可折叠卡片，**标题即摘要** | 侧栏任务状态 |
| **lazygit / k9s** | 分屏 panel + 标题栏 | 独立面板 | 顶/底栏 |

**共性四条**：

1. **角色靠结构区分**（前缀/边框/背景块），不只靠颜色深浅
2. **工具调用折叠**为一行摘要，详情按需展开——绝不把原始输出糊进正文
3. **每次发言都带说话者身份**（名字或角色标签）
4. **状态信息常驻**（token/成本/上下文占用），不占正文空间

## 优化方案（按性价比排序）

### P0 —— 直接解决"看不出谁在回复"

| # | 改动 | 落点 |
|---|---|---|
| 1 | `DisplayMessage` 增 `AgentName` / `TeamName`；header 渲染为 `team › agent`（如 `diagnose › root-cause-analyst`），复用已有的 `agentHeaderStyles` 上色 | `tui.go:58-67`、`renderMessages`（`tui.go:703`） |
| 2 | `main.go` 组装 `TeamOutput` 时带上 agent 名（`call_result.AgentID` 已存在） | `cmd/server/main.go:996` |
| 3 | 用户输入加**背景块**或换成醒目 gutter（如青色 `▌`），与助手拉开差异 | 样式定义 |
| 4 | 修宽度：**只算一次** `bodyWidth`，header 也套用同一约束 | `renderMessages`（`tui.go:703`） |
| 5 | **补长串断行**：glamour 后处理，对超宽行按显示宽度硬断（或对 `a/b/c` 在 `/` 处断） | `internal/view/markdown.go:33` |

### P1 —— 提升信息密度

| # | 改动 |
|---|---|
| 5 | command / tool 输出**折叠为一行摘要**（如 `⏺ acquire_slot → genie-agent-1 ✓`），完整输出存文件、正文只留关键行 |
| 6 | 状态栏补 `tokens` / 当前 `team` / 轮次耗时（`m.totalUsage`、`m.roundNum` 已有） |

### P2 —— 长会话可读性

| # | 改动 |
|---|---|
| 7 | 每轮之间加浅色分隔线 |
| 8 | team 切换时打印一行显著标记（如 `── diagnose ──`） |
| 9 | 长消息可折叠/展开（配合 `ISSUE-tui-text-not-selectable` 的 `/copy`） |

## 附：本次截图暴露的其他点

- **右侧被切**与 `ISSUE-tui-long-text-truncation`（viewport v1.0.0 不换行）**可能是同一问题的不同表现**——
  该 issue 已标记"已修复"，本截图若来自修复前版本则此项已解；若修复后仍溢出，
  则是上面"宽度连续减 2"的残余问题。
- 欢迎页框线错位见 `ISSUE-tui-welcome-banner-misaligned`。

## 验证方式

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron                                    # 进 TUI
# 输入会触发多 agent 的问题，例如：
#   "占用 genie-1，然后分支切换到 feat/134288-miniprogram-p0，了解这个分支做了什么"
# 观察：
#   1. 能否区分哪个 team/agent 在回复
#   2. 用户问句与模型回复是否一眼可分
#   3. command 输出的 JSON 是否糊在正文里
#   4. 是否有右侧被切的行
```

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `internal/view/tui.go:58` | `DisplayMessage` 定义（缺 agent/team 字段） |
| `internal/view/tui.go:111` / `:121` / `:152` | 角色样式（252 vs 255 差异过小）；`agentHeaderStyles` 已定义未充分利用 |
| `internal/view/tui.go:703` | `renderMessages()`（宽度连续减 2；header 无宽度约束） |
| `internal/view/tui.go:644` | `buildWelcome()` |
| `internal/view/tui.go:569` | `View()`（header / viewport / status / input 布局） |
| `cmd/server/main.go:996` | `TeamOutput` 组装（未下传 agent 名） |

## 一句话

TUI 目前只显示 `[call_id]` 而无 team/agent 身份、用户与助手仅差 3 个色阶、
command 原始 JSON 直接进正文、宽度连续减 2 导致右侧被切；
建议补齐 `DisplayMessage` 的 agent/team 字段、拉开角色视觉对比、折叠工具输出。
