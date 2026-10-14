# auto_bugfix → heron-ai：分层与流程（评估稿）

> 数据来源：`auto_bugfix/heron-ai/.agents/` 当前工作区（未提交）+ heron-ai 引擎源码，2026-10-14。
> 标记：**[已落地]** 配置已改；**[已核实]** 读引擎源码确认；**[未验证]** 只推断，没跑过。
> 评估时可直接说"第 X 点"。
>
> **⚠️ 修订（2026-10-14 晚）：owner 用「转交（activate）」而非「Spawn」。**
> 本文档最初按 Spawn 写，已被否决：Spawn 是引擎里 agent 派子 agent 的机制，不符合
> "围绕 heron-ai 协作能力建设"的方向。**owner 是一个 team，它用 `next.activate`
> 转交给开发域 team，开发域 team 结束后**自动回** owner（引擎的默认回程）**。
> 因此 Flow 里**保留** worktree/fix/verify/review/deliver 五个绑定（不是"移出 Flow"），
> 只去掉它们的 `on_proceed`——去掉后默认回激活者（owner）。下文相关章节已按此修订。

---

## 一、两个角色的划分（核心结论）

原先我把 coordinator 削成纯路由器，丢了原主 Agent 最关键的"整任务判断责任"。现在的划分是：

| | **coordinator** | **owner** |
|---|---|---|
| 定位 | 面向用户的唯一入口 + 引擎掌控者 | 一次复杂开发的统筹者 |
| 干什么 | 理解用户意图、派发、读会话回答进度、改配置 | 拿槽位和分支、**转交** specialist、核对产物、控制回退 |
| 何时出场 | 始终在 | 用户确定要**改代码**时才被派 |
| 手里有什么 | Bash / Read / Grep / Glob / Write / Edit / State + `heron-ai-config` skill | Read / Grep / Glob / State（**无 Bash**，靠 `next.activate` 转交） |
| 不做 | 不统筹开发、不碰 `repos/**` | 不直接和用户对话（要问就问协调员） |
| 私有知识 | 无（引擎规定：有私有知识就不给 Bash） | 无 |

**为什么 owner 由协调员派、且一次只派一个**：owner 是一个 team（`owner_team`，内含单 agent `owner`）。
它声明 `can_activate: [worktree, fix, verify, review, deliver]`，用 `next.activate` 把活转交给开发域 team；
开发域 team 结束后**默认回它的激活者（owner）**，owner 读回产物再决定下一步。协调员只派一次 owner，
此后整条开发链（含回退）由 owner 自己串。并行多个 owner 属于将来扩展，本轮不做。

---

## 二、总览

```
                            用户消息
                               │
                               ▼
        ┌──────────────────────────────────────────┐
   ┌───▶│  default / coordinator                   │◀────┐
   │    │  沟通 · 派发 · 读会话 · 改配置            │     │
   │    └────────────────┬─────────────────────────┘     │
   │                     │                               │
   │      ① 直接回答（proceed）                           │
   │         寒暄 / 目标不明→问用户 / 问进度→读会话        │
   │                                                     │
   │      ② 分析类：派 team                               │
   │         explore ‖ sandbox → diagnose | design        │
   │         → challenge                                  │
   │                                                     │
   │      ③ 开发类：派 owner（只派一次）                  │
   │         owner 用 activate 转交：worktree → fix →     │
   │         verify → review → deliver，每步回 owner       │
   │                                                     │
   └──── 分析域 team 结束后默认回 default；               │
         开发域 team 结束后默认回 owner；                 │
         owner 结束（done/blocked）后回 default ──────────┘
```

---

## 三、Flow 绑定（目标形态：14 个）

| # | 绑定 | Team | 角色 | 由谁激活 | 状态 |
|---|---|---|---|---|---|
| 1 | `default` | default_team | coordinator | —（entry） | **[已落地]** |
| 2 | `explore` | explore_team | explorer | default | **[已落地]** |
| 3 | `sandbox` | sandbox_team | sandbox-debugger | default | **[已落地]** |
| 4 | `diagnose` | diagnose_team | root-cause-analyst | default | **[已落地]** |
| 5 | `design` | design_team | feature-analyst | default | **[已落地]** |
| 6 | `challenge` | challenge_team | challenger + challenge-skeptic | default | **[已落地]** |
| 7 | **`owner`** | **owner_team** | **owner** | default | **[已落地]** |
| 8 | `worktree` | worktree_team | manage.sh | **owner** | **[已落地]** |
| 9 | `fix` | fix_team | code-fixer | **owner** | **[已落地]** |
| 10 | `verify` | verify_team | test-runner | **owner** | **[已落地]** |
| 11 | `review` | review_team | code-reviewer | **owner** | **[已落地]** |
| 12 | `deliver` | deliver_team | pr-creator | **owner** | **[已落地]** |
| 13 | `learn` | learn_team | knowledge-learner | default | **[已落地]** |
| 14 | `audit` | audit_team | audit-agent | default | **[已落地]** |

- 开发域（8–12）**保留在 Flow**，但**不在 default 的 `can_activate` 里**，只由 owner 激活；
- `coordinator: true` 字段已从引擎删除：**任何 team 只要声明 `can_activate` 就能转交**，team 之间平权，
  唯一的结构差异是 `entry`。default 的 `can_activate` 现为：
  `explore / sandbox / diagnose / design / challenge / owner / learn / audit`。

---

## 四、分析域：coordinator 直接派（不需要 owner）

你的判断是"只是排查不需要人控全局"，所以分析由协调员自己编排：

```
explore   explore[explorer]                              → ExplorationReport
sandbox   sandbox_debug[cmd] → sandbox_analyze[agent]     → SandboxDiagnosis / SandboxAnalysis
diagnose  root_cause[root-cause-analyst]                  → DiagnosisReport   （Bug）
design    design[feature-analyst]                         → DesignReport      （Feature）
challenge challenge[challenger] → skeptic[challenge-skeptic] → ChallengeReport / SkepticReport

典型编排（可用一次 activate 并行）：[explore ‖ sandbox] → diagnose|design → challenge
```

**代码源由协调员的 `source` 字段决定**：`primary` = 读 `repos/<slug>`（只读副本，分支未确定时一律用）；`slot` = 读已持有的槽位（分支已确定、在其上继续开发）。

**根因分析是一整条线**，不是散的：`explore`（代码地形）+ `sandbox`（运行时证据）→ `diagnose`（根因结论）。三者是可独立执行、可并行、可按需省略的步骤，合起来才是"根因分析"。

---

## 五、owner：开发域的统筹者

### 5.1 职责

对应原主 Agent 的四项：

| 原主 Agent 职责 | owner 落地方式 |
|---|---|
| 派发带明确目标、范围、证据、预期产物 | `OwnerReply.item` 就是给下游的任务单 |
| 保留整任务判断责任，核对实际产物 | 读下游 team 回流的 record（`ChangeSet` / `TestDecision` / `ReviewReport` …），判断是否推进；不合格就重转交 |
| 独立任务可并行，有依赖的顺序执行 | 一次 `activate` 可带多个 team（本轮用串行"等结果再决定"） |
| 恢复时核对目标、授权、工作位置 | 读 `.agents/data/sessions/` + 自己的 `State` |

### 5.2 手里的原语：`next.activate`（转交）

owner **是 agent，不是跑脚本的人**。它唯一的动作是输出 `next`：

```json
{"next":{"action":"activate","teams":["worktree"],"reason":"本会话还没有槽位，先拿槽位并切分支"},
 "worktree_action":"acquire_branch","repo":"genie","task":"issue-143462","branch":"fix/143462-p0","source":"slot","phase":"development"}
```

- 目标 team 必须在 owner 声明的 `can_activate` 里（`worktree` / `fix` / `verify` / `review` / `deliver`）；
  名字写错会被引擎 `validateActivation` 拒绝。
- 目标 team 执行完，**结果自动回 owner**（它是激活者），owner 在**新一轮**里读到产物再决定下一步。
- **owner 无 Bash**：拿槽位、切分支、跑测试这些机械动作本身就是 `worktree` / `verify` 两个 team 的职责，
  owner 只负责"什么时候该派它们"。

**owner 的输出契约（`OwnerReply`，Flow 级 record）**：`repo` / `task` / `branch` / `source` / `worktree_action` /
`phase` / `item` / `next`。其中 `worktree_action`（`acquire` / `branch` / `acquire_branch` / `sync` / `status` / `release`）
被 `worktree_team` 的 command `env` 直接引用（`${records.OwnerReply.Data.worktree_action}`），**这是开发域取参的唯一来源**。

### 5.3 顺序：一步一确认

owner 每次被唤醒都**先核对已有产物**（records 里有没有 `WorktreeState.ok=true` / 本轮 `ChangeSet` / 当前 revision 的
`TestDecision` / `ReviewReport` / `DeliveryReport`），据此决定下一个要派谁；**已经做完的步骤绝不重派**。
这就是"原主 Agent 的整任务判断责任"落到 owner 上的形式。

### 5.4 边界

- **要问用户**（缺授权、目标有歧义）→ `phase=blocked` 结束本轮，在 `reply` 里写明问题，由协调员转达；
  用户回复后协调员再派 owner 续做。
- **不碰不属于本次任务的仓库**：只动 `WorktreeState` 对应的槽位。
- **`fix`、`pr-creator` 的 HITL 约束**：**[已核实]** 引擎只在两类动作上要求审批——Bash 含
  `rm -rf` / `sudo ` / `shutdown` / `mkfs` / `git reset --hard`，或写 `.env`。这些动作发生在
  `fix` / `deliver` team 内部，审批请求会直达用户界面（不经过 owner 层层上抛）；owner 只需在
  `item` 里写明"避免这些命令"。**[未验证]** 子 agent 触发审批时的交互细节。

### 5.5 产出

owner 每轮产出一份 `OwnerReply`（Flow 级 record），协调员与后续轮次的 owner 都读它：

| 字段 | 含义 |
|---|---|
| `phase` | `development`（开发链推进中）/ `done`（闭环完成）/ `blocked`（需用户或上游介入） |
| `repo` / `task` / `branch` / `source` | 本次开发对象，供开发域 team 取参 |
| `worktree_action` | 要让 `worktree` team 做的动作 |
| `item` | 给下游 team 的自包含任务单 |
| `reply` | 自然语言说明（协调员转达用户的内容） |
| `next` | 下一步：`activate [team…]` 或 `proceed` |

**协调员只读 `phase` 决定要不要再介入**（`done`/`blocked` → `proceed` 汇总给用户；`development` → 本轮不介入，等引擎把结果送回 owner）。
详细的开发产物（改了什么、测试/审查结论）落在各开发域 record 里（`ChangeSet` / `TestDecision` / `ReviewReport` / `DeliveryReport`），
owner 在 `reply` 里汇总它们。

### 5.6 引擎侧的两个"平权"前提（本轮同步改了引擎）

1. **`coordinator: true` 字段删除**：任何 team 声明 `can_activate` 即可转交，没有特权位。
2. **回程按激活者**：team 结束（`proceed` 且无 `on_proceed`）回它的激活者；无激活者（entry）则等用户。
   这样 owner 转交开发域后，开发域自然回 owner——不需要任何 `on_proceed: [owner]` 声明。

---

## 六、开发链（owner 内部）

```
coordinator: activate [owner]，source=slot（或 primary，若分支未定）
  owner（每轮先核对已有产物，再决定转交谁）:
    1. 没有 ok=true 的 WorktreeState → activate [worktree]（acquire_branch）
       ← worktree team 结束回 owner，owner 拿到 WorktreeState
    2. 有槽位、无本轮 ChangeSet → activate [fix]，item=任务单（根因/方案、写边界、AC）
       ← fix team 内部：similar_fixes → code-fixer 改码 → check_write 写门禁；结束回 owner
    3. 有 ChangeSet、无当前 revision 的 TestDecision → activate [verify]
       ← verify team 内部：run_tests → test-runner 判收敛 → git_status；结束回 owner
       → 测试不过 → 回到 2 重派 fix（item 带上失败明细）
    4. 测试通过、无 ReviewReport → activate [review]
       ← review team 内部：code-reviewer 审 diff；结束回 owner
       → 审查不过 → 回到 2 重派 fix（item 带上审查要求）
    5. 审查通过、无 DeliveryReport → 检查授权；有 → activate [deliver]；无 → phase=blocked，问用户
    6. 交付完成（或用户只要分析结论）→ phase=done，proceed，reply 里汇总
  结束 → 回 coordinator（coordinator 读 phase=done 才收尾）
```

**回退**：测试失败或审查不通过 → owner 自己重转交 `fix`（item 里带上失败事实），不需要回协调员。

**收尾**：owner 报 `phase=done` 后，协调员按需派 `learn`（沉淀）/ `audit`（复盘）。

---

## 七、收尾域

```
learn   dup_check[cmd] → persist[knowledge-learner]   → DupCheckReport / KnowledgePersisted
audit   snapshot[cmd] → inspect[audit-agent]          → AuditSnapshot / AuditReport
```

这两处读 `ChangeSet` / `VerificationReport` / `ReviewReport` / `DeliveryReport`（开发域 team 发布的 Flow record）。
owner 不额外产出 `DevReport`——开发产物本身就在 Flow 里，收尾域直接读它们即可；owner 的 `OwnerReply.reply` 只是给用户的汇总。

---

## 八、协调员的路由（目标形态）

| 用户/状态 | 动作 |
|---|---|
| 寒暄、概念、与流程无关 | `proceed` 直接回答 |
| 问进度 / 卡在哪 / 花了多少 | `proceed`（先读 `.agents/data/sessions/<id>/` 再答） |
| 目标不明（没说处理哪个） | `proceed`，**问用户**，不自动挑 Issue |
| 要分析（Bug / Feature），分支未定 | `activate [explore, sandbox]` → 之后 `[diagnose]`/`[design]` → `[challenge]` |
| 分支已确定、在其上继续分析 | 同上，`source=slot` |
| **要改代码 / 加功能 / 修 Bug** | `activate [owner]`（**只派一次**） |
| 要拉代码 / 看池 / 归还槽位 | 交给 `owner`（owner 转交 `worktree` 做对应动作） |
| `OwnerReply.phase=development` | `proceed`，本轮不介入（引擎会把结果送回 owner） |
| `OwnerReply.phase=done` | `proceed` 汇总，或按需 `[learn]` / `[audit]`（**不再派 owner**） |
| `OwnerReply.phase=blocked` | `proceed`，转达 owner 的 `reply`，等用户 |
| 要复盘 | `activate [audit]` |
| 要沉淀 | `activate [learn]` |

**授权**：`deliver` 类对外动作必须用户明确授权；owner 拿不到授权时报 `phase=blocked`，协调员向用户要，拿到后再派 owner。

---

## 九、待评估点

### 形状

- **Q1 分析到开发的切换**：现在靠用户明说（"修吧"）。是否允许协调员在根因结论出来后主动问"要不要进入开发"？
- **Q2 `challenge` 读哪份代码**：它没有 `source`，输入里也没有 `WorktreeState`。倾向它只审查 `DiagnosisReport` / `DesignReport` 的证据，不读代码。
- **Q3 owner 的粒度**：一个 owner 管一次开发。若一次开发里包含多个不相关子任务，是否拆成多次 owner（先不做并行）。

### 契约

- **Q4 `sandbox` 的目标从哪来**：原来自 `IssueCandidates`，现在没有了，也没有沙箱 ID / ACP 地址字段。**[未验证]**
- **Q5 授权信息怎么到 owner**：`CoordinatorReply` 目前无"授权"字段。要么加，要么由 owner 从用户原话判断，要么协调员在任务单里写明。
- **Q6 owner 的 `OwnerReply` 字段是否够协调员与下游用**（当前：`repo/task/branch/source/worktree_action/phase/item/next`）。
- **Q7 `explore` 声明读 `SandboxDiagnosis`，但与 `sandbox` 并行时拿不到本轮结果**。是否去掉这条声明。

### 机制

- **Q8 槽位只在单个 heron 进程存活期间可写**：`--prompt` 单轮进程退出后槽位变 stale。只有 TUI / json-rpc 长会话里"申请 → 开发 → 归还"才完整。
- **Q9 分支名从哪来**：用户明确给就用；没给就 `issue-N` → `fix/N`。Feature 没有 Issue 号时怎么办。
- **Q10 槽位何时归还**：倾向"用户要求或闭环后 owner 建议"，不自动归还。
- **Q11 开发域 team 的 HITL**：**[已核实]** 触发审批（`rm -rf` / `sudo` / `git reset --hard` / 写 `.env`）时，
  审批请求直达用户界面，team turn 进入 `waiting_approval`。owner 只需在 `item` 里写明"避免这些命令"。
  **[未验证]** 审批通过后 team 如何续跑、owner 是否被正确唤回。

### 环境

- **Q12 `repos/genie`（primary）不干净**：停在 `fix/sandbox-proxy-reconnect-recovery`，5002 个改动。分析链路读它会读到错的代码。
- **Q13 git-lfs 在槽位根生成 4 个文件**导致 release 报 `worktree_dirty`；`release.sh` 已加重试清理，根因在池侧。

---

## 十、已落地 / 未落地

| 项 | 状态 |
|---|---|
| 取消 intake 与 issue-searcher；目标不明问用户 | **[已落地]** |
| 协调员挂 `heron-ai-config` + Bash/Write/Edit，可读会话汇报进度 | **[已落地]**，实测通过 |
| 协调员无私有知识（`routing-playbook` 已删） | **[已落地]** |
| 分析类 agent 按 `source` 选代码源，去掉槽位硬依赖 | **[已落地]** |
| `explore` / `sandbox` 拆出，`diagnose`、`design` 只剩结论环节 | **[已落地]** |
| `worktree` 5 个 team 合并为 1 个（`manage.sh`） | **[已落地]** |
| 开发域 4 份团队知识留在共享目录（`scope: all`） | **[已落地]** |
| **`owner` agent 与 `owner_team`** | **[已落地]** |
| **开发域 team 参数来源改 `OwnerReply`** | **[已落地]**（`worktree_team` 的 command env、`fix` / `deliver` 的 inputs） |
| **Flow 保留开发域绑定、去掉全部 `on_proceed`**；协调员 `can_activate` 用 `owner` 替换 5 个开发域 | **[已落地]** |
| **协调员路由改为"要开发→owner"，去 `worktree_action` 字段** | **[已落地]** |
| **引擎：删除 `coordinator: true`，任何 team 声明 `can_activate` 即可转交** | **[已落地]**，全量测试通过 |
| **引擎：回程按激活者（`proceed` 无 `on_proceed` → 回 activator）** | **[已落地]** |
| **实跑最小链路（default → owner → worktree → owner → default）** | **[已落地]**，`heron --prompt` 只读看池 exit 0 |
| 引擎：删 `coordinator` 字段时旧配置报错（拒绝静默降级） | **[已落地]**（`FlowTeamBinding.UnmarshalYAML`） |

---

## 十一、引擎约束速查（都读自源码）

| 约束 | 位置 |
|---|---|
| 任何 team 声明 `can_activate` 即可 activate；未声明则不可 | `flow/runtime.go` NextActivate 分支 |
| 回程：`proceed` 且无 `on_proceed` → 回激活者；entry 无激活者 → 等用户 | `flow/runtime.go` NextProceed 分支 |
| `activate` 的目标须在 Flow 内且在该 binding 的 `can_activate` 内 | `flow/runtime.go` validateActivation |
| 同批多 sibling 回同一 team 只唤醒一次（有 activator 的按 teamID 去重） | `flow/runtime.go` enqueueBatchRoute |
| 同一 team 内无依赖的 call 会全部执行 | `team/runtime.go` readyCalls |
| command 的 record 只有 `exit_code/stdout/stderr`，不展开 JSON | `call/command.go` |
| `Spawn`：`items` 并行、上限 8、深度 3；子 agent 沿用自己的定义；只拿到 `item` | `agent/spawn.go` |
| agent 有私有知识（`agents/<id>/knowledge` 有可加载条目）就不给 Bash | `agent/bash_gate.go` |
| `.agents/data` 对 Read/Grep/Glob 无条件封闭；只有 Bash 能读 | `tool/path_scope.go` |
| HITL 只对 `rm -rf`/`sudo `/`shutdown`/`mkfs`/`git reset --hard` 及写 `.env` 触发 | `agent/policy.go` |
| 环境变量靠 settings.json 的 `env` 段注入（相对路径按配置目录解析） | 本次新增 |
| 后台派发（mailbox）只用于入口 team 的 activate，且目标不能再自行 activate | `flow/runtime.go` NextActivate 分支 |
