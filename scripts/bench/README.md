# scripts/bench

Dependency-free HTTP 压测客户端（仅 `net/http` + `encoding/json` + `flag`），
用于压 Heron 引擎的 HTTP 服务（`bin/heron --serve`）。附带一个 OpenAI 兼容的
mock LLM 服务，无真实模型端点时也能跑端到端压测。

**脚本只测吞吐与延迟，不测内存。** 观察内存请另开终端盯 RSS（见下文）。

## 无模型端点时的完整三件套（mock LLM → serve → 压测）

simple-qa 的默认模型走 `http://127.0.0.1:15721/tencent/v1`，不在内网时该地址
不通。用内置 mock 起一个同地址的假模型服务即可全链路压测（models.json 零改动）：

```bash
# 1) 起 mock LLM（监听 127.0.0.1:15721，返回固定回答 + 真实 usage 结构）
go run -C heron-ai ./scripts/bench/mockllm

# 2) 起引擎 HTTP 服务（cwd 必须是 examples/simple-qa，models/flow 相对 cwd 解析）
zsh -c 'cd heron-ai/examples/simple-qa && nohup ../../bin/heron --serve --port 8080 > /tmp/heron-serve.log 2>&1 &'

# 3) 压测
go run -C heron-ai ./scripts/bench -concurrency 10 -rounds 20 -base http://127.0.0.1:8080
```

> mock 端口 15721 与默认 models.json 一致，**无需改 models.json**；压测后停掉
> mock 即可（`kill` 对应进程）。实测基线（M3 Pro，mock 后端）：c=10 QPS~136、
> p50~70ms；c=100 QPS~114、p99~1.1s，2000+ 请求 0 错误、RSS 从 33MB→46MB 无泄漏。

## 起服务（真实模型）

先构建并启动 HTTP 服务端（cwd 必须在 `heron-ai`，因为 flow/models 相对 cwd 解析）：

```bash
go build -C heron-ai -o bin/heron ./cmd/server
cd heron-ai
./bin/heron --serve --port 8080 --flow examples/simple-qa/.agents/flows/default.yml
```

看到 `Heron AI FlowRuntime server listening on :8080` 即就绪。

## 跑压测

```bash
go run -C heron-ai ./scripts/bench -concurrency 8 -rounds 10 -base http://127.0.0.1:8080
```

参数：

| flag | 默认 | 说明 |
|------|------|------|
| `-concurrency N` | 8 | 并发 worker goroutine 数 |
| `-rounds M` | 10 | 每个 worker 的请求轮数 |
| `-base URL` | `http://127.0.0.1:8080` | 服务端地址 |

每个 worker 第一轮 `POST /api/run` 开新会话，后续轮次用返回的
`session_id` 走 `POST /api/sessions/turn?session_id=...` 续聊，从而覆盖
会话续聊热路径。

输出示例：

```
=== Heron HTTP load summary ===
concurrency:     8
rounds/worker:   10
total requests:  80
errors:          0
elapsed:         12.3s
qps:             6.50 req/s
latency p50:     1.24s
latency p95:     2.81s
latency p99:     3.15s
```

## 观察内存

脚本不采集内存。压测期间另开一个终端盯服务端进程 RSS：

```bash
# 找服务端 PID
pgrep -f 'bin/heron --serve'
# 或者看当前 RSS（KB）
ps -o pid,rss,comm -p <PID>
# 或实时刷新
top -pid <PID>
# 或采样一段时间看是否有增长
while true; do ps -o rss= -p <PID>; sleep 2; done
```

关键看 RSS 是否随轮次持续上涨（泄漏），还是压测结束后回落并稳定。
