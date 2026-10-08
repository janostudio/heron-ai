# Skill / Knowledge 渐进式披露设计

> 本文定义 heron-ai 的 Skill 与 Knowledge 从「启动时全量驻留内存 + 全量注入
> 上下文」迁移到「元数据常驻 + 正文按需加载」的设计。依据是 Anthropic Agent
> Skills 的 Progressive Disclosure 模型与 MCP 的 Lazy Schema Loading 实践。

## 1. 问题

### 1.1 现状

三类跨层资源的加载与注入策略不一致：

| 资源 | 内存驻留 | 注入给模型 | 落在哪 |
|---|---|---|---|
| **tool** | 表 + 轻元数据 | `name` + `description` + `parameters` schema | ✅ 基准 |
| **rule** | **只存元数据**（body 延迟） | 命中才读文件取正文 | ✅ 已分层 |
| **skill** | **全量 `Body` 常驻** | **每个声明 skill 的 `Body` 全文** | ❌ 无分层 |
| **knowledge** | **全量 `Content` 常驻** | 命中的 `Content` **全文** | ⚠️ 检索分层，内容没分层 |

具体证据：

- `SkillRegistry.Register`（`internal/skill/registry.go:25`）把整个 `types.Skill`
  **值拷贝**进全局 map，`Skill.Body`（`pkg/types/skill.go:17`）是 `SKILL.md`
  去掉 frontmatter 的**全文**。
- `SkillInjector.Inject`（`internal/skill/injector.go:13-33`）对每个声明的 skill
  把 `skill.Body` 直接 `append` 进 prompts：
  ```go
  if skill.Body != "" {
      prompts = append(prompts, skill.Body)   // injector.go:19-20
  }
  ```
- `Skill.Description` 字段**存在但注入路径完全没用到**。
- `KnowledgeIndex.entries`（`internal/knowledge/store.go:18`）持有全量
  `types.KnowledgeEntry`，其中 `Content` 是全文（`pkg/types/knowledge.go:8`）；
  `formatEntries`（`internal/knowledge/injector.go:116`）注入的是
  `entry.Content` 而非 `entry.Summary`。
  > 注：R5 已删除 `KnowledgeIndex` 与 `injector.go` 整文件，此段保留为改造前
  > 的现状描述。见 §3.4.4。

### 1.2 实测基线

在决定改造成本前，实测了仓库现有 skill 的真实注入量。
测量对象：`examples/auto-bugfix-gitignore`（13 个 skill，全树最多），
一次性注入**全部** 13 个 skill 的 body（比任何单 agent 的实际情况更悲观）：

| 指标 | 实测值 |
|---|---|
| 树内 skill 总数 | 13 |
| 全部 body 注入字节 | **5776 B** |
| 折算 tokens | **~1444** |
| 附带授予的工具数 | **39** |
| registry 是否持有 body | `true`（值拷贝，确认） |
| 仅 listing（name + description）等效 | **1010 B ≈ 252 tokens** |
| **listing 化后的降幅** | **83%** |

单 agent 的实际情况更小（`audit-agent` 声明 2 个 skill → 1023 B ≈ 255 tokens）。

**结论**：
1. 上界场景（13 个全注入）是 **~1.4 K tokens**，且顺带授予 **39 个工具** ——
   工具数比 token 更值得注意，因为它直接影响模型的选择质量。
2. listing 化可省 **83%** 的 skill 注入体积。
3. 但绝对量级（1.4 K → 0.25 K）在典型上下文预算里仍属小项，所以这**首先是
   结构性改造**（热更新、语义一致性、规模护栏），token 是附带收益。

### 1.3 那为什么还要改

三个不依赖规模的理由：

1. **热更新**：body 是启动时值拷贝，改 `SKILL.md` 必须重启。这是功能缺陷，
   与 skill 数量无关。
2. **不对称难理解**：rule 的正文是时读的，skill 不是（见 §1.5）。同一类"外部
   资源"，两套生命周期，使用者必然踩坑。这类认知成本比 token 成本更贵。
3. **工具污染**：实测 13 个 skill 顺带授予 39 个工具。上下文里的工具定义越多，
   模型选择质量越差（这是 MCP 领域的共识问题）。listing 化让工具按需授予。
4. **规模上界没有护栏**：现在没有机制阻止一个 skill 长到 500 行、或一个 agent
   声明 20 个 skill。Anthropic 明确给 `SKILL.md` 设了 500 行建议且用
   `references/` 分层，说明规模增长是必然路径。等撞上再改，成本更高。

**因此实施顺序应按"收益/风险"排**：先做 knowledge（检索层已就绪，改动最小），
再做 skill（涉及工具授予，最复杂）。

### 1.4 后果（结构性）

1. **上下文膨胀**：`SKILL.md` 正文在 skill 被声明时无条件进入 system prompt，
   与"本轮是否真的用到"无关。当前量级小（§1.2），但无上界。
2. **内存驻留**：全部 skill 的 body 全在内存，与是否被引用无关。
3. **热更新失效**：body 是启动时的值拷贝，改 `SKILL.md` 必须重启。
4. **不对称难理解**：rule 的正文是时读的，skill 不是。

### 1.5 rule 已经是对的

`loadRuleMeta`（`internal/config/rule.go:28-43`）**只解析 frontmatter**，
`Content` 留空、只记 `Path`；正文在渲染时通过 `ruleLoader` 按需读
（`internal/runtime/team/runtime.go:1002-1009`）。这是本设计要推广到 skill 与
knowledge 的现成模式 —— **无需引入新机制，只需让另外两类资源采用同一套**。

## 2. 业内模型

### 2.1 Anthropic Agent Skills：三层渐进式披露

| 层 | 内容 | 进入上下文的时机 | 量级 |
|---|---|---|---|
| **1. Listing** | `name` + `description` | **启动时常驻** | 极小 |
| **2. Body** | `SKILL.md` 正文 | **被调用时**注入 | ~200-800 tokens |
| **3. Resources** | `references/` `assets/` | 模型按需读文件 | 按需 |

官方文档的关键约束：

- **只有 name 与 description 常驻**："skill descriptions are loaded into
  context so Claude knows what's available, but full skill content only loads
  when invoked"。
- **body 由 harness 注入，不是模型读文件**："When you or Claude invoke a skill,
  the rendered `SKILL.md` content enters the conversation as a single message
  and stays there across later turns"。
- **第 3 层才是模型自己读**：主 `SKILL.md` 由 harness 注入，`references/`
  下的文件由模型按需引用读取。
- **budget**：listing 总预算 = 模型上下文窗口的 **1%**（`skillListingBudgetFraction`
  可调）；单条 `description` 截断至 **1536 字符**；`SKILL.md` 建议 **< 500 行**，
  超出拆到 `references/`。
- **预算溢出时从最少调用的 skill 开始丢 description**，保高频 skill 的完整文本。
- **description 就是选择信号**："Claude uses this to decide when to apply the
  skill"；frontmatter 解析失败则 metadata 为空，自动匹配失效。

### 2.2 MCP：Lazy Schema Loading

MCP 工具定义遇到同一个问题，解法是 **Search → Inspect → Execute** 三段：

| 阶段 | 进上下文的内容 |
|---|---|
| Search | 只给"工具卡"：`title` / `summary` / `intents` / `required_inputs` / `risk` / `auth_scopes` |
| Inspect | 选中工具的**完整 schema** |
| Execute | 用未裁切的 schema 再校验 + 权限门 |

Claude Code 内建 Tool Search 默认启用："启动时载入工具名称与 server
instructions，完整定义等到需要时才载入"。阈值可配
（`ENABLE_TOOL_SEARCH=auto`，未达上下文 10% 时直接全量加载）。

**重要**：业内并非一刀切 lazy。官方明确提示工具少于约 10 个且 schema 小时，
upfront 加载更快，因为 search 多一次 round trip。

### 2.3 对本设计的取舍

- 采用 Anthropic 的 **listing 常驻 + body 按需注入**，而非 MCP 的 search 阶段。
  原因：skill 的数量级（个位数到几十）远小于 MCP 工具（可上千），
  且 agent 已在 `AGENT.md` 中**显式声明**了 skill 列表 —— 声明本身就是过滤器，
  不需要再用检索来收窄候选。
- 保留"声明即纳入 listing"语义：agent 声明哪些 skill，listing 就含哪些。
  不引入全局 skill 发现。

## 3. 设计

### 3.1 三层结构

```
第 1 层  Listing（常驻）    name + description            → 进 system prompt
第 2 层  Body（按需）       SKILL.md 正文                 → 模型调用 UseSkill 时注入
第 3 层  Resources（按需）  references/ 下的文件          → 模型用 Read 读（已有能力）
```

第 3 层**无需改动**：skill 包内的 `references/` 现在是普通文件，模型已经能用
`Read` 访问。`SKILL.md` 正文只需写明"详细内容见 references/xxx.md"。

### 3.2 激活机制：`UseSkill` builtin tool

沿用 heron 现有的 tool 模型（`name` + `description` + `parameters` 给模型，
执行时才做事），新增一个 builtin tool：

```go
// 参数
{
  "skill": string  // required，要激活的 skill 名
}
// 返回
{
  "skill": "deep_research",
  "activated": true,
  "tools_granted": ["Read", "Grep", "Glob"]   // 该 skill 带来的工具
}
```

语义：

1. 查 registry 确认 skill 存在且在**该 agent 声明的列表内**（越权访问其他
   skill 要拒绝，与 `State` tool 的 `currentAgentID` 边界一致）。
2. 从磁盘**现读** `SKILL.md` 正文（`Skill.Path` + `fileLoader`）。
3. body 作为 **tool result** 返回 —— tool result 天然进入对话消息流，
   且后续轮次仍在上下文里，与 Anthropic "enters the conversation as a single
   message and stays there" 一致。
4. 授予该 skill 声明的 `tools` / `allowed-tools`，**从激活轮开始生效**。
   工具的授予需 per-turn 生效，因此激活状态要在 agent turn 内可累积。

**为什么用 tool 而不是自动注入**：模型的判断比相似度匹配准（Anthropic 也是
模型决定）；且 tool 调用会留在 session 日志里，可审计、可回放。

### 3.3 工具授予的落地

`team/runtime.go:474-489` 现在是每轮从 `agent.Skills` 全量 `Inject` 得到工具并
`appendUnique` 到 `agent.Tools.Builtin`。改造后：

```
listing    → 只产出 prompts（name + description），不产出工具
激活       → UseSkill 返回该 skill 的 tools，累加进本轮及后续轮次的允许集
```

激活状态存在哪：需要一个 **turn 级的已激活 skill 集合**。候选位置：

- **会话级**（写入 agent state）：激活跨轮持续，符合"body 已进上下文就不该丢"。
  但需处理 session 恢复。
- **agent turn 级**（内存）：实现简单，但 body 已在上下文里而工具授予丢了，
  会出现"模型看得见指令却调不了工具"的不一致。

**建议会话级**，与 body 常驻上下文的生命周期对齐。具体实现见 §5 开放问题。

### 3.4 Knowledge 的对应改造（⚠️ 方案已修订为 agentic search）

**修订说明（2026-09-16）**：本节最初设计为「启动时建元数据索引 + 内存 substring
匹配 + summary 注入」，并已实现（见 §4.3 的历史记录）。随后调研发现该方向与业内
主流不一致，现已改向 **agentic search**。修订依据见 §3.4.1。

#### 3.4.1 为什么放弃预索引

Anthropic 在 knowledge/文档检索上的选择是 **grep**，不是"summary 索引"：

> "Early versions of Claude Code used RAG + a local vector db, but we found pretty
> quickly that **agentic search generally works better**. It is also simpler and
> doesn't have the same issues around security, privacy, staleness, and reliability."
> — Boris Cherny（Claude Code 创始人）

内部 benchmark 结论是 agentic search "comprehensively won — by a lot"，整条
RAG 流水线被砍掉。淘汰理由（结构性、非可优化）：

| 缺陷 | 含义 |
|---|---|
| **黑盒不可诊断** | 流水线 5 个环节，结果不对时无法定位是哪一环 |
| **精度链式衰减** | 5 个 90% 环节串联 → ~59%；grep 是精确匹配，无衰减 |
| **索引漂移** | 预建索引是静态快照，源文件变更即失效 |

**关键区分**：Anthropic 的 `description` 索引是给 **skill** 用的（"要不要激活这个
能力"，数量有限、需模型主动介入）。**knowledge/文档检索**走的是 grep。两者不是
一回事，把 skill 的模型套到 knowledge 上是错的。

**heron 原有实现的问题**（`entryMatches`，`store.go:705`）：

```go
fields := []string{entry.ID, entry.Title, entry.Summary}
fields = append(fields, entry.Keys...)
if strings.Contains(searchText, query) { return true }   // 内存 substring
```

- 是**内存 substring 匹配**，不是 grep：无正则、无按行、无文件扫描
- 只搜**元数据**，搜不到正文 —— 能力**严格弱于**已有的 `Grep` 工具
- 同时继承索引漂移（改 `summary` 要重启）与不可诊断（为何未命中？）

#### 3.4.2 修订后的方案

| 项 | 修订前（已实现） | 修订后 |
|---|---|---|
| 检索 | 启动时元数据索引 + 内存 substring | **删除预索引**；agent 用 `Grep`/`Glob`/`Read` 实时搜 |
| 注入 | 命中即自动注入 summary | **只注入一个轻量指路块**（知识库位置 + 可见范围） |
| 正文 | `ReadKnowledge(id)` 新工具 | 不需要新工具，`Read` 即可 |
| 权限 | `LookupAllowed` scope 过滤 | **必须补**：给 Grep/Glob 加 scope 路径过滤（见下） |

#### 3.4.3 真正的难点：scope 隔离

实测确认（`internal/workspace`）：`.agents` **不在** `isDefaultExcludedDir` 的排除
列表（只排除 `.git`/`node_modules`/`dist`/`build`/`target`）。因此 agent 用现有
`Grep` **可以读到其他 agent 的私有知识**：

```
match: .agents/agents/other-agent/knowledge/private.md:1 PRIVATE_SECRET_TOKEN
```

这是方案 A 必须解决的核心问题。难点在于 `GrepTool` 位于 `internal/tool`，
**完全不接触 agent 身份** —— 它不知道谁在调用（身份 key 是 `internal/agent`
的私有 context key，`tool` 包读不到）。

可选解法：

| # | 方案 | 评价 |
|---|---|---|
| 1 | 由 agent 层提供路径级 allowlist，注入给工具 | 需要把身份从 `agent` 传到 `tool`；改动面中等 |
| 2 | 目录布局隔离：私有知识移出 workspace 可达范围 | 最彻底（grep 天然到不了）；但要改 `.agents/agents/*/knowledge` 的位置 |
| 3 | 接受现状（知识库非机密） | 最省事，但 `scope` 字段就失去意义 |

**倾向 1**：`scope` 字段的存在说明隔离是设计意图，且方案 2 会破坏"私有知识随
agent 目录一起拷贝"的可移植性（见 `docs/configuration/skill.md` 的 Skill 包契约）。

#### 3.4.4 已实现部分的去留

> **实施状态（2026-09-18，batch R5 已落地）**：下表为 R5 执行后的最终结论，
> 与本文档早前版本的「保留」判断有两处不同，原因记录在备注里。

| 组件 | 处置 |
|---|---|
| `ReadKnowledge` 工具 | **删除**。R2 给 `Grep`/`Read` 加了路径过滤后，它已不比裸 `Read` 多任何权限校验；它唯一强绑定的 `KnowledgeIndex.LookupAllowed` 正是迫使索引存在的东西。代价：id→path 的映射消失，模型须用 `Glob` 先定位文件再 `Read`（见下） |
| `KnowledgeIndex` 与全部检索方法 | **删除**（`Add`/`Search`/`SearchWithScope`/`SearchWithScopeAndAllowlist`/`List`/`Count`/`LookupAllowed`） |
| `KnowledgeInjector`（`injector.go`） | **删除**。整文件，含 `Inject`/`InjectWithAllowlist`/`InjectAll`/`formatEntries`/`recordHits`/`KnowledgeUsageInstruction` |
| `StatsRecorder` / `StatsKey` / `stats.jsonl` | **删除**。hit 由注入器在匹配时记录，注入器没了就再没有生产者。`heron knowledge gc` 改为只依据 `expires_at` |
| `KnowledgeExtractor` | **删除**。唯一用途是往索引里灌条目 |
| `deriveSummaryFromBody` | **保留**。summary 仍是 `RebuildIndex`（写 `index.md`）与 `FindDuplicate`（learn 路径去重）的输入 |
| `MarkdownStore.Load` 去 body | **保留**。这是纯收益（内存不驻留正文）；R5 后它还是「这棵树里有没有东西」的唯一判据，Bash gate 与指路块都问它 |
| `entryMatches` | **保留**（范围收窄）。检索用途随索引删除，但 `FindDuplicate` 仍用它——那是**写路径去重**，不是检索 |
| 注入格式 | **改为**固定的轻量指路块（`internal/knowledge/pointer.go`）：只列该 agent 可读的目录，零条目内容 |

**指路块的三个设计点**（实现于 `internal/knowledge/pointer.go`）：

| 点 | 取值 | 理由 |
|---|---|---|
| `Stability` | `stable`（原为 `semi_stable`）| 文本只由 agent 身份决定，不再随 query 变；同一 agent 跨轮逐字节相同，可进 prompt cache 前缀 |
| `Placement` | `system` | 与 skill listing、rules 同类：常驻前缀而非每轮变动内容 |
| `Compressible` | `false` | 仅约 8 行、固定、且是「去哪找」的前提；被压掉等于静默禁用知识查找 |
| 是否出现 | 该 agent 至少有一个可读目录 | `.agents/knowledge/` 对所有人可读故恒列出；私有目录仅在有可加载条目时列出，否则等于指模型去撞墙 |

**已知能力差异（有意为之）**：删掉 `ReadKnowledge` 后，**只凭 id 不再能直接取正文** ——
没有 id→path 映射了。模型需两步：`Glob`/`Grep` 定位文件，再 `Read`。这正是指路块
指向目录（而非条目）的预期流程，`internal/agent/knowledge_access_test.go` 对此有测试
（`TestKnowledgeBodyHasNoIDToPathMapping` 与 `TestGlobThenReadRecoversTheBodyByID`）。

**hit 统计的死因**：`recordHits` 是唯一的生产者，且它在注入器的匹配路径上。检索改成
agentic search 后，没有任何组件能观察到「模型读了一个知识文件」——`Grep`/`Read` 是通用
工具，不知道某个路径恰好是知识。因此 `heron knowledge gc` 不再报告 hit，也不能再按
「久未被使用」归档；改为只依据 `expires_at`。

#### 3.4.5 原设计决策（历史记录）

以下为修订前的决策，保留作决策沿革：

| # | 决策 | 结论 |
|---|---|---|
| 1 | 索引是否含 `Content` | **不含**。索引只存元数据，正文不驻留内存 |
| 2 | 检索依据 | **只用元数据**（ID / Title / Summary / Keys / Scope），正文不参与匹配 |
| 3 | 正文获取 | **新增 `ReadKnowledge(id)` builtin tool**，与 `UseSkill` 同构 |
| 4 | `summary` 来源 | **缺失时自动派生**（从正文首段），只入内存索引、不写回文件 |

| 项 | 现状 | 目标 |
|---|---|---|
| 索引内容 | 全量 `KnowledgeEntry`（含 `Content`） | 去掉 `Content`，保留 `ID`/`Title`/`Summary`/`Keys`/`Scope`/`Path` |
| 注入 | `entry.Content` 全文（`injector.go:116`） | `entry.Summary` |
| 检索匹配字段 | `ID/Title/Summary/Content/Keys`（`store.go:491`） | 去掉 `Content` |
| 正文 | 内存常驻 | 按需读 `Path`（同 `ruleLoader` 模式） |
| 工具 | — | 新增 `ReadKnowledge(id)` |

#### 4.1 为什么必须自动派生 summary

去掉 `Content` 后，`summary` 成为检索的唯一语义依据。但 `summary` 是**可选字段**：
实测仓库 23 个 knowledge 文件中仅 1 个有 `summary`，3 个完全没有 frontmatter
（加载后 `Title`/`Summary`/`Keys` 全空）。

若不派生，这些 entry 只能靠 ID 字符串偶然命中 —— 检索实际失效。

因此**加载时**（`MarkdownStore.Load`）在 `Summary` 为空时从正文派生：
取首个非标题、非空段落，截断到上限（建议 200 字符）。派生值**只进内存索引，
不写回文件** —— 文件内容的写回是 `Save`/`UpsertActive` 的职责，加载路径只读。

派生是**兜底**而非替代：frontmatter 有 `summary` 时永远优先使用它。

#### 4.2 summary 作为质量约定

`description`（skill）与 `summary`（knowledge）是同一类东西：**模型的选择信号**。
文档需明确这一约定，且可考虑在加载时对缺失 `summary` 发出警告（不失败），
推动配置质量逐步提升。

#### 4.3 实施记录（knowledge 部分，两阶段）

第一阶段（元数据索引 + summary 注入，后被推翻）：

| 项 | 落点（当时） | R5 后 |
|---|---|---|
| 索引去 body | `MarkdownStore.load(ctx, withBody)`；导出 `Load`/`LoadAll` 走 `withBody=false` | 保留 |
| 写路径保持 body | `Archive`、`findByID` 传 `withBody=true` | 保留 |
| summary 派生 | `deriveSummaryFromBody` | 保留（改由 index.md 与 dedup 消费） |
| 检索去 Content | `entryMatches` 只用 `ID/Title/Summary/Keys` | `entryMatches` 保留但仅供 `FindDuplicate` |
| 注入格式 | `- <title> [<id>]: <summary>`（`injector.go:formatEntries`） | **删除**，改为 `pointer.go` 的固定指路块 |
| 正文按需 | `ReadKnowledge` builtin tool（`internal/agent/knowledge_tool.go`） | **删除**，改用 `Read`/`Grep`/`Glob` |
| 权限 | `KnowledgeIndex.LookupAllowed(id, agent, team, allowlist)` | **删除**，权限由 `internal/tool/path_scope.go` 的路径过滤承担 |

第二阶段即 §3.4.4 所述的 R5 改造。

**派生规则的三层降级**（这是实现中最容易做错的地方）：

```
tier 1  prose     标题下的句子              ← 最好的选择信号
tier 2  items     列表项（用 ";" 连接）      ← 无 prose 时的兜底
tier 3  heading   标题本身                  ← 好过空白
```

**关键陷阱**：最初按"空行分段 + 跳过以 `#` 开头的段落"实现，在本仓库自己的
数据上**派生出空 summary**。原因是一个 Markdown 块
`## 标题\n- 要点`（标题与内容之间无空行）是**同一段**，整段被跳过；若文档
通篇由标题 + 列表构成，则什么都派生不出来。而 summary 为空的 entry
在 `index.md` 里就是一个没有描述的条目，按元数据去重的 `FindDuplicate`
也失去判据。

因此实现改为**逐行**扫描：首个标题不终止扫描（标题是标签不是正文），
遇到正文或"正文之后的第二个标题"才结束开头块。回归测试见
`TestDerivedSummaryFromHeadingPlusListBody`。

优先做 knowledge：检索层已就绪，改动面小，可作为"元数据常驻 + 正文延迟"
在本仓库的第二个验证点。

### 3.5 配置格式

**`SKILL.md` 现有字段保持不变**，`description` 升格为**必填且被真正使用**：

```yaml
---
name: deep_research
description: "Deep research: systematically search and analyze information. Use when the task needs multi-source investigation."
tools:
  - Read
  - Grep
  - Glob
---
```

新增约定（不新增字段）：

- `description` 必须以"何时使用"结尾（Anthropic 的 `when_to_use` 语义），
  因为它是模型唯一的选择信号。
- 正文超 500 行应拆分到 `references/`，正文只留纲要 + 指向。
- 需要携带额外元数据时用 `metadata`（自由 map），引擎不解释。

`types.Skill` 需补 `Path string` 字段（供延迟读取），与 `rule.Path` 同构。

## 4. 改动清单

### 4.1 `internal/config`

| 文件 | 改动 |
|---|---|
| `skill.go` / `definitions.go:318` | 拆分 `loadSkill`：新增 `loadSkillMeta`（只读 frontmatter，记 `Path`，不读 body），`loadSkill` 保留供需要全文的调用方 |
| `definitions.go:57` | `loadSkillDefinitions` 改用 meta 版本 |
| `pkg/types/skill.go` | 新增 `Path string` 字段（`yaml:"-"`），与 `types.RuleItem.Path` 同构 |

注意：`validateSkillScripts`（`definitions.go:303`）在 meta 加载下仍需执行 ——
它只依赖 frontmatter 的 `scripts` 字段，不受影响。

### 4.2 `internal/skill`

| 文件 | 改动 |
|---|---|
| `registry.go` | `Register` 只存 meta（`Skill` 不含 body）；新增 `List()` 返回元数据快照 |
| `injector.go` | 拆分职责：`Catalog(names)` 只产出 `name + description` 的 listing 文本（不再叫 `Inject`，语义变了）；新增 `LoadBody(ctx, name)` 现读正文 |

### 4.3 `internal/runtime/team`

| 位置 | 改动 |
|---|---|
| `runtime.go:474-481` | `Inject` → `Catalog`；`ContextBlock.Kind` 从 `skills` 改为 `skill_listing`，`Stability` 保持 `stable`，`Priority` 保持 80 |
| `runtime.go:474-489` | 工具不再按声明全量 `appendUnique`，改为按**已激活集合**累加 |
| `runtime.go` | 新增 `SetSkillFileLoader`，与现有 `SetRuleLoader`（`:77`）同构 |

### 4.4 `internal/agent`

新增 `internal/agent/skill_tool.go`，结构对齐 `state_tool.go`：

```go
type SkillTool struct { /* registry, loader, activation store */ }
func NewSkillTool(...) *SkillTool
func (t *SkillTool) Name() string { return "UseSkill" }
func (t *SkillTool) NeedsApproval() bool { return false }
func (t *SkillTool) Execution() types.ToolExecutionSpec {
    return types.ToolExecutionSpec{Class: types.ToolSerial}
}
```

必须用 `currentAgentID(ctx)` 做越权校验（同 `state_tool.go:63-71`）。

**双重注册**（否则工具对模型不可见且无任何报错）：
1. `internal/app/runtime.go` 里 `toolRegistry.Register(...)`
2. `internal/agent/runtime.go` 的 `builtinSchemas` map
3. 使用 `UseSkill` 的 agent 需在 `AGENT.md` 的 `tools.builtin` 里声明它

### 4.5 不变量（必须保持）

- **不引入全局 skill 发现**：listing 只含该 agent 在 `AGENT.md` 声明的 skill。
- **`Inject` 的调用方语义变化必须有测试**：现有测试若断言 prompts 含 body 全文，
  需改为断言 listing，并新增"body 不在 listing 里"的断言。
- **越权拒绝**：`UseSkill` 激活未声明的 skill 必须失败。
- **正文时读**：改 `SKILL.md` body 后，下一次 `UseSkill` 应拿到新内容（热更新）。

## 5. 开放问题

| # | 问题 | 倾向 |
|---|---|---|
| 1 | 激活状态的生命周期：会话级 vs turn 级 | **会话级**，与 body 常驻上下文的寿命对齐；但需定义 session 恢复时的重放语义 |
| 2 | listing 是否设字符预算（Anthropic 用 1%）| 暂不设硬上限，但记录 listing 字符数，超阈值告警；skill 数量级小，先观察 |
| 3 | `description` 是否设为加载期必填 | **是**，缺失即加载失败；它是唯一选择信号，缺失等于 skill 不可用 |
| 4 | 是否保留"自动注入 body"的兼容开关 | 保留一个 `skills.auto_inject: true` 开关，供少量 skill 且希望零 round trip 的场景（对应 Anthropic 的 `disable-model-invocation` 与 MCP 的 `auto` 阈值） |
| 5 | 激活后如何**撤销** | 暂不提供。上下文里的 body 无法真正移除；若要控制，靠 compaction 自然淘汰 |
| 6 | knowledge 是否也走 tool 激活 | knowledge 已有 query 检索，`formatEntries` 改注入 `Summary` 即可，不需要模型显式激活 |

## 6. 验证

### 6.1 单元测试

- `TestSkillRegistryStoresMetadataOnly`：`Register` 后 registry 内的 `Skill.Body` 为空。
- `TestSkillCatalogOmitsBody`：`Catalog` 输出含 `name`/`description`，**不含** body 正文。
- `TestUseSkillLoadsBodyFromDisk`：改盘上 `SKILL.md` 后，`UseSkill` 返回新正文。
- `TestUseSkillRejectsUndeclaredSkill`：激活未声明 skill 失败。
- `TestUseSkillGrantsTools`：激活后该 skill 的 `tools` 出现在允许集内。
- `TestPointerBlockNamesOnlyReadableDirectories` / `TestPointerBlockOmittedWhenNoKnowledge`：
  指路块只列出该 agent 可读的目录，无目录可指时完全不出现。
- `TestPointerBlockIsQueryIndependent` / `TestPointerBlockHasNoEntryContent`：
  块文本不随 query 变、不随知识库增长——这是取代逐条注入的两个性质。
- `TestReadReachesKnowledgeBodyByPath` / `TestGrepSearchesKnowledgeBodies`：
  `ReadKnowledge` 删除后，文件工具仍能读到知识正文（能力未丢失）。
- `TestBuildToolSchemasIncludesUseSkill`：`tools.builtin: [UseSkill]` 时
  `buildToolSchemas` 必须返回该 schema —— 这是**静默失败点**的守卫测试。

### 6.2 效果度量

改造前基线（§1.2 已实测，作为对照 arm）：

| 指标 | 改造前（实测） |
|---|---|
| 13 个 skill 全量 body 注入 | 5776 B ≈ 1444 tokens |
| 附带授予工具数 | 39 |
| listing 等效体积 | 1010 B ≈ 252 tokens |
| 改 `SKILL.md` body 后生效 | ❌ 需重启 |

要测的指标：

- **listing 字符数**：改造前后对比同一次执行的 system prompt 体积。
  预期降幅 ~83%（5776 → 1010 B）。
- **授予工具数**：预期从 39 降到"按需激活后的实际个数"。这是比 token 更重要的
  指标 —— 工具定义数量直接影响模型选择质量。
- **首轮 input tokens**：同任务、同 model、同 prompt 下改造前后对比。
  **注意**：绝对节省仅 ~1.2 K tokens，占比取决于上下文预算。若测得占比 < 5%，
  说明主要收益来自热更新与工具收敛，而非 token —— 结论要如实记录，不要为
  改造找 token 理由。
- **任务质量**：固定任务集上对比完成率，确认节省不以正确率为代价
  （Anthropic 与 MCP 文档都强调先守正确率再看 token）。
- **round trip 开销**：激活 skill 多一次工具调用，测量"到首次正确使用 skill
  的时间"是否变差。这是 lazy 方案的主要代价，必须量化。

### 6.3 端到端

用带 skill 的真实样例跑完整 turn，确认 listing 进 prompt、`UseSkill` 能激活、
激活后工具可用：

- `examples/auto-bugfix-gitignore` —— 13 个 skill，`audit-agent` 声明 2 个，
  是验证 listing 与激活的最小真实场景。
- `examples/blog-writer` —— 4 个 skill，含 `deep_research`，带 `references/`，
  可顺带验证第 3 层（模型用 `Read` 读 references）。

验收：改造前先跑一次存下 system prompt 与工具调用序列作为对照，改造后跑同一
任务对比。

## 7. 参考

- Anthropic Claude Code — Agent Skills（progressive disclosure、listing budget、
  `description` 作为选择信号）
- Agent Skills 开放标准（`name` / `description` / `license` / `compatibility` /
  `metadata` / `allowed-tools`）
- MCP Lazy Schema Loading / Tool Search（Search → Inspect → Execute 三段式，
  `defer_loading` 机制）
- 本仓库既有先例：`internal/config/rule.go` 的 `loadRuleMeta` +
  `internal/runtime/team/runtime.go:984` 的 `renderRulesWithLoader`
- 相关文档：`docs/context-management.md`（上下文分层与压缩）、
  `docs/configuration/skill.md`（skill 配置格式）
