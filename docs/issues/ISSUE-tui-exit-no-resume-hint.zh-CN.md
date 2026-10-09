# 发现：TUI 退出时不显示 resume 命令，会话 ID 无用户可见途径

> 状态：**已修复**（2026-10-09）
> 影响面：TUI 交互模式（`heron` 不带 `--prompt`）下，用户无法得知 session id，因而无法续聊
> 关联：`internal/view/tui.go:481`、`cmd/server/main.go:828-857`

## 修复记录（2026-10-09）

按 issue 列出的 3 处改动实施，贯通了 session id：

1. `internal/view/tui.go` 的 `FlowResult` 增加 `SessionID string` 字段。
2. `cmd/server/main.go` 构造 `FlowResult` 时带上 `SessionID: result.Session.ID`。
3. `internal/view/tui.go` 的 `TUIModel` 增加 `sessionID` 字段，在 `flowResultMsg`
   分支赋值；`View()` 的 `quitting` 分支在 `sessionID` 非空时输出
   `Goodbye!  Resume with: heron --session <fs_id>`，为空时仍只输出 `Goodbye!`
   （覆盖边界情况 1：一进来就 Ctrl+C）。

Ctrl+C 与 `/exit`、`/quit` 都走 `m.quitting`，两条路径一并生效。已补测试
`TestTUIModel_View_QuittingWithSessionID` 与 `TestTUIModel_FlowResultRecordsSessionID`。

## 现象

用户在 TUI 里聊完，按 Ctrl+C 退出时只看到：

```
Goodbye!
```

**没有任何地方告诉用户怎么回到这个会话。** 想续聊必须自己知道：

```bash
heron --session <fs_xxxxxxxx>        # ← session id 从哪来？
```

而 session id 在 TUI 里**从未展示过**——用户只能去翻
`.agents/data/sessions/` 目录猜哪个是最新的。

期望：退出时打印形如

```
Goodbye!  Resume with: heron --session fs_7a3e3c95f9b3fbf9
```

## 根因：session id 在传递链上被丢弃

数据其实**一直都有**，只是在两处断了：

### 断点 1：`FlowResult` 不含 session id

`internal/view/tui.go:25-30`：

```go
type FlowResult struct {
	Teams  []TeamOutput
	Status types.SessionStatus
	Usage  types.TokenUsage
}
```

没有 `SessionID` 字段。

### 断点 2：runner 收到了但没往外传

`cmd/server/main.go:828-857` 的 `sessionFlowRunner` **自己持有 session id**
并在每轮更新，但构造返回值时丢掉了：

```go
type sessionFlowRunner struct {
	runtime   types.FlowRuntime
	sessionID string          // ← 一直维护着
}

func (r *sessionFlowRunner) Run(...) (*view.FlowResult, error) {
	...
	if r.sessionID == "" {
		result, err = r.runtime.Start(ctx, types.StartFlowRequest{Input: input})
	} else {
		result, err = r.runtime.HandleInput(ctx, r.sessionID, input)
	}
	...
	r.sessionID = result.Session.ID      // ← 拿到了
	...
	return &view.FlowResult{
		Teams:  outputs,
		Status: result.Session.Status,
		Usage:  aggregateTeamUsage(result.TeamResults),
	}, nil                                // ← 但没带上 sessionID
}
```

### 断点 3：TUI 模型没有字段接它

`payload 结构`：`TUIModel` 只有 `flowName` / `modelName` / `agentCount` /
`teamCount` / `runner`，**没有 session 相关字段**（`grep sessionID
internal/view/tui.go` 无结果）。

### 结果：退出提示写死

`internal/view/tui.go:481-482`：

```go
if m.quitting {
	return "Goodbye!\n"     // ← 写死，无 session 信息
}
```

## 修复所需的改动（3 处，均很小）

| 位置 | 改动 |
|---|---|
| `internal/view/tui.go:25-30` | `FlowResult` 加 `SessionID string` |
| `cmd/server/main.go:855` | 构造 `FlowResult` 时带上 `SessionID: result.Session.ID` |
| `internal/view/tui.go` | `TUIModel` 加 `sessionID` 字段，在 `flowResultMsg` 分支赋值；`quitting` 时输出 resume 命令 |

退出提示建议同时覆盖两条路径（都走 `m.quitting`）：

- `tea.KeyCtrlC`（`:275-277`）
- `/exit` 的 `/quit` 分支（`:338-339`、`:361-362`）

## 边界情况

1. **会话尚未创建就退出**（用户一进来就 Ctrl+C）：`sessionID` 为空，
   此时应只输出 `Goodbye!`，不打印 resume 命令。
2. **退出时 turn 仍在跑**：此时 session 已创建但未结束。可考虑提示
   "会话仍在执行，稍后可用 `--session <id>` 查看"，或提示需等待落盘。
3. **`--session` 续聊后再退出**：应显示**同一个** id（`runner.sessionID`
   在 `HandleInput` 后仍是原 id），不应变化。

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `internal/view/tui.go:21-23` | `FlowRunner` 接口（`Run` 返回 `*FlowResult`） |
| `internal/view/tui.go:25-30` | `FlowResult` 定义（缺 `SessionID`） |
| `internal/view/tui.go:275-277` | Ctrl+C → `quitting = true` |
| `internal/view/tui.go:338-362` | `/exit` `/quit` → `quitting = true` |
| `internal/view/tui.go:383` | `flowResultMsg` 分支（可在此记录 sessionID） |
| `internal/view/tui.go:481-482` | 退出渲染（写死 `Goodbye!`） |
| `cmd/server/main.go:828-831` | `sessionFlowRunner`（已持有 `sessionID`） |
| `cmd/server/main.go:846` | `r.sessionID = result.Session.ID`（拿到但没传） |
| `cmd/server/main.go:854-858` | 构造 `FlowResult`（丢掉 sessionID） |
| `cmd/server/main.go:812-818` | `NewTUIModel(...)`（未传 session 能力） |

## 一句话

`sessionFlowRunner` 每轮都在更新 `sessionID`，但没有沿 `FlowResult` 传到
`TUIModel`，退出提示因此写死为 `Goodbye!`；补一个字段贯通三处即可让退出时
打印 `heron --session <fs_id>`。
