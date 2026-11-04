# heron-ai 内置 Skill

引擎内置的、可复用的 Skill。用户通过**软连接**在自己的项目里复用，无需拷贝维护两份。

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

## 目录

```
builtin/skills/
└── heron-ai-config/          # 配置编写 + 运维 + 运行排查
    ├── SKILL.md              # 入口：先自检 → 再排查 → 按需读参考
    ├── references/
    │   ├── debug-triage.md   # 排查入口：判定标准、异常模式、从配置预测行为
    │   ├── session-files.md  # 数据落点、三层 jsonl、执行日志
    │   ├── token-usage.md    # 按 team / agent / 轮次统计 token
    │   ├── trace-ids.md      # 事件 ID 串联地图
    │   ├── fields.md         # 每个配置字段的含义 + ${records.*} 模板
    │   ├── organization.md   # 目录组织、三层编排、设计优势
    │   ├── operations.md     # 安装/启动/关闭/校验的完整命令
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
