# 缺陷：TUI 无法恢复卡住的会话（有报错但无恢复入口，形成死锁）

> 状态：**从根因修复**（2026-10-09）——不修 recovery 入口，而是让"卡住"不再发生
> 影响面：任何处于 `running`（未正常收尾）的会话
> 关联：`internal/tool/executor.go`、`internal/tool/builtin.go`、`internal/tool/codenav.go`、`internal/workspace/exec_process.go`、`internal/app/runtime.go`

## 修复记录（2026-10-09）：让"卡死"不可能，而非让用户去 recovery

**结论**：不该让用户处理"会话怎么修"——要在**卡死的那一层**让失败成为**确定信号**，
这样会话根本不会停在 `running`。经过排查，根因是**工具调用没有兜底超时**：

| 工具 | 阻塞点 | 修复前 | 修复 |
|---|---|---|---|
| **Bash** | `workspace.Run` | `timeout_ms<=0` → `context.WithCancel`（**永不超时**）；模型不传就是无限 | `DefaultBashTimeout=10min`，未传也必有 deadline |
| **CodeNav** | `exec.CommandContext` + `CombinedOutput` | 只靠上层 ctx；且与 Bash 同样的"孙进程持有管道"坑 | 进程组隔离 + `Cancel` 杀整组 + `WaitDelay` |
| **WebSearch/Fetch** | `http.DefaultClient` | `Timeout=0`（无限） | `http.Client{Timeout: 30s}` |
| **MCP** | 独立子进程/HTTP | 已有 `DefaultCallTimeout=120s` | 无需改 |

**中心化兜底**：`internal/tool/executor.go` 的 `Execute`（**所有工具调用的唯一入口**）
在 ctx 无 deadline 时统一包一层 `DefaultToolTimeout=15min`；超时返回**确定的失败**
`ToolResult{Success:false, Error:"tool ... timed out"}`，agent 据此正常收尾
（`failed` / 继续），turn **不会永久 open**。工具自己的更短超时仍优先生效。

**连带修复**：`internal/workspace/exec_process.go` 导出 `SetProcessGroup` /
`KillProcessGroup`，供 `codenav.go`（在 workspace 后端之外自己起进程）复用同一套
"杀进程组"保证。

**为什么不做 TUI `/recover`**：recovery 只能事后收拾，且把"修会话"推给用户不合理；
根因（工具无界）修掉后，正常路径不会再产生 `running` 悬挂。引擎的 `Recover`
（HTTP API）保留作为运维兜底，但不是用户日常要碰的东西。

测试：`internal/tool/tool_test.go` 的 `TestCommandContextAlwaysHasDeadline` /
`...HonorsExplicitTimeout`；`internal/tool/executor_timeout_test.go` 的
`TestToolExecutorAppliesDefaultDeadline` / `...SurfacesContextDeadline` /
`...KeepsCallerDeadline`。

## 现象

在 TUI 里向一个卡住的历史会话发消息，直接报错：

```
Error: flow session "fs_7a3e3c95f9b3fbf9" has unfinished execution; use recovery status first
```

而状态栏显示：

```
odel: hy4-preview-ioa | Round: 7 | 472.7k tok                ↑↓ history | PgUp
```

**用户没有任何可操作的下一步** —— TUI 里既看不到怎么"use recovery status"，
也没有相应命令或按键。

## 根因链

### 1. 会话真的停在未完成状态

该会话（`fs_7a3e3c95f9b3fbf9`）的最后事件：

```
seq 158  flow_turn.waiting_input     ← 上一轮正常结束
seq 159  flow_session.updated        status=running
seq 160  flow_turn.started           ← 新的一轮开始
seq 162  team_turn.started  default
seq 172  team_turn.completed default
seq 175  team_turn.started  diagnose  ← 之后没有任何事件
```

`diagnose` 的 `team_turn` 有 `started` 无 `completed`，会话 status 停在 `running`。

这是已知问题 `ISSUE-bash-timeout-grandchild-hang` 的结果：
`root_cause` 发起全仓递归 grep，`exec.CommandContext` 只杀 `bash` 不杀孙进程，
`cmd.Run()` 永久阻塞；用户强杀进程后，**事件流里留下了一个永不收尾的 turn**。

### 2. 引擎正确拒绝了新消息

`internal/runtime/flow/runtime.go:162`（`Resume`）与 `:191`（`ResumeWithContext`）：

```go
if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
	return types.FlowTurnResult{}, fmt.Errorf(
		"flow session %q has unfinished execution; use recovery status first", sessionID)
}
```

**这个保护是合理的**：不允许在一个未收尾的 turn 之上再叠新 turn
（否则会叠加出多个 open turn）。

### 3. 但 TUI 没有恢复入口 —— 这是缺陷

```bash
$ grep -rn "recovery\|Recovery\|recover" internal/view/tui.go
（无结果）
```

recovery 目前**只有 HTTP API**：

```go
// cmd/server/main.go
mux.HandleFunc("/api/recovery/status", handler.HandleRecoveryStatus)
mux.HandleFunc("/api/recovery", handler.HandleRecover)
```

- CLI 没有子命令（`grep recovery cmd/server/main.go` 只命中这两行 HTTP 注册）
- TUI 没有命令、没有按键、没有提示
- JSON-RPC 也没有对应方法

**于是形成死锁**：TUI 报错要求 recovery，而 TUI 无法执行 recovery。

### 4. 引擎其实提供了能力，只是没接出来

`internal/runtime/flow/runtime.go` 已有完整实现：

```go
func (r *Runtime) RecoveryStatus(ctx, sessionID) (types.RecoveryStatus, error)   // :356
func (r *Runtime) Recover(ctx, sessionID, req types.RecoveryRequest) (types.FlowTurnResult, error)  // :380
```

只是 TUI / CLI 侧没有任何调用点。

## 影响面

| 场景 | 现状 |
|---|---|
| TUI 里遇到卡住的会话 | **只能用 HTTP API 或删目录** |
| `--session <卡住的> --prompt` | 同样报错，无出路 |
| 用户是否知道要发 HTTP 请求 | 不知道，也不合理 |

对普通用户而言，这个会话**等于永久损坏**。

## 修复方向

### A. TUI 增加恢复入口（推荐）

1. **报错时给出可操作提示**，而非只有引擎原文。例如把错误渲染为：
   ```
   该会话上次执行未收尾（diagnose/root_cause 卡住）。
   按 r 尝试恢复；若失败，输入 /recover 查看详情。
   ```
2. **新增 `/recover` 命令**：
   - 先调 `RecoveryStatus` 展示有哪些 interrupted 的 team/call
   - 再调 `Recover`（`types.RecoveryRequest`）执行恢复
   - 恢复后照常接受新输入
3. 若 recovery 不适用（如 turn 永远无法续跑），提供**明确的放弃选项**
   （类似 `failed`/abandon 语义），让会话回到 `waiting_input` 而非永久 `running`。

### 可用的恢复动作（引擎已定义）

`pkg/types/recovery.go` 提供了四种动作，UI 侧可据此设计：

| Action | 含义 |
|---|---|
| `inspect` | 只查看中断详情，不改状态 |
| `wait` | 等待（适用于其实还在跑的异步任务） |
| `retry` | 重试指定的 Team（`target_turn_id`，V1 以 Team 为单位，不重试单个有副作用的 call） |
| `coordinate` | 交回协调者处置 |

`RecoveryRequest` 另含 `allow_side_effect_replay`（重放可能重复副作用，
须显式开启）与 `reason`（留痕）。

对本次场景（诊断阶段卡死、无产出），`retry` 的 `target_turn_id` 应指向
`diagnose` 的 `tt_a45aae5655267974`；若用户只想继续对话，也可考虑
`coordinate` 让协调者重新决策。

### B. CLI 入口

加 `--recovery-status` / `--recover` 参数（与现有 `--session` 配合），
至少给一个不依赖 HTTP 的路径。

### C. 考虑"超时自动收尾"

卡死的根因是外部强杀导致事件流断裂。若能在下次启动时**自动检测并标记
这些 open turn 为 interrupted**（类似崩溃恢复），用户就不需要手动 recovery。
但这属于更深的运行时改动，且要避免误判仍在执行的 turn。

## 当下绕过方式

**HTTP API**（需要 `--serve` 模式）：

```bash
# 终端 A：启动 HTTP 服务
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai
heron --serve --port 8080

# 终端 B：查恢复状态
curl -s http://localhost:8080/api/recovery/status?session=<fs_id>

# 执行恢复
curl -s -X POST http://localhost:8080/api/recovery \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"<fs_id>", ...}'
```

（需确认 handler 的实际请求格式，见 `internal/view/handler.go:252` 的
`HandleRecoveryStatus` 与 `HandleRecover`。）

**或直接放弃该会话**：保留 `.agents/data/sessions/<fs_id>/` 留档，
新开会话继续（丢失上下文连续性）。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 1. 找停在不一致状态的会话（有 team_turn.started 无 completed）
for d in .agents/data/sessions/fs_*; do
  last=$(jq -r '.type' $d/flow.jsonl 2>/dev/null | tail -1)
  [ "$last" = "team_turn.started" ] && echo "$d → 卡在 $last"
done

# 2. 用 TUI 恢复它
heron --session <上一步的 fs_id>
# 输入任意消息 → 观察报错，且无恢复手段
```

实测对象：`fs_7a3e3c95f9b3fbf9`（最后事件 `team_turn.started`，team=diagnose）。

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `internal/runtime/flow/runtime.go` `Resume` / `ResumeWithContext` | 拒绝未收尾会话的两处检查（`:162`、`:191`） |
| `internal/runtime/flow/runtime.go` `RecoveryStatus` / `Recover` | 已实现的恢复能力（无 UI 调用点） |
| `cmd/server/main.go` 的 `/api/recovery*` 注册 | 唯一暴露路径（HTTP） |
| `internal/view/handler.go:252` `HandleRecoveryStatus` | HTTP handler |
| `internal/view/tui.go` | **缺恢复入口**（grep `recovery` 无结果） |
| `internal/view/tui.go` 的 per-turn 错误渲染 | 报错文案落点（当前直接显示引擎原文） |

## 一句话

卡住的会话让引擎正确拒绝新消息（"use recovery status first"），
但 recovery 只通过 HTTP API 暴露，TUI 与 CLI 都没有入口——
形成"被要求恢复却无法恢复"的死锁，用户只能发 HTTP 请求或删会话。
