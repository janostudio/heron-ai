# Roadmap

按能力分组，不绑定版本号——版本号会过期，能力清单不会。已完成的条目在代码里都有
对应实现，未完成的条目在括号里注明现状。

## 已完成

**编排**

- [x] 三层编排：`Flow → Team → Call → Agent` / `Command` / `Webhook`
- [x] 信号路由：`continue` / `wait_input` / `goal_achieved` / `goal_failed` / `goal_impossible`
- [x] Team 内并行 call 与 `depends_on` 依赖
- [x] 层间 `SharedRecord` 传递，含 `${records.*}` 模板与 flow 作用域副本
- [x] `per_task` 生命周期与嵌套深度限制
- [x] 每轮结束保持会话可续，失败只作用于当轮

**Agent 运行时**

- [x] 模型/工具回合循环，20 个内置工具（`Script` 为第 21 个，需显式开启）
- [x] Guardrail、Hooks、结构化输出、Handoff、HITL 审批
- [x] 上下文压缩与缓存锚定
- [x] 子 agent：`Spawn` / `Collect`，以及 Team 间派发与结果回流
- [x] Skill 渐进式披露（`SKILL.md` + `references/`）

**接入与运行**

- [x] 交互 TUI（bubbletea）
- [x] `--prompt` 单轮、`--session` 续聊、`--list` 会话选择
- [x] `--json-rpc`：stdin/stdout JSON-RPC，供 `heron-connect` 等外部调用
- [x] `--serve`：HTTP + SSE，含内置 Web UI
- [x] `stream-json` 客户端模式
- [x] `--check-config`：不建模型客户端的配置门禁

**工具与生态**

- [x] MCP（stdio + http）
- [x] 多媒体输入（图片 / 文件内容块）
- [x] 知识：`heron knowledge` 增量学习与 `gc` 归档
- [x] Team 分享包：`heron teams` 安装 / 卸载 / 导出，含依赖闭包与脚本复核
- [x] npm 分发（tag 触发，版本由 CI 注入）
- [x] 文档链接检查接入 CI

## 下一步

- [ ] **扩展运行时**：`internal/extension` 目前只有注册表。Lua / WASM 的执行器与
      能力沙箱均未实现，配置里写扩展不会生效
- [ ] **文档站点**：把 `docs/` 发布成可搜索的站点。当前入口是 [docs/INDEX.md](docs/INDEX.md)
- [ ] **eval 能力对外化**：`internal/eval` 已有关联 ID 与事实派生，但还没有 CLI/TUI 入口
- [ ] **Team 包版本与兼容性**：manifest 已声明 `version` 与 `apiVersion`，尚未做升级路径

## 远期

- [ ] 分布式执行：把 Team 调度扩展到多进程 / 多机
- [ ] 更多 provider 协议适配

## 不打算做

- 常驻数据库：状态与会话继续以文件为事实源，可读、可 diff、可重放是刻意选择
- 把编排逻辑写进代码：Flow/Team 保持声明式配置
