# heron-ai 目录组织与设计优势

## 目录组织

所有配置落在 `.agents/` 目录，按类型分目录：

```
.agents/
├── <flow>.yml              # Flow 定义（或 flows/<name>.yml）
├── teams/<name>.yml        # Team 定义
├── agents/<id>/AGENT.md    # Agent 定义（frontmatter + 正文提示词）
│   ├── knowledge/          # 该 agent 私有知识
│   └── rules/              # 该 agent 私有规则
├── skills/<id>/SKILL.md    # Skill（提示词 + scripts/ + references/）
├── rules/<name>.md         # 全局规则
├── knowledge/<name>.md     # 全局知识
├── models.json             # 模型注册表（含 api_key，勿提交）
└── settings.json           # 引擎全局设置
```

## 编排层次

```
Flow（调度）→ Team（组织）→ Call（执行项）→ Agent / Command / Webhook
```

- **Flow**：把多个 Team 绑成一个执行图，标记入口和协调者。
- **Team**：组织自己的 Call（可混排 agent / command / webhook）。
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
├── dev_flow.yml            # Flow：entry=dev，coordinator=true
├── teams/dev_team.yml      # Team：单 call develop → developer
├── agents/developer/AGENT.md  # Agent：persona + model + tools + 正文
├── rules/coding-boundary.md   # 硬规则：只改 project/
├── models.json             # 模型注册表
└── settings.json           # 运行时限制
```

这是 `examples/dev-agent/` 的结构，可作为最小完整配置的参照。
