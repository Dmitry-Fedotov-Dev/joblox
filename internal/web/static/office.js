// The back office: a form with data-confirm asks first (copying is in copy.js).
(function () {
  "use strict";
  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute && e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) e.preventDefault();
  }, true);
  // chosen photos show up as previews above their drop zone before the form is sent: each can be taken
  // out again, a second pick adds to the first, and the first one of a product without photos is its cover
  var picked = new WeakMap(); // input → the files chosen so far
  function render(input) {
    var zone = input.closest("[data-dropzone]");
    if (!zone) return;
    var files = picked.get(input) || [];
    var grid = zone.previousElementSibling;
    if (!grid || !grid.hasAttribute("data-previews")) {
      grid = document.createElement("div");
      grid.className = "photos";
      grid.setAttribute("data-previews", "");
      zone.parentNode.insertBefore(grid, zone);
    }
    grid.querySelectorAll("img").forEach(function (im) { URL.revokeObjectURL(im.src); });
    grid.textContent = "";
    var cover = input.hasAttribute("data-cover");
    files.forEach(function (f, i) {
      var cell = document.createElement("div");
      cell.className = "photo";
      var im = document.createElement("img");
      im.alt = f.name;
      im.src = URL.createObjectURL(f);
      cell.appendChild(im);
      if (cover && i === 0) {
        var pill = document.createElement("span");
        pill.className = "pill rose cover";
        pill.textContent = "Обложка";
        cell.appendChild(pill);
      }
      var tools = document.createElement("div");
      tools.className = "photo-tools";
      var del = document.createElement("button");
      del.type = "button";
      del.className = "del";
      del.title = "Убрать";
      del.textContent = "🗑";
      del.addEventListener("click", function () {
        files.splice(files.indexOf(f), 1);
        sync(input, files);
      });
      tools.appendChild(del);
      cell.appendChild(tools);
      grid.appendChild(cell);
    });
    grid.hidden = !files.length;
    var note = zone.querySelector("[data-picked]");
    if (note) {
      if (!note.hasAttribute("data-idle")) note.setAttribute("data-idle", note.textContent);
      note.textContent = files.length
        ? "Выбрано: " + files.length + ". Загрузятся, когда нажмёте «" + (input.getAttribute("data-save") || "Сохранить") + "»."
        : note.getAttribute("data-idle");
    }
  }
  function sync(input, files) {
    picked.set(input, files);
    if (window.DataTransfer) {
      var dt = new DataTransfer();
      files.forEach(function (f) { dt.items.add(f); });
      input.files = dt.files;
    }
    render(input);
  }
  document.addEventListener("change", function (e) {
    var input = e.target;
    if (!input.matches || !input.matches("[data-photo-input]")) return;
    var now = Array.prototype.slice.call(input.files);
    if (!window.DataTransfer) { picked.set(input, now); render(input); return; } // old browsers: the last pick only
    var files = (picked.get(input) || []).concat(now.filter(function (f) { return f.type.indexOf("image/") === 0 || !f.type; }));
    sync(input, files);
  });
})();
