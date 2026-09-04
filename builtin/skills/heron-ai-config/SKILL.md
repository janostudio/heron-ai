---
name: heron-ai-config
description: "编写与运维 heron-ai 配置（Agent/Team/Flow/models/settings/rules/knowledge/skill），含字段速查与启停脚本"
tools:
  - Read
  - Write
  - Edit
  - Glob
  - Grep
  - Bash
scripts:
  - scripts/build.sh
  - scripts/serve.sh
---

# heron-ai 配置与运维

你在为一个基于 heron-ai 多 Agent 引擎的项目编写或修改配置，或执行
下载/启动/关闭等运维操作。

## 参考资料（先按需读取）

| 参考文件 | 内容 |
|---|---|
| `references/fields.md` | **每个配置字段的含义**（Flow/Team/Call/Agent/record/rules） |
| `references/organization.md` | 目录组织、三层编排、设计优势 |
| `references/operations.md` | 下载/使用/启动/关闭的详细命令 |
| `references/debug.md` | **排查与诊断**：三层 jsonl / 执行日志 / 证据链的读取与 jq 查询 |

读取方式：用 `Read` 打开 `references/<name>.md`（本 skill 目录下）。

## 核心要点

1. 三层编排：`Flow → Team → Call → Agent/Command/Webhook`，`Call` 是执行项不是层级。
2. 配置全部在 `.agents/` 目录下，按类型分目录。
3. `model.model` 直接写模型 id（来自 models.json），**勿用 `${ENV}` 占位符**（引擎不展开环境变量）。
4. record 靠 `output.record` + 下游 `inputs` 的 `{from, record}` 按名精确匹配，
   名称是自由字符串、无校验，拼写必须一致。
5. `models.json` 含 api_key，永不提交。

## 脚本

| 脚本 | 用法 |
|---|---|
| `scripts/build.sh` | `bash build.sh [输出路径]` 从源码构建二进制 |
| `scripts/serve.sh` | `bash serve.sh {start\|stop\|status}` 后台启停服务（PID 管理） |

## 修改配置的原则

1. 修改前先 `Read` 目标文件，保留已有字段与结构。
2. 只改必要的最小差异，不重构无关配置。
3. 新增 agent/team 时，确保 model、tools、call 引用的 agent 名、record 名前后一致。
4. 修改后运行引擎加载确认无 validate 错误。
