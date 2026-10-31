// The back office: a form with data-confirm asks first; a button with data-copy copies its text.
(function () {
  "use strict";
  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute && e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) e.preventDefault();
  }, true);
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-copy]");
    if (!b || !navigator.clipboard) return;
    navigator.clipboard.writeText(b.getAttribute("data-copy")).then(function () {
      var t = b.textContent;
      b.textContent = "Скопировано";
      setTimeout(function () { b.textContent = t; }, 1500);
    });
  });
  // a chosen photo shows up in its drop zone: it is uploaded with the form
  document.addEventListener("change", function (e) {
    var input = e.target;
    if (!input.matches || !input.matches("[data-photo-input]")) return;
    var note = input.closest("[data-dropzone]") && input.closest("[data-dropzone]").querySelector("[data-picked]");
    if (note) note.textContent = input.files.length ? "Выбрано фото: " + input.files.length + ". Нажмите «Сохранить»." : "";
  });
})();
