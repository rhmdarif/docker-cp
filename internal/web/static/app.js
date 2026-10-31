// htmx 2.0.11 (htmx.min.js vendored from https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js)
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
    var target = form.getAttribute("data-stream");
    if (form.matches(htmxAttrs) || (!msg && !target)) return;
    var submitter = e.submitter;
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
        ok = await streamPost(form.action, new FormData(form, submitter), document.querySelector(target));
      } catch (err) {
        console.error("data-stream:", err);
      } finally {
        buttons.forEach(function (b) { b.disabled = false; });
      }
      form.dispatchEvent(new CustomEvent("stream:done", { bubbles: true, detail: { ok: ok } }));
    })();
  });
})();
