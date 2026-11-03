# Heron Team 分享与加载设计（草案 v2）

**状态：** 方案草案，待评审
**范围：** `heron teams`（列表）、`heron teams add` / `remove` / `export`
**关联：** `docs/generic-engine/04-modules/04F-skill.md`、`internal/definitions/`、`internal/config/definitions.go`

**v2 变更（相对 v1）：**

1. **只做 Team 维度**，不做 Agent 单独分享。Agent 作为 Team 的组成部分被打包，
   不能脱离 Team 独立分发。理由见 §2.2。
2. **命令面只有四种用法**：`heron teams`（列表本身）+ `add` / `remove` / `export`。
   没有 `teams list`，命令越少越好（§5.1）。
3. **`teams add` 接受远程地址**（GitHub 仓库、`.tar.gz` URL），不只是本地路径（§5.3）。
4. **`teams export` 默认产出压缩包**，不是目录（§5.6）。
5. **存储布局定为 `_teams/<scope>/`**：包整体落在一个顶层目录，卸载一条 `rm -rf`，
   加载器只需多扫一个根（§5.4）。
6. **不声明模型。** `models.json` 是纯本地资产，既不打包也不在 manifest 里声明，
   装完缺模型交由运行时既有错误提示（§8.1）。

## 1. 背景

当前所有 Team / Agent / Skill / 知识 / 规则都只能从本地 `.agents/` 目录加载，
`LoadDefinitions` 一次性把一个目录树编译成 `types.Definitions`
（`internal/config/definitions.go:26-92`）。别人编排好的一套 Team，只能靠手工复制目录，
而且复制完通常跑不起来。

"跑不起来"才是真正的难点。一个 Team 实际牵连的资产远比它自己多：

| 资产 | 被谁引用 | 落在哪 |
|---|---|---|
| Agent 定义 | `Call.AgentID` | `.agents/agents/<id>/AGENT.md` |
| Agent 私有知识 | 目录归属（无配置字段，§9.2） | `.agents/agents/<id>/knowledge/` |
| 全局知识 | 目录归属（无配置字段，§9.2） | `.agents/knowledge/` |
| Skill | `AgentConfig.Skills` | `.agents/skills/<name>/SKILL.md` + `scripts/` |
| Skill 脚本 | `Skill.Scripts` | 同上，`scripts/` 子目录 |
| 规则 | `AgentConfig.Rules` | `.agents/rules/` 或 `.agents/agents/<id>/rules/` |
| 工具 | `AgentConfig.Tools.Builtin/Custom/MCP` | 引擎内置 / MCP 配置 |

**`models.json` 不参与分享。** 它是收件方的本地环境（含 api_key / base_url），
既不打包、也不在 manifest 里声明。分享包里 Agent 的 `model.model` 保留作者原本写的值，
那是行为语义的一部分（"这个 Agent 本来是给哪个模型调的"）；收件方用不用这个模型，
由他自己的 `models.json` 和运行时决定，装包阶段不介入（见 §8.1）。

真正"分享会出事"的是两类：

1. **Skill 脚本**：可以执行任意命令。装别人的 Team 等于让它跑别人的 shell。
2. **工具授权**：`Tools.Builtin` 里可能有 `Bash`/`Write`。别人的 Agent
   默认不该拿到这些。

所以装载不能是"解压一个目录进去"，必须是**带依赖闭包、带安全边界、带冲突策略的单元**。

## 2. 目标与非目标

### 2.1 目标

- 一个 Team 能被打包成**自包含单元**，含它引用的全部 Agent / Skill / 知识 / 规则。
- 来源可以是**本地路径或远程地址**（GitHub 仓库、`tar.gz` URL）。
- 接收方能**先看后装**（解析 → 预览 → 确认），不是盲装。
- 装完能被现有 `LoadDefinitions` 直接加载，不需要改加载语义。
- 同一份 Team 可以装多次、多来源，同名冲突有明确策略，不是后装覆盖先装。
- 分享出去的包**不含密钥**，且默认收紧工具授权。
- 卸载是一步操作，不留残渣。

### 2.2 非目标（本期）

- **不做 Agent 单独分享。** 见下方理由。
- 不做中心化市场服务、账号体系、评分/排行。分享靠地址（GitHub 仓库 / 链接）流转。
- 不做自动升级与语义化版本求解（依赖只做精确匹配 + 冲突报错）。
- 不做私有仓库认证以外的来源托管；认证复用 `git`/`curl` 既有凭据，不新建账号体系。
- 不解决跨包的 Flow 组合（分享单位是 Team，Flow 是使用方自己的事）。

**为什么先不做 Agent：**

Agent 单独分享看起来更小，实际上更难保证可用：

| 问题 | Team 分享 | Agent 单独分享 |
|---|---|---|
| 它要干什么 | Team 的 `goal` + call 的 `responsibility` 写清楚了 | 只有一个 persona，没有使用场景 |
| 怎么验证能用 | 装完 `heron --flow` 跑一轮就知道 | 装完没地方跑，得先自己编个 Team 套上 |
| 引用是否会断 | 闭包从 Team 出发，所有引用都能追到 | Agent 可能引用只在自己的 Team 里才有意义的 Skill/知识 |
| 冲突面 | 一个 Team 名字 | 每个 Agent 都可能撞名，且撞名后无法判断该用哪个 |

也就是说，**Agent 的分享价值依附于 Team**。先把 Team 这一层做扎实，
Agent 分发可以之后作为"从已分享的 Team 里单独取一个 Agent"的能力补上，
那时它已经有了最小可运行上下文，不是裸的 persona。

## 3. 核心模型

### 3.1 两个概念

```text
TeamPackage  一个可分发单元 = 清单(manifest) + 资产树(files)
Manifest     声明"我是谁、我要什么、我提供什么"，不含运行逻辑
```

Manifest 里的"需求"用名字表达（可在接收方被替换），"提供物"是实际文件
（不可变，由内容哈希标识）。两者分开，才能既保证完整性又允许本地适配。

### 3.2 包目录结构

```text
review-team/
├── heron-team.yml               # manifest，唯一必需文件
├── teams/
│   └── review_team.yml
├── agents/
│   ├── security-reviewer/
│   │   └── AGENT.md
│   └── lead-reviewer/
│       └── AGENT.md
├── skills/
│   └── owasp-check/
│       ├── SKILL.md
│       └── scripts/
│           └── scan.sh
└── rules/
    └── review-standards.md
```

子目录名与 `.agents/` 完全一致，安装时直接映射过去，loader 不需要知道"包"的存在。

### 3.3 Manifest（`heron-team.yml`）

```yaml
apiVersion: heron/v1
kind: team
name: review-team
version: 1.2.0
description: 安全与性能并行审查
authors: ["someone"]
license: MIT

# 本包提供的资产
provides:
  teams:  [review_team]
  agents: [security-reviewer, lead-reviewer]
  skills: [owasp-check]
  rules:  [review-standards]

# 本包对外部资产的依赖（接收方解析或替换）
requires:
  skills:
    - name: code-fix
      version: ">=1.0.0"

# 安全声明（见 §6）
sandbox:
  tools: [Read, Grep, Glob]      # 包内所有 agent 的工具上限
  scripts: review                # allow | deny | review（默认 review）
```

manifest 只描述**资产之间**的关系，不涉及运行环境。模型配置、密钥、endpoint
都不在 manifest 里（原因见 §8.1）。

## 4. 依赖闭包

### 4.1 引用图

```text
Team.Calls[*].agent ──> Agent
Agent.Skills         ──> Skill ──> Skill.Knowledge ──> Knowledge
Agent.Knowledge      ──> Knowledge
Agent.Rules          ──> Rule
Agent.Model.Model    ──> Model（只声明 id，不打包）
Agent.Tools.*        ──> 内置工具 / MCP（不打包）
```

从入口 Team 出发做反向遍历，把所有能到达的资产收进来。
图里允许环（Skill 依赖的 Knowledge 又被 Skill 的 prompt 引用），遍历要记录已访问节点。

### 4.2 打包时怎么算

复用 `internal/definitions/render.go:53-66` 已有的资源收集器
（它已经在扫 `.agents/knowledge/` 与每个 agent 的私有 knowledge，并排除 `index.md`）。
注意 `knowledgeIDs` 本身在 §9.2.2 要随字段一起删除，这里的"复用"指的是同一个目录遍历思路，
不是复用那个函数。
需要补的：

- 从 `types.Team.Calls` 取 `AgentID` 集合
- 从 `types.AgentConfig.Skills / Knowledge / Rules` 取引用名
- 每个 Skill 目录**整体复制**（含 `scripts/`），不按文件挑
- 跳过 `data/`、`models.json`、`flows/`、`settings.json`

### 4.3 不打包的东西

| 资产 | 原因 | 替代 |
|---|---|---|
| `models.json` | 含 api_key / base_url，属收件方本地环境 | 不需要替代，见 §8 |
| `flows/*.yml` | 是使用方的编排决策 | 装完给粘贴片段 |
| `settings.json` | 含全局 env、web 配置 | 不传 |
| `data/` | 会话与运行态 | 不传 |
| `knowledge/**`（全局） | 可能含个人信息，不允许携带分享 | **一律不带，无选项** |
| `agents/<id>/knowledge/**`（私有） | 同上，且是使用沉淀 | **一律不带，无选项** |
| `teams/<team>/knowledge/**`（私有） | 同上 | **一律不带，无选项** |
| `AGENT.md` 的 `knowledge:` 字段 | 字段本身已删除（§9.2.2） | 无需处理 |

## 5. 命令行

### 5.1 命令面：`heron teams` 一个名词，三个动词

现有 CLI 只有两个入口：全局 flag（`heron --prompt`）和 `heron <domain> <sub>`
（目前仅 `heron knowledge [gc|<sid>]`，`cmd/server/main.go:36-38`）。

Team 相关操作走第二种，并且**命令数量压到最少**：

```bash
heron teams                        # 列表（无子命令时就是 list）
heron teams add <source> [flags]   # 装
heron teams remove <name> [flags]  # 卸
heron teams export <team> [flags]  # 导出
```

**`heron teams` 本身就是列表，没有 `heron teams list`。**
同样没有 `heron teams ls`、`heron teams show`、`heron listeams` 之类的别名。
一个名词 + 三个动词，四种用法，到此为止。

**为什么不是 `heron addteam` / `removeteam`：**

| 方案 | 好处 | 问题 |
|---|---|---|
| `heron addteam` / `removeteam` | 单看命令即可知道用途 | 动词成为命令名的一部分，将来加 `updateteam` / `webteam` 会无限膨胀；且与 `heron knowledge` 的 `名词 + 子命令` 风格不一致 |
| `heron team add` | 标准 CLI 风格 | 既有子命令只有 `knowledge`，单数形式没有先例 |
| **`heron teams add`** | 与 `heron knowledge gc` 同构；`teams` 名词本身即列表命令 | —— |

不用 `install`/`uninstall`：它们暗示程序安装，而这里只是往配置树加一组可随时撤的文本定义。

### 5.2 四条用法

```bash
# ---- 列表 ----
heron teams
```

列表输出**直接区分原生与下载**，不需要额外的过滤 flag：

```text
TEAM               SOURCE                       VERSION  STATUS
review_team        local                        -        ok
@review-team       github.com/x/y#v1.2          1.2.0    ok
@blog              ./blog.tar.gz                -        model missing
```

- 不带前缀的是本地原生定义（`.agents/teams/`，或 flow 绑定的 team）
- 带 `@` 的是从包安装的（`.agents/_teams/<scope>/`），`SOURCE` 记录来源
- `STATUS` 只在有问题时给提示（缺模型、依赖缺失），正常显示 `ok`

```bash
# ---- 装 ----
heron teams add ./review-team
heron teams add ./review-team.tar.gz
heron teams add https://github.com/x/y
heron teams add https://github.com/x/y#v1.2
heron teams add https://x.com/team.tar.gz

  --dry-run            只解析和预览，不写盘
  --yes                跳过脚本 review 确认
  --as <scope>         覆盖安装作用域（默认取 manifest 的 name）
  --refresh            忽略缓存，重新下载

# ---- 卸 ----
heron teams remove @review-team           # 默认整包卸载
heron teams remove @review-team --keep-agents

# ---- 导出 ----
heron teams export review_team             # -> ./review_team.tar.gz
heron teams export review_team -o /tmp/out
heron teams export review_team --format dir   # 调试用
```

- `<source>` 里 `#ref` 与 `--ref <rev>` 等价，二选一
- `remove` 的参数带 `@` 前缀，与列表输出一致，避免与本地同名 Team 混淆
- `export` 默认压缩包：分享场景下压缩包才是可传递物，目录是中间态

### 5.3 远程来源

`teams add` 支持三类地址，统一先落到临时目录再走同一条解析路径：

| 来源形态 | 识别方式 | 取法 |
|---|---|---|
| 本地路径 | 存在且是目录 / `.tar.gz` | 直接用 |
| GitHub 仓库 | `https://github.com/<owner>/<repo>`（可带 `#ref`） | `codeload.github.com/<o>/<r>/tar.gz/<ref>` |
| 任意 `.tar.gz` 直链 | 以 `.tar.gz`/`.tgz` 结尾 | HTTP GET |

```text
teams add <url>
  ├─ 归一化：拆出 ref，缓存键 = sha256(url + "@" + ref)
  ├─ 命中缓存（且无 --refresh）→ 直接用
  ├─ 下载 / 解压到临时目录
  └─ 进入与本地路径相同的解析流程
```

约束：

- **只认 `https`**。`git@` / `ssh` 需要凭据交互，本期不做。
- 私有仓库复用 `$GITHUB_TOKEN` → `Authorization: Bearer`，不新建账号体系。
- 必须含 `heron-team.yml`；解压限制单文件与总大小，防解压炸弹。
- **符号链接一律拒绝**（防止指向安装目录之外）。
- 内容寻址缓存位于 `~/.heron/cache/<sha256>/`，不写进 `.agents/`。

**不需要 Node。** 下载由 Go 二进制的 `teams add` 用 `net/http` + `archive/tar` + `compress/gzip` 完成。

### 5.4 存储布局：`_teams/<scope>/`（已定）

装进来的包整体落在 `.agents/_teams/<scope>/`，与本地 `.agents/` 目录完全分开。

曾经的备选是 `@scope/` 前缀（把包资产嵌进本地目录），已否决：
它把文件和本地混在一起，卸载要跨多个目录删，加载器要改五处遍历。
`_teams` 的方案卸载是一条 `rm -rf`，加载器只需多扫一个根。

下面保留对比，供后来者理解为什么不选 A。

#### 方案 A：`@scope/` 前缀（扁平，已否决）

```text
.agents/
├── teams/
│   ├── review_team.yml                  # 本地原生
│   └── @review-team/
│       └── review_team.yml              # 来自包
├── agents/
│   ├── security-reviewer/               # 本地原生
│   └── @review-team/
│       ├── security-reviewer/
│       └── lead-reviewer/
└── skills/
    ├── code-fix/
    └── @review-team/
        └── owasp-check/
```

#### 方案 B：`_teams/<scope>/` 前缀（集中）

```text
.agents/
├── teams/
│   └── review_team.yml                  # 本地原生（不变）
├── _teams/                              # 所有装进来的包集中在顶层
│   └── review-team/
│       ├── heron-team.yml
│       ├── teams/review_team.yml
│       ├── agents/{security-reviewer,lead-reviewer}/
│       ├── skills/owasp-check/
│       └── rules/
├── agents/                              # 完全不动
├── skills/
└── knowledge/
```

#### 对比

| | A：`@scope/` | B：`_teams/<scope>/` |
|---|---|---|
| 加载器改动 | 每类资产都要认两级（agents/teams/skills/knowledge/rules 各一处） | 只在加载时**多扫一个根**，每类资产内部规则不变 |
| 卸载 | 删 N 个分散目录（agents/@x、skills/@x、teams/@x…） | `rm -rf .agents/_teams/x` 一条命令 |
| 能看到"这个包带了哪些文件" | 要跨多个目录拼 | 一个目录就能看全 |
| 本地目录 vs 包目录混在一起 | 混在一起，`ls agents/` 会看到 `@xxx` | 完全分开 |
| 引用写法 | `@review-team/security-reviewer` | `review-team/security-reviewer`（少一个符号，但**与 Agent 名可混淆**——需要加载器能区分） |
| 包内外引用改写 | 必须改写，否则撞名 | **可以不改写**（见下） |

#### 结论：选 B（已定）

理由集中在"卸载"和"加载器改动量"两点：

1. **卸载是一条 `rm -rf`**，不用记这个包往哪些目录撒过文件。A 方案要维护一份
   "包 → 目录列表"的映射，一旦索引损坏就卸不干净。
2. **加载器只需要多扫一个根**。`_teams` 内部结构与 `.agents/` 完全同构，
   现有的 `loadAgentDefinitions` / `loadDefinitionTeams` 可以**原样复用**，
   只是对每个 `_teams/<scope>/` 再调一次。A 方案要改五处目录遍历。

**B 方案下引用怎么写：**

包内引用**保持裸名**（`call.agent: security-reviewer`），加载时按作用域解析：

```text
解析 Call.AgentID = "security-reviewer"
  ├─ 若该 Team 来自 _teams/<scope>/，先在该 scope 内找
  ├─ 找不到 → 报错（不静默回退到本地同名 Agent，否则行为会变）
  └─ 报错：包必须完全自包含，不存在"回退到本地"的路径
```

这条"**不静默回退**"是整个方案里最容易做错也最重要的一条：
如果一个包里的 Team 要的 `security-reviewer` 和本地同名但行为完全不同，
静默用本地那份会让包"看起来装成功了但行为是错的"。宁可安装时或加载时报错。

**没有 `@local/`。** 包里的 Team 只能引用包内资产。想用收件方本地的 Agent，
正确做法是把那个 Agent 也打进包里，或者由收件方自己写一个 Team 去组合两边。

#### 索引文件

```json
// .agents/.teams.json
{
  "version": 1,
  "installed": {
    "review-team": {
      "version": "1.2.0",
      "source": "https://github.com/x/y#v1.2",
      "installed_at": "2026-10-20T10:00:00Z",
      "manifest_hash": "sha256:...",
      "files": { "agents/security-reviewer/AGENT.md": "sha256:..." }
    }
  }
}
```

用途：`teams` 展示来源与版本、`remove` 校验目录没被手工改过、重复安装判幂等。
`LoadDefinitions` **忽略它**（文件名不以 `.yml` 结尾，不会被扫到）。

### 5.5 冲突策略

选 B 之后，冲突面变得很小：

**包与包之间**：不同 scope 天然隔离（`_teams/a/` vs `_teams/b/`），互不影响。

**包与本地之间**：

| 情况 | 行为 |
|---|---|
| 包内 Agent 名与本地同名 | 允许。包内引用在 scope 内解析，不会撞到本地 |
| 包里需要调用**本地**的 Agent | 不支持，报错。包必须自包含 |
| 同一个包装两次（同 name + 同 version） | 幂等覆盖 |
| 同 name 不同 version | 报错，提示先 `heron teams remove <name>` |

**依赖版本冲突**（两个包都依赖 `code-fix` 但要求不相容）：直接失败并列出两个来源，
本期不做自动求解。

因为 B 方案不改写引用，"安装时改写包内引用"这一步（原 §5.3 的内容）**整个去掉了**，
安装变成纯粹的目录复制 + 校验。这也是选 B 的一个实际收益。

### 5.6 装完之后的 Flow 绑定

`teams add` **不自动写 Flow 绑定**。`Flow.Teams` 是使用方的编排决策
（哪些 team 是本 Flow 的入口、`CanActivate` 关系如何），装包不应替用户决定。

安装结束时打印一段可直接粘贴的片段：

```yaml
# 粘贴到 .agents/flows/default.yml
teams:
  review:
    team: "review-team/review_team"
    inputs:
      user_message: true
```

### 5.7 `teams export` 的产物

```bash
heron teams export review_team              # -> ./review_team.tar.gz
heron teams export review_team -o ./dist    # -> ./dist/review_team.tar.gz
```

产物结构就是 §3.2 的目录树，打包进一个 `.tar.gz`：

```text
review_team.tar.gz
└── review-team/                      # 顶层目录 = manifest.name
    ├── heron-team.yml
    ├── teams/review_team.yml
    ├── agents/...
    ├── skills/...
    ├── knowledge/...
    └── rules/...
```

打包时自动完成：

1. 从 `review_team` 出发做闭包遍历（§4），只收能到达的资产
2. 生成 `heron-team.yml`（`provides` 由遍历结果填，不手写）
3. **剥离密钥**：扫描 `api_key:` / `sk-` / `Bearer` / `ck_`，命中即失败；
   `ModelConfig.APIKey` 非空则删字段并提示
4. **跳过** `data/`、`models.json`、`flows/`、`settings.json`，以及**所有** `knowledge/` 目录
5. 无需处理 `knowledge:` —— 字段在 §9.2.2 已从 `AgentConfig` 删除
6. `command` / `webhook` call **原样导出**，不做限制

导出报告列出：移除的知识目录与条目数、被删掉的 APIKey 字段。

`--format dir` 产出目录，用于人工检查（核对 puts 与文件清单）。

## 6. 安全边界

### 6.1 工具上限（安装期裁剪）

包内每个 Agent 的 `Tools.Builtin` 与本包 `sandbox.tools` **取交集**。
包声明 `[Read, Grep, Glob]` 但 Agent 写了 `Bash` → 安装时裁掉 `Bash` 并警告。

在安装期做而不是运行期兜底，因为运行期兜底会让用户困惑
"它明明写了 Bash 却执行不了"。

### 6.2 脚本策略

`sandbox.scripts`：

| 值 | 行为 |
|---|---|
| `deny` | 拒绝安装带 `scripts/` 的包 |
| `review`（默认） | 安装前打印每个脚本内容，要求显式确认（`--yes` 跳过） |
| `allow` | 直接装 |

脚本内容 sha256 写进 `.agents/.teams.json` 索引，包升级后哈希变化会再次触发 review。

### 6.3 密钥

- **打包时扫描**：`AGENT.md` / `SKILL.md` / 知识文件里出现形如
  `sk-`、`api_key:`、`Bearer `、`ck_` 的片段 → 打包失败，指出文件与行号。
- **安装时扫描**：同一规则再跑一次，防止手工改包绕过。
- `AgentConfig.Model.APIKey` 非空 → 导出时**删掉该字段**并提示作者别在 Agent 里写密钥，
  不因一个误填拒绝整个包。

### 6.4 安装期不执行任何包内代码

安装只做文件读写与文本检查。脚本只在运行时（Skill 被调用时）执行，
执行前已经过 `sandbox.scripts` 的策略。

## 7. 与现有架构的接缝

| 现有组件 | 怎么接 |
|---|---|
| `LoadDefinitions`（`internal/config/definitions.go:26`） | 语义不变。**新增**：加载完 `.agents/` 后，遍历 `.agents/_teams/<scope>/`，对每个目录用同一套 loader 再跑一次，结果按 scope 归档 |
| `types.Definitions` | 不加字段。scope 只在解析 `call.agent` 时用于查找顺序 |
| `internal/definitions/writer.go` | `CreateTeamRequest` 已能"一次调用写 team + agent"，可复用；差异是不写 Flow 绑定 |
| `internal/definitions/render.go` | 资源收集器复用，需抽成可独立调用的函数 |
| `checkAgentSpecSupport`（`validate.go:135`） | 安装解析阶段用同一套规则提前报错 |
| `main.go` CLI 分发 | 参照 `runKnowledgeCLI`（`main.go:36-38`）：`if os.Args[1] == "teams" { runTeamsCLI(os.Args[2:]) }` |

**关键改动只有一处**：加载器多扫一个根目录。`_teams/<scope>/` 内部结构与 `.agents/` 完全同构，
所以 `loadAgentDefinitions` / `loadDefinitionTeams` / `loadSkillDefinitions` 都能原样复用。

需要新增的只有**引用解析的作用域规则**：

```text
resolve(ref, scope):
  scope != ""  → 只在 _teams/<scope>/ 里找；找不到即报错
  scope == ""  → 在本地根找（本地 Team 只能引用本地资产）
```

## 8. 关于模型：为什么不管

**`models.json` 是收件方的本地环境文件，不参与分享。**

- `teams export` 不打包它
- manifest 里没有模型相关字段
- `teams add` 不检查、不报告、不阻塞

理由：一个 Agent 写 `model: claude-sonnet-4.6`，这句话的语义是"我是按这个模型调的"，
是**行为定义的一部分**，和 `temperature`、`max_rounds` 一样属于包内容。
而"我本机有没有这个模型"是另一层事实，与包无关。

### 8.1 缺模型时会发生什么

现有运行时已经会报错：`LoadModelConfig` 找不到 id 时会给出明确提示。
`teams add` 结束时不拦截，理由有三：

1. 收件方可能**先装后配** —— 要求对方先有这个模型才能装是反直觉的
2. 包可能只是被"看看别人怎么编排的"，不打算立刻跑
3. 一旦开始"检查模型"，就得定义"什么算满足"（同名不同 endpoint？限额不同算不算？），
   这套判定没有正确答案，且与 §2.2「不做版本求解」的取舍矛盾

所以：**装完不报错，跑的时候报错，错误信息由已有的 model 加载路径负责。**

将来若需要"我装的这些 Team 里哪些缺模型"的视图，那是纯查询
（读已装 Team 的 Agent，提取 `model.model`，对照 `models.json`），
不需要 manifest 配合，可以后加。

## 9. 已定的边界与遗留项

### 9.1 已定（本轮拍板）

| 问题 | 决定 | 理由 |
|---|---|---|
| `command` / `webhook` call 能否导出 | **原样导出，不做限制** | 这是基础能力，引擎不判断业务用法 |
| **知识是否打包** | **一律不带，无任何开关** | `knowledge/` 可能含个人信息（人名、内部约定、真实数据）。分享的是编排与提示，不是数据 |
| **`knowledge:` 字段** | **从 `AgentConfig` 删除**（§9.2.2） | 运行时无人读取，是 agentic search 改造的遗留；保留会让人误以为是访问控制 |
| 包内能否引用收件方本地 Agent | **不提供 `@local/`** | 包必须完全自包含，行为才可预测 |

### 9.2 知识：一律不带，并顺带清理 `knowledge:` 字段

#### 9.2.1 目录不带

heron 的知识目录有三处，**全部不打包**：

```text
.agents/knowledge/              全局，所有 Agent 可读
.agents/agents/<id>/knowledge/  Agent 私有
.agents/teams/<team>/knowledge/ Team 私有
```

不给 `--with-knowledge` 之类开关，因为这不是"默认值选择"，而是**数据归属问题**：
知识是在某个具体 workspace 里、由具体的人和事产生的，作者本人也无法保证
里面没有别人的名字、内部链接、真实工单号。一旦可选，就一定会有人打开它。

#### 9.2.2 字段删除（与 §11 第一步一起做）

`AgentConfig.Knowledge []string`（`yaml:"knowledge"`）**删除**。

这不是为了给导出让路，而是它本身已经失效。理由链条：

1. 知识可见性现在的唯一判据是**文件在哪**（`internal/tool/path_scope.go` 的路径过滤器，
   以及 `internal/knowledge/store.go:686` 的 `locationScope`：agentic search 下
   路径过滤器读不到文件里的 YAML，所以 frontmatter 声明的 scope 只能退化为
   "必须与位置一致"，真正生效的是位置）。
2. 检索由模型自己用 Grep/Glob/Read 完成，`ReadKnowledge` 工具已删除
   （`internal/agent/runtime.go:2339` 注释）。
3. 于是 `agent.Knowledge` 在运行时**没有任何读取方**。全仓只有
   `internal/definitions/validate.go:155` 和 `writer.go:193,311` 在做存在性校验。
   官方文档自己也写着 `id` "**is not a lookup key at run time**"
   （`heron-ai/docs/configuration/knowledge.md:97`）。

它现在是一个**看起来像访问控制、实际只是拼写检查**的字段，比没有更糟：
作者写 `knowledge: [seo-guide]` 会以为自己限定了这个 Agent 的知识范围，
而模型照样能 grep 到该目录下的所有文件。

#### 9.2.3 删除的影响面

| 位置 | 改动 |
|---|---|
| `pkg/types/agent.go` | 删 `Knowledge []string` |
| `internal/definitions/validate.go:135-167` | `checkAgentSpecSupport` 去掉 knowledge 参数与循环；只保留 skills 校验 |
| `internal/definitions/validate.go:207` | `checkUnknownAgentSpecKeys` 的 known 集合**保留** `"knowledge"`，并加注释说明是兼容空转键（§9.2.4） |
| `internal/definitions/writer.go:193,311` | 调用处去掉 `knowledgeIDs(root)` 实参 |
| `internal/definitions/render.go:19-66` | 删 `knowledgeIDs`（唯一用途就是给上面那两处校验） |
| `internal/definitions/merge.go` | 注释里"skills/knowledge/rules are resolved by name"改为不含 knowledge |
| `examples/**/AGENT.md` | 14 个文件含 `knowledge:` 段，逐一删除 |
| `heron-ai/docs/configuration/agent.md` | 示例与字段说明去掉 `knowledge` |

**不动的部分**（这些是目录级机制，与新字段无关）：
`internal/knowledge/` 整体、`KnowledgePointer`、`bash_gate.go` 的 `hasPrivateKnowledge`、
`path_scope.go` 的路径过滤、`heron knowledge` CLI。

#### 9.2.4 直接删除，known 集合保留一个空转键

**类型字段删掉**（`AgentConfig.Knowledge`），示例与文档删掉，不留兼容 shim、不留 TODO。

但 `checkUnknownAgentSpecKeys`（`internal/definitions/validate.go:207`）的 known 集合里
**保留 `"knowledge"`**。

原因：这个守卫的职责是"拦住写了却没生效的字段"，而它报错的文案是
`unsupported field(s) ...`——用户看到会以为是自己写错了字段名，
实际原因是这个字段已经不承载任何语义。两种处理都会留下痕迹，但方向不同：

| 做法 | 老配置的表现 | 问题 |
|---|---|---|
| known 集合删掉 `"knowledge"` | 报 `unsupported field(s) knowledge` | 误导：字段名是对的，错的是"它还在"这个前提。用户会去查拼写 |
| **known 集合保留**（本方案） | 静默通过，字段无效果 | 留下一个无意义的名字 |

选后者。代价只是 known 集合里多一个空转键，而收益是：**升级不炸**。
有人机器上几十个 AGENT.md 都写着 `knowledge:`，不该因为升级一个次要功能就全量加载失败。

#### 9.2.5 为什么不干脆删掉这个键

因为它会变成一个"静默失效"的字段——正是 `checkUnknownAgentSpecKeys` 当初要解决的问题
（`validate.go:196` 注释：`temperature` 写在顶层而不是 `model` 下，文件看着配置了、
行为却像没配）。

这里接受这个矛盾，理由是两者性质不同：

- `temperature` 写错位置是**用户笔误**，静默通过会让用户长期困惑
- `knowledge:` 是**我们删掉的字段**，静默通过不会改变任何行为，
  用户也不会因为"它没生效"遇到问题——它从来就没生效过

把 known 集合里的 `"knowledge"` 加一行注释说明它是为兼容保留的空转键，
就足够让后来者不再追问。

#### 9.2.6 与导出的关系

字段删除后，导出规则简化为两条：

1. 三个 `knowledge/` 目录整体排除
2. Agent 定义里**不存在** `knowledge:` 字段，无需清空或改写

包里一个 Agent 只剩：`name` / `persona` / `model` / `tools` / `skills` / `rules` /
`loop` / `context` / `budget` / `completion` / `structured_output` / `hitl` / `hooks` /
`workspace` + 正文。**纯方法论文本，不含任何数据。**

### 9.3 实现时可自定的

1. **`.teams.json` 放哪、算不算定义树的一部分？**
   建议 `.agents/.teams.json`，加载器天然忽略（不扫非 `.yml`）。
2. **`_teams` 保留目录说明**
   在 `docs/CONFIGURATION.md` 写明它是保留目录、不要手写。
3. **`source` 字段**
   `heron teams` 的 SOURCE 列显示"来自 `https://github.com/x/y#v1.2`"。
   已定记录在 `.teams.json`，纯展示用途。
4. **同一 scope 多包**
   建议 scope 与 manifest.name 一一绑定；需要多版本共存时 `--as <scope>`。

### 9.4 明确不做

- 模型检查（§8）
- 版本求解（依赖冲突直接报错）
- 中心化市场服务
- 签名与来源验证（可后加 `manifest.signature`）

## 10. 验收标准（本期）

- [ ] `heron teams` 在无 `.agents/` 的目录也能列出（不依赖 Flow 配置）
- [ ] `heron teams` 输出能区分本地原生与 `@scope` 下载项，并显示来源
- [ ] 不存在 `heron teams list` 子命令（输入它应报未知子命令）
- [ ] `heron teams export review_team` 默认产出 `./review_team.tar.gz`；
      `-o /tmp/x` 产出 `/tmp/x/review_team.tar.gz`
- [ ] 干净目录 `heron teams add ./review_team.tar.gz` 后目录落在 `.agents/_teams/review-team/`，
      且能直接 `heron --flow ...` 跑通
- [ ] `heron teams add https://github.com/<o>/<r>` 与 `...#v1.2` 均能装成功（缓存命中不重复下载）
- [ ] 包内缺少 `requires` 声明的引用 → 解析阶段失败，错误点名"哪个文件引用了什么"
- [ ] 同包装两次幂等；不同版本同名安装报错并提示 `heron teams remove`
- [ ] 本地已有 `security-reviewer` 时装包不覆盖、不报错，两套共存；
      包内 Team 引用的是**包内**那份（不静默回退到本地）
- [ ] 包内引用包外资产（含曾被考虑过的 `@local/`）→ 解析阶段报错，不静默回退
- [ ] `heron teams remove review-team` 后 `.agents/_teams/review-team/` 消失，
      `.teams.json` 同步移除，本地其它目录零变化
- [ ] 包声明 `sandbox.tools: [Read, Grep]` 而 Agent 写了 `Bash` → 安装后无 Bash + 警告
- [ ] 含 `api_key:` / `sk-` 的包 → 导出与安装都被拒，指出文件与行号
- [ ] 含 `scripts/` 且策略为 `review` → 安装时打印脚本内容并要求确认
- [ ] `models.json` 不出现在产物里；包里 Agent 的 `model.model` 原样保留
- [ ] 产物里**没有任何 `knowledge/` 目录**（全局、Agent 私有、Team 私有全部排除）
- [ ] 产物里没有任何 Agent 带 `knowledge:` 字段（该字段已不存在）
- [ ] 没有 `--with-knowledge` 之类开关，尝试传未知 flag 报错
- [ ] `teams add` 不检查模型，本地缺模型时安装成功
- [ ] 压缩包含符号链接 / 超大文件 → 安装被拒
- [ ] `--dry-run` 不产生任何文件写入

## 11. 实施顺序

1. **删除 `AgentConfig.Knowledge`**（§9.2.2）：类型字段、`checkAgentSpecSupport`、
   `checkUnknownAgentSpecKeys`、`writer.go` 调用处、`render.go` 的 `knowledgeIDs`、
   14 个示例 AGENT.md、`docs/configuration/agent.md`。
   直接删干净、不留兼容（§9.2.4）。这一步独立于分享功能，可单独提交与发版
2. **Manifest 与解析器**：`heron-team.yml` 结构 + 闭包遍历 + 检查项（纯逻辑，不写盘）
3. **`heron teams` 列表 + `teams add --dry-run`**：接上 CLI，
   此时已能给人看闭包与冲突，零写入风险
4. **`teams export`**：闭包收集 + 剥离密钥 + 产出 `.tar.gz`
5. **`teams add` 本地安装**：`_teams/<scope>/` 布局 + 冲突检测 + 工具裁剪 + 脚本 review
6. **`teams remove` + 列表输出** + `.teams.json` 索引
7. **加载器适配**：多扫 `_teams/` 根 + 按 scope 解析引用（无 `@local` 回退）
8. **远程来源**：GitHub tarball / 直链 + 内容寻址缓存 + 安全检查
9. **Flow 绑定建议输出**

## 12. 实现状态

以下六项均已落地并通过全量测试（`go test ./...`）。

| 模块 | 位置 | 说明 |
|---|---|---|
| manifest / 闭包 / 检查 | `internal/teamspkg/` | `Manifest`、`Tree`、`Collect`、`Closure`、`Check`；`ValidName` 供 install 复用 |
| CLI | `cmd/server/teams_cli.go` | 四种用法，`teams list` 明确报未知子命令 |
| 列表 | `teams_list.go` | SOURCE 取索引 → manifest → `-`；`partial` 状态 |
| 导出 | `teams_export.go` | tar.gz 默认、`--format dir`、密钥扫描、knowledge 全排除 |
| 安装 | `teams_install.go` `teams_add.go` | 目录与 tar.gz；先写临时目录再 rename；工具裁剪、脚本 review |
| 卸载 | `teams_remove.go` | 默认整包删除，`--keep-agents` 保留 agents/skills/rules |
| 远程 | `teams_fetch.go` | GitHub codeload / 直链，内容寻址缓存，仅 https |
| 索引 | `teams_index.go` | `.agents/.teams.json`，含 manifest_hash 与 scripts 哈希 |
| 加载器多根 | `internal/config/scopes.go` | scope 资产一律 `<scope>/<name>`，本地保持裸名 |

### 实现中发现并修正的一点

**命名空间方向与 §5.4 原设想相反，但更安全。** 原方案是"本地同名时包用 `@scope/`"，
实现时发现那样会让包内 Agent 的 id 随本地环境变化，且 AgentID 改名会连带改写 Team 的
`call.agent`，一旦漏改就会静默解析到本地同名 Agent。

实际采用：**包内一律 `<scope>/<name>`，本地永远裸名**。这样本地定义永不被覆盖，
包内引用只在自己 scope 内解析，两端都是硬保证。代价是 Flow 绑定必须写
`review-team/review_team` 全名（安装摘要打印的正是这个形式）。

`--keep-agents` 会留下"无 manifest 的 scope 目录"，加载器照常发布其中的
agents/skills/rules，不报错 —— 否则卸载后整个配置会起不来。

## 13. 参考

- `internal/config/definitions.go:26-92` — `LoadDefinitions` 全部加载路径
- `internal/config/definitions.go:225-250` — Agent 目录遍历（需扩展 `@scope`）
- `internal/definitions/validate.go:135-167` — `checkAgentSpecSupport` 现有引用校验
- `internal/definitions/render.go:53-66` — 知识资源收集器
- `internal/definitions/writer.go` — `CreateTeamRequest` 一次写 team + agent
- `pkg/types/agent.go` — `AgentConfig`（含 `ModelConfig.APIKey`）
- `pkg/types/skill.go` — `Skill`（含 `Scripts`）
- `pkg/types/domain.go:72-81` — `Team` 结构
- `pkg/types/call.go:124-135` — `Call` 结构
- `cmd/server/main.go` — CLI 子命令分发（`runKnowledgeCLI` 是写法参考）
- `npm/bin/heron` — Node 入口，`spawnSync` 转发 Go 二进制

## 14. 与任务实例的关系（2026-11-03 补）

引擎在同一天合入了 Team 的任务实例模型：Flow 绑定可以声明 `lifecycle: per_task`，同一个 Team 定义按任务开多个实例，对话隔离、状态共享，派发需要 `task_key`。分享包原本只管"把团队搬过去"，不管"搬过去以后按什么形态跑"，这一节补上两者的接缝。

### 14.1 包如何声明期望的运行形态

`heron-team.yml` 增加可选段：

```yaml
runtime:
  lifecycle: per_task   # 缺省 resident
```

它是**提示，不是运行环境**：和 sandbox 一样，接收方仍然决定这个 team 摆在 Flow 的哪个位置。但少了它，作者"这个团队应该一任务一实例"的意图无法随包传递，接收方按摘要接线就会得到 resident——跨任务共享上下文，而且不报错。这是比报错更糟的失败方式。

`lifecycle` 取值只接受 `resident` / `per_task`，其他值在 `LoadManifest` 阶段报错，不做静默降级（与 Flow 绑定的校验一致）。

### 14.2 安装摘要会说什么

`per_task` 包的摘要多两段：

```
      team: "review-team/review_team"
      lifecycle: per_task
      inputs:
        user_message: true

This team runs one instance per task: dispatch it with a task_key, and
declare TeamSession in the caller if it needs to query or close tasks.
```

`resident` 包保持原样，不出现 `lifecycle` 行，避免让常见情况变吵。

### 14.3 接收方的接线清单

1. 粘贴摘要里的 Flow 片段（`lifecycle` 行只在 `per_task` 时出现）；
2. 确认 `inputs.user_message: true` 保留了——`per_task` 没有它会在加载期直接报错（任务说明书送不进去）；
3. 在**调用方** Agent 的 `tools.builtin` 里加 `TeamSession`，并在其提示词里约定 `task_key` 的取法（如 `<repo>-<issue>`）；只写提示词不声明工具，模型看不到该工具，指令等于死指令；
4. 跑一次 `heron --check-config`，它打印每个绑定的 lifecycle，并在 `per_task` 缺 `user_message` 时启动前就失败。

### 14.4 依赖声明：requires.teams

一个 Team 的 Agent 通过 `Team` 工具派活给别的 Team，这是运行时的名字解析，静态闭包看不到。因此包可以声明：

```yaml
requires:
  teams: [lint-team]
```

校验规则两条：名字必须符合 team id 语法；**同一个 team 不能既 provides 又 requires**（自己带的东西不叫依赖，声明成依赖会让接收方去找第二份）。安装摘要会打印"需要另外安装的 team"。

### 14.5 sandbox 与 TeamSession

`sandbox.tools` 是包内所有 Agent 的 builtin 白名单。如果包里的 Agent 需要查询/关闭自己派发的任务，白名单必须包含 `TeamSession`——否则安装时被裁剪，能力消失。裁剪会出现在摘要的 `trimmed tools` 行里，安装不阻断，所以作者应在打包前自查。

### 14.6 关闭与复活

`TeamSession(action="close", task_key=...)` 是软关闭：只置状态，transcript 与记录保留。用同一个 `task_key` 再派一次会复活原实例并回报 `reopen_count`。分享包不负责这条链路的配置，接收方按 §14.3 接线即可。
