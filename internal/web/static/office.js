// The back office: a form with data-confirm asks first (copying is in copy.js).
(function () {
  "use strict";
  document.addEventListener("submit", function (e) {
    var msg = e.target.getAttribute && e.target.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) e.preventDefault();
  }, true);
  // a chosen photo shows up in its drop zone: it is uploaded with the form
  document.addEventListener("change", function (e) {
    var input = e.target;
    if (!input.matches || !input.matches("[data-photo-input]")) return;
    var note = input.closest("[data-dropzone]") && input.closest("[data-dropzone]").querySelector("[data-picked]");
    if (note) note.textContent = input.files.length ? "Выбрано фото: " + input.files.length + ". Нажмите «Сохранить»." : "";
  });
})();
