# Real auto-bugfix fixture: `.gitignore`

这个示例不是一组“模拟 Agent 名字”，而是一个可以在真实 Workspace 中运行的
`Flow → Team → Call` 项目。它把 `auto_bugfix` 里真正有价值的部分落到 Heron：

- 多个专业 Agent；
- Team 之间通过 `SharedRecord` 传递事实；
- `command` 负责确定性快照、验证和知识写入；
- `subagent` 负责诊断、挑战、修复、审查和解释；
- Team / Agent 使用短期 Memory；
- Knowledge 以 `.md` 文件长期保存；
- `session.jsonl` / `evidence.jsonl` 可以观测和恢复；
- `recovery` 可以检查中断而不会默认重放副作用。

本示例**不复制** auto_bugfix 中与这个业务无关的能力：

- Issue / PR / CNB API；
- Sandbox / Grafana；
- Worktree 池；
- 外部仓库分支管理。

这些属于 auto_bugfix 业务包或 Skill，不是 Heron Core 的固定能力。

## Flow

```text
default
  → diagnose
  → challenge
  → fix
  → test
  → review
  → learn
  → audit
  → default
```

其中：

```text
diagnose
  ├── git_snapshot: command
  ├── explorer: subagent
  └── root-cause-analyst: subagent

challenge
  ├── challenger: subagent
  └── challenge-skeptic: subagent

test
  ├── verify_gitignore.sh: command
  ├── check_project_scope.sh: command
  └── test-runner: subagent

learn
  ├── record_learning.sh: command
  └── knowledge-learner: subagent

audit
  ├── analyze_session.py: command
  └── audit-agent: subagent
```

这正是“Team 下面不一定全是 Agent”的实际例子。

## Agent / Skill / Script 目录

```text
.agents/
├── agents/
│   ├── default-coordinator.md
│   ├── explorer.md
│   ├── root-cause-analyst.md
│   ├── challenger.md
│   ├── challenge-skeptic.md
│   ├── code-fixer.md
│   ├── test-runner.md
│   ├── code-reviewer.md
│   ├── knowledge-learner.md
│   └── audit-agent.md
├── skills/
│   ├── root-cause-analysis-skill/
│   ├── code-review-skill/
│   ├── self-evolving/
│   ├── backend-test/
│   ├── gitignore-diagnostics/
│   └── session-observation/
├── scripts/
│   ├── git_snapshot.sh
│   ├── verify_gitignore.sh
│   ├── check_project_scope.sh
│   ├── record_learning.sh
│   └── analyze_session.py
├── teams/
├── flows/
├── knowledge/
└── rules/
```

其中 `root-cause-analysis-skill`、`code-review-skill`、`self-evolving` 和
`backend-test` 是从原 auto_bugfix 的能力抽取后放入示例的参考 Skill；
示例额外用 `gitignore-diagnostics`、`session-observation` 等业务 Skill 进行
收敛。它们都只能操作当前 Workspace，不依赖 `.codebuddy`。

## 测试目标

初始 `project/.gitignore` 只有：

```gitignore
*.log
```

故意遗漏：

- `.env`
- `.env.local`
- `.pytest_cache/`
- `__pycache__/`
- `dist/`
- `.idea/`

必须保留：

- `README.md`
- `app.py`
- `requirements.txt`
- `.gitignore`

## 准备 Fixture

在本目录运行：

```bash
bash setup-fixture.sh
```

脚本会：

1. 初始化嵌套的 `project/.git`；
2. 提交源码、README、依赖声明和初始 `.gitignore`；
3. 创建本地 `.env`、缓存、构建产物和 IDE 文件；
4. 不把测试生成物提交到外层仓库。

## 运行

### 模型配置

示例已经配置为使用本地 OpenAI-compatible endpoint：

```text
API Base URL:
http://127.0.0.1:15721/tencent/v1

Models URL:
http://127.0.0.1:15721/tencent/v1/models
```

请在以下文件中自行填写 API Key：

```text
.agents/models.json
.env.example
```

当前这个 auto-bugfix 示例的所有 Agent 统一使用：

```text
hy3-ioa
```

`models.json` 中保存的是你提供的 Hy3 模型元数据：

```text
maxAllowedSize: 192000
maxInputTokens: 192000
maxOutputTokens: 64000
temperature: 0.7
top_p: 0.8
supportsReasoning: true
supportsToolCall: true
```

这里的 `maxOutputTokens=64000` 是模型能力上限，不代表每个 Agent 每次都会
请求 64000 个 token。当前 Agent 没有单独配置 `max_tokens`，
会自动继承当前模型的 `maxOutputTokens`。同样，Agent 没有配置
`temperature`、`top_p` 或推理参数时，也会继承模型条目中的值。
只有 Agent 显式配置时才会覆盖模型默认值。

本地服务当前返回的其他可用模型包括：

```text
claude-sonnet-5
claude-sonnet-5-1m
claude-sonnet-4.6
claude-sonnet-4.6-1m
claude-opus-5
gpt-5.6-terra
gpt-5.6-sol
gpt-5.6-luna
glm-5.3-ioa
deepseek-v4-flash-ioa
```

如果切换模型，只需要修改：

```text
.agents/models.json 的 model
各 Agent frontmatter 中的 model.model（如果希望只切换部分 Agent）
```

这里的模型都通过本地 OpenAI-compatible endpoint 调用，所以
`.agents/models.json` 中的 `protocol` 为 `openai_chat`。如果以后增加
原生 Anthropic endpoint，应将对应模型的 `protocol` 配置为
`anthropic_messages`，Agent 配置不需要改成另一套参数名称。

检查本地模型服务：

```bash
curl http://127.0.0.1:15721/tencent/v1/models
```

### 先做本地检查

```bash
bash .agents/scripts/preflight.sh
bash .agents/scripts/git_snapshot.sh
bash .agents/scripts/verify_gitignore.sh
```

`verify_gitignore.sh` 初始会输出 `RESULT failed`，这是预期的业务事实，
不是脚本崩溃。它仍然以成功的 CommandTurn 发布 `VerificationReport`，由
Test Team 决定是否回到 Fix Team。

### 启动真实 Flow

在 `heron-ai/examples/auto-bugfix-gitignore` 目录运行：

```bash
export OPENAI_API_KEY=...
./run-auto-bugfix.sh "请检查 project 的 .gitignore，补齐必要规则并验证。"
```

等价命令：

```bash
go run ../../cmd/server \
  --flow .agents/flows/auto_bugfix.yml \
  --prompt "请检查 project 的 .gitignore，补齐必要规则并验证。"
```

Workspace 是进程当前目录，因此建议始终在本示例目录运行。脚本中的
`project/.gitignore`、`.agents/scripts/...` 都是相对于这个 cwd 的路径。

## 观测和恢复

运行结束后，Session 数据位于：

```text
.agents/data/sessions/<flow_session_id>/
├── session.jsonl
├── evidence.jsonl
├── teams/<team>/memory.md
└── agents/<team>/<call>/memory.md
```

查看当前会话的精简摘要：

```bash
python3 .agents/scripts/analyze_session.py \
  --session <flow_session_id>
```

实时观测：

```bash
curl -N \
  "http://localhost:8080/api/stream?session_id=<flow_session_id>"
```

发现进程中断时先检查：

```bash
curl \
  "http://localhost:8080/api/recovery/status?session_id=<flow_session_id>"
```

不要直接重放 `command`。只有配置了 `replay_policy: idempotent` 或
`allow`，并且通过 Recovery API 显式请求，才允许恢复执行。
