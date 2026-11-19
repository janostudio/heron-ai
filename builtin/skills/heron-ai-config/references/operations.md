# heron-ai 下载 / 使用 / 启动 / 关闭

## 下载与安装

### 方式一：npm 全局安装（用户）

```bash
npm install -g @qinghuangniao/heron-ai
heron --version
```

二进制随平台包分发，版本由 release tag 注入（npm 包里的 `0.0.0-dev` 是占位）。
npm 包只装二进制，**不含**仓库里的 `builtin/skills/`，想看内置 skill 得用源码。

### 方式二：源码构建（开发）

需要 Go 1.26+。

```bash
git clone git@github.com:janostudio/heron-ai.git
cd heron-ai
go build -o bin/heron ./cmd/server/
```

或用本 skill 附带的脚本（自动定位仓库根，也可用 `HERON_REPO` 指定）：

```bash
bash <skill>/scripts/build.sh              # 产物默认 <仓库>/bin/heron
bash <skill>/scripts/build.sh /tmp/heron   # 指定输出路径
```

## 配置 API Key

```bash
export OPENAI_API_KEY=sk-your-key       # openai_chat 协议
export ANTHROPIC_API_KEY=sk-ant-...     # anthropic_messages 协议
```

或写进 `.agents/models.json` 对应模型的 `api_key` 字段，**支持 `${ENV_VAR}` 展开**：

```json
{"id": "gpt-x", "name": "gpt-x", "api_key": "${OPENAI_API_KEY}"}
```

展开为空会直接报 `API key ... is not set`，不会回退到别的变量——防止把 A 厂商的 key
发给 B 厂商。Agent 的 `model.model` 写的是 profile 的 **`name`**，那里不做 `${}` 展开。

## 启动（运行模式）

```bash
# TUI 交互模式（默认）
heron

# 指定 flow
heron --flow .agents/flows/default.yml

# 非交互单次
heron --prompt "Hello" --flow .agents/flows/default.yml

# 续聊已有会话
heron --session <fs_id> --prompt "接着说"

# 列出会话并选一个续聊（非终端环境打印文本列表）
heron --list

# HTTP 服务（带 Web UI）
heron --serve --port 8080 --flow .agents/flows/default.yml

# 常驻 JSON-RPC（供 heron-connect 等外部调用方）
heron --json-rpc --flow .agents/flows/default.yml
```

`--flow` 省略时依次找 `.agents/flows/default.yml`、`.agents/flows/default.yaml`，
都找不到才报错。所以项目根是 `.agents/` 的上一级、flow 就叫 default 时，只写
`heron` 或 `heron --serve --port 8080` 就够。

后台启动（带 PID 管理）：

```bash
bash <skill>/scripts/serve.sh start --mode http --port 8080 --flow .agents/flows/default.yml
bash <skill>/scripts/serve.sh status
bash <skill>/scripts/serve.sh stop
```

脚本必须在项目根目录运行（heron 按 cwd 找配置）；PID 与日志默认落在 cwd 的
`heron-service.pid` / `heron-service.log`，可用 `HERON_PID` / `HERON_LOG` 改。

## 关闭

- TUI / 非交互：`Ctrl+C` 或进程自然退出。
- `--json-rpc` / `--serve` 是常驻进程，需显式结束：
  ```bash
  bash <skill>/scripts/serve.sh stop   # 有 PID 文件时
  kill <pid>                           # 或直接按 PID 结束
  ```

## 常用命令速查

| 命令 | 作用 |
|---|---|
| `heron --flow <path>` | 指定 flow 启动 |
| `heron --prompt <text> --flow <path>` | 非交互单次 |
| `heron --session <id> --prompt <text>` | 续聊指定会话 |
| `heron --list` | 列出会话并选一个续聊（不能与 `--session` / `--prompt` 同用） |
| `heron --check-config --flow <path>` | **只校验配置**，打印每个 Team 绑定的 lifecycle，退出码 0 = 合法 |
| `heron --json-rpc --flow <path>` | JSON-RPC 常驻（不能与 `--prompt` / `--serve` 同用） |
| `heron --serve --port <port> --flow <path>` | HTTP 服务 + 内置 Web UI |
| `heron --serve --host 0.0.0.0 --auth-token <t>` | 对外暴露时必须给 token（或 `HERON_SERVE_TOKEN`），否则启动即拒绝 |
| `heron --version` | 版本（含构建平台与 commit） |
| `heron teams` | 列出全部 Team：`.agents/teams/*.yml`（source=local）与 `.agents/_teams/<scope>/`（已装包），带 VERSION/STATUS |
| `heron teams add <source> --dry-run` | 预演安装：解析 + 预览，不写入安装目录 |
| `heron teams add <source>` | 安装 Team 包 |
| `heron teams remove <name> [--keep-agents]` | 卸载包；`--keep-agents` 保留 agents/skills/rules，只删 team 定义与清单 |
| `heron teams export <team> [-o <dir>] [--format tar.gz\|dir] [--version x.y.z]` | 导出为分享包 |
| `heron knowledge` | 学习全部会话的知识（需能解析到 flow） |
| `heron knowledge <session-id> [--flow <path>] [--model <name>]` | 学习单个会话 |
| `heron knowledge gc` | 归档过期知识（按 `ExpiresAt`） |

其余可选 flag：

| flag | 作用 |
|---|---|
| `--model <name>` | 覆盖 models.json 的默认模型（写 profile 的 `name`） |
| `--log-level <level>` | 覆盖 `settings.json` 的 `logging.level`（debug/info/warn/error） |
| `--max-rounds <n>` | 覆盖 agent 的 `loop.max_rounds`（0 = 用配置） |
| `--host <host>` | HTTP 绑定地址（默认 `127.0.0.1`） |
| `--auth-token <t>` | `--serve` 的 bearer token（默认读 `HERON_SERVE_TOKEN`） |
| `--exit-with-parent` | 启动它的进程退出时自己也退出（被 supervisor 拉起时用） |
| `--input-format stream-json` / `--output-format stream-json` / `--server <url>` | 三者一起用：以 stream-json 客户端模式连远端 heron 服务，需同时给 `--flow` |

两条子命令的坑：

- **`heron teams list` 不是合法子命令**：列表就是不带参数（`heron teams`），
  多写一个 `list` 会被当成未知子命令拒绝。`teams add` 的 `source` 可以是本地目录、
  本地归档、`https://github.com/<owner>/<repo>[#ref]` 或 `https://…/<pkg>.tgz`
  （远程只接受 https，且归档扩展名必须是 `.tgz`）；`--as` 改安装作用域，
  `--refresh` 忽略下载缓存，`--yes` 跳过脚本复核（等于让它跑别人的 shell，慎用）。
- **`heron knowledge <sid> --flow ...` 的 flag 要写在 sid 之前**：Go 的 flag 解析在第一
  个非 flag 参数处停止，放在 sid 后面的 `--flow` 会被当成第二个位置参数静默忽略，
  然后因为找不到 flow 报错。正确写法：`heron knowledge --flow <.yml> <sid>`。

`--list` 与 `--check-config` 都在解析 Flow 之前或之外执行：`--list` 在没有 `.agents/`
的目录也能列出（空则打印「暂无会话」），`--check-config` 不需要能连上模型。

## HTTP 服务的入口

`--serve` 除 API 外还自带 Web UI 与运行拓扑页：

| 路径 | 作用 |
|---|---|
| `GET /` | 内置 Web UI（会话、运行拓扑、对话视图） |
| `GET /api/health` | 健康检查（含版本与 commit） |
| `GET /api/topology?session_id=<fs_id>` | 该会话的 flow 拓扑（team/call 结构与执行状态） |
| `GET /api/stream` | SSE 事件流 |
| `POST /api/sessions/{id}/events` | 提交用户输入 |

非 loopback 绑定（如 `--host 0.0.0.0`）**必须**同时提供 token，否则进程直接拒绝启动，
不会先起服务再报错。

## 目录与运行时产物

- 会话事件按产生层级落盘三份：`.agents/data/sessions/<fs_id>/flow.jsonl`（flow/team
  编排 + `session.record.*`）、`team.jsonl`（agent/command/webhook call 时间线，token
  消耗在此可查）、`agent.jsonl`（模型输出与工具调用）
- evidence 落盘 `.agents/data/sessions/<fs_id>/evidence.jsonl`（Flow-scope SharedRecord，
  有 Flow 级记录时才出现；被派发的子 Team 产出的记录会以 flow 作用域副本写进来，
  所以里面出现非入口 Team 写的记录是正常的）
- 状态快照落盘 `sessions/<fs_id>/teams/<team_id>/state.md`、
  `sessions/<fs_id>/agents/<team_id>/<call_id>/state.md`；跨会话状态在
  `.agents/data/agents/<agentID>/state/`
- 这些是运行时产物，勿提交（`.gitignore` 已忽略 `.agents/data/`）。

## 验证配置是否正确

先用 `--check-config`，它只做加载与校验，不建模型客户端、不产生会话，秒级返回：

```bash
cd <项目根>   # 必须在含 .agents/ 的目录运行：定义以 cwd 的 .agents 为根解析
heron --check-config --flow .agents/<flow>.yml
```

输出形如：

```
default      team=qa_team       lifecycle=resident
flow default: 1 bindings ok (0 per_task)
```

退出码 0 = 合法；非 0 会把 loader 的错误打到 stderr。它拦得住：

- agent 名对不上、call 的 key 与 `id` 不一致、`depends_on` 未知或成环
- 引用了没人生产的 record、`${records.X}` 的 X 没写进同一 call 的 `inputs`
- `per_task` 绑定缺 `inputs.user_message: true`、entry team 写了 `per_task`
- skill 的 `scripts` 路径不存在或跑到 skill 目录外
- rule 的 `paths` 条件触发写错（打印警告，不失败）

它**拦不住**：模型名对应不上、API key 未设置、MCP server 起不来、工具被策略拒绝——
它不读 `models.json`，也不建模型客户端。所以顺序是「先 `--check-config`，再
`--prompt` 试一轮」：

```bash
heron --prompt "ping" --flow .agents/<flow>.yml
```

这一步会花 token，但能暴露上面那四类问题。
