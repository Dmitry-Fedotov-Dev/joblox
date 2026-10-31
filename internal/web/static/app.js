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

// the back office: a whole table row opens its order, as the number in it does
document.addEventListener("click", function (e) {
  var row = e.target.closest && e.target.closest("tr.click[data-href]");
  if (row && !e.target.closest("a, button, form")) location.href = row.getAttribute("data-href");
});

// the storefront shows two rows of cards a page, however many fit in a row; without scripts, all of them
(function () {
  var grids = document.querySelectorAll("[data-paginate]");
  Array.prototype.forEach.call(grids, function (grid) {
    var items = Array.prototype.slice.call(grid.children), page = 0, pager = document.createElement("nav");
    pager.className = "pager";
    pager.setAttribute("aria-label", "Страницы");
    grid.parentNode.insertBefore(pager, grid.nextSibling);
    function perPage() {
      var cols = getComputedStyle(grid).gridTemplateColumns.split(" ").filter(Boolean).length || 1;
      return cols * 2;
    }
    function show(n, scroll) {
      var per = perPage(), pages = Math.max(1, Math.ceil(items.length / per));
      page = Math.min(Math.max(0, n), pages - 1);
      items.forEach(function (el, i) { el.hidden = Math.floor(i / per) !== page; });
      pager.innerHTML = "";
      if (pages > 1) {
        var add = function (label, to, on, aria) {
          var b = document.createElement("button");
          b.type = "button"; b.textContent = label; b.disabled = to < 0 || to >= pages || on;
          if (on) b.setAttribute("aria-current", "page");
          if (aria) b.setAttribute("aria-label", aria);
          b.addEventListener("click", function () { show(to, true); });
          pager.appendChild(b);
        };
        add("←", page - 1, false, "Назад");
        for (var i = 0; i < pages; i++) add(String(i + 1), i, i === page);
        add("→", page + 1, false, "Дальше");
      }
      if (scroll) grid.scrollIntoView({ behavior: "smooth", block: "start" });
      window.dispatchEvent(new Event("resize")); // the yarn and the figure follow the new height
    }
    var t = 0;
    window.addEventListener("resize", function (e) {
      if (!e.isTrusted) return;
      clearTimeout(t); t = setTimeout(function () { show(page); }, 150);
    });
    show(0);
  });
})();
