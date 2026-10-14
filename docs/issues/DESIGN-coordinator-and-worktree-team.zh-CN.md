# 设计：coordinator 独立设计 + worktree 独立 team

> 状态：**设计稿（待评审，未落地）** 2026-10-10
> 范围：`auto_bugfix/heron-ai/.agents/` 的 team/agent 职责划分
> 约束：不改 auto_bugfix（`.codebuddy/` 资产不动），只调 heron 侧配置
> 取舍：**先保证功能正确，暂不考虑性能**（LLM 往返次数、token 不作为设计约束）

## 一、割裂是什么

### 1.1 coordinator 不是 auto_bugfix 里的角色

| | auto_bugfix | heron 迁移后 |
|---|---|---|
| 谁和用户沟通 | 主 A（wx-78） | coordinator |
| 谁派发任务 | 主 A，派发 prompt 带"明确目标、范围、相关证据和预期产物"（`SYSTEM-PROMPT.md:47`） | coordinator，只输出 `next.teams` + `reason` |
| 谁干活 | subagent（岗位） | team / agent / command |
| 谁对整任务负责 | 主 A 保留整任务判断责任，核对实际产物 | 没有明确承担者 |

auto_bugfix 的 subagent 是被"带着上下文派发"的岗位；迁移时 specialist 的 prompt 照搬过来，
但**派发这一半（主 A 的职责）没有对应物**，coordinator 是新造的，它的契约没有被设计过。

### 1.2 已验证的断点：下游看不到协调员的意图

flow 绑定里带 `user_message` 的只有 `default / intake / diagnose / design`；
`CoordinatorReply` 只出现在 default 自己的输入里。其余 team
（`challenge / fix / verify / review / deliver / learn / audit`）既看不到用户原话，也看不到协调员的任务说明。

**具体后果（读代码推断，未实测）**：`deliver` 的授权判定（`teams/deliver/knowledge/delivery-playbook.md` §五）
第 1 条要求"用户明确说了提交/推送/评论，或上游 record 里记录了授权"。
但 deliver 的 flow 绑定没有 `user_message`（`internal/runtime/flow/runtime.go:1908 teamInput` 会把输入置空），
也没有任何 record 记录授权 → 它永远判未授权。上一版"授权判断下沉到 deliver 自己检查"不成立。

### 1.3 已实测的症状

用户说"对 genie-1 拉一下代码"：没有任何 team 具备这个能力，coordinator 只能反复 activate `intake`，
触发 `max_activation_retries` 后整轮 failed 且会话不可续聊（失败原因已修复落盘，见 commit `1dd0c9a`）。

### 1.4 worktree 能力散落

acquire / setup_branch 在 intake；`check.sh` 在 verify；`release.sh`、`list.sh`、`checkpoint.sh`、
`rollback.sh`、`preflight.sh`、`sync_primary_repo.py` 没有任何 team 调用。

## 二、coordinator 独立设计

### 2.1 定位

**面向用户的唯一入口 + 调度者**。它对应 auto_bugfix 主 A 的"沟通 + 派发"那一半；
"干活"那一半在 team。它不来自 auto_bugfix，所以它的契约要按 heron 的机制单独设计，而不是从 auto_bugfix 的 prompt 改。

### 2.2 它必须知道的 heron 事实

以下都来自 `docs/configuration/flow.md` 与引擎代码，应写进它的私有知识（取代现在 101 行的业务路由表）：

1. **动作**：`activate`（激活 team）、`proceed`（本轮向用户输出）、`return`、`coordinate`、`fail`、`wait_tool`、`wait_approval`。
   协调员常用的是 `activate` 和 `proceed`。
2. **谁能激活谁**：只有 `coordinator: true` 的 team 能 activate，目标必须在 `can_activate` 内。
3. **回流**：本配置里每个 team 都 `on_proceed: [default]`，team 结束后一定回到 coordinator。
4. **它看不到 team 内部过程**，只看得到 team 产出的 record 和 `TeamFailureReport`。
5. **失败语义**（`flow.md` 107-115）：`TeamFailureReport` 是执行结果，如实汇总，不自动重试，用户要求才重试。
6. **轮次生命周期**：`proceed` 后会话进入 `waiting_input`，用户可继续；
   但同一 FlowTurn 内对同一目标重复 activate 超过 `max_activation_retries` 会让整轮 `failed` 且不可续聊。
   因此判断标准是**"这次再派有没有新依据"**（新 record 或用户新指令），没有就 `proceed` 向用户说明，而不是空转。
   这是"没有进展就别重复"，不是限制次数，也不是替下游判断产出好坏。
7. **看不到的能力**：只有 `can_activate` 里的 team。用户要的事没有 team 能做，就如实告知，不要硬派。

### 2.3 对用户的职责

- 理解意图；歧义时提问（候选意图列出来让用户选）。
- 缺授权（提交/推送/评论/部署）时请求授权，并**把用户的授权如实记录下来**（见 2.4）。
- 外部阻塞（缺凭据、环境不可用）说明阻塞点和需要用户提供什么。
- 汇报先给结论，再给证据、未完成项、下一步；"未运行/受阻/跳过"不写成"通过"。
- 简体中文。

### 2.4 对 team 的职责：派发契约（弥合割裂的关键）

对应 auto_bugfix 主 A 的派发 prompt。每次派发要让下游知道：**目标、范围、相关证据、预期产物、授权状态**。

**提案**：coordinator 的结构化输出在现有 `reply` / `next` 之外加一个 `brief` 对象：

```json
{"reply": "...", "next": {...},
 "brief": {
   "goal": "本轮要达成什么（用户原意，不改写）",
   "scope": "仓库 / Issue / 分支 / 槽位",
   "authorization": {"commit": false, "push": false, "comment": false, "pr": false, "source": "用户原话引用或空"},
   "expected": "期望下游产出什么"
 }}
```

- 已验证的引擎行为：agent 结构化输出的字段会展开到 record `Data` 顶层（commit `de8f6ca`），
  所以 `brief` 会出现在 `CoordinatorReply.Data.brief`。
- 各下游 team 的 flow `inputs.flow_records` 加上 `CoordinatorReply`，下游 agent 读 `Data.brief`。
- `authorization` 只来自用户原话，由 coordinator 解析并记录；`deliver` 读它，不再依赖自己看不到的 `user_message`。
- **已验证（读 `internal/prompt/builtin.go:225 formatRecordData` + 临时单测）**：下游 agent 的提示里，
  record 的 Data 按 `- key: %v` 逐键输出，嵌套对象会变成 Go 的 map 字面量：
  `- brief: map[authorization:map[comment:false push:true source:用户说：可以 push] goal:推送分支]`。
  **信息完整，但格式是 `map[k:v]`，不是 JSON。** 所以：
  - 授权这类关键字段**不要嵌套得太深**；建议把 `authorization` 拍平成顶层的几个标量键
    （如 `authorize_push` / `authorize_comment` / `authorize_pr` / `authorization_source`），
    比 `map[...]` 更不容易被模型读错。
  - 下游 prompt 里要明确写"授权字段在 `CoordinatorReply` 的 `authorize_*` 键里"，不要指望模型自己从 map 字面量里猜。
- **仍未验证**：模型真的按这个读、并据此改变行为（需要端到端实验，见第五节第 1 项）。

### 2.5 路由

coordinator 做"理解任务"层面的粗路由，业务细节留在 team 里：

| 用户意图 / 当前 record | 派谁 |
|---|---|
| 寒暄、咨询、与流程无关 | `proceed`，直接回答 |
| 指定了 Issue / 要筛选候选 | `intake` |
| 要槽位、切分支、拉代码、看槽位、归还 | 对应 `worktree_*`（见三） |
| 报错、异常、卡住（Bug 类） | `diagnose` |
| 新增能力（Feature 类） | `design` |
| 已有诊断或设计、需要挑战 / 修复 / 验证 / 审查 / 交付 / 沉淀 / 复盘 | 按 record 进度选下一位 |
| 用户要求停止或只要结论 | `proceed` |
| 没有 team 能做 | `proceed`，说明缺什么 |

Bug/Feature 的粗判属于"理解任务"，留在 coordinator；`complexity` 由 diagnose/design 产出，coordinator 只据此选下一位。

## 三、worktree 独立 team

### 3.1 引擎硬约束（已验证）

`internal/runtime/team/spawn.go:129 readyCalls()`：一个 team 内所有 `depends_on` 已满足的 call **全部执行**；
`Call` 结构（`pkg/types/call.go:124`）没有条件字段。所以不能把多个动作放进同一个 team，否则想拉代码也会顺带申请槽位。

**采用：一个动作一个 team**，共 5 个：`worktree_acquire / worktree_status / worktree_sync / worktree_branch / worktree_release`，
每个只含一个 `command` call。纯确定性命令，不用 LLM。

### 3.2 各动作的真实入口（已逐个核对脚本）

| 动作 | 入口 | 必需参数 | 备注 |
|---|---|---|---|
| acquire | `worktree-guard/scripts/acquire.sh` | repo、task（env） | 缺省会兜底 |
| status | `worktree-guard/scripts/check.sh` | 无 | 只读快照，输出 JSON |
| sync | `.codebuddy/scripts/sync_primary_repo.py` | `--repo <slug>` | primary 唯一合法更新入口，必须单独调用 |
| branch | `branch-gate/scripts/create_fix_branch.sh` | `AUTO_BRANCH` 或 `AUTO_TASK` | 按 `HERON_FLOW_SESSION_ID` 匹配槽位 |
| release | `worktree-guard/scripts/release.sh` | 位置参数 `<slot_id> <lease_id>` | 必须带 lease，禁止 force-release |

### 3.3 command 输出不是结构化的

`WorktreeSlot` 的 data 只有 `{exit_code, passed, stderr, stdout}`，引擎不会展开 stdout 里的 JSON，
所以 release 要的 `lease_id` 无法用 `${records.WorktreeSlot.Data.lease_id}` 引用。

- **R1（建议）**：`release.sh` 无参时按 `HERON_FLOW_SESSION_ID` 查池，找到本会话持有的槽位和 lease。
  `create_fix_branch.sh` 已是这个做法，不动引擎。
- **R2**：引擎给 command 加"stdout 解析为 JSON 并展开进 Data"。通用，但是引擎改动。

### 3.4 两个已知的环境问题

- 槽位的"可写"只在发起它的 heron 进程存活期间成立（owner_pid 是引擎进程）。
  `acquire` 与后续写入必须在同一个 heron 进程里完成；`--prompt` 单轮结束后槽位变 `stale`。
  独立成 team 不改变这一点。
- git-lfs 会在槽位根目录写 4 个 hook 文件，使 `release` 被判 `worktree_dirty` 而失败。
  `create_fix_branch.sh` 已有清理，`release.sh` 需要同样的前置清理。

## 四、改动清单（落地时）

| # | 改动 | 文件 |
|---|---|---|
| 1 | 重写 coordinator prompt：定位、heron 事实、对用户与对 team 的职责、`brief` 输出 | `agents/coordinator/AGENT.md` |
| 2 | 重写协调员知识：heron 机制 + 极简路由（取代 101 行业务表） | `agents/coordinator/knowledge/` |
| 3 | `brief` 进入输出 schema | `agents/coordinator/AGENT.md` 的 `structured_output` |
| 4 | 下游 team 的 flow inputs 加 `CoordinatorReply`；deliver 读 `brief.authorization` | `flows/default.yml`、`agents/pr-creator/AGENT.md`、`teams/deliver/knowledge/delivery-playbook.md` §五 |
| 5 | 新建 5 个 worktree 单动作 team | `teams/worktree_{acquire,status,sync,branch,release}_team.yml` |
| 6 | intake 瘦身（移除 acquire_slot / setup_branch） | `teams/intake_team.yml` |
| 7 | flow 注册 5 个 worktree team | `flows/default.yml` |
| 8 | `release.sh` 无参兜底 + lfs 前置清理 | `skills/worktree-guard/scripts/release.sh` |

## 五、待定

1. **下游是否真按 `brief` 行事**（阻塞项）：渲染已验证（见 2.4），行为未验证。
   端到端实验需要：改 coordinator 输出 schema + 给 `deliver` 的 flow 绑定加 `CoordinatorReply` + 改 `pr-creator` 与
   `delivery-playbook.md` §五的授权判定依据，再跑一轮"用户明确授权 push"与"用户未授权"两个对照。
   注意 deliver 会走 `pr_diff`（需要 `--repo/--number`，当前 team 没给参数，会 exit 2），
   实验前要先处理这个，或只跑 `deliver` 这一个 call 的等价实验。
   读不到或不按行事的话，回退方案：在下游 `inputs` 里给 `user_message`，授权判定回到读用户原话。
2. **release 的 lease 来源**：R1 还是 R2（3.3）。
3. **跨进程续用槽位**：接受"槽位只在单个 heron 进程内有效"，还是只在 TUI/json-rpc 长会话里跑 worktree 相关流程。

## 六、与 auto_bugfix 的差异（设计意图）

| 维度 | auto_bugfix | 本设计 |
|---|---|---|
| 沟通与派发 | 主 A | coordinator（独立设计，不来自 auto_bugfix） |
| 派发上下文 | 派发 prompt（目标/范围/证据/预期产物） | `CoordinatorReply.brief` |
| 授权来源 | 主 A 持有用户原话 | coordinator 解析后写入 `brief.authorization` |
| worktree | 散在脚本与 scheduler | 5 个单动作 team，唯一入口 |

skill / 知识 / 门禁 / 脚本全部复用 auto_bugfix 现状，只重组调用关系。
