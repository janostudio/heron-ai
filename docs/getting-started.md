# 快速开始

从零到一个能对话的 Agent，只需要四个文件。本文每一步都给可复制的完整内容，
写完后就能跑通，不需要再额外查配置。

## 1. 安装

```bash
# 通过 npm（推荐）
npm install -g @qinghuangniao/heron-ai
heron --version

# 或从源码构建
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
```

`heron --version` 会打印 `Heron AI v<版本> (<os>/<arch>)`。源码构建不带版本号时
显示 `dev` 或 `-local`，这是正常的。

## 2. 准备工作区

Heron 的全部配置都在**工作区根目录**的 `.agents/` 下。工作区根就是启动
`heron` 时所在的目录，引擎按当前目录解析配置，不做向上查找。

```bash
mkdir -p my-agent/.agents/{flows,teams,agents/assistant}
cd my-agent
```

目录里每个位置都有固定含义，多一层少一层都会加载不到：

| 路径 | 内容 |
|---|---|
| `.agents/models.json` | 模型注册表：id、协议、endpoint、key |
| `.agents/settings.json` | 引擎行为配置（可选，不写就用默认值） |
| `.agents/flows/default.yml` | Flow 定义，`--flow` 省略时读这个 |
| `.agents/teams/*.yml` | Team 定义 |
| `.agents/agents/<id>/AGENT.md` | Agent 定义；标识取 frontmatter 的 `name`，目录名只用于定位文件 |
| `.agents/skills/`、`knowledge/`、`rules/` | 按需添加，用于 skill / 知识 / 规则 |

## 3. 配置模型

`.agents/models.json`：

```json
{
  "model": "gpt-4o-mini",
  "models": [
    {
      "id": "gpt-4o-mini",
      "name": "gpt-4o-mini",
      "protocol": "openai_chat",
      "base_url": "https://api.openai.com/v1",
      "api_key": "${OPENAI_API_KEY}"
    }
  ]
}
```

两点容易踩：

- **`model` 要和某个条目的 `name` 对齐**。Agent 里的 `model: xxx` 也按 `name` 找；
  `name` 缺省时取 `id`。
- **`api_key` 可以写 `${ENV_VAR}`**，引擎会展开；也可以整段省略，此时按协议读
  `OPENAI_API_KEY`（openai）或 `ANTHROPIC_API_KEY`（anthropic）。
  `${VAR}` 展开失败**不会**回退到别的变量，而是直接报 key 未设置 —— 这是刻意的，
  避免把 A 厂商的 key 发给 B 厂商。

其他模型字段（`maxInputTokens`、`temperature`、多模型 fallback 等）见
[configuration/models.md](./configuration/models.md)。

## 4. 写最小三件套

Flow 决定"谁先跑"，Team 决定"跑哪些 call"，Agent 决定"这一个角色是什么"。

`.agents/flows/default.yml`：

```yaml
id: default
entry: default

teams:
  default:
    team: hello_team
    inputs:
      user_message: true
```

`.agents/teams/hello_team.yml`：

```yaml
id: hello_team
goal: 回答用户的问题

calls:
  answer:
    type: agent
    agent: assistant
    responsibility: 回答用户的问题
    inputs:
      user_message: true
    output:
      record: Answer

output:
  from: answer
  record: Answer
```

`.agents/agents/assistant/AGENT.md`：

```markdown
---
name: assistant
persona:
  role: "助手"
  goal: "准确、简洁地回答用户问题"
model:
  model: gpt-4o-mini
tools:
  builtin:
    - Read
    - Grep
---

你是一个简洁可靠的助手。不知道就说不知道，不要编造。
```

`AGENT.md` 由 frontmatter 和正文两部分组成：frontmatter 是配置，正文是这个 Agent
的常驻系统提示。`model.model` 可以整段省略，此时用 `models.json` 的默认模型。

`agent: assistant` 这个名字来自 `AGENT.md` 的 `name`，不是目录名 —— 但两者保持一致
是最省事的选择。

## 5. 先验证配置，再跑

```bash
heron --check-config
```

它加载 flow / team / agent 定义，构建模型客户端以外的一切，然后打印每个 Team 绑定的
生命周期：

```
default      team=hello_team   lifecycle=resident
flow default: 1 bindings ok (0 per_task)
```

退出码 0 表示配置合法；非 0 会把 loader 的错误打到 stderr，例如 agent 名对不上、
record 引用未声明、`per_task` 绑定缺 `inputs.user_message`。**每次改完配置先跑这一步**，
比跑一整轮对话再猜原因快得多。

## 6. 跑起来

```bash
# 交互模式（TUI）
heron

# 非交互：跑一轮就退出，适合脚本和 CI
heron --prompt "用一句话介绍你自己"
```

TUI 里 `/help` 看命令，`/usage` 看**累计** token 消耗（续聊会话会把历史轮次一起累加，
不是单轮值），`Ctrl+C` 退出。
非交互模式会把答复打到 stdout。

## 7. 结果落在哪

每一轮都会在 `.agents/data/sessions/<flow_session_id>/` 下追加记录：

```bash
ls -1dt .agents/data/sessions/fs_* | head -3

S=$(ls -1dt .agents/data/sessions/fs_* | head -1)

# 用户每一轮问了什么
jq -r 'select(.type=="session.turn.started") | .payload.turn.input' $S/flow.jsonl

# 模型每一轮原始回复
jq -r 'select(.type=="session.agent.model_response") | .payload.text' $S/agent.jsonl

# 本轮 token 消耗（team.jsonl 是消耗的事实源）
jq -s '[.[]|select(.type=="session.agent_turn.completed")|.payload.call_result.usage.total_tokens]|add' $S/team.jsonl
```

三份 jsonl 按**产生它的层级**分流，不是同级混放：`flow.jsonl` 是 flow + team turn，
`team.jsonl` 是 call 层（agent/command 调用与消耗），`agent.jsonl` 是 agent 内部的模型
回复与工具调用。找不到某类事件时先确认文件选对了。逐层排查手法见
[排查入口](../builtin/skills/heron-ai-config/SKILL.md)。

## 8. 续聊与换会话

```bash
# 列出历史会话，选中后带着上下文继续
heron --list

# 直接续指定会话
heron --prompt "继续，把刚才的结论整理成三条" --session fs_xxxx
```

一个 FlowSession 是永久可续的：正常结束、失败、等待输入都只是本轮的状态，不会让会话
变成死会话。

## 常见错误

| 现象 | 原因 | 处理 |
|---|---|---|
| `a new-format Flow config is required` | 当前目录没有 `.agents/flows/default.yml`，也没传 `--flow` | 确认在**工作区根**执行；或用 `--flow` 指相对路径 |
| `load .agents/models.json: no such file` | 缺少模型注册表 | 按第 3 步创建 |
| `model "xxx" not found in models.json` | `model` / Agent 的 `model.model` 与条目的 `name` 不一致 | 对齐 `name` |
| `API key for default model "xxx" is not set` | `${VAR}` 指向的环境变量为空，且没有其他 key 来源 | `export` 该变量，或直接写 key |
| `config invalid: ...` | `--check-config` 报的定义错误 | 按报错里的 agent / team / record 名逐项对齐 |
| 想看执行日志但终端里没有 | 日志只写文件，不进终端 | 打开 `.agents/data/logs/YYYY-MM-DD.log`；需要更详细就设 `logging.level: debug` |
