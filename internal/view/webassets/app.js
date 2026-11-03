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
  var currentRecord = null;

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
    node.appendChild(typeof child === "string" ? text(child) : child);
  }

  function clear(node) {
    while (node.firstChild) { node.removeChild(node.firstChild); }
  }

  function link(href, label) {
    return el("a", { href: href, "data-link": "1" }, label);
  }

  function tag(label, kind) {
    return el("span", { class: "tag" + (kind ? " " + kind : "") }, label);
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

  function tokenCount(value) {
    if (!value) { return "0"; }
    return value >= 1000 ? (value / 1000).toFixed(1) + "k" : String(value);
  }

  function statusDot(status) {
    var kind = "dot";
    if (status === "completed") { kind += " ok"; }
    else if (status === "failed") { kind += " bad"; }
    else if (status === "running") { kind += " warn"; }
    else if (status && status.indexOf("waiting") === 0) { kind += " warn"; }
    return el("span", { class: kind });
  }

  function eventClass(type) {
    if (type === "flow_turn.started") { return "mm-user"; }
    if (type === "tool_call.started" || type === "tool_call.completed" || type === "tool_call.failed") { return "mm-tool"; }
    if (type === "context.compacted") { return "mm-compact"; }
    if (type.indexOf("wakeup.") === 0) { return "mm-wakeup"; }
    if (type.indexOf("failed") >= 0 || type === "recovery.requested") { return "mm-error"; }
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
    clear(app);
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
    if (match) {
      renderRecord(decodeURIComponent(match[1]));
      return;
    }
    renderList();
  }

  function fail(message) {
    clear(app);
    app.appendChild(el("div", { class: "error-box" }, message));
    app.appendChild(el("p", null, link("/", "\u2190 \u8fd4\u56de\u4f1a\u8bdd\u5217\u8868")));
  }

  /* ---------- new session ---------- */
  /* POST /api/sessions without a session id starts a new flow session: the body
   * carries the first turn, and the answer only comes back once that turn is
   * done. There is no "create empty session" call — the first message is what
   * creates it — so the button has to warn that it may take a while instead of
   * pretending to be instant. */
  function renderNewSession() {
    var busy = false;
    var input = el("textarea", {
      class: "new-input",
      rows: "2",
      placeholder: "\u7b2c\u4e00\u53e5\u8981\u505a\u4ec0\u4e48\uff1f\u8fd9\u4f1a\u521b\u5efa\u4f1a\u8bdd\u5e76\u7acb\u5373\u5f00\u59cb\u7b2c\u4e00\u8f6e\u3002"
    });
    var submit = el("button", { type: "button", class: "new-submit" }, "\u521b\u5efa\u5e76\u5f00\u59cb");
    var cancel = el("button", { type: "button", class: "new-cancel" }, "\u53d6\u6d88");
    var status = el("span", { class: "new-status" }, "");
    var toggle = el("button", { type: "button", class: "new-toggle" }, "\uff0b \u65b0\u5efa\u4f1a\u8bdd");
    var form = el("div", { class: "new-form hidden" },
      input,
      el("div", { class: "new-bar" }, submit, cancel, status));
    var box = el("div", { class: "new-session" }, toggle, form);

    function setOpen(value) {
      form.classList.toggle("hidden", !value);
      toggle.classList.toggle("hidden", value);
      if (value) {
        input.focus();
        return;
      }
      status.textContent = "";
    }

    toggle.addEventListener("click", function () { setOpen(true); });
    cancel.addEventListener("click", function () { if (!busy) { setOpen(false); } });

    function create() {
      if (busy) { return; }
      var value = input.value.trim();
      if (!value) { input.focus(); return; }
      busy = true;
      submit.disabled = true;
      submit.textContent = "\u521b\u5efa\u4e2d\u2026";
      status.textContent = "\u9996\u8f6e\u6b63\u5728\u6267\u884c\uff0c\u8dd1\u5b8c\u4f1a\u81ea\u52a8\u6253\u5f00\uff1b\u6d3e\u53d1\u7c7b\u4efb\u52a1\u53ef\u80fd\u8981\u51e0\u5206\u949f\u3002";
      apiFetch("/api/sessions", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ input: value })
      })
        .then(function (response) {
          if (response.ok) { return response.json(); }
          return response.text().then(function (text) {
            throw new Error((text || "").trim() || ("HTTP " + response.status));
          });
        })
        .then(function (result) {
          var id = result && result.Session && result.Session.id;
          if (!id) { throw new Error("\u54cd\u5e94\u91cc\u6ca1\u6709\u4f1a\u8bdd ID"); }
          navigate("/s/" + encodeURIComponent(id));
        })
        .catch(function (error) {
          if (error && error.unauthorized) { return; }
          busy = false;
          submit.disabled = false;
          submit.textContent = "\u521b\u5efa\u5e76\u5f00\u59cb";
          status.textContent = "\u521b\u5efa\u5931\u8d25\uff1a" + error.message;
        });
    }

    submit.addEventListener("click", create);
    input.addEventListener("keydown", function (event) {
      if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        create();
      }
    });

    return box;
  }

  /* ---------- list ---------- */
  function renderList() {
    clear(app);
    app.appendChild(el("div", { class: "top" },
      el("h1", null, "heron-ai"),
      el("span", { class: "muted" }, "\u4f1a\u8bdd\u5217\u8868")));
    app.appendChild(renderNewSession());

    apiFetch("/api/sessions")
      .then(function (response) {
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (payload) {
        var sessions = payload.sessions || [];
        if (sessions.length === 0) {
          app.appendChild(el("div", { class: "empty" },
            "\u8fd8\u6ca1\u6709\u4f1a\u8bdd\u3002\u70b9\u4e0a\u9762\u7684\u300c\u65b0\u5efa\u4f1a\u8bdd\u300d\u5f00\u59cb\u7b2c\u4e00\u4e2a\u3002"));
          return;
        }
        var rows = el("div", { class: "rows" });
        sessions.forEach(function (session) {
          rows.appendChild(el("a", {
            class: "row",
            href: "/s/" + encodeURIComponent(session.id),
            "data-link": "1"
          },
            el("span", { class: session.active ? "dot active" : "dot" }),
            el("span", null,
              el("div", { class: "title" }, session.title || "(\u672a\u547d\u540d)"),
              el("div", { class: "meta" },
                session.turns + " \u8f6e \u00b7 " + tokenCount(session.tokens) + " tok \u00b7 " + relativeTime(session.updated_at))),
            el("span", { class: "right" },
              el("div", { class: "id" }, session.id),
              el("div", null, session.active ? "\u8fd0\u884c\u4e2d" : "\u7b49\u5f85\u8f93\u5165"))));
        });
        app.appendChild(rows);
      })
      .catch(function (error) {
        // 401 already swapped in the login form; an error box on top of it
        // would hide the field the reader has to type into.
        if (error && error.unauthorized) { return; }
        fail("\u8bfb\u53d6\u4f1a\u8bdd\u5217\u8868\u5931\u8d25\uff1a" + error.message);
      });
  }

  /* ---------- record ---------- */
  function renderRecord(sessionID) {
    clear(app);
    app.appendChild(el("div", { class: "top" },
      el("h1", null, "heron-ai"),
      el("span", { class: "crumb" }, link("/", "\u2190 \u4f1a\u8bdd\u5217\u8868")),
      el("span", { class: "crumb mono" }, sessionID)));

    apiFetch("/api/sessions/" + encodeURIComponent(sessionID) + "/record")
      .then(function (response) {
        if (response.status === 404) { throw new Error("\u4f1a\u8bdd\u4e0d\u5b58\u5728"); }
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (record) { paintRecord(sessionID, record); })
      .catch(function (error) {
        if (error && error.unauthorized) { return; }
        fail("\u8bfb\u53d6\u4f1a\u8bdd\u8bb0\u5f55\u5931\u8d25\uff1a" + error.message);
      });
  }

  function paintRecord(sessionID, record) {
    currentRecord = record;
    clear(app);
    app.appendChild(el("div", { class: "top" },
      el("h1", null, "heron-ai"),
      el("span", { class: "crumb" }, link("/", "\u2190 \u4f1a\u8bdd\u5217\u8868")),
      el("span", { class: "crumb mono" }, sessionID)));

    if (record.truncated) {
      app.appendChild(el("div", { class: "error-box" },
        "\u8fd8\u6709 " + record.truncated + " \u6761\u4e8b\u4ef6\u672a\u52a0\u8f7d\uff08\u5355\u6b21\u4e0a\u9650\uff09\u3002\u53ef\u52a0 ?limit= \u63d0\u9ad8\u3002"));
    }

    app.appendChild(kpiStrip(record.facts));
    app.appendChild(timeline(record.events));

    app.appendChild(el("div", { class: "modes" },
      el("div", { class: "seg", role: "group" },
        modeButton("chat", "\u5bf9\u8bdd", true),
        modeButton("tree", "\u7ed3\u6784", false),
        modeButton("raw", "\u539f\u59cb", false)),
      el("span", { class: "hint", id: "mode-hint" }, "\u540c\u4e00\u4efd\u8bb0\u5f55\uff0c\u4e09\u79cd\u5448\u73b0\u3002")));

    var body = el("div", { id: "record-body" });
    app.appendChild(body);
    showMode(body, "chat");

    app.appendChild(renderComposer(sessionID));
    if (scrollToBottom) {
      scrollToBottom = false;
      window.scrollTo(0, document.body.scrollHeight);
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
    });
    return button;
  }

  function showMode(body, mode) {
    if (!body || !currentRecord) { return; }
    clear(body);
    var hint = document.getElementById("mode-hint");
    if (mode === "tree") {
      body.appendChild(renderTree(currentRecord));
      if (hint) { hint.textContent = "\u7ed3\u6784\uff1a\u56de\u5408 \u2192 Team \u2192 Call \u2192 Agent\uff0c\u70b9\u8282\u70b9\u770b\u5b83\u540d\u4e0b\u7684\u4e8b\u4ef6\u3002"; }
      return;
    }
    if (mode === "raw") {
      body.appendChild(renderRaw(currentRecord));
      if (hint) { hint.textContent = "\u539f\u59cb\uff1a\u5408\u5e76\u4e09\u5c42\u540e\u7684\u5b8c\u6574\u4e8b\u4ef6\u5217\u8868\uff0c\u70b9\u5f00\u770b payload\u3002"; }
      return;
    }
    body.appendChild(renderChat(currentRecord));
    if (hint) { hint.textContent = "\u5bf9\u8bdd\uff1a\u4eba\u80fd\u8bfb\u7684\u6d88\u606f\u6d41\u3002"; }
  }

  /* ---------- kpi ---------- */
  function kpiStrip(facts) {
    facts = facts || {};
    var items = [
      ["\u56de\u5408", facts.turns || 0],
      ["Team \u8f6e\u6b21", facts.team_turns || 0],
      ["Call", facts.calls || 0],
      ["\u5de5\u5177\u8c03\u7528", (facts.tool_calls || 0) + (facts.tool_failures ? " \u00b7 " + facts.tool_failures + " \u5931\u8d25" : "")],
      ["Token", tokenCount(facts.input_tokens) + " / " + tokenCount(facts.output_tokens)],
      ["\u6a21\u578b", (facts.models || []).join(", ") || "\u2014"]
    ];
    var strip = el("div", { class: "kpi" });
    items.forEach(function (item) {
      strip.appendChild(el("div", null,
        el("div", { class: "k" }, item[0]),
        el("div", { class: "v" }, String(item[1]))));
    });
    return strip;
  }

  /* ---------- timeline ---------- */
  function timeline(events) {
    var wrap = el("div", { class: "timeline" });
    var bar = el("div", { class: "bar" });
    events.forEach(function (event) {
      var segment = el("i", {
        class: eventClass(event.type),
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
   * screen, using the same body the CLI bridge posts:
   * POST /api/sessions/turn?session_id=… with {input, attachments}.
   *
   * That call is synchronous — it answers when the whole flow turn finishes,
   * which for a dispatching turn is minutes. So the send button locks and says
   * so; a second message typed meanwhile would land in a turn that is still
   * running.
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
    var status = el("span", { class: "composer-status" }, "");

    var box = el("div", { class: "composer" },
      chips,
      input,
      el("div", { class: "composer-bar" },
        attachButton,
        filePicker,
        status,
        el("span", { class: "composer-hint" }, "Enter \u53d1\u9001 \u00b7 Shift+Enter \u6362\u884c"),
        sendButton));

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

    function setBusy(value, text) {
      busy = value;
      input.disabled = value;
      attachButton.disabled = value;
      sendButton.disabled = value;
      sendButton.textContent = value ? "\u6267\u884c\u4e2d\u2026" : "\u53d1\u9001";
      status.textContent = text || "";
    }

    function autosize() {
      input.style.height = "auto";
      input.style.height = Math.min(input.scrollHeight || 0, 180) + "px";
    }

    var liveRun = null;

    function submit() {
      if (busy) { return; }
      var value = input.value.trim();
      if (!value && pending.length === 0) { return; }
      var payload = { input: value };
      if (pending.length) { payload.attachments = pending.slice(); }

      /* Subscribe before posting. The turn starts the moment the request
       * lands; a stream opened afterwards would miss its opening events. The
       * stream resumes from the last seq already on screen, so nothing replays. */
      var chat = document.querySelector("#record-body .chat");
      if (liveRun) { liveRun.stop(); liveRun = null; }
      if (chat && currentRecord) { liveRun = startLiveRun(sessionID, chat, currentRecord); }

      input.value = "";
      autosize();
      pending = [];
      refreshChips();
      setBusy(true, "\u5df2\u53d1\u9001\uff0c\u6b63\u5728\u6267\u884c\u2026\u8fc7\u7a0b\u4f1a\u5b9e\u65f6\u51fa\u73b0\u5728\u5bf9\u8bdd\u91cc\u3002");
      window.scrollTo(0, document.body.scrollHeight);

      apiFetch("/api/sessions/turn?session_id=" + encodeURIComponent(sessionID), {
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
        .then(function () {
          setBusy(false, "");
          // The POST answers when the turn is over, so the stream has nothing
          // left to add. Give the last frame a beat, then close it.
          setTimeout(function () {
            var missed = liveRun ? liveRun.count() === 0 : true;
            if (liveRun) { liveRun.settleAll(); liveRun.stop(); liveRun = null; }
            if (missed) {
              // The stream never delivered a frame; the record is the fallback
              // so the reader still sees what happened.
              scrollToBottom = true;
              renderRecord(sessionID);
            }
          }, 800);
        })
        .catch(function (error) {
          if (error && error.unauthorized) { return; }
          if (liveRun) { liveRun.stop(); liveRun = null; }
          setBusy(false, "\u53d1\u9001\u5931\u8d25\uff1a" + error.message);
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

    view.push = function (event) {
      var payload = event.payload || {};
      var call = event.call_turn_id || "";

      if (event.type === "flow_turn.started") {
        view.startedAt = event.created_at || "";
        if (payload.input) {
          view.body.appendChild(el("div", { class: "msg from-user" },
            el("div", { class: "who" }, "\u4f60", stamp(event.created_at)),
            el("div", { class: "body" }, payload.input)));
        }
        return;
      }

      if (event.type === "agent_turn.started" && call) {
        view.spans[call] = { start: event.created_at || "", end: "" };
        return;
      }
      if (event.type === "agent_turn.completed" && call) {
        if (!view.spans[call]) { view.spans[call] = { start: "", end: "" }; }
        view.spans[call].end = event.created_at || "";
        return;
      }

      if (event.type === "team_turn.started") {
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
      if (event.type === "team_turn.completed") {
        var doneID = (payload.team_turn && payload.team_turn.id) || "";
        var doneRow = view["team_" + doneID];
        if (doneRow) {
          var state = doneRow.querySelector(".step-state");
          if (state) { state.textContent = "\u5b8c\u6210"; }
          var took = elapsed(
            (payload.team_turn && payload.team_turn.started_at) || "",
            event.created_at || "");
          if (took) { doneRow.appendChild(el("span", { class: "took" }, took)); }
        }
        return;
      }

      if (event.type === "agent.model_response") {
        var reply = modelReply(payload);
        if (!reply.body) { return; }
        view.lastText = reply.body;
        view.lastSeqOfCall[call] = event.seq;
        var body = el("div", { class: "body" }, reply.body);
        if (reply.raw) {
          body.appendChild(el("details", { class: "raw-reply" },
            el("summary", null, "\u539f\u59cb\u8f93\u51fa"),
            el("pre", null, reply.raw)));
        }
        var speaker = speakerLabel(event);
        view.body.appendChild(el("div", { class: "msg from-agent" },
          el("div", { class: "who" },
            speaker.team ? el("span", { class: "who-team" }, speaker.team + " / ") : null,
            speaker.agent || "\u52a9\u624b",
            stamp(event.created_at),
            el("span", { class: "took-slot", "data-call": call })),
          body));
        return;
      }

      if (event.type === "tool_call.started") {
        var key = payload.tool_call_id || ("seq-" + event.seq);
        var node = el("details", { class: "tool running" },
          el("summary", null,
            el("span", { class: "name" }, payload.tool_name || "tool"),
            el("span", { class: "arg" }, payload.arguments ? JSON.stringify(payload.arguments) : ""),
            el("span", { class: "state" }, "\u8fdb\u884c\u4e2d")),
          el("pre", null, ""));
        view.tools[key] = node;
        view.body.appendChild(node);
        return;
      }
      if (event.type === "tool_call.completed" || event.type === "tool_call.failed") {
        var target = view.tools[payload.tool_call_id || ""];
        if (!target) { return; }
        target.classList.remove("running");
        var label = target.querySelector(".state");
        if (label) { label.textContent = event.type === "tool_call.failed" ? "\u5931\u8d25" : "\u6210\u529f"; }
        var pre = target.querySelector("pre");
        if (pre) {
          pre.textContent = event.type === "tool_call.failed"
            ? (payload.error || "")
            : (typeof payload.content === "string" ? payload.content : JSON.stringify(payload.content || "", null, 2));
        }
        if (event.type === "tool_call.failed") { target.open = true; }
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
          el("div", { class: "body" }, view.reply)));
      }
      if (view.intermediate) { view.tags.appendChild(tag("\u4e2d\u95f4\u56de\u5408")); }
      if (view.origin === "wakeup") { view.tags.appendChild(tag("\u5b9a\u65f6\u68c0\u67e5", "warn")); }
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

  function startLiveRun(sessionID, container, record) {
    var turns = {};
    var activeTurnID = "";
    var finished = false;
    var seen = 0;

    function turnFor(flowTurnID) {
      if (!turns[flowTurnID]) {
        turns[flowTurnID] = createLiveTurn(flowTurnID);
        container.appendChild(turns[flowTurnID].box);
      }
      activeTurnID = flowTurnID;
      return turns[flowTurnID];
    }

    function onEvent(event) {
      if (!event || !event.type) { return; }
      seen++;
      var flowTurnID = event.flow_turn_id || "flow";
      var view = turnFor(flowTurnID);
      if (event.type === "flow_turn.waiting_input" || event.type === "flow_turn.completed") {
        view.push(event);
        view.finish(event);
        finished = true;
        return;
      }
      view.push(event);
      window.scrollTo(0, document.body.scrollHeight);
    }

    return {
      stop: subscribeLive(sessionID, lastSeqOf(record), onEvent, function (error) {
        if (activeTurnID && turns[activeTurnID]) {
          turns[activeTurnID].timing.textContent = "\u6d41\u65ad\u5f00\uff1a" + error.message;
        }
      }),
      done: function () { return finished; },
      count: function () { return seen; },
      settleAll: function () {
        Object.keys(turns).forEach(function (id) { turns[id].settle(); });
      }
    };
  }

  /* ---------- chat ---------- */
  function renderChat(record) {
    var wrap = el("div", { class: "chat" });
    var turns = groupByTurn(record.events || []);
    if (turns.length === 0) {
      wrap.appendChild(el("div", { class: "empty" }, "\u6ca1\u6709\u53ef\u5c55\u793a\u7684\u5bf9\u8bdd\u5185\u5bb9\u3002"));
      return wrap;
    }
    turns.forEach(function (turn) { wrap.appendChild(renderTurn(turn)); });
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
          error: "", intermediate: false, limit: null, startedAt: "", endedAt: ""
        };
        order.push(map[key]);
      }
      var turn = map[key];
      turn.events.push(event);
      var payload = event.payload || {};
      if (event.type === "flow_turn.started") {
        if (payload.input) { turn.input = payload.input; }
        turn.startedAt = event.created_at || turn.startedAt;
      }
      if (event.type === "flow_turn.waiting_input" || event.type === "flow_turn.completed") {
        turn.origin = payload.origin || turn.origin;
        turn.reply = payload.reply || turn.reply;
        turn.error = payload.error || turn.error;
        turn.intermediate = payload.intermediate === true;
        if (payload.limit) { turn.limit = payload.limit; }
        turn.endedAt = event.created_at || turn.endedAt;
      }
    });
    return order;
  }

  /* Who said it: team_id plus the call name carried in call_turn_id
   * ("<team_turn_id>:<call name>"). One flow turn really can hold several
   * agents — a coordinator that dispatches keeps the sub-agent's turns inside
   * the same flow turn — and without this every one of them renders as the same
   * "助手".
   *
   * Agent names are only unique inside a team: `inspect` runs in both the facts
   * and repo-info teams. So the team belongs in the label, not in a tooltip
   * that needs a mouse to reveal. When the two names coincide (the analyze
   * team's analyze call) printing both would just repeat the word. */
  function speakerLabel(event) {
    var call = event.call_turn_id || "";
    var index = call.lastIndexOf(":");
    var agent = index >= 0 ? call.slice(index + 1) : "";
    var team = event.team_id || "";
    if (!team || !agent || team === agent) {
      return { team: "", agent: agent || team };
    }
    return { team: team, agent: agent };
  }

  /* agent.model_response carries the reply twice: `reply` is what the engine
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

  function renderTurn(turn) {
    var cls = "turn";
    if (turn.origin === "wakeup") { cls += " wakeup"; }
    if (turn.error) { cls += " failed"; }
    var box = el("div", { class: cls });

    /* Per-call spans, so a message can say how long its agent actually ran.
     * agent_turn.started/completed carry the same call_turn_id as the messages;
     * a call that never completed (still running, or the turn was cut) simply
     * has no end and shows no duration. */
    var spans = {};
    var lastSeqOfCall = {};
    turn.events.forEach(function (event) {
      var call = event.call_turn_id || "";
      if (!call) { return; }
      if (event.type === "agent_turn.started") {
        spans[call] = { start: event.created_at || "", end: "" };
      }
      if (event.type === "agent_turn.completed") {
        if (!spans[call]) { spans[call] = { start: "", end: "" }; }
        spans[call].end = event.created_at || "";
      }
      if (event.type === "agent.model_response") {
        lastSeqOfCall[call] = event.seq;
      }
    });

    var head = el("div", { class: "turn-head" }, el("span", { class: "t" }, shortId(turn.id)));
    if (turn.origin) {
      head.appendChild(tag(
        turn.origin === "wakeup" ? "\u5b9a\u65f6\u68c0\u67e5" : turn.origin,
        turn.origin === "wakeup" ? "warn" : null));
    }
    if (turn.intermediate) { head.appendChild(tag("\u4e2d\u95f4\u56de\u5408")); }
    if (turn.limit) { head.appendChild(tag("\u88ab " + turn.limit.name + " \u7ec8\u6b62", "danger")); }
    var turnSpan = elapsed(turn.startedAt, turn.endedAt);
    if (turnSpan) { head.appendChild(el("span", { class: "turn-span" }, turnSpan)); }
    box.appendChild(head);

    if (turn.input) {
      box.appendChild(el("div", { class: "msg from-user" },
        el("div", { class: "who" },
          "\u4f60",
          stamp(turn.startedAt)),
        el("div", { class: "body" }, turn.input)));
    }

    var tools = {};
    var lastText = "";
    turn.events.forEach(function (event) {
      var payload = event.payload || {};
      if (event.type === "agent.model_response") {
        var view = modelReply(payload);
        if (view.body) {
          lastText = view.body;
          var body = el("div", { class: "body" }, view.body);
          if (view.raw) {
            body.appendChild(el("details", { class: "raw-reply" },
              el("summary", null, "\u539f\u59cb\u8f93\u51fa"),
              el("pre", null, view.raw)));
          }
          var speaker = speakerLabel(event);
          var span = spans[event.call_turn_id || ""] || {};
          var took = lastSeqOfCall[event.call_turn_id || ""] === event.seq ? elapsed(span.start, span.end) : "";
          box.appendChild(el("div", { class: "msg from-agent" },
            el("div", { class: "who" },
              speaker.team ? el("span", { class: "who-team" }, speaker.team + " / ") : null,
              speaker.agent || "\u52a9\u624b",
              stamp(event.created_at),
              took ? el("span", { class: "took", title: "\u8fd9\u4e2a agent \u8fd9\u4e00\u8f6e\u7684\u8017\u65f6" }, took) : null),
            body));
        }
        return;
      }
      if (event.type === "tool_call.started") {
        var key = payload.tool_call_id || ("seq-" + event.seq);
        tools[key] = appendTool(box, payload);
        return;
      }
      if (event.type === "tool_call.completed" || event.type === "tool_call.failed") {
        var node = tools[payload.tool_call_id || ""];
        if (!node) { return; }
        var state = node.querySelector(".state");
        if (state) { state.textContent = event.type === "tool_call.failed" ? "\u5931\u8d25" : "\u6210\u529f"; }
        var pre = node.querySelector("pre");
        if (pre) {
          var output = event.type === "tool_call.failed"
            ? (payload.error || "")
            : (typeof payload.content === "string" ? payload.content : JSON.stringify(payload.content || "", null, 2));
          pre.textContent = output;
        }
        if (event.type === "tool_call.failed") { node.open = true; }
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
        el("div", { class: "body" }, turn.reply)));
    }
    return box;
  }

  function appendTool(box, call) {
    var args = call.arguments ? JSON.stringify(call.arguments) : "";
    var node = el("details", { class: "tool" },
      el("summary", null,
        el("span", { class: "name" }, call.tool_name || "tool"),
        el("span", { class: "arg" }, args),
        el("span", { class: "state" }, "\u8fdb\u884c\u4e2d")),
      el("pre", null, ""));
    box.appendChild(node);
    return node;
  }

  /* ---------- tree ---------- */
  function renderTree(record) {
    var wrap = el("div");
    var nodes = record.nodes || [];
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
    if (roots.length === 0) {
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
          el("span", { class: "kind" }, node.kind),
          labelForNode(node),
          el("span", { class: "id" }, shortId(node.id))),
        el("span", { class: "time" }, duration(node)),
        el("span", { class: "tok" }, ""),
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
    wrap.appendChild(detail);
    return wrap;
  }

  function labelForNode(node) {
    if (node.kind === "flow_session") { return "\u4f1a\u8bdd"; }
    if (node.kind === "flow_turn") { return ""; }
    if (node.kind === "team_turn") { return (node.team_id || "team"); }
    if (node.kind === "call_turn") { return (node.call_id || "call") + (node.call_type ? " \u00b7 " + node.call_type : ""); }
    if (node.kind === "agent_session") { return node.agent_id || ""; }
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
    add("\u8017\u65f6", duration(node));
    add("Team", node.team_id);
    add("Call", node.call_id);
    add("Call \u7c7b\u578b", node.call_type);
    add("Agent", node.agent_id);
    add("attempt", node.attempt);
    container.appendChild(list);

    var events = (record.events || []).filter(function (event) {
      return event.seq >= node.first_seq && event.seq <= node.last_seq;
    });
    container.appendChild(el("div", { class: "faint" },
      "\u8be5\u8282\u70b9\u4e8b\u4ef6\uff08" + events.length + " \u6761\uff09"));

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
    var table = el("table", { class: "raw" },
      el("thead", null, el("tr", null,
        el("th", null, "seq"),
        el("th", null, "\u65f6\u95f4"),
        el("th", null, "type"),
        el("th", null, "\u6458\u8981"))));
    var tbody = el("tbody");
    (record.events || []).forEach(function (event) {
      var payload = event.payload || {};
      tbody.appendChild(el("tr", { id: "ev-" + event.seq },
        el("td", { class: "seq" }, String(event.seq)),
        el("td", { class: "sum" }, clock(event.created_at)),
        el("td", { class: "type" }, event.type),
        el("td", { class: "sum" },
          eventSummary(event),
          el("details", null,
            el("summary", null, "payload"),
            el("pre", null, JSON.stringify(payload, null, 2))))));
    });
    table.appendChild(tbody);
    return table;
  }

  route();
})();
