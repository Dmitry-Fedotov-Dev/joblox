// Owner's area: reloads the page when the list of open orders changes, unless the owner is typing.
(function () {
  "use strict";
  var last = null;
  function check() {
    fetch("/admin/live.json", { cache: "no-store" })
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (d) {
        if (!d) return;
        var now = JSON.stringify((d.open || []).map(function (o) { return [o.ID, o.State, o.Paid]; }));
        var typing = document.activeElement && /^(INPUT|TEXTAREA)$/.test(document.activeElement.tagName);
        if (last !== null && now !== last && !typing) location.reload();
        last = now;
      })
      .catch(function () {});
  }
  check();
  setInterval(check, 10000);
})();
