# Issue 索引

本目录统一维护 heron-ai 已发现的问题。**所有 issue 文档都在这里**，
不再散放在 `docs/` 根目录。

命名约定：`ISSUE-<简短英文描述>.zh-CN.md`。

| 文档 | 现象 | 状态 |
|---|---|---|
| [bash-timeout-grandchild-hang](./ISSUE-bash-timeout-grandchild-hang.zh-CN.md) | **Bash 超时后不杀孙进程，agent 永久卡死**（含[复现脚本](./repro-bash-timeout-grandchild.go)） | **已修复** |
| [tui-exit-no-resume-hint](./ISSUE-tui-exit-no-resume-hint.zh-CN.md) | TUI 退出只显示 `Goodbye!`，不告知如何用 `--session` 续聊 | **已修复** |
| [skill-symlink](./ISSUE-skill-symlink.zh-CN.md) | Skill 目录不支持符号链接（软链 skill 加载时静默跳过 + 工具访问越界） | **问题 1 已修复**；问题 2 有意设计 |
| [structured-retry-no-raw-output](./ISSUE-structured-retry-no-raw-output.zh-CN.md) | 结构化重试丢弃的模型原始输出不可回溯 | **已修复** |
| [acquire-silent-slot-occupation](./ISSUE-acquire-silent-slot-occupation.zh-CN.md) | `acquire.sh` 无参调用会静默占用真实 worktree 槽位 | **已修复** |
| [cli-session-list-command](./ISSUE-cli-session-list-command.zh-CN.md) | `heron --list` 列出会话并回车进入；附带 CLI 参数风格统一 | **已实现** |
| [tui-long-text-truncation](./ISSUE-tui-long-text-truncation.zh-CN.md) | TUI 中较长文本显示不全（viewport v1.0.0 不换行而是硬切）；含"无法上下滚动"修复 | **已修复** |
| [tui-message-distinction](./ISSUE-tui-message-distinction.zh-CN.md) | TUI 用户消息与机器人消息难以区分 | **已修复** |
| [command-call-no-params](./ISSUE-command-call-no-params.zh-CN.md) | **command call 无法接收参数，用户指定的仓库被静默忽略**（7/10 个 command 完全无参） | **引擎侧已修复**（配置需自行接线） |
| [tui-text-not-selectable](./ISSUE-tui-text-not-selectable.zh-CN.md) | TUI 文字无法划取复制（AltScreen + 鼠标追踪导致） | **已按方案 A 处理**（保留捕获 + 提示 Shift/⌥ 拖选） |
| [tui-welcome-banner-misaligned](./ISSUE-tui-welcome-banner-misaligned.zh-CN.md) | 欢迎页 ASCII 边框错位 3 列，整体观感简陋 | **框线已修复**（改用 lipgloss 自动绘框） |
| [tui-conversation-display-research](./ISSUE-tui-conversation-display-research.zh-CN.md) | 调研：看不出哪个 team/agent 在回复；工具输出糊正文；信息密度 | **全部完成**（身份头 / 长串断行 / 工具摘要 / 状态栏 / 轮次分隔） |
| [tui-resume-tokens-zero](./ISSUE-tui-resume-tokens-zero.zh-CN.md) | resume 会话后 TUI 显示 `Tokens: 0`（历史 usage 未恢复） | **已修复** |
| [tui-arrow-keys-input-history](./ISSUE-tui-arrow-keys-input-history.zh-CN.md) | ↑/↓ 应为输入历史回填（主流 REPL 惯例） | **已实现**（Alt+↑/↓ 滚动） |
| [tui-default-message-folding](./ISSUE-tui-default-message-folding.zh-CN.md) | 长消息默认折叠到 12 行、须 `Ctrl+O` 展开（未经要求新增的功能） | **已修复**（移除折叠） |
| [tui-resume-identity-and-round-count](./ISSUE-tui-resume-identity-and-round-count.zh-CN.md) | resume 后消息丢失 team/agent 身份；`round` 按模型回复递增（应为用户输入） | **已修复** |
| [view-package-build-broken](./ISSUE-view-package-build-broken.zh-CN.md) | **`internal/view` + `internal/sessionlist` 编译失败，两包测试全部无法运行**（重构后测试未同步） | **已修复** |
| [tui-no-recovery-entry-deadlock](./ISSUE-tui-no-recovery-entry-deadlock.zh-CN.md) | 卡住的会话在 TUI 报"use recovery status first"，但 TUI/CLI 都没有 recovery 入口（死锁） | **从根因修复**（工具调用统一兜底超时，卡死不再发生） |
| [tui-team-duplicate-and-history-no-log](./ISSUE-tui-team-duplicate-and-history-no-log.zh-CN.md) | team 名重复展示；输入历史 Ctrl+C 重开就丢（未持久化） | **两个 bug 均已修复**（team 标记去重 + 历史落盘） |
| [structured-output-forces-single-line](./ISSUE-structured-output-forces-single-line.zh-CN.md) | `structured_output` agent 的回复永远是单行（无分段） | **已修复**（根因更正：非 response_format，是提示缺"允许换行"；引擎 contract 已加） |
| [replay-missing-tool-call-failed](./ISSUE-replay-missing-tool-call-failed.zh-CN.md) | 同一 call 第二次执行时 400 `tool calls and tool results do not match`；`replay.go` 漏处理 `tool_call.failed` | **未复现**（对照组均 200，因果未证实） |
| [debug-log-level-no-observability](./ISSUE-debug-log-level-no-observability.zh-CN.md) | **`--log-level debug` 几乎无效**：全引擎仅 1 处 debug 日志；从不记录请求/响应体，协议类错误无法排查 | **未修复** |

## 状态含义

| 状态 | 含义 |
|---|---|
| **已修复** | 已改代码并补测试，改动已通过验证 |
| **未修复** | 根因已定位到具体代码，修复方案已列出，但当前不实施 |
| **未实现** | 需求已记录，尚未开始实现 |
| **未复现，待补证据** | 现象已记录，但最小复现未能重现；文档含已排除项与待补材料清单 |

## 每份 issue 的固定结构

为便于后续接手，每份文档都包含：

1. **现象** —— 用户看到什么（含截图描述或原始数据）
2. **根因** —— 定位到的代码位置（未复现的则写"已排除项"）
3. **影响面** —— 什么场景会触发
4. **复现方式** —— 可执行的命令或脚本
5. **可选修复方向** —— 含代价对比，**均标注是否已实施**
6. **一句话总结**

## 写 issue 的原则

- 只记录**不合理/缺陷**；合理的设计（哪怕反直觉）写进 skill 文档，不进 issue。
- **先复现再断言**。未复现的必须显式标注"未复现"，不得写成已定位的根因。
- 修复方向可以留空（"暂不修复"是合法状态），但要写清为什么暂不修
  （如频率低、影响面小、需要引擎改动等）。
