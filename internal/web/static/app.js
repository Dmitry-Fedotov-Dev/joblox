// Storefront helpers. The pages work without this file: it only adds the cart sum preview,
// refreshes the order page when the order changes and keeps the online counter.
(function () {
  "use strict";
  var base = document.documentElement.getAttribute("data-base") || ""; // the site may live under a path

  // Cart sum preview: the server computes the real total, this is only a hint while choosing.
  var cart = document.querySelector("[data-cart]");
  if (cart) {
    var out = cart.querySelector("[data-total]");
    var recalc = function () {
      var cents = 0;
      cart.querySelectorAll(".item").forEach(function (item) {
        var price = item.querySelector("[data-price]");
        var qty = item.querySelector("input[type=number]");
        if (!price || !qty) return;
        var q = parseInt(qty.value, 10);
        if (q > 0) cents += Math.round(parseFloat(price.getAttribute("data-price")) * 100) * q;
      });
      if (out) out.textContent = (cents / 100).toFixed(2);
    };
    cart.addEventListener("input", recalc);
    recalc();
  }

  // Order page: poll the state, reload when the operator moved the order.
  var order = document.querySelector("[data-order]");
  if (order) {
    var token = order.getAttribute("data-order");
    var version = order.getAttribute("data-version");
    var poll = function () {
      if (document.hidden) return;
      fetch(base + "/o/" + encodeURIComponent(token) + "/state.json", { credentials: "same-origin", cache: "no-store" })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (s) {
          var typing = document.activeElement && document.activeElement.tagName === "INPUT";
          if (s && String(s.version) !== version && !typing) location.reload();
        })
        .catch(function () {});
    };
    setInterval(poll, 10000);
  }

  // Online counter: a heartbeat while the page is visible.
  var beat = function () {
    if (!document.hidden) fetch(base + "/beat", { method: "POST", credentials: "same-origin" }).catch(function () {});
  };
  setInterval(beat, 30000);
})();
