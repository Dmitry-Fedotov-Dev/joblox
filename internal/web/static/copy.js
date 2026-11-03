// Copy by a tap: an element with data-copy copies that value (or its own text when the value is empty);
// every <pre> (a command, a text to paste) gets a «Скопировать» button.
(function () {
  "use strict";
  function copy(text, el) {
    var done = function () {
      el.classList.add("copied");
      if (el.tagName === "BUTTON") { var was = el.textContent; el.textContent = "Скопировано";
        setTimeout(function () { el.textContent = was; el.classList.remove("copied"); }, 1500); }
      else setTimeout(function () { el.classList.remove("copied"); }, 1500);
    };
    if (navigator.clipboard && window.isSecureContext) { navigator.clipboard.writeText(text).then(done, function () { legacy(text) && done(); }); return; }
    if (legacy(text)) done();
  }
  function legacy(text) {
    var t = document.createElement("textarea");
    t.value = text; t.setAttribute("readonly", ""); t.style.position = "fixed"; t.style.opacity = "0";
    document.body.appendChild(t); t.select();
    var ok = false; try { ok = document.execCommand("copy"); } catch (_) {}
    document.body.removeChild(t); return ok;
  }
  document.addEventListener("click", function (e) {
    var el = e.target.closest && e.target.closest("[data-copy]");
    if (!el || e.target.closest("a")) return;
    var v = el.getAttribute("data-copy");
    copy(v ? v : el.textContent.trim(), el);
  });
  function buttons() {
    Array.prototype.forEach.call(document.querySelectorAll("pre:not([data-nocopy])"), function (pre) {
      if (pre.parentNode.classList && pre.parentNode.classList.contains("pre-wrap")) return;
      var wrap = document.createElement("div"); wrap.className = "pre-wrap";
      pre.parentNode.insertBefore(wrap, pre); wrap.appendChild(pre);
      var b = document.createElement("button"); b.type = "button"; b.className = "pre-copy"; b.textContent = "Скопировать";
      b.setAttribute("data-copy", pre.textContent.trim()); wrap.appendChild(b);
    });
  }
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", buttons); else buttons();
})();
