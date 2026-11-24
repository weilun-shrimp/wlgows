// Menu button, on narrow windows: opens and closes the links panel. A click
// on a link, the Escape key, or a window wide enough for the links closes it.
export function setUpMenu() {
  var header = document.querySelector(".site-header");
  var menuToggle = document.querySelector(".menu-toggle");
  if (!header || !menuToggle) {
    return;
  }

  function setMenuOpen(open) {
    header.classList.toggle("menu-open", open);
    menuToggle.setAttribute("aria-expanded", open ? "true" : "false");
  }

  menuToggle.addEventListener("click", function () {
    setMenuOpen(!header.classList.contains("menu-open"));
  });
  document.querySelectorAll(".nav a").forEach(function (link) {
    link.addEventListener("click", function () {
      setMenuOpen(false);
    });
  });
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      setMenuOpen(false);
    }
  });
  window.matchMedia("(min-width: 761px)").addEventListener("change", function (event) {
    if (event.matches) {
      setMenuOpen(false);
    }
  });
}
