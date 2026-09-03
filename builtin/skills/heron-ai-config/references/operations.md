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
| `heron --json-rpc --flow <path>` | JSON-RPC 常驻 |
| `heron --serve --port <port>` | HTTP 服务 |
| `heron --version` | 版本 |

## 目录与运行时产物

- 会话落盘 `.agents/data/sessions/<fs_id>/session.jsonl`
- evidence 落盘 `.agents/data/sessions/<fs_id>/evidence.jsonl`
- 这些是运行时产物，勿提交（`.gitignore` 已忽略 `.agents/data/`）。

## 验证配置是否正确

```bash
cd <项目根>   # 必须在含 .agents/ 的目录运行（models.json 相对 cwd 解析）
heron --prompt "ping" --flow .agents/<flow>.yml
```

若配置错误，会在加载/校验阶段报错（如 model not found、duplicate rule 等）。
