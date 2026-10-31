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

  // knitting shops frame their cards with yarn: now and then he walks up to a ball and knocks it off; the thread
  // snaps and winds back to the needles, then wraps the card again and the ball comes back
    var Y = function () { return window.JobloxYarn; };  // yarn.js: the thread with weight, when it is loaded
  function ballAt(el) {
    var b = (Y() && Y().ball(el)) || el.querySelector(".ball"), r = b.getBoundingClientRect();
    return [r.left + r.width / 2, r.top + r.height / 2, r];
  }
  function snap(el) {
    if (Y() && Y().knock(el)) return;
    el.classList.add("snapped");
    setTimeout(function () {
      el.classList.add("noball"); el.classList.remove("snapped");
      setTimeout(function () { el.classList.remove("noball"); }, 2700);
    }, 5000);
  }
  function wiggle(el) {
    if (Y()) { Y().nudge(el); return; }
    el.classList.remove("wiggle"); void el.offsetWidth; el.classList.add("wiggle");
    setTimeout(function () { el.classList.remove("wiggle"); }, 1000);
  }
  var play = null;                                  // the round of play with the ball on screen
  // to play he comes out in front of the page (his canvas never takes a press), then hides behind it again
  function front(on) { if (kitten.canvas) kitten.canvas.style.zIndex = on ? "5" : "-1"; }
  function yarnStep(now, c, pos) {
    var all = document.querySelectorAll(".yarn:not(.bare):not(.snapped):not(.noball)");
    if (Y()) Y().touch(pos[0], pos[1] - 20);        // walking along a thread, he brushes it
    var el = null, b = null;
    for (var i = 0; i < all.length && !el; i++) {
      var bi = ballAt(all[i]), r = bi[2];
      if (r.width && r.bottom > 50 && r.top < innerHeight - 10 && r.right > 0 && r.left < innerWidth) { el = all[i]; b = bi; }
    }
    if (!el) { if (play) front(false); play = null; return false; }
    if (!play || play.el !== el) play = { el: el, bats: 0, next: now, rest: now + 1200, spot: null, went: 0 };
    if (now < play.rest) { front(false); return false; } // between rounds he wanders and hides again
    front(true);
    var side = pos[0] < b[0] ? -1 : 1, sc = kitten.scale;
    var spot = [b[0] + side * 15 * sc, b[1] + 4 * sc];  // right beside the ball, paws on its level
    var d = Math.hypot(spot[0] - pos[0], spot[1] - pos[1]);
    if (d > 6 * sc) {                                // on the way to the ball
      if (!play.spot || now - play.went > 2500 || Math.hypot(spot[0] - play.spot[0], spot[1] - play.spot[1]) > 20) {
        goTo(c, spot); play.spot = spot; play.went = now;
      }
      if (now - play.went < 6000) return true;
    }
    if (now < play.next) return true;                // at the ball: bat it, pounce, sometimes knock it off
    c.facing = b[0] < pos[0] ? -1 : 1;
    play.bats++;
    if (play.bats >= 4 && Math.random() < 0.35) {
      snap(el); c.setEmote(EMO_JOY, 1.6);
      play = { el: el, bats: 0, next: now, rest: now + 12000, spot: null, went: 0 }; // the thread wraps again meanwhile
      setTimeout(function () { front(false); }, 1500);
    } else {
      wiggle(el);
      if (Math.random() < 0.45) c.setEmote(EMO_JOY, 0.8);
      play.next = now + 650 + Math.random() * 900;
      if (play.bats >= 9) { play.bats = 0; play.spot = null; play.rest = now + 5000 + Math.random() * 6000; }
    }
    return true;
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
        var chasing = target && now - targetAt < 4000;
        if (!chasing && now - targetAt > 2500 && yarnStep(now, c, pos)) { /* busy with the yarn */ }
        else if (chasing) {
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
