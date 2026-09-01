# models.json

`models.json` 是全局模型注册表，也是 Agent 的默认模型配置来源。
Agent 文件只需要指定模型名称；没有在 Agent 上显式配置的参数，会从当前
模型条目继承。

## Structure

```json
{
  "model": "hy3-ioa",
  "models": [
    {
      "id": "hy3-ioa",
      "name": "hy3-ioa",
      "protocol": "openai_chat",
      "base_url": "http://127.0.0.1:15721/tencent/v1",
      "api_key": "${OPENAI_API_KEY}",
      "maxInputTokens": 192000,
      "maxOutputTokens": 64000,
      "temperature": 0.7,
      "top_p": 0.8,
      "supportsReasoning": true,
      "supportsToolCall": true
    },
    {
      "id": "claude-sonnet-4.6",
      "name": "claude-sonnet-4.6",
      "protocol": "anthropic_messages",
      "base_url": "https://api.anthropic.com/v1",
      "api_key": "${ANTHROPIC_API_KEY}",
      "maxOutputTokens": 24000
    }
  ]
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `model` | string | 默认模型，必须匹配某个模型的 `id` 或 `name` |
| `models` | array | 可用模型 profile 列表 |

### Model Entry

| Field | Type | Description |
|-------|------|-------------|
| `id` / `name` | string | 模型标识；`name` 缺省时使用 `id` |
| `protocol` | string | `openai_chat` 或 `anthropic_messages` |
| `base_url` | string | Provider API 基地址 |
| `api_key` | string | API Key，支持 `${ENV_VAR}` |
| `maxAllowedSize` | integer | 模型上下文及请求总大小上限 |
| `maxInputTokens` | integer | 最大输入 token 数 |
| `maxOutputTokens` | integer | 单次模型调用的默认输出上限 |
| `temperature` | number | 模型默认 temperature；缺省则不发送 |
| `top_p` | number | 模型默认 top-p；缺省则不发送 |
| `top_k` | integer | 模型默认 top-k；缺省则不发送 |
| `repetition_penalty` | number | 模型默认重复惩罚；缺省则不发送 |
| `reasoning` | object | Provider-neutral 推理配置 |
| `supportsReasoning` | boolean | 是否允许推理配置 |
| `supportsToolCall` | boolean | 是否发送工具定义 |
| `supportsTemperature` | boolean | 是否允许发送 temperature |
| `supportsTopP` | boolean | 是否允许发送 top-p |
| `supportsTopK` | boolean | 是否允许发送 top-k |
| `supportsRepetitionPenalty` | boolean | 是否允许发送重复惩罚 |
| `supportsStructuredOutput` | boolean | 是否声明支持结构化输出 |
| `fallback` | string[] | 有序备选模型名列表；本模型失败时按顺序切换 |
| `cooldown_seconds` | integer | 失败后作为被动备选被跳过的秒数；缺省 600 |

能力字段没有配置时表示“未知”，不是“支持”或“不支持”。Provider 会根据
协议选择安全的发送策略；服务端明确返回不支持参数时，会尝试移除可选参数重试。

## 模型故障转移（fallback）

当模型因可重试错误（限流 429 / 服务端 5xx / 超时 / 网络故障）失败时，
引擎会按 `fallback` 声明的顺序切换备选模型。认证错误（401/403）和参数
错误（400）不会触发切换。

```json
{
  "id": "gpt-4o",
  "name": "gpt-4o",
  "protocol": "openai_chat",
  "base_url": "https://api.openai.com/v1",
  "api_key": "${OPENAI_API_KEY}",
  "fallback": ["claude-sonnet", "deepseek-v3"],
  "cooldown_seconds": 60
}
```

语义：

- `fallback` 是扁平链，只遍历主模型直接声明的列表，不递归展开备选自身的
  `fallback`。
- 失败后该模型进入 cooldown 冷却（时长由 `cooldown_seconds` 决定，缺省
  600 秒），到期自动恢复。
- 冷却只影响被动备选的跳过；用户显式指定的主模型始终会尝试。
- cooldown 状态保存在进程内存中，不持久化，重启后重置。
- 实际使用的模型名会记录在 session.jsonl 的 `requests[]` 中（`model` 字段）。

## Agent 覆盖规则

参数合并优先级如下：

```text
Agent 显式配置
    >
models.json 当前模型条目
    >
Provider 默认值
    >
省略该参数
```

推荐的 Agent 配置：

```yaml
model:
  model: hy3-ioa
```

只有需要特殊行为时才覆盖：

```yaml
model:
  model: hy3-ioa
  temperature: 0
  max_output_tokens: 8192
```

`max_tokens` 仍然可以读取，但只是旧配置兼容字段；新配置使用
`max_output_tokens`。

## Provider 差异

Heron 使用统一的内部模型配置，但不同 Provider 会转换成不同请求协议：

| 内部概念 | OpenAI-compatible | Anthropic |
|-----------|-------------------|-----------|
| 输出上限 | `max_completion_tokens` | `max_tokens` |
| 工具 | `tools` | `tools` + `input_schema` |
| 系统消息 | `system` message | 顶层 `system` |
| 推理 | `reasoning_effort` | `thinking` / `output_config` |
| 结构化输出 | `response_format` | `output_config.format` |

模型名称不会决定协议。`protocol` 和 `base_url` 才决定使用哪个 Provider。
例如通过 OpenAI-compatible Gateway 调用 Claude，仍然应该配置
`protocol: openai_chat`。

## Environment Variables

```json
{ "api_key": "${OPENAI_API_KEY}" }
```

如果配置显式引用了一个不存在的环境变量，Heron 不会静默使用其他 Provider
的 API Key。

## Override via CLI

```bash
export OPENAI_API_KEY=sk-xxx
heron --flow .agents/flows/blog.yml
```
