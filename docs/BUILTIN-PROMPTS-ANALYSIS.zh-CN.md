# heron-ai 内置提示词分析

> 目的：系统性梳理 heron-ai 引擎「应该内置哪些提示词」，区分**引擎级强制策略**与**能力级可选提示词**，并给出落地路径。
> 参照系：CodeBuddy CLI 的提示词工程（`packages/agent-cli/product.json` 的 `prompts` 数组）。

## 0. 核心判断框架

CodeBuddy 是面向用户的 CLI 产品，heron-ai 是通用多 Agent 引擎。二者定位不同，**不能照搬 CodeBuddy 的提示词**，而要按「归属层级」拆分：

| 层级 | 载体 | 特征 | 何时内置 |
|---|---|---|---|
| **引擎级强制策略** | `internal/prompt/builtin.go` 模板 | 所有 agent 都需要、与具体能力无关 | 引擎启动即注入 |
| **能力级可选提示词** | 内置 Skill（`types.Skill.Body`） | 有对应能力的 agent 才需要 | agent 通过 `skills:` 按需引用 |
| **产品层专属政策** | 用户 `agent.body` / CODEBUDDY.md | 产品/组织的价值观与政策 | **不内置**，留给用户 |

关键结论：**不是所有提示词都该进 `builtin.go`。** 凡是「只有具备某能力（spawn 子 agent、产出结构化数据、等待审批）的 agent 才需要」的提示词，应做成**内置 skill**，让 agent 按需声明 `skills:`，而不是默认注入所有 agent——避免通用问答 agent 也被塞进"如何 spawn 子 agent"的无关指令。

---

## 1. 现状盘点

### 1.1 已内置的引擎级模板（`internal/prompt/builtin.go`）

| 模板键 | 状态 | 说明 |
|---|---|---|
| `execution-management` | ✅ 已增强 | 任务闭环、进度追踪 |
| `tool-usage` | ✅ 已增强 | 工具是 ground truth |
| `memory-management` | ✅ 已增强 | 决策+依据 |
| `perspective-isolation` | ✅ 已增强 | 多 agent 知识边界 |
| `output-format` | ⚠️ 简略 | 被 `structuredOutputContract` 部分覆盖 |
| `knowledge-query` | ❌ **死代码** | 从未被 `BuildSystemPrompt` 引用 |

### 1.2 散落的提示词（未收敛到统一体系）

| 位置 | 内容 | 问题 |
|---|---|---|
| `internal/agent/summarizer_llm.go` | 压缩摘要提示词 | 合理，属引擎级但独立场景 |
| `internal/agent/builtin.go` `structuredOutputContract` | 结构化输出契约 | 合理 |
| `internal/agent/spawn.go` `Description()` | Spawn 工具描述 | 属工具描述，非系统提示 |

### 1.3 CodeBuddy 参照系的分类

CodeBuddy 的 `prompts` 数组（60+ 条）可归为几类：

1. **元任务提示词**：`terminal-title-generator-instructions`、`summary-generator-instructions`、`memory-selector-instructions`、`compact-prompt`
2. **主 Agent 提示词**：`cli-agent-prompt`、`base-agent-instructions`、`system-reminder-*`
3. **工具描述提示词**：`tool-read-description`、`tool-bash-description`、`tool-edit-description` 等
4. **产品功能提示词**：`command-commit-prompt`、`command-security-review-prompt`、`insights-facet-*`、`init-prompt`、`content-analyzer-*`
5. **政策提示词**：`cli-agent-prompt` 里的 `content_policy`

---

## 2. 应该内置的提示词清单

### 第一档：补齐引擎能力缺口（强烈建议）

#### 2.1 `session-title`（会话标题生成）—— 元任务类，建议引擎级 + 独立调用

- **对应 CodeBuddy**：`terminal-title-generator-instructions`
- **现状**：heron-ai 会话无标题，续聊/归档体验缺失。
- **提示词要点**（参照 CodeBuddy，适配 heron-ai）：
  - 纯 JSON 输出：`{"isNewTopic": boolean, "title": string|null}`
  - 2-3 词标题，用对话语言（中文标题用中文）
  - 禁止执行用户请求，只识别话题
  - 配套逻辑：幂等锁 + 超时 + 失败重试上限（见 CodeBuddy `session-title-service.ts`）
- **归属**：标题是"元任务"，不是 agent 的常规职责，应像压缩摘要一样作为**独立的轻量调用**（复用 `ModelProvider.Chat`，无 tools），而非注入主 agent 系统提示。

#### 2.2 `knowledge-query` 激活 —— 引擎级，直接接入

- **现状**：`knowledgeQueryTemplate` 已是死代码，`knowledge/injector.go` 在注入知识内容，但模型没有"如何用知识"的指令。
- **动作**：在 `BuildSystemPrompt` 中，当 `agent.Knowledge` 非空时引用 `knowledge-query` 模板（与 `tool-usage` 的 `len(agent.Tools.Builtin) > 0` 条件一致）。
- **提示词要点**：搜索知识库获取背景、交叉验证多来源、标注不确定/冲突信息。

#### 2.3 `entity-spawn`（子 Agent 视角）—— 能力级，建议内置 Skill

- **对应 CodeBuddy**：Task Agent（`agent-explore-instructions` 等子 agent 的视角）
- **现状**：heron-ai 有 `Spawn` 原语，子 agent 收到 `## Your Item`，但缺一条"我是被 spawn 出来的、我的边界、我如何回报"的系统提示。
- **归属**：这是**能力级**的——只有支持 spawn 的 agent 需要，应做成内置 skill（如 `entity-spawn`），agent 声明 `skills: [entity-spawn]` 后注入。
- **提示词要点**：
  - 我是动态 spawn 的子实体，只处理 `## Your Item` 交给我的那一项
  - 我有独立持久 memory（keyed by `key`），先读自己的 memory
  - 完成后用固定结构回报（见 2.5）

#### 2.4 `structured-output` 收紧 —— 引擎级，措辞增强

- **现状**：`structuredOutputContract` 已有"首字符 `{` 末字符 `}`、禁止 prose/Markdown/YAML"。
- **动作**：补 CodeBuddy 的"缺失字段用空值而非省略、禁止第二个 JSON 对象、运行时将拒绝空响应"等更强约束（部分已有，可再收紧措辞）。

### 第二档：值得内置的能力级 Skill（按需，建议做成内置 skill）

#### 2.5 `structured-handoff`（如何传出固定结构）—— 能力级 Skill

- **你的补充点**：告诉 agent「有哪些可分发（spawn 什么 agent）、怎么传出固定结构（structured output 约定）」。
- **现状**：`StructuredOutput` 有 schema 校验，但"何时该用结构化输出、如何把结果传给 coordinator/下游"没有系统提示。
- **归属**：能力级，做成内置 skill。内容是"当你需要把结论交给团队/下游时，按 `structured_output` schema 产出，不要自由发挥；字段缺失用空值"。
- **提示词要点**：
  - 声明本 agent 可产出的记录类型（`output.record`）
  - 按 schema 严格产出，缺失字段用空值/空数组/false/null
  - 区分"给用户看的正文"与"给下游的机器可读结构"

#### 2.6 `approval-await`（审批等待）—— 能力级 Skill

- **对应 CodeBuddy**：`waiting_approval` 门禁语义
- **现状**：heron-ai 有 `waiting_approval` 态，但缺一条"你正在等待人工审批，不要自行模拟审批、不要用另一个 Bash 模拟审批流程"的提示。simple-qa 的 AGENT.md 里手工写过，说明是通用需求。
- **归属**：能力级（只有 `hitl.enabled` 的 agent 需要），做成内置 skill。

### 第三档：明确不内置的（引擎中立，留给用户）

| 提示词 | 理由 |
|---|---|
| `content_policy`（政治/色情/违法拒绝） | 产品/组织政策，不应写死进通用引擎 |
| `insights-facet-*`（8 个数据分析 facet） | CodeBuddy 专属 /insights 功能 |
| `command-commit-prompt`、`command-security-review-prompt` 等 | slash 命令产品功能，非引擎能力 |
| `init-prompt`（生成 CODEBUDDY.md） | 产品功能 |
| `content-analyzer-*`（网页内容分析） | 特定产品功能 |
| `agent-instructions`（生成 agent 配置的元 agent） | 产品功能 |

---

## 3. 落地路径建议

### 3.1 分类决策树

```
这个提示词是「所有 agent 都需要」还是「有某能力的 agent 才需要」？
├── 所有 agent 都需要 → builtin.go 模板（引擎级）
│   ├── execution-management ✅
│   ├── tool-usage ✅
│   ├── memory-management ✅
│   ├── perspective-isolation ✅
│   ├── knowledge-query（激活）
│   └── output-format（收紧）
│
├── 有某能力的 agent 才需要 → 内置 skill（能力级）
│   ├── entity-spawn（spawn 子 agent）
│   ├── structured-handoff（产出固定结构）
│   └── approval-await（HITL 审批）
│
└── 产品/组织价值观 → 不内置，留 agent.body / CODEBUDDY.md
```

### 3.2 落地优先级

1. **P0 — 激活 `knowledge-query`**（一行接入，死代码激活，成本最低价值明确）
2. **P1 — `session-title` 标题生成**（元任务，独立轻量调用，补齐续聊体验）
3. **P1 — 内置 Skill 骨架**：`entity-spawn`、`structured-handoff`、`approval-await` 三个能力级 skill，放到 `.agents/skills/` 或引擎内置 skill 注册表
4. **P2 — `structured-output` 措辞收紧**

### 3.3 内置 Skill 的存放位置（需决策）

heron-ai 的 skill 目前是**用户态**（`.agents/skills/<name>/SKILL.md`，example 里已有）。内置 skill 有两种选择：

- **方案 A**：引擎内置注册表（`internal/skill` 里预注册 `entity-spawn` 等，`app/runtime.go` 启动时注入）—— 开箱即用，agent 声明 `skills:` 即可。
- **方案 B**：提供一份「推荐 skill 模板」放在 `docs/` 或 `examples/`，用户拷贝到 `.agents/skills/` —— 引擎保持中立。

建议：**方案 A 为主**（能力级提示词是引擎能力的一部分），但允许用户覆盖/禁用。此项需进一步决策。

---

## 4. 待决策事项

1. **内置 Skill 存放方式**：引擎预注册（方案 A）vs 文档模板（方案 B）？
2. **`session-title` 是否本期做**：标题生成需要独立的"元任务调用"机制（无 tools 的轻量 `Chat`），工程量比激活 knowledge-query 大。
3. **`structured-handoff` 的边界**：它和现有的 `StructuredOutput` 字段、`output.record` 机制如何分工，需要读 `docs/configuration/team.md` 的 record 约定后细化。
