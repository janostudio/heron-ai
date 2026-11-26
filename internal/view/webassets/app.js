/* heron-ai session viewer.
 *
 * Read-only: it fetches /api/sessions and /api/sessions/:id/record and renders
 * them three ways. There is no framework and no build step; the whole page is
 * one file so a change here is one file in review.
 *
 * Everything that reaches the DOM goes through el()/text(). innerHTML is never
 * used: the content includes model output, and this page sits in front of an
 * agent that can run commands.
 */
(function () {
  "use strict";

  var app = document.getElementById("app");

  /* The session page pins its header, tab strip and composer; only the record
   * pane scrolls. Every "jump to the newest" therefore moves that pane, never
   * the window. */
  function recordPane() { return document.getElementById("record-body"); }

  function scrollRecordToBottom() {
    var pane = recordPane();
    if (pane) { pane.scrollTop = pane.scrollHeight; return; }
    window.scrollTo(0, document.body.scrollHeight);
  }

  function setRecordView(inRecord) {
    app.className = inRecord ? "app record-view" : "app";
  }
  var sidebar = document.getElementById("sidebar");
  var shell = document.querySelector(".shell");
  var currentRecord = null;
  var currentMode = "chat";

  /* ---------- DOM helpers ---------- */
  function text(value) {
    return document.createTextNode(value === null || value === undefined ? "" : String(value));
  }

  function el(tag, attrs) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (key) {
        var value = attrs[key];
        if (value === null || value === undefined || value === false) { return; }
        if (key === "class") { node.className = value; return; }
        if (key === "dataset") {
          Object.keys(value).forEach(function (name) { node.dataset[name] = value[name]; });
          return;
        }
        node.setAttribute(key, value === true ? "" : value);
      });
    }
    for (var i = 2; i < arguments.length; i++) {
      appendChild(node, arguments[i]);
    }
    return node;
  }

  function appendChild(node, child) {
    if (child === null || child === undefined || child === false) { return; }
    if (Array.isArray(child)) {
      child.forEach(function (item) { appendChild(node, item); });
      return;
    }
    node.appendChild(typeof child === "string" || typeof child === "number" ? text(child) : child);
  }

  function clear(node) {
    while (node.firstChild) { node.removeChild(node.firstChild); }
  }

  function link(href, label) {
    return el("a", { href: href, "data-link": "1" }, label);
  }

  function tag(label, kind, title) {
    return el("span", { class: "tag" + (kind ? " " + kind : ""), title: title || "" }, label);
  }

  /* ---------- formatting ---------- */
  function shortId(id) {
    if (!id) { return ""; }
    return id.length > 14 ? id.slice(0, 13) + "\u2026" : id;
  }

  function relativeTime(iso) {
    if (!iso) { return ""; }
    var then = new Date(iso).getTime();
    if (isNaN(then)) { return ""; }
    var seconds = Math.round((Date.now() - then) / 1000);
    if (seconds < 60) { return "\u521a\u521a"; }
    if (seconds < 3600) { return Math.round(seconds / 60) + " \u5206\u949f\u524d"; }
    if (seconds < 86400) { return Math.round(seconds / 3600) + " \u5c0f\u65f6\u524d"; }
    return Math.round(seconds / 86400) + " \u5929\u524d";
  }

  function clock(iso) {
    if (!iso) { return ""; }
    var d = new Date(iso);
    if (isNaN(d.getTime())) { return ""; }
    return d.toTimeString().slice(0, 8);
  }

  /* Wall-clock gap between two timestamps. Minutes stay readable with the
     seconds attached: a probe that ran 8m33s should not read as "8m". */
  function elapsed(startISO, endISO) {
    var start = new Date(startISO).getTime();
    var end = new Date(endISO).getTime();
    if (isNaN(start) || isNaN(end) || end < start) { return ""; }
    var ms = end - start;
    if (ms < 1000) { return ms + "ms"; }
    if (ms < 60000) { return (ms / 1000).toFixed(1) + "s"; }
    var minutes = Math.floor(ms / 60000);
    var seconds = Math.round((ms % 60000) / 1000);
    return minutes + "m" + (seconds ? seconds + "s" : "");
  }

  /* Timestamp chip that goes on a message header. The clock alone is what the
     eye needs; the full timestamp (with date) stays in the tooltip so a
     transcript that spans days is still decidable. */
  function stamp(iso) {
    if (!iso) { return null; }
    var text = clock(iso);
    if (!text) { return null; }
    var when = new Date(iso);
    var title = isNaN(when.getTime()) ? iso : when.toLocaleString();
    return el("span", { class: "when", title: title }, text);
  }

  function duration(node) {
    if (!node.started_at) { return ""; }
    var start = new Date(node.started_at).getTime();
    var end = node.finished_at ? new Date(node.finished_at).getTime() : Date.now();
    if (isNaN(start) || isNaN(end) || end < start) { return ""; }
    var ms = end - start;
    if (ms < 1000) { return ms + "ms"; }
    if (ms < 60000) { return (ms / 1000).toFixed(1) + "s"; }
    return Math.round(ms / 60000) + "m";
  }

  /* A turn that never emitted session.turn.started (a dispatched Team runs as
   * its own flow turn) has no timestamps on its node; the events it owns do, so
   * the span is taken from them. */
  function nodeSpanDuration(node, record) {
    if (node.started_at) { return duration(node); }
    var start = 0;
    var end = 0;
    (record.events || []).forEach(function (event) {
      if (!event.created_at) { return; }
      if (event.flow_turn_id !== node.id && event.team_turn_id !== node.id &&
          event.call_turn_id !== node.id) { return; }
      var at = new Date(event.created_at).getTime();
      if (isNaN(at)) { return; }
      if (!start || at < start) { start = at; }
      if (!end || at > end) { end = at; }
    });
    if (!start || !end || end <= start) { return duration(node); }
    return elapsed(new Date(start).toISOString(), new Date(end).toISOString()) || "";
  }

  function tokenCount(value) {
    if (!value) { return "0"; }
    return value >= 1000 ? (value / 1000).toFixed(1) + "k" : String(value);
  }

  // cacheRate 把命中量换成一个百分比。分母优先用 miss（网关亲口说"这部分
  // 是现读的"），没有 miss 时退回输入总量；两个都没有就返回空，让调用方
  // 显示 "—"，不要用 0% 冒充"没命中"。
  function cacheRate(hit, miss, input) {
    hit = hit || 0;
    var total = (miss || 0) > 0 ? hit + miss : (input || 0);
    if (!hit && !total) { return ""; }
    if (!total) { return ""; }
    return (hit / total * 100).toFixed(1) + "%";
  }

  // cacheKpi 是 KPI 里那一格：命中率 + 命中量。百分比是判断"前缀有没有被复用"
  // 的唯一直接信号，命中量说明这个百分比有没有分量（1 token 的 100% 没意义）。
  function cacheKpi(facts) {
    var rate = cacheRate(facts.cache_hit_tokens, facts.cache_miss_tokens, facts.input_tokens);
    if (!rate) { return "\u2014"; }
    return rate + " \u00b7 " + tokenCount(facts.cache_hit_tokens);
  }

  function statusDot(status) {
    var kind = "dot";
    if (status === "completed") { kind += " ok"; }
    else if (status === "failed") { kind += " bad"; }
    else if (status === "running") { kind += " warn"; }
    else if (status && status.indexOf("waiting") === 0) { kind += " warn"; }
    return el("span", { class: kind });
  }

  function eventClass(event) {
    var type = (event && event.type) || "";
    var turn = (event && event.payload && event.payload.turn) || {};
    if (type === "session.turn.started") { return "mm-user"; }
    if (type === "session.tool_call.started" || type === "session.tool_call.completed" || type === "session.tool_call.failed") { return "mm-tool"; }
    if (type === "session.context.compacted") { return "mm-compact"; }
    if (type.indexOf("session.wakeup.") === 0) { return "mm-wakeup"; }
    // A failed turn reports `turn.status`, not a "failed" event name.
    if (turn.status === "failed" || type === "session.recovery.requested") { return "mm-error"; }
    return "mm-step";
  }

  function eventSummary(event) {
    var payload = event.payload || {};
    if (payload.tool_name) {
      var args = payload.arguments ? JSON.stringify(payload.arguments) : "";
      return payload.tool_name + (args ? " " + args : "") + (payload.error ? " \u00b7 " + payload.error : "");
    }
    if (payload.limit) { return "\u9650\u5236\u89e6\u53d1\uff1a" + payload.limit.name + " " + payload.limit.value; }
    if (payload.text) { return payload.text; }
    if (payload.reply) { return payload.reply; }
    if (payload.input) { return payload.input; }
    if (payload.error) { return payload.error; }
    if (payload.reason) { return payload.reason; }
    return "";
  }

  /* ---------- markdown ---------- */
  /* A deliberately small renderer: headings, lists, fenced code, quotes, tables,
   * rules, and inline code/bold/italic/strikethrough/links. Enough for what
   * models actually emit, small enough to read in review.
   *
   * It builds DOM nodes and never touches innerHTML. The input is model output,
   * and the model sits in front of an agent that can run commands — HTML in the
   * text is shown as text, not parsed.
   */
  function renderMarkdown(source) {
    var root = el("div", { class: "md" });
    var lines = String(source === null || source === undefined ? "" : source)
      .replace(/\r\n?/g, "\n")
      .split("\n");
    var i = 0;

    while (i < lines.length) {
      var line = lines[i];

      if (/^```/.test(line)) {
        var lang = line.slice(3).trim();
        var code = [];
        i += 1;
        while (i < lines.length && !/^```/.test(lines[i])) {
          code.push(lines[i]);
          i += 1;
        }
        i += 1; // closing fence (a missing one just ends the block)
        root.appendChild(codeBlock(code.join("\n"), lang));
        continue;
      }

      var heading = line.match(/^(#{1,6})\s+(.*)$/);
      if (heading) {
        root.appendChild(el("h" + heading[1].length, null, inlineNodes(heading[2])));
        i += 1;
        continue;
      }

      if (/^\s*([-*_])\s*\1\s*\1[\s\S]*$/.test(line) && line.replace(/[\s\-*_]/g, "") === "") {
        root.appendChild(el("hr"));
        i += 1;
        continue;
      }

      if (/^>\s?/.test(line)) {
        var quoted = [];
        while (i < lines.length && /^>\s?/.test(lines[i])) {
          quoted.push(lines[i].replace(/^>\s?/, ""));
          i += 1;
        }
        root.appendChild(el("blockquote", null, renderMarkdown(quoted.join("\n"))));
        continue;
      }

      if (line.indexOf("|") >= 0 && i + 1 < lines.length && isTableDivider(lines[i + 1])) {
        var header = splitRow(line);
        i += 2;
        var rows = [];
        while (i < lines.length && lines[i].indexOf("|") >= 0 && lines[i].trim()) {
          rows.push(splitRow(lines[i]));
          i += 1;
        }
        root.appendChild(tableNode(header, rows));
        continue;
      }

      if (/^\s*([-*+]|\d+[.)])\s+/.test(line)) {
        var ordered = /^\s*\d+[.)]\s+/.test(line);
        var list = el(ordered ? "ol" : "ul");
        while (i < lines.length && /^\s*([-*+]|\d+[.)])\s+/.test(lines[i])) {
          var item = lines[i].replace(/^\s*([-*+]|\d+[.)])\s+/, "");
          list.appendChild(el("li", null, inlineNodes(item)));
          i += 1;
        }
        root.appendChild(list);
        continue;
      }

      if (!line.trim()) {
        i += 1;
        continue;
      }

      // Paragraph: consecutive plain lines until a blank line or a block start.
      var paragraph = [line];
      i += 1;
      while (i < lines.length && lines[i].trim() && !isBlockStart(lines[i])) {
        paragraph.push(lines[i]);
        i += 1;
      }
      root.appendChild(el("p", null, inlineNodes(paragraph.join("\n"))));
    }

    return root;
  }

  function isBlockStart(line) {
    return /^```/.test(line) || /^#{1,6}\s/.test(line) || /^>\s?/.test(line) ||
      /^\s*([-*+]|\d+[.)])\s+/.test(line) || line.indexOf("|") >= 0;
  }

  function isTableDivider(line) {
    return /^\s*\|?[\s:|-]+\|[\s:|-]*$/.test(line) && line.indexOf("-") >= 0;
  }

  function splitRow(line) {
    return line.replace(/^\s*\|/, "").replace(/\|\s*$/, "").split("|").map(function (cell) {
      return cell.trim();
    });
  }

  function tableNode(header, rows) {
    var table = el("table", { class: "md-table" });
    var head = el("thead", null, el("tr", null));
    header.forEach(function (cell) {
      head.firstChild.appendChild(el("th", null, inlineNodes(cell)));
    });
    table.appendChild(head);
    var body = el("tbody");
    rows.forEach(function (row) {
      var tr = el("tr");
      for (var index = 0; index < Math.max(row.length, header.length); index += 1) {
        tr.appendChild(el("td", null, inlineNodes(row[index] || "")));
      }
      body.appendChild(tr);
    });
    table.appendChild(body);
    return table;
  }

  /* Inline runs inside one line of text. No nesting: models rarely need
   * bold-inside-link, and supporting it would cost more than it is worth here. */
  function inlineNodes(source) {
    var fragment = document.createDocumentFragment();
    var pattern = /(`[^`]+`)|(\*\*[^*]+\*\*)|(__[^_]+__)|(\*[^*\n]+\*)|(~~[^~]+~~)|(\[[^\]]+\]\((https?:\/\/[^)\s]+)\))/g;
    var last = 0;
    var match;
    while ((match = pattern.exec(source)) !== null) {
      if (match.index > last) { fragment.appendChild(text(source.slice(last, match.index))); }
      last = match.index + match[0].length;
      if (match[1]) {
        fragment.appendChild(el("code", null, match[1].slice(1, -1)));
      } else if (match[2] || match[3]) {
        fragment.appendChild(el("strong", null, match[2] ? match[2].slice(2, -2) : match[3].slice(2, -2)));
      } else if (match[4]) {
        fragment.appendChild(el("em", null, match[4].slice(1, -1)));
      } else if (match[5]) {
        fragment.appendChild(el("del", null, match[5].slice(2, -2)));
      } else if (match[6]) {
        fragment.appendChild(el("a", {
          href: match[7],
          target: "_blank",
          rel: "noopener noreferrer"
        }, match[6].replace(/^\[|\]\(.*\)$/g, "")));
      }
    }
    if (last < source.length) { fragment.appendChild(text(source.slice(last))); }
    return fragment;
  }

  function codeBlock(code, lang) {
    var copy = el("button", { type: "button", class: "code-copy" }, "\u590d\u5236");
    copy.addEventListener("click", function () { copyText(code, copy); });
    return el("div", { class: "code-block" },
      el("div", { class: "code-head" },
        el("span", { class: "code-lang" }, lang || "text"),
        copy),
      el("pre", null, el("code", null, code)));
  }

  /* Clipboard with a fallback: the page is also served over plain HTTP on a LAN
   * address, where navigator.clipboard is unavailable (it needs a secure
   * context). */
  function copyText(value, button) {
    function done() {
      if (!button) { return; }
      if (!button.dataset.original) { button.dataset.original = button.textContent; }
      button.textContent = "\u5df2\u590d\u5236";
      setTimeout(function () { button.textContent = button.dataset.original; }, 1200);
    }
    function fallback() {
      var area = el("textarea", { class: "copy-area" });
      area.value = value;
      document.body.appendChild(area);
      area.select();
      try { document.execCommand("copy"); done(); } catch (error) { /* nothing else to try */ }
      document.body.removeChild(area);
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(value).then(done, fallback);
      return;
    }
    fallback();
  }

  /* ---------- auth ---------- */
  /* The server leaves the page itself open and guards /api/. That split is what
   * makes a token field possible: the browser can load this script and ask for
   * the token here. A page served behind the token would answer 401 in plain
   * text, with no UI to type anything into.
   */
  var TOKEN_KEY = "heron.serve.token";

  function readToken() {
    try { return window.localStorage.getItem(TOKEN_KEY) || ""; } catch (error) { return ""; }
  }

  function writeToken(value) {
    try { window.localStorage.setItem(TOKEN_KEY, value); } catch (error) { /* storage disabled */ }
  }

  function dropToken() {
    try { window.localStorage.removeItem(TOKEN_KEY); } catch (error) { /* storage disabled */ }
  }

  function rejected(error) {
    dropToken();
    error.unauthorized = true;
    renderLogin("token \u4e0d\u6b63\u786e\u6216\u5df2\u5931\u6548\uff0c\u8bf7\u91cd\u65b0\u8f93\u5165\u3002");
    throw error;
  }

  /* apiFetch attaches the bearer token and turns 401 into the login screen.
   * Every data request goes through it: a bare fetch would fail with an
   * unreadable error whenever the token is missing or stale. */
  function apiFetch(path, options) {
    var init = options || {};
    var headers = init.headers || {};
    var token = readToken();
    if (token) { headers.Authorization = "Bearer " + token; }
    init.headers = headers;
    return fetch(path, init).then(function (response) {
      if (response.status === 401) { return rejected(new Error("HTTP 401")); }
      return response;
    });
  }

  function renderLogin(message) {
    if (sidebar) { sidebar.classList.add("hidden"); }
    // The collapse control belongs to the sidebar; on the login screen there is
    // no sidebar to collapse.
    if (shell) { shell.classList.add("logged-out"); }
    clear(app);
    setRecordView(false);
    var input = el("input", {
      type: "password",
      class: "token-input",
      placeholder: "\u8bbf\u95ee token",
      autocomplete: "current-password",
      spellcheck: "false"
    });
    var form = el("form", { class: "login" },
      el("h1", null, "heron-ai"),
      el("p", { class: "muted" }, "\u8fd9\u4e2a\u670d\u52a1\u8981\u6c42\u8bbf\u95ee token\u3002"),
      input,
      el("button", { type: "submit" }, "\u8fdb\u5165"),
      el("p", { class: "login-hint" }, message || ""));
    form.addEventListener("submit", function (event) {
      event.preventDefault();
      var value = input.value.trim();
      if (!value) { input.focus(); return; }
      writeToken(value);
      route();
    });
    app.appendChild(form);
    input.focus();
  }

  /* ---------- router ---------- */
  function navigate(href) {
    history.pushState(null, "", href);
    route();
  }

  document.addEventListener("click", function (event) {
    var anchor = event.target.closest ? event.target.closest("a[data-link]") : null;
    if (!anchor) { return; }
    event.preventDefault();
    navigate(anchor.getAttribute("href"));
  });
  window.addEventListener("popstate", route);

  function route() {
    /* No local "is there a token" gate here on purpose: loopback serves run
     * without a token, and asking for one up front would lock the reader out of
     * a server that never wanted it. The gate is the API's 401 — apiFetch turns
     * that into the login form, and a clean 200 renders straight through. */
    var match = location.pathname.match(/^\/s\/(.+)$/);
    var sessionID = match ? decodeURIComponent(match[1]) : "";
    // Leaving a conversation must close its stream: one open subscription per
    // visited session would pile up silently.
    stopSessionStream();
    renderSidebar(sessionID);
    if (sessionID) {
      renderRecord(sessionID);
      return;
    }
    if (location.pathname === "/config") {
      renderConfig();
      return;
    }
    renderWelcome();
  }

  function fail(message) {
    clear(app);
    setRecordView(false);
    app.appendChild(el("div", { class: "error-box" }, message));
    app.appendChild(el("p", null, link("/", "\u2190 \u8fd4\u56de\u4f1a\u8bdd\u5217\u8868")));
  }

  /* ---------- new session helpers ---------- */
  /* currentSessionID is the id in the address bar, or "" on the list page. */
  function currentSessionID() {
    var match = location.pathname.match(/^\/s\/(.+)$/);
    return match ? decodeURIComponent(match[1]) : "";
  }

  /* isEmptyRecord answers "does this conversation have anything in it yet".
   * The created event is the session itself, not content. */
  function isEmptyRecord(record) {
    if (!record) { return false; }
    if ((record.facts || {}).turns) { return false; }
    var events = record.events || [];
    for (var i = 0; i < events.length; i++) {
      if (events[i].type !== "session.created") { return false; }
    }
    return events.length > 0;
  }

  function focusComposer() {
    var input = app.querySelector(".composer-input");
    if (input) { input.focus(); }
  }

  /* ---------- new session ---------- */
  /* POST /api/sessions with no body opens an empty conversation; the first turn
   * is typed into that page's composer. Creating a session and starting a turn
   * are two steps for a person: waiting on this form for the first answer — a
   * dispatch can take minutes — leaves them with nothing to look at. */
  function renderNewSession() {
    var busy = false;
    var toggle = el("button", { type: "button", class: "new-toggle", title: "\u65b0\u5efa\u4f1a\u8bdd" }, "\uff0b \u65b0\u5efa");
    var notice = el("div", { class: "new-notice hidden" });
    var box = el("div", { class: "new-session" }, toggle, notice);

    function say(text) {
      notice.textContent = text || "";
      notice.classList.toggle("hidden", !text);
    }

    function reset() {
      busy = false;
      toggle.disabled = false;
      toggle.textContent = "\uff0b \u65b0\u5efa";
    }

    function create() {
      if (busy) { return; }
      // Already sitting in an empty session: another one would render the same
      // page with a different id in 8pt grey, which reads as "the click did
      // nothing". Reuse it and move the caret where the next action is.
      //
      // The test is the page's own marker, not a remembered record: the badge is
      // drawn from the record that is on screen right now, so the two cannot
      // disagree about whether this conversation is still empty.
      if (currentSessionID() && app.querySelector(".badge-new")) {
        say("\u5df2\u5728\u65b0\u4f1a\u8bdd\u91cc\uff0c\u76f4\u63a5\u8f93\u5165\u7b2c\u4e00\u53e5\u3002");
        focusComposer();
        return;
      }
      say("");
      busy = true;
      toggle.disabled = true;
      toggle.textContent = "\u521b\u5efa\u4e2d\u2026";
      apiFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}"
      })
        .then(function (response) {
          if (response.ok) { return response.json(); }
          return response.text().then(function (text) {
            throw new Error((text || "").trim() || ("HTTP " + response.status));
          });
        })
        .then(function (result) {
          var id = result && result.session && result.session.id;
          if (!id) { throw new Error("\u54cd\u5e94\u91cc\u6ca1\u6709\u4f1a\u8bdd ID"); }
          // The new session is not in the list already fetched; rebuild it so
          // the sidebar shows what was just created.
          sidebarReady = false;
          navigate("/s/" + encodeURIComponent(id));
        })
        .catch(function (error) {
          if (error && error.unauthorized) { return; }
          reset();
          // "创建失败" in a hover title is invisible on a click: the button goes
          // back to its normal text and the page stays put, which is exactly
          // what "the button did nothing" looks like.
          say("\u521b\u5efa\u5931\u8d25\uff1a" + error.message);
        });
    }

    toggle.addEventListener("click", create);
    return box;
  }

  /* ---------- sidebar ---------- */
  /* The list lives beside the transcript instead of replacing it: switching
   * sessions used to mean a full page jump back to a list, then into another
   * session. Search filters what is already loaded — the list endpoint returns
   * every session, so there is nothing to ask the server for. */
  var sidebarSearch = "";
  var sidebarReady = false;

  /* A prompt is often several sentences long. The row shows two lines of it and
   * puts the rest in the tooltip: a list where every item is five lines tall is
   * a list nobody can scan. */
  function sessionRow(session, activeID) {
    var title = session.title || "(\u672a\u547d\u540d)";
    var main = el("span", { class: "row-main" },
      el("div", { class: "title", title: session.title ? session.title : "" }, title),
      el("div", { class: "meta" },
        session.turns + " \u8f6e \u00b7 " + tokenCount(session.tokens) + " tok \u00b7 " + relativeTime(session.updated_at)));
    return el("a", {
      class: "row" + (session.id === activeID ? " active" : ""),
      href: "/s/" + encodeURIComponent(session.id),
      "data-link": "1",
      "data-id": session.id
    },
      el("span", { class: session.active ? "dot active" : "dot" }),
      main);
  }

  function applySidebarFilter(rows, keyword) {
    var needle = String(keyword || "").trim().toLowerCase();
    Array.prototype.forEach.call(rows.querySelectorAll(".row"), function (row) {
      var haystack = (row.textContent + " " + (row.getAttribute("data-id") || "")).toLowerCase();
      var hit = needle === "" || haystack.indexOf(needle) >= 0;
      row.classList.toggle("hidden", !hit);
    });
  }

  function updateSidebarActive(activeID) {
    if (!sidebar) { return; }
    Array.prototype.forEach.call(sidebar.querySelectorAll(".row"), function (row) {
      var isActive = row.getAttribute("href") === "/s/" + encodeURIComponent(activeID);
      row.classList.toggle("active", isActive);
    });
  }

  /* The configuration entry sits at the bottom, away from the list it does not
   * act on: it opens the server's own settings, not a session. */
  function renderSidebarFoot() {
    return el("div", { class: "side-foot" },
      el("a", { class: "side-foot-link", href: "/config", "data-link": "1" }, "\u5168\u5c40\u914d\u7f6e"));
  }

  /* The entry's own highlight, kept apart from renderSidebarFoot so switching
   * between a session and the settings page can update it without rebuilding
   * the list — the same reason the rows get updateSidebarActive. */
  function updateSidebarFoot() {
    if (!sidebar) { return; }
    var entry = sidebar.querySelector(".side-foot-link");
    if (entry) { entry.classList.toggle("active", location.pathname === "/config"); }
  }

  /* Collapse control.
   *
   * It lives outside the sidebar so it stays reachable while the sidebar is
   * gone; it is positioned against the sidebar's edge, half way down, where the
   * eye looks for it. The state is remembered: collapsing is a preference about
   * the page, not about one visit. */
  var SIDEBAR_COLLAPSED_KEY = "heron.sidebar.collapsed";

  function sidebarCollapsedPreference() {
    try { return window.localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1"; } catch (error) { return false; }
  }

  function applySidebarCollapsed(collapsed) {
    if (!shell) { return; }
    shell.classList.toggle("side-collapsed", collapsed);
    var button = shell.querySelector(".side-toggle");
    if (button) {
      button.setAttribute("aria-expanded", collapsed ? "false" : "true");
      button.title = collapsed ? "\u5c55\u5f00\u4fa7\u680f" : "\u6536\u8d77\u4fa7\u680f";
    }
  }

  function renderSidebarToggle() {
    if (!shell || shell.querySelector(".side-toggle")) { return; }
    var button = el("button", { type: "button", class: "side-toggle", "aria-label": "\u6536\u8d77\u4fa7\u680f" },
      el("span", { class: "chev" }));
    button.addEventListener("click", function () {
      var collapsed = !shell.classList.contains("side-collapsed");
      applySidebarCollapsed(collapsed);
      try { window.localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "1" : "0"); } catch (error) { /* storage disabled */ }
    });
    shell.insertBefore(button, app);
    applySidebarCollapsed(sidebarCollapsedPreference());
  }

  function renderSidebar(activeID) {
    if (!sidebar) { return; }
    sidebar.classList.remove("hidden");
    if (shell) { shell.classList.remove("logged-out"); }

    // Switching sessions only moves the highlight; re-fetching the list on
    // every click would make the sidebar flicker and drop the search term.
    if (sidebarReady) {
      updateSidebarActive(activeID);
      updateSidebarFoot();
      return;
    }

    clear(sidebar);
    var search = el("input", {
      class: "side-search",
      type: "search",
      placeholder: "\u641c\u7d22\u4f1a\u8bdd",
      value: sidebarSearch
    });
    // The name is the page's anchor and takes the full width; the two controls
    // that act on the list below share the next row.
    sidebar.appendChild(el("div", { class: "side-brand" },
      el("a", { class: "side-title", href: "/", "data-link": "1" }, "heron-ai")));
    sidebar.appendChild(el("div", { class: "side-actions" }, renderNewSession(), search));
    var rows = el("div", { class: "rows side-rows" });
    sidebar.appendChild(rows);
    sidebar.appendChild(renderSidebarFoot());
    updateSidebarFoot();

    search.addEventListener("input", function () {
      sidebarSearch = search.value;
      applySidebarFilter(rows, sidebarSearch);
    });

    apiFetch("/api/sessions")
      .then(function (response) {
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (payload) {
        var sessions = payload.sessions || [];
        sidebarReady = true;
        if (sessions.length === 0) {
          rows.appendChild(el("div", { class: "empty" },
            "\u8fd8\u6ca1\u6709\u4f1a\u8bdd\u3002\u70b9\u4e0a\u9762\u7684\u300c\u65b0\u5efa\u4f1a\u8bdd\u300d\u5f00\u59cb\u7b2c\u4e00\u4e2a\u3002"));
          return;
        }
        sessions.forEach(function (session) {
          rows.appendChild(sessionRow(session, activeID));
        });
        applySidebarFilter(rows, sidebarSearch);
      })
      .catch(function (error) {
        if (error && error.unauthorized) { return; }
        rows.appendChild(el("div", { class: "empty" },
          "\u8bfb\u53d6\u4f1a\u8bdd\u5217\u8868\u5931\u8d25\uff1a" + error.message));
      });
  }

  function renderWelcome() {
    clear(app);
    setRecordView(false);
    app.appendChild(el("div", { class: "welcome" },
      el("h1", null, "heron-ai"),
      el("p", { class: "muted" }, "\u4ece\u5de6\u4fa7\u9009\u4e00\u4e2a\u4f1a\u8bdd\uff0c\u6216\u65b0\u5efa\u4e00\u4e2a\u3002")));
  }

  /* ---------- config ---------- */
  /* What this server is actually running, and the little of it that can be
   * changed from here. Edits go to .agents/settings.json through
   * PATCH /api/config; those values are read when the runtime is built, so the
   * page says "restart to apply" instead of pretending they are live. */
  var CONFIG_LABELS = {
    max_team_turns: "\u5355\u8f6e\u6700\u5927 team \u56de\u5408",
    max_calls_per_team_turn: "\u5355 team \u56de\u5408\u6700\u5927 call",
    max_agent_rounds: "\u5355\u8f6e\u6700\u5927 agent \u6b65\u6570",
    max_parallel_teams: "\u5e76\u884c team \u4e0a\u9650",
    max_parallel_calls: "\u5e76\u884c call \u4e0a\u9650",
    max_coordinate_retries: "\u56de\u9000\u534f\u8c03\u6b21\u6570\u4e0a\u9650",
    max_parallel_tools: "\u5e76\u884c\u5de5\u5177\u4e0a\u9650",
    max_active_team_sessions: "\u5e76\u5b58 team \u5b9e\u4f8b\u4e0a\u9650",
    max_task_context_depth: "\u4efb\u52a1\u5d4c\u5957\u5c42\u6570",
    level: "\u7ea7\u522b",
    output: "\u8f93\u51fa",
    dir: "\u76ee\u5f55",
    max_file_size: "\u5355\u6587\u4ef6\u4e0a\u9650",
    max_backups: "\u5907\u4efd\u4e2a\u6570",
    retention_days: "\u4fdd\u7559\u5929\u6570",
    bash: "Bash",
    write: "Write",
    websearch: "WebSearch",
    webfetch: "WebFetch",
    enabled: "\u5f00\u5173",
    timeout_ms: "\u8d85\u65f6\uff08ms\uff09",
    max_steps: "\u5d4c\u5957\u5de5\u5177\u6b21\u6570\u4e0a\u9650",
    max_output_bytes: "\u8f93\u51fa\u4e0a\u9650\uff08\u5b57\u8282\uff09",
    catalog_budget: "\u5de5\u5177\u76ee\u5f55\u9884\u7b97",
    summary_model: "\u603b\u7ed3\u6a21\u578b",
    provider: "\u63d0\u4f9b\u65b9",
    restricted_bash_phrases: "\u7981\u6b62\u547d\u4ee4\u7247\u6bb5"
  };

  // The order a reader expects, which is not the alphabetical order a JSON
  // object comes back in.
  var CONFIG_FIELD_ORDER = {
    runtime: ["max_team_turns", "max_calls_per_team_turn", "max_agent_rounds",
      "max_parallel_teams", "max_parallel_calls", "max_coordinate_retries",
      "max_parallel_tools", "max_active_team_sessions", "max_task_context_depth"],
    logging: ["level", "output", "dir", "max_file_size", "max_backups", "retention_days"],
    permissions: ["bash", "write", "websearch", "webfetch"],
    repl: ["enabled", "timeout_ms", "max_steps", "max_output_bytes", "catalog_budget"]
  };

  var PERMISSION_CHOICES = [
    ["allow", "\u5141\u8bb8"],
    ["ask", "\u8be2\u95ee"],
    ["deny", "\u62d2\u7edd"]
  ];

  // Which fields are numbers. The edit form cannot infer this from a value the
  // file does not set yet: an unset budget would render as a text box and be
  // sent back as a string.
  var CONFIG_NUMBER_FIELDS = {
    runtime: ["max_team_turns", "max_calls_per_team_turn", "max_agent_rounds",
      "max_parallel_teams", "max_parallel_calls", "max_coordinate_retries",
      "max_parallel_tools", "max_active_team_sessions", "max_task_context_depth"],
    logging: ["max_backups", "retention_days"],
    repl: ["timeout_ms", "max_steps", "max_output_bytes", "catalog_budget"]
  };

  function isNumberField(group, key) {
    var fields = CONFIG_NUMBER_FIELDS[group];
    return Boolean(fields && fields.indexOf(key) >= 0);
  }

  var configState = { data: null, editing: false, envDrafts: [], notice: "" };

  function renderConfig() {
    clear(app);
    setRecordView(false);
    app.appendChild(el("div", { class: "top" }, el("h1", null, "\u5168\u5c40\u914d\u7f6e")));
    var body = el("div", { class: "config-view" },
      el("div", { class: "muted" }, "\u8bfb\u53d6\u4e2d\u2026"));
    app.appendChild(body);

    configState.editing = false;
    configState.envDrafts = [];
    configState.notice = "";

    apiFetch("/api/config")
      .then(function (response) {
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (config) {
        configState.data = config || {};
        paintConfig(body);
      })
      .catch(function (error) {
        if (error && error.unauthorized) { return; }
        clear(body);
        body.appendChild(el("div", { class: "error-box" },
          "\u8bfb\u53d6\u914d\u7f6e\u5931\u8d25\uff1a" + error.message));
      });
  }

  function configCard(title, content) {
    return el("section", { class: "config-card" }, el("h2", null, title), content);
  }

  /* Rows are a definition list, not a table: every value is one line of text
   * and labels are short, so there is nothing to align across columns. */
  function configRows(pairs) {
    var list = el("dl", { class: "config-rows" });
    pairs.forEach(function (pair) {
      var value = pair[1];
      if (value === null || value === undefined || value === "") { return; }
      // false is a value here, not an absence: a tool that is switched off is
      // exactly what a settings page has to say.
      list.appendChild(el("dt", null, pair[0]));
      list.appendChild(el("dd", null, value === true ? "\u5f00\u542f" : value === false ? "\u5173\u95ed" : value));
    });
    return list;
  }

  function sectionValues(section) {
    return (section && section.values) || {};
  }

  function sectionExplicit(section) {
    var set = {};
    ((section && section.explicit) || []).forEach(function (key) { set[key] = true; });
    return set;
  }

  function orderedFields(group, values, includeMissing) {
    var order = CONFIG_FIELD_ORDER[group];
    if (!order) { return Object.keys(values || {}); }
    if (includeMissing) { return order.slice(); }
    return order.filter(function (key) { return values && values[key] !== undefined; });
  }

  function formatConfigValue(value) {
    if (value === true) { return "\u5f00\u542f"; }
    if (value === false) { return "\u5173\u95ed"; }
    if (value === null || value === undefined || value === "") { return "\u2014"; }
    return String(value);
  }

  /* A value the file does not set is shown with its default and a marker: the
   * difference between "this workspace chose 200" and "200 is the fallback" is
   * the first thing a reader needs from this page. */
  function configValueRows(group, values, explicit) {
    var list = el("dl", { class: "config-rows" });
    // Every field the section can hold is listed: a reader has to see that
    // logging.output exists and is unset, not just the keys someone happened to
    // write into the file.
    orderedFields(group, values, true).forEach(function (key) {
      var dd = el("dd", null, formatConfigValue(values[key]));
      if (explicit && !explicit[key]) {
        dd.appendChild(el("span", { class: "config-default" }, "\u9ed8\u8ba4"));
      }
      list.appendChild(el("dt", null, CONFIG_LABELS[key] || key));
      list.appendChild(dd);
    });
    return list;
  }

  function configEditInput(path, value, numeric) {
    if (typeof value === "boolean") {
      var box = el("input", { class: "config-toggle", type: "checkbox", "data-path": path });
      box.checked = value;
      box.setAttribute("data-before", value ? "1" : "0");
      return box;
    }
    var text = value === null || value === undefined ? "" : String(value);
    // Zero means "not set" for these budgets, and a 0 in the box would fail the
    // "positive integer" check on save.
    if (numeric && Number(value) === 0) { text = ""; }
    return el("input", {
      class: "config-input",
      type: numeric ? "number" : "text",
      value: text,
      "data-path": path,
      "data-before": text
    });
  }

  function configEditRows(group, values, explicit, prefix) {
    var list = el("dl", { class: "config-rows" });
    orderedFields(group, values, true).forEach(function (key) {
      var numeric = isNumberField(group, key) || typeof values[key] === "number";
      var dd = el("dd", null, configEditInput((prefix || group) + "." + key, values[key], numeric));
      if (explicit && !explicit[key]) {
        dd.appendChild(el("span", { class: "config-default" }, "\u9ed8\u8ba4"));
      }
      list.appendChild(el("dt", null, CONFIG_LABELS[key] || key));
      list.appendChild(dd);
    });
    return list;
  }

  function configPermissionRows(values, explicit) {
    if (!configState.editing) { return configValueRows("permissions", values, explicit); }
    var list = el("dl", { class: "config-rows" });
    orderedFields("permissions", values).forEach(function (key) {
      var select = el("select", { class: "config-input config-select", "data-path": "permissions." + key, "data-before": values[key] });
      PERMISSION_CHOICES.forEach(function (choice) {
        var option = el("option", { value: choice[0] }, choice[1]);
        if (values[key] === choice[0]) { option.selected = true; }
        select.appendChild(option);
      });
      list.appendChild(el("dt", null, CONFIG_LABELS[key] || key));
      list.appendChild(el("dd", null, select));
    });
    return list;
  }

  /* data-path values are dotted because the write is one nested object:
   * tools.repl.enabled has to end up inside tools.repl, not as a flat key. */
  function assignConfigPath(target, path, value) {
    var parts = path.split(".");
    var node = target;
    for (var index = 0; index < parts.length - 1; index++) {
      if (!node[parts[index]] || typeof node[parts[index]] !== "object") { node[parts[index]] = {}; }
      node = node[parts[index]];
    }
    node[parts[parts.length - 1]] = value;
  }

  /* The edit/save pair sits above the cards: one mode for the whole page beats a
   * button per card, because the write is one PATCH either way. */
  function configToolbar(container) {
    var bar = el("div", { class: "config-bar" },
      el("span", { class: "config-notice" }, configState.notice || ""),
      el("span", { class: "config-actions" },
        configState.editing
          ? [el("button", { type: "button", class: "config-action", "data-config-action": "cancel" }, "\u53d6\u6d88"),
             el("button", { type: "button", class: "config-action primary", "data-config-action": "save" }, "\u4fdd\u5b58")]
          : el("button", { type: "button", class: "config-action", "data-config-action": "edit" }, "\u7f16\u8f91")));

    bar.addEventListener("click", function (event) {
      var action = event.target.getAttribute ? event.target.getAttribute("data-config-action") : "";
      if (!action) { return; }
      if (action === "save") { saveConfig(container); return; }
      configState.editing = action === "edit";
      configState.envDrafts = [];
      configState.notice = "";
      paintConfig(container);
    });
    return bar;
  }

  function showConfigNotice(text) {
    configState.notice = text || "";
    var notice = document.querySelector(".config-notice");
    if (notice) { notice.textContent = configState.notice; }
  }

  /* The REPL block and the bash phrases the gate refuses. Both halves live in
   * the tools section, so both are edited here. */
  function configToolsCard(tools) {
    var repl = tools.repl || {};
    var phrases = tools.restricted_bash_phrases || [];
    var body = el("div", null);

    body.appendChild(el("div", { class: "config-sub" }, "REPL"));
    if (configState.editing) {
      var replValues = {};
      CONFIG_FIELD_ORDER.repl.forEach(function (key) {
        replValues[key] = repl[key] === undefined ? "" : repl[key];
      });
      body.appendChild(configEditRows("repl", replValues, null, "tools.repl"));
    } else {
      body.appendChild(configValueRows("repl", repl, null));
    }

    body.appendChild(el("div", { class: "config-sub" }, "\u7981\u6b62\u547d\u4ee4\u7247\u6bb5"));
    if (configState.editing) {
      body.appendChild(el("input", {
        class: "config-input config-input-wide",
        type: "text",
        value: phrases.join(", "),
        "data-before": phrases.join(", "),
        "data-path": "tools.restricted_bash_phrases",
        placeholder: "\u9017\u53f7\u5206\u9694\uff0c\u7559\u7a7a\u8868\u793a\u4e0d\u9650\u5236"
      }));
    } else {
      body.appendChild(phrases.length
        ? el("div", { class: "config-phrases" }, phrases.join("\u3001"))
        : el("div", { class: "muted" }, "\u672a\u8bbe\u7f6e"));
    }
    return body;
  }

  /* Web tools carry an API key and an endpoint, and either can hold a token:
   * this page reports the provider and whether a key is set, nothing else. */
  function describeWebEndpoint(endpoint) {
    var parts = [];
    if (endpoint.provider) { parts.push(endpoint.provider); }
    if (endpoint.max_results) { parts.push(endpoint.max_results + " \u6761"); }
    if (endpoint.format) { parts.push(endpoint.format); }
    if (endpoint.location) { parts.push(endpoint.location); }
    if (endpoint.language) { parts.push(endpoint.language); }
    if (endpoint.timeout_ms) { parts.push(endpoint.timeout_ms + "ms"); }
    if (endpoint.url_set) { parts.push("\u81ea\u5b9a\u4e49 endpoint"); }
    return parts.join(" \u00b7 ");
  }

  function configWebCard(web) {
    var rows = [
      ["\u63d0\u4f9b\u65b9", web.provider || "duckduckgo"],
      ["API Key", web.api_key_set ? "\u5df2\u914d\u7f6e" : "\u672a\u914d\u7f6e"]
    ];
    var search = describeWebEndpoint(web.search || {});
    var fetch = describeWebEndpoint(web.fetch || {});
    if (search) { rows.push(["search", search]); }
    if (fetch) { rows.push(["fetch", fetch]); }
    return configRows(rows);
  }

  function configMCPCard(servers) {
    var body = el("div", { class: "config-teams" });
    servers.forEach(function (server) {
      var meta = [];
      if (server.transport) { meta.push(server.transport); }
      if (server.command) { meta.push(server.command); }
      if (server.args_count) { meta.push(server.args_count + " \u4e2a\u53c2\u6570"); }
      if (server.url_set) { meta.push("url \u5df2\u914d\u7f6e"); }
      if ((server.header_keys || []).length) { meta.push("header: " + server.header_keys.join("\u3001")); }
      if ((server.env_keys || []).length) { meta.push("env: " + server.env_keys.join("\u3001")); }
      body.appendChild(el("div", { class: "config-team" },
        el("span", { class: "config-team-name" }, server.name || "(\u672a\u547d\u540d)"),
        el("span", { class: "config-team-meta" }, meta.join(" \u00b7 "))));
    });
    return body;
  }

  /* The files that define this workspace. Folded by default: they answer "what
   * is in here", not "what is in force" — the cards above do that. */
  function configAssetsCard(assets) {
    var groups = [
      ["flows", "Flow"], ["teams", "Team"], ["agents", "Agent"],
      ["skills", "Skill"], ["rules", "\u89c4\u5219"], ["knowledge", "\u77e5\u8bc6"]
    ];
    var body = el("div", { class: "config-assets" });
    var total = 0;
    groups.forEach(function (pair) {
      var names = assets[pair[0]] || [];
      total += names.length;
      if (!names.length) { return; }
      body.appendChild(el("div", { class: "config-asset" },
        el("span", { class: "config-asset-kind" }, pair[1] + "\uff08" + names.length + "\uff09"),
        el("span", { class: "config-asset-names" }, names.join("\u3001"))));
    });
    if (!total) { body.appendChild(el("div", { class: "muted" }, "\u6ca1\u6709\u5b9a\u4e49\u6587\u4ef6\u3002")); }
    return el("details", { class: "config-fold" },
      el("summary", null, "\u5df2\u52a0\u8f7d\u7684\u5b9a\u4e49\uff08" + total + " \u4e2a\u6587\u4ef6\uff09"),
      body);
  }

  /* Environment values never come back from the server, so editing is
   * write-only: a name and a new value go into a draft list, and nothing here
   * can show what the current value is. */
  function configEnvCard(config) {
    var keys = config.env_keys || [];
    var wrap = el("div", null,
      configRows([["\u540d\u79f0", keys.length ? keys.join("\u3001") : "\uff08\u65e0\uff09"]]));
    if (!configState.editing) { return wrap; }

    var drafts = el("div", { class: "config-drafts" });
    function paintDrafts() {
      clear(drafts);
      configState.envDrafts.forEach(function (item) {
        drafts.appendChild(el("span", { class: "config-draft" }, item.name));
      });
    }
    paintDrafts();

    var nameInput = el("input", { class: "config-input", type: "text", placeholder: "\u53d8\u91cf\u540d" });
    var valueInput = el("input", { class: "config-input", type: "password", placeholder: "\u65b0\u503c\uff08\u5df2\u6709\u503c\u4e0d\u56de\u663e\uff09" });
    var add = el("button", { type: "button", class: "config-action" }, "\u52a0\u5165");
    add.addEventListener("click", function () {
      var name = nameInput.value.trim();
      if (!name) { nameInput.focus(); return; }
      configState.envDrafts.push({ name: name, value: valueInput.value });
      nameInput.value = "";
      valueInput.value = "";
      paintDrafts();
    });

    wrap.appendChild(el("div", { class: "config-env-editor" }, nameInput, valueInput, add));
    wrap.appendChild(drafts);
    return wrap;
  }

  function saveConfig(container) {
    var update = {};
    var invalid = null;

    Array.prototype.forEach.call(container.querySelectorAll("[data-path]"), function (control) {
      var path = control.getAttribute("data-path");
      var before = control.getAttribute("data-before");
      var value;

      if (control.classList.contains("config-toggle")) {
        value = control.checked;
        if ((before === "1") === value) { return; }
      } else if (control.tagName === "SELECT") {
        value = control.value;
        if (value === before) { return; }
      } else {
        var raw = control.value.trim();
        if (path === "tools.restricted_bash_phrases") {
          if (raw === (before || "")) { return; }
          value = raw ? raw.split(",").map(function (item) { return item.trim(); }).filter(Boolean) : [];
        } else if (control.type === "number") {
          if (!raw) { return; }
          value = Number(raw);
          if (!isFinite(value) || value <= 0) { invalid = control; return; }
        } else {
          value = raw;
        }
        // Compare as text: a number field yields a number here while the
        // original is the string it was rendered from.
        if (String(value) === before) { return; }
      }
      assignConfigPath(update, path, value);
    });
    if (invalid) {
      showConfigNotice("\u8bf7\u586b\u5199\u6b63\u6574\u6570");
      invalid.focus();
      return;
    }

    if (configState.envDrafts.length) {
      if (!update.env) { update.env = {}; }
      configState.envDrafts.forEach(function (item) { update.env[item.name] = item.value; });
    }

    if (!Object.keys(update).length) {
      showConfigNotice("\u6ca1\u6709\u6539\u52a8");
      return;
    }

    showConfigNotice("\u4fdd\u5b58\u4e2d\u2026");
    apiFetch("/api/config", {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(update)
    })
      .then(function (response) {
        if (response.ok) { return response.json(); }
        return response.text().then(function (text) {
          throw new Error((text || "").trim() || ("HTTP " + response.status));
        });
      })
      .then(function (config) {
        configState.data = config || configState.data;
        configState.editing = false;
        configState.envDrafts = [];
        configState.notice = config && config.restart_required
          ? "\u5df2\u4fdd\u5b58\uff0c\u91cd\u542f\u540e\u751f\u6548"
          : "\u5df2\u4fdd\u5b58";
        paintConfig(container);
      })
      .catch(function (error) {
        if (error && error.unauthorized) { return; }
        showConfigNotice("\u4fdd\u5b58\u5931\u8d25\uff1a" + error.message);
      });
  }

  function paintConfig(container) {
    clear(container);
    var config = configState.data || {};
    container.appendChild(configToolbar(container));

    var build = [config.version, config.commit].filter(Boolean).join(" \u00b7 ");
    container.appendChild(configCard("\u8fd0\u884c", configRows([
      ["\u5de5\u4f5c\u76ee\u5f55", config.workdir || ""],
      ["\u7248\u672c", build]
    ])));

    var flow = config.flow || {};
    if (flow.flow_id) {
      var teams = el("div", { class: "config-teams" });
      (flow.teams || []).forEach(function (team) {
        var calls = (team.calls || []).map(function (call) {
          return call.agent ? call.name + "\uff08" + call.agent + "\uff09" : call.name;
        }).join("\u3001");
        teams.appendChild(el("div", { class: "config-team" },
          el("span", { class: "config-team-name" }, team.name),
          el("span", { class: "config-team-meta", title: team.goal || "" },
            (team.entry ? "\u5165\u53e3 \u00b7 " : "") + (team.lifecycle || "") +
            (calls ? " \u00b7 " + calls : ""))));
      });
      container.appendChild(configCard("Flow",
        el("div", null,
          configRows([["Flow", flow.flow_id], ["\u5165\u53e3 team", flow.entry || ""]]),
          teams)));
    }

    var runtime = config.runtime || {};
    var runtimeValues = sectionValues(runtime);
    var runtimeExplicit = sectionExplicit(runtime);
    container.appendChild(configCard("\u8fd0\u884c\u65f6\u9650\u5236",
      configState.editing
        ? configEditRows("runtime", runtimeValues, runtimeExplicit)
        : configValueRows("runtime", runtimeValues, runtimeExplicit)));

    var logging = config.logging || {};
    var loggingValues = sectionValues(logging);
    var loggingExplicit = sectionExplicit(logging);
    container.appendChild(configCard("\u65e5\u5fd7",
      configState.editing
        ? configEditRows("logging", loggingValues, loggingExplicit)
        : configValueRows("logging", loggingValues, loggingExplicit)));

    var permissions = config.permissions || {};
    container.appendChild(configCard("\u6743\u9650", configPermissionRows(
      sectionValues(permissions), sectionExplicit(permissions))));

    container.appendChild(configCard("\u5de5\u5177", configToolsCard(config.tools || {})));

    container.appendChild(configCard("Web", configWebCard(config.web || {})));

    var knowledge = config.knowledge || {};
    var summaryModel = sectionValues(knowledge).summary_model || "";
    container.appendChild(configCard("\u77e5\u8bc6",
      configState.editing
        ? configEditRows("knowledge", { summary_model: summaryModel }, null)
        : configRows([["\u603b\u7ed3\u6a21\u578b", summaryModel || "\u9ed8\u8ba4\u6a21\u578b"]])));

    if ((config.mcp || []).length) {
      container.appendChild(configCard("MCP", configMCPCard(config.mcp)));
    }

    container.appendChild(configCard("\u73af\u5883\u53d8\u91cf", configEnvCard(config)));

    if (config.model && (config.model.default || (config.model.available || []).length)) {
      var modelBody = el("div", null, configRows([["\u9ed8\u8ba4", config.model.default || ""]]));
      var available = config.model.available || [];
      if (available.length) {
        // A deployment can carry dozens of model ids. The list is what this page
        // is for, but it is not the first thing to read: it starts folded with
        // its count on the summary line.
        modelBody.appendChild(el("details", { class: "config-fold" },
          el("summary", null, "\u53ef\u7528\u6a21\u578b\uff08" + available.length + " \u4e2a\uff09"),
          el("div", { class: "config-model-list" }, available.join("\u3001"))));
      }
      container.appendChild(configCard("\u6a21\u578b", modelBody));
    }

    if (config.assets) {
      container.appendChild(configCard("\u5b9a\u4e49\u8d44\u4ea7", configAssetsCard(config.assets)));
    }

    var files = config.files || {};
    var fileRows = [];
    if (files.settings) { fileRows.push(["\u8bbe\u7f6e", files.settings]); }
    if (files.models) { fileRows.push(["\u6a21\u578b", files.models]); }
    if (fileRows.length) { container.appendChild(configCard("\u914d\u7f6e\u6587\u4ef6", configRows(fileRows))); }
  }

  /* ---------- record ---------- */
  /* The heading of a conversation is the question that started it. The product
   * name and the session id used to sit here and said nothing a reader needed:
   * the sidebar already names the workspace, and the id is in the address bar. */
  function sessionHeading(record) {
    var events = (record && record.events) || [];
    for (var index = 0; index < events.length; index++) {
      if (events[index].type !== "session.turn.started") { continue; }
      var payload = events[index].payload || {};
      var input = payload.input || (payload.turn && payload.turn.input) || "";
      var text = String(input).replace(/\s+/g, " ").trim();
      if (text) { return text; }
    }
    return "";
  }

  function renderRecord(sessionID) {
    clear(app);
    // The sidebar is the way back to the list: a second link in the header was
    // one more thing to read on every page and pointed at the same place.
    app.appendChild(el("div", { class: "top" },
      el("h1", { class: "session-title" }, "")));

    apiFetch("/api/sessions/" + encodeURIComponent(sessionID) + "/record")
      .then(function (response) {
        if (response.status === 404) { throw new Error("\u4f1a\u8bdd\u4e0d\u5b58\u5728"); }
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (record) {
        /* The topology request is optional: an older server answers 404/501 and
         * the board falls back to the event-derived view it used before. */
        return apiFetch("/api/topology?session_id=" + encodeURIComponent(sessionID))
          .then(function (response) {
            return response.ok ? response.json() : null;
          })
          .catch(function () { return null; })
          .then(function (topology) { paintRecord(sessionID, record, topology); });
      })
      .catch(function (error) {
        if (error && error.unauthorized) { return; }
        fail("\u8bfb\u53d6\u4f1a\u8bdd\u8bb0\u5f55\u5931\u8d25\uff1a" + error.message);
      });
  }

  function paintRecord(sessionID, record, topology) {
    currentRecord = record;
    topologyState = topology || null;
    indexCallAgents(record, topologyState);
    resetRunState(record);
    // A conversation with nothing in it renders the same chrome as one with
    // history: same header, same counters at zero, no messages. Saying "新会话"
    // and putting the caret in the box is what makes the arrival visible.
    var empty = isEmptyRecord(record);
    var heading = sessionHeading(record);
    clear(app);
    setRecordView(true);
    var top = el("div", { class: "top" },
      el("h1", { class: "session-title", title: heading }, heading));
    if (empty) { top.appendChild(el("span", { class: "badge-new" }, "\u65b0\u4f1a\u8bdd")); }
    app.appendChild(top);

    if (record.truncated) {
      app.appendChild(el("div", { class: "error-box" },
        "\u8fd8\u6709 " + record.truncated + " \u6761\u4e8b\u4ef6\u672a\u52a0\u8f7d\uff08\u5355\u6b21\u4e0a\u9650\uff09\u3002\u53ef\u52a0 ?limit= \u63d0\u9ad8\u3002"));
    }


    app.appendChild(el("div", { class: "modes" },
      el("div", { class: "seg", role: "group" },
        modeButton("chat", "\u5bf9\u8bdd", true),
        modeButton("overview", "概览", false),
        modeButton("run", "\u8fd0\u884c", false),
        modeButton("waterfall", "\u65f6\u5e8f", false),
        modeButton("tree", "\u7ed3\u6784", false),
        modeButton("raw", "\u539f\u59cb", false))));

    var body = el("div", { id: "record-body" });
    app.appendChild(body);
    showMode(body, "chat");

    app.appendChild(renderComposer(sessionID));
    // The stream subscribed while the transcript rendered: paint the running
    // state onto the composer that only exists now.
    syncTurnChrome();
    if (empty) { focusComposer(); }

    /* A conversation is read from its newest message, so opening one lands at
     * the bottom. The button is the way back after scrolling up into history. */
    var jump = el("button", { type: "button", class: "scroll-bottom", title: "\u5230\u4f1a\u8bdd\u5e95\u90e8" },
      el("span", { class: "chev" }));
    jump.addEventListener("click", function () { scrollRecordToBottom(); });
    function syncJump() {
      var distance = body.scrollHeight - body.scrollTop - body.clientHeight;
      jump.classList.toggle("hidden", distance < 160);
    }
    body.addEventListener("scroll", syncJump);
    app.appendChild(jump);

    if (currentMode === "chat" || scrollToBottom) {
      scrollToBottom = false;
      scrollRecordToBottom();
      syncJump();
    }
  }

  function modeButton(mode, label, pressed) {
    var button = el("button", {
      type: "button",
      "data-mode": mode,
      "aria-pressed": pressed ? "true" : "false"
    }, label);
    button.addEventListener("click", function () {
      Array.prototype.forEach.call(document.querySelectorAll(".seg button"), function (other) {
        other.setAttribute("aria-pressed", other === button ? "true" : "false");
      });
      showMode(document.getElementById("record-body"), mode);
      // Switching views must show the view's beginning: keeping the previous
      // scroll offset lands the new view half-way through (or past its end).
      var pane = recordPane();
      if (pane) { pane.scrollTop = 0; }
    });
    return button;
  }

  function showMode(body, mode) {
    if (!body || !currentRecord) { return; }
    currentMode = mode;
    stopSessionStream();
    clear(body);
    if (mode === "overview") {
      // The strip is navigation, not a statistic, but it is about the session as
      // a whole: it sat above every view before, taking height from the
      // transcript on all of them. Here it sits next to the numbers it indexes.
      body.appendChild(timeline(currentRecord.events));
      body.appendChild(kpiStrip(currentRecord.facts));
      return;
    }
    if (mode === "run") {
      body.appendChild(renderRunners());
      return;
    }
    if (mode === "waterfall") {
      body.appendChild(renderWaterfall(currentRecord));
      return;
    }
    if (mode === "tree") {
      body.appendChild(renderTree(currentRecord));
      return;
    }
    if (mode === "raw") {
      body.appendChild(renderRaw(currentRecord));
      return;
    }
    body.appendChild(renderChat(currentRecord));
    attachSessionStream(body);
  }

  /* ---------- kpi ---------- */
  function kpiStrip(facts) {
    facts = facts || {};
    var items = [
      // 用户回合，不是 flow turn 的总数：一次用户消息会让引擎再跑出内部回合
      // （派给下游 team 的那一轮、工具结果回来后的续跑），把它们算进“回合”会让
      // 一句提问看起来像四句。
      ["回合", facts.turns || 0, "", facts.internal_turns || 0],
      // 回合下面挂的是执行轮：派给下游 team 的那一轮，以及工具结果回来后的续跑
      ["Team \u8f6e\u6b21", facts.team_turns || 0],
      ["Call", facts.calls || 0],
      ["\u5de5\u5177\u8c03\u7528", (facts.tool_calls || 0) + (facts.tool_failures ? " \u00b7 " + facts.tool_failures + " \u5931\u8d25" : "")],
      ["Token 入 / 出", tokenCount(facts.input_tokens) + " / " + tokenCount(facts.output_tokens)],
      ["缓存命中", cacheKpi(facts)],
      ["模型", (facts.models || []).join(", ") || "\u2014", "v-model"]
    ];
    var strip = el("div", { class: "kpi" });
    items.forEach(function (item) {
      strip.appendChild(el("div", null,
        el("div", { class: "k" }, item[0]),
        el("div", { class: "v" + (item[2] ? " " + item[2] : "") },
          String(item[1]),
          item[3] ? el("span", {
            class: "v-note",
            title: "本次会话的内部回合：派给下游 team 的那一轮，以及工具结果回来后的续跑"
          }, " +" + item[3] + " 执行轮") : null)));
    });
    return strip;
  }

  /* ---------- timeline ---------- */
  function timeline(events) {
    var wrap = el("div", { class: "timeline" });
    var bar = el("div", { class: "bar" });
    events.forEach(function (event) {
      var segment = el("i", {
        class: eventClass(event),
        style: "flex:1",
        title: "#" + event.seq + " " + event.type
      });
      segment.addEventListener("click", function () {
        var body = document.getElementById("record-body");
        Array.prototype.forEach.call(document.querySelectorAll(".seg button"), function (other) {
          other.setAttribute("aria-pressed", other.getAttribute("data-mode") === "raw" ? "true" : "false");
        });
        showMode(body, "raw");
        var row = document.getElementById("ev-" + event.seq);
        if (row && row.scrollIntoView) { row.scrollIntoView({ block: "center" }); }
      });
      bar.appendChild(segment);
    });
    wrap.appendChild(bar);
    wrap.appendChild(el("div", { class: "legend" },
      legendItem("mm-user", "\u7528\u6237"),
      legendItem("mm-step", "\u6b65\u9aa4"),
      legendItem("mm-tool", "\u5de5\u5177"),
      legendItem("mm-compact", "\u538b\u7f29"),
      legendItem("mm-error", "\u9519\u8bef"),
      legendItem("mm-wakeup", "\u5b9a\u65f6\u68c0\u67e5")));
    return wrap;
  }

  function legendItem(cls, label) {
    return el("span", null, el("b", { class: cls }), label);
  }

  /* ---------- composer ---------- */
  /* The page was read-only; this sends a turn into the session already on
   * screen: POST /api/sessions/{id}/events with {input, attachments}.
   *
   * That call answers 202 as soon as the message is accepted, so the composer
   * frees immediately — a dispatching turn runs for minutes, and the events
   * describing it arrive on the session stream, not on this response. Whether
   * a turn is still running is shown by the stop button, which the stream
   * drives (markTurnActive), not by the composer's sending state.
   */
  var MAX_ATTACHMENT_BYTES = 8 * 1024 * 1024;
  var scrollToBottom = false;

  function attachmentKind(mime) {
    if (!mime) { return "file"; }
    if (mime.indexOf("image/") === 0) { return "image"; }
    if (mime.indexOf("audio/") === 0) { return "audio"; }
    if (mime.indexOf("video/") === 0) { return "video"; }
    return "file";
  }

  function humanSize(bytes) {
    if (!bytes) { return "0B"; }
    if (bytes < 1024) { return bytes + "B"; }
    if (bytes < 1024 * 1024) { return Math.round(bytes / 1024) + "KB"; }
    return (bytes / 1024 / 1024).toFixed(1) + "MB";
  }

  function readBase64(file) {
    return new Promise(function (resolve, reject) {
      var reader = new FileReader();
      reader.onload = function () {
        var result = String(reader.result || "");
        var comma = result.indexOf(",");
        resolve(comma >= 0 ? result.slice(comma + 1) : result);
      };
      reader.onerror = function () { reject(reader.error || new Error("\u8bfb\u53d6\u5931\u8d25")); };
      reader.readAsDataURL(file);
    });
  }

  function renderComposer(sessionID) {
    var pending = [];
    var busy = false;

    var chips = el("div", { class: "chips" });
    var input = el("textarea", {
      class: "composer-input",
      rows: "1",
      placeholder: "\u53d1\u6d88\u606f\u7ed9\u8fd9\u4e2a\u4f1a\u8bdd\u3002Enter \u53d1\u9001\uff0cShift+Enter \u6362\u884c\uff1b\u53ef\u76f4\u63a5\u7c98\u8d34\u56fe\u7247\u6216\u62d6\u5165\u6587\u4ef6\u3002"
    });
    var filePicker = el("input", { type: "file", multiple: "multiple", class: "composer-file" });
    var attachButton = el("button", { type: "button", class: "composer-attach" }, "\u9644\u4ef6");
    var sendButton = el("button", { type: "button", class: "composer-send" }, "\u53d1\u9001");
    var stopButton = el("button", { type: "button", class: "composer-stop" }, "\u505c\u6b62");
    var status = el("span", { class: "composer-status" }, "");

    var box = el("div", { class: "composer" },
      chips,
      input,
      el("div", { class: "composer-bar" },
        attachButton,
        filePicker,
        status,
        el("span", { class: "composer-hint" }, "Enter \u53d1\u9001 \u00b7 Shift+Enter \u6362\u884c"),
        sendButton,
        stopButton));

    function refreshChips() {
      clear(chips);
      chips.style.display = pending.length ? "" : "none";
      pending.forEach(function (item, index) {
        var remove = el("button", { type: "button", class: "chip-x", title: "\u79fb\u9664" }, "\u00d7");
        remove.addEventListener("click", function () {
          pending.splice(index, 1);
          refreshChips();
        });
        chips.appendChild(el("span", { class: "chip" },
          el("span", { class: "chip-kind" }, item.kind),
          el("span", { class: "chip-name" }, item.name),
          el("span", { class: "chip-size" }, humanSize(item.size_bytes)),
          remove));
      });
    }

    function addFiles(files) {
      if (!files || !files.length) { return; }
      Array.prototype.forEach.call(files, function (file) {
        if (file.size > MAX_ATTACHMENT_BYTES) {
          status.textContent = "\u300c" + file.name + "\u300d \u8d85\u8fc7 " + humanSize(MAX_ATTACHMENT_BYTES) + "\uff0c\u672a\u6dfb\u52a0\u3002";
          return;
        }
        readBase64(file).then(function (data) {
          pending.push({
            name: file.name || "pasted",
            mime_type: file.type || "application/octet-stream",
            kind: attachmentKind(file.type),
            size_bytes: file.size || 0,
            data_base64: data
          });
          refreshChips();
        }).catch(function (error) {
          status.textContent = "\u8bfb\u53d6\u300c" + file.name + "\u300d\u5931\u8d25\uff1a" + error.message;
        });
      });
    }

    // Sending only disables the composer. Whether a turn is running is shown by
    // the stop button, which the session stream drives (see markTurnActive):
    // an accepted message frees the composer at once, while the turn it started
    // may still be running.
    function setBusy(value, text) {
      busy = value;
      input.disabled = value;
      attachButton.disabled = value;
      sendButton.classList.toggle("hidden", value);
      status.textContent = text || "";
    }

    function autosize() {
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight || 0, 180) + "px";
    }

    /* Bumped on every send and on every stop. A POST that answers after the
     * reader already stopped (or started another turn) must not touch the UI. */
    var runToken = 0;

    function submit() {
      if (busy) { return; }
      var value = input.value.trim();
      if (!value && pending.length === 0) { return; }
      var payload = { input: value };
      if (pending.length) { payload.attachments = pending.slice(); }

      var token = ++runToken;

      input.value = "";
      autosize();
      pending = [];
      refreshChips();
      setBusy(true, "\u5df2\u53d1\u9001\uff0c\u6b63\u5728\u6267\u884c\u2026\u8fc7\u7a0b\u4f1a\u5b9e\u65f6\u51fa\u73b0\u5728\u5bf9\u8bdd\u91cc\u3002");
      scrollRecordToBottom();

      /* 202 means accepted, not done. The body says *where* it was accepted:
       * `steered` means the turn that is running already has the message and
       * will read it before its next decision, `queued` alone means the next
       * turn will. Showing the difference is the point — otherwise the message
       * leaves the composer and nothing visible happens for minutes. */
      apiFetch("/api/sessions/" + encodeURIComponent(sessionID) + "/events", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      })
        .then(function (response) {
          if (response.ok) { return response.json(); }
          return response.text().then(function (text) {
            throw new Error((text || "").trim() || ("HTTP " + response.status));
          });
        })
        .then(function (accepted) {
          if (token !== runToken) { return; }
          var hint = "";
          if (accepted && accepted.steered) {
            hint = "\u5df2\u63d2\u5165\u5f53\u524d\u8f6e\u6b21\uff1a\u4e0b\u4e00\u6b65\u51b3\u7b56\u524d\u4f1a\u5e26\u7740\u8fd9\u6761\u6d88\u606f\u91cd\u65b0\u89c4\u5212\u3002";
          } else if (accepted && accepted.queued) {
            hint = "\u5df2\u6392\u961f\uff1a\u5f53\u524d\u8f6e\u7ed3\u675f\u540e\u6267\u884c\u3002";
          }
          setBusy(false, hint);
        })
        .catch(function (error) {
          if (error && error.unauthorized) { return; }
          if (token !== runToken) { return; }
          setBusy(false, "\u53d1\u9001\u5931\u8d25\uff1a" + error.message);
        });
    }

    /* Stopping is two things, and only the first one is fully ours to do:
     * drop the client stream, and ask the server to cancel the session. The
     * server marks the session cancelled; whether the in-flight model call dies
     * with it depends on ctx propagation inside the runtime, which this page
     * cannot see. So the copy says "停止" and reports what the server answered,
     * rather than claiming the work definitely ended. */
    var stopping = false;

    function stop() {
      // The button is shown by CSS from the same class, so a click that arrives
      // here is a click on a visible button. The guard that used to sit here
      // tested that class instead, which made the handler depend on a style rule
      // nothing guaranteed — and the listener itself was missing, so the button
      // did nothing at all. `stopping` is the real duplicate-click guard.
      if (stopping) { return; }
      stopping = true;
      runToken += 1;
      setBusy(false, "\u6b63\u5728\u8bf7\u6c42\u505c\u6b62\u2026");
      apiFetch("/api/cancel?session_id=" + encodeURIComponent(sessionID), { method: "POST" })
        .then(function (response) {
          if (response.ok) { return response.json(); }
          return response.text().then(function (text) {
            throw new Error((text || "").trim() || ("HTTP " + response.status));
          });
        })
        .then(function () {
          stopping = false;
          scrollToBottom = true;
          renderRecord(sessionID);
        })
        .catch(function (error) {
          stopping = false;
          if (error && error.unauthorized) { return; }
          setBusy(false, "\u505c\u6b62\u5931\u8d25\uff1a" + error.message);
        });
    }

    input.addEventListener("keydown", function (event) {
      // isComposing: an Enter that confirms an IME candidate must not send.
      if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        submit();
      }
    });
    input.addEventListener("input", autosize);
    sendButton.addEventListener("click", submit);
    stopButton.addEventListener("click", stop);
    attachButton.addEventListener("click", function () { filePicker.click(); });
    filePicker.addEventListener("change", function () {
      addFiles(filePicker.files);
      filePicker.value = "";
    });
    box.addEventListener("paste", function (event) {
      var files = (event.clipboardData && event.clipboardData.files) || [];
      if (!files.length) { return; } // plain text keeps the browser default
      event.preventDefault();
      addFiles(files);
    });
    box.addEventListener("dragover", function (event) {
      event.preventDefault();
      box.classList.add("dragging");
    });
    box.addEventListener("dragleave", function () { box.classList.remove("dragging"); });
    box.addEventListener("drop", function (event) {
      event.preventDefault();
      box.classList.remove("dragging");
      if (event.dataTransfer && event.dataTransfer.files) { addFiles(event.dataTransfer.files); }
    });

    refreshChips();
    return box;
  }

  /* ---------- live run ---------- */
  /* Sending a turn used to mean staring at a disabled button for minutes: the
   * POST answers only when the whole flow turn is done. This subscribes to the
   * session event stream first, then posts, so the transcript grows while the
   * turn runs — tool calls, team dispatches and agent messages appear as they
   * are written.
   *
   * The stream is read with fetch + ReadableStream rather than EventSource:
   * EventSource cannot send the Authorization header, and this endpoint is
   * behind the bearer token.
   */
  function lastSeqOf(record) {
    var max = 0;
    ((record && record.events) || []).forEach(function (event) {
      if (event.seq > max) { max = event.seq; }
    });
    return max;
  }

  function subscribeLive(sessionID, lastSeq, onEvent, onError) {
    var controller = new AbortController();
    var stopped = false;
    apiFetch("/api/stream?session_id=" + encodeURIComponent(sessionID) + "&follow=1", {
      headers: { "Last-Event-ID": String(lastSeq || 0) },
      signal: controller.signal
    })
      .then(function (response) {
        if (!response.ok || !response.body) { throw new Error("HTTP " + response.status); }
        var reader = response.body.getReader();
        var decoder = new TextDecoder();
        var buffer = "";
        function parseFrame(frame) {
          var payload = [];
          frame.split("\n").forEach(function (line) {
            if (line.indexOf("data:") === 0) { payload.push(line.slice(5).trim()); }
          });
          if (!payload.length) { return; } // id-only or `event: task` frames
          try { onEvent(JSON.parse(payload.join("\n"))); } catch (error) { /* skip malformed */ }
        }
        function pump() {
          return reader.read().then(function (chunk) {
            if (chunk.done) { return; }
            buffer += decoder.decode(chunk.value, { stream: true });
            var index;
            while ((index = buffer.indexOf("\n\n")) >= 0) {
              var frame = buffer.slice(0, index);
              buffer = buffer.slice(index + 2);
              parseFrame(frame);
            }
            return pump();
          });
        }
        return pump();
      })
      .catch(function (error) {
        if (stopped) { return; }
        if (error && error.unauthorized) { return; }
        if (error && error.name === "AbortError") { return; }
        onError(error);
      });
    return function () { stopped = true; controller.abort(); };
  }

  /* One flow turn, built event by event. Mirrors renderTurn's markup so a live
   * turn and the same turn replayed from history look alike. */
  function createLiveTurn(flowTurnID) {
    var view = {
      id: flowTurnID,
      box: el("div", { class: "turn live" }),
      head: el("div", { class: "turn-head" }),
      tags: el("span", { class: "tags" }),
      timing: el("span", { class: "turn-span", title: "\u8fd0\u884c\u4e2d" }, "\u8fd0\u884c\u4e2d\u2026"),
      body: el("div", null),
      tools: {},
      spans: {},
      lastSeqOfCall: {},
      lastText: "",
      startedAt: "",
      endedAt: "",
      reply: "",
      error: ""
    };
    view.head.appendChild(el("span", { class: "t" }, shortId(flowTurnID)));
    view.head.appendChild(view.tags);
    view.head.appendChild(view.timing);
    view.box.appendChild(view.head);
    view.box.appendChild(view.body);
    view.run = toolRun(view.body);

    view.push = function (event) {
      var payload = event.payload || {};
      var call = event.call_turn_id || "";

      if (event.type === "session.turn.started") {
        view.startedAt = event.created_at || "";
        if (payload.input || turnAttachments(payload).length) {
          var liveMessage = el("div", { class: "msg from-user" },
            el("div", { class: "who" }, "\u4f60", stamp(event.created_at)),
            el("div", { class: "body" }, payload.input || ""));
          var liveAttachments = turnAttachments(payload);
          if (liveAttachments.length) { liveMessage.appendChild(attachmentChips(liveAttachments)); }
          view.body.appendChild(liveMessage);
        }
        return;
      }

      /* A message the user sent while this turn was running. It belongs inside
       * the turn, at the point it was taken: the answer below is replying to it
       * too, and a reader who cannot see it would think the turn changed its
       * mind on its own. */
      if (event.type === "session.user_input.injected") {
        view.run.segment();
        view.body.appendChild(el("div", { class: "msg from-user" },
          el("div", { class: "who" }, "\u4f60\uff08\u63d2\u8bdd\uff09", stamp(event.created_at)),
          el("div", { class: "body" }, payload.input || "")));
        return;
      }

      if (event.type === "session.agent_turn.started" && call) {
        view.spans[call] = { start: event.created_at || "", end: "" };
        return;
      }
      if (event.type === "session.agent_turn.completed" && call) {
        if (!view.spans[call]) { view.spans[call] = { start: "", end: "" }; }
        view.spans[call].end = event.created_at || "";
        return;
      }

      if (event.type === "session.team_turn.started") {
        var team = (payload.team_turn && payload.team_turn.team_id) || event.team_id || "";
        var teamID = (payload.team_turn && payload.team_turn.id) || "";
        var row = el("div", { class: "step team-step", "data-team-turn": teamID },
          el("span", { class: "step-kind" }, "\u6d3e\u53d1"),
          el("span", { class: "step-target" }, team || "team"),
          stamp(event.created_at),
          el("span", { class: "step-state" }, "\u8fd0\u884c\u4e2d"));
        view.body.appendChild(row);
        view["team_" + teamID] = row;
        return;
      }
      if (event.type === "session.team_turn.ended") {
        // The terminal event carries the finished turn under team_result.turn.
        var doneTurn = (payload.team_result || {}).turn || {};
        var doneID = doneTurn.id || event.team_turn_id || "";
        var doneRow = view["team_" + doneID];
        if (doneRow) {
          var state = doneRow.querySelector(".step-state");
          if (state) { state.textContent = "\u5b8c\u6210"; }
          var took = elapsed(doneTurn.started_at || "", event.created_at || "");
          if (took) { doneRow.appendChild(el("span", { class: "took" }, took)); }
        }
        return;
      }

      if (event.type === "session.agent.model_response") {
        var reply = modelReply(payload);
        if (!reply.body) { return; }
        // The tools that follow this message are what it led to; keeping them
        // out of the fold above is what makes the message readable as a step.
        view.run.segment();
        view.lastText = reply.body;
        view.lastSeqOfCall[call] = event.seq;
        var body = el("div", { class: "body markdown" }, renderMarkdown(reply.body));
        if (reply.raw) {
          body.appendChild(el("details", { class: "raw-reply" },
            el("summary", null, "\u539f\u59cb\u8f93\u51fa"),
            el("pre", null, reply.raw)));
        }
        var speaker = speakerLabel(event);
        var message = el("div", { class: "msg from-agent" },
          el("div", { class: "who" },
            speaker.team ? el("span", { class: "who-team" }, speaker.team + " / ") : null,
            speaker.agent || "\u52a9\u624b",
            stamp(event.created_at),
            el("span", { class: "took-slot", "data-call": call })),
          body);
        var copy = el("button", { type: "button", class: "copy-msg", title: "\u590d\u5236\u8fd9\u6761\u56de\u590d" }, "\u590d\u5236");
        copy.addEventListener("click", function () { copyText(reply.body, copy); });
        message.appendChild(copy);
        view.body.appendChild(message);
        return;
      }

      if (event.type === "session.tool_call.started") {
        var key = payload.tool_call_id || ("seq-" + event.seq);
        var tool = buildTool(payload);
        tool.classList.add("running");
        view.tools[key] = view.run.add(tool);
        return;
      }
      if (event.type === "session.tool_call.completed" || event.type === "session.tool_call.failed") {
        var target = view.tools[payload.tool_call_id || ""];
        if (!target) { return; }
        target.classList.remove("running");
        var label = target.querySelector(".state");
        if (label) { label.textContent = event.type === "session.tool_call.failed" ? "\u5931\u8d25" : "\u6210\u529f"; }
        var pre = target.querySelector("pre.out");
        if (pre) {
          pre.textContent = event.type === "session.tool_call.failed"
            ? (payload.error || "")
            : (typeof payload.content === "string" ? payload.content : JSON.stringify(payload.content || "", null, 2));
        }
        if (event.type === "session.tool_call.failed") {
          target.open = true;
          view.run.failure();
        }
        return;
      }

      // An approval request is a turn that stopped to ask a human. It is a step
      // like a Team dispatch, and the answer is a second field, not a new turn.
      if (event.type === "session.approval.requested") {
        var approval = payload.approval || {};
        var approvalRow = el("div", { class: "step approval-step", "data-request": approval.request_id || "" },
          el("span", { class: "step-kind" }, "\u5ba1\u6279"),
          el("span", { class: "step-target" }, approval.tool_name || "tool"),
          stamp(event.created_at),
          el("span", { class: "step-state" }, "\u7b49\u5f85\u5ba1\u6279"));
        view["approval:" + (approval.request_id || approval.tool_call_id || "")] = approvalRow;
        view.body.appendChild(approvalRow);
        return;
      }
      if (event.type === "session.approval.resolved") {
        var decision = payload.approval || {};
        var pendingRow = view["approval:" + (decision.request_id || "")];
        if (!pendingRow) { return; }
        var pendingState = pendingRow.querySelector(".step-state");
        if (pendingState) { pendingState.textContent = decision.approved ? "\u5df2\u6279\u51c6" : "\u5df2\u62d2\u7edd"; }
        if (decision.reason) { pendingRow.appendChild(el("span", { class: "took" }, decision.reason)); }
        return;
      }
    };

    view.finish = function (event) {
      var payload = event.payload || {};
      view.endedAt = event.created_at || "";
      view.origin = payload.origin || "";
      view.reply = payload.reply || "";
      view.error = payload.error || "";
      view.intermediate = payload.intermediate === true;
      if (payload.limit) {
        view.body.appendChild(el("div", { class: "error-box" },
          "\u9650\u5236 " + payload.limit.name + " \u89e6\u53d1\uff08\u9608\u503c " + payload.limit.value +
          "\uff0c\u5b9e\u9645 " + payload.limit.observed + "\uff09"));
      } else if (view.error) {
        view.body.appendChild(el("div", { class: "error-box" }, view.error));
      } else if (view.reply && view.reply.trim() !== view.lastText.trim()) {
        view.body.appendChild(el("div", { class: "msg from-agent" },
          el("div", { class: "who" }, "\u56de\u5408\u7b54\u590d", stamp(event.created_at)),
          el("div", { class: "body markdown" }, renderMarkdown(view.reply))));
      }
      if (view.intermediate) { view.tags.appendChild(tag("\u4e2d\u95f4\u56de\u5408")); }
      if (view.origin === "wakeup") { view.tags.appendChild(tag("\u5b9a\u65f6\u68c0\u67e5", "warn")); }
      // Same four fields the static renderer reads off the same event: the
      // deliveries a Team sent the user, where the reply came from, and how the
      // turn ended.
      renderDeliveries(view.body, payload.deliveries);
      renderContributors(view.body, payload.contributors);
      renderTurnOutcome(view.body, payload.stop_reason, payload.awaiting);
      foldProcess(view.body);
      // Durations land once the call actually has an end; until then the
      // per-message slot stays empty rather than guessing.
      Object.keys(view.spans).forEach(function (callID) {
        var span = view.spans[callID];
        var took = elapsed(span.start, span.end);
        if (!took) { return; }
        var slot = view.body.querySelector('.took-slot[data-call="' + callID + '"]');
        if (slot) { slot.textContent = took; }
      });
      var total = elapsed(view.startedAt, view.endedAt);
      view.timing.textContent = total;
      view.timing.title = "";
      view.box.classList.remove("live");
    };

    /* The closing frame can be missed — the stream may drop right after the
     * turn ends. The POST already told us the turn is over, so settle rather
     * than leave a turn pulsing "运行中…" forever. */
    view.settle = function () {
      view.box.classList.remove("live");
      if (view.timing.textContent === "\u8fd0\u884c\u4e2d\u2026") { view.timing.textContent = ""; }
    };

    return view;
  }

  /* The transcript follows the session while the page is open.
   *
   * Subscribing only for the duration of one POST covers the turn the reader
   * sent and nothing else: a dispatched team comes back minutes later, and a
   * scheduled wake-up has no request behind it at all, so those turns landed in
   * the log with the page showing nothing new until a reload. */
  var sessionStream = null;

  /* Live views for turns the record still had open when it was read. renderChat
   * builds them from the record; attachSessionStream takes them over and keeps
   * appending, so a turn that was already running keeps growing on screen. */
  var liveTurnViews = {};

  /* A turn is active from its started event to its terminal one. The stop
   * button follows this rather than the composer's sending state: a message
   * that was accepted but whose turn is still running must keep 停止 available. */
  var activeTurns = 0;
  var RUNNING_HINT = "\u6b63\u5728\u6267\u884c\u2026\u8fc7\u7a0b\u4f1a\u5b9e\u65f6\u51fa\u73b0\u5728\u5bf9\u8bdd\u91cc\u3002";

  /* The stop button and the running hint follow the turn, not the composer's
   * sending state: an accepted message frees the composer at once, while the
   * turn it started may run for minutes. */
  /* A flow turn ends exactly once, through session.turn.ended: stop_reason
   * says how, awaiting says who it is waiting for. There is no second name to
   * remember. */
  function isTurnEndType(type) {
    return type === "session.turn.ended";
  }

  /* Does the record still hold a turn open?
   *
   * The events decide, not the session status: status is written when a turn
   * starts and rewritten when it ends, so a process killed mid-turn leaves it
   * "running" for good — and with the status as the source, the stop button then
   * never goes away. The events are what the runtime folds anyway: a turn is
   * active from its started event until its terminal one. */
  /* Which turns the record still has open: started, no terminal event. They are
   * the ones whose later events describe work the page has not drawn yet. */
  function openTurnIds(events) {
    var open = {};
    (events || []).forEach(function (event) {
      if (!event || !event.type) { return; }
      var id = event.flow_turn_id;
      if (!id) { return; }
      if (event.type === "session.turn.started") { open[id] = true; return; }
      if (isTurnEndType(event.type)) { delete open[id]; }
    });
    return open;
  }

  function turnsOpenIn(events) {
    return Object.keys(openTurnIds(events)).length > 0;
  }

  function markTurnActive(delta) {
    activeTurns = Math.max(0, activeTurns + delta);
    syncTurnChrome();
  }

  /* Paints the running state onto the chrome.
   *
   * Split out from markTurnActive because of the render order: the record view
   * renders the transcript (and subscribes the stream) before the composer
   * exists, so the first markTurnActive has no status line to write to. The
   * composer render calls this again. */
  function syncTurnChrome() {
    var active = activeTurns > 0;
    document.body.classList.toggle("turn-active", active);
    // The hint shares the status line with attachment and send errors, so only
    // clear the text this function wrote.
    var status = document.querySelector(".composer-status");
    if (!status) { return; }
    if (active) {
      status.textContent = RUNNING_HINT;
      status.dataset.turnHint = "1";
    } else if (status.dataset.turnHint) {
      status.textContent = "";
      delete status.dataset.turnHint;
    }
  }

  function stopSessionStream() {
    if (sessionStream) { sessionStream.stop(); sessionStream = null; }
  }

  function attachSessionStream(body) {
    stopSessionStream();
    if (!body || !currentRecord) { return; }
    // record.session carries {id, status}; keep accepting a bare string in case
    // an older record is replayed.
    var sessionID = currentRecord.session;
    var sessionStatus = "";
    if (sessionID && sessionID.id) {
      sessionStatus = sessionID.status || "";
      sessionID = sessionID.id;
    }
    var container = body.querySelector(".chat");
    if (!sessionID || !container) { return; }

    var known = {};   // flow turns already rendered from the record
    groupByTurn(currentRecord.events || []).forEach(function (turn) { known[turn.id] = true; });
    // Rebuilding the stream re-derives the counter from the record. Without
    // this reset, switching views back and forth would stack counts and leave
    // the stop button on after the turn ended.
    activeTurns = 0;
    document.body.classList.remove("turn-active");
    // A turn that was already running when the page loaded will not emit
    // another started event — its started is in the record — so the counter is
    // primed from the record. A terminal session status still wins in the other
    // direction: it means the session is over, whatever the fold holds open
    // (Cancel, for one, returns before the terminal event is written).
    var sessionOver = sessionStatus === "completed" || sessionStatus === "failed" ||
      sessionStatus === "cancelled";
    var openIds = Object.keys(openTurnIds(currentRecord.events || []));
    if (!sessionOver) { openIds.forEach(function () { markTurnActive(1); }); }
    // Turns the record left open already have a live view; the ones that end
    // later are created here.
    var turns = liveTurnViews;
    liveTurnViews = {};
    var lastSeq = lastSeqOf(currentRecord);
    var stopped = false;
    var stopFn = null;
    var retry = 1000;

    var settled = {};
    function isTerminalTurn(type) { return isTurnEndType(type); }
    function settleTurn(id) {
      if (settled[id]) { return; }
      settled[id] = true;
      markTurnActive(-1);
    }

    function turnFor(id) {
      if (!turns[id]) {
        turns[id] = createLiveTurn(id);
        container.appendChild(turns[id].box);
      }
      return turns[id];
    }

    function onEvent(event) {
      if (!event || !event.type) { return; }
      retry = 1000;
      if (typeof event.seq === "number" && event.seq > lastSeq) { lastSeq = event.seq; }
      /* Keep the loaded record in step: switching mode re-renders from it, and
       * resuming the stream uses its last seq. */
      if (currentRecord) { (currentRecord.events = currentRecord.events || []).push(event); }

      var id = event.flow_turn_id || "flow";
      if (known[id]) {
        var live = turns[id];
        if (!live) {
          // This turn is already on screen as a finished block of history.
          // Drawing its later events would duplicate it; the record is what the
          // page renders from. Its terminal event still has to settle the
          // counter primed from the record.
          if (isTerminalTurn(event.type)) { settleTurn(id); }
          return;
        }
        // The record was read while this turn was still running, so the page
        // holds a snapshot of it. What arrives now is the rest of the same
        // turn: append it to the view the record already produced.
        live.push(event);
        if (isTerminalTurn(event.type)) {
          settleTurn(id);
          live.finish(event);
        } else {
          applyRunEvent(event);
          refreshRunView();
        }
        scrollRecordToBottom();
        return;
      }
      var view = turnFor(id);
      view.push(event);
      if (event.type === "session.turn.started") { markTurnActive(1); }
      if (isTerminalTurn(event.type)) {
        settleTurn(id);
        view.finish(event);
        return;
      }
      applyRunEvent(event);
      refreshRunView();
      scrollRecordToBottom();
    }

    function connect() {
      stopFn = subscribeLive(sessionID, lastSeq, onEvent, function () {
        if (stopped) { return; }
        setTimeout(function () {
          if (stopped) { return; }
          retry = Math.min(retry * 2, 15000);
          connect();
        }, retry);
      });
    }
    connect();

    sessionStream = {
      stop: function () { stopped = true; if (stopFn) { stopFn(); } }
    };
  }

  /* ---------- chat ---------- */
  /* A session with hundreds of turns would otherwise build every node before
   * the first paint. Older turns stay one click away instead of in the DOM. */
  var CHAT_PAGE_SIZE = 40;

  function renderChat(record) {
    var wrap = el("div", { class: "chat" });
    var turns = groupByTurn(record.events || []);
    // A turn the record still has open is drawn as a live one and handed to the
    // stream, so the events that follow it are appended instead of dropped.
    var open = openTurnIds(record.events || []);
    liveTurnViews = {};
    if (turns.length === 0) {
      wrap.appendChild(el("div", { class: "empty" }, "\u8fd9\u662f\u4e00\u4e2a\u65b0\u4f1a\u8bdd\uff0c\u5728\u4e0b\u9762\u7684\u8f93\u5165\u6846\u8f93\u5165\u7b2c\u4e00\u6761\u6d88\u606f\u3002"));
      return wrap;
    }

    var hiddenCount = Math.max(0, turns.length - CHAT_PAGE_SIZE);
    if (hiddenCount > 0) {
      var older = el("button", { type: "button", class: "load-older" },
        "\u52a0\u8f7d\u66f4\u65e9\u7684 " + hiddenCount + " \u4e2a\u56de\u5408");
      older.addEventListener("click", function () {
        older.remove();
        var fragment = document.createDocumentFragment();
        turns.slice(0, hiddenCount).forEach(function (turn) {
          fragment.appendChild(renderTurn(turn));
        });
        wrap.insertBefore(fragment, wrap.firstChild);
      });
      wrap.appendChild(older);
    }

    turns.slice(hiddenCount).forEach(function (turn) {
      if (open[turn.id]) {
        var live = createLiveTurn(turn.id);
        turn.events.forEach(function (past) { live.push(past); });
        liveTurnViews[turn.id] = live;
        wrap.appendChild(live.box);
        return;
      }
      wrap.appendChild(renderTurn(turn));
    });
    return wrap;
  }

  function groupByTurn(events) {
    var order = [];
    var map = {};
    events.forEach(function (event) {
      // Session-level events (created/updated) belong to no turn. Showing them
      // as an empty turn would add a blank block with no way to act on it.
      if (!event.flow_turn_id) { return; }
      var key = event.flow_turn_id;
      if (!map[key]) {
        map[key] = {
          id: key, events: [], input: "", origin: "", reply: "",
          error: "", intermediate: false, limit: null, startedAt: "", endedAt: "",
          models: [], promptTokens: 0, completionTokens: 0,
          cacheHitTokens: 0, cacheMissTokens: 0, attachments: [],
          deliveries: [], contributors: [], stopReason: "", awaiting: ""
        };
        order.push(map[key]);
      }
      var turn = map[key];
      turn.events.push(event);
      var payload = event.payload || {};
      if (event.type === "session.agent.model_response") {
        var usage = payload.usage || {};
        if (payload.model && turn.models.indexOf(payload.model) < 0) { turn.models.push(payload.model); }
        turn.promptTokens += usage.prompt_tokens || 0;
        turn.completionTokens += usage.completion_tokens || 0;
        // 同一件事有两种字段名（OpenAI 兼容 prompt_cache_hit_tokens / Anthropic 兼容
        // cache_read_input_tokens），而且有网关一次请求两个都发。取较大值，不相加，
        // 否则命中率会翻倍。
        turn.cacheHitTokens += Math.max(usage.prompt_cache_hit_tokens || 0, usage.cache_read_input_tokens || 0);
        turn.cacheMissTokens += usage.prompt_cache_miss_tokens || 0;
      }
      if (event.type === "session.turn.started") {
        if (payload.input) { turn.input = payload.input; }
        turn.attachments = turnAttachments(payload);
        turn.startedAt = event.created_at || turn.startedAt;
      }
      if (event.type === "session.turn.ended") {
        turn.origin = payload.origin || turn.origin;
        turn.reply = payload.reply || turn.reply;
        turn.error = payload.error || turn.error;
        turn.intermediate = payload.intermediate === true;
        if (payload.limit) { turn.limit = payload.limit; }
        // The terminal event carries four more fields this page renders: who
        // contributed to the reply, the messages a Team delivered to the user,
        // how the turn ended and who it waits for.
        if (payload.contributors) { turn.contributors = payload.contributors; }
        if (payload.deliveries) { turn.deliveries = payload.deliveries; }
        turn.stopReason = payload.stop_reason || turn.stopReason;
        turn.awaiting = payload.awaiting || turn.awaiting;
        turn.endedAt = event.created_at || turn.endedAt;
      }
    });
    return order;
  }

  /* How a turn ended, and who it is waiting for. Two fields on one event, not a
   * state to be inferred from the event's name. */
  var STOP_REASON_LABELS = {
    end_turn: "\u6b63\u5e38\u7ed3\u675f",
    max_rounds: "\u8fbe\u5230\u6b65\u6570\u4e0a\u9650",
    max_tool_calls: "\u8fbe\u5230\u5de5\u5177\u8c03\u7528\u4e0a\u9650",
    max_tokens: "\u8fbe\u5230 token \u4e0a\u9650",
    cancelled: "\u5df2\u53d6\u6d88",
    refusal: "\u6a21\u578b\u62d2\u7edd",
    failed: "\u5931\u8d25"
  };
  var AWAITING_LABELS = {
    user: "\u7b49\u5f85\u4f60\u8f93\u5165",
    tool: "\u7b49\u5f85\u5de5\u5177\u8fd4\u56de",
    approval: "\u7b49\u5f85\u5ba1\u6279"
  };

  function renderTurnOutcome(box, stopReason, awaiting) {
    var parts = [];
    if (stopReason && stopReason !== "end_turn") {
      parts.push("\u7ed3\u675f\uff1a" + (STOP_REASON_LABELS[stopReason] || stopReason));
    }
    if (awaiting) {
      parts.push(AWAITING_LABELS[awaiting] || ("\u7b49\u5f85\uff1a" + awaiting));
    }
    if (!parts.length) { return; }
    box.appendChild(el("div", { class: "turn-outcome" }, parts.join(" \u00b7 ")));
  }

  /* A delivery is a message a Team sent the user, carried on the turn's
   * terminal event rather than as a reply: it has to be read as the user's own
   * message, with the Team that sent it named. */
  function renderDeliveries(box, deliveries) {
    (deliveries || []).forEach(function (delivery) {
      var text = (delivery && delivery.text) || "";
      if (!text.trim()) { return; }
      box.appendChild(el("div", { class: "msg delivery" },
        el("div", { class: "who" },
          el("span", { class: "delivery-tag" }, "\u6295\u9012"),
          delivery.team_id || "team"),
        el("div", { class: "body markdown" }, renderMarkdown(text))));
    });
  }

  /* Provenance of the closing answer: which Teams said what. Folded, because it
   * is a second reading of the reply above, not a new one. */
  function renderContributors(box, contributors) {
    if (!contributors || !contributors.length) { return; }
    var details = el("details", { class: "turn-contributors" },
      el("summary", null, "\u6765\u6e90\uff1a" + contributors.map(function (item) {
        return item.team || "team";
      }).join("\u3001")));
    contributors.forEach(function (item) {
      details.appendChild(el("div", { class: "contributor" },
        el("span", { class: "contributor-team" }, item.team || "team"),
        el("span", { class: "contributor-reply" }, item.reply || "")));
    });
    box.appendChild(details);
  }

  /* The first turn carries what the user attached, inside
   * turn.context_blocks[].parts[].media. The transcript used to show only the
   * text, so a message that was mostly a file looked like a sentence. */
  function turnAttachments(payload) {
    var turn = (payload && payload.turn) || {};
    var blocks = turn.context_blocks || [];
    var items = [];
    blocks.forEach(function (block) {
      (block.parts || []).forEach(function (part) {
        // ContentPart serialises its attachment as `source` (provider-neutral
        // wire shape); older recordings and the live-preview path use `media`.
        // Reading only one of them is why an attached image rendered as nothing.
        var media = part && (part.source || part.media);
        if (media) { items.push(media); }
      });
    });
    return items;
  }

  /* Media bytes live on disk, so the page has to fetch them. The bytes are
   * cached per storage ref: the same attachment shows up in several turns and
   * should not be downloaded again. */
  var mediaURLCache = {};

  function loadMedia(ref) {
    if (!ref) { return Promise.reject(new Error("no storage ref")); }
    if (mediaURLCache[ref]) { return Promise.resolve(mediaURLCache[ref]); }
    return apiFetch("/api/media?ref=" + encodeURIComponent(ref)).then(function (response) {
      if (!response.ok) { throw new Error("HTTP " + response.status); }
      return response.blob();
    }).then(function (blob) {
      var url = URL.createObjectURL(blob);
      mediaURLCache[ref] = url;
      return url;
    });
  }

  /* mediaKindOf names the attachment's kind. Recorded parts carry media_type
   * (image/png) but no kind, so reading only `kind` labelled every image a
   * "file" and never rendered it. */
  function mediaKindOf(item) {
    var kind = item.kind || "";
    if (kind) { return kind; }
    var mime = item.media_type || item.mime_type || "";
    if (mime.indexOf("image/") === 0) { return "image"; }
    if (mime.indexOf("video/") === 0) { return "video"; }
    if (mime.indexOf("audio/") === 0) { return "audio"; }
    return "file";
  }

  /* mediaNode renders an attachment inline. It returns null for anything the
   * transcript cannot display, so the caller keeps the chip. */
  function mediaNode(item) {
    var kind = mediaKindOf(item);
    var ref = item.storage_ref || "";
    var node = null;
    if (kind === "image") {
      node = el("img", { class: "msg-media", alt: item.name || "attachment" });
    } else if (kind === "video") {
      node = el("video", { class: "msg-media", controls: "controls" });
    } else if (kind === "audio") {
      node = el("audio", { class: "msg-media", controls: "controls" });
    } else {
      return null;
    }
    if (!ref) { return null; }
    loadMedia(ref).then(function (url) {
      node.src = url;
    }).catch(function () {
      // Keep the chip: the file is gone or unreadable, and a broken <img> says
      // less than the name and size do.
      if (node.parentNode) { node.parentNode.removeChild(node); }
    });
    return node;
  }

  function attachmentChips(items) {
    var row = el("div", { class: "msg-attachments" });
    items.forEach(function (item) {
      var preview = mediaNode(item);
      if (preview) { row.appendChild(preview); }
      row.appendChild(el("span", { class: "chip" },
        el("span", { class: "chip-kind" }, mediaKindOf(item)),
        el("span", { class: "chip-name" }, item.name || "(\u672a\u547d\u540d)"),
        el("span", { class: "chip-size" }, humanSize(item.size_bytes || 0))));
    });
    return row;
  }

  /* Who said it: team_id plus the call name carried in call_turn_id
   * ("<team_turn_id>:<call name>"). One flow turn really can hold several
   * agents — a coordinator that dispatches keeps the sub-agent's turns inside
   * the same flow turn — and without this every one of them renders as the same
   * "助手".
   *
   * Agent names are only unique inside a team: `inspect` runs in both the facts
   * and repo-info teams. So the team belongs in the label, not in a tooltip
   * that needs a mouse to reveal.
   *
   * The call name is not the agent: the coding team's coding call is run by
   * code-fixer. Both layers matter — the team says where the work belongs, the
   * agent says who spoke — so the label always carries both, even when the two
   * names coincide. Dropping the team in that case left the message signed with
   * one word that named neither layer. */
  /* team + call name → the agent that runs it. Built from the record's nodes
   * and the configured topology: the first says what ran, the second also knows
   * the calls that were configured but never started. */
  var callAgents = {};

  function indexCallAgents(record, topology) {
    callAgents = {};
    function put(team, call, agent) {
      if (!team || !call || !agent) { return; }
      callAgents[team + "\u0000" + call] = agent;
    }
    ((record && record.nodes) || []).forEach(function (node) {
      if (node.kind === "call_turn" || node.kind === "agent_session") {
        put(node.team_id, node.call_id, node.agent_id);
      }
    });
    ((topology && topology.teams) || []).forEach(function (team) {
      (team.calls || []).forEach(function (call) {
        put(team.name, call.name, call.agent);
      });
    });
  }

  function speakerLabel(event) {
    var call = event.call_turn_id || "";
    var index = call.lastIndexOf(":");
    var name = index >= 0 ? call.slice(index + 1) : "";
    var team = event.team_id || "";
    if (!name) { return { team: "", agent: team }; }
    return { team: team, agent: callAgents[team + "\u0000" + name] || name };
  }

  /* session.agent.model_response carries the reply twice: `reply` is what the engine
   * unwrapped for the caller, `text` is what the model literally emitted —
   * usually a JSON envelope holding reply/intent_hint/next.action/user_request.
   * Reading the envelope aloud is noise, so show `reply` and keep `text` behind
   * a fold; the envelope is the only place intent_hint and next.action survive.
   *
   * Older events have no `reply`. For those, unwrap a JSON envelope here rather
   * than printing the braces. Anything that is not an envelope passes through
   * untouched. */
  function modelReply(payload) {
    var reply = typeof payload.reply === "string" ? payload.reply.trim() : "";
    var text = typeof payload.text === "string" ? payload.text.trim() : "";
    if (reply) {
      return { body: reply, raw: text && text !== reply ? text : "" };
    }
    if (text.charAt(0) === "{") {
      try {
        var parsed = JSON.parse(text);
        if (parsed && typeof parsed.reply === "string" && parsed.reply.trim()) {
          return { body: parsed.reply.trim(), raw: text };
        }
      } catch (error) { /* not an envelope: fall through to the raw text */ }
    }
    return { body: text, raw: "" };
  }

  /* The reasoning behind an answer — the messages that thought aloud and the
   * tools they called — reads as one block once the turn is over: what a reader
   * wants first is the conclusion, and the process is one click away instead of
   * forty rows of scrolling. The last message is the conclusion and stays out;
   * a turn with nothing behind it is not folded at all. */
  function foldProcess(body) {
    var messages = [];
    Array.prototype.forEach.call(body.children, function (node) {
      if (node.classList && node.classList.contains("msg") &&
        node.classList.contains("from-agent")) {
        messages.push(node);
      }
    });
    if (!messages.length) { return; }
    // Calls, not folds: the header counts what ran, not how it was grouped.
    var tools = body.querySelectorAll(".tool").length;
    if (messages.length < 2 && tools === 0) { return; }

    var first = messages[0];
    var last = messages[messages.length - 1];
    var collected = [];
    for (var node = first; node && node !== last; node = node.nextSibling) {
      collected.push(node);
    }
    if (!collected.length) { return; }

    var fold = el("details", { class: "turn-process" },
      el("summary", null, "\u601d\u8003\u8fc7\u7a0b \u00b7 " + messages.length +
        " \u6bb5 \u00b7 " + tools + " \u6b21\u5de5\u5177"),
      el("div", { class: "turn-process-body" }));
    body.insertBefore(fold, first);
    var target = fold.querySelector(".turn-process-body");
    collected.forEach(function (item) { target.appendChild(item); });
  }

  function renderTurn(turn) {
    // A dispatch or resume turn has no words of the user in it: labelling it
    // with its id made one question read as four 回合. The id stays in the
    // tooltip, and the block is indented because it belongs to the turn above.
    // A dispatch turn never emits session.turn.started (it is not the entry
    // turn), so its startedAt is empty; that plus the origin tells the two
    // apart without trusting one field alone.
    var userTurn = Boolean(turn.startedAt) && (turn.origin === "user" || turn.origin === "");
    var cls = "turn";
    if (!userTurn) { cls += " exec"; }
    if (turn.origin === "wakeup") { cls += " wakeup"; }
    if (turn.error) { cls += " failed"; }
    var box = el("div", { class: cls });

    /* Per-call spans, so a message can say how long its agent actually ran.
     * session.agent_turn.started/completed carry the same call_turn_id as the messages;
     * a call that never completed (still running, or the turn was cut) simply
     * has no end and shows no duration. */
    var spans = {};
    var lastSeqOfCall = {};
    turn.events.forEach(function (event) {
      var call = event.call_turn_id || "";
      if (!call) { return; }
      if (event.type === "session.agent_turn.started") {
        spans[call] = { start: event.created_at || "", end: "" };
      }
      if (event.type === "session.agent_turn.completed") {
        if (!spans[call]) { spans[call] = { start: "", end: "" }; }
        spans[call].end = event.created_at || "";
      }
      if (event.type === "session.agent.model_response") {
        lastSeqOfCall[call] = event.seq;
      }
    });

    var head = el("div", { class: "turn-head" },
      el("span", { class: "t", title: turn.id }, userTurn ? "回合" : "执行轮"));
    if (turn.origin === "wakeup") { head.appendChild(tag("定时检查", "warn")); }
    // "中间回合" is engine vocabulary for "this reply is a step, not the answer".
    // What the reader needs to know is that the work was handed on.
    if (turn.intermediate) {
      head.appendChild(tag("已派发", null, "这一轮只是把活派下去，结果稍后回到这里"));
    }
    if (turn.error && String(turn.error).toLowerCase().indexOf("cancel") >= 0) {
      head.appendChild(tag("\u5df2\u4e2d\u65ad", "warn"));
    }
    if (turn.limit) { head.appendChild(tag("\u88ab " + turn.limit.name + " \u7ec8\u6b62", "danger")); }
    var turnSpan = elapsed(turn.startedAt, turn.endedAt);
    if (turnSpan) { head.appendChild(el("span", { class: "turn-span" }, turnSpan)); }
    if (turn.promptTokens || turn.completionTokens) {
      var usageTitle = "\u8f93\u5165 / \u8f93\u51fa token";
      var usageText = (turn.models.length ? turn.models.join(", ") + " \u00b7 " : "") +
        tokenCount(turn.promptTokens) + " / " + tokenCount(turn.completionTokens) + " tok";
      var turnRate = cacheRate(turn.cacheHitTokens, turn.cacheMissTokens, turn.promptTokens);
      if (turnRate) {
        usageTitle += "\uff0c\u5176\u4e2d\u7f13\u5b58\u547d\u4e2d " + tokenCount(turn.cacheHitTokens) + " token";
        usageText += " \u00b7 \u7f13\u5b58 " + turnRate;
      }
      head.appendChild(el("span", { class: "turn-usage", title: usageTitle }, usageText));
    }
    box.appendChild(head);

    if (turn.input) {
      var userMessage = el("div", { class: "msg from-user" },
        el("div", { class: "who" },
          "\u4f60",
          stamp(turn.startedAt)),
        el("div", { class: "body" }, turn.input));
      if (turn.attachments && turn.attachments.length) {
        userMessage.appendChild(attachmentChips(turn.attachments));
      }
      box.appendChild(userMessage);
    }

    var tools = {};
    var teamRows = {};
    var lastText = "";
    var run = toolRun(box);
    turn.events.forEach(function (event) {
      var payload = event.payload || {};
      // Input the user sent while this turn was running: same reason as the live
      // view — the reply below is answering it, so it has to be visible.
      if (event.type === "session.user_input.injected") {
        run.segment();
        box.appendChild(el("div", { class: "msg from-user" },
          el("div", { class: "who" }, "\u4f60\uff08\u63d2\u8bdd\uff09", stamp(event.created_at)),
          el("div", { class: "body" }, payload.input || "")));
        return;
      }
      if (event.type === "session.agent.model_response") {
        var view = modelReply(payload);
        if (view.body) {
          // Same boundary as the live renderer: a message ends the current tool
          // fold, so the tools below it belong to this message, not the one above.
          run.segment();
          lastText = view.body;
          var body = el("div", { class: "body markdown" }, renderMarkdown(view.body));
          if (view.raw) {
            body.appendChild(el("details", { class: "raw-reply" },
              el("summary", null, "\u539f\u59cb\u8f93\u51fa"),
              el("pre", null, view.raw)));
          }
          var speaker = speakerLabel(event);
          var span = spans[event.call_turn_id || ""] || {};
          var took = lastSeqOfCall[event.call_turn_id || ""] === event.seq ? elapsed(span.start, span.end) : "";
          var message = el("div", { class: "msg from-agent" },
            el("div", { class: "who" },
              speaker.team ? el("span", { class: "who-team" }, speaker.team + " / ") : null,
              speaker.agent || "\u52a9\u624b",
              stamp(event.created_at),
              took ? el("span", { class: "took", title: "\u8fd9\u4e2a agent \u8fd9\u4e00\u8f6e\u7684\u8017\u65f6" }, took) : null),
            body);
          var copy = el("button", { type: "button", class: "copy-msg", title: "\u590d\u5236\u8fd9\u6761\u56de\u590d" }, "\u590d\u5236");
          copy.addEventListener("click", function () { copyText(view.body, copy); });
          message.appendChild(copy);
          box.appendChild(message);
        }
        return;
      }
      if (event.type === "session.tool_call.started") {
        var key = payload.tool_call_id || ("seq-" + event.seq);
        tools[key] = run.add(buildTool(payload));
        return;
      }
      if (event.type === "session.tool_call.completed" || event.type === "session.tool_call.failed") {
        var node = tools[payload.tool_call_id || ""];
        if (!node) { return; }
        var state = node.querySelector(".state");
        if (state) { state.textContent = event.type === "session.tool_call.failed" ? "\u5931\u8d25" : "\u6210\u529f"; }
        var pre = node.querySelector("pre.out");
        if (pre) {
          var output = event.type === "session.tool_call.failed"
            ? (payload.error || "")
            : (typeof payload.content === "string" ? payload.content : JSON.stringify(payload.content || "", null, 2));
          pre.textContent = output;
        }
        if (event.type === "session.tool_call.failed") {
          node.open = true;
          run.failure();
        }
        return;
      }
      /* The same two rows the live renderer builds, built from the same events:
       * a Team dispatch and the approval request it may raise. Without them a
       * reload dropped the dispatch line and never showed a pending approval at
       * all, even though both are in the record.
       */
      if (event.type === "session.team_turn.started") {
        var teamTurn = payload.team_turn || {};
        var teamTurnID = teamTurn.id || "";
        if (!teamTurnID) { return; }
        var teamRow = el("div", { class: "step team-step", "data-team-turn": teamTurnID },
          el("span", { class: "step-kind" }, "\u6d3e\u53d1"),
          el("span", { class: "step-target" }, teamTurn.team_id || event.team_id || "team"),
          stamp(event.created_at),
          el("span", { class: "step-state" }, "\u8fd0\u884c\u4e2d"));
        teamRows[teamTurnID] = teamRow;
        box.appendChild(teamRow);
        return;
      }
      if (event.type === "session.team_turn.ended") {
        var endedTurn = (payload.team_result || {}).turn || {};
        var endedRow = teamRows[endedTurn.id || event.team_turn_id || ""];
        if (!endedRow) { return; }
        var endedState = endedRow.querySelector(".step-state");
        if (endedState) { endedState.textContent = "\u5b8c\u6210"; }
        var teamTook = elapsed(endedTurn.started_at || "", event.created_at || "");
        if (teamTook) { endedRow.appendChild(el("span", { class: "took" }, teamTook)); }
        return;
      }
      if (event.type === "session.approval.requested") {
        var approval = payload.approval || {};
        var approvalRow = el("div", { class: "step approval-step", "data-request": approval.request_id || "" },
          el("span", { class: "step-kind" }, "\u5ba1\u6279"),
          el("span", { class: "step-target" }, approval.tool_name || "tool"),
          stamp(event.created_at),
          el("span", { class: "step-state" }, "\u7b49\u5f85\u5ba1\u6279"));
        teamRows["approval:" + (approval.request_id || approval.tool_call_id || "")] = approvalRow;
        box.appendChild(approvalRow);
        return;
      }
      if (event.type === "session.approval.resolved") {
        var decision = payload.approval || {};
        var pendingRow = teamRows["approval:" + (decision.request_id || "")];
        if (!pendingRow) { return; }
        var pendingState = pendingRow.querySelector(".step-state");
        if (pendingState) { pendingState.textContent = decision.approved ? "\u5df2\u6279\u51c6" : "\u5df2\u62d2\u7edd"; }
        if (decision.reason) { pendingRow.appendChild(el("span", { class: "took" }, decision.reason)); }
        return;
      }
    });

    if (turn.limit) {
      box.appendChild(el("div", { class: "error-box" },
        "\u9650\u5236 " + turn.limit.name + " \u89e6\u53d1\uff08\u9608\u503c " + turn.limit.value +
        "\uff0c\u5b9e\u9645 " + turn.limit.observed + "\uff09\n" +
        "\u6700\u9891\u7e41\u7684\u8fb9\uff1a" + (turn.limit.top_pairs || []).join("\u3001") + "\n" +
        "\u6700\u8fd1\uff1a" + (turn.limit.recent || []).join(" \u2192 ")));
    } else if (turn.error) {
      box.appendChild(el("div", { class: "error-box" }, turn.error));
    } else if (turn.reply && turn.reply.trim() !== lastText.trim()) {
      // The turn's reply is often the last model response restated. Rendering
      // both would show the same paragraph twice with no hint why.
      //
      // When it does differ it is the turn's closing answer — one level above
      // the individual agent messages, possibly a coordinator summary of them —
      // so it is labelled by level rather than by agent.
      box.appendChild(el("div", { class: "msg from-agent" + (turn.intermediate ? " intermediate" : "") },
        el("div", { class: "who" }, "\u56de\u5408\u7b54\u590d", stamp(turn.endedAt)),
        el("div", { class: "body markdown" }, renderMarkdown(turn.reply))));
    }
    renderDeliveries(box, turn.deliveries);
    renderContributors(box, turn.contributors);
    renderTurnOutcome(box, turn.stopReason, turn.awaiting);
    foldProcess(box);
    return box;
  }

  /* A tool call reads better as "what did it act on" than as a JSON blob: Bash
   * wants its command, Read/Write their path, Team its description. The full
   * arguments stay available in the expanded body. */
  function toolSummary(name, args) {
    var value = args || {};
    if (typeof value === "string") { return value.slice(0, 160); }
    if (value.command) { return String(value.command).split("\n")[0].slice(0, 160); }
    if (value.file_path) { return value.file_path; }
    if (value.pattern) { return value.pattern + (value.path ? "  " + value.path : ""); }
    if (value.description) { return value.description.slice(0, 160); }
    if (value.query) { return String(value.query).slice(0, 160); }
    var json = JSON.stringify(value);
    if (!json || json === "{}") { return ""; }
    return json.slice(0, 120);
  }

  function buildTool(call) {
    var args = call.arguments || {};
    return el("details", { class: "tool" },
      el("summary", null,
        el("span", { class: "name" }, call.tool_name || "tool"),
        el("span", { class: "arg" }, toolSummary(call.tool_name, args)),
        el("span", { class: "state" }, "\u8fdb\u884c\u4e2d")),
      el("div", { class: "tool-body" },
        Object.keys(args).length ? el("pre", { class: "args" }, JSON.stringify(args, null, 2)) : null,
        el("pre", { class: "out" }, "")));
  }

  /* A turn's tool calls, folded behind a header per message.
   *
   * A question can leave dozens of calls behind it, and one line per call turns
   * the transcript into a wall of tool rows that has to be scrolled past to
   * find the next thing the agent said. Each fold covers the calls one message
   * led to, so the reader can see which message prompted which calls.
   *
   * A fold that holds a single call still folds, and names that call on its
   * header line: a bare row among folded groups breaks the rhythm, and the
   * header would otherwise hide the only thing it holds. */
  function toolRun(parent) {
    var run = { group: null, body: null, cards: [], failures: 0, count: null, fails: null };

    function paint() {
      if (!run.group) { return; }
      var label = "\u5de5\u5177\u8c03\u7528 " + run.cards.length + " \u6b21";
      // A fold that holds one call would otherwise hide the only thing it has:
      // name that call on the header line.
      if (run.cards.length === 1) {
        var name = run.cards[0].querySelector(".name");
        var arg = run.cards[0].querySelector(".arg");
        var detail = [(name && name.textContent) || "", (arg && arg.textContent) || ""]
          .join(" ").trim();
        if (detail) { label += " \u00b7 " + detail; }
      }
      run.count.textContent = label;
      run.fails.textContent = run.failures ? " \u00b7 " + run.failures + " \u5931\u8d25" : "";
    }

    /* Every call goes into a fold, even a lone one: a message followed by one
     * raw tool row and a message followed by a folded group read as two
     * different things, and the row is what breaks the rhythm of the
     * transcript. */
    run.add = function (node) {
      if (!run.group) {
        run.count = el("span", { class: "count" });
        run.fails = el("span", { class: "fails" });
        run.group = el("details", { class: "tool-group" },
          el("summary", null, run.count, run.fails));
        run.body = el("div", { class: "tool-group-body" });
        run.group.appendChild(run.body);
        parent.appendChild(run.group);
      }
      run.body.appendChild(node);
      run.cards.push(node);
      paint();
      return node;
    };

    run.failure = function () {
      run.failures += 1;
      paint();
    };

    /* A message closes the current fold: the tools after it are what that
     * message led to, and folding them together with the ones it explained
     * makes the next message look unmotivated. */
    run.segment = function () {
      run.group = null;
      run.body = null;
      run.cards = [];
      run.failures = 0;
      run.count = null;
      run.fails = null;
    };

    return run;
  }

  /* ---------- run board ---------- */
  /* Answers one question only: which teams and agents are running right now.
   * Everything else about a turn (what it said, which tools it called, how long
   * each step took) already has a view; this one is the status board. */
  var runState = { teams: [], agents: [] };

  /* The configured graph for the session being viewed, or null when the server
   * does not expose /api/topology. Events alone can only describe what ran; the
   * graph is what lets the board show the Teams that have not started yet. */
  var topologyState = null;

  function resetRunState(record) {
    runState = { teams: [], agents: [] };
    ((record && record.events) || []).forEach(applyRunEvent);
    inferDispatchParents(record);
  }

  function runEntry(group, key) {
    for (var index = 0; index < runState[group].length; index += 1) {
      if (runState[group][index].key === key) { return runState[group][index]; }
    }
    return null;
  }

  function upsertRunEntry(group, key, name, team) {
    var entry = runEntry(group, key);
    if (entry) { return entry; }
    entry = { key: key, name: name, team: team, status: "pending", startedAt: "", endedAt: "" };
    runState[group].push(entry);
    return entry;
  }

  /* The board shows entities, not history: one card per team_session and one
   * per agent_session, with the session's own status. A team that ran four
   * turns is one card, and it reads "空闲" once nothing is running. */
  function applyRunEvent(event) {
    var type = event.type || "";
    var payload = event.payload || {};

    // Matched on the middle segment, not on a prefix: every event type lives
    // under the `session.` namespace now, so a prefix test for "team_session."
    // never fires and the board renders empty.
    if (type.indexOf("team_session.") >= 0) {
      var teamSession = payload.team_session || {};
      var teamKey = teamSession.id || "";
      if (!teamKey) { return; }
      var teamEntry = upsertRunEntry("teams", teamKey, teamSession.team_id || event.team_id || "?", "");
      if (type === "session.team_session.created") {
        teamEntry.startedAt = teamSession.created_at || "";
      }
      teamEntry.status = teamSession.status || teamEntry.status;
      return;
    }

    if (type.indexOf("agent_session.") >= 0) {
      var agentSession = payload.agent_session || {};
      var agentKey = agentSession.id || "";
      if (!agentKey) { return; }
      // team_session_id reads "fs_x:default" — the tail is the team's name.
      var parts = (agentSession.team_session_id || "").split(":");
      var agentEntry = upsertRunEntry("agents", agentKey,
        agentSession.agent_id || agentSession.call_id || "?", parts[parts.length - 1] || "");
      agentEntry.teamSessionKey = agentSession.team_session_id || "";
      agentEntry.call = agentSession.call_id || agentEntry.call || "";
      if (type === "session.agent_session.created") {
        agentEntry.startedAt = agentSession.created_at || "";
      }
      agentEntry.status = agentSession.status || agentEntry.status;
    }
  }

  /* "任务从哪里指派过来": the caller of the Team tool is the dispatching team.
   * Walk the events once, remembering which team made the last Team call; the
   * team_turn that follows it was dispatched by that caller. No explicit
   * parent field exists in the events, so this is reconstruction — if the
   * engine later writes one, this can go away. */
  function inferDispatchParents(record) {
    var teamsByName = {};
    runState.teams.forEach(function (team) {
      if (!teamsByName[team.name]) { teamsByName[team.name] = team; }
    });
    var turnTeam = {};
    var events = (record && record.events) || [];
    events.forEach(function (event) {
      if (event.type === "session.team_turn.started") {
        var teamTurn = (event.payload || {}).team_turn || {};
        if (teamTurn.id) { turnTeam[teamTurn.id] = teamTurn.team_id || event.team_id || ""; }
      }
    });
    var pendingCaller = "";
    events.forEach(function (event) {
      var payload = event.payload || {};
      if (event.type === "session.tool_call.started" && payload.tool_name === "Team") {
        var callTurn = event.call_turn_id || "";
        pendingCaller = turnTeam[callTurn.split(":")[0]] || "";
        return;
      }
      if (event.type === "session.team_turn.started") {
        var teamTurn = payload.team_turn || {};
        var entity = teamsByName[teamTurn.team_id || event.team_id || ""] ||
          teamsByName[turnTeam[event.team_turn_id || teamTurn.id || ""]] || null;
        if (entity && pendingCaller && pendingCaller !== entity.name) {
          entity.parent = pendingCaller;
        }
      }
    });
  }


  /* A stable color per name, so the same team/agent keeps the same badge
   * across refreshes instead of reshuffling the palette. */
  var RUN_COLORS = ["#4c8dff", "#3fb950", "#d29922", "#a371f7", "#f778ba", "#39c5cf"];

  function colorFor(name) {
    var hash = 0;
    String(name || "").split("").forEach(function (ch) {
      hash = (hash * 31 + ch.charCodeAt(0)) >>> 0;
    });
    return RUN_COLORS[hash % RUN_COLORS.length];
  }

  function avatar(name, delay) {
    return el("span", { class: "run-avatar", style: "background:" + colorFor(name) + ";" + delay },
      (name || "?").charAt(0).toUpperCase());
  }

  /* Two sources, on purpose: /api/topology says which Teams and Calls exist
   * (the shape, whether or not anything ran), the event fold says which of them
   * is executing right now. An older server without the endpoint degrades to
   * the event view, which shows only what has already run. */
  function runnerItems() {
    var liveTeams = {};
    runState.teams.forEach(function (team) { liveTeams[team.name] = team; });
    var liveCalls = {};
    runState.agents.forEach(function (agent) {
      if (!agent.team || !agent.call) { return; }
      liveCalls[agent.team + "\u0000" + agent.call] = agent;
    });

    if (!topologyState || !(topologyState.teams || []).length) {
      return { configured: false, teams: runState.teams, agents: runState.agents };
    }

    var teams = [];
    var agents = [];
    (topologyState.teams || []).forEach(function (team) {
      var live = liveTeams[team.name];
      var status = team.status || "never";
      if (live && live.status === "running") { status = "running"; }
      teams.push({
        key: "cfg:" + team.name,
        name: team.name,
        team: "",
        status: status,
        lifecycle: team.lifecycle || "",
        canActivate: team.can_activate || [],
        entry: !!team.entry,
        instances: team.instances || 0,
        runningInstances: team.running_instances || 0,
        calls: (team.calls || []).map(function (call) { return call.name; }),
        runningCalls: (team.calls || []).filter(function (call) {
          return (team.call_status && team.call_status[call.name]) === "running";
        }).map(function (call) { return call.name; })
      });
      (team.calls || []).forEach(function (call) {
        var liveCall = liveCalls[team.name + "\u0000" + call.name];
        var callStatus = (team.call_status && team.call_status[call.name]) || "never";
        if (liveCall && liveCall.status === "running") { callStatus = "running"; }
        agents.push({
          key: "cfg:" + team.name + ":" + call.name,
          // The Call is the row; the agent it runs is a secondary label, shown
          // only when it differs.
          name: call.agent || call.name,
          call: call.name,
          callType: call.type,
          teamSessionKey: "cfg:" + team.name,
          status: callStatus
        });
      });
    });
    return { configured: true, teams: teams, agents: agents };
  }

  /* This view answers "what is running now", not "what has run". Anything that
   * is not executing right now gets no label: a word like 空闲 or 未参与 reads as
   * a status of its own and turns a live board into a history list. */
  function runStatusLabel(status) {
    if (status === "running") { return "运行中"; }
    if (status === "failed") { return "失败"; }
    return "";
  }

  function runDot(status) {
    return el("span", { class: "run-dot " + status });
  }

  /* One box per Team, with its Calls as rows inside it.
   *
   * The box is the Team, the rows are what it runs: that containment is exactly
   * what the config says, so it is drawn with a border and a rail instead of
   * connector lines between free-floating cards. */
  /* The Run view is a picture of the Flow's own shape: Teams as nodes, a
   * `can_activate` edge between them, and the executing ones lit up.
   *
   * It deliberately says nothing about the past. A node is either running now
   * or it is not; there is no 已参与 / 未参与, because a board that answers
   * "what happened" cannot also answer "what is happening" at a glance. */
  function runGraphLayout(teams) {
    var byName = {};
    teams.forEach(function (team) { byName[team.name] = team; });

    var edges = [];
    var incoming = {};
    teams.forEach(function (team) { incoming[team.name] = 0; });
    teams.forEach(function (team) {
      (team.canActivate || []).forEach(function (target) {
        // A can_activate target that is not bound in this Flow cannot be drawn.
        if (!byName[target]) { return; }
        edges.push({ from: team.name, to: target });
        incoming[target] += 1;
      });
    });

    // Depth = longest path from a root, so a Team always sits to the right of
    // everything that can activate it. Roots are the entry Team and Teams
    // nobody activates. The pass count is bounded by the Team count, which
    // keeps a misconfigured cycle from looping forever.
    var depth = {};
    teams.forEach(function (team) {
      if (team.entry || incoming[team.name] === 0) { depth[team.name] = 0; }
    });
    for (var pass = 0; pass <= teams.length; pass++) {
      var changed = false;
      edges.forEach(function (edge) {
        var from = depth[edge.from] === undefined ? 0 : depth[edge.from];
        var candidate = Math.min(from + 1, teams.length);
        if (depth[edge.to] === undefined || depth[edge.to] < candidate) {
          depth[edge.to] = candidate;
          changed = true;
        }
      });
      if (!changed) { break; }
    }
    teams.forEach(function (team) {
      if (depth[team.name] === undefined) { depth[team.name] = 0; }
    });

    var columns = [];
    teams.forEach(function (team) {
      var column = depth[team.name];
      if (!columns[column]) { columns[column] = []; }
      columns[column].push(team);
    });

    var NODE_W = 196;
    var NODE_H = 68;
    var GAP_X = 88;
    var GAP_Y = 14;
    var tallest = 0;
    columns.forEach(function (column) { tallest = Math.max(tallest, column.length); });
    var height = Math.max(1, tallest) * NODE_H + Math.max(0, tallest - 1) * GAP_Y;
    var width = columns.length * NODE_W + Math.max(0, columns.length - 1) * GAP_X;

    var position = {};
    columns.forEach(function (column, index) {
      var columnHeight = column.length * NODE_H + Math.max(0, column.length - 1) * GAP_Y;
      var top = (height - columnHeight) / 2;
      column.forEach(function (team, row) {
        position[team.name] = {
          x: index * (NODE_W + GAP_X),
          y: top + row * (NODE_H + GAP_Y)
        };
      });
    });

    return {
      edges: edges, width: width, height: height, position: position,
      nodeW: NODE_W, nodeH: NODE_H
    };
  }

  function renderRunEdges(layout) {
    var NS = "http://www.w3.org/2000/svg";
    var svg = document.createElementNS(NS, "svg");
    svg.setAttribute("class", "run-links");
    svg.setAttribute("width", String(layout.width));
    svg.setAttribute("height", String(layout.height));
    svg.setAttribute("viewBox", "0 0 " + layout.width + " " + layout.height);

    layout.edges.forEach(function (edge) {
      var from = layout.position[edge.from];
      var to = layout.position[edge.to];
      if (!from || !to) { return; }
      var x1 = from.x + layout.nodeW;
      var y1 = from.y + layout.nodeH / 2;
      var x2 = to.x;
      var y2 = to.y + layout.nodeH / 2;
      var reach = Math.max(28, (x2 - x1) / 2);
      var path = document.createElementNS(NS, "path");
      path.setAttribute("d", "M" + x1 + " " + y1 +
        " C" + (x1 + reach) + " " + y1 + " " + (x2 - reach) + " " + y2 + " " + x2 + " " + y2);
      path.setAttribute("class", edge.active ? "run-edge active" : "run-edge");
      svg.appendChild(path);
    });
    return svg;
  }

  function renderRunners() {
    var wrap = el("div", { class: "runboard" });
    var items = runnerItems();
    var runningTeams = items.teams.filter(function (team) {
      return team.status === "running";
    });
    var runningCalls = items.agents.filter(function (agent) {
      return agent.status === "running";
    }).length;
    // An isolated team can hold several instances at once: "1 个 team 在跑" would
    // hide that three tasks are executing.
    var runningInstances = items.teams.reduce(function (sum, team) {
      return sum + (team.runningInstances || 0);
    }, 0);
    if (!runningInstances) { runningInstances = runningTeams.length; }

    wrap.appendChild(el("div", { class: "run-summary" },
      items.configured
        ? el("span", { class: runningTeams.length ? "run-live" : "muted" },
            "配置 " + items.teams.length + " 个 team / " + items.agents.length + " 个 agent · " +
            (runningTeams.length
              ? "当前 " + runningTeams.length + " 个 team / " + runningInstances + " 个实例在跑"
              : "当前没有在跑的 team 或 agent"))
        : el("span", { class: runningTeams.length ? "run-live" : "muted" },
            runningTeams.length
              ? "运行中 " + runningTeams.length + " 个 team"
              : "当前没有在跑的 team 或 agent。")));

    if (!items.teams.length) {
      wrap.appendChild(el("div", { class: "empty" },
        items.configured ? "本次 Flow 没有配置 team。" : "这个会话还没有 team / agent 运行过。"));
      return wrap;
    }

    var layout = runGraphLayout(items.teams);
    // An edge is live when the Team it points at is executing: that is the
    // dispatch currently in flight.
    var runningNames = {};
    runningTeams.forEach(function (team) { runningNames[team.name] = true; });
    layout.edges.forEach(function (edge) {
      if (runningNames[edge.to]) { edge.active = true; }
    });

    var canvas = el("div", { class: "run-canvas", style: "width:" + layout.width + "px;height:" + layout.height + "px" });
    canvas.appendChild(renderRunEdges(layout));

    var enterIndex = 0;
    items.teams.forEach(function (team) {
      var spot = layout.position[team.name];
      var calls = (team.calls || []).join(" · ");
      var running = team.status === "running";
      // The name is the label that matters; entry is drawn as a thicker rail on
      // the left of the node instead of eating the row's width.
      var meta = team.lifecycle ? [team.lifecycle] : [];

      var node = el("div", {
        class: "run-node " + team.status + (team.entry ? " entry" : ""),
        "data-run-key": "cfg:" + team.name,
        "data-team": team.name,
        "data-entry": team.entry ? "1" : "0",
        title: team.name + (team.entry ? "（入口）" : "") + (calls ? "：" + calls : ""),
        style: "left:" + spot.x + "px;top:" + spot.y + "px;width:" + layout.nodeW +
          "px;height:" + layout.nodeH + "px;animation-delay:" +
          Math.min(enterIndex++ * 25, 200) + "ms"
      },
        el("div", { class: "run-node-head" },
          runDot(team.status),
          el("span", { class: "run-name" }, team.name),
          meta.length ? el("span", { class: "run-badge" }, meta.join(" · ")) : null,
          running && team.runningInstances > 1
            ? el("span", { class: "run-badge run-parallel", title: "并发的 isolated 实例数" },
                "×" + team.runningInstances)
            : null),
        el("div", { class: "run-node-calls" },
          running && team.runningCalls && team.runningCalls.length
            ? "运行中：" + team.runningCalls.join(" · ")
            : (calls || "无 call")));

      canvas.appendChild(node);
    });

    // A wide Flow scrolls sideways inside its own frame: the summary line above
    // it stays put, and the page itself never gets a horizontal scrollbar.
    var frame = el("div", { class: "run-scroll" }, canvas);
    wrap.appendChild(frame);
    return wrap;
  }

  function refreshRunView() {
    if (currentMode !== "run") { return; }
    var body = document.getElementById("record-body");
    if (!body) { return; }
    clear(body);
    body.appendChild(renderRunners());
  }

  /* ---------- waterfall ---------- */
  /* The tree shows hierarchy but not time: two teams running in parallel look
   * identical to two teams running one after another. This puts every team turn
   * and agent turn on a shared axis, so overlap is visible at a glance. */
  function collectSpans(events) {
    var spans = {};
    var order = [];

    function slot(id, label) {
      if (!spans[id]) {
        spans[id] = { id: id, label: label || "", start: "", end: "" };
        order.push(spans[id]);
      }
      if (!spans[id].label && label) { spans[id].label = label; }
      return spans[id];
    }

    events.forEach(function (event) {
      var payload = event.payload || {};
      if (event.type === "session.team_turn.started" || event.type === "session.team_turn.ended") {
        // The started event carries the turn under team_turn, the terminal one
        // under team_result.turn. Reading one field for both left every Team
        // row with no end time, so a finished turn rendered as 运行中.
        var teamTurn = event.type === "session.team_turn.started"
          ? (payload.team_turn || {})
          : ((payload.team_result || {}).turn || {});
        var teamID = teamTurn.id || "";
        if (!teamID) { return; }
        var team = teamTurn.team_id || event.team_id || "";
        var teamSlot = slot(teamID, "team " + (team || "?"));
        if (event.type === "session.team_turn.started") {
          teamSlot.start = event.created_at || teamSlot.start;
        } else {
          teamSlot.end = teamTurn.finished_at || event.created_at || teamSlot.end;
        }
        return;
      }
      if (event.type === "session.agent_turn.started" || event.type === "session.agent_turn.completed") {
        var call = event.call_turn_id || "";
        if (!call) { return; }
        var speaker = speakerLabel(event);
        var label = speaker.team ? speaker.team + " / " + speaker.agent : (speaker.agent || call);
        var callSlot = slot(call, label);
        if (event.type === "session.agent_turn.started") { callSlot.start = event.created_at || callSlot.start; }
        else { callSlot.end = event.created_at || callSlot.end; }
      }
    });

    return order;
  }

  /* Which configured Teams and Calls this session never reached.
   *
   * The timeline and the tree are built from events, so anything that has not
   * run is simply absent — the reader then cannot tell "not configured" from
   * "not reached". These rows make the difference visible. */
  function spanTeamName(span) {
    var label = span.label || "";
    if (label.indexOf("team ") === 0) { return label.slice(5).trim(); }
    var index = label.indexOf(" / ");
    return index > 0 ? label.slice(0, index) : "";
  }

  function missingTopologyRows() {
    if (!topologyState || !(topologyState.teams || []).length) { return []; }
    var spans = collectSpans((currentRecord && currentRecord.events) || []);
    var seenTeams = {};
    spans.forEach(function (span) {
      var name = spanTeamName(span);
      if (name) { seenTeams[name] = true; }
    });
    var rows = [];
    topologyState.teams.forEach(function (team) {
      if (seenTeams[team.name]) { return; }
      rows.push({ label: "team " + team.name, team: team.name });
      (team.calls || []).forEach(function (call) {
        // The row names the Call, the same word the Run view and the tree use.
        // The agent it would run is the tooltip: "check / test-runner" reads
        // like a Call named test-runner, which it is not.
        rows.push({
          label: team.name + " / " + call.name,
          title: call.agent ? call.name + " 由 " + call.agent + " 执行" : call.name,
          team: team.name
        });
      });
    });
    return rows;
  }

  function renderWaterfall(record) {
    var wrap = el("div", { class: "waterfall" });
    var spans = collectSpans((record && record.events) || []);
    var missing = missingTopologyRows();
    if (spans.length === 0 && missing.length === 0) {
      wrap.appendChild(el("div", { class: "empty" }, "\u8fd9\u4e2a\u4f1a\u8bdd\u6ca1\u6709 team / call \u65f6\u5e8f\u3002"));
      return wrap;
    }

    var starts = [];
    var ends = [];
    spans.forEach(function (span) {
      var start = new Date(span.start).getTime();
      var end = new Date(span.end || span.start).getTime();
      if (!isNaN(start)) { starts.push(start); }
      if (!isNaN(end)) { ends.push(end); }
    });
    var origin = starts.length ? Math.min.apply(null, starts) : Date.now();
    var finish = ends.length ? Math.max.apply(null, ends) : origin;
    var total = Math.max(1, finish - origin);

    if (spans.length) {
      spans.forEach(function (span) {
        var start = new Date(span.start).getTime();
        var end = new Date(span.end).getTime();
        var left = isNaN(start) ? 0 : ((start - origin) / total) * 100;
        var width = (isNaN(start) || isNaN(end)) ? 1.5 : Math.max(1.5, ((end - start) / total) * 100);
        var isTeam = span.id.indexOf(":") < 0;
        wrap.appendChild(el("div", { class: "wf-row" + (isTeam ? "" : " wf-child") },
          el("span", { class: "wf-label", title: span.id }, span.label),
          el("span", { class: "wf-track" },
            el("i", {
              class: isTeam ? "wf-team" : "wf-call",
              style: "left:" + left.toFixed(2) + "%;width:" + width.toFixed(2) + "%"
            })),
          el("span", { class: "wf-time" }, elapsed(span.start, span.end) || "\u8fd0\u884c\u4e2d")));
      });
    }

    // Configured but never reached: a dashed track and no duration, so it reads
    // as "part of the Flow, idle in this session" rather than as history.
    // Configured but never reached: a dashed track and no duration, so it reads
    // as "part of the Flow, idle in this session" rather than as history. It is
    // folded away by default: thirteen dashed rows out of twenty take more room
    // than the execution this view is actually about.
    var folded = el("details", { class: "wf-missing" },
      el("summary", null, "\u914d\u7f6e\u4e2d\u5b58\u5728\u3001\u672c\u6b21\u4f1a\u8bdd\u672a\u53c2\u4e0e\uff08" +
        missing.length + " \u9879\uff09"));
    missing.forEach(function (row) {
      folded.appendChild(el("div", { class: "wf-row missing" },
        el("span", { class: "wf-label", title: row.title || row.label }, row.label),
        el("span", { class: "wf-track missing" }),
        el("span", { class: "wf-time" }, "\u672a\u53c2\u4e0e")));
    });
    wrap.appendChild(folded);

    return wrap;
  }

  /* ---------- tree ---------- */
  function renderTree(record) {
    var wrap = el("div");
    var nodes = record.nodes || [];
    /* An agent_session hangs off its team_session, which the record does not
     * ship as a node. Left alone the row is unreachable from the roots and the
     * agent layer never renders — the tree then shows a Team with nothing but
     * its call under it. Reattach it to the call_turn that ran it, so the tree
     * reads team → call → agent. */
    var ids = {};
    nodes.forEach(function (node) { ids[node.id] = true; });
    var callTurns = {};
    nodes.forEach(function (node) {
      if (node.kind === "call_turn" && node.team_turn_id) {
        callTurns[node.team_turn_id + "\u0000" + (node.call_id || "")] = node.id;
      }
    });
    nodes = nodes.map(function (node) {
      var host = node.parent && ids[node.parent] ? "" :
        (node.kind === "agent_session"
          ? callTurns[(node.team_turn_id || "") + "\u0000" + (node.call_id || "")] || ""
          : "");
      if (!host) { return node; }
      var copy = {};
      for (var key in node) { if (Object.prototype.hasOwnProperty.call(node, key)) { copy[key] = node[key]; } }
      copy.parent = host;
      return copy;
    });
    var childrenOf = {};
    nodes.forEach(function (node) {
      var parent = node.parent || "";
      if (!childrenOf[parent]) { childrenOf[parent] = []; }
      childrenOf[parent].push(node);
    });
    Object.keys(childrenOf).forEach(function (key) {
      childrenOf[key].sort(function (a, b) { return (a.first_seq || 0) - (b.first_seq || 0); });
    });

    var roots = childrenOf[""] || [];
    var missing = missingTopologyRows();
    if (roots.length === 0 && missing.length === 0) {
      wrap.appendChild(el("div", { class: "empty" }, "\u6ca1\u6709\u6267\u884c\u6811\u3002"));
      return wrap;
    }

    var tree = el("div", { class: "tree" });
    var detail = el("div", { class: "detail", id: "node-detail" },
      el("span", { class: "muted" }, "\u70b9\u4e00\u4e2a\u8282\u70b9\u770b\u8be6\u60c5\u3002"));

    function addNode(node, depth, branch) {
      var children = childrenOf[node.id] || [];
      var row = el("div", {
        class: "tnode",
        style: "padding-left:" + (depth * 14) + "px",
        dataset: { node: node.id, parent: node.parent || "" },
        "aria-selected": "false"
      },
        el("span", { class: "branch" }, branch),
        el("span", { class: "tw" + (children.length ? "" : " leaf") }),
        el("span", null,
          // "team_turn dev-lead" says the same thing twice: the label already
          // names the Team. The kind chip stays where it adds information.
          node.kind === "team_turn" || node.kind === "call_turn"
            ? null : el("span", { class: "kind" }, node.kind),
          labelForNode(node),
          el("span", { class: "id" }, shortId(node.id))),
        el("span", { class: "time" }, nodeSpanDuration(node, record)),
        statusDot(node.status));
      tree.appendChild(row);

      row.addEventListener("click", function (event) {
        if (event.target.classList.contains("tw")) { return; }
        Array.prototype.forEach.call(tree.querySelectorAll(".tnode"), function (other) {
          other.setAttribute("aria-selected", other === row ? "true" : "false");
        });
        paintNodeDetail(detail, node, record);
      });

      var toggle = row.querySelector(".tw");
      if (children.length) {
        toggle.addEventListener("click", function (event) {
          event.stopPropagation();
          var collapsed = row.classList.toggle("collapsed");
          hideDescendants(tree, node.id, collapsed);
        });
      }

      children.forEach(function (child, index) {
        addNode(child, depth + 1, index === children.length - 1 ? "\u2514\u2500" : "\u251c\u2500");
      });
    }

    roots.forEach(function (root, index) {
      addNode(root, 0, index === roots.length - 1 ? "\u2514\u2500" : "\u251c\u2500");
    });

    wrap.appendChild(tree);

    if (missing.length) {
      var block = el("div", { class: "tree-missing" },
        el("div", { class: "muted" }, "\u914d\u7f6e\u4e2d\u5b58\u5728\u3001\u672c\u6b21\u4f1a\u8bdd\u672a\u53c2\u4e0e\uff1a"));
      var list = el("div", { class: "tree-missing-list" });
      missing.forEach(function (row) {
        list.appendChild(el("span", { class: "tree-missing-item", title: row.title || row.label }, row.label));
      });
      block.appendChild(list);
      wrap.appendChild(block);
    }

    wrap.appendChild(detail);
    return wrap;
  }

  function labelForNode(node) {
    if (node.kind === "flow_session") { return "\u4f1a\u8bdd"; }
    // A dispatched Team runs as its own flow turn. Calling both "回合" is what
    // made one question look like four; the second level is an execution turn.
    if (node.kind === "flow_turn") { return node.user_turn ? "回合" : "执行轮"; }
    if (node.kind === "team_turn") { return (node.team_id || "team"); }
    if (node.kind === "call_turn") {
      // The call names the step, the agent names who ran it. A command call has
      // no agent, so its type is the only thing there is to show; when the two
      // names coincide the type says more than the repetition would.
      var detail = node.agent_id && node.agent_id !== node.call_id
        ? node.agent_id : (node.call_type || "");
      return (node.call_id || "call") + (detail ? " \u00b7 " + detail : "");
    }
    if (node.kind === "agent_session") { return node.agent_id ? "agent " + node.agent_id : ""; }
    return "";
  }

  function hideDescendants(tree, id, hidden) {
    var queue = [id];
    while (queue.length) {
      var current = queue.shift();
      Array.prototype.forEach.call(tree.querySelectorAll(".tnode"), function (row) {
        if (row.dataset.parent !== current) { return; }
        row.style.display = hidden ? "none" : "";
        if (row.dataset.node) { queue.push(row.dataset.node); }
      });
    }
  }

  function paintNodeDetail(container, node, record) {
    clear(container);
    var events = (record.events || []).filter(function (event) {
      return event.seq >= node.first_seq && event.seq <= node.last_seq;
    });

    // What this node actually cost and produced. Clicking a Team used to give a
    // list of event types, which answers "what happened" but not "was it
    // expensive" — the two questions a reader opens this panel for.
    var models = {};
    var usage = { prompt: 0, completion: 0 };
    var toolCalls = 0;
    var toolFailures = 0;
    var records = [];
    events.forEach(function (event) {
      var payload = event.payload || {};
      if (event.type === "session.agent.model_response") {
        if (payload.model) { models[payload.model] = true; }
        var used = payload.usage || {};
        usage.prompt += used.prompt_tokens || 0;
        usage.completion += used.completion_tokens || 0;
      }
      if (event.type === "session.tool_call.started") { toolCalls++; }
      if (event.type === "session.tool_call.failed") { toolFailures++; }
      if (event.type === "session.record.published") {
        var record_ = payload.record || {};
        if (record_.name) { records.push(record_.name); }
      }
    });

    var summary = el("div", { class: "node-metrics" });
    function metric(key, value) {
      if (!value) { return; }
      summary.appendChild(el("div", null,
        el("span", { class: "k" }, key),
        el("span", { class: "v" }, String(value))));
    }
    metric("模型", Object.keys(models).join(", "));
    metric("Token 入 / 出", usage.prompt || usage.completion
      ? tokenCount(usage.prompt) + " / " + tokenCount(usage.completion) : "");
    metric("工具调用", toolCalls
      ? toolCalls + (toolFailures ? " · " + toolFailures + " 失败" : "") : "");
    metric("产出 record", records.join(", "));
    metric("事件", events.length + " 条");
    container.appendChild(summary);

    var list = el("dl");

    function add(key, value) {
      if (value === null || value === undefined || value === "" || value === 0) { return; }
      list.appendChild(el("dt", null, key));
      list.appendChild(el("dd", null, String(value)));
    }

    add("\u7c7b\u578b", node.kind);
    add("id", node.id);
    add("\u72b6\u6001", node.status);
    add("\u4e8b\u4ef6\u533a\u95f4", "seq " + node.first_seq + " \u2013 " + node.last_seq);
    add("\u8017\u65f6", nodeSpanDuration(node, record));
    add("Team", node.team_id);
    add("Call", node.call_id);
    add("Call \u7c7b\u578b", node.call_type);
    add("Agent", node.agent_id);
    add("attempt", node.attempt);
    container.appendChild(list);

    // The tree can be taller than the pane; reveal the panel the click filled.
    if (container.scrollIntoView) { container.scrollIntoView({ block: "nearest" }); }


    container.appendChild(el("div", { class: "faint" },
      "该节点事件（" + events.length + " 条）"));

    var table = el("table", { class: "raw" });
    var tbody = el("tbody");
    events.forEach(function (event) {
      tbody.appendChild(el("tr", null,
        el("td", { class: "seq" }, String(event.seq)),
        el("td", { class: "type" }, event.type),
        el("td", { class: "sum" }, eventSummary(event))));
    });
    table.appendChild(tbody);
    container.appendChild(table);
  }

  /* ---------- raw ---------- */
  function renderRaw(record) {
    var events = record.events || [];

    // 136 events is normal for one question: scanning them for the failed tool
    // call or the record publish is the actual work here, so the type filter and
    // a per-event copy sit above the table rather than behind a menu.
    var filter = el("input", {
      class: "raw-filter",
      type: "search",
      placeholder: "按 type 过滤，例如 tool_call / record / model_response"
    });
    var count = el("span", { class: "raw-count" });
    var bar = el("div", { class: "raw-bar" }, filter, count);

    var table = el("table", { class: "raw" },
      el("thead", null, el("tr", null,
        el("th", null, "seq"),
        el("th", null, "时间"),
        el("th", null, "type"),
        el("th", null, "摘要"))));
    var tbody = el("tbody");
    var rows = [];

    events.forEach(function (event) {
      var payload = event.payload || {};
      var copy = el("button", { type: "button", class: "copy-payload", title: "复制 payload JSON" }, "复制");
      copy.addEventListener("click", function () { copyText(JSON.stringify(payload, null, 2), copy); });
      var row = el("tr", { id: "ev-" + event.seq },
        el("td", { class: "seq" }, String(event.seq)),
        el("td", { class: "sum" }, clock(event.created_at)),
        el("td", { class: "type" }, event.type),
        el("td", { class: "sum" },
          eventSummary(event),
          copy,
          el("details", null,
            el("summary", null, "payload"),
            el("pre", null, JSON.stringify(payload, null, 2)))));
      rows.push({ row: row, type: event.type });
      tbody.appendChild(row);
    });
    table.appendChild(tbody);

    function applyFilter() {
      var query = String(filter.value || "").trim().toLowerCase();
      var shown = 0;
      rows.forEach(function (entry) {
        var hit = !query || entry.type.toLowerCase().indexOf(query) >= 0;
        entry.row.classList.toggle("hidden", !hit);
        if (hit) { shown++; }
      });
      count.textContent = shown + " / " + rows.length + " 条";
      return shown;
    }
    filter.addEventListener("input", applyFilter);
    applyFilter();

    var wrap = el("div", { class: "raw-view" }, bar, table);
    if (rows.length === 0) {
      wrap.appendChild(el("div", { class: "empty" }, "这个会话没有事件。"));
    }
    return wrap;
  }

  renderSidebarToggle();
  route();
})();
