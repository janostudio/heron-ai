# 最小可跑模板

三份模板都是**能直接跑**的最小形态：没有 `loop` / `context` / `budget` / `completion`，
因为那些默认值就是最佳实践（见 `../SKILL.md` §2）。先跑通，再按症状加。

目录固定：

```text
.agents/
├── models.json                     # 必需
├── flows/default.yml               # 入口
├── teams/<team>.yml
└── agents/<agent>/AGENT.md
```

---

## 模板 1：单 agent（一个 team、一个 call）

`models.json`（唯一必需文件）：

```json
{
  "model": "gpt-4o-mini",
  "models": [
    {
      "name": "gpt-4o-mini",
      "id": "gpt-4o-mini",
      "protocol": "openai_chat",
      "base_url": "http://127.0.0.1:15721/v1",
      "api_key": "${OPENAI_API_KEY}",
      "maxInputTokens": 128000
    }
  ]
}
```

`agents/reviewer/AGENT.md`：

```markdown
---
name: reviewer
model:
  model: gpt-4o-mini
tools:
  builtin: [Read, Grep, Glob]
---
你是代码评审员。只读，不改文件。输出问题清单（文件:行 + 问题 + 建议）。
```

`teams/solo.yml`：

```yaml
id: solo
calls:
  work:
    type: agent
    agent: reviewer
    inputs:
      user_message: true
output:
  from: work
  record: Reply
```

`flows/default.yml`：

```yaml
id: main
entry: solo
teams:
  solo:
    team: solo
```

跑：

```bash
heron --check-config          # 期望 exit 0
heron --prompt "评审 internal/agent/runtime.go 的错误处理"
```

---

## 模板 2：两跳（入口路由到执行侧）

入口只分诊、不干活；执行侧出结论。两跳靠 `can_activate` + SharedRecord 连接。

`flows/default.yml`：

```yaml
id: main
entry: frontdesk
teams:
  frontdesk:
    team: frontdesk
    can_activate: [worker]        # 它可以激活谁
    inputs:
      user_message: true
      flow_records: [WorkerReply] # 它要读下游的结论（按名匹配）
  worker:
    team: worker
    lifecycle: isolated           # 每个任务一个实例；必须配 inputs.user_message
    inputs:
      user_message: true
```

`teams/frontdesk.yml`：

```yaml
id: frontdesk
goal: 分诊并转述结论。
calls:
  triage:
    type: agent
    agent: receptionist
    inputs:
      user_message: true
output:
  from: triage
  record: CoordinatorReply
```

`teams/worker.yml`：

```yaml
id: worker
goal: 完成一个具体任务。
calls:
  do:
    type: agent
    agent: worker
    inputs:
      user_message: true
output:
  from: do
  record: WorkerReply          # 名字必须与 flow 里 flow_records 一致
```

要点：
- `can_activate` 不写 → 这个 team 谁也不能激活。
- 被激活的 team 结束后**回到激活它的 team**，不是回到用户。
- 入口 team 不能 `isolated`。

---

## 模板 3：脚本 call + 下游消费（command → agent）

脚本确定性执行，结果交给 agent 判断。**command 不读对话**，只能通过 `inputs` + `${records.*}` 拿上游数据。

`teams/check.yml`：

```yaml
id: check
calls:
  build:
    type: command
    command:
      command: "bash .agents/skills/ci/scripts/build.sh"
    output:
      record: BuildResult

  judge:
    type: agent
    depends_on: [build]           # 有依赖 → 串行
    agent: judge
    inputs:
      records:                    # 同 team 内消费前一个 call 的产出用 records+from
        - from: build
          record: BuildResult
    output:
      record: Verdict
```

`agents/judge/AGENT.md` 里让模型知道上游内容在哪：

```markdown
## 输入
本轮会附带上游 `BuildResult` 记录：`Data.exit_code` 是退出码，`Data.stdout` 是构建输出字符串，
`Data.stderr` 是错误输出。
```

注意：command 的 `Data` 是 `{exit_code, stdout, stderr, passed}`，业务 JSON 在 `stdout` **字符串**里，
要自己解析；`${records.*}` 模板只在 command/webhook 的 `env` / `args` 里展开，agent 正文不展开。

---

## 跑起来之后检查三件事

```bash
S=.agents/data/sessions/$(ls -1t .agents/data/sessions | head -1)

# 1) 事件走完了没有（正常：started → team_turn → ended）
jq -r '.type' $S/flow.jsonl

# 2) 这一轮怎么结束的、在等谁
jq -r 'select(.type=="session.turn.ended") | "\(.payload.stop_reason) awaiting=\(.payload.awaiting)"' $S/flow.jsonl

# 3) 有没有被压缩（0 次是常态；频繁说明上下文配置有问题）
jq -r 'select(.type=="session.context.compacted") | .payload.dropped_count' $S/agent.jsonl | wc -l
```

正常的三个特征：`stop_reason=end_turn`、`awaiting=user`、压缩 0 次。
出现异常再读 `diagnose.md`，并按 `tuning.md` 只改一个键。

---

## 加配置的正确顺序

1. **先不加**：跑一轮，看 `reply` 是否符合预期。
2. **只加"能力"**：工具不够就加 `tools.builtin`；要读外部目录就加 `read_roots`。
3. **再考虑"边界"**：确实需要更早停才加 `max_rounds`；有明确成本约束才加 `budget`。
4. **最后才碰"行为调参"**：`context.*` / `loop.*` 的守卫项，只有证据指向它们时才动。

一次只改一个键，改完再跑一轮——同时改三个，出问题就分不清是谁造成的。
