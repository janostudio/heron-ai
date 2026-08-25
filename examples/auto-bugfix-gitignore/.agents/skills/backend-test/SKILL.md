---
name: backend-test
description: "确定性验证命令的执行和结果边界"
allowed-tools: Read,Grep,Glob
---

# 确定性测试

这是 auto_bugfix backend-test 的本地化适配版本。
本示例没有 genie、sandbox-proxy 或 Node 后端，不调用原仓库测试入口。

真正的验证由 Team 中的 `command` 成员执行：

```bash
bash .agents/scripts/verify_gitignore.sh
bash .agents/scripts/check_project_scope.sh
```

Agent 只解释 `VerificationReport` 和 `GitStatusReport`，不模拟命令结果，
也不通过修改验证脚本来让测试通过。
