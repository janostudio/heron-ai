# 会话页面的浏览器回归用例

页面是 `internal/view/webassets/` 下三个文件，`go:embed` 进二进制，没有构建步骤。这意味着 `go test` 看不到它——渲染、token 门禁、输入框、实时事件流都只在浏览器里才存在。这个目录补的就是那一段。

不需要 heron 二进制，也不需要 `.agents` 目录：脚本自己起一个静态服务器供 `webassets/`，`/api/*` 由 fixtures 回答。**fixtures 全是合成数据**，不含任何真实会话内容。

## 跑

```bash
cd web-e2e
npm install
node run.mjs
```

`npm install` 会拉 `playwright`。若本机已有 chromium（`~/.cache/ms-playwright/chromium-*`），可以跳过下载并复用它：

```bash
PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install
CHROMIUM_PATH=~/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome node run.mjs
```

输出：逐条 `PASS` / `FAIL`，末尾给通过数；报告写到 `results/report.json`（`ran_at` + 每条断言的实际值与期望值）。退出码 0/1，可直接用于门禁。

## 覆盖

| 用例 | 断言的东西 |
|---|---|
| token gate | 服务端 401 时弹登录框；填 token 后进列表并落到 localStorage |
| loopback without token | 服务端不要求 token 时**不**弹登录框（回环默认模式） |
| new session | 列表页有新建入口、表单默认收起、提交后请求体为 `{input}`、跳到 `/s/<新 id>` 并渲染出该会话 |
| record: single agent | 发送者标签、正文取 `reply` 而非信封、原始输出折叠默认收起、每条消息时间、agent 耗时、回合耗时、工具行状态与输出、回合答复不重复 |
| record: multiple agents | 同一回合内 `default / coordinator`、`probe / terrain`、`facts / inspect`、`repo-info / inspect`、`analyze`（team 同名只显示一次） |
| composer | Enter 发送、请求体 `{input}`、粘附件生成 chip、请求体 `attachments[].data_base64` |
| live stream | 发送时出现 live 回合、工具行从「进行中」到「成功」并填入输出、team 派发行带 team 名、回合结束散去高亮并补耗时、agent 消息边跑边出现 |

`live stream` 的 SSE **不用 route mock**：mock 会把整个 body 一次吐出，页面在断言之前就跑完了，看不到任何中间态。所以本地 HTTP server 按 120ms 一帧真实地写，POST 也按住 2.2s 再返回，模拟同步接口的真实时序。

## 加用例

- 数据放 `fixtures/`：`sessions.json`（列表）、`record-*.json`（记录）、`stream.txt`（SSE 帧，`id:` + `data:` + 空行）
- 断言放 `run.mjs` 的 `run("名字", async () => { ... })` 块里，用 `check` / `checkEqual`
- 注意 `<details>` 折叠时 `innerText` 为空，读取里面的内容要用 `textContent`

## 没有接 CI

CI 现在只有 Go（build / test -race / lint）。接这套要在 workflow 里加 node + 浏览器，会让流水线多一到两分钟并多一个外部依赖，所以暂未接。要接的话：

```yaml
  web-e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
      - uses: actions/setup-node@v4
        with: { node-version: '20' }
      - run: npm ci
        working-directory: web-e2e
      - run: npx playwright install --with-deps chromium
        working-directory: web-e2e
      - run: node run.mjs
        working-directory: web-e2e
```
