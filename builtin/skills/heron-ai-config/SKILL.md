---
name: heron-ai-config
description: "写、改 heron 的 .agents 配置（Flow / Team / Call / Agent / models / settings / rules）并排查运行结果不符合预期。默认优先：先不配，出现具体症状才改一个键。当需要新建或修改 agent、team、flow、模型列表，或某次运行失败、卡住、答非所问时使用"
tools:
  - Read
  - Write
  - Glob
  - Grep
  - Bash
scripts:
  - scripts/build.sh
  - scripts/serve.sh
---

# 写 heron 配置：默认优先

引擎的默认值是**设计出来的**，不是"还没配"。只在两种情况下动配置：默认做不到，或者有具体证据说明默认做错了。

## 0. 三条规则

1. **默认即设计**：§2「别写」档里的键，默认就是最优。改它们只会缩小能力或增加失败面。
2. **症状驱动**：先跑一轮看结果，从 §3 找到唯一该改的键。不做预防性配置。
3. **配置预算**：一个 agent 的 frontmatter 超过约 15 行，就要能对每行说出它解决的**具体已发生问题**；说不出就删掉。

## 1. 最小可用

一个能跑的 agent 只有三项配置：

```yaml
---
name: reviewer                  # 必写：Team / Spawn 按它引用，事件里也用它
model:
  model: gpt-4o-mini            # 必写：models.json 里 profile 的 name（不是 id）
tools:
  builtin: [Read, Grep, Glob]   # 必写：不写 = 没有工具
---
你是代码评审员。给出问题清单与修改建议。
```

`flow` / `team` 各一份最小形态见 `references/minimal.md`。**先让它跑起来，再按症状加配置。**

## 2. 三档字段

| 档 | 判据 |
|---|---|
| 必写 | 不写跑不起来或跑错对象 |
| 有目标才写 | 默认够用；出现对应症状、且你明确要那个效果时才写 |
| 别写 | 默认即设计；写了会缩小能力或制造新的失败面 |
| 不能写 | 已移除或不存在；写已移除的**加载报错**，自造键**静默丢弃** |

### 必须写

| 键 | 不写会怎样 |
|---|---|
| `name` | 加载失败 `agent definition name is required` |
| `model.model` | 回退到 `models.json` 顶层 `model`；默认一改，所有没写 model 的 agent 静默换模型 |
| `tools.builtin` | 没有工具。写错名字**不报错**，只是模型看不见那个工具 |

### 有目标才写

| 键 | 什么时候才写 |
|---|---|
| `context.max_tool_output_chars` | 单条工具输出会挤掉上下文（日志、大文件）；先跑，看到压缩频繁再加 |
| `loop.max_rounds` | 默认 200；只在"确实要更早停"时写 |
| `loop.max_repeated_reads` / `stuck_action` | 只在真的被判停、且重读合理（纯只读分析）时放宽 |
| `budget.max_input_tokens` | 有明确的单轮成本上限要求时（按窗口 × 轮数估，别凭感觉给数） |
| `budget.max_tool_calls` / `max_file_changes` | 要防失控调用 / 只读任务想硬性禁写时 |
| `completion.*` | 只在 prompt 已写清完成判据、模型仍反复不收口时开 |
| `hitl` | 只有需要人工审批的动作才开 |
| `workspace.read_roots` | agent 必须读工作区根之外的目录时 |
| `skills` / `rules` | 需要该能力时（引用不存在的会加载报错） |

### 别写（默认即设计）

| 键 | 默认 | 为什么 |
|---|---|---|
| `context.max_input_tokens` | 模型容量（如 960000） | 它是**容量覆盖**。写 40000 = 窗口砍到 1/24，压缩线落到 36K，长任务的历史被反复折掉——最常见的自伤 |
| `context.target_ratio` / `compaction_threshold` / `output_reserve_ratio` / `tool_output_ratio` | 0.45 / 0.90 / 0 / 0.10 | 晚压压狠是算过成本的：每次压缩 = 一次摘要调用 + 一次缓存重置 |
| `context.microcompact_*` / `compactor` / `recent_message_groups` | 8192 / 4096 / `llm` / 2 | 内部旋钮；调之前先有压缩相关的证据 |
| `loop.max_same_tool_calls` / `max_no_progress_rounds` / `max_same_model_texts` | 内置 | 判停守卫；放宽前先确认不是真的卡住 |
| `budget.max_model_rounds` | 0 | 与 `loop.max_rounds` 取 **min**，写它等于给同一个上限加第二个入口，事后看不出谁在生效 |

### 不能写

| 键 | 现状 |
|---|---|
| `loop.timeout` / `budget.max_wall_time` | 已移除：turn 由轮次与工具调用约束，不看墙钟。写了**加载报错** |
| `context.hard_limit_ratio` | 已移除：硬限 = `1 - output_reserve_ratio`。写了**加载报错** |
| `handoffs` / `spawn_targets` 等自造键 | 引擎没有这些字段；YAML 非严格解码 → **静默丢弃**，不报错也不生效 |

## 3. 症状 → 改哪个键

| 症状（日志 / 事件里看到） | 先确认 | 才改 |
|---|---|---|
| 启动日志 `narrows the model window` | 是否写了 `max_input_tokens` | 删掉它 |
| `agent budget exceeded: max_input_tokens` | 累计 prompt token 到顶 | 抬 `budget.max_input_tokens` 或删除 |
| `session.context.compacted` 出现得频繁 | 窗口是否被覆盖；单条工具输出是否过大 | 删 `max_input_tokens`；仍频繁再加 `max_tool_output_chars` |
| `agent stuck: same read returned N times` | 是否压缩导致重读同一文件 | 删窗口覆盖；确属只读任务再放宽 `max_repeated_reads` |
| 模型说"没有某工具" | `tools.builtin` 名字拼写 | 改名字（写错不报错） |
| 子 agent 返回 `needs_input` / 找不到文件 | 路径是否相对**进程 cwd** | 改路径，或补 `read_roots` |
| 模型反复声称完成但不收口 | prompt 是否已写清完成判据 | 才考虑 `completion.require_*` |
| `stop_reason=max_rounds` | 任务真的大，还是提示词/路由有问题 | 先修提示词；确需更久再抬 `max_rounds` |
| 模型每轮都重发大段工具输出、token 涨得快 | 是否整读文件 | 加 `max_tool_output_chars`，或改用 Grep 定位 |

## 4. 必须配的例外（安全边界，不是调优）

- **`models.json` 是唯一必需文件**：缺它或 `models` 为空，启动直接失败。
- **`workspace.read_roots`**：只读扩展根，相对**进程 cwd** 解析；条目失效被跳过（不报错），代价是那一刻读不到。
- **`lifecycle: per_task`** 必须同时 `inputs.user_message: true`，否则加载报错。
- **`settings.json` 的 `tools.restricted_bash_phrases`**：只增不减，命中变"需审批"。

## 5. 自检：`--check-config` 能拦什么

```bash
heron --check-config      # 退出码 0 才算合法
```

| 能拦 | 拦不住 |
|---|---|
| `entry` / `agent` / `skill` / `rule` / `record` 引用缺失 | 未知键（静默丢弃） |
| `per_task` 缺 `inputs.user_message`、entry 用 `per_task` | 数值语义（窗口写小、预算写紧） |
| `loop.timeout` / `max_wall_time` / `hard_limit_ratio` | 字段缺失带来的默认行为 |
| 比率顺序（`target < threshold < 硬限`） | 多个上限互相钳制（取 min） |

**`1 bindings ok` 只证明引用对得上，不证明配得对。**

## 6. 编排心智模型

```text
Flow(entry) → Team 绑定 → Call(agent | command | webhook) → Agent
```

- Team 平权：没有 coordinator；能激活谁看绑定的 `can_activate`，结果回到激活者。
- 数据靠 SharedRecord：`output.record` 产出，`inputs` 显式声明消费；名字是自由字符串，拼错在**加载期**报"没有生产者"。
- 无依赖的 call 自动并行，有依赖的自动串行。

## 7. 参考索引

| 你要做的事 | 读哪份 |
|---|---|
| 抄一份最小可跑的 flow / team / agent | `references/minimal.md` |
| 查字段名、类型、默认值、三档归属 | `references/fields.md` |
| 某个症状该调哪个旋钮、代价是什么 | `references/tuning.md` |
| 结果不对 / 卡住 / 报错 | `references/diagnose.md` |
| 找聊天记录、事件、token、调用链 | `references/observe.md` |
| 让 agent 在对话里自建 agent / team | `references/define.md` |
| 启停服务、CLI 参数、校验 | `references/operations.md` |
