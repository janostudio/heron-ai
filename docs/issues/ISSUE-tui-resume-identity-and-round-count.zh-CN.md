# 发现：resume 后消息丢失 team/agent 身份；round 计数语义错误

> 状态：**已修复**（2026-10-09）
> 影响面：TUI 中消息归属标识（跨 resume）+ 轮次编号（所有场景）
> 关联：`internal/view/tui.go`、`internal/sessionlist/history.go`、`cmd/server/main.go`

## 修复记录（2026-10-09）

### 问题 1：resume 身份丢失 —— 已修

- `sessionlist.Turn` 改为按 call 粒度：`Messages []TurnMessage{TeamID, CallID,
  AgentID, Reply, Ops}`；`History()` 解析 `team_result.Turn.team_id` 与
  `team_result.CallResults[]`（按 call 名排序保证稳定）。无 `CallResults` 时
  回退到 `team_result.Reply` + `Turn.team_id`；空 Reply 但有 Ops 的 call 也保留。
- `view.HistoryTurn` 同步为 `Messages []HistoryMessage`；`SeedHistory` 改用
  **`RoleAgent`** 并填 `TeamName`/`AgentName`/`Ops`（复用 `historyMessages()`），
  与新消息渲染一致。
- `cmd/server/main.go` 的 `historyTurns()` 做 `sessionlist → view` 投影。
- 实测 auto_bugfix 会话：历史消息恢复为 `default › coordinator` 等，带 ops 摘要。

### 问题 2：round = 用户输入 —— 已修

- `m.roundNum++` 从 `flowResultMsg` 移到 **`tea.KeyEnter` 提交分支**，与
  `SeedHistory` 的 `len(turns)` 语义一致（都是"用户输入次数"）。
- `flowResultMsg` 不再递增；同一次输入触发的多个 team 共享同一 round。
- usage 消息改为 `Round N · X tokens this step (Total: M)`，避免同轮多条
  误读成"进度"。

测试：`history_test.go`（身份/回退/ops-only）、`agent_identity_test.go`
（`TestSeedHistoryPreservesIdentity`、`TestRoundAdvancesOncePerUserInput`）。

两个独立问题放在一份文档里，因为它们同属"TUI 展示的身份/轮次语义"，且都在
`SeedHistory` / `DisplayMessage` 附近。

---

## 问题 1：resume 后看不到是哪个 team / agent 回复的

### 现象

resume 一个多 agent 会话后，历史消息**全部以无身份的普通助手消息展示**——
看不出每条是哪个 `team` 的哪个 `agent` 产生的。而新产生的消息是有身份的
（形如 `diagnose › root-cause-analyst`）。

### 根因：`SeedHistory` 用 `RoleAssistant` 且不带身份字段

`SeedHistory()`：

```go
if strings.TrimSpace(turn.Reply) != "" {
	m.messages = append(m.messages, DisplayMessage{
		Role:      RoleAssistant,     // ← 无身份的通用助手角色
		Content:   turn.Reply,
		RoundNum:  i + 1,
		Timestamp: time.Now(),
		// 未设置 TeamName / AgentName
	})
}
```

对比新消息走的是 `RoleAgent` 且填了身份：

```go
m.addMessage(DisplayMessage{
	Role:       RoleAgent,
	CallID:     call.CallID,
	Content:    call.Reply,
	AgentName:  call.AgentID,      // ← 有身份
	TeamName:   team.TeamID,       // ← 有身份
	...
})
```

### 数据在落盘里是有的

`flow.jsonl` 的 `team_turn.completed` 带完整归属：

```
payload.team_result.Turn.team_id    ← team 名
payload.team_result.CallResults[]   ← 每个 call 的 AgentID / Reply / Ops
```

`evidence.jsonl` 的 `producer` 也有五元组（`team_id` / `call_id` / …）。

### 根因链

| 环节 | 现状 |
|---|---|
| `sessionlist.Turn`（`internal/sessionlist/history.go`） | 只有 `Input` / `Reply` / `Usage`，**无 team/agent** |
| `view.HistoryTurn`（`internal/view/tui.go`） | 同上，**无 team/agent** |
| `sessionlist.History()` | 只取 `team_result.Reply`（`teamTurnReply`），**未取 `Turn.TeamID` 与 `CallResults[]`** |
| `SeedHistory()` | 用 `RoleAssistant`，不设 `TeamName` / `AgentName` |

### 修复方向

1. `sessionlist.Turn` / `view.HistoryTurn` 增加按 call 粒度的归属：
   建议 `[]HistoryMessage{AgentID, TeamID, Reply, Ops}`（一次 turn 可含多个 call）。
2. `History()` 解析 `team_result.CallResults[]`（与 `Turn.TeamID`）填充上述结构，
   回退路径：无 `CallResults` 时用 `team_result.Reply` + `Turn.TeamID`。
3. `SeedHistory()` 改用 `RoleAgent` 并填 `TeamName` / `AgentName`，
   复用现有 `renderGutterMessage` 的身份头渲染（已实现，见问题 2 相关代码）。

---

## 问题 2：`round` 计数按"模型回复"递增，应为"用户输入"

### 现象

一次用户输入触发的多轮 agent 协作，被 SPLIT 成多个 round：

```
> 占用 genie-1，然后分支切换到 feat/134288-miniprogram-p0，了解这个分支做了什么
...
    Round 4        ← 协调员第一次回复
    Round 5        ← intake team 回复
    Round 6        ← 协调员汇报
```

**一次用户输入 = 3 个 round**，与用户理解不符。

用户反馈：

> "用户输入一次算一个 round，现在模型回复一次算一个 round，这个理解也不对"

### 根因：`roundNum++` 放在每次 `flowResultMsg` 里

```go
case flowResultMsg:
	...
	m.roundNum++          // ← 每次 agent 结果返回就 +1
```

而 `flowResultMsg` 在一次用户输入内会**触发多次**——每个 team 跑完都回一次
（`default` → 激活 `intake` → 回 `default` 汇报）。于是轮次被拆碎。

对照 `SeedHistory()` 的做法反而是对的：

```go
m.roundNum = len(turns)     // turns 是按"用户输入"配对的
```

即历史恢复用"用户轮次数"，新消息却用"回复次数"——**两套语义不一致**。

### 期望语义

**1 个 round = 1 次用户输入**（含该输入触发的全部 team/agent 协作）。

### 修复方向

| 方案 | 做法 |
|---|---|
| A. 在用户提交时递增 | 在 `tea.KeyEnter` 提交分支里 `m.roundNum++`，`flowResultMsg` 不再递增 |
| B. 用 session 的 turn 计数 | 从 `FlowResult` 取该 session 已完成的 flow_turn 数（`len(turns)` 同源） |

方案 A 更简单，且与 `SeedHistory` 的 `len(turns)` 语义天然一致
（每次提交 +1，恢复时按历史 turn 数设初值）。

### 注意连带影响

`roundNum` 还被用于三处，改动后需一并核对：

1. `DisplayMessage.RoundNum` —— 每条消息记录所属轮次
2. 状态栏 `Model: x | Round: n`
3. usage 消息 `Round %d: %d tokens (Total: %d)` —— **这条尤其要留意**：
   它当前打印的是"本次回复的 token"，若 round 改为按用户输入计，
   同一次输入内的多条 usage 会共享同一个 round 号，需要明确是否合并、还是各标一次。

---

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 问题 2：一次输入触发多 team
heron
> 占用 genie-1，然后分支切换到 feat/134288-miniprogram-p0，了解这个分支做了什么
# 观察：同一次输入出现了 Round 4 / 5 / 6

# 问题 1：resume 后身份丢失
heron --session <上面的 fs_id>
# 观察：历史消息无 team › agent 头，新消息有
```

---

## 相关代码位置

> 行号易漂移（该文件正在被频繁改动），以符号定位。

| 定位 | 内容 |
|---|---|
| `Update()` 的 `case flowResultMsg:` | `m.roundNum++`（问题 2 根源） |
| `Update()` 的 `case tea.KeyEnter:` | 用户提交分支（方案 A 的落点） |
| `SeedHistory()` | `m.roundNum = len(turns)`；用 `RoleAssistant` 且无身份（问题 1 根源） |
| `type HistoryTurn struct` | 只有 `Input` / `Reply` / `Usage` |
| `type DisplayMessage struct` | 已有 `TeamName` / `AgentName` / `Ops` 字段（可直接复用） |
| `sessionlist.History()` / `teamTurnReply()` | 只取 `Reply`，未取 `Turn.TeamID` 与 `CallResults` |
| `type Turn struct`（`internal/sessionlist/history.go`） | 只有 `Input` / `Reply` / `Usage` |
| `renderMessages()` | 消息渲染；`RoleAgent` 分支已实现身份头 |
| `statusLeft` 拼装 | `Round: %d` 状态栏显示 |
| usage 消息构造 | `fmt.Sprintf("Round %d: %d tokens ...")` |

---

## 一句话

`SeedHistory` 用无身份的 `RoleAssistant` 且 `HistoryTurn` 不含 team/agent 字段，
导致 resume 后历史消息丢失归属；`roundNum` 又在每次 `flowResultMsg`（每次 agent 回复）
而非每次用户提交时递增，使一次输入被拆成多个 round——两处都应改为按
「用户输入」与「call 粒度归属」建模。
