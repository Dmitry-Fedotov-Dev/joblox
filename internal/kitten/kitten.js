// Dymok lives behind the page: hides behind cards and buttons, hunts the cursor and the finger, steps out
// where he can be seen and cheers when something goes into the cart. He never takes a press meant for the
// page: links, buttons, forms and cards are always the page's.
(function () {
  "use strict";
  if (!window.Dymok) return;
  var reduceMotion = window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduceMotion) return;

  var IGNORE = "a,button,input,textarea,select,label,form,summary,details,.card,.tile,.chip,.gallery,.buy,.sizes," +
               "header,footer,.sticky,.banner,[data-dymok-ignore]";
  var COVER = IGNORE + ",img,figure,h1,h2,h3,p,li,dl,table,pre,code,.price,.lines,.facts,.empty,.hero-meta";
  var EMO_JOY = 1, MODE_JUMP = 3, MODE_SMOKE = 5, MODE_ROLL = 12, MODE_FLIP = 13, BODY_W = 26, BODY_H = 27;

  var kitten;
  try { kitten = window.Dymok.roam({ zIndex: -1, hud: false, ignore: IGNORE, storageKey: "joblox.kitten" }); }
  catch (e) { return; }

  function covered(x, y) {
    if (x < 2 || y < 2 || x > innerWidth - 2 || y > innerHeight - 2) return true;
    var el = document.elementFromPoint(x, y);
    return !el || !!(el.closest && el.closest(COVER));
  }
  // is there room for the whole kitten standing at (x, y), in CSS pixels
  function freeAt(x, y) {
    var sc = kitten.scale, hw = BODY_W / 2 * sc, h = BODY_H * sc;
    return !covered(x, y - 3) && !covered(x - hw, y - 4) && !covered(x + hw, y - 4) &&
           !covered(x, y - h / 2) && !covered(x - hw * 0.7, y - h + 4) && !covered(x + hw * 0.7, y - h + 4);
  }
  function nearestFree(px, py) {
    var best = null, bd = 1e9, step = 44, top = 80;
    for (var y = top; y < innerHeight - 6; y += step) {
      for (var x = 40; x < innerWidth - 40; x += step) {
        var d = (x - px) * (x - px) + (y - py) * (y - py);
        if (d < bd && freeAt(x, y)) { bd = d; best = [x, y]; }
      }
    }
    return best;
  }
  function goTo(c, spot) { // close: he runs; far: he melts into smoke and appears there
    var tx = spot[0] / kitten.scale, ty = spot[1] / kitten.scale;
    var far = Math.hypot(spot[0] - c.x * kitten.scale, spot[1] - c.y * kitten.scale) > 260;
    if (far) c.startSmoke(tx, ty); else c.walkTo(tx, ty);
  }

  var target = null, targetAt = 0, cool = 0, checkAt = 0, scrollT = 0;
  function aim(x, y) { target = [x, y]; targetAt = performance.now(); }
  document.addEventListener("mousemove", function (e) { aim(e.clientX, e.clientY); }, { passive: true });
  document.addEventListener("pointerdown", function (e) { if (e.pointerType !== "mouse") { aim(e.clientX, e.clientY); cool = 0; } }, { passive: true });
  window.addEventListener("scroll", function () { scrollT = performance.now(); }, { passive: true });

  (function tick(now) {
    var c = kitten && kitten.cat;
    if (c && !kitten.dead && now >= checkAt) {
      checkAt = now + 500;
      var busy = c.mode === MODE_JUMP || c.mode === MODE_SMOKE || c.mode === MODE_ROLL || c.mode === MODE_FLIP;
      if (!busy && !c.down && !c.isAsleep() && now - scrollT > 300 && now >= cool) {
        var pos = [c.x * kitten.scale, c.y * kitten.scale];
        if (target && now - targetAt < 4000) {
          var spot = nearestFree(target[0], target[1] + 20);
          if (spot && Math.hypot(spot[0] - pos[0], spot[1] - pos[1]) > 30) {
            goTo(c, spot);
            if (Math.hypot(spot[0] - target[0], spot[1] - target[1]) < 70) c.setEmote(EMO_JOY, 1.0);
            cool = now + 1200;
          }
        } else if (!freeAt(pos[0], pos[1])) {
          var free = nearestFree(pos[0], pos[1]);
          if (free) { goTo(c, free); cool = now + 1500; }
        }
      }
    }
    requestAnimationFrame(tick);
  })(performance.now());

  // the page marks good news with data-kitten-cheer (an item went into the cart, an order was placed)
  if (document.querySelector("[data-kitten-cheer]")) {
    setTimeout(function () {
      try { kitten.feed(); kitten.cat.setEmote(EMO_JOY, 2.2); } catch (e) { /* busy */ }
    }, 900);
  }
})();
