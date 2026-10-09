# 发现：Bash 工具超时后不杀孙进程，导致 agent 永久卡死

> 状态：**已修复**（2026-10-09）
> 严重度：高——可让整个 FlowTurn 永久挂起，无超时、无失败事件、无错误提示
> 关联：`internal/workspace/workspace.go:370`、`internal/tool/builtin.go:195`

## 修复记录（2026-10-09）

按"方向 1（进程组隔离）+ 方向 3（显式 Cancel）"实施，与 `agent` 包 hook 超时
（commit `6a1cd81`）同一套做法：

- 新增 `internal/workspace/exec_process_unix.go`（`//go:build !windows`）：
  `setProcessGroup` 设 `Setpgid: true`，`killProcessGroup` 用 `syscall.Kill(-pid, SIGKILL)`
  杀整组；组 id 等于子进程 pid，不会误伤引擎自身进程组。
- 新增 `internal/workspace/exec_process_windows.go`：Windows 无 POSIX 进程组，
  退化为直接 Kill（已知限制，与 `internal/agent`、`internal/mcp` 一致）。
- `internal/workspace/workspace.go` 的 `Run`：`exec.CommandContext` 之后
  `setProcessGroup(cmd)`、覆写 `cmd.Cancel = killProcessGroup`、设
  `cmd.WaitDelay = 500ms`（脱离进程组的 setsid 孙进程兜底）。
- 回归测试 `TestServiceRunTimeoutKillsGrandchildren`（`sleep 30 & wait` 强制 fork）：
  **修复前 30.00s，修复后 0.50s**；已在 macOS 本地与 `golang:1.26` Linux 容器各验一次，
  并确认把三行修复注掉后测试必失败（证明测试有效）。

## 现象

`root_cause`（root-cause-analyst）agent 执行一条 Bash 命令后**永久卡死**：

```
team.jsonl
  seq=237  agent_turn.started   call=root_cause   at=16:18:47

agent.jsonl
  seq=281  tool_call.started    round=10  tool=Bash  at=16:24:03
  （之后无任何事件：没有 tool_call.completed / failed，没有 agent_turn.completed）
```

进程最终消失、无 `flow_turn.failed`——不是报错退出，是被外力中断（关闭 TUI）。

## 触发命令

agent 在第 10 轮调用了：

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix && \
grep -rn "get_default_repo\|is_default" \
  --include=*.py --include=*.sh --include=*.md --include=*.json . \
  | grep -v "^./repos/" | head -40
```

从 auto_bugfix **根目录递归全仓搜索**，只排除了 `./repos/`。实际扫描范围：

| 目录 | 文件数 |
|---|---|
| `.git` | 4883 |
| `.codebuddy` | 7021 |
| `knowledge` | 9364 |
| `logs` | 1755 |
| 其余 | ~700 |

合计 **2 万+ 文件**。本机复现该命令，**90 秒仍未结束**（被看门狗杀掉）。

## 根因：`exec.CommandContext` 不杀孙进程，且 `CombinedOutput` 会干等管道关闭

`internal/workspace/workspace.go:370`：

```go
cmd = exec.CommandContext(ctx, shell, "-c", req.Command)
```

`exec.CommandContext` 在 context 到期时只对**直接子进程**（这里是 `bash`）发
`SIGKILL`。`bash -c` 派生的**孙进程**（`grep`、以及管道里的 `grep -v`、`head`）
不在其杀死范围内。

更关键的是：`cmd.Run()` / `CombinedOutput()` 会等待**所有持有 stdout/stderr
写端的进程**结束。孙进程仍持有管道，于是即使 `bash` 已死，调用方仍会一直阻塞。

### 最小复现

```go
package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 模拟：bash -c 里派生孙进程，且孙进程持有 stdout
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", "sleep 30 & sleep 30")
	start := time.Now()
	out, err := cmd.CombinedOutput()
	fmt.Printf("超时设置=3s  实际耗时=%v  err=%v  ctx.Err=%v  输出=%q\n",
		time.Since(start).Round(time.Millisecond), err, ctx.Err(), out)
}
```

实测输出：

```
超时设置=3s  实际耗时=30.015s  err=signal: killed  ctx.Err=context deadline exceeded  输出=""
```

**超时设 3 秒，实际阻塞 30 秒**（= 孙进程 `sleep 30` 的自然结束时间）。
`ctx.Err()` 确实返回了 `deadline exceeded`，但因为阻塞在管道读取上，
超时**对调用方无效**。

## 为什么现有超时逻辑没能兜住

`internal/tool/builtin.go:195` 与 `workspace.go:397` 的超时判定本身是**正确的**：

```go
// tool/builtin.go
execCtx, cancel := commandContext(ctx, intParam(params, "timeout_ms"))
...
success := err == nil && result.ExitCode == 0 && !result.TimedOut && !result.Canceled

// workspace.go
timedOut := ctx != nil && ctx.Err() == context.DeadlineExceeded
```

问题不在**判定**，而在**阻塞**：`cmd.Run()` 不返回，后面的 `timedOut` 计算
永远执行不到。所以 `timeout_ms: 60000` 形同虚设。

## 影响面

- **任何含管道或后台子进程的长时间 Bash 命令**都可能触发。
  agent 很自然地会写 `... | head -40`、`... | grep -v ...` 这类管道。
- 一旦触发，**该 AgentTurn 永久挂起**：无超时、无 failed 事件、无用户可见错误。
- 上层 `max_team_turns` / `runtime` 限额也兜不住——因为它们按**轮次**计数，
  而这一轮从未结束。
- 用户只能强杀进程，会话留在"team_turn.started 无 completed"的不一致状态。

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 |
|---|---|---|
| 进程组隔离 | `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}`，超时后 `syscall.Kill(-pgid, SIGKILL)` 杀整个进程组 | 推荐。Unix 有效；Windows 需另写（`CREATE_NEW_PROCESS_GROUP` + `taskkill /T`） |
| 超时用独立 goroutine 兜底 | `cmd.Start()` + `select { case <-ctx.Done(): ...; case <-done: }`，超时时主动 `Process.Kill()` 并**放弃等待管道** | 较小改动，但需处理 goroutine 泄漏与延迟关闭 stdout |
| 用 `Cancel` 函数而非 context | 显式 `cmd.Cancel = func() error { return killProcessGroup(cmd) }`（Go 1.20+） | 与方向 1 配合，语义最清晰 |

方向 1 是根因修复：不改就永远有"孙进程逃逸 + 管道阻塞"的组合风险。

## 规避方式（当下）

给 agent 的 prompt/知识里加约束，避免全仓递归扫描：

```bash
# 不要（会扫 2 万+ 文件，且管道让超时失效）
grep -rn "pattern" --include=*.py . | grep -v "^./repos/"

# 要（限定目录 + 明确文件集）
grep -n "pattern" .codebuddy/scripts/worktree_pool.py
rg "pattern" .codebuddy/scripts/ --glob '!repos/'
```

`root_cause`（root-cause-analyst）、`explorer`、`issue-searcher` 这几个会大量用
Bash 的 agent 都该加这条约束。可放进全局知识 `.agents/knowledge/`。

## 复现步骤

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix

# 1. 观察扫描规模（会卡住，用 Ctrl+C 中断）
grep -rn "get_default_repo\|is_default" \
  --include=*.py --include=*.sh --include=*.md --include=*.json . | grep -v "^./repos/"

# 2. 看卡死会话的事件序列
S=heron-ai/.agents/data/sessions/fs_7a3e3c95f9b3fbf9
jq -r 'select(.type=="agent_turn.started" or .type=="agent_turn.completed")
       | "\(.seq) \(.type) \(.call_id)"' $S/team.jsonl | tail -3
# → 只见 started，无 completed

# 3. 看它最后调用的命令
jq -r 'select(.type=="tool_call.started") | "\(.seq) \(.payload.arguments.command)"
       | .[0:200]' $S/agent.jsonl | tail -1

# 4. 验证超时失效
cd /tmp && go run ctxkill.go     # 见上文最小复现
```

## 一句话

`exec.CommandContext` 只杀 `bash` 不杀它派生的 `grep`，而 `cmd.Run()` 会等
孙进程释放 stdout 管道，于是 `timeout_ms: 60000` 完全不生效——agent 一旦写出
全仓递归 + 管道的命令，AgentTurn 就永久挂起且无任何错误事件。
