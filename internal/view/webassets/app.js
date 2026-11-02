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
  function apiFetch(path) {
    var headers = {};
    var token = readToken();
    if (token) { headers.Authorization = "Bearer " + token; }
    return fetch(path, { headers: headers }).then(function (response) {
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
    if (!readToken()) {
      renderLogin("");
      return;
    }
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

  /* ---------- list ---------- */
  function renderList() {
    clear(app);
    app.appendChild(el("div", { class: "top" },
      el("h1", null, "heron-ai"),
      el("span", { class: "muted" }, "\u4f1a\u8bdd\u5217\u8868")));

    apiFetch("/api/sessions")
      .then(function (response) {
        if (!response.ok) { throw new Error("HTTP " + response.status); }
        return response.json();
      })
      .then(function (payload) {
        var sessions = payload.sessions || [];
        if (sessions.length === 0) {
          app.appendChild(el("div", { class: "empty" },
            "\u8fd8\u6ca1\u6709\u4f1a\u8bdd\u3002\u5728\u7ec8\u7aef\u91cc\u8fd0\u884c heron-ai \u53d1\u8d77\u4e00\u4e2a\uff0c\u8fd9\u91cc\u4f1a\u51fa\u73b0\u3002"));
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
          error: "", intermediate: false, limit: null
        };
        order.push(map[key]);
      }
      var turn = map[key];
      turn.events.push(event);
      var payload = event.payload || {};
      if (event.type === "flow_turn.started" && payload.input) { turn.input = payload.input; }
      if (event.type === "flow_turn.waiting_input" || event.type === "flow_turn.completed") {
        turn.origin = payload.origin || turn.origin;
        turn.reply = payload.reply || turn.reply;
        turn.error = payload.error || turn.error;
        turn.intermediate = payload.intermediate === true;
        if (payload.limit) { turn.limit = payload.limit; }
      }
    });
    return order;
  }

  function renderTurn(turn) {
    var cls = "turn";
    if (turn.origin === "wakeup") { cls += " wakeup"; }
    if (turn.error) { cls += " failed"; }
    var box = el("div", { class: cls });

    var head = el("div", { class: "turn-head" }, el("span", { class: "t" }, shortId(turn.id)));
    if (turn.origin) {
      head.appendChild(tag(
        turn.origin === "wakeup" ? "\u5b9a\u65f6\u68c0\u67e5" : turn.origin,
        turn.origin === "wakeup" ? "warn" : null));
    }
    if (turn.intermediate) { head.appendChild(tag("\u4e2d\u95f4\u56de\u5408")); }
    if (turn.limit) { head.appendChild(tag("\u88ab " + turn.limit.name + " \u7ec8\u6b62", "danger")); }
    box.appendChild(head);

    if (turn.input) {
      box.appendChild(el("div", { class: "msg" },
        el("div", { class: "who" }, "\u4f60"),
        el("div", { class: "body" }, turn.input)));
    }

    var tools = {};
    var lastText = "";
    turn.events.forEach(function (event) {
      var payload = event.payload || {};
      if (event.type === "agent.model_response") {
        var value = typeof payload.text === "string" ? payload.text.trim() : "";
        if (value) {
          lastText = value;
          box.appendChild(el("div", { class: "msg" },
            el("div", { class: "who" }, "\u52a9\u624b"),
            el("div", { class: "body" }, value)));
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
      box.appendChild(el("div", { class: "msg" + (turn.intermediate ? " intermediate" : "") },
        el("div", { class: "who" }, "\u52a9\u624b"),
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
