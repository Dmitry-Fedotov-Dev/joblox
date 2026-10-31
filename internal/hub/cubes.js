// The tiles of the home page on a phone: there is no mouse to hover, so a touch straightens and lifts a tile the
// way hovering does on a computer, and tilting the phone leans the tiles after it.
(function () {
  "use strict";
  if (!window.matchMedia || !matchMedia("(hover: none)").matches) return;
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  var root = document.documentElement;

  document.addEventListener("touchstart", function (e) {
    var c = e.target.closest && e.target.closest(".cube");
    if (c) c.classList.add("press");
  }, { passive: true });
  var release = function () {
    var on = document.querySelectorAll(".cube.press");
    setTimeout(function () { Array.prototype.forEach.call(on, function (c) { c.classList.remove("press"); }); }, 220);
  };
  document.addEventListener("touchend", release, { passive: true });
  document.addEventListener("touchcancel", release, { passive: true });

  // the phone's tilt: left-right leans the tiles a little, the pictures slide further, like a parallax
  var raf = 0, gx = 0;
  window.addEventListener("deviceorientation", function (e) {
    if (e.gamma == null) return;
    gx = Math.max(-25, Math.min(25, e.gamma));
    if (raf) return;
    raf = requestAnimationFrame(function () {
      raf = 0;
      root.style.setProperty("--tilt", (gx / 25 * 2.2).toFixed(2) + "deg");
      root.style.setProperty("--slide", (gx / 25 * 10).toFixed(1) + "px");
    });
  });
})();
