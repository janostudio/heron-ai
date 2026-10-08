# Define 工具：Agent 自建 Agent / Team 定义

## 它是什么

`Define` 是内置工具，让一个正在运行的 Agent **在对话中**创建或修改
Agent / Team 定义。调用一次 = 写文件 + 校验 + 发布到运行中的引擎。

没有 `Define` 时，扩展编排只能靠人改 `.agents/` 再重启；有了它，Agent
可以在解决一个具体问题的过程中，把「我需要的那个角色/那条流水线」写成
定义，下一轮就用上。

## 前置：必须声明才可见

`Define` **不是默认工具**，Agent 必须在 `AGENT.md` 里显式声明：

```yaml
tools:
  builtin:
    - Read
    - Define
```

三条链路缺一不可，任一条缺失的表现都是「模型从不知道有这个工具」：

1. 引擎启动时把工具注册进工具注册表（内置，恒真）；
2. 引擎内置的 schema 表里有 `Define`（内置，恒真）；
3. **`AGENT.md` 的 `tools.builtin` 里有 `Define`**（配置方负责）。

第 3 条同时决定「模型是否被提供该工具」和「调用是否被允许执行」：
未声明的调用会被运行时的允许清单拦掉，报
`tool is not allowed for this Agent`。

## 参数

| 参数 | 必填 | 说明 |
|---|---|---|
| `action` | 是 | `create_agent` 或 `create_team` |
| `spec` | 是 | 定义内容，形状见下 |
| `mode` | 否 | `create`（默认）或 `upsert` |
| `template` | 否 | 内置模板名：`agent.generic` / `team.sequential` / `team.fanout` |

### `action=create_agent` 的 spec

```yaml
name: reviewer                     # 必填（也可写 id）
persona:
  role: "代码审查员"
  goal: "找出真实缺陷并给出可执行修复建议"
  backstory: "..."
model:
  model: hy3-ioa                   # 省略则继承 models.json
tools:
  builtin: [Read, Grep, Glob]
skills: [code-review]
knowledge: [review-guide]
rules: [privacy]
loop: { max_rounds: 14 }
hitl: { enabled: true }
body: |                            # 正文 = System Prompt
  你是代码审查员……
```

**`create_agent` 只写 Agent 定义，不做别的事**——不加入任何 Team、
不绑定进 Flow。这样产生的 Agent 是**合法但永不被调度**的。
必须再用 `create_team` 在 `spec.calls` 里引用它（并给 `spec.bind`），
它才会进编排图。

### `action=create_team` 的 spec

```yaml
id: qa_team                        # 必填（也可写 name）
goal: "回答用户问题并给出依据"
state: { enabled: true, max_items: 20 }
calls:
  answer:
    type: agent
    agent: assistant               # 树里没有会自动创建
    responsibility: "理解诉求并答复"
    inputs: { user_message: true }
    output: { record: Answer }
agent_specs:                       # 与 team 一并创建的 agent 定义
  assistant: { persona: { role: "助手" }, body: "你是一个简洁的助手。" }
bind:                              # 绑定进当前 flow（省略则不绑定）
  key: qa
  coordinator: true
```

`create_team` 一次调用可能落三类文件：Team 定义、`calls` 里引用但树里
不存在的 Agent 定义、当前 Flow 里的绑定。**合并成一次调用是刻意的**：
一个引用了不存在 Agent 的 Team 是加载不通过的树，拆成多次调用会让树在
中间态坏掉。

`spec.bind` 决定新 Team 是否可路由：

| `bind` 字段 | 含义 |
|---|---|
| `key` | 在 Flow 里的局部名（必填，不能与已有绑定重复） |
| `coordinator` | 标记为协调 Team（一个 Flow 只能有一个） |
| `can_activate` | 本绑定可激活的其他 Team |
| `depends_on` | 前置 Team |
| `inputs` | 输入声明 |
| `on_proceed` | 结束后的固定路由 |

## create 与 upsert

| mode | 目标已存在时 |
|---|---|
| `create`（默认） | 报错，提示改用 `upsert` |
| `upsert` | 把 spec **合并**进已有定义，未提到的字段保留 |

upsert 的合并规则（容易误判，务必记住）：

- **标量与嵌套对象按 key 深合并**：只写 `persona.role` 不会清掉
  `persona.goal`。
- **列表整体替换**：`tools.builtin`、`skills`、`knowledge`、`rules`
  都是整体替换，不是追加。想保留原值必须在 spec 里写全。
- **正文（`body`）整体替换**。
- **未提到的字段保留**。

没有 `patch` 模式：upsert 本身就已经是「只改给定的字段」。

## 生效时机：下一轮

定义写入后**立即**校验并发布，但**下一轮才生效**：

- 当前这一轮里已经排好的调用**不会**改指向新定义；
- 下一轮开始时，引擎从已发布的定义里重新解析，新 Agent/Team 才可见；
- 新 Team 的绑定 key 从下一轮起可被路由引用。

这个延迟是安全性质而非实现限制：它保证一次定义变更不会在读取该定义的
那一轮中途改写它。

## 失败与回滚

每次调用走一条固定的流水线：读原文件 → 在影子树里应用变更 → 用真实
配置加载器校验整棵候选树 → 提交 → 发布。因此：

- spec 不合法（字段名错、引用了不存在的 skill、绑定了重复的 key）时
  **什么都不写**，错误直接回给模型，模型可以改 spec 重试；
- 提交阶段失败会按逆序回滚已落盘的文件；
- 提交成功后重载失败不会回滚——文件是用户/模型要的，错误信息会指明
  文件路径并提示 reload 或重启。

## 串行执行

`Define` 声明的执行类别是 `serial`：并发调用会交错同一个 Flow 文件的
读-改-写，第二个调用会基于第一个已作废的快照做合并。

## 排查

| 现象 | 原因 |
|---|---|
| 模型从不调用 `Define` | `AGENT.md` 的 `tools.builtin` 里没写 `Define` |
| 调用返回 `tool is not allowed for this Agent` | 同上（允许清单拦截） |
| 调用返回 `define tool is only available inside an Agent execution context` | 不在 Agent 轮次内调用 |
| 建了 Agent 但没人调用它 | `create_agent` 不负责调度，需 `create_team` 引用 |
| upsert 后 `tools.builtin` 变少了 | 列表是整体替换，spec 里要写全 |
| 改完当轮看不到效果 | 正常，下一轮才生效 |

验证某次调用是否真的落地，读会话日志里的 `tool_call.completed` 事件
（`payload.tool_name == "Define"`），并检查 `payload.effective == "next_turn"`。
