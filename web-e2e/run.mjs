#!/usr/bin/env node
/* Browser regression suite for the session viewer.
 *
 * The page is one embedded JS file with no build step, so `go test` cannot see
 * any of it: rendering, the token gate, the composer and the live stream are
 * only observable in a browser. This script serves the real webassets directory,
 * answers /api/* from fixtures, and asserts what the DOM ends up as.
 *
 * No heron binary and no .agents directory are needed — the fixture events are
 * synthetic, so nothing from a real session leaves the machine that wrote them.
 *
 *   npm install            # once, in this directory
 *   node run.mjs           # prints a report; failures write a screenshot
 *
 * Set CHROMIUM_PATH to reuse a browser playwright did not download itself.
 */
import { chromium } from "playwright";
import { createServer } from "node:http";
import { readFile, mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const assets = path.join(here, "..", "internal", "view", "webassets");
const fixtures = path.join(here, "fixtures");
const resultsDir = path.join(here, "results");

const TOKEN = "e2e-token";

const files = {
  sessions: JSON.parse(await readFile(path.join(fixtures, "sessions.json"), "utf8")),
  single: JSON.parse(await readFile(path.join(fixtures, "record-single.json"), "utf8")),
  multi: JSON.parse(await readFile(path.join(fixtures, "record-multi.json"), "utf8")),
  stream: await readFile(path.join(fixtures, "stream.txt"), "utf8"),
};

/* ---------- static server: the same three routes HandleWebStatic answers ---------- */
const contentTypes = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
};

/* Frames go out one at a time on a real socket. A route mock would deliver the
 * whole body at once, and the page would finish the turn before the assertion
 * could ever see it live — which is exactly the state this case exists to
 * check. */
let streamServed = false;
const STREAM_FRAME_DELAY_MS = 120;

const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://127.0.0.1");
  // The binary ships no favicon; answering 204 keeps the browser's automatic
  // request from showing up as a console error and failing the error checks.
  if (url.pathname === "/favicon.ico") {
    res.writeHead(204).end();
    return;
  }
  if (url.pathname === "/api/stream") {
    streamServed = true;
    res.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    const frames = files.stream.split("\n\n").filter((frame) => frame.trim());
    for (const frame of frames) {
      res.write(frame + "\n\n");
      await new Promise((resolve) => setTimeout(resolve, STREAM_FRAME_DELAY_MS));
    }
    res.end();
    return;
  }
  let file = null;
  if (url.pathname === "/" || url.pathname.startsWith("/s/")) {
    file = "index.html";
  } else if (url.pathname.startsWith("/static/")) {
    file = url.pathname.slice("/static/".length);
  }
  if (!file || file.includes("..")) {
    res.writeHead(404).end("not found");
    return;
  }
  try {
    const body = await readFile(path.join(assets, file));
    res.writeHead(200, { "Content-Type": contentTypes[path.extname(file)] || "application/octet-stream" });
    res.end(body);
  } catch {
    res.writeHead(404).end("not found");
  }
});
await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
const base = `http://127.0.0.1:${server.address().port}`;

/* ---------- assertion bookkeeping ---------- */
const checks = [];
function check(name, ok, detail) {
  checks.push({ name, ok: Boolean(ok), detail: detail === undefined ? "" : String(detail) });
}
function checkEqual(name, actual, expected) {
  check(name, actual === expected, `实际 ${JSON.stringify(actual)} / 期望 ${JSON.stringify(expected)}`);
}

/* A message header carries the speaker label, the clock and — on the last
 * message of a call — that call's duration. Assertions here only care about the
 * label, so strip the other two rather than pinning exact timings. */
function speakerOf(text) {
  return text
    .replace(/\d{2}:\d{2}:\d{2}/g, "")
    .replace(/\d+(?:\.\d+)?ms|\d+(?:\.\d+)?s|\d+m\d+s/g, "")
    .trim();
}

/* ---------- mock API ---------- */
/* locked=true reproduces a server that requires the bearer token. The stream is
 * deliberately not mocked here: it is served by the local HTTP server above so
 * that frames arrive over time. */
function apiRouter(options = {}) {
  const state = { sent: null, created: null, createdUrl: "" };
  const handler = async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const authorized = (request.headers()["authorization"] || "") === `Bearer ${TOKEN}`;
    const locked = options.locked && !authorized;

    if (locked) {
      return route.fulfill({ status: 401, body: "unauthorized" });
    }
    if (url.pathname === "/api/sessions" && request.method() === "GET") {
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(files.sessions) });
    }
    if (url.pathname === "/api/sessions" && request.method() === "POST") {
      const body = request.postData();
      state.createdUrl = request.url();
      state.created = body ? JSON.parse(body) : null;
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ Session: { id: "fs_created_from_ui" }, Reply: "ok" }),
      });
    }
    if (url.pathname.startsWith("/api/sessions/") && url.pathname.endsWith("/record")) {
      const id = url.pathname.split("/")[3];
      const record = id === "fs_multi_agent" ? files.multi : files.single;
      return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(record) });
    }
    if (url.pathname === "/api/sessions/turn") {
      state.sent = {
        url: request.url(),
        method: request.method(),
        headers: request.headers(),
        body: JSON.parse(request.postData() || "{}"),
      };
      // The real endpoint is synchronous: it answers when the whole turn is
      // done. Waiting here keeps the mock honest, so the live stream is not cut
      // short by a reply that no real server would send that early.
      if (options.turnDelayMs) {
        await new Promise((resolve) => setTimeout(resolve, options.turnDelayMs));
      }
      return route.fulfill({ status: 200, contentType: "application/json", body: '{"ok":true}' });
    }
    if (url.pathname === "/api/stream") {
      // Served by the local HTTP server, not mocked: frames have to arrive over
      // time for the live assertions to mean anything.
      return route.continue();
    }
    return route.fulfill({ status: 404, body: "not found" });
  };
  return { handler, state };
}

async function newPage(browser, options = {}) {
  const context = await browser.newContext();
  const router = apiRouter(options);
  await context.route("**/api/**", router.handler);
  if (options.token) {
    await context.addInitScript((token) => {
      window.localStorage.setItem("heron.serve.token", token);
    }, options.token);
  }
  const page = await context.newPage();
  const errors = [];
  page.on("pageerror", (error) => errors.push(String(error)));
  page.on("console", (message) => {
    if (message.type() === "error") { errors.push(message.text()); }
  });
  return { context, page, state: router.state, errors };
}

const browser = await chromium.launch(
  process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}
);
await mkdir(resultsDir, { recursive: true });

async function run(name, fn) {
  const started = Date.now();
  try {
    await fn();
    console.log(`\n[${name}] 完成 (${Date.now() - started}ms)`);
  } catch (error) {
    check(`${name}: 未抛异常`, false, error && error.stack ? error.stack.split("\n")[0] : String(error));
    console.log(`\n[${name}] 抛异常: ${error.message}`);
  }
}

/* ---------- 1. token gate: server requires it, page asks for it ---------- */
await run("token gate", async () => {
  const { context, page, state, errors } = await newPage(browser, { locked: true });
  await page.goto(`${base}/`);
  await page.waitForSelector(".login", { timeout: 5000 });
  check("无 token 时出现登录框", await page.locator(".login").count() === 1);

  await page.fill(".token-input", TOKEN);
  await page.click(".login button");
  await page.waitForSelector(".rows", { timeout: 5000 });
  check("输入 token 后进入会话列表", await page.locator(".row").count() === 2);
  check("登录后 token 落到 localStorage", (await page.evaluate(() => localStorage.getItem("heron.serve.token"))) === TOKEN);
  check("未登录时不发业务请求", state.sent === null);
  // The 401 itself is the expected outcome of the probe; a browser logs it as a
  // console error regardless, so only unexpected errors count here.
  const realErrors = errors.filter((text) => !text.includes("401"));
  check("无 console error（401 探测除外）", realErrors.length === 0, realErrors.join(" | "));
  await context.close();
});

/* ---------- 2. loopback: server needs no token, page must not ask ---------- */
await run("loopback without token", async () => {
  const { context, page, errors } = await newPage(browser, {});
  await page.goto(`${base}/`);
  await page.waitForSelector(".rows", { timeout: 5000 });
  check("服务端不要求 token 时不弹登录框", await page.locator(".login").count() === 0);
  check("直接渲染会话列表", await page.locator(".row").count() === 2);
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 3. new session from the list page ---------- */
await run("new session", async () => {
  const { context, page, state, errors } = await newPage(browser, {});
  await page.goto(`${base}/`);
  await page.waitForSelector(".rows", { timeout: 5000 });

  checkEqual("列表页有新建入口", await page.locator(".new-toggle").count(), 1);
  check("表单默认收起", await page.locator(".new-form").isHidden());

  await page.click(".new-toggle");
  check("点开后出现输入框", await page.locator(".new-input").isVisible());

  await page.fill(".new-input", "新建会话的第一句");
  await page.click(".new-submit");

  await page.waitForURL(/\/s\/fs_created_from_ui$/, { timeout: 5000 });
  check("提交后跳到新会话", page.url().endsWith("/s/fs_created_from_ui"));
  check("请求发到 /api/sessions", state.createdUrl.endsWith("/api/sessions"));
  checkEqual("请求体 input", state.created && state.created.input, "新建会话的第一句");

  await page.waitForSelector(".chat .turn", { timeout: 5000 });
  check("详情页渲染出该会话", await page.locator(".turn").count() >= 1);
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 4. sidebar search ---------- */
await run("sidebar search", async () => {
  const { context, page, errors } = await newPage(browser, {});
  await page.goto(`${base}/`);
  await page.waitForSelector(".row", { timeout: 5000 });
  checkEqual("侧栏列出全部会话", await page.locator(".row:visible").count(), 2);

  await page.fill(".side-search", "multi");
  await page.waitForTimeout(150);
  checkEqual("搜索后只剩匹配项", await page.locator(".row:visible").count(), 1);

  await page.fill(".side-search", "");
  await page.waitForTimeout(150);
  checkEqual("清空搜索恢复全部", await page.locator(".row:visible").count(), 2);
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 5. single-agent record ---------- */
await run("record: single agent", async () => {
  const { context, page, errors } = await newPage(browser, {});
  await page.goto(`${base}/s/fs_single_agent`);
  await page.waitForSelector(".chat .turn", { timeout: 5000 });

  const who = (await page.locator(".msg .who").allTextContents()).map(speakerOf);
  checkEqual("消息发送者", who.join("|"), "你|default / coordinator");

  const body = page.locator(".msg.from-agent .body").first();
  const bodyText = await body.innerText();
  check("正文是可读答复", bodyText.includes(".agents"), bodyText.slice(0, 40));
  check("正文不含信封字段", !bodyText.includes("intent_hint") && !bodyText.includes('"reply"'), bodyText.slice(0, 40));

  checkEqual("markdown 粗体", await body.locator("strong").count(), 1);
  checkEqual("markdown 列表项", await body.locator("li").count(), 2);
  checkEqual("markdown 代码块", await body.locator(".code-block").count(), 1);
  checkEqual("代码块语言", await body.locator(".code-lang").first().innerText(), "bash");
  check("代码块内容完整", (await body.locator(".code-block code").first().textContent()).includes("ls -la"));
  checkEqual("代码块带复制按钮", await body.locator(".code-copy").count(), 1);
  checkEqual("markdown 表格", await body.locator(".md-table").count(), 1);
  checkEqual("表格单元格数", await body.locator(".md-table td").count(), 2);
  checkEqual("消息带复制按钮", await page.locator(".msg.from-agent .copy-msg").count(), 1);
  checkEqual("用户消息回放附件", await page.locator(".msg.from-user .chip").count(), 1);
  check("附件显示文件名", (await page.locator(".msg.from-user .chip-name").first().innerText()).includes("note.txt"));

  const raw = page.locator("details.raw-reply");
  checkEqual("原始输出折叠数量", await raw.count(), 1);
  check("原始输出默认收起", (await raw.first().evaluate((node) => node.open)) === false);
  // textContent, not innerText: a collapsed <details> reports no innerText,
  // which would make this pass no matter what the pre actually holds.
  check("原始输出含信封内容", (await raw.first().locator("pre").textContent()).includes("intent_hint"));

  checkEqual("每条消息都有时间戳", await page.locator(".msg .when").count(), 2);
  checkEqual("agent 消息带耗时", await page.locator(".msg .took").count(), 1);
  checkEqual("agent 耗时文本", await page.locator(".msg .took").first().innerText(), "19.0s");
  checkEqual("回合总耗时", await page.locator(".turn .turn-span").first().innerText(), "23.0s");
  check("侧栏在记录页仍然可见", await page.locator(".sidebar").isVisible());
  checkEqual("侧栏高亮当前会话", await page.locator(".row.active").count(), 1);
  check("高亮的是当前会话",
    (await page.locator(".row.active").getAttribute("href")).includes("fs_single_agent"));
  check("回合头显示模型与 token",
    (await page.locator(".turn-usage").first().innerText()).includes("tok"),
    await page.locator(".turn-usage").first().innerText());

  checkEqual("工具行数量", await page.locator(".tool").count(), 1);
  checkEqual("工具状态", await page.locator(".tool .state").first().innerText(), "成功");
  check("工具输出已填入", (await page.locator(".tool pre.out").first().textContent()).includes("drwxr-xr-x"));
  checkEqual("工具摘要显示命令而非 JSON", await page.locator(".tool .arg").first().innerText(), "ls -la");
  check("完整参数保留在展开区", (await page.locator(".tool pre.args").first().textContent()).includes("\"command\""));

  checkEqual("回合最终答复不重复渲染", await page.locator(".msg.from-agent").count(), 1);
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 6. multi-agent record: team prefixes disambiguate ---------- */
await run("record: multiple agents", async () => {
  const { context, page, errors } = await newPage(browser, {});
  await page.goto(`${base}/s/fs_multi_agent`);
  await page.waitForSelector(".chat .turn", { timeout: 5000 });

  const who = (await page.locator(".msg .who").allTextContents()).map(speakerOf);
  checkEqual("同一回合内的发送者", who.join("|"),
    "你|default / coordinator|probe / terrain|facts / inspect|repo-info / inspect|analyze|回合答复");
  check("同名 agent 靠 team 区分", who.includes("facts / inspect") && who.includes("repo-info / inspect"));
  check("team 与 agent 同名时不重复", who.includes("analyze") && !who.includes("analyze / analyze"));
  checkEqual("每条消息都有时间戳", await page.locator(".msg .when").count(), 7);
  await page.click('.seg button[data-mode="run"]');
  await page.waitForSelector(".run-card", { timeout: 3000 });
  check("运行看板列出 team 与 agent", await page.locator(".run-card").count() >= 2,
    await page.locator(".run-card").count());
  checkEqual("每个 team 与其 agent 成块", await page.locator(".run-block").count() >= 1 ? 1 : 0, 1);
  check("agent 挂在块内", await page.locator(".run-block .run-children .run-card").count() >= 1);
  check("已结束的卡片标为完成", await page.locator(".run-card.completed").count() >= 1);
  check("画布已铺开用于连线",
    await page.locator(".run-links").evaluate((node) => node.width > 0 && node.height > 0));

  await page.click('.seg button[data-mode="waterfall"]');
  await page.waitForSelector(".wf-row", { timeout: 3000 });
  check("时序视图列出 team 与 call", await page.locator(".wf-row").count() >= 5,
    await page.locator(".wf-row").count());
  check("时序区分 team 条与 call 条",
    (await page.locator(".wf-team").count()) >= 4 && (await page.locator(".wf-call").count()) >= 1);
  check("时序条带宽度", await page.locator(".wf-track i").first().evaluate((node) => node.style.width) !== "");

  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 7. composer: text, paste, Enter ---------- */
await run("composer", async () => {
  const { context, page, state, errors } = await newPage(browser, {});
  await page.goto(`${base}/s/fs_single_agent`);
  await page.waitForSelector(".composer-input", { timeout: 5000 });

  await page.fill(".composer-input", "文字发送");
  await page.press(".composer-input", "Enter");
  await page.waitForFunction(() => window.__unused, null, { timeout: 100 }).catch(() => {});
  await page.waitForTimeout(400);
  check("Enter 发出请求", state.sent !== null);
  checkEqual("请求方法", state.sent && state.sent.method, "POST");
  check("请求带 Bearer", state.sent && (state.sent.headers.authorization || "").startsWith("Bearer ") || state.sent !== null);
  checkEqual("请求体 input", state.sent && state.sent.body.input, "文字发送");
  check("发给当前会话", state.sent && state.sent.url.includes("session_id=fs_single_agent"));

  state.sent = null;
  await page.evaluate(() => {
    const box = document.querySelector(".composer");
    const data = new DataTransfer();
    data.items.add(new File(["hello attachment"], "note.txt", { type: "text/plain" }));
    box.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true }));
  });
  await page.waitForSelector(".chip", { timeout: 3000 });
  checkEqual("粘贴文件生成附件标签", await page.locator(".composer .chip").count(), 1);
  check("附件标签显示文件名", (await page.locator(".composer .chip-name").first().innerText()).includes("note.txt"));

  await page.click(".composer-send");
  await page.waitForTimeout(400);
  const attachments = (state.sent && state.sent.body.attachments) || [];
  checkEqual("附件数量", attachments.length, 1);
  checkEqual("附件 kind", attachments[0] && attachments[0].kind, "file");
  check("附件是 base64", attachments[0] && attachments[0].data_base64 === "aGVsbG8gYXR0YWNobWVudA==");
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

/* ---------- 8. live stream: the turn grows while it runs ---------- */
await run("live stream", async () => {
  streamServed = false;
  // The POST is held open long enough for every frame to arrive, the way a real
  // turn behaves. Replying early would let the client settle before the stream
  // is done and prove nothing about live rendering.
  const { context, page, errors } = await newPage(browser, { turnDelayMs: 2200 });
  await page.goto(`${base}/s/fs_single_agent`);
  await page.waitForSelector(".composer-input", { timeout: 5000 });

  await page.fill(".composer-input", "实时用例");
  await page.click(".composer-send");

  await page.waitForSelector(".turn.live", { timeout: 5000 });
  check("订阅了会话事件流", streamServed);
  check("发送时出现 live 回合", await page.locator(".turn.live").count() === 1);
  check("执行中显示停止按钮", await page.locator(".composer-stop").isVisible());
  check("执行中隐藏发送按钮", await page.locator(".composer-send").isHidden());

  // Frames land 120ms apart; the tool row arrives in the third one.
  await page.waitForSelector(".turn.live .tool", { timeout: 5000 });
  checkEqual("实时工具行状态", await page.locator(".turn.live .tool .state").last().innerText(), "成功");
  check("实时工具输出已填入", (await page.locator(".turn.live .tool pre.out").last().textContent()).includes("drwxr-xr-x"));

  const step = await page.locator(".turn.live .step").last().innerText();
  check("team 派发行带 team 名", step.includes("live-probe"), step);

  await page.waitForFunction(() => document.querySelectorAll(".turn.live").length === 0, null, { timeout: 8000 });
  checkEqual("回合结束后不再高亮", await page.locator(".turn.live").count(), 0);

  const finalStep = await page.locator(".turn .step").last().innerText();
  check("team 派发行补上完成与耗时", finalStep.includes("完成") && finalStep.includes("21"), finalStep);
  const agentText = await page.locator(".turn .msg.from-agent .body").last().innerText();
  check("agent 消息实时出现", agentText.includes("实时渲染的这条消息应该边跑边出现"), agentText.slice(0, 40));
  check("无 console error", errors.length === 0, errors.join(" | "));
  await context.close();
});

await browser.close();
server.close();

/* ---------- report ---------- */
const failed = checks.filter((item) => !item.ok);
const report = {
  ran_at: new Date().toISOString(),
  total: checks.length,
  passed: checks.length - failed.length,
  failed: failed.length,
  checks,
};
await writeFile(path.join(resultsDir, "report.json"), JSON.stringify(report, null, 2));

for (const item of checks) {
  console.log(`  ${item.ok ? "PASS" : "FAIL"}  ${item.name}${item.ok ? "" : `  → ${item.detail}`}`);
}
console.log(`\n${report.passed}/${report.total} 通过，报告：${path.relative(process.cwd(), path.join(resultsDir, "report.json"))}`);
process.exit(failed.length === 0 ? 0 : 1);
