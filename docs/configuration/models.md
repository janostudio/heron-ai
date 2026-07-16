# models.json

Model registry. Defines available LLM models and the default model.

## Structure

```json
{
  "model": "deepseek-v4-flash",
  "models": [
    {
      "name": "deepseek-v4-flash",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "${OPENAI_API_KEY}",
      "max_tokens": 64000
    },
    {
      "name": "deepseek-v4-pro",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "${OPENAI_API_KEY}",
      "max_tokens": 64000
    }
  ]
}
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `model` | string | Default model name. Must match a `name` in the `models` array |
| `models` | array | List of available models |

### Model Entry

| Field | Type | Description |
|-------|------|-------------|
| `name` | string | Model identifier (e.g. `deepseek-v4-flash`) |
| `base_url` | string | API base URL |
| `api_key` | string | API key. Supports `${ENV_VAR}` syntax |
| `max_tokens` | integer | Maximum context window |

## Environment Variables

The `api_key` field supports `${ENV_VAR}` syntax:

```json
{ "api_key": "${OPENAI_API_KEY}" }
```

This reads from the `OPENAI_API_KEY` environment variable.

## Override via CLI

```bash
export OPENAI_API_KEY=sk-xxx
export LLM_MODEL=deepseek-v4-pro
heron --flow .agents/flows/blog.yml
```

Agent configs can also override: `model: ${LLM_MODEL:-deepseek-v4-flash}`
