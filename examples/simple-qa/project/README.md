# Simple QA Project Fixture

这是 `simple-qa` 的本地测试项目，用于验证 Agent 的查询、写入和多步联动。

测试目标：

- 读取项目文件
- 搜索关键字
- 查找文件
- 基于 revision 修改文件
- 再次读取并确认修改结果
- 启动、查询和停止本地服务

服务测试命令：

```bash
sh project/start.sh
sh project/status.sh
sh project/stop.sh
```

服务启动后会输出 `SERVICE_READY`，并保持运行，便于 Agent 测试
通过 Bash 完成服务启动、状态查询、日志读取和停止。

HTTP 接口：

```text
GET /health
GET /greeting?name=QA
```
