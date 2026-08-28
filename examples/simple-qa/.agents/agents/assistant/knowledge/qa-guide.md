---
id: qa-guide
title: Simple QA 项目测试指南
keys:
  - project
  - greeting
  - service
  - test
  - memory
  - knowledge
scope:
  type: agents
  agents:
    - assistant
status: active
---

Simple QA 项目测试规范：

1. 查询文件时优先使用 Read、Grep、Glob，并先确认目标文件和当前内容。
2. 修改文件前必须先 Read 获取 revision；修改时使用该 revision，避免覆盖并发修改。
3. 修改后必须再次 Read 验证实际内容，并使用 Bash 运行项目测试。
4. 服务必须通过 `sh project/start.sh` 启动，通过 `sh project/status.sh` 检查，
   从 `SERVICE_READY` 日志读取动态地址后调用 `/health` 和 `/greeting`。
5. 测试结束必须通过 `sh project/stop.sh` 停止服务，并确认输出 `SERVICE_STOPPED`。
6. Team Memory 和 Agent Memory 只记录短期、已确认的事实；完整过程以 Session 事件为准。
