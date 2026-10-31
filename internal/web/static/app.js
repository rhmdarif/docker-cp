// htmx 2.0.11 (htmx.min.js vendored from https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js)
// xterm.js @xterm/xterm 5.5.0 (xterm.js, xterm.css) and @xterm/addon-fit 0.10.0 (addon-fit.js), vendored from the npm registry tarballs
(function () {
  "use strict";

  function csrfToken() {
    var m = document.querySelector('meta[name="csrf"]');
    return m ? m.content : "";
  }

  // Every htmx request carries the session's CSRF token.
  document.addEventListener("htmx:configRequest", function (e) {
    e.detail.headers["X-CSRF-Token"] = csrfToken();
  });

  // ---- confirmation dialog for [data-confirm], replacing the browser built-in ----

  var dialog = null;

  function confirmDialog() {
    if (dialog) return dialog;
    dialog = document.createElement("dialog");
    dialog.className = "confirm";
    dialog.innerHTML =
      '<form method="dialog">' +
      '<p class="confirm-msg"></p>' +
      '<div class="confirm-actions">' +
      '<button value="cancel" class="btn btn-ghost">Batal</button>' +
      '<button value="ok" class="btn btn-danger">Ya, lanjutkan</button>' +
      "</div></form>";
    document.body.appendChild(dialog);
    return dialog;
  }

  // ask shows msg in a modal <dialog> and resolves to true on confirmation.
  function ask(msg) {
    var d = confirmDialog();
    d.querySelector(".confirm-msg").textContent = msg;
    d.returnValue = "";
    return new Promise(function (resolve) {
      d.addEventListener("close", function onClose() {
        d.removeEventListener("close", onClose);
        resolve(d.returnValue === "ok");
      });
      d.showModal();
      d.querySelector('button[value="cancel"]').focus();
    });
  }

  var htmxAttrs = "[hx-get],[hx-post],[hx-put],[hx-patch],[hx-delete]";

  // htmx-driven elements: hold the request until confirmed.
  document.addEventListener("htmx:confirm", function (e) {
    var msg = e.detail.elt.getAttribute("data-confirm");
    if (!msg) return;
    e.preventDefault();
    ask(msg).then(function (ok) {
      if (ok) e.detail.issueRequest(true);
    });
  });

  // Plain links and buttons: intercept the click, replay it once confirmed.
  document.addEventListener(
    "click",
    function (e) {
      var el = e.target.closest("a[data-confirm],button[data-confirm],input[data-confirm]");
      if (!el || el.matches(htmxAttrs) || el.dataset.confirmed) return;
      e.preventDefault();
      e.stopPropagation();
      ask(el.getAttribute("data-confirm")).then(function (ok) {
        if (!ok) return;
        el.dataset.confirmed = "1";
        el.click();
        delete el.dataset.confirmed;
      });
    },
    true
  );

  // ---- streaming POST ----

  // streamPost POSTs formData to url with the CSRF header and appends the
  // response body to a <pre> (targetEl itself, or one created inside it) as
  // it arrives. Resolves to true when the response status is 2xx; never
  // rejects. A missing targetEl is logged and resolves to false.
  async function streamPost(url, formData, targetEl) {
    if (!targetEl) {
      console.error("streamPost: target element not found for", url);
      return false;
    }
    var pre = targetEl.tagName === "PRE" ? targetEl : targetEl.querySelector("pre");
    if (!pre) {
      pre = document.createElement("pre");
      targetEl.appendChild(pre);
    }
    pre.textContent = "";
    pre.classList.add("stream");
    var resp;
    try {
      resp = await fetch(url, {
        method: "POST",
        body: formData,
        headers: { "X-CSRF-Token": csrfToken() },
        credentials: "same-origin",
      });
    } catch (err) {
      pre.textContent += "Gagal terhubung ke panel: " + err + "\n";
      return false;
    }
    if (resp.redirected && new URL(resp.url).pathname === "/login") {
      window.location.href = "/login";
      return false;
    }
    if (!resp.ok && (resp.headers.get("Content-Type") || "").indexOf("text/html") === 0) {
      // Validation failure: show the re-rendered page (error banner, refilled form).
      var html = await resp.text();
      document.open();
      document.write(html);
      document.close();
      return false;
    }
    if (!resp.body) {
      pre.textContent += await resp.text();
      return resp.ok;
    }
    var reader = resp.body.getReader();
    var dec = new TextDecoder();
    try {
      for (;;) {
        var chunk = await reader.read();
        if (chunk.done) break;
        pre.textContent += dec.decode(chunk.value, { stream: true });
        pre.scrollTop = pre.scrollHeight;
      }
      pre.textContent += dec.decode();
    } catch (err) {
      pre.textContent += "\nKoneksi terputus: " + err + "\n";
      return false;
    }
    return resp.ok;
  }
  window.streamPost = streamPost;

  // form[data-stream="<selector>"] submits through streamPost into the
  // element matched by the selector, and fires "stream:done" (detail.ok) on
  // the form when finished. Combines with data-confirm on the form.
  document.addEventListener("submit", function (e) {
    var form = e.target;
    var msg = form.getAttribute("data-confirm");
    var submitter = e.submitter;
    var target = (submitter && submitter.getAttribute("data-stream")) || form.getAttribute("data-stream");
    if (form.matches(htmxAttrs) || (!msg && !target)) return;
    if (msg && !form.dataset.confirmed) {
      e.preventDefault();
      ask(msg).then(function (ok) {
        if (!ok) return;
        form.dataset.confirmed = "1";
        form.requestSubmit(submitter && submitter.form === form ? submitter : undefined);
        delete form.dataset.confirmed;
      });
      return;
    }
    if (!target) return;
    e.preventDefault();
    var buttons = form.querySelectorAll("button,input[type=submit]");
    buttons.forEach(function (b) { b.disabled = true; });
    (async function () {
      var ok = false;
      try {
        ok = await streamPost((submitter && submitter.formAction) || form.action, new FormData(form, submitter), document.querySelector(target));
      } catch (err) {
        console.error("data-stream:", err);
      } finally {
        buttons.forEach(function (b) { b.disabled = false; });
      }
      form.dispatchEvent(new CustomEvent("stream:done", { bubbles: true, detail: { ok: ok } }));
    })();
  });
  // pre[data-sse="<url>"] shows a server-sent event log stream. The stream is
  // closed when the page is left so the server stops following the container.
  document.querySelectorAll("pre[data-sse]").forEach(function (pre) {
    var es = new EventSource(pre.getAttribute("data-sse"));
    es.onmessage = function (ev) {
      var atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 20;
      pre.textContent += ev.data + "\n";
      var lines = pre.textContent.split("\n");
      if (lines.length > 2001) pre.textContent = lines.slice(lines.length - 2001).join("\n");
      if (atBottom) pre.scrollTop = pre.scrollHeight;
    };
    // No auto-reconnect: a reconnect would replay the tail and duplicate lines.
    es.onerror = function () {
      es.close();
      pre.textContent += "\u2014 stream berakhir \u2014\n";
    };
    window.addEventListener("pagehide", function () { es.close(); });
  });
  // ---- terminals (xterm.js over WebSocket) ----

  // openTerminal attaches an xterm to el and bridges it to the WebSocket at
  // wsURL (binary frames for data, JSON text frames for resize). Returns
  // { close } which closes the socket and disposes the terminal.
  function openTerminal(el, wsURL) {
    var term = new window.Terminal({ cursorBlink: true, fontFamily: 'ui-monospace, Menlo, Consolas, monospace', fontSize: 14 });
    var fit = new window.FitAddon.FitAddon();
    term.loadAddon(fit);
    term.open(el);
    var scheme = location.protocol === "https:" ? "wss:" : "ws:";
    var ws = new WebSocket(scheme + "//" + location.host + wsURL);
    ws.binaryType = "arraybuffer";
    var enc = new TextEncoder();
    function sendResize() {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: "resize", cols: term.cols, rows: term.rows }));
      }
    }
    function refit() {
      if (el.offsetWidth === 0 || el.offsetHeight === 0) return;
      try { fit.fit(); } catch (e) { /* not laid out yet */ }
    }
    ws.onopen = function () { refit(); sendResize(); term.focus(); };
    ws.onmessage = function (ev) {
      if (ev.data instanceof ArrayBuffer) term.write(new Uint8Array(ev.data));
    };
    ws.onclose = function () { term.write("\r\n\x1b[2m[koneksi ditutup]\x1b[0m\r\n"); };
    term.onData(function (d) {
      if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(d));
    });
    term.onResize(sendResize);
    var onWin = function () { refit(); };
    window.addEventListener("resize", onWin);
    var ro = typeof ResizeObserver === "function" ? new ResizeObserver(onWin) : null;
    if (ro) ro.observe(el);
    return {
      focus: function () { term.focus(); },
      refit: refit,
      close: function () {
        window.removeEventListener("resize", onWin);
        if (ro) ro.disconnect();
        ws.onclose = null;
        ws.close();
        term.dispose();
      }
    };
  }

  // xterm.js is a deferred script loaded after this file, so wait for it.
  document.addEventListener("DOMContentLoaded", function () {
    var single = document.getElementById("term-single");
    if (single && window.Terminal) {
      var st = openTerminal(single, single.getAttribute("data-ws"));
      window.addEventListener("pagehide", function () { st.close(); });
    }

    // /terminal: one tab per host shell, each with its own WebSocket.
    var panes = document.getElementById("term-panes");
    if (panes && window.Terminal) {
      var tabs = document.getElementById("term-tabs");
      var tabList = [];
      var counter = 0;
      var select = function (t) {
        tabList.forEach(function (o) {
          var on = o === t;
          o.pane.hidden = !on;
          o.tab.setAttribute("aria-selected", on ? "true" : "false");
        });
        t.term.refit();
        t.term.focus();
      };
      var addTab = function () {
        counter++;
        var pane = document.createElement("div");
        pane.className = "term-pane";
        panes.appendChild(pane);
        var tab = document.createElement("span");
        tab.className = "term-tab btn";
        tab.setAttribute("role", "tab");
        var label = document.createElement("button");
        label.type = "button";
        label.className = "btn";
        label.textContent = "Shell " + counter;
        var x = document.createElement("button");
        x.type = "button";
        x.className = "btn";
        x.setAttribute("aria-label", "Tutup Shell " + counter);
        x.textContent = "\u00d7";
        tab.appendChild(label);
        tab.appendChild(x);
        tabs.appendChild(tab);
        var t = { pane: pane, tab: tab, term: null };
        tabList.push(t);
        t.term = openTerminal(pane, panes.getAttribute("data-ws"));
        label.addEventListener("click", function () { select(t); });
        x.addEventListener("click", function () {
          t.term.close();
          pane.remove();
          tab.remove();
          tabList.splice(tabList.indexOf(t), 1);
          if (tabList.length) select(tabList[tabList.length - 1]);
        });
        select(t);
      };
      document.getElementById("term-add").addEventListener("click", addTab);
      window.addEventListener("pagehide", function () { tabList.forEach(function (t) { t.term.close(); }); });
      addTab();
    }
  });
})();
