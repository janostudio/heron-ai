# 缺陷：`internal/view` + `internal/sessionlist` 编译失败，两包测试全部无法运行

> 状态：**已修复**（2026-10-09）
> 影响面：`internal/view` 与 `internal/sessionlist` **两个包**编译不过，二者测试全部无法执行
> 关联：`HistoryTurn` / `Turn` 重构（`tui.go` / `history.go`）；两处测试文件未同步
> **这是当前最该优先修的一条**：它让两个包都失去测试保护

## 修复记录（2026-10-09）

属**重构过程中的窗口期**问题（源码已改、测试未同步），已在同一轮修复中同步：

- `internal/sessionlist/history_test.go`：3 处 `Turn{Reply: ...}` → `Turn{Messages:
  []TurnMessage{{Reply: ...}}}`；并新增 `TestHistoryCapturesCallIdentity` /
  `TestHistoryFallsBackToTeamReply` / `TestHistoryKeepsOpsOnlyCall`。
- `internal/view/tui_test.go`：5 处 `HistoryTurn{Reply: ...}` → `HistoryTurn{Messages:
  []HistoryMessage{{...Reply: ...}}}`；新增 `TestSeedHistoryPreservesIdentity`
  覆盖 `team › agent` 归属（issue 里的建议用例）。

验证（全部 exit 0）：

```
go build ./...            # ok
go vet ./...              # ok
go test -count=1 ./...    # 26 个包全 ok；view 149 个用例通过、0 失败
```

**教训（流程）**：数据结构重构（`Turn`/`HistoryTurn` 换形状）后，测试同步必须与
源码改动**同一次提交**完成，且每轮改动后应强制 `go test -count=1 ./...` 收尾，
不能让"某包 build failed"的窗口期存在。

## 现象

```bash
$ go test ./...
ok    ... 其余包正常
FAIL  github.com/heron-ai/heron-engine/internal/sessionlist [build failed]
FAIL  github.com/heron-ai/heron-engine/internal/view        [build failed]
FAIL
```

两个包共 **8 处编译错误**：

```
internal/view/tui_test.go:202:20: unknown field Reply in struct literal of type HistoryTurn
internal/view/tui_test.go:203:18: unknown field Reply in struct literal of type HistoryTurn
internal/view/tui_test.go:232:17: unknown field Reply in struct literal of type HistoryTurn
internal/view/tui_test.go:233:17: unknown field Reply in struct literal of type HistoryTurn
internal/view/tui_test.go:248:62: unknown field Reply in struct literal of type HistoryTurn

internal/sessionlist/history_test.go:29:39: unknown field Reply in struct literal of type Turn
internal/sessionlist/history_test.go:30:37: unknown field Reply in struct literal of type Turn
internal/sessionlist/history_test.go:45:42: unknown field Reply in struct literal of type Turn
```

**主代码可编译**（`go build ./...` exit 0），仅测试文件失配。

## 根因：`Turn` / `HistoryTurn` 重构后测试未同步

同一重构在两层各定义了一份结构，**两处测试都未同步**。

`internal/view/tui.go:1106` 现在定义：

```go
type HistoryTurn struct {
	Input    string
	Messages []HistoryMessage      // ← 原 Reply string 被替换为 Messages 切片
	Usage    types.TokenUsage
}

// HistoryMessage is one reply inside a HistoryTurn, carrying its origin.
type HistoryMessage struct {
	TeamID  string
	CallID  string
	AgentID string
	Reply   string                 // ← Reply 下沉到这一层
	Ops     []OpOutput
}
```

`internal/sessionlist/history.go:13`：

```go
type Turn struct {
	Input string
	// Messages are the replies produced by this input, one per call, carrying
	// their team/agent identity so a resumed transcript attributes each reply
	// the same way a live one does.
	Messages []TurnMessage      // ← 同样由 Reply string 改为切片
	Usage    types.TokenUsage
}

// TurnMessage is one call's reply inside a Turn.
type TurnMessage struct { ... }
```

而两处测试仍在用旧结构：

```go
model.SeedHistory([]HistoryTurn{
	{Input: "hello", Reply: "Hi there."},        // :202  Reply 字段已不存在
	{Input: "bye", Reply: "Bye."},               // :203
})
...
{Input: "q1", Reply: "a1", Usage: ...},          // :232-233
...
model.SeedHistory([]HistoryTurn{{Input: "earlier question", Reply: "earlier answer"}})  // :248
```

**注意**：这个重构方向本身是**正确的**——它正是
`ISSUE-tui-resume-identity-and-round-count`（问题 1）所要求的：
让历史消息携带 team/agent 归属。问题是**测试没跟着改**。

## 影响

1. **该包测试全废**。`go test ./internal/view/` 直接 build failed，
   任何回归都无法被这套测试捕获。
2. **与用户报的"↑/↓ 不回填历史"直接相关**：
   该行为**有**测试覆盖（见下），但因为包编译不过，**测试从未运行**——
   所以要么是功能真的坏了没被发现，要么是测试与实现同时失配。
   在修复编译前，**无法通过测试判断**。

```
internal/view/tui_scroll_test.go:60   TestTUIModel_CtrlPNInputHistory
internal/view/tui_scroll_test.go:79   TestTUIModel_ArrowKeysRecallInputHistory
internal/view/tui_scroll_test.go:101  TestTUIModel_ArrowKeyHistoryRespectsMultiLineCursor
internal/view/tui_test.go:196         TestTUIModel_SeedHistoryShowsPriorTurns
internal/view/tui_test.go:215         TestTUIModel_SeedHistoryEmptyIsNoOp
internal/view/tui_test.go:226         TestTUIModel_SeedHistoryRestoresUsage
```

## 修复方向

**同步测试到新结构**（不是回退重构）。

### `internal/sessionlist/history_test.go`（3 处）

```go
// 旧：{Input: "q", Reply: "a"}
// 新：
{Input: "q", Messages: []TurnMessage{{Reply: "a"}}}
```

### `internal/view/tui_test.go`（5 处）

```go
model.SeedHistory([]HistoryTurn{
	{Input: "hello", Messages: []HistoryMessage{{Reply: "Hi there."}}},
	{Input: "bye",   Messages: []HistoryMessage{{Reply: "Bye."}}},
})
...
{Input: "q1", Messages: []HistoryMessage{{Reply: "a1"}}, Usage: types.TokenUsage{...}}
...
model.SeedHistory([]HistoryTurn{{Input: "earlier question", Messages: []HistoryMessage{{Reply: "earlier answer"}}}})
```

顺带建议：**增加一条带 `TeamID` / `AgentID` 的用例**，以覆盖
`ISSUE-tui-resume-identity-and-round-count` 问题 1 的期望行为
（resume 后消息应显示 `team › agent`）。

## 复现

```bash
cd /Users/jahweijiang/Documents/github/multirole-agent/heron-ai
go test ./...                     # → sessionlist + view 两个包 build failed
go build ./...                    # exit 0：主代码可编译，仅测试文件失配
go vet ./internal/view/ ./internal/sessionlist/
```

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/view/tui.go` 的 `type HistoryTurn struct` | 新结构（`Input` / `Messages` / `Usage`） |
| `internal/view/tui.go` 的 `type HistoryMessage struct` | 新增的 call 粒度归属结构 |
| `internal/view/tui_test.go:202` / `:203` | 用旧 `Reply` 字段 |
| `internal/view/tui_test.go:232` / `:233` | 同上 |
| `internal/view/tui_test.go:248` | 同上 |

## 一句话

`Turn.Reply` / `HistoryTurn.Reply` 被重构为 `Messages` 切片（为携带 team/agent 归属，
方向正确），但 `history_test.go`（3 处）与 `tui_test.go`（5 处）的旧用法未同步，
导致 **`internal/sessionlist` 与 `internal/view` 两个包编译失败**、测试全部无法运行——
需先把两处测试改到新结构；在此之前这两个包的回归测试形同虚设。
