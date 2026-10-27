// Built-in dashboard: reads /admin/live.json every two seconds and fills the tiles and the RPS chart.
(function () {
  "use strict";

  var el = function (k) { return document.querySelector('[data-k="' + k + '"]'); };
  var set = function (k, v) { var n = el(k); if (n) n.textContent = v; };
  var fmt = function (v, d) { return Number(v || 0).toFixed(d); };

  var duration = function (sec) {
    var d = Math.floor(sec / 86400), h = Math.floor(sec % 86400 / 3600), m = Math.floor(sec % 3600 / 60);
    if (d) return d + " д " + h + " ч";
    if (h) return h + " ч " + m + " мин";
    return m + " мин";
  };

  var chart = function (series) {
    var svg = el("series");
    if (!svg || !series.length) return;
    var max = Math.max.apply(null, series.concat([1]));
    var step = 600 / Math.max(series.length - 1, 1);
    var pts = series.map(function (v, i) { return (i * step).toFixed(1) + "," + (115 - v / max * 105).toFixed(1); });
    var ns = "http://www.w3.org/2000/svg";
    svg.textContent = "";
    var area = document.createElementNS(ns, "path");
    area.setAttribute("class", "area");
    area.setAttribute("d", "M0,120 L" + pts.join(" L") + " L600,120 Z");
    var line = document.createElementNS(ns, "path");
    line.setAttribute("d", "M" + pts.join(" L"));
    svg.appendChild(area);
    svg.appendChild(line);
  };

  var list = function (open) {
    var table = el("list");
    if (!table) return;
    while (table.rows.length > 1) table.deleteRow(1);
    (open || []).forEach(function (o) {
      var row = table.insertRow();
      var state = o.State + (o.Review ? " ⚠️" : "") + (o.Paid ? " · оплачен" : "");
      [o.ID, state, o.Total, o.At].forEach(function (v, i) {
        var c = row.insertCell();
        c.textContent = v;
        if (i === 2) c.className = "num";
      });
    });
  };

  var tick = function () {
    fetch("/admin/live.json", { cache: "no-store" })
      .then(function (r) { return r.json(); })
      .then(function (d) {
        var l = d.live, s = l.system, o = d.orders;
        set("online", l.online);
        set("rps", fmt(l.rps_10s, 1));
        set("rps_more", "за минуту " + fmt(l.rps_1m, 1) + ", за 5 минут " + fmt(l.rps_5m, 1));
        set("p95", fmt(l.p95_ms, 0) + " мс");
        set("p50", "95% запросов быстрее; медиана " + fmt(l.p50_ms, 1) + " мс");
        set("errors", l.errors_5m);
        set("cpu", s.linux ? fmt(s.cpu_percent, 0) + " %" : "—");
        set("load", s.linux ? "load " + fmt(s.load1, 2) + " на " + s.cores + " ядер" : s.cores + " ядер");
        set("mem", s.linux ? fmt(s.mem_used_mb, 0) + " МБ" : "—");
        set("mem_total", s.linux ? "из " + fmt(s.mem_total_mb, 0) + " МБ" : "только на Linux");
        set("proc", fmt(s.process_mb, 0) + " МБ");
        set("goroutines", s.goroutines + " горутин");
        set("uptime", duration(l.uptime_sec));
        set("paused", d.paused ? "приём заказов на паузе" : "приём заказов открыт");
        set("today", o.Today);
        set("open", o.Open);
        set("review", o.Review);
        set("paid", o.PaidToday);
        set("revenue", d.revenue);
        chart(l.series);
        list(d.open);
      })
      .catch(function () {});
  };
  tick();
  setInterval(tick, 2000);
})();
