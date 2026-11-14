// An action that takes something away asks twice in place: the first click
// turns its button into the sentence that states the effect, the second
// does it, and a click anywhere else leaves things as they are.
(function () {
  "use strict";
  var armed = null;

  function disarm() {
    if (!armed) return;
    armed.button.textContent = armed.label;
    armed.button.classList.remove("armed");
    armed.sure.remove();
    armed = null;
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest ? event.target.closest("button[data-sentence]") : null;
    if (armed && armed.button === button) return;
    disarm();
    if (!button || !button.form) return;
    event.preventDefault();
    var sure = document.createElement("input");
    sure.type = "hidden";
    sure.name = "sure";
    sure.value = "yes";
    button.form.appendChild(sure);
    armed = { button: button, label: button.textContent, sure: sure };
    button.textContent = button.dataset.sentence;
    button.classList.add("armed");
  });

  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") disarm();
  });
})();
