# heron-ai 下载 / 使用 / 启动 / 关闭

## 下载与安装

### 方式一：npm 全局安装（推荐给用户）

```bash
npm install -g @qinghuangniao/heron-ai
heron --version
```

### 方式二：源码构建（开发）

```bash
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
```

或使用本 skill 附带的脚本：

```bash
bash <skill>/scripts/build.sh
```

## 配置 API Key

```bash
export OPENAI_API_KEY=sk-your-key
```

或写入 `.agents/models.json` 的对应模型 `api_key` 字段。

## 启动（运行模式）

```bash
# TUI 交互模式（默认）
heron

# 指定 flow
heron --flow .agents/flows/default.yml

# 非交互单次
heron --prompt "Hello" --flow .agents/flows/default.yml

# HTTP 服务
heron --serve --port 8080

# 常驻 JSON-RPC（供 heron-connect 等外部调用方）
heron --json-rpc --flow .agents/flows/default.yml
```

后台启动（带 PID 管理）可参考本 skill 附带的 `scripts/serve.sh`。

## 关闭

- TUI / 非交互：`Ctrl+C` 或进程自然退出。
- `--json-rpc` / `--serve` 是常驻进程，需显式结束：
  ```bash
  # 若有 PID 文件
  bash <skill>/scripts/serve.sh stop
  # 或直接
  kill <pid>
  ```

## 常用命令速查

| 命令 | 作用 |
|---|---|
| `heron --flow <path>` | 指定 flow 启动 |
| `heron --prompt <text> --flow <path>` | 非交互单次 |
| `heron --session <id> --prompt <text>` | 续聊指定会话 |
| `heron --list` | 列出会话并选一个续聊（非终端环境打印文本列表） |
| `heron --check-config --flow <path>` | **只校验配置**，打印每个 Team 绑定的 lifecycle，退出码 0 = 合法 |
| `heron --json-rpc --flow <path>` | JSON-RPC 常驻 |
| `heron --serve --port <port> --flow <path>` | HTTP 服务（**必须带 `--flow`**，否则报 `--serve requires a new-format Flow config` 并退出） |
| `heron --serve --host 0.0.0.0 --auth-token <t>` | 对外暴露时必须给 token，否则启动即拒绝 |
| `heron teams` | 列出全部 Team：`.agents/teams/*.yml`（source=local）与 `.agents/_teams/<scope>/`（已装包），带 VERSION/STATUS |
| `heron teams add <source> --dry-run` | 预演安装：解析 + 预览，不落盘 |
| `heron teams add <source>` | 安装 Team 包（本地目录 / `https://github.com/<owner>/<repo>[#ref]` / `.tar.gz` URL） |
| `heron teams remove <name> [--keep-agents]` | 卸载包；`--keep-agents` 只删 `teams/`，保留 agents/skills/rules |
| `heron teams export <team> [-o <dir>] [--format tar.gz\|dir] [--version x.y.z]` | 导出为分享包 |
| `heron knowledge [<session-id>]` | 增量学习会话知识 |
| `heron knowledge gc` | 归档过期知识（按 `ExpiresAt`） |
| `heron --version` | 版本 |

`--list` 与 `--check-config` 都在解析 Flow 之前或之外执行：`--list` 在没有 `.agents/`
的目录也能列出（空则打印「暂无会话」），`--check-config` 不需要能连上模型。

## 目录与运行时产物

- 会话事件按产生层级落盘三份：`.agents/data/sessions/<fs_id>/flow.jsonl`（flow/team
  编排 + `session.record.*`）、`team.jsonl`（agent call 时间线，token 消耗在此可查）、
  `agent.jsonl`（模型输出与工具调用）
- evidence 落盘 `.agents/data/sessions/<fs_id>/evidence.jsonl`（Flow-scope SharedRecord，
  有 Flow 级记录时才出现；被派发的子 Team 产出的记录会以 flow 作用域副本写进来，
  所以里面出现非入口 Team 写的记录是正常的）
- 状态快照落盘 `teams/<team_id>/state.md`、`agents/<team_id>/<call_id>/state.md`
- 这些是运行时产物，勿提交（`.gitignore` 已忽略 `.agents/data/`）。

## 验证配置是否正确

先用 `--check-config`，它只做加载与校验，不建模型客户端、不产生会话，秒级返回：

```bash
cd <项目根>   # 必须在含 .agents/ 的目录运行（models.json 相对 cwd 解析）
heron --check-config --flow .agents/<flow>.yml
```

输出形如：

```
default      team=qa_team       lifecycle=resident
flow default: 1 bindings ok (0 per_task)
```

退出码 0 = 合法；非 0 会把 loader 的错误打到 stderr（agent 名对不上、引用未声明的
record、`per_task` 绑定缺 `inputs.user_message` 等）。

只有在 `--check-config` 通过之后，才用一轮真实对话确认模型/工具链路：

```bash
heron --prompt "ping" --flow .agents/<flow>.yml
```

这一步会花 token，但能暴露 `--check-config` 覆盖不到的问题：模型名对应不上
(`model not found`)、API key 未设置、MCP server 起不来、工具被策略拒绝。
