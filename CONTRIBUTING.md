# 贡献指南

感谢你愿意花时间。这份文档只写"怎么让改动被顺利合入"，不重复
[docs/](docs/INDEX.md) 里的用法说明。

## 环境

- Go 1.26 或更新（版本以 `go.mod` 为准）
- [golangci-lint](https://golangci-lint.run/) v2.13.x（配置在 `.golangci.yml`，版本刻意固定）
- 可选：Node.js 18+，仅在改动 `npm/` 或 Web UI e2e（`web-e2e/`）时需要

## 本地验证

提交前按顺序跑，三条都过再开 PR：

```bash
go build ./...
go test ./...
golangci-lint run
```

改到文档时多跑一条：

```bash
python3 scripts/check-docs-links.py --verbose
```

它会检查所有 Markdown 的相对链接是否指向真实文件。CI 的 `Docs links` 步骤跑的是同一个
脚本——改了文件名却没改引用，会直接构建失败。

跑单个包或单个用例：

```bash
go test ./internal/config/ -run TestLoadMCPServers -v
go test ./examples/          # 四个示例配置的结构断言
```

## 改动应该带什么

| 改动类型 | 期望 |
|---|---|
| 修 Bug | 一个能复现的测试用例，先红后绿。仓库里 `docs/issues/` 下的问题单是这类先例 |
| 加功能 | 配置字段要同时更新 `docs/configuration/`；新增的对外行为要能被 `--check-config` 校验到 |
| 新增内置工具 | 除在 `internal/app` 注册外，**必须**同步 `internal/agent/runtime.go` 的 `builtinSchemas`，否则工具对模型不可见且没有任何报错 |
| 改文档 | 新增的 `docs/` 根目录文档要进 `docs/INDEX.md`；不生效的配置项要明确标注，而不是留着让人以为能调 |
| 改示例 | 保证 `go test ./examples/` 通过，并让 `heron --check-config` 在示例目录下通过 |

`builtinSchemas` 与工具自身 `Parameters()` 是两份重复声明，必须保持一致；仓库里有测试
守住其中的关键条目。

## 提交信息

沿用仓库历史的 Conventional Commits 风格，`scope` 用受影响的包或目录：

```
fix(flow): 中间回合不再发布正文
feat(teams): 分享包声明运行模式与 team 依赖
docs(CLI): HTTP 端点表与 TUI 快捷键对齐实现
```

正文写"为什么"而不是"改了什么"——改了什么看 diff 就知道。一次提交只做一件事，
把格式化、重命名和逻辑改动混在一起会让 review 变慢。

## 分支与 PR

- 分支：`feat/<topic>` 或 `fix/<topic>`
- PR 目标分支：`master`
- 填完 `.github/PULL_REQUEST_TEMPLATE.md` 的 Checklist（构建、测试、lint、文档）
- 关联 Issue 用 `Closes #<n>`

PR 描述里值得写清的三件事：问题是什么、为什么这么修、怎么验证的。如果有取舍，把被放弃
的方案和原因也写上——reviewer 最常问的就是"为什么不那样做"。

## 文档改动特别注意

文档里的每条命令都应该真的能跑。描述行为时以源码为准，不要以"应该如此"为准：写之前
用 `--help`、`--check-config` 或直接读实现确认一遍，比写完再被读者发现要省事。

一条经验：配置字段"写了没生效"比"写了报错"危害大得多。如果发现某个字段没有任何读取点，
正确做法是在文档里标明它不生效（参考 `docs/configuration/settings.md` 的 Inert sections），
而不是把它描述成可用选项。

## 行为准则

讨论对事不对人。技术分歧用可复现的证据解决：日志、最小复现、测试用例。
