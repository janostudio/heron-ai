# heron-ai 内置 Skill

引擎内置的、可复用的 Skill。用户通过**软连接**在自己的项目里复用，无需拷贝维护两份。

> **前提**：这个目录随源码仓库分发，npm 包只装二进制、不含 `builtin/`。所以软连接
> 方式只适用于从源码使用（clone 或 `go build`）的场景；npm 安装的用户想用这份 skill，
> 需要另行取得该目录（例如 clone 仓库后指过去）。

## 使用方式（软连接）

在你的项目 `.agents/skills/` 下软连接到本目录的某个 skill：

```bash
# 在项目根目录执行
mkdir -p .agents/skills
ln -s <heron-ai 仓库路径>/builtin/skills/heron-ai-config .agents/skills/heron-ai-config
```

然后在 Agent 的 `AGENT.md` frontmatter 里引用：

```yaml
skills:
  - heron-ai-config
```

声明后立刻生效的检验方式：

```bash
heron --check-config --flow .agents/flows/default.yml   # 会校验 skill 的 scripts 路径存在
heron --prompt "这个项目的 flow 入口是哪个 team？"       # 一轮真实对话，看它是否按 skill 回答
```

> **索引常驻，正文按需读**：Agent 声明了 skill 之后，system prompt 里只多一行索引
> （名字 + `description` + `SKILL.md` 的路径），正文**不进** prompt；模型判断任务匹配时
> 才用 `Read` 打开那个路径。所以正文改了**立即生效**，不需要重启；代价是模型多一次读
> 文件调用。但 skill 的 `tools` 是**声明即授予**——工具定义会常驻上下文，所以只给真正
> 需要的 agent 声明。

## 目录

```
builtin/skills/
└── heron-ai-config/          # 配置编写 + 运维 + 运行排查
    ├── SKILL.md              # 入口：默认优先 → 三档字段 → 症状索引 → 自检
    ├── references/
    │   ├── minimal.md        # 三份最小可跑模板（单 agent / 两跳 / command 链）
    │   ├── fields.md         # 配置字段字典：类型、默认值、必写/别写/禁写
    │   ├── tuning.md         # 旋钮手册：哪个症状才该改哪个键、代价与验证
    │   ├── diagnose.md       # 判定：正常长什么样、异常看哪、配置 vs 运行 vs 环境
    │   ├── observe.md        # 运行时：数据落点、三层事件、payload、token、ID 串联
    │   ├── operations.md     # 安装/启动/CLI/HTTP 入口/校验
    │   └── define.md         # Define 工具：让 Agent 自建 Agent/Team
    └── scripts/
        ├── build.sh          # 从源码构建二进制
        └── serve.sh          # 后台启停服务（PID 管理）
```

`SKILL.md` 保持精简，只放高频路径；细节一律下沉到 `references/`，由模型按需读取
（渐进式披露）。新增参考文件时，记得同时更新 `SKILL.md` 的参考表和本图，否则
文件写了也没人知道该读它。

## 约定

- Skill 目录结构遵循 `docs/configuration/skill.md`：`SKILL.md` + `scripts/` + `references/`。
- `scripts` 在 SKILL.md frontmatter 里声明，路径相对 Skill 目录，由 `validateSkillScripts` 校验。
- 软连接是用户手动建立的本地行为，**不固化在任何 example 里**（绝对路径因机器而异）。
- 引擎加载器（`ConfigLoader.loadSkillDefinitions`）通过 `os.ReadDir`/`os.Stat`/`os.ReadFile`
  遍历与读取，均跟随符号链接，故软连接的 skill（含 references 和 scripts）可被正确加载。
