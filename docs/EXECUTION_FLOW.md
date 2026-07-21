# Heron AI - 执行流程

## 1. 用户输入 → 主 Agent 调度

```
用户输入 "帮我写一篇关于 AI Agent 安全的博客"
  │
  ▼
全局主 Agent（状态机，引擎内置）
  │
  ├── State: understanding
  │   └── LLM 分析："用户要写博客，主题是 AI Agent 安全"
  │
  ├── State: planning
  │   └── LLM 决策："需要 blog_team，research + planner + writer 协作"
  │
  ├── State: dispatching
  │   └── dispatch → blog_team
  │
  ├── State: waiting
  │   └── blog_team 执行中...
  │
  ├── State: reviewing
  │   └── LLM 评估："结果满意，文章完整，可以交付"
  │
  └── State: delivering
      └── 输出最终博客，保存运行数据
```

## 2. Team 内部执行

```
全局主 Agent dispatch → blog_team
  │
  ▼
blog_team 主 Agent（引擎内置，每个 Team 自动创建）
  │
  ├── Stage 1: parallel
  │   ├── researcher: 搜索 "AI Agent 安全" 资料
  │   └── planner: 设计博客大纲
  │
  ├── Stage 2: sequential
  │   └── writer: 基于调研和大纲撰写博客
  │       └── writer 需要保存文件 → HITL 请求
  │           └── Team 主 Agent 判断：文件写入需要审批
  │               ├── 自己权限内 → 自动批准
  │               └── 需要用户 → 上浮给用户审批
  │
  └── 汇总结果 → 返回全局主 Agent
```

## 3. ReAct 循环

```
全局主 Agent 收到 Team 结果
  │
  ├── 满意 → delivering → 交付用户
  │
  ├── 不满意 → planning → 重新 dispatch
  │   └── "writer 写得不够专业，再 dispatch 一次 blog_team 重点优化语气"
  │
  └── 需要澄清 → 问用户
      └── "调研发现 AI 安全有多个方向，你关注哪方面？"
```

## 4. 执行中用户追加输入

```
blog_team 正在执行（writer 写作中）
  │
  │  用户看到: "[blog_team/writer] 正在撰写..."
  │
  ├── 用户输入 "加点案例"
  │   └── 路由 → blog_team 主 Agent
  │       └── Team 主 A 判断："追加指令给当前 writer，不中断"
  │           └── writer 收到 "加点案例"，继续写作
  │
  ├── 用户输入 "换个角度，从安全工程师视角写"
  │   └── 路由 → blog_team 主 Agent
  │       └── Team 主 A 判断："方向冲突，停掉当前 writer"
  │           └── 重新 dispatch writer 新任务
  │
  └── 用户 Ctrl+C
      └── 中断当前执行 → 回到全局主 Agent
          └── 用户输入 "算了，帮我查天气"
              └── 全局主 Agent 重新理解意图 → dispatch 新 Team
```

## 5. 并发执行

```
用户输入 "帮我审查这 3 个文件的代码"
  │
  ▼
全局主 Agent 识别：3 个文件，互不关联，可以并发
  │
  ├── dispatch → code_review_team（审查 auth.go）
  ├── dispatch → code_review_team（审查 model.go）  ← 并发
  └── dispatch → code_review_team（审查 handler.go） ← 并发
  
  │  3 个 Team 实例同时执行
  │
  ▼
全局主 Agent 等待全部完成 → 汇总 3 份报告 → ReAct 评估 → 交付
```

## 6. 状态流转总图

```
┌─────────────┐
│   用户输入   │
└──────┬──────┘
       ▼
┌─────────────────────────────────────────────┐
│              全局主 Agent                    │
│                                             │
│  init → understanding → planning → dispatching│
│                          ↑          ↓        │
│                          │     waiting       │
│                          │          ↓        │
│                          │     reviewing     │
│                          │       ↓   ↓       │
│                          └───────┘  delivering│
│                                      ↓       │
│                                     done     │
└─────────────────────────────────────────────┘
       │ dispatch              ▲ result
       ▼                       │
┌─────────────┐          ┌─────────────┐
│  Team 主 A  │   ...    │  Team 主 A  │
│             │          │             │
│  Worker A   │          │  Worker C   │
│  Worker B   │          │             │
└─────────────┘          └─────────────┘
```

## 7. 对比：当前 vs 未来

| 步骤 | 当前（Flow 编排） | 未来（主 Agent） |
|------|-----------------|-----------------|
| 1 | 用户写 Flow YAML | 用户直接对话 |
| 2 | 用户写 Team YAML | Team YAML 不变 |
| 3 | 用户写 Agent MD | Agent MD 不变 |
| 4 | `heron --flow xxx.yml` | `heron` 直接启动 |
| 5 | FlowEngine 按 Stage 顺序执行 | 主 A 动态决定 dispatch 谁 |
| 6 | `on_signal` 静态路由 | 主 A ReAct 动态决策 |
| 7 | 不支持执行中追加 | 执行中输入路由到 Team 主 A |
| 8 | 不支持并发多个 Team | 主 A 动态并发 dispatch |
