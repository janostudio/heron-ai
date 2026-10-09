# 需求：增加会话列表与选择进入（`heron --list`）

> 状态：**已实现**（2026-10-09）
> 关联：`cmd/server/main.go`（参数解析与 `--list` 分支）、`internal/sessionlist/`、`internal/view/sessionlist.go`
> 附带议题：**CLI 参数风格不统一**（本次已统一 `--help` 显示为双横线）

## 实现记录（2026-10-09）

按 issue 的建议形态落地：

- **命令形式**：`heron --list`（与现有 flag 风格一致）。
- **会话枚举**：新增 `internal/sessionlist` 包，扫描 `.agents/data/sessions/`，
  逐会话读 `flow.jsonl` 取首条输入（截 60 rune）、轮次数、最后活跃时间；
  读 `team.jsonl` 累加 `agent_turn.completed` → `call_result.Usage.total_tokens`。
  **容错**：坏行跳过而非报错；缺失目录/空目录返回空列表不报错；非目录项跳过。
- **"进行中"标注**：由最后一个 `flow_turn.*` 事件推导（`flow_turn.started` /
  `waiting_tool` / `waiting_approval` = 进行中；`completed` / `waiting_input` = 空闲）。
  不能只看 `flow_session.status`——实测 flow.jsonl 从不把最后的 `status` 从
  `running` 改写为 `waiting_input`，只看它会把所有会话标成进行中。
- **交互式**：`internal/view/sessionlist.go` 的 `SessionPicker`（手写 bubbletea
  model，↑↓/j/k、Enter、Esc/q）；选中后以该 id 进入 TUI（`runTUIWithSession`）。
- **非交互降级**：`charmbracelet/x/term` 判断 stdin+stdout 是否 tty；非 tty 走
  `view.RenderSessionsText` 输出纯文本（无 ANSI，`| grep` 友好）。
- **退出提示**：TUI 用 AltScreen，`View()` 里画的 goodbye 会被 AltScreen 回滚、
  用户看不到，故改在 TUI 退出后由 `runTUIWithSession` 向真实终端打印
  `Goodbye!  Resume with: heron --session <id>`。

### 附带议题（已一并处理）

`--help` 输出由 `-prompt` 改为 `--prompt`：自定义 `flag.Usage` / `printUsage`
遍历 `flag.VisitAll` 补 `--` 前缀。**仅显示层**，Go `flag` 仍接受单横线输入，
兼容无损。子命令风格（`heron knowledge gc`）本次未动。

### 后续修复（2026-10-09，用户反馈）

**问题**：`--list` 选中进入会话后，TUI 里看不到历史聊天记录（白屏）。

**根因**：引擎在续聊时会 `replayAgentContext` 把历史注入**模型上下文**（LLM 记得），
但 TUI 的 **显示层**从不加载历史——`runTUIWithSession` 只 seed 了 `sessionID`，
`TUIModel` 从空开始。

**修复**：
- `internal/sessionlist/history.go`：`History()` 从 `flow.jsonl` 按顺序配对
  `flow_turn.started.input`（用户）与 `team_turn.completed.team_result.Reply`
  （助手），重建对话（容错坏行）。
- `internal/view/tui.go`：`TUIModel.SeedHistory([]HistoryTurn)` 把历史灌进 viewport
  并滚到底；`WindowSizeMsg` 首次分支改为"有历史就渲染历史，否则渲染欢迎页"。
- `cmd/server/main.go` 的 `runTUIWithSession`：`sessionID != ""` 时加载历史并
  `SeedHistory`（加载失败非致命，会话仍可续聊）。
- 测试：`history_test.go`（配对/坏行/空会话）、`tui_test.go` 的
  `TestTUIModel_SeedHistory*`（含"首个 size 事件前 seed"的回归）。

### 边界（均覆盖）

| 情况 | 实现行为 |
|---|---|
| 无任何会话 | 打印"暂无会话"，exit 0 |
| jsonl 坏行 | 跳过该行，会话仍列出 |
| 会话进行中 | 列出并标 `●` |
| `--list` + `--session`/`--prompt` | 报错 `--list cannot be combined with --session or --prompt`，exit 1 |
| 非交互终端 | 纯文本列表，不启动全屏 |

### 测试

- `internal/sessionlist/list_test.go`：空/缺目录、正常字段、坏行跳过、截断、
  token 求和、进行中判定（含"状态字段仍 running 但已 waiting_input"的回归）、排序。
- `internal/view/sessionlist_test.go`：选择返回 id、Esc 取消、导航边界、纯文本输出
  （断言无 ANSI）。
- `cmd/server/usage_test.go`：`printUsage` 输出 `--prompt string` 且不含单横线。

---
## 需求

1. **列出所有会话**：`heron --list` 展示会话列表
2. **选择后回车进入**：列表里选中某个会话，回车即进入该会话（等价于
   `heron --session <fs_id>`）

现状：用户想续聊必须自己知道 session id，但 TUI 从不展示它
（见 [ISSUE-tui-exit-no-resume-hint](./ISSUE-tui-exit-no-resume-hint.zh-CN.md)）。
`--list` 是这条链路的补齐——让用户**不用记 id 就能找到并回到会话**。

## 会话数据的可用字段

数据已存在 `.agents/data/sessions/<fs_id>/`，足以支撑列表展示：

```bash
S=.agents/data/sessions/<fs_id>

# 现有可直接取用的字段
jq -r 'select(.type=="flow_session.created")
       | "\(.payload.session.id)\t\(.payload.session.flow_id)\t\(.created_at)"' $S/flow.jsonl

# 首条用户输入（可作列表标题）
jq -r 'select(.type=="flow_turn.started") | .payload.turn.input' $S/flow.jsonl | head -1

# 轮次与消耗
jq -r 'select(.type=="agent_turn.completed")
       | .payload.call_result.Usage.total_tokens' $S/team.jsonl \
  | jq -s 'add'
```

建议列表每行展示：`session id` + 首条输入（截断）+ 轮次数 + 总 token + 最后活跃时间。

## 建议实现形态

### 命令形式

推荐 **`heron --list`**（与现有 flag 风格一致，易实现），
或 **`heron sessions`**（与已有的 `heron knowledge gc` 子命令风格一致）。

不推荐 `heron -list`——虽然 Go 的 `flag` 包接受单横线，但它混用了
"参数"的写法表达"子命令"的语义，见下方"风格议题"。

### 交互流程

```
$ heron --list

  会话列表（↑↓ 选择，回车进入，q 退出）

  ● fs_7a3e3c95f9b3fbf9   2026-10-09 16:12   3 轮   59.3k tok   "帮我查一下当前有哪些待修 Issue"
    fs_63f041b056487198   2026-10-09 06:20   3 轮  628.5k tok   "帮我查一下当前有哪些待修 Issue"
    fs_ec68a13b6b03ce78   2026-10-09 15:43   1 轮    5.3k tok   "你好"
    ...

  共 27 个会话
```

回车后：以该 `fs_id` 作为 `--session` 值进入 TUI（等价
`heron --session fs_xxx`），即可继续对话。

### 边界情况

| 情况 | 期望行为 |
|---|---|
| 无任何会话 | 提示"暂无会话"，正常退出（不报错） |
| 会话目录损坏（jsonl 坏行） | 跳过该行/该会话，不因此崩溃（容错读取） |
| 会话仍在执行中 | 仍可见但标注"进行中"；或不允许进入并说明原因 |
| `--list` + `--session` 同时给 | 报错提示互斥，或 `--list` 优先 |
| 非交互终端（管道/CI） | 输出纯文本列表（不启动全屏选择器），便于脚本消费 |

最后一条尤其重要：TUI 选择器依赖 tty，管道场景下应降级为纯文本，
否则 `heron --list | grep xxx` 会挂住。

## 附带议题：CLI 参数风格不统一

当前的风格是**三种混用**：

| 场景 | 现有写法 | 来源 |
|---|---|---|
| 参数 | `-prompt` / `--prompt` | Go `flag` 包，**单双横线等价**（实测都可用） |
| 参数（`--help` 输出） | 全部显示为 `-prompt` 形式 | Go `flag` 默认输出 |
| 子命令 | `heron knowledge gc` | 手写 `os.Args[1]` 判断 |

### 实测：单横线可用

```bash
$ heron -version     # → Heron AI vdev (darwin/arm64)   exit=0
$ heron --version    # → Heron AI vdev (darwin/arm64)   exit=0
```

两者等价，因为 Go 的 `flag` 包对单/双横线一视同仁。

### 哪个更合理

- **双横线（GNU 风格）更主流**：`--prompt` / `--session`
  与绝大多数现代 CLI（git、docker、kubectl）一致，长短选项可区分
  （`-p` 短 / `--prompt` 长）。
- **单横线是 Go 特有**：`flag` 包默认把 `-prompt` 当长选项名。
  这也是 `--help` 里显示单横线的原因。
- **子命令（无横线）表达"动作"**：`list` / `serve` / `knowledge gc`
  属于"做什么"，不该写成 flag。

建议统一为：

```
heron --prompt <文本>        # 参数用双横线
heron --session <fs_id>
heron sessions               # 动作类用子命令
heron knowledge gc
heron serve
```

但**改变现有单横线显示会影响兼容**（现有脚本可能依赖 `-prompt`），
所以这属于需要单独决策的议题，不在本需求范围内实施。

## 相关代码位置

| 位置 | 内容 |
|---|---|
| `cmd/server/main.go:31-34` | 唯一的子命令分派（`knowledge`），`--list`/`sessions` 需在此加分支 |
| `cmd/server/main.go:37-49` | 全部 flag 定义（新增 `--list` 加在这里） |
| `cmd/server/main.go:102-106` | 主分派：`--prompt` → `runPrompt`，否则 → `runTUI` |
| `cmd/server/main.go:803-826` | `runTUI`（会话选择后以此进入） |
| `internal/view/tui.go:217` | `viewport.New(80, 20)`，列表渲染可复用该组件 |
| `internal/view/tui.go:286-330` | 现有的 slash 下拉选择器（`slashMode` / `filterCommands`）——列表选择器的现成参考实现 |

## 一句话

需要 `heron --list` 列出 `.agents/data/sessions/` 下的会话并支持回车进入，
以补齐"用户无从得知 session id"的链路；同时 CLI 存在单横线/双横线/子命令
三种风格混用，建议参数统一双横线、动作统一子命令，但属独立决策。
