# 发现：resume 会话后 TUI 显示 Tokens: 0

> 状态：**已修复**（2026-10-09）
> 影响面：`heron --session <fs_id>` 或 `--list` 选择会话进入时，header 的 token 累计归零
> 关联：`cmd/server/main.go`（`runTUIWithSession`）、`internal/sessionlist/history.go`、`internal/view/tui.go`

## 修复记录（2026-10-09）

按 issue 列的三处改动实施：

- `internal/sessionlist/history.go`：`Turn` 加 `Usage types.TokenUsage`；`History()`
  在 `team_turn.completed` 分支解析 `team_result.Usage`（`teamTurnData()` 一次解码
  `Reply`+`Usage`；容器字段大驼峰、`Usage` 叶子 snake_case）。**reply 为空的 turn
  也累加 usage**，避免漏计。
- `internal/view/tui.go`：`HistoryTurn` 加 `Usage`；`SeedHistory` 累加进 `m.totalUsage`。
- `cmd/server/main.go`：resume 时把 `t.Usage` 一并传进 `HistoryTurn`。

语义按 issue 推荐：header 的 `Tokens` = **会话历史累计**（跨轮），resume 后继续对话
在此基础上叠加。实测 `fs_70a86c17e1120f38` 历史总消耗 4502（2217+2285），恢复后一致。

测试：`history_test.go` `TestHistoryCapturesUsage`；`tui_test.go`
`TestTUIModel_SeedHistoryRestoresUsage`。

## 现象

用 `--session <fs_id>` 恢复一个已有会话，TUI 顶部显示：

```
Heron AI - auto_bugfix_flow | Tokens: 0        ← 应为历史累计值
```

但**历史对话内容是正常显示的**（之前的问答都在），只是 token 从 0 重新计。
之后新增的轮次会在 0 的基础上累加，所以「显示值 = 本次运行的消耗」而非「会话总消耗」。

对照：同一个会话用 `--prompt` 续聊时，输出的 `Tokens:` 是该轮 flow 级加总
（见 `debug-triage` / `token-usage` skill 文档），也不含历史——但 TUI 的 header
语义是「本会话累计」，归零更容易被误读。

## 根因：resume 只恢复了显示文本，没恢复 token 累计

### 环节 1：恢复入口只传 Input / Reply

`cmd/server/main.go` 的 `runTUIWithSession`：

```go
if sessionID != "" {
	if turns, err := sessionlist.History(storage.NewFileStore("."), filepath.Join(sessionlist.SessionsDir, sessionID)); err == nil {
		history := make([]view.HistoryTurn, 0, len(turns))
		for _, t := range turns {
			history = append(history, view.HistoryTurn{Input: t.Input, Reply: t.Reply})
			//                                              ^^^^^^^^^^^^ 只有文本
		}
		model.SeedHistory(history)
	}
}
```

### 环节 2：`Turn` / `HistoryTurn` 本身就没有 usage 字段

`internal/sessionlist/history.go:12`：

```go
type Turn struct {
	Input string
	Reply string          // ← 无 usage
}
```

`internal/view/tui.go:975`：

```go
type HistoryTurn struct {
	Input string
	Reply string          // ← 无 usage
}
```

### 环节 3：`History()` 解析 flow.jsonl 时跳过了 usage

`internal/sessionlist/history.go:24-55` 只处理两类事件：

```go
switch event.Type {
case types.EventFlowTurnStarted:      // 取 input
case types.EventTeamTurnCompleted:    // 取 team_result.Reply
}
```

**`team_turn.completed` 的 payload 里其实带 `Usage`**（`team_result.Usage`，
含 `prompt_tokens` / `completion_tokens` / `total_tokens`），但解析时没取。

### 环节 4：`totalUsage` 是纯累加器，无初始化路径

`internal/view/tui.go:245`：

```go
totalUsage types.TokenUsage      // 零值起步
```

只在收到新的 `flowResultMsg` 时累加（`:509` 起）：

```go
m.totalUsage.PromptTokens += msg.result.Usage.PromptTokens
m.totalUsage.TotalTokens  += msg.result.Usage.TotalTokens
...
```

`SeedHistory`（`:983`）只填 `m.messages`，**完全不碰 `m.totalUsage`**。

## 数据其实已经落盘

`flow.jsonl` 的 `team_turn.completed.payload.team_result.Usage` 就是每轮的消耗，
历史累计完全可以重建：

```bash
S=.agents/data/sessions/<fs_id>

# 该会话历史总消耗
jq -s '[.[] | select(.type=="team_turn.completed")
        | .payload.team_result.Usage.total_tokens] | add' $S/flow.jsonl

# 逐轮
jq -r 'select(.type=="team_turn.completed")
       | "\(.payload.team_result.Turn.team_id) total=\(.payload.team_result.Usage.total_tokens)"' \
  $S/flow.jsonl
```

注意：TUI header 的 `Tokens` 应等于**所有 team turn 的加总**（跨轮累计），
而 `--prompt` 的 `Tokens:` 只统计**当前这一轮 FlowTurn**。两者语义不同，
修复时不要混淆。

## 修复方向（未实施）

| # | 改动 | 落点 |
|---|---|---|
| 1 | `sessionlist.Turn` 加 `Usage types.TokenUsage`；`History()` 在 `EventTeamTurnCompleted` 分支解析 `team_result.Usage` | `internal/sessionlist/history.go:12`、`:24` |
| 2 | `view.HistoryTurn` 加 `Usage`；`runTUIWithSession` 赋值 | `internal/view/tui.go:975`、`cmd/server/main.go:851` |
| 3 | `SeedHistory` 里累加 `m.totalUsage` | `internal/view/tui.go:983` |

改动小且局部，三处各几行。

### 需要一并决定的语义问题

- **header 显示什么？** 是「本会话历史累计」（推荐，与"Tokens: 0"的期望一致）
  还是「本次运行消耗」？若选前者，需明确：resume 后继续对话会在此基数上累加。
- **首轮 Header 位置**：TUI 在 session 未创建时 header 就是 `Tokens: 0`（正常）。
  resume 场景下应在 `SeedHistory` 完成后再渲染，避免闪一下 0。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 1. 跑一轮产生消耗
heron --prompt "你好"
# 记下输出里的 FlowSession: fs_xxxx

# 2. 进 TUI 恢复该会话
heron --session fs_xxxx
# 观察 header：显示 Tokens: 0（而历史对话内容可见）

# 3. 对照该会话的真实历史消耗
jq -s '[.[] | select(.type=="team_turn.completed")
        | .payload.team_result.Usage.total_tokens] | add' \
  .agents/data/sessions/fs_xxxx/flow.jsonl
```

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `cmd/server/main.go:851-854` | 恢复入口：构造 `HistoryTurn` 时只取 Input/Reply |
| `internal/sessionlist/history.go:12` | `Turn` 结构（无 usage） |
| `internal/sessionlist/history.go:24` | `History()`：解析时未取 `team_result.Usage` |
| `internal/view/tui.go:245` | `totalUsage` 字段（零值起步） |
| `internal/view/tui.go:509` 起 | 累加逻辑（只在新轮次触发） |
| `internal/view/tui.go:634` | header 渲染 `Tokens: %d` |
| `internal/view/tui.go:975` | `HistoryTurn`（无 usage） |
| `internal/view/tui.go:983` | `SeedHistory()`（只填 messages） |

## 一句话

resume 时 `sessionlist.History` 只从 `flow.jsonl` 取了 `input` 和 `team_result.Reply`，
`team_result.Usage` 被丢弃，而 `TUIModel.totalUsage` 是零值起步的纯累加器——
于是 header 显示 `Tokens: 0`；补 `Turn`/`HistoryTurn` 的 usage 字段并在 `SeedHistory` 里累加即可修复。
