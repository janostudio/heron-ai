# heron-ai 提示词方案

本文说明三件事：提示词在引擎里分几层、每层现在有哪些内容、知识是怎么进入上下文的。
这是**现状说明**，不是提案；代码位置都标在文中，与实现不符时以代码为准。

## 1. 提示词的三个归属层

| 层 | 载体 | 特征 | 何时生效 |
|---|---|---|---|
| **引擎级策略** | `internal/prompt/builtin.go` 的模板常量 | 所有 agent 都需要，与具体能力无关 | 每次组装系统提示 |
| **能力级提示词** | 独立模块常量（如知识总结、上下文压缩） | 只有具备该能力时才用 | 由对应模块调用 |
| **产品层政策** | 用户的 `AGENT.md` 正文 | 组织/产品的价值观与规范 | agent 自己的系统提示 |

分层的目的只有一个：**避免把"只有部分 agent 需要"的指令塞给所有 agent**。一个只做问答的
agent 不需要知道如何 spawn 子 agent，也不需要知道如何产出结构化交接。

## 2. 引擎级模板（`internal/prompt/builtin.go`）

| 模板键 | 内容 | 装配条件 |
|---|---|---|
| `execution-management` | 任务闭环、进度追踪 | 始终 |
| `tool-usage` | 工具是 ground truth、不要凭记忆断言 | agent 声明了任意 `tools.builtin` |
| `state-management` | 决策与依据要落进 State | agent 的 `tools.builtin` 声明了 `State` |
| `perspective-isolation` | 多 agent 的知识与视角边界 | 始终 |
| `output-format` | 输出格式约束 | agent 声明了 `structured_output` |
| `knowledge-query` | 知识使用规范 | **未装配**：`BuildSystemPrompt` 不引用它 |

`knowledge-query` 是历史遗留：它按"知识被注入进上下文"的旧机制写的，而现在的知识走
agentic search（见 §3），既没有可注入的文本，这条指令也没有落点。保留常量但没有引用点，
改外部行为时不要以为它在生效。

## 3. 知识如何进入上下文

现在是**指路 + 自取**，不是预索引注入：

```
配置了知识指针 → 注入一个 pointer 块（internal/knowledge/pointer.go）
          内容：知识目录在哪、目录清单、用什么工具读（**不列条目名**）
                ↓
          模型自己用 Read / Grep / Glob 打开需要的条目
```

三点值得记住：

- **没有"知识文本"这回事**。旧实现（`internal/knowledge/injector.go`、`formatEntries`、
  `KnowledgeIndex`）已删除，不要在改动里假设它还在。
- **pointer 块恒出现**：只要配置了指针就注入，`.agents/knowledge/` 对所有人可读，所以
  空知识库也会有这一块（只是没有条目）。它不随「有没有知识」变化。
- **`agent.Knowledge` 不是作用域白名单**：运行时没有读取方，字段只在定义校验时检查
  条目是否存在（`internal/definitions/validate.go`）。真正的可见性由目录路径过滤决定
  （`internal/tool/path_scope.go`），模型能 `Glob`/`Read` 到的就是它能看到的。

知识条目的产出（`heron knowledge`）见 §4；条目格式与分层见
[configuration/knowledge.md](configuration/knowledge.md)。

## 4. 能力级：知识总结提示词（已实现）

`internal/knowledge/summarizer.go` 的 `summarizerSystemPrompt` 是一个**独立元任务调用**：
用 `ModelProvider.Chat` 跑一次无工具调用，把候选来源提炼成一条固定格式的知识条目。

**输入**：SharedRecord、Workspace diff、测试结果、State 的 Confirmed/Decisions。
**输出**：一份带 YAML frontmatter 与固定 section 的 Markdown。

约束要点（改这段提示词时别丢）：

- 只提炼**长期可复用**的内容：事实、规则、偏好、流程、决策、教训。
- 丢掉闲聊、草稿、未发布的工具输出。
- **没有可验证依据的候选直接丢弃，绝不编造出处**。
- 与既有知识冲突时标出冲突，不静默改写历史。
- 严格输出：一份文档、无前言、无额外文本。

frontmatter 与 section 的字段定义在
[configuration/knowledge.md](configuration/knowledge.md)；总结用哪个模型由
`settings.json` 的 `knowledge.summary_model` 决定，空则用默认模型。

## 5. 与上下文压缩提示词的关系

两者是同一个技术模式（无工具轻量调用 + 严格输出 + 失败降级），但服务的目标不同：

| 维度 | 上下文压缩（`internal/agent/compactor_llm.go`） | 知识总结（`internal/knowledge/summarizer.go`） |
|---|---|---|
| 目的 | 保住**当前会话**的续聊上下文 | 沉淀**跨会话**可复用知识 |
| 输入 | 被裁剪的消息组 | SharedRecord + Workspace diff + State |
| 输出 | `## Compacted Agent Context` 纯文本 | 固定 frontmatter + section 的 Markdown |
| 时机 | 压缩时同步 | 会话封存或 `heron knowledge` 时 |
| 失败处理 | 降级回机械压缩 | 丢弃候选，不产出脏知识 |

压缩机制本身见 [context-management.md](context-management.md)。

## 6. 明确不做

- **不做会话标题生成**：标题是产品体验，不是引擎能力。需要的话由上层应用调一次
  无工具的 `Chat` 自行实现。
- **不把知识总结提示词注入主 agent 系统提示**：它是独立元任务，与 agent 的常规职责无关。
- **不做向量检索 / rerank**：知识规模仍在"模型用 Grep 就能找"的量级，加索引只会增加
  维护面。
- **不统一 pointer 块的标题措辞**：它已经和 agent 的读文件行为一致，改名不产生收益。
