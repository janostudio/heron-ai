# heron-ai 配置字段速查

本文列出 Flow / Team / Call / Agent 各配置字段的含义。权威来源：
`docs/configuration/*.md`。

## 编排层次

```
Flow → Team → Call → Agent / Command / Webhook
```

`Call` 是执行项（不是层级），统一了「LLM Agent / 固定命令 / Webhook」三种调用。

---

## Flow（`.agents/flows/<name>.yml`）

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | string | Flow 标识 |
| `entry` | string | 入口 Team 的 Flow 局部名 |
| `teams` | map | Team 绑定表，key 为 Flow 局部名 |

### Team 绑定（teams 下每个条目）

| 字段 | 类型 | 含义 |
|---|---|---|
| `team` | string | 指向 `.agents/teams/` 下的真实 Team id |
| `coordinator` | bool | 标记唯一协调 Team（失败汇总/聚合点） |
| `can_activate` | []string | 本绑定可激活的 Team |
| `depends_on` | []string | 前置 Team（完成后本 Team 才执行） |
| `inputs` | object/list | 用户消息 + SharedRecord 输入 |
| `on_proceed` | list/object | Team 以 `proceed` 结束后的固定路由 |

---

## Team（`.agents/teams/<name>.yml`）

| 字段 | 类型 | 含义 |
|---|---|---|
| `id` | string | Team 标识 |
| `goal` | string | Team 目标 |
| `calls` | map | 执行项集合 |
| `output` | object | 对外发布的 SharedRecord（`from` + `record` + `scope`） |
| `memory` | object | 可选 Team Memory（`enabled`/`max_items`/`max_chars`/`inject_summary`） |

### Call（calls 下每个条目）

| 字段 | 类型 | 含义 |
|---|---|---|
| `type` | string | `agent` / `command` / `webhook` |
| `agent` | string | （type=agent）Agent 定义名 |
| `command` | string/object | （type=command）Shell 命令 |
| `webhook`/`url` | object/string | （type=webhook）URL |
| `depends_on` | []string | 依赖的同 Team 内其他 call |
| `inputs` | object/list | 显式输入与 SharedRecord |
| `output` | object | 本次调用发布的 SharedRecord |
| `responsibility` | string | Agent 职责说明 |
| `timeout` | string | 超时 |

### 调度规则

- 无依赖的 call → 自动并行
- 有依赖的 call → 依赖完成后执行
- 依赖多个 → 读取声明的 SharedRecord 后执行

---

## Agent（`.agents/agents/<id>/AGENT.md`）

frontmatter 分组：

### persona
| 字段 | 含义 |
|---|---|
| `role` | 角色名 |
| `goal` | 主要目标 |
| `backstory` | 背景 |

### model（未配置的字段从 models.json 继承）
| 字段 | 含义 |
|---|---|
| `model` | 模型名（**直接写模型 id，勿用 `${ENV}` 占位符**） |
| `temperature` | 覆盖默认温度 |
| `top_p` / `top_k` / `repetition_penalty` | 覆盖采样参数 |
| `reasoning` | 覆盖推理配置 |
| `max_output_tokens` | 覆盖单次输出上限 |
| `api_key` / `base_url` | 特殊场景覆盖密钥/地址 |

### tools
| 字段 | 含义 |
|---|---|
| `builtin` | 内置工具：Read/Write/Edit/Grep/Glob/Bash/WebSearch/WebFetch/CodeNav/AskUserQuestion/TodoWrite/TodoRead |
| `custom` / `mcp` | 自定义 / MCP 工具 |

### loop
| 字段 | 含义 |
|---|---|
| `max_rounds` | 单次执行最大模型轮次 |
| `tool_execution` | `sequential` / `parallel_safe` |
| `max_parallel_tools` | 最大并发只读工具数 |
| `async_tools` | 允许创建持久异步任务的工具 |
| `timeout` | 执行超时 |

### context（上下文窗口控制）
| 字段 | 含义 |
|---|---|
| `target_ratio` | 目标 Active Context 比例（默认 0.70） |
| `compaction_threshold` | 触发压缩的比例（默认 0.80） |
| `hard_limit_ratio` | 硬上限比例（默认 0.90） |
| `output_reserve_ratio` | 为输出预留的比例（默认 0.15） |
| `tool_output_ratio` | 工具输出预算比例（默认 0.10） |
| `max_tool_output_chars` | 单条工具结果字符上限 |

### budget（资源预算，0 = 不限制）
| 字段 | 含义 |
|---|---|
| `max_model_rounds` | 最大模型调用数 |
| `max_tool_calls` | 最大工具调用数 |
| `max_wall_time` | 最大墙钟时间（如 `10m`） |
| `max_input_tokens` / `max_output_tokens` | 累计 token 上限 |
| `max_file_changes` | 最大 workspace 写操作数 |
| `max_tool_output` | 累计工具输出字符上限 |

### 其他
| 字段 | 含义 |
|---|---|
| `structured_output` | `{type, schema}` 结构化输出契约 |
| `hitl` | `{enabled, timeout}` 人工审批 |
| `hooks` | `{event, command, timeout}` 生命周期钩子 |
| `handoffs` | 可委派任务的其他 agent 列表 |
| `skills` / `knowledge` / `rules` | 引用的 skill / 知识 / 规则 |

---

## SharedRecord（跨层数据契约）

- `output.record` 声明产出名（如 `DiagnosisReport`）。
- 下游用 `inputs` 显式声明要读的 record：
  ```yaml
  inputs:
    - from: research          # 上游 call 名
      record: ResearchReport  # 上游产出的 record 名
  ```
- **record 名称是自由字符串，无 schema 校验**——拼写必须与上游 `output.record` 完全一致，否则下游静默收不到。
- 调用不会自动读全部历史；需要什么就在 `inputs` 里声明什么。

## rules（`.agents/rules/<name>.md`）

```markdown
---
type: hard        # hard 强制 / soft 建议
scope:
  type: all       # all / team / agents
priority: 10
---
规则正文。
```
