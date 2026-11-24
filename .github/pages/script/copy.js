// Copy buttons: copy the text of the element named in data-copy, then show
// data-copied-label for a moment.
export function setUpCopyButtons() {
  document.querySelectorAll(".copy-button").forEach(function (button) {
    var label = button.textContent;
    button.addEventListener("click", function () {
      var text = document.getElementById(button.dataset.copy).textContent;
      navigator.clipboard.writeText(text).then(function () {
        button.textContent = button.dataset.copiedLabel;
        setTimeout(function () {
          button.textContent = label;
        }, 1500);
      });
    });
  });
}
