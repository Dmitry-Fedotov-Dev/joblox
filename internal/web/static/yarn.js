// Yarn on the cards of a knitting shop: the thread from the needles in a card's corner is a little rope with
// weight. It is held to the card's edge at a few points and sags between them, differently on every card;
// at its end lies a ball of yarn. The kitten (kitten.js) knocks a ball off now and then: it swings on the
// thread, the thread snaps, the ball tumbles away, the loose end hangs and winds back to the needles, then
// the thread wraps the card again and a new ball appears. Without this script the card keeps its plain
// CSS frame (.yarn .loop).
(function () {
  "use strict";
  var ropes = [];
  var reduce = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var SVG = "http://www.w3.org/2000/svg";
  var G = 1400;                // gravity, px/s²
  var STEP = 1 / 60, ITER = 14, SEG = 6;

  function rng(seed) {         // mulberry32: the same card drapes the same way on every visit
    var a = seed >>> 0;
    return function () {
      a = (a + 0x6D2B79F5) >>> 0;
      var t = Math.imul(a ^ (a >>> 15), 1 | a);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }

  function Rope(el) {
    this.el = el;
    this.seed = +el.getAttribute("data-seed") || 1;
    this.toy = el.classList.contains("y-bunny") ? "bunny" : "bear";
    this.svg = document.createElementNS(SVG, "svg");
    this.svg.setAttribute("class", "rope");
    this.svg.setAttribute("aria-hidden", "true");
    this.path = document.createElementNS(SVG, "path");
    this.svg.appendChild(this.path);
    var needles = el.querySelector(".needles");
    el.insertBefore(this.svg, needles);
    this.ball = document.createElement("img");
    this.ball.className = "rope-ball";
    this.ball.alt = "";
    this.ball.src = el.querySelector(".ball").src.replace(/ball-(bear|bunny)\.png/, "yarnball-$1.png");
    el.appendChild(this.ball);
    el.classList.add("live");
    this.state = "rest";
    this.build();
  }
  var R = Rope.prototype;

  // build lays the thread along the frame: from the needles left along the top, down the left side and
  // right along the bottom to the ball. Pins hold it to the edge every so often; between them it gets a
  // little slack, and here and there a lot, so a loop hangs down.
  R.build = function (reveal) {
    var el = this.el, box = el.getBoundingClientRect(), nd = el.querySelector(".needles").getBoundingClientRect();
    var w = box.width, h = box.height, r = rng(this.seed);
    if (w < 40 || h < 40 || nd.width === 0) { this.pts = null; return; }
    var page = el.classList.contains("page-yarn");
    var m = page ? 10 : w < 260 ? 4 : 6, ex = nd.left - box.left + 1, ey = nd.top - box.top + nd.height * 7.5 / 30;
    this.thick = page ? 3.5 : w < 260 ? 2.5 : 3;
    this.br = page ? 22 : w < 260 ? 13 : 17;             // the ball's radius
    var bx = w - (page ? 60 : w < 260 ? 26 : 34), by = h + m;
    var route = [[ex, ey], [-m, ey], [-m, by], [bx - this.br, by]];
    // the pins: the needles, around each corner, then every 55–95 px along the way
    var pins = [], len = 0, i;
    var segs = [];
    for (i = 0; i < 3; i++) {
      var a = route[i], b = route[i + 1], d = Math.hypot(b[0] - a[0], b[1] - a[1]);
      segs.push({ a: a, b: b, d: d, at: len, side: i }); len += d;
    }
    function point(s) {
      for (var k = 0; k < segs.length; k++) {
        var g = segs[k];
        if (s <= g.at + g.d || k === segs.length - 1) {
          var t = Math.max(0, Math.min(1, (s - g.at) / g.d));
          return { x: g.a[0] + (g.b[0] - g.a[0]) * t, y: g.a[1] + (g.b[1] - g.a[1]) * t, side: g.side };
        }
      }
    }
    var marks = [0, len];
    for (i = 1; i < 3; i++) marks.push(segs[i].at - 12, segs[i].at + 12); // round the corners
    for (var s = 50 + r() * 30; s < len - 30; s += 55 + r() * 40) marks.push(s);
    marks.sort(function (x, y) { return x - y; });
    var clean = [];
    for (i = 0; i < marks.length; i++) if (!clean.length || marks[i] - clean[clean.length - 1] > 18 || i === marks.length - 1) clean.push(marks[i]);
    if (clean[clean.length - 1] !== len) clean.push(len);
    var drapes = 0;
    this.pts = []; this.cons = [];
    var pts = this.pts, cons = this.cons;
    for (i = 0; i < clean.length; i++) {
      var p = point(clean[i]);
      if (i > 0 && i < clean.length - 1) {              // pins sit a little off the edge, never in a line
        if (p.side === 1) p.x += (r() - 0.5) * 3; else p.y += (r() - 0.5) * 3;
      }
      if (i > 0) {
        var q = pts[pts.length - 1], dx = p.x - q.x, dy = p.y - q.y, d0 = Math.hypot(dx, dy);
        var horiz = Math.abs(dx) > Math.abs(dy);
        var top = horiz && p.y < h / 2;                 // along the top: loose, now and then a loop hangs down
        var slack = top ? 0.03 + r() * 0.06 : horiz ? 0.01 + r() * 0.025 : 0.004; // the bottom lies nearly straight
        if (top && d0 > 40 && drapes < (page ? 3 : 2) && r() < 0.45) { slack = page ? 0.06 + r() * 0.1 : 0.16 + r() * 0.28; drapes++; }
        var L = d0 * (1 + slack), n = Math.max(1, Math.round(L / SEG)), sag = d0 * Math.sqrt(3 * slack / 8);
        for (var k = 1; k <= n; k++) {
          var t = k / n, x = q.x + dx * t, y = q.y + dy * t;
          if (horiz) y += Math.sin(Math.PI * t) * sag;
          pts.push({ x: x, y: y, px: x, py: y, w: k === n ? 0 : 1, tx: x, ty: y });
          cons.push(L / n);
        }
        pts[pts.length - 1].tx = p.x; pts[pts.length - 1].ty = p.y;
        pts[pts.length - 1].x = pts[pts.length - 1].px = p.x;
        pts[pts.length - 1].y = pts[pts.length - 1].py = p.y;
        pts[pts.length - 1].pin = true;
      } else {
        pts.push({ x: p.x, y: p.y, px: p.x, py: p.y, w: 0, tx: p.x, ty: p.y, pin: true });
      }
    }
    var end = pts[pts.length - 1];
    end.ball = true;
    this.total = 0;
    for (i = 0; i < cons.length; i++) { this.total += cons[i]; pts[i + 1].s = this.total; }
    pts[0].s = 0;
    for (i = 0; i < 160; i++) this.step(STEP);              // settle before the first paint
    this.reveal = reveal === undefined ? Infinity : reveal;
    this.calm = 0;
    this.draw();
  };

  R.step = function (dt) {
    var pts = this.pts, cons = this.cons, i, p;
    if (!pts) return 0;
    var n = pts.length, rv = this.reveal;
    var moved = 0, g = G * dt * dt;
    for (i = 0; i < n; i++) {
      p = pts[i];
      if (p.w === 0 || p.s > rv) continue;
      var vx = (p.x - p.px) * 0.985, vy = (p.y - p.py) * 0.985;
      p.px = p.x; p.py = p.y;
      p.x += vx; p.y += vy + g;
      moved = Math.max(moved, Math.abs(vx) + Math.abs(vy));
    }
    for (var it = 0; it < ITER; it++) {
      for (i = 0; i < cons.length && i + 1 < n; i++) {
        var a = pts[i], b = pts[i + 1];
        var wa = a.s > rv ? 0 : a.w, wb = b.s > rv ? 0 : b.w, ws = wa + wb;  // not laid yet: holds still
        if (ws === 0) continue;
        var dx = b.x - a.x, dy = b.y - a.y, d = Math.hypot(dx, dy) || 0.001, diff = (d - cons[i]) / d / ws;
        if (d < cons[i] && !this.taut) continue;         // thread: it pulls, it does not push
        a.x += dx * diff * wa; a.y += dy * diff * wa;
        b.x -= dx * diff * wb; b.y -= dy * diff * wb;
      }
    }
    return moved;
  };

  // draw: a smooth curve through the points, and the ball at the end
  R.draw = function () {
    var pts = this.pts;
    if (!pts) { this.path.setAttribute("d", ""); return; }
    var last = pts.length - 1;
    while (last > 0 && pts[last].s > this.reveal) last--;
    var d = "M" + pts[0].x.toFixed(1) + " " + pts[0].y.toFixed(1);
    for (var i = 1; i < last; i++) {
      var mx = (pts[i].x + pts[i + 1].x) / 2, my = (pts[i].y + pts[i + 1].y) / 2;
      d += "Q" + pts[i].x.toFixed(1) + " " + pts[i].y.toFixed(1) + " " + mx.toFixed(1) + " " + my.toFixed(1);
    }
    if (last > 0) d += "L" + pts[last].x.toFixed(1) + " " + pts[last].y.toFixed(1);
    this.path.setAttribute("d", d);
    this.path.style.strokeWidth = this.thick + "px";
    var e = pts[pts.length - 1], br = this.br, show = this.free || (e.ball && this.reveal >= this.total);
    var cx = e.x + br, cy = e.y;                         // lying: the thread runs into the ball's side
    if (this.state === "swing") { cx = e.x; cy = e.y + br * 0.8; } // hanging: the ball under the thread's end
    if (this.free) { cx = this.free.x; cy = this.free.y; }
    this.ball.style.width = this.ball.style.height = br * 2 + "px";
    this.ball.style.transform = "translate(" + (cx - br).toFixed(1) + "px," + (cy - br).toFixed(1) + "px) rotate(" + (this.spin || 0).toFixed(2) + "rad)";
    this.ball.style.opacity = show ? (this.free ? Math.max(0, this.fade) : 1) : 0;
  };

  R.awake = function () { return this.state !== "rest" || this.calm < 40; };

  R.tick = function (now, dt) {
    var pts = this.pts;
    if (!pts) return;
    var moved = this.step(dt);
    this.calm = moved < 0.03 ? this.calm + 1 : 0;
    var t = (now - this.t0) / 1000, end = pts[pts.length - 1];
    switch (this.state) {
      case "swing":                                    // the ball hangs and swings on the thread, then it snaps
        this.spin += (end.x - end.px) / this.br;
        if (t > 1.3) {
          this.free = { x: end.x, y: end.y + this.br * 0.8, vx: (end.x - end.px) / dt, vy: (end.y - end.py) / dt - 80 };
          this.fade = 1; end.ball = false; end.w = 1;
          for (var k = 0; k < 4 && pts.length > 2; k++) { pts.pop(); this.cons.pop(); } // a bit stays on the ball
          this.state = "loose"; this.t0 = now;
        }
        break;
      case "loose":                                    // the end hangs free; then it winds back to the needles
        if (t > 1.8) { this.state = "wind"; this.t0 = now; this.acc = 0; }
        break;
      case "wind":
        this.acc += dt * 260;
        while (this.acc > 0 && pts.length > 2) {
          this.acc -= this.cons[this.cons.length - 1];
          pts.pop(); this.cons.pop();
          var tail = pts[pts.length - 1];
          if (tail.pin && pts.length > 1) { tail.pin = false; tail.w = 1; } // passing a pin frees it
        }
        if (pts.length <= 2) { this.state = "empty"; this.t0 = now; this.path.setAttribute("d", ""); }
        break;
      case "empty":
        if (t > 0.7) { this.build(0); this.state = "wrap"; this.t0 = now; this.el.classList.add("wrapping"); }
        break;
      case "wrap":                                     // the thread wraps the card again, the ball comes last
        this.reveal = t * 340;
        if (this.reveal >= this.total) {
          this.reveal = Infinity; this.state = "rest"; this.calm = 0;
          this.el.classList.remove("snapped", "wrapping");
          this.ball.classList.remove("pop"); void this.ball.offsetWidth; this.ball.classList.add("pop");
        }
        break;
    }
    if (this.free) {
      var f = this.free;
      f.vy += G * dt; f.x += f.vx * dt; f.y += f.vy * dt;
      this.spin += f.vx * dt / this.br; this.fade -= dt * 1.1;
      if (this.fade <= 0) this.free = null;
    }
    this.draw();
  };

  // knock: the kitten sends the ball off the edge
  R.knock = function () {
    if (this.state !== "rest" || !this.pts) return false;
    var pts = this.pts, end = pts[pts.length - 1], dir = Math.random() < 0.5 ? -1 : 1;
    end.w = 0.35; end.pin = false;
    end.px = end.x - dir * 4; end.py = end.y + 5;         // a push: sideways and up
    for (var i = pts.length - 2; i > 0; i--) {           // the last pin before the ball lets go too
      if (pts[i].pin) { pts[i].pin = false; pts[i].w = 1; break; }
    }
    this.spin = 0; this.state = "swing"; this.t0 = performance.now(); this.calm = 0;
    this.el.classList.add("snapped");
    wake();
    return true;
  };

  // nudge: a paw bats the ball; it hops and the thread jerks
  R.nudge = function () {
    if (this.state !== "rest" || !this.pts) return;
    var pts = this.pts;
    for (var i = Math.max(1, pts.length - 22); i < pts.length - 1; i++) if (pts[i].w) { pts[i].py += 5; pts[i].px += (Math.random() - 0.5) * 6; }
    var cls = Math.random() < 0.6 ? "hop" : "wiggle";
    this.ball.classList.remove("hop", "wiggle"); void this.ball.offsetWidth; this.ball.classList.add(cls);
    this.calm = 0; wake();
  };

  // touch: the kitten walks along the thread (viewport coordinates)
  R.touch = function (cx, cy) {
    if (!this.pts) return;
    var box = this.el.getBoundingClientRect(), x = cx - box.left, y = cy - box.top, hit = false;
    for (var i = 1; i < this.pts.length; i++) {
      var p = this.pts[i];
      if (p.w && Math.abs(p.x - x) < 26 && Math.abs(p.y - y) < 26) { p.px -= (x - p.x) * 0.06; p.py -= 1.2; hit = true; }
    }
    if (hit) { this.calm = 0; wake(); }
  };

  function find(el) {
    for (var i = 0; i < ropes.length; i++) if (ropes[i].el === el) return ropes[i];
    return null;
  }

  var running = false, last = 0;
  function frame(now) {
    var dt = Math.min(0.033, (now - last) / 1000 || STEP); last = now;
    var any = false;
    for (var i = 0; i < ropes.length; i++) {
      var r = ropes[i];
      if (r.visible !== false && r.awake()) { r.tick(now, dt); any = true; }
    }
    if (any) requestAnimationFrame(frame); else running = false;
  }
  function wake() {
    if (reduce || running) return;
    running = true; last = performance.now();
    requestAnimationFrame(frame);
  }

  function init() {
    var els = document.querySelectorAll(".yarn");
    for (var i = 0; i < els.length; i++) {
      var card = els[i].closest(".grid > .pcard");
      // a page with its own thread keeps the only ball; else in a row of cards only the penultimate has one
      if (card && (document.querySelector(".page-yarn") || card.nextElementSibling !== card.parentNode.lastElementChild)) {
        els[i].classList.add("bare"); continue;
      }
      if (!find(els[i]) && els[i].querySelector(".needles")) ropes.push(new Rope(els[i]));
    }
    if ("IntersectionObserver" in window) {
      var io = new IntersectionObserver(function (es) {
        es.forEach(function (e) { var r = find(e.target); if (r) { r.visible = e.isIntersecting; if (e.isIntersecting) wake(); } });
      });
      ropes.forEach(function (r) { io.observe(r.el); });
    }
    var t = 0;
    window.addEventListener("resize", function () {
      clearTimeout(t);
      t = setTimeout(function () { ropes.forEach(function (r) { if (r.state === "rest") r.build(); }); }, 150);
    });
    // the needles' picture sets the geometry: lay the thread again once it has its size
    ropes.forEach(function (r) {
      var img = r.el.querySelector(".needles");
      if (img && !img.complete) img.addEventListener("load", function () { r.build(); }, { once: true });
    });
    wake();
  }

  window.JobloxYarn = {
    knock: function (el) { var r = find(el); return !!(r && r.knock()); },
    nudge: function (el) { var r = find(el); if (r) r.nudge(); },
    touch: function (x, y) { ropes.forEach(function (r) { if (r.visible !== false && r.state === "rest") r.touch(x, y); }); },
    busy: function (el) { var r = find(el); return !!(r && r.state !== "rest"); },
    ball: function (el) { var r = find(el); return r ? r.ball : null; },
    ropes: ropes
  };
  if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init); else init();
})();
