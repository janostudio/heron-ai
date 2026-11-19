# heron-ai 目录组织与设计优势

## 目录组织

所有配置落在 `.agents/` 目录，按类型分目录：

```
.agents/
├── flows/<name>.yml        # Flow 定义（默认取 flows/default.yml）
├── teams/<name>.yml        # Team 定义
│   └── <name>/knowledge/   # 该 team 私有知识（可选）
├── agents/<id>/AGENT.md    # Agent 定义（frontmatter + 正文提示词）
│   ├── knowledge/          # 该 agent 私有知识
│   └── rules/              # 该 agent 私有规则
├── skills/<id>/SKILL.md    # Skill（提示词 + scripts/ + references/）
├── rules/<name>.md         # 全局规则
├── knowledge/<name>.md     # 全局知识
├── _teams/<scope>/         # 安装的 Team 分享包（teams/ agents/ skills/ rules/ + heron-team.yml）
├── data/                   # 运行时产物（sessions/ logs/ …，勿提交）
├── models.json             # 模型注册表（含 api_key，勿提交）
└── settings.json           # 引擎全局设置
```

`.agents/` 的**上一级就是工作区根**（heron 进程的 cwd）。文件工具的读写默认都限制在
这个根里；要读根之外的目录（例如同级放着的代码仓库），在 `workspace.read_roots` 里
列出来，读取放行、写入仍拒绝。详见 `fields.md` 的 workspace 节。

## 编排层次

```
Flow（调度）→ Team（组织）→ Call（执行项）→ Agent / Command / Webhook
```

- **Flow**：把多个 Team 绑成一个执行图，用 `entry` 指定从哪个 Team 进。
- **Team**：组织自己的 Call（可混排 agent / command / webhook）。Team 之间**平权**：
  除了谁是入口，没有角色差异；一个 Team 能激活谁由绑定的 `can_activate` 决定，
  结果回到激活它的那个 Team。
- **Call**：一次执行定义，不是额外的协作层级。
- **Agent**：模型 + 提示词 + 工具的执行体。

## 设计优势

1. **关注点分离**：调度、组织、执行三层各管各的，改一个 Agent 不影响调度图。

2. **Call 抽象统一执行项**：LLM Agent、Shell 命令、Webhook 在同一层级混排，
   让「确定性脚本」和「LLM 推理」能在同一个 Team 里协作。

3. **显式数据流，无隐式全局状态**：record 必须显式 `inputs` 声明才能读，
   杜绝 agent 读到不该读的历史，边界清晰、可审计。

4. **依赖驱动并行**：`depends_on` 声明依赖，无依赖的 call 自动并行，
   声明式编排而非命令式。

5. **配置即复用**：skill 是「提示词 + 脚本 + 参考资料」的完整目录包，
   可整体软连接或复制到另一个 Flow 复用。

6. **模型配置继承**：agent 未显式配置的模型参数，从 `models.json` 继承，
   避免重复配置。

## 一个最小单 Agent 开发的完整例子

```
.agents/
├── flows/dev_flow.yml         # Flow：entry=dev
├── teams/dev_team.yml         # Team：单 call develop → developer
├── agents/developer/AGENT.md  # Agent：persona + model + tools + 正文
├── rules/coding-boundary.md   # 硬规则：只改 project/
├── models.json                # 模型注册表
└── settings.json              # 运行时限制
```

仓库里可对照的最小完整配置是 `examples/simple-qa/`（单 Team 问答）与
`examples/code-review/`（多 call + 结构化输出）；`examples/blog-writer/` 额外演示
多个 skill 与 references 分层。
