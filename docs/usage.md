# 05 使用文档：TUI + HTTP API

> 目录：`docs/generic-engine/`
> 定位：**终端用户使用文档**。描述 TUI 和 HTTP API 的使用方式、参数、示例。
> 前置阅读：[00-config-design.md](./00-config-design.md)

---

## 一、快速开始

### 安装

```bash
go install github.com/heron-ai/heron-engine/cmd/server@latest
```

### 最小配置

创建 `.env` 文件，设置 API Key：

```bash
OPENAI_API_KEY=sk-your-api-key
LLM_MODEL=gpt-4o-mini
OPENAI_BASE_URL=https://api.openai.com/v1  # DeepSeek: https://api.deepseek.com/v1
```

### 启动

```bash
# 问答模式（代码内置默认 Flow + 默认 Agent，零配置）
heron

# 指定 Flow
heron --flow .agents/flows/default.yml

# 非交互模式（单次问答，适合 CI/CD）
heron --prompt "你好" --flow .agents/flows/default.yml

# HTTP 服务模式
heron --serve --port 8080
```

---

## 二、TUI 模式

### 启动

```bash
# 直接启动（自动查找 .agents/flows/default.yml，找不到用内置默认）
heron

# 指定 Flow
heron --flow .agents/flows/default.yml
```

### 欢迎界面

```
+----------------------------------------------------------+
|                                                          |
|                    HERON AI                              |
|              Multi-Agent Generic Engine                  |
|                                                          |
|  Flow:  default          Model:  deepseek-chat           |
|  Agents: 1               Teams:  1                      |
|                                                          |
|  Type /help for commands    Ctrl+C to exit               |
|  Enter to send              Up/Down to navigate history  |
|                                                          |
+----------------------------------------------------------+
```

### 主界面

```
+----------------------------------------------------------+
|  Heron AI - default                     Tokens: 1,234    |
+----------------------------------------------------------+
|                                                          |
|  [Round 1]                                               |
|                                                          |
|  assistant: 我去调查一下相关代码...  [500 tokens]         |
|                                                          |
|  ------------------------------------                    |
|  This round: 2,550 tokens | Total: 12,800 tokens         |
|                                                          |
|  Waiting for input...                                    |
|                                                          |
+----------------------------------------------------------+
|  Model: deepseek-chat | Round: 2/10 | Ctrl+C: quit       |
+----------------------------------------------------------+
|  >                                                       |
+----------------------------------------------------------+
```

### 界面区域说明

```
+---------------------------------------------+
|  标题栏: Flow 名 + 累计 Token               |  <- 固定顶部
+---------------------------------------------+
|                                             |
|  消息区 (viewport, 可滚动):                  |  <- 可滚动
|    - 用户消息 (右对齐)                       |
|    - Agent 回复 (左对齐, 带角色名)            |
|    - 多 Agent 并行输出 (颜色区分)             |
|    - Token 消耗统计                          |
|    - Signal 状态提示                         |
|                                             |
+---------------------------------------------+
|  状态栏: Model | Round | 快捷键提示           |  <- 固定底部
+---------------------------------------------+
|  输入区: > _                                |  <- 固定底部
+---------------------------------------------+
```

### 键盘操作

| 按键 | 操作 |
|------|------|
| 输入文本 + `Enter` | 发送消息 |
| `Up` / `Down` | 浏览输入历史 |
| `Ctrl+L` | 清屏（保留对话历史）|
| `Ctrl+C` | 退出 |

### Slash 命令

在输入框中以 `/` 开头的文本被识别为命令，不发送给 Agent：

| 命令 | 功能 | 示例 |
|------|------|------|
| `/help` | 显示帮助信息 | `/help` |
| `/exit` | 退出 TUI | `/exit` |
| `/clear` | 清空消息列表 | `/clear` |
| `/model` | 显示当前模型信息 | `/model` |
| `/usage` | 显示 Token 消耗统计 | `/usage` |
| `/flow` | 显示当前 Flow 配置 | `/flow` |
| `/agents` | 列出所有 Agent | `/agents` |

### 展示模式

| 模式 | 触发条件 | 界面表现 |
|------|---------|---------|
| **单 Agent** | Flow 只有 1 个 Agent | 只显示对话内容，不显示 Agent 名 |
| **多 Agent** | Flow 有多个 Agent | 每个 Agent 用不同颜色标识，显示角色名和输出 |

### 多 Agent 颜色方案

| 顺序 | 颜色 | 用途 |
|------|------|------|
| 1 | 蓝色 | Agent 1 |
| 2 | 绿色 | Agent 2 |
| 3 | 黄色 | Agent 3 |
| 4 | 品红 | Agent 4 |
| 5 | 青色 | Aggregator / 汇总 |

### 状态指示

| 图标 | 含义 |
|------|------|
| ... | Agent 执行中 |
| OK | Agent 完成 |
| ERR | Agent 出错 |
| PAUSE | 等待用户输入 |
| STOP | Run 结束 |

---

## 三、HTTP API 模式

### 启动服务

```bash
heron --serve --port 8080
```

服务启动后，可通过 HTTP 调用：

```
http://localhost:8080/api/run
```

### 端点列表

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/run` | 启动 Run |
| `GET` | `/api/run/{id}` | 查询 Run 状态 |
| `GET` | `/api/run/{id}/stream` | SSE 流式输出 |
| `GET` | `/api/run/{id}/usage` | 查询 token 消耗详情 |
| `POST` | `/api/run/{id}/resume` | wait_input 后继续 |
| `POST` | `/api/run/{id}/cancel` | 中断 Run |
| `POST` | `/api/run/{id}/approve` | HITL 审批 |

### POST /api/run

启动一个新的 Run。

```bash
curl -X POST http://localhost:8080/api/run \
  -H "Content-Type: application/json" \
  -d '{
    "flow": ".agents/flows/default.yml",
    "variables": { "Topic": "AI Agent" }
  }'
```

**响应**：

```json
{
  "run_id": "abc123",
  "status": "running",
  "stream_url": "/api/run/abc123/stream"
}
```

### GET /api/run/{id}

查询 Run 当前状态。

```bash
curl http://localhost:8080/api/run/abc123
```

**响应**：

```json
{
  "run_id": "abc123",
  "status": "waiting",
  "flow": ".agents/flows/default.yml",
  "current_round": 3,
  "signal": "wait_input",
  "created_at": "2026-07-08T10:00:00Z"
}
```

**status 枚举**：

| status | 说明 |
|--------|------|
| `running` | 正在执行 |
| `waiting` | 等待用户输入 |
| `ended` | 已结束 |
| `cancelled` | 已被用户取消 |
| `error` | 执行出错 |

### GET /api/run/{id}/stream

SSE 流式接收 Agent 输出。

```bash
curl -N http://localhost:8080/api/run/abc123/stream
```

**SSE 事件格式**：

```
data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"..."}}]}
data: {"object":"agent.chunk","agent_name":"reviewer","choices":[{"delta":{"content":"..."}}]}
data: {"object":"round.end","round_num":1,"signal":"wait_input"}
data: {"object":"usage.chunk","agent_name":"reviewer","input_tokens":500,"output_tokens":200}
data: {"object":"run.end","signal":"goal_achieved"}
data: [DONE]
```

**事件类型**：

| object | 说明 |
|--------|------|
| `chat.completion.chunk` | 标准 OpenAI chunk |
| `agent.chunk` | 多 Agent 模式内容块 |
| `agent.done` | Agent 输出完成 |
| `usage.chunk` | 每次 LLM 调用的 token 消耗 |
| `usage.round` | Round 结束时的 token 汇总 |
| `round.end` | Round 结束，携带 Signal |
| `hitl.request` | HITL 审批请求 |
| `run.end` | Run 结束，含总消耗 |

### POST /api/run/{id}/resume

```bash
curl -X POST http://localhost:8080/api/run/abc123/resume \
  -H "Content-Type: application/json" \
  -d '{"input": "继续"}'
```

### POST /api/run/{id}/cancel

```bash
curl -X POST http://localhost:8080/api/run/abc123/cancel
```

### POST /api/run/{id}/approve

```bash
curl -X POST http://localhost:8080/api/run/abc123/approve \
  -H "Content-Type: application/json" \
  -d '{"request_id": "hitl-001", "approved": true}'
```

---

## 四、非交互模式

```bash
# 单次问答
heron --prompt "用 Go 写 Hello World" --flow .agents/flows/default.yml

# 输出到文件
heron --prompt "审查以下代码..." --flow .agents/code_review.yml > review.md
```

非交互模式用于自动化测试和 CI/CD。Signal=wait_input 时直接结束，不等待用户。

---

## 五、错误码

| HTTP Code | 说明 |
|-----------|------|
| 200 | 成功 |
| 201 | Run 创建成功 |
| 400 | 参数校验失败 |
| 404 | Flow 不存在 / Run 不存在 |
| 409 | Run 状态不允许此操作 |
| 500 | 引擎内部错误 |
