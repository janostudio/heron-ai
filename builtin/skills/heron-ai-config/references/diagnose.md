# 判定：跑得对不对、错在哪

命令与字段位置在 `observe.md`。这里只给**判据**与**下一步**。

## 1. 定位顺序（按序，别跳）

```bash
S=.agents/data/sessions/$(ls -1t .agents/data/sessions | head -1)
```

| 步 | 看什么 | 判据 |
|---|---|---|
| 1 | `flow.jsonl` 的 `type` 序列 | `session.created` → `session.turn.started` → `session.team_turn.*` → `session.turn.ended` 走完就是收口了 |
| 2 | `session.turn.ended` 的 `stop_reason` / `awaiting` | `end_turn` 正常；`failed`/`cancelled`/`max_*` 看 `error` 与 `payload.limit` |
| 3 | `session.team_turn.ended` 的 `next.action` 链 | `proceed` 一路到底；出现 `activate`/`return` 是正常移交；`fail` 停下看原因 |
| 4 | `session.agent_turn.completed` 的 `status` 与 `requests` 长度 | 非 `completed` → `error`；`requests` 接近 `max_rounds` → 快撞上限 |
| 5 | `agent.jsonl` 的 `model_response` 序列与 `session.agent.feedback` | 模型为什么会那样答：看每轮 `text`、工具调用、结构化重试 |

## 2. 正常长什么样

| 检查项 | 预期 |
|---|---|
| 会话定位 | 一次提问/续聊追加进同一个 `sessions/<fs_id>/`；判先后用 `seq`，不是行号 |
| 轮次 | 一轮 = `call_result.requests` 里一个元素；同一 agent 多个元素说明它多轮调模型 |
| 终态 | `stop_reason=end_turn` 且 `awaiting=user` 是"答完等你"；`awaiting=tool/approval` 是**在等**，不是失败 |
| team 范围 | 寒暄类只激活 `entry`；出现专家 team 说明被当成任务路由了 |
| 消耗 | flow 级 = 各 `team_result.usage` 之和（与终端 `Tokens:` 同口径）；不要再加 call 层 |
| 证据 | record 同时出现在 `flow.jsonl` 的 `record.published` 与 `evidence.jsonl`；子 Team 的产出出现在 `evidence.jsonl` 是正常的（flow 作用域副本） |

## 3. 异常 → 看什么 → 下一步

| 现象 | 先看 | 下一步 |
|---|---|---|
| `stop_reason=failed` | `payload.error`；撞上限时看 `payload.limit`（哪条上限、实际到多少、最频繁的激活边、最近几跳） | 路由成环就先修路由；`limit.name` 指明的是引擎兜底，别急着调大 |
| `stop_reason=cancelled` + `error=interrupted…` | 这是**上一个**回合被进程中断后补写的终态 | 不是本轮的错误；找进程为什么退出（日志尾部） |
| `call_result.status` 非 completed | `call_result.error` | 工具报错看 `session.tool_call.failed`；模型报错看日志的 provider 错误 |
| `requests` 暴涨 | `session.context.compacted` 次数；单轮 `prompt_tokens` | 上下文膨胀：先删窗口覆盖，再加 `max_tool_output_chars`（见 `tuning.md`） |
| 压缩频繁出现 | `dropped_count`、窗口配置 | 同上；压缩本身不是错误，频繁才是 |
| 模型说"没有某工具" | `AGENT.md` 的 `tools.builtin` 拼写 | 改名字；写错不报错 |
| 模型输出为空 | 是否读了 `payload.text`；是否有 `session.agent.feedback` | 纯工具轮正常；有 feedback 是结构化重试，看 `raw_text` |
| command call 无输出 | `team.jsonl` 的 `session.command_turn.*` | 看退出码与 stderr |
| record 产出了但下游读不到 | `session.record.consumed` 有无对应事件 | 没有 → 下游 `inputs` 没声明或时间窗不对；有 → 看下游注入 |
| 子 agent 返回 `needs_input` | 派发文本是否给了完整目标 | 补目标；不要让它自己反查环境 |
| 一轮迟迟不结束 | `runtime.max_team_turns` 与路由是否成环 | 先确认不是成环，再考虑调高兜底 |

## 4. 从配置预测"应该发生什么"

排查的前提是知道正常形态。先读配置再对照运行时，能立刻分出"配置错了"还是"运行错了"。

| 配置 | 决定 | 对照 |
|---|---|---|
| `flows/*.yml` 的 `teams` 与 `can_activate` | 有哪些 team、谁能激活谁 | `flow.jsonl` 里出现的 `team_id` 必须是 `can_activate` 的子集 |
| `teams/*.yml` 的 `calls` | 每个 team 跑哪些 call、依赖顺序 | `team.jsonl` 的 `agent_turn.*` / `command_turn.*` 应与 calls 对应 |
| `teams/*.yml` 的 `output.record` | 该 team 发布什么 record | `session.record.published` 的 `name` |
| `AGENT.md` 的 `loop.max_rounds` / `budget.*` | 轮数上限（取 min） | `requests` 长度不应超过生效值 |
| `AGENT.md` 的 `structured_output` | 会不会有重试 | 有 `session.agent.feedback` = 模型没按 schema 输出 |
| `settings.json` 的 `runtime` | 编排兜底上限 | 触发时 `stop_reason=max_rounds` / `max_tool_calls` / `max_tokens` |

典型结论：
- 实际激活的 team 不在 `can_activate` 里 → 加载的不是你以为的那份定义。
- 配了 agent call 却只有 `command_turn.*` → 看 call 的 `type`。
- `requests` 接近 `max_rounds` → 该 agent 快撞上限：先收敛 prompt，或按 `tuning.md` 调一个键。

## 5. 划清边界

下结论时区分三类，别混：

| 类别 | 特征 | 处理 |
|---|---|---|
| 配置错 | 与 §4 的预测不一致、加载没报错但行为不对 | 改一个键，再跑一轮 |
| 引擎 bug | 配置自洽、事件与代码逻辑矛盾（例如终态缺失、字段为空） | 最小复现 + 事件片段，报给引擎维护方 |
| 环境/外部 | 网络、凭证、上游服务、沙箱不可达 | 不属配置问题，记录证据后交给对应系统 |

**不要把第三类记成第一类。** 配置改不好网络问题，只会多出一份无用的配置。
