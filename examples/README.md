# 示例

每个子目录都是一份完整的 `.agents/` 配置树，可以整目录复制当起点。
四个示例都会被 `examples_test.go` 用真实的加载器加载一遍，改坏了 CI 会红。

## 先补一份 models.json

示例里**没有** `models.json`：模型地址和 key 属于使用方本地环境，不随示例分发。
复制示例后，在同级 `.agents/` 下新建：

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

然后把示例里 Agent 的 `model.model` 改成上面某个 `name`。**Agent 的 `model` 段不做
`${ENV}` 展开**，那里只能写模型名；只有 `models.json` 的 `api_key` 支持
`${ENV_VAR}`。

## 逐个示例

| 示例 | Flow 路径 | 规模 | 看点 |
|---|---|---|---|
| [simple-qa](./simple-qa/) | `.agents/flows/default.yml` | 1 Agent / 1 Team | 最小可用结构：一条 call、一条 record |
| [code-review](./code-review/) | `.agents/code_review.yml` | 3 Agent / 1 Team | 并行 call + `depends_on` 汇总，适合看 Team 内调度 |
| [blog-writer](./blog-writer/) | `.agents/flows/blog.yml` | 4 Agent / 3 Team | 三个阶段串行、按 record 交接，适合看 Flow 路由 |
| [novel-rp](./novel-rp/) | `.agents/flows/flow.yml` | 3 Agent / 3 Team | `can_activate` 动态唤起 + `.agents/cast/` 角色卡 |

启动方式一致，只是 flow 路径不同：

```bash
cd examples/simple-qa
heron --check-config                       # 先确认定义合法
heron                                      # 或 heron --prompt "你好"
```

## 复制到自己的项目

```bash
cp -r examples/simple-qa/.agents /path/to/my-project/
cd /path/to/my-project
# 写 .agents/models.json，对齐 Agent 里的 model 名
heron --check-config
```

示例里的 `knowledge/`、`rules/`、`skills/` 都是演示用法，删掉不影响跑通；
但删之前要同时删掉 Agent 里对它们的引用（`knowledge:` / `rules:` / `skills:`），
否则加载期会报引用不存在。

## 与 Engine 版本的对应

示例只用当前引擎支持的字段。如果某个示例在你的版本上 `--check-config` 失败，
先确认引擎版本（`heron --version`）与仓库同步；`${ENV}` 占位符、`Edit` 工具这类
写法在当前引擎里都不是合法配置。
