# 00 配置设计：BDD 驱动

> 目录：`docs/generic-engine/`
> 本篇定位：**从 BDD 场景出发，设计各实体的配置方案**。先定场景（谁想做什么），再落到配置文件。
> 前置阅读：[01-ddd-strategic.md](./01-ddd-strategic.md)（实体定义）[02-entity-relations.md](./02-entity-relations.md)（关系图）

---

## 一、配置优先级模型

```
本地文件（默认配置）     前端/API 传入（覆盖配置）      最终生效配置
─────────────────  +  ─────────────────────────  =  ──────────────
.agents/agents/*.md    POST /api/run { agents:  runtime 内存中的
.agents/teams/*.yml           teams: {...},      AgentDef / RoundDef
my_novel/flow.yml            flows: {...} }     / FlowDef 实例
.agents/knowledge/*.md
.agents/rules/*.md
```

**覆盖规则**：
- 前端/API 传入的字段 **完全覆盖** 本地文件的同名字段（不是 merge）
- 未传入的字段保留本地文件的值
- 优先级链：`API 传入 > 本地文件 > 引擎默认值`

**合并算法**（深度合并，非浅覆盖）：

```go
// mergeConfig 深度合并：override 的非 nil 字段覆盖 base 的同名字段
// 嵌套 map 递归合并，非 map 类型直接替换
func mergeConfig(base, override map[string]any) map[string]any {
    result := copyMap(base)
    for key, val := range override {
        if val == nil { continue } // nil 表示"不覆盖"
        if baseVal, ok := result[key]; ok {
            if baseMap, baseIsMap := baseVal.(map[string]any); baseIsMap {
                if overrideMap, overrideIsMap := val.(map[string]any); overrideIsMap {
                    result[key] = mergeConfig(baseMap, overrideMap) // 递归合并
                    continue
                }
            }
        }
        result[key] = val // 非 map 或类型不同 → 直接替换
    }
    return result
}
```

**示例**：

```yaml
# 本地 agent.md 配置
model:
  provider: openai
  model: gpt-4o
  temperature: 0.3
  max_tokens: 4096

# API 覆盖
agents:
  researcher:
    model:
      model: claude-sonnet-4  # 只改 model，不改 provider/temperature/max_tokens

# 最终生效
model:
  provider: openai          # 保留本地
  model: claude-sonnet-4    # API 覆盖
  temperature: 0.3          # 保留本地
  max_tokens: 4096          # 保留本地
```

**不合并的情况**：
- `tools.builtin: ["Read", "Edit"]` — 数组类型整体替换，不是 append
- `permissions.ask: ["delete_file"]` — 数组整体替换
- `hooks: {on_start: "log"}` — map 类型递归合并（只覆盖 on_start，保留其他 hook）

**为什么不用数据库**：配置是静态定义（Agent 配方、Round 编组、Flow 时间轴），不是运行时数据。文件版本可追踪、可 diff、可复用。运行时状态（Run 的进度、Agent 私有状态）才走存储。

---

## 二、BDD 场景

### 场景 1：定义一个 Agent

> **作为** 开发者
> **我想** 用一个 Markdown 文件定义 Agent（YAML frontmatter 元数据 + Markdown body 作为 System Prompt）
> **以便** 引擎能加载并实例化这个 Agent 运行

### 场景 2：定义一组 Round（一个回合的协作）

> **作为** 开发者
> **我想** 用 YAML 定义一组 Agent 怎么配合（阶段序列 + Task 分配）
> **以便** 引擎能按这个配方跑一次协作

### 场景 3：定义一条 Flow（时间轴编排）

> **作为** 开发者
> **我想** 用 YAML 定义时间轴（早上/中午/下午/晚上，每段触发哪个 Round）
> **以便** 引擎能按时间轴驱动整个 Run

### 场景 4：定义知识库

> **作为** 内容创作者
> **我想** 用 Markdown 定义领域知识（关键词触发注入）
> **以便** Agent 在推理时能获取相关背景

### 场景 5：定义规则与护栏

> **作为** 开发者
> **我想** 用 Markdown 定义全局/Agent 级的约束规则和输出护栏
> **以便** Agent 行为受控、输出安全

### 场景 6：前端/API 覆盖配置启动 Run

> **作为** 用户
> **我想** 在启动一个 Run 时，传入部分覆盖配置（换模型、换 Persona）
> **以便** 不修改本地文件就能定制本次运行

### 场景 7：Agent 定义用 Markdown（配置 + Prompt 合并在一个文件）

> **作为** 开发者
> **我想** 用一个 Markdown 文件定义 Agent（YAML frontmatter 元数据 + Markdown body 作为 System Prompt）
> **以便** 与主流 CLI 工具一致，一个文件看懂一个 Agent

---

## 三、目录结构

**极简原则**：
- `.agents/` 是唯一默认配置根，包含全部配置能力（settings/models/mcp + 实体配置 + skills）
- `.agents/` 内置一套最小默认配置（一个 Flow + 一个 Round + 一个问答 Agent），引擎开箱即用
- 用户可指定其他项目根目录，Flow 的 `config_root` 指向包含 `.agents/` 的目录，引擎自动读 `{config_root}/.agents/`

### Agent 自包含设计

**核心理念**：一个 Agent 目录就是完整的配置包——Agent 定义 + 私有 knowledge + 私有 rules，拖到别的项目直接能用。

**共享 vs 私有**：

| 资源 | 放在全局 `.agents/` 下 | 放在 Agent 目录内 |
|------|----------------------|------------------|
| knowledge/ | 所有 Agent 共享的知识 | 该 Agent 私有的知识 |
| rules/ | 所有 Agent 生效的规则 | 该 Agent 私有的规则 |
| skills/ | 引擎级 Skill（所有 Agent 可用）| — |

**引用方式**：

| 资源 | 作用域 | 引用方式 |
|------|--------|---------|
| Agent 目录内的 `knowledge/` `rules/` | 该 Agent 私有 | 自动加载（同目录即关联）|
| `.agents/knowledge/` | 所有 Agent 共享 | 自动注入（scope=all 的条目）|
| `.agents/rules/` | 所有 Agent 生效 | 自动生效（scope=all 的规则）|
| `.agents/skills/` | 所有 Agent 可用 | Agent frontmatter 中 `skills: [name]` 声明式引用 |

```
project-root/
├── AGENTS.md
│
├── .agents/                         # 默认配置根
│   ├── settings.json
│   ├── models.json
│   ├── mcp.json
│   │
│   ├── flows/
│   │   └── default.yml              # 默认 Flow
│   ├── teams/
│   │   └── default.yml              # 默认 Round
│   │
│   ├── agents/
│   │   ├── default.md               # 简单 Agent：纯 .md 文件
│   │   │
│   │   └── researcher/               # 复杂 Agent：目录自包含
│   │       ├── AGENT.md             # Agent 定义（YAML frontmatter + body）
│   │       ├── knowledge/           # 该 Agent 私有的知识（可选）
│   │       │   └── case_files.md
│   │       └── rules/               # 该 Agent 私有的规则（可选）
│   │           └── conduct.md
│   │
│   ├── knowledge/                   # 全局共享知识（所有 Agent 可见）
│   │   └── world.md
│   │
│   ├── rules/                       # 全局规则（所有 Agent 生效）
│   │   └── safety.md
│   │
│   ├── prompts/                      # 引擎默认提示词模板（可选，覆盖代码内置）
│   │   ├── task-management.md
│   │   ├── tool-usage.md
│   │   ├── memory-management.md
│   │   ├── knowledge-query.md
│   │   ├── perspective-isolation.md
│   │   └── output-format.md
│   │
│   └── skills/                      # 引擎级 Skill（所有 Agent 可用）
│       └── deep_research/
│           └── SKILL.md
│
└── my_novel/                        # 用户项目（Flow.config_root 指定）
    └── .agents/
        ├── flow.yml
        ├── agents/
        │   ├── hero/
        │   │   ├── AGENT.md
        │   │   ├── knowledge/
        │   │   └── rules/
        │   └── villain/
        │       └── AGENT.md
        ├── teams/
        ├── knowledge/               # 该项目的共享知识
        └── rules/                   # 该项目的全局规则
```

**Agent 两种形态**：

| 形态 | 文件 | 适用 |
|------|------|------|
| **单文件** | `agents/<name>.md` | 简单 Agent（纯 Prompt + 配置，不需要私有 knowledge/rules）|
| **目录** | `agents/<name>/AGENT.md` + `knowledge/` + `rules/` | 复杂 Agent（需要私有知识库和规则）|

引擎加载 Agent 时：先检查 `agents/<name>/AGENT.md` 是否存在（目录形态），不存在则读 `agents/<name>.md`（单文件形态）。

### 3.2 加载优先级

```
{config_root}/.agents/          ←  最高（用户指定项目）
  ↓ 未找到
当前项目 .agents/               ←  默认配置（内置最小可运行配置）
```

**与当前项目 `.agents/` 的关系**：`config_root` 指定外部项目后，实体配置（agents/teams/flows/knowledge/rules）从 `{config_root}/.agents/` 加载。全局配置（settings.json/models.json/mcp.json/skills/）始终从当前项目 `.agents/` 加载——引擎级配置不随 Flow 切换。

| 实体 | 格式 | 原因 |
|------|------|------|
| Agent | `.md` | LLM 要读 body（System Prompt），frontmatter 放配置 |
| Knowledge | `.md` | LLM 要读 content，frontmatter 放 keys/scope |
| Rule | `.md` | LLM 要读规则文本，frontmatter 放 type/scope |
| Round | `.yml` | 纯引擎编排参数，LLM 不读 |
| Flow | `.yml` | Signal 路由表 + 默认顺序兜底，LLM 不读 |
| Skill | `SKILL.md` | 主流共识（Claude Code/CodeBuddy/Codex）|

**对比主流工具**：

| 工具 | 配置目录 | Agent 文件 | Skill 目录 | Flow/Round |
|------|---------|-----------|-----------|-----------|
| Claude Code | `.claude/` | `agents/*.md` | `skills/<name>/SKILL.md` | — |
| CodeBuddy | `.codebuddy/` | `agents/*.md` | `skills/<name>/SKILL.md` | `workflows/*.js` |
| Codex CLI | `.agents/` | — | `.agents/skills/<name>/SKILL.md` | — |
| **本项目** | **`.agents/`** | **`agents/*.md`** | **`skills/<name>/SKILL.md`** | **`teams/*.yml` + `flows/*.yml`** |

**与 AGENTS.md 的关系**：`AGENTS.md` 是给 CodeBuddy/Claude Code 读的项目说明。`.agents/` 是本项目引擎自己的配置目录。两者不冲突——前者是 AI 编程助手的上下文，后者是运行时引擎的配置。

---

## 四、全局配置

引擎级的全局设置，不属于任何实体。

### 4.1 settings.json — 引擎参数

```jsonc
// .agents/settings.json
{
  // ── Flow 默认值 ─────────────────────
  "flow": {
    "loopMaxRounds": 0             // 0 = 不循环（每次输入执行一个环节后返回）
                                   // > 0 = 循环模式，最多自主推进 N 个环节
  },

  // ── Team 默认值 ─────────────────────
  "team": {
    "maxParallelAgents": 10,      // 同一 Round 内最大并发 Agent 数
    "maxParallelTeams": 1         // 同一 Flow 内最大并发 Round 数
  },

  // ── Agent 默认值 ────────────────────
  "agent": {
    // 运行时
    "loopMaxRounds": 10,          // Turn（Agent 内部 LLM 调用循环） 最大轮次
    "timeout": "120s",            // 单 Agent 运行超时
    "toolMode": "parallel",       // 工具执行模式：parallel | sequential
    "stream": true,               // 默认开启流式输出

    // 权限
    "permissions": {
      "allow": [],                // 允许的工具列表（空=全部允许）
      "deny": [],                 // 禁止的工具列表
      "ask": []                   // 调用前需确认的工具列表
    },

    // 生命周期
    "hooks": {
      "onAgentStart": "log_agent_start",
      "onAgentEnd": "log_agent_end",
      "onToolStart": "log_tool",
      "onToolEnd": "log_tool",
      "onHandoff": "log_handoff",
      "onError": "log_error"
    },

    // 可观测
    "tracing": {
      "enabled": true,
      "sampleRate": 1.0,
      "includeSensitiveData": false
    }
  },

  // ── 日志 ────────────────────────────
  "logging": {
    "level": "info",              // debug | info | warn | error | fatal
    "output": "stdout",           // stdout | file
    "maxFileSize": "50MB",        // 单文件大小上限，超过则轮转
    "maxBackups": 5               // 保留最近 N 个轮转文件
  },

  // ── 可观测性 ────────────────────────
  "observability": {
    "retentionDays": 30,          // Run 数据保留天数，超过自动删除 {run_id}/ 目录
    "eventBus": {
      "bufferSize": 256,          // Event Bus channel 默认 buffer 大小
      "enablePersistence": false  // 是否持久化事件到 events.jsonl
    }
  },

  // ── 运行时路径（项目根目录下）───────
  "paths": {
    "data": ".agents/data/"               // 运行时数据根目录（所有 Run 数据在此）
  }
}
```

### 4.2 models.json — LLM Provider 列表

```jsonc
// .agents/models.json
{
  "providers": [
    {
      "name": "openai",
      "apiKey": "${OPENAI_API_KEY}",
      "models": [
        { "id": "gpt-4o",      "maxTokens": 128000 },
        { "id": "gpt-4o-mini", "maxTokens": 128000 }
      ],
      "default": "gpt-4o-mini"
    },
    {
      "name": "anthropic",
      "apiKey": "${ANTHROPIC_API_KEY}",
      "models": [
        { "id": "claude-sonnet-4-20250514", "maxTokens": 200000 },
        { "id": "claude-haiku-4-20250514",  "maxTokens": 200000 }
      ],
      "default": "claude-haiku-4-20250514"
    },
      "default": "claude-haiku-4-20250514"
    },
    {
      "name": "ollama",
      "apiKey": "not-needed",
      "baseUrl": "http://localhost:11434/v1",
      "models": [
        { "id": "llama3.1", "maxTokens": 128000 }
      ],
      "default": "llama3.1"
    }
  ],

  // 全局默认 Provider（Agent 未指定时使用）
  "defaultProvider": "openai"
}
```

**环境变量引用**：`${VAR_NAME}` 语法，引擎启动时从环境变量读取。也支持 `.env` 文件（项目根目录）。

### 4.3 mcp.json — 全局 MCP 服务列表

```jsonc
// .agents/mcp.json
{
  "servers": [
    {
      "name": "web_search",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@anthropic/mcp-server-search"],
      "env": {
        "API_KEY": "${SEARCH_API_KEY}"
      }
    },
    {
      "name": "database",
      "transport": "http",
      "url": "http://localhost:3000/mcp",
      "headers": {
        "Authorization": "Bearer ${DB_TOKEN}"
      }
    }
  ]
}
```

### 4.4 配置优先级（全局 → 实体 → API）

```
settings.json 默认值  ←  最低优先
  ↓ 被覆盖
Agent/Round/Flow 文件中的同名配置
  ↓ 被覆盖
POST /api/run 传入的覆盖配置  ←  最高优先
```

---

## 五、实体配置定义

### 5.1 Agent 配置（Markdown：frontmatter + body）

**场景 1 + 场景 7 实现**：一个 Markdown 文件定义 Agent 的全部。YAML frontmatter 放配置，Markdown body 就是 System Prompt（支持 Go template 变量）。

```markdown
---
# .agents/agents/researcher.md
name: researcher
persona:
  role: "研究员"
  goal: "搜索和分析指定主题的最新信息，给出结构化的研究报告"
  backstory: "一名资深研究员，擅长信息检索和归纳总结"

model:
  provider: openai
  model: gpt-4o
  temperature: 0.3
  max_tokens: 4096

reasoning:
  enabled: true
  max_attempts: 3

tools:
  builtin:
    - Read
    - Edit
    - Write
    - Grep
    - Glob
    - Grep
    - TodoWrite
    - TodoRead

skills:
  - name: deep_research
    path: .agents/skills/research/

mcpservers:
  - name: web_search
    transport: stdio
    command: npx
    args: ["-y", "@anthropic/mcp-server-search"]

handoff:
  enabled: false

guardrails:
  input:
    - type: regex
      pattern: "^.{1,5000}$"
      message: "输入不能超过5000字符"
  output:
    - type: schema
      schema: ResearchResult          # Pydantic/Go struct 名

structured_output:
  type: json
  schema:
    title: string
    summary: string
    findings:
      type: array
      items:
        fact: string
        source: string
        confidence: string

# 工具审批（HITL）
# 不再使用 human_in_loop.enabled 字段。
# 审批触发由两层决定（见 04C / 04D）：
#   1. Tool 级 needs_approval（工具定义里硬编码）
#   2. Agent 级 permissions.ask（这里配置，列出需要确认的工具名）
permissions:
  allow: []                  # 允许的工具列表（空=全部允许）
  deny: []                   # 禁止的工具列表
  ask: []                    # 调用前需人工确认的工具列表

loop:
  max_rounds: 10
  tool_mode: parallel                 # parallel | sequential
  timeout: 60s

hooks:
  on_start: log_start
  on_end: log_end
  on_tool_start: log_tool
  on_tool_end: log_tool

tracing:
  enabled: true
  sample_rate: 1.0

checkpoint:
  enabled: false
---

# System

你是 **{{.Persona.Role}}**。{{.Persona.Backstory}}

## 目标
{{.Persona.Goal}}

## 行为准则
{{range .Rules}}
- {{.}}
{{end}}

## 可用工具
{{range .Tools}}
- **{{.Name}}**: {{.Description}}
{{end}}

## 输出格式
请按以下 JSON schema 输出：
{{.StructuredOutput.Schema}}

# User

{{.Input}}
```

**与主流 CLI 对比**：

| 工具 | Agent 文件格式 |
|------|--------------|
| Claude Code | `agents/*.md` — YAML frontmatter + Markdown body |
| CodeBuddy | `agents/*.md` — YAML frontmatter + Markdown body |
| **本项目** | **`agents/*.md` — YAML frontmatter + Go template body** |

差异说明：本项目 body 支持 Go template 变量（`{{.Persona.Role}}` 等），因需运行时注入上下文。Claude Code/CodeBuddy 的 body 是纯静态 System Prompt。

### 5.2 Round 配置

**场景 2 实现**：定义一组 Agent 如何协作。

```yaml
# .agents/teams/research_round.yml
name: research_round
description: "研究回合：两个研究员并发搜索，一个主编汇总"

stages:
  - process: parallel
    tasks:
      - name: research_topic_a
        agent: researcher
        description: "从技术角度研究 {{.Topic}}"
        expected_output: "技术分析报告，含关键发现和引用来源"
        tools: []                      # 覆盖 Agent 级工具（空=继承）
        context: []                    # 前置 Task ID 列表
        output_format: json
        human_input: false
        guardrail:
          - type: schema
            schema: ResearchResult

      - name: research_topic_b
        agent: researcher
        description: "从商业角度研究 {{.Topic}}"
        expected_output: "商业分析报告，含市场规模和竞争格局"
        context: []
        output_format: json
        human_input: false

  - process: sequential
    tasks:
      - name: synthesize
        agent: writer
        description: "综合两份研究报告，产出最终分析"
        expected_output: "综合分析报告（Markdown 格式）"
        context: [research_topic_a, research_topic_b]
        output_format: text
        human_input: false
```

### 5.3 Flow 配置

**场景 3 实现**：定义时间轴编排。

```yaml
# my_novel/flow.yml
name: my_novel_story
description: "小说 A 的剧情推进流程"

config_root: "my_novel/"              # ← 指定项目根目录
                                      #    引擎自动读 {config_root}/.agents/
                                      #    Agent → my_novel/.agents/agents/
                                      #    Round  → my_novel/.agents/teams/
                                      #    未找到时 fallback 到 .agents/

loop_max_rounds: 20                   # > 0 = 循环模式，最多自主推进 20 个环节

stages:
  - name: opening
    team: opening_round
    on_signal:
      continue: development
      wait_input: null
      goal_achieved: null
      goal_failed: null
      goal_impossible: null

  - name: development
    team: story_round
    on_signal:
      continue: climax
      wait_input: null
      goal_achieved: null
      goal_failed: null
      goal_impossible: null

  - name: climax
    team: story_round
    on_signal:
      continue: ending
      wait_input: null
      goal_achieved: null
      goal_failed: null
      goal_impossible: null

  - name: ending
    team: ending_round
    on_signal:
      continue: null                   # 最后一个阶段，所有信号结束
      wait_input: null
      goal_achieved: null
      goal_failed: null
      goal_impossible: null
```

**切换组合**：选不同 Flow 即切换整组角色卡。

```
POST /api/run { flow: "my_novel/flow.yml" }     # 用 my_novel/ 配置
POST /api/run { flow: "another/flow.yml" }     # 用 another/ 配置
POST /api/run                                   # 用 .agents/flows/default.yml
```

### 5.4 Knowledge 配置

**场景 4 实现**：定义知识库条目。Markdown 格式：frontmatter 放 keys/scope，body 就是知识内容。

```markdown
---
# .agents/knowledge/lore.md
name: lore_knowledge
description: "世界观知识"
---

# 血月

血月是这个世界每百年出现一次的天象，传说会唤醒沉睡的古代生物。

> keys: 血月, red moon, 赤月
> scope: all

---

# 审讯室

审讯室位于警局地下二层，灯光昏暗，只有一张铁桌和两把椅子。

> keys: 审讯室, interrogation room
> scope: all

---

# 匕首

一把刻有古老符文的白银匕首，刀柄镶嵌红宝石。

> keys: 匕首, dagger, 凶器
> scope: ["researcher"]

---

# 世界观

故事发生在近未来的赛博朋克城市「新东京」。

> always: true
> scope: all
```

**scope 解析规则**（解析为 `Scope` 结构体，见 04K）：

| 写法 | 解析结果 |
|------|---------|
| `> scope: all` | `Scope{Type: "all"}` — 所有 Agent 可见 |
| `> scope: owner_only` | `Scope{Type: "owner_only"}` — 仅 Agent 私有 knowledge 目录中的条目有效，加载时绑定 owner |
| `> scope: ["researcher", "writer"]` | `Scope{Type: "agents", Agents: ["researcher", "writer"]}` — 精确匹配 agent 列表 |

### 5.5 Rule 配置

**场景 5 实现**：定义全局约束规则。Markdown 格式：frontmatter 放 type/scope，body 就是规则文本。

```markdown
---
# .agents/rules/global.md
name: global_rules
description: "全局行为约束"
---

# 视角隔离

你不能读取其他 Agent 的内心想法和私有记忆。

> type: soft
> scope: all

---

# 隐私保护

不能输出真实的个人信息（电话/地址/身份证号）。

> type: hard
> scope: all

---

# 世界观约束

这个世界没有魔法，所有现象必须有科学或技术解释。

> type: soft
> scope: all
```

```markdown
---
# .agents/rules/safety.md
name: safety_rules
description: "安全约束"
---

# 内容限制

不能详细描写暴力、血腥场景。

> type: hard
> scope: all

---

# 内容过滤

> type: guardrail
> pattern: (暴力|血腥|色情)
> action: block
> message: 输出包含不允许的内容，已被拦截。
> scope: all
```

---

## 六、API 覆盖配置

**场景 6 实现**：前端/API 启动 Run 时传入覆盖配置。

```json
// POST /api/run
{
  "flow": "my_novel/flow.yml",

  // 可选：覆盖 Agent 配置
  "agents": {
    "researcher": {
      "model": {
        "model": "claude-sonnet-4-20250514",
        "temperature": 0.5
      },
      "persona": {
        "goal": "研究指定主题并输出中文报告"
      }
    }
  },

  // 可选：覆盖 Round 配置
  "teams": {
    "research_round": {
      "stages": [
        {
          "process": "parallel",
          "tasks": [
            {
              "name": "custom_research",
              "agent": "researcher",
              "description": "研究用户指定的话题"
            }
          ]
        }
      ]
    }
  },

  // 可选：覆盖 Flow 配置
  "flow_config": {
    "loopMaxRounds": 10
  },

  // 可选：注入临时变量（Prompt 模板渲染用）
  "variables": {
    "Topic": "AI Agent 框架对比",
    "Language": "zh-CN"
  },

  // 可选：注入临时知识（不影响本地文件）
  "knowledge": {
    "entries": [
      {
        "keys": ["CrewAI"],
        "content": "CrewAI 是一个多 Agent 框架...",
        "scope": "all"
      }
    ]
  }
```

---

## 七、配置生效流程

```
POST /api/run { flow: "my_novel/flow.yml", agents: {...}, teams: {...} }
  │
  ├── 1. 加载全局配置（始终从 .agents/ 加载）
  │     .agents/settings.json → 引擎参数
  │     .agents/models.json → LLM Provider 列表
  │     .agents/mcp.json → 全局 MCP 服务列表
  │
  ├── 2. 加载 Flow → 确定 config_root
  │     my_novel/flow.yml → FlowDef
  │     → config_root = "my_novel/"
  │     → 配置根 = {config_root}/.agents/
  │
  ├── 3. 级联加载实体配置
  │     {config_root}/.agents/agents/*.md → AgentDef[]
  │       ↓ 未找到
  │     .agents/agents/*.md → 默认问答 Agent
  │
  │     同理：teams/ knowledge/ rules/
  │
  ├── 4. API 覆盖
  │     对每个实体：API 传入的字段完全覆盖本地文件的同名字段
  │     未传入的字段保留本地值
  │
  ├── 5. 注入变量
  │     variables: { Topic: "AI Agent" } → 渲染 Prompt 模板中的 {{.Topic}}
  │
  ├── 6. 构建运行时对象
  │     FlowDef + RoundDef[] + AgentDef[] + Knowledge + Rules + Settings
  │     → 实例化 Run → 开始执行
  │
  └── 7. 执行
        Run 持有最终配置 → Run 驱动 Round → Round 驱动 Agent Turn
```

---

## 八、配置校验

引擎加载配置时校验：

| 校验项 | 规则 | 失败行为 |
|--------|------|---------|
| 引用完整性 | Round 引用的 agent 必须在 `.agents/agents/` 中存在 | 加载失败，报错 |
| 引用完整性 | Flow 引用的 team 必须在 `.agents/teams/` 中存在 | 加载失败，报错 |
| 引用完整性 | Task.context 引用的 task name 必须在同 Round 内存在 | 加载失败，报错 |
| Schema 校验 | 每个文件必须符合对应的 schema（.md 文件解析 frontmatter，.yml 文件解析结构体）| 加载失败，报错 |
| 必填字段 | Agent: persona.role/goal/backstory 必填 | 加载失败，报错 |
| 循环依赖 | Flow.on_signal 的跳转不能形成无限循环 | 加载时检测，报错 |
| 工具存在性 | Agent.tools.custom 引用的函数必须在代码中注册 | 加载失败，报错 |

---

## 九、BDD 验收场景

### 验收 1：最小配置启动

```
Given 只有一个 Agent 配置文件（researcher.md）
  And 没有 Round 配置（引擎使用默认单 Agent Round）
  And 没有 Flow 配置（引擎使用默认单轮 Flow）
When 调用 POST /api/run（不传 flow，使用 .agents/flows/default.yml）
Then 引擎加载默认配置，成功启动一个单 Agent 单轮 Run
```

### 验收 2：API 覆盖模型

```
Given 本地 researcher.md 配置 model: gpt-4o
When 调用 POST /api/run {
       agents: { researcher: { model: { model: "claude-sonnet" } } }
     }
Then 最终生效的 Agent.model.model = "claude-sonnet"
  And 其他字���（persona/tools）保持本地配置值
```

### 验收 3：多阶段 Flow 完整执行

```
Given my_novel/flow.yml 定义 4 个阶段
When 调用 POST /api/run { flow: "my_novel/flow.yml" }
Then 引擎按顺序执行 opening → development → climax → ending
  And 每个阶段的 Round 内 Agent 按 Process 执行
  And 所有 Signal = continue 时自动推进
```

### 验收 4：Signal 路由

```
Given 某 Round 的 Aggregator 输出 Signal = wait_input
When Flow 读到 wait_input
Then Flow 暂停，返回给用户等待输入
  And 用户再次调用 POST /api/run（不带覆盖配置）
  And Flow 从暂停的环节继续执行
```

### 验收 5：配置校验失败

```
Given researcher.md 缺少 persona.role 字段
When 引擎加载配置
Then 报错 "Agent 'researcher': persona.role is required"
  And Run 不启动
```

### 验收 6：知识触发注入

```
Given knowledge/lore.md 定义了 keys=["血月"] 的知识条目
  And scope=all
When Agent 的 input 包含 "血月"
Then 引擎匹配到知识条目
  And 将 content 注入 Agent 的 user prompt
```

---

## 十、Go 配置结构体定义

```go
// config/def.go

type AgentConfig struct {
    Name             string             `yaml:"name"`
    Persona          PersonaConfig      `yaml:"persona"`
    Model            ModelConfig        `yaml:"model"`
    Reasoning        ReasoningConfig    `yaml:"reasoning"`
    Tools            ToolsConfig        `yaml:"tools"`
    Skills           []SkillConfig      `yaml:"skills"`
    MCPServers       []MCPServerConfig  `yaml:"mcpservers"`
    Handoff          HandoffConfig      `yaml:"handoff"`
    Guardrails       GuardrailConfig    `yaml:"guardrails"`
    StructuredOutput *StructuredOutput  `yaml:"structured_output"`
    Permissions      PermissionsConfig  `yaml:"permissions"`  // 工具审批（见 04C/04D）
    Loop             LoopConfig         `yaml:"loop"`
    Hooks            HooksConfig        `yaml:"hooks"`
    Tracing          TracingConfig      `yaml:"tracing"`
    StateSchema      *StructuredOutput  `yaml:"state_schema"`  // State 的 JSON Schema（每个 Agent 自定义，见 04G）
    Checkpoint       CheckpointConfig   `yaml:"checkpoint"`
    Body             string             `yaml:"-"`  // Agent .md 的 Markdown body（System Prompt 主体，见 04P/048）。非配置字段，解析时填充
}

type PersonaConfig struct {
    Role      string `yaml:"role"`
    Goal      string `yaml:"goal"`
    Backstory string `yaml:"backstory"`
}

type ModelConfig struct {
    Provider    string  `yaml:"provider"`
    Model       string  `yaml:"model"`
    Temperature float64 `yaml:"temperature"`
    MaxTokens   int     `yaml:"max_tokens"`
}

type PermissionsConfig struct {
    Allow []string `yaml:"allow"`  // 允许的工具列表（空=全部允许）
    Deny  []string `yaml:"deny"`   // 禁止的工具列表
    Ask   []string `yaml:"ask"`    // 调用前需人工确认的工具列表
}

type ToolsConfig struct {
    Builtin []string         `yaml:"builtin"`
    Custom  []string         `yaml:"custom"`
    MCP     []MCPToolConfig  `yaml:"mcp"`
}

type ReasoningConfig struct {
    Enabled     bool `yaml:"enabled"`      // 是否启用推理模式（o1/Claude thinking），见 04L
    MaxAttempts int  `yaml:"max_attempts"` // 推理失败重试上限
}

// SkillConfig Agent 声明的 Skill 引用
type SkillConfig struct {
    Name string `yaml:"name"`  // Skill 名称（对应 .agents/skills/<name>/SKILL.md）
    Path string `yaml:"path"`  // Skill 目录路径（可选，默认 .agents/skills/<name>/）
}

// StructuredOutput 结构化输出约束（见 04R）
type StructuredOutput struct {
    Type   string         `yaml:"type"`   // json
    Schema map[string]any `yaml:"schema"`  // JSON Schema 定义
}

// HandoffConfig 任务委派配置（见 04B）
type HandoffConfig struct {
    Enabled  bool     `yaml:"enabled"`   // 是否启用委派
    Targets  []string `yaml:"targets"`   // 可委派的目标 Agent 列表
    MaxDepth int      `yaml:"max_depth"` // 最大委派深度（防递归），默认 3
}

// GuardrailConfig 输入输出护栏（见 04A）
type GuardrailConfig struct {
    Input  []GuardrailRule `yaml:"input"`
    Output []GuardrailRule `yaml:"output"`
}

type GuardrailRule struct {
    Type    string `yaml:"type"`    // regex | schema | llm
    Pattern string `yaml:"pattern"` // regex 模式
    Schema  string `yaml:"schema"`  // JSON schema 名称
    Message string `yaml:"message"` // 拦截时返回的消息
}

// LoopConfig Agent 内部 tool-use loop 参数（见 047）
type LoopConfig struct {
    MaxRounds int           `yaml:"max_rounds"`  // 最大轮次，默认 10
    ToolMode  ToolExecMode  `yaml:"tool_mode"`   // parallel | sequential
    Timeout   time.Duration `yaml:"timeout"`     // 单轮 Agent 运行超时，默认 120s
}

type ToolExecMode string
const (
    ToolModeParallel   ToolExecMode = "parallel"
    ToolModeSequential ToolExecMode = "sequential"
)

// HooksConfig 生命周期钩子配置（见 04Q）
// key = 事件名（on_start/on_end/on_tool_start/on_tool_end/on_handoff/on_error）
// value = 函数名（需在代码中注册）
type HooksConfig map[string]string

// TracingConfig 可观测配置（见 04O）
type TracingConfig struct {
    Enabled     bool    `yaml:"enabled"`      // 是否开启追踪
    SampleRate  float64 `yaml:"sample_rate"`  // 采样率 0.0-1.0
}

// CheckpointConfig 检查点配置
type CheckpointConfig struct {
    Enabled bool `yaml:"enabled"` // 是否开启每 Round 自动 checkpoint
}

// MCPServerConfig / MCPToolConfig 见 04E
type MCPServerConfig struct {
    Name      string            `yaml:"name"`
    Transport string            `yaml:"transport"` // stdio | http | sse
    Command   string            `yaml:"command"`   // stdio: 可执行文件
    Args      []string          `yaml:"args"`
    URL       string            `yaml:"url"`       // http/sse: 服务地址
    Headers   map[string]string `yaml:"headers"`
    Env       map[string]string `yaml:"env"`
}

type MCPToolConfig struct {
    Server string `yaml:"server"` // 引用的 MCPServer 名称
    Tools  []string `yaml:"tools"` // 允许的工具列表（空=全部）
}

type RoundConfig struct {
    Name               string        `yaml:"name"`
    Description        string        `yaml:"description"`
    Stages             []StageConfig `yaml:"stages"`
    DefaultOutputAgent string        `yaml:"default_output_agent"`  // parallel 最后阶段时，指定哪个 Agent 的产出作为 Team 输出（见 04S）
}

type StageConfig struct {
    Process string       `yaml:"process"` // parallel | sequential | hierarchical
    Tasks   []TaskConfig `yaml:"tasks"`
}

type TaskConfig struct {
    Name           string              `yaml:"name"`
    Agent          string              `yaml:"agent"`
    Description    string              `yaml:"description"`
    ExpectedOutput string              `yaml:"expected_output"`
    Tools          []string            `yaml:"tools"`
    Context        []string            `yaml:"context"`
    OutputFormat   string              `yaml:"output_format"`
    HumanInput     bool                `yaml:"human_input"`
    Guardrail      []GuardrailRule     `yaml:"guardrail"`
}

type FlowConfig struct {
    Name        string            `yaml:"name"`
    Description string            `yaml:"description"`
    ConfigRoot      string            `yaml:"config_root"`      // 项目根目录（可选）
    LoopMaxRounds   int               `yaml:"loop_max_rounds"`  // 0=不循环, >0=最大自主推进环节数
    Stages      []FlowStageConfig `yaml:"stages"`
}

type FlowStageConfig struct {
    Name      string              `yaml:"name"`
    Team      string              `yaml:"team"`
    OnSignal  map[string]*string  `yaml:"on_signal"` // signal → next_stage (null=stop)
}

// Knowledge 文件解析（.md）
// 文件 frontmatter: name, description
// 每个 --- 分隔的 section 解析为一条 KnowledgeEntry
// frontmatter 的 keys/always/scope 放正文前的引用块（> 开头）
// body 为知识内容
type KnowledgeEntry struct {
    Keys    []string // 触发关键词（从 > keys: 解析）
    Always  bool     // 始终注入（从 > always: 解析）
    Scope   Scope    // 可见范围（从 > scope: 解析）
    Content string   // Markdown body（知识内容）
}

type Scope struct {
    Type   string   // all（全局）| team（指定 Team）| agents（指定 Agent 列表）
    Teams  []string // Type=team 时，指定可见的 team 列表
    Agents []string // Type=agents 时，指定可见的 agent 列表
}

// Rule 文件解析（.md）
// 文件 frontmatter: name, description
// 每个 --- 分隔的 section 解析为一条 RuleItem
// frontmatter 的 type/scope/pattern 放正文前的引用块（> 开头）
// body 为规则文本
type RuleItem struct {
    Type    string // soft | hard | guardrail（从 > type: 解析）
    Scope   string // 生效范围（从 > scope: 解析）
    Pattern string // guardrail 匹配模式（从 > pattern: 解析）
    Content string // Markdown body（规则文本）
}
```
