# 发现：acquire.sh 无参调用会静默占用真实 worktree 槽位

> 状态：**已修复**（2026-10-09）
> 影响面：任何直接执行 `acquire.sh`（不带参数、不设 `AUTO_REPO`）的场景，包括手工排查
> 关联：`.agents/skills/worktree-guard/scripts/acquire.sh`、`.codebuddy/scripts/worktree_pool.py`

## 修复记录（2026-10-09）

已按"方向 1（靠 env 区分）"实施：在 `acquire.sh` 的三段兜底**之前**加了一道
fail-closed 守卫——**无位置参数、无 `AUTO_REPO`、且无 `HERON_FLOW_SESSION_ID`**
时打印错误 + 用法并 `exit 2`，不产生任何副作用。

- 手工排查/自检（裸调用）→ `exit 2`，不再占槽（实测验证）。
- team 的 command call（注入 `HERON_FLOW_SESSION_ID`）→ 守卫放行，兜底照常工作
  （实测：带 env 无参调用仍能走 repo 兜底正常申请槽位）。

注意 `acquire.sh` 是 auto_bugfix 仓库 `heron-ai/.agents/skills/worktree-guard/scripts/`
下的资产，不在引擎仓库内。

## 现象

`acquire.sh` 的兜底逻辑会**自行填满所有参数**，于是"无参调用"不是安全的自检，
而是真的去池里占一个可写槽位。

实测（排查时误跑了一次）：

```bash
# 没有任何参数，只想看看脚本行为
bash .agents/skills/worktree-guard/scripts/acquire.sh
# exit=0，无任何输出

# 结果：真的占了一个槽位
python3.12 .codebuddy/scripts/worktree_pool.py list --json
# genie-agent-1 | busy | task=heron-29815 | session=standalone:heron-29815:29815
#               | lease=WEtQnkmLEfiMl76WkJYKCYyDPIjB_uDq-avJCXhwBXQ
```

（该槽位已用 lease 释放，池已恢复。）

## 根因

`acquire.sh` 有三段兜底，任意一段都足以让调用成立：

```bash
REPO="${1:-${AUTO_REPO:-}}"          # 空 → 走下面的 repo 兜底
TASK="${2:-${AUTO_TASK:-}}"          # 空 → heron-${HERON_FLOW_SESSION_ID:-$$}
SESSION_ID="${3:-${HERON_FLOW_SESSION_ID:-}}"   # 空 → standalone:${TASK}:${OWNER_PID}

# task 兜底：永不为空
if [ -z "$TASK" ]; then
    TASK="heron-${HERON_FLOW_SESSION_ID:-$$}"
fi

# session 兜底：永不为空
if [ -z "$SESSION_ID" ]; then
    SESSION_ID="standalone:${TASK}:${OWNER_PID}"
fi

# repo 兜底：从 repos-config.json 取 is_default == true 的 slug
if [ -z "$REPO" ]; then
    CONFIG="$ROOT/.codebuddy/repos-config.json"
    ...
fi
```

所以只要 `repos-config.json` 里有 `is_default: true` 的仓库，无参调用必然成功占槽。
脚本头部注释写的 `2 = 参数不足` 在现实中不会触发。

## 为什么这是问题

1. **违反"无参数不产生副作用"的常识**。排查、自检、`--help` 性质的调用都会真的占槽。
2. **占用会持续到被回收**。默认回收条件是"不在执行 + 空闲 3 小时"，所以一次误调用
   可能让槽位 busy 数小时，影响后续任务。
3. **失败时不明显**。exit 0 且无输出（`worktree_pool.py` 把 JSON 写到 stdout，
   但脚本的调用方可能没打印），不留意根本不知道发生了副作用。

对比同类脚本的正确做法（fail-closed）：

- `risk-guard/check_write.sh`：参数不足 → 打印用法 + `exit 2`，无副作用 ✅
- `branch-gate/create_fix_branch.sh`：无目标 → `{"status":"skipped"}` + `exit 0`，无副作用 ✅
- `worktree-guard/acquire.sh`：无目标 → **真的占槽** ❌

## 设计意图与矛盾的边界

兜底本身是**有意加的**：heron 的 command call 无法传位置参数
（`commandEnv` 只传 `command.env` 声明的变量），所以 `intake_team` 里
`acquire_slot` 只能无参调用，必须靠兜底工作。

问题在于**同一套兜底同时服务了两个场景**：

| 场景 | 期望行为 |
|---|---|
| team 的 command call（无参，有 `HERON_FLOW_SESSION_ID`） | 用兜底正常占槽 ✅ |
| 手工排查 / 自检（无参，无 `HERON_FLOW_SESSION_ID`） | 拒绝，不产生副作用 ❌ 当前会占槽 |

## 可选修复方向（均未实施）

| 方向 | 做法 | 代价 |
|---|---|---|
| 靠 env 区分 | 无参且无 `HERON_FLOW_SESSION_ID` 时 fail-closed（`exit 2`）；有 env 才允许兜底 | 最小改动；team 场景不受影响（team 的 call 已注入该 env） |
| 显式确认开关 | 兜底需额外设 `AUTO_ALLOW_DEFAULT=1` 才生效 | 需同步改 `intake_team.yml` 加 env |
| 干掉 repo 兜底 | 只保留 task/session 兜底，repo 必填 | 会让 `intake_team` 无参调用失败 |

推荐第 1 种：`HERON_FLOW_SESSION_ID` 是"我在一次真实运行里"的可靠信号，
手工调用天然没有它。

## 规避方式（当下）

排查时**不要无参执行 `acquire.sh`**。要检查脚本行为，用不会占槽的方式：

```bash
# 语法检查
bash -n .agents/skills/worktree-guard/scripts/acquire.sh

# 看用法（会 exit 2，因为带了未知参数不会匹配任何分支前的 usage）
bash .agents/skills/worktree-guard/scripts/acquire.sh --help

# 只读查看池状态（无副作用）
bash .agents/skills/worktree-guard/scripts/check.sh
bash .agents/skills/worktree-guard/scripts/list.sh
```

若已误占，用 lease 释放（lease 必须是完整值，不能截断）：

```bash
LEASE=$(python3.12 .codebuddy/scripts/worktree_pool.py list --json \
  | jq -r '.slots[] | select(.id=="genie-agent-1") | .owner.lease_id')
bash heron-ai/.agents/skills/worktree-guard/scripts/release.sh genie-agent-1 "$LEASE"
```

## 一句话

`acquire.sh` 的 task/session/repo 三段兜底保证无参调用也能成功，于是"只想看看"
就会真的占用 worktree 槽位且 exit 0 无输出；应先判 `HERON_FLOW_SESSION_ID`
存在再启用兜底。
