# 配置模板

两份可直接复制的 `.agents/` 配置模板：

| 文件 | 复制到 | 作用 |
|---|---|---|
| `models.json` | `.agents/models.json` | 模型注册表：协议、endpoint、key、上下文上限 |
| `settings.json` | `.agents/settings.json` | 引擎行为：日志、runtime 上限、web 数据源、MCP、工具策略、子进程 env |

用法：

```bash
mkdir -p my-agent/.agents
cp configs/models.json configs/settings.json my-agent/.agents/
# 按需改 model / base_url，key 用 ${ENV_VAR} 或直接写
```

这两份**不会被引擎自动读取**：引擎只读工作区根下 `.agents/` 里的同名文件，`configs/` 只是
仓库里存放模板的位置。字段含义见：

- [docs/configuration/models.md](../docs/configuration/models.md)
- [docs/configuration/settings.md](../docs/configuration/settings.md)（含不生效段的清单）

模板里的取值以"能跑通"为准，不是性能调优建议：

- `logging.max_backups: 5`、`retention_days: 7` 是合理的清理窗口；`0` 表示不限制。
- `runtime.*` 是防跑飞的兜底，默认值刻意远高于正常工作负载。
- `tools.script.enabled: false`：`Script` 工具默认关闭，需要才开。
- `web.provider: duckduckgo` 免 key 但只答实体类查询；要真实搜索结果需换 provider 并配 key。
- `mcp: []` 与不写这一段等价，显式写出来是为了让人知道有这个位置。
