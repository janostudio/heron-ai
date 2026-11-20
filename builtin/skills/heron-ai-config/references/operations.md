# 安装 / 启动 / CLI

## 安装

```bash
# 用户：npm 全局（二进制随平台包分发，版本由 release tag 注入）
npm install -g @qinghuangniao/heron-ai
heron --version

# 开发：源码构建（Go 1.26+）
go build -o bin/heron ./cmd/server/
bash <skill>/scripts/build.sh [输出路径]     # 自动定位仓库根，可用 HERON_REPO 指定
```

npm 包只装二进制，**不含** `builtin/skills/`；要看内置 skill 得用源码。

## Key

```bash
export OPENAI_API_KEY=sk-...        # openai_chat
export ANTHROPIC_API_KEY=sk-ant-... # anthropic_messages
```

或写进 `models.json` 的 `api_key`，支持 `${ENV_VAR}`；展开为空直接报 `API key ... is not set`，不回退。
Agent 的 `model.model` 写 profile 的 **`name`**，那里不做 `${}` 展开。

## 启动

```bash
heron                                       # TUI
heron --flow .agents/flows/default.yml
heron --prompt "..." --flow .agents/flows/default.yml    # 非交互单次
heron --session <fs_id> --prompt "接着说"                  # 续聊
heron --list                                # 列会话并选一个
heron --serve --port 8080                   # HTTP + Web UI
heron --json-rpc                            # 常驻 JSON-RPC（供 heron-connect）
```

`--flow` 省略时依次找 `.agents/flows/default.yml` / `.yaml`，所以项目根就是 `.agents/` 的上一级、
flow 叫 default 时 `heron` 或 `heron --serve --port 8080` 就够。

后台（PID 管理）：

```bash
bash <skill>/scripts/serve.sh start --mode http --port 8080 --flow .agents/flows/default.yml
bash <skill>/scripts/serve.sh status
bash <skill>/scripts/serve.sh stop
```

脚本要在项目根跑（heron 按 cwd 找配置）；PID 与日志默认是 cwd 的
`heron-service.pid` / `heron-service.log`，可用 `HERON_PID` / `HERON_LOG` 改。

## 关闭

`Ctrl+C` 或进程自然退出；`--serve` / `--json-rpc` 是常驻进程，用 `serve.sh stop` 或 `kill <pid>`。

## 命令速查

| 命令 | 作用 |
|---|---|
| `--check-config` | 只加载并校验定义，打印每个 Team 绑定的 lifecycle；退出码 0 = 合法 |
| `--prompt <text>` | 非交互单次 |
| `--session <id>` | 续聊指定会话 |
| `--list` | 列会话（不能与 `--session` / `--prompt` 同用） |
| `--json-rpc` | JSON-RPC 常驻（不能与 `--prompt` / `--serve` 同用） |
| `--serve --port <p>` | HTTP + 内置 Web UI |
| `--serve --host 0.0.0.0 --auth-token <t>` | 对外暴露**必须**给 token（或 `HERON_SERVE_TOKEN`），否则拒绝启动 |
| `--version` | 版本（含平台与 commit） |
| `teams` | 列全部 Team（`teams/*.yml` 与已装包 `_teams/<scope>/`），带 VERSION / STATUS |
| `teams add <source> [--dry-run]` | 装 Team 包；`--dry-run` 只解析不写入 |
| `teams remove <name> [--keep-agents]` | 卸包；`--keep-agents` 保留 agents/skills/rules |
| `teams export <team> [-o dir] [--format tar.gz\|dir]` | 导出分享包 |
| `knowledge [<sid>]` / `knowledge gc` | 学知识 / 归档过期知识 |

| flag | 作用 |
|---|---|
| `--model <name>` | 覆盖 models.json 默认模型（写 profile 的 `name`） |
| `--log-level <level>` | 覆盖 `logging.level` |
| `--max-rounds <n>` | 覆盖 `loop.max_rounds`（0 = 用配置） |
| `--host` / `--auth-token` | 绑定地址 / Bearer token |
| `--exit-with-parent` | 父进程退出时自己也退出 |
| `--input-format` / `--output-format stream-json` + `--server <url>` | stream-json 客户端模式连远端服务，需同时给 `--flow` |

三个坑：
- **`heron teams list` 不合法**：列表就是 `heron teams`。`teams add` 的 source 支持本地目录、
  本地归档、`https://github.com/<owner>/<repo>[#ref]`、`https://…/<pkg>.tgz`（远程只收 https，
  归档必须是 `.tgz`）；`--as` 改作用域，`--refresh` 忽略缓存，`--yes` 跳过脚本复核（等于跑别人的 shell）。
- **`knowledge` 的 flag 要写在 sid 之前**：Go flag 在第一个非 flag 参数处停止解析，
  `heron knowledge <sid> --flow x.yml` 会把 `--flow` 当第二个位置参数静默忽略。
- `--list` 与 `--check-config` 不依赖 flow 能跑通：前者在没有会话时打印「暂无会话」，
  后者不需要模型可用。

## HTTP 入口

| 路径 | 作用 |
|---|---|
| `GET /` | 内置 Web UI（会话 / 拓扑 / 对话） |
| `GET /api/health` | 健康检查（版本、commit、`pending_wakeups`、`sessions_loaded`） |
| `GET /api/topology?session_id=` | 该会话的 flow 拓扑 |
| `GET /api/stream` | SSE 事件流 |
| `POST /api/sessions/{id}/events` | 提交用户输入 |

非 loopback 绑定必须给 token，否则**启动即拒绝**（不是先起后报）。

## 校验配置

```bash
cd <项目根>            # 必须在含 .agents/ 的目录：定义以 cwd 的 .agents 为根解析
heron --check-config
```

输出形如 `solo team=solo lifecycle=shared` / `flow main: 1 bindings ok (0 isolated)`。
能拦什么、拦不住什么见 `../SKILL.md` §5。校验通过后再 `--prompt "ping"` 试一轮——那一步会花 token，
但能暴露校验看不见的问题（模型名、key、MCP、工具被拒）。

运行时产物落在哪、怎么读，见 `observe.md`。这些文件是运行时产物，**勿提交**（`.gitignore` 已忽略 `.agents/data/`）。
