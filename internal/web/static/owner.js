// Owner's area. While the public address is being prepared it waits for it: in the cabinet the page reloads,
// on the setup form only the banner changes, so nothing typed is lost. In the cabinet it also reloads when
// the list of open orders changes, unless the owner is typing.
(function () {
  "use strict";
  var base = document.documentElement.getAttribute("data-base") || ""; // the site may live under a path
  function typing() {
    var el = document.activeElement;
    return el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
  }
  var setup = document.querySelector('form[action="/owner/setup"]');

  var status = document.getElementById("site-status");
  if (status) {
    var wait = setInterval(function () {
      fetch(base + "/owner/status.json", { cache: "no-store" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (d) {
          if (!d || d.site.indexOf("https://") !== 0) return;
          clearInterval(wait);
          if (!setup && !typing()) { location.reload(); return; }
          status.textContent = "Ваш сайт в интернете: ";
          var a = document.createElement("a");
          a.href = d.site; a.target = "_blank"; a.rel = "noopener"; a.textContent = d.site;
          status.appendChild(a);
        })
        .catch(function () {});
    }, 3000);
  }

  if (setup) return;
  var last = null;
  function check() {
    fetch(base + "/admin/live.json", { cache: "no-store" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        if (!d) return;
        var now = JSON.stringify((d.open || []).map(function (o) { return [o.ID, o.State, o.Paid]; }));
        if (last !== null && now !== last && !typing()) location.reload();
        last = now;
      })
      .catch(function () {});
  }
  check();
  setInterval(check, 10000);
})();
