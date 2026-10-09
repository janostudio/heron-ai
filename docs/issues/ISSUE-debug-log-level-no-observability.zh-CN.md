# 缺陷：`--log-level debug` 几乎没有可观测性（全引擎仅 1 处 debug 日志）

> 状态：**未修复**（2026-10-09）
> 影响面：所有需要排查"引擎实际发出了什么请求/收到了什么响应"的问题
> 关联：`cmd/server/main.go:45`（参数）、`internal/agent/runtime.go:686`（唯一 debug 日志）

## 现象

`--log-level debug` **可以传、也不报错，但基本看不到额外信息**：

```bash
heron --session <fs_id> --prompt "..." --log-level debug
# 输出与不加 --log-level 时几乎无差别
```

实测：用 debug 复现一次「HTTP 400 `tool calls and tool results do not match`」，
日志里**依然只有 error 级记录**，没有任何 debug 级内容可用于定位。

## 附：日志级别本应由配置控制（命令行应移除）

排查中发现 `--log-level` 这个命令行参数**是多余的**——`settings.json` 已支持同等能力：

```json
// .agents/settings.json
"logging": {
  "level": "debug",        // ← 配置在这里即可
  "output": "file",
  "dir": ".agents/data/logs",
  ...
}
```

优先级在 `internal/app/runtime.go:566-573`：

```go
func buildLogger(workspaceRoot, logLevelOverride string) *logging.RotatingLogger {
	loader := config.NewConfigLoader(workspaceRoot)
	cfg := loader.LoadLoggingSettings()            // 先读 settings.json
	if strings.TrimSpace(logLevelOverride) != "" {
		cfg.Level = logLevelOverride               // 命令行覆盖配置
	}
	...
}
```

**结论：配置本就是权威来源，命令行只是覆盖。** 日志级别属于项目级设置，
不应散在每条命令里。建议：

- **删除 `--log-level` 参数**（`cmd/server/main.go:45`）
- **删除 `logLevelOverride` 传参链路**（`BuildRuntime` 的形参、`cliOverrides.logLevel`）
- 需要临时提级时改 `.agents/settings.json` 的 `logging.level`

> 注：本节是**配置简化建议**，与下面的"debug 级日志几乎不存在"是两个独立问题：
> 即便把 level 设成 debug，当前也几乎看不到额外信息。

## 根因：debug 级日志几乎不存在

全仓 `logging.Debug` 调用数：

```bash
$ grep -rn "logging.Debug" internal/ cmd/ | wc -l
1
```

**只有 1 处**，且在 `internal/agent/runtime.go:686`，只在**模型请求成功**后记录 usage：

```go
logging.Debug("model request usage", map[string]any{
	"flow_session_id": req.FlowSessionID,
	"team_id":         req.TeamID,
	"call_id":         req.CallID,
	...
})
```

于是 debug 级别能提供的额外信息**仅限"成功的请求用了多少 token"**。

## 关键缺口：不记录请求 / 响应体

排查模型协议类问题（400 / 参数不合法 / 工具配对）时，**唯一能定位的证据是实际发出的 request body**。
当前代码不记录它：

| 内容 | 是否记录 |
|---|---|
| 请求 body（messages / tools / response_format） | ❌ 从不 |
| 响应 body（错误详情） | ⚠️ 仅错误消息的字符串（`openai chat: bad_request (http 400): {...}`） |
| 请求失败时的上下文（第几轮、消息条数、是否含 tool 消息） | ❌ |

以本次排查为例，`internal/model/openai.go` 的 `post()` 失败路径只把错误文本往上抛，
没有把请求体落到日志。结果：

- 知道"服务端说工具配对不合法"
- **不知道**引擎实际发出的 messages 长什么样
- 只能靠手工构造请求去猜触发条件（已试 8 种形态，均未复现）

## 影响

1. **协议层问题无法定位**。本次 11148 就是典型：本地无法复现、线上必然失败、
   日志不提供证据，排查只能靠猜。
2. **`--log-level debug` 是误导性选项**：用户以为开了就能看到细节，
   实际几乎无输出，反而浪费排查时间。
3. **同类问题会反复出现**（任何 400/参数错误都要经历同样的困境）。

## 修复方向

### A. 失败路径记录请求摘要（最小、最有用）

`internal/model/openai.go` 的 `post()` 返回错误时，以 **debug 级**记录：

```go
logging.Debug("model request failed", map[string]any{
    "model":        effective.Model,
    "message_count": len(req.Messages),
    "roles":         roleSequence(req.Messages),   // ["system","user","assistant","tool",...]
    "tool_call_ids": toolCallIDs(req.Messages),    // assistant 声明的 id 列表
    "tool_result_ids": toolResultIDs(req.Messages),// tool 消息回填的 id 列表
    "request_body":  redact(sanitizedBody),        // 可选：完整 body（脱敏 api_key）
})
```

其中 `roleSequence` + 两套 id 列表**足以定位"工具配对"类问题**，且体积可控
（不必每次落完整 body）。

### B. 成功路径也记录请求规模（辅助）

已有 usage 日志，可补 `request_messages` 条数与角色序列，便于对比正常/异常。

### C. 完整 body 只在显式开关下落盘

`HERON_DEBUG_DUMP_REQUEST=<path>` 之类环境变量，把完整 request/response 写到文件。
默认关闭（body 可能含业务数据），仅在深度排查时启用。

## 复现

```bash
cd /Users/jahweijiang/Documents/tencent/auto_bugfix/heron-ai

# 1. debug 级几乎无输出
heron --prompt "你好" --log-level debug 2>&1 | head
# 与不加参数对比，缺少可定位的 debug 细节

# 2. 全仓 debug 日志只有 1 处
grep -rn "logging.Debug" \
  /Users/jahweijiang/Documents/github/multirole-agent/heron-ai/internal/ \
  /Users/jahweijiang/Documents/github/multirole-agent/heron-ai/cmd/ | wc -l
# → 1

# 3. 用 debug 复现协议错误，日志仍只有 error
heron --session <fs_id> --prompt "..." --log-level debug 2>&1 | tail
# → 只有 [TeamFailureReport] 之类的 error，无请求细节
```

## 相关代码位置

| 定位 | 内容 |
|---|---|
| `cmd/server/main.go:45` | `--log-level` 参数定义 |
| `internal/app/runtime.go:60` | `BuildRuntime(..., logLevelOverride)` 接收该参数 |
| `internal/agent/runtime.go:686` | **全仓唯一**的 `logging.Debug`（成功后的 usage） |
| `internal/model/openai.go` `post()` | 请求发送点；失败时未记录请求上下文 |
| `internal/logging/` | 日志级别与输出实现 |

## 一句话

两件事：① `--log-level` 应从命令行移除——`settings.json` 的 `logging.level` 已是权威来源，
命令行只是覆盖；② 即便把 level 设为 debug，全引擎也仅 1 处 debug 日志（只记成功后的 usage），
从不记录请求/响应体，导致协议类错误（如本次 11148）本地无法复现、线上无证据可查——
应补齐模型请求失败路径的请求摘要（角色序列 + tool id 配对）日志。
