# 发现：command call 无法接收参数，导致用户指定的仓库被静默忽略

> 状态：**引擎侧已修复**（2026-10-09）；调用方配置（auto_bugfix/intake_team.yml）需自行接线
> 影响面：所有 command call（精确统计见下），其中 7 个完全无参、依赖脚本兜底
> 关联：`internal/runtime/call/policy.go`、`internal/runtime/call/command.go`、`docs/configuration/team.md`

## 修复记录（2026-10-09）

按 issue 推荐的"引擎支持 command 消费 SharedRecord"实施（根因修复）：

- `internal/runtime/call/policy.go` 扩展 `resolveTemplate`：在原有会话标识占位符
  之外，支持 **SharedRecord 引用**：
  - `${records.<Name>.Summary}` / `${records.<Name>}` → 记录的 `summary`
  - `${records.<Name>.Data.<field>}` → 记录 `data` 里的字段
  - `${records.<Name>.RecordID}` / `.Name` → 记录 ID / 名称
- `commandEnv` 与新增的 `resolveCommandArgs` 都做展开——**env 和 args 都能用**。
  因此 `acquire_slot` 可以写成
  `env: { AUTO_REPO: "${records.IssueCandidates.Data.repo}" }`，
  把用户指定的仓库经上游记录传进确定性命令。
- **fail-closed**：引用的记录/字段不存在 → 该 command call `failed`，**不替换成空串**。
  静默传空正是本 issue 要消灭的失败模式。`commandEnv`/`resolveTemplate` 因此改为
  返回 `(value, error)`，`CommandExecutor` 与 `WebhookExecutor` 相应处理。
- `webhook` 的 `idempotency_key` 模板一并对齐（同用 `resolveTemplate`）。
- 文档：`docs/configuration/team.md` 新增"Command 的参数与模板"一节，列出全部占位符
  与 fail-closed 约定。

**调用方仍要做的事**（不在引擎仓库）：让上游 agent 把目标仓库/分支写进记录
（如 `IssueCandidates` 的 `data.repo`），再把 `acquire_slot` 的 env 改为引用它。
只改引擎、不改配置时，命令仍走"无参 + `repos-config.json` 默认仓库"的老路。

### 测试

- `policy_test.go`：`TestResolveTemplateRecords`（Summary/Data/RecordID/数字/mixed）、
  `TestResolveTemplateRecordsMissingIsError`（5 种缺字段场景）、
  `TestResolveTemplateRecordsLastWins`、`TestResolveCommandArgs`、
  `TestCommandEnvRecordMissingFails`。
- `execute_coverage_test.go`：`TestCommandExecutorResolvesRecordTemplates`（env 端到端）、
  `TestCommandExecutorArgTemplateResolves`（args 端到端）、
  `TestCommandExecutorMissingRecordFails`。

## 现象

用户明确指定仓库，系统却选了另一个，且**无任何提示、退出码 0**。

会话 `fs_7a3e3c95f9b3fbf9` 第 4 轮：

```
用户输入：占用 genie-1，然后分支切换到 feat/134288-miniprogram-p0，了解这个分支做了什么
```

实际结果（`WorktreeSlot` record）：

```json
{"slot_id": "genie-agent-1", "repo": "genie-agent",
 "path": "repos/.pool/genie-agent-1", ...}
```

**要的是 `genie-1`，拿到的是 `genie-agent-1`。**

## 根因链（三环，缺一不可）

### 环节 1：heron 的 command call 不支持位置参数

`internal/runtime/call/policy.go:11-20`：

```go
func commandEnv(req types.CallRequest) []string {
	env := make([]string, 0, len(req.Call.Command.Env)+1)
	for name, value := range req.Call.Command.Env {
		env = append(env, name+"="+resolveTemplate(value, req))
	}
	if key := req.Call.Command.IdempotencyKey; key != "" {
		env = append(env, "HERON_IDEMPOTENCY_KEY="+resolveTemplate(key, req))
	}
	return append(os.Environ(), env...)
}
```

只组装 `command.env`（支持 `${flow_session_id}` 等占位符）+ `HERON_IDEMPOTENCY_KEY`。
**`inputs.flow_records` 的内容不会进入 env 或 argv。**

因此 `intake_team.yml` 里只能写成：

```yaml
acquire_slot:
  type: command
  command:
    command: "bash .agents/skills/worktree-guard/scripts/acquire.sh"   # 无参数
    env:
      HERON_FLOW_SESSION_ID: "${flow_session_id}"
```

传不进去"genie"这个信息。

### 环节 2：脚本兜底自己拍了一个默认仓库

`skills/worktree-guard/scripts/acquire.sh`：

```bash
REPO="${1:-${AUTO_REPO:-}}"        # ① 位置参数（空）
                                   # ② env AUTO_REPO（空）
# ③ 兜底：读 repos-config.json 的 is_default == true
if [ -z "$REPO" ]; then
    CONFIG="$ROOT/.codebuddy/repos-config.json"
    REPO="$("$NODE" -e '...取 is_default...' "$CONFIG")"
fi
```

`repos-config.json` 里：

```json
{"genie-agent": {"is_default": true, ...}}     // ← 唯一的 is_default
```

所以拿到 `genie-agent`。**脚本行为本身没错**（无参时用默认值是合理设计），
错在**用户的意图从未到达这里**。

### 环节 3：协调员理解了，但传不下去

协调员的 reason 显示它**完全理解用户意图**：

```
"reason":"第 1 次激活 intake：...且用户已明确指定仓库槽位 genie-1 与分支
feat/134288-miniprogram-p0（已授权占用与切换...）"
```

但它是 **agent**，表达意图的方式只有 JSON 里的 `teams: ["intake"]`——
**没有通道把"仓库=genie"传给下游的 command call**。command 是确定性的，
不读对话内容，也不接 agent 的输出。

## 影响面：7/10 个 command call 完全无参

| 文件 | call | 参数传递 |
|---|---|---|
| `audit_team.yml` | `snapshot` | env（仅 session id） |
| `intake_team.yml` | `acquire_slot` | env（仅 session id） ← **本次问题** |
| `intake_team.yml` | `setup_branch` | env（仅 session id） |
| `deliver_team.yml` | `pr_diff` | **无参无 env** |
| `diagnose_team.yml` | `sandbox_debug` | **无参无 env** |
| `fix_team.yml` | `similar_fixes` | **无参无 env** |
| `fix_team.yml` | `verify_write` | **无参无 env** |
| `learn_team.yml` | `dup_check` | **无参无 env** |
| `verify_team.yml` | `run_tests` | **无参无 env** |
| `verify_team.yml` | `git_status` | **无参无 env** |

其中 `acquire_slot` / `setup_branch` 的后果最直接：**用户指定的仓库和分支会被忽略**。

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 |
|---|---|---|
| 上游 command 生成参数 | 加一个 command call 先产出 `repo`/`task`（如从 IssueCandidates 提取），再由下一个 command 经 **env** 传入 | 不改引擎。但 heron 的 command 间无法动态传值到 env（模板只认固定占位符），**实际不可行** |
| 用 agent call 包装 | 把 `acquire_slot` 改成 agent call，让 agent 决定参数后调 Bash | 失去"确定性 command"的意义；但 agent 能读对话 → 能拿到"genie" |
| **引擎支持 command 参数** | `command` 支持 `args: []`，或 env 支持 `${record.xxx}` 取 SharedRecord 字段 | **根因修复**；需改引擎，是本议题的正解 |
| 拆成两个 call | `pick_repo`（agent，确定 repo）→ `acquire_slot`（command，从 env 读 `AUTO_REPO`） | 不改引擎；但 env 仍无法引用上游 agent 产出，**同样不可行** |

**唯一可行且彻底的是第三种**：让 command 能消费 SharedRecord 字段，
例如

```yaml
command:
  command: "bash .agents/skills/worktree-guard/scripts/acquire.sh"
  env:
    AUTO_REPO: "${records.WorktreeTarget.repo}"     # 目前不支持
```

## 为什么这次没被及早发现

1. 脚本**退出码 0**、产出完整 JSON，看起来是成功。
2. `acquire.sh` 的 head 注释写了 `2 = 参数不足`，但兜底让它从不触发。
3. 用户要自己去看 `WorktreeSlot` 的 `repo` 字段才发现不对。

## 规避方式（当下）

在 `intake_team.yml` 的 `acquire_slot` 里**显式写死** env：

```yaml
env:
  HERON_FLOW_SESSION_ID: "${flow_session_id}"
  AUTO_REPO: "genie"          # ← 按需改；但这样只服务单一仓库
```

这只适合"一段时间内只修一个仓库"的场景；要按用户输入动态选仓库，必须走上面的引擎修复。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 1. 看那次会话拿到的槽位（应为 genie-agent-1）
S=.agents/data/sessions/fs_7a3e3c95f9b3fbf9
jq -r 'select(.type=="command_turn.completed" and .call_id=="acquire_slot")
       | .payload.call_result.Reply' $S/team.jsonl | grep -o '"slot_id": "[^"]*"'

# 2. 看用户实际要求
jq -r 'select(.type=="flow_turn.started") | .payload.turn.input' $S/flow.jsonl \
  | grep genie-1

# 3. 看默认仓库配置
python3.12 -c "import json;print([k for k,v in json.load(open('../.codebuddy/repos-config.json'))['repos'].items() if v.get('is_default')])"

# 4. 确认 command call 未传参
grep -A6 'acquire_slot:' .agents/teams/intake_team.yml
```

## 一句话

`command call` 只能通过 `command.env` 拿固定占位符，**无法接收用户输入或上游记录**；
`acquire.sh` 因此在无参时按 `repos-config.json` 的 `is_default` 选了 `genie-agent`，
而用户要的 `genie` 从未进入这条链路——需引擎支持 command 消费 SharedRecord/env 动态绑定。
