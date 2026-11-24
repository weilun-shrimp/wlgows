// The theme: light or dark. A plain script, loaded in <head> without defer,
// so the saved theme applies before the page paints. index.js does not load
// it: a module would run too late for that.

// Applies a saved theme. No saved theme: follow the system.
try {
  var savedTheme = localStorage.getItem("theme");
  if (savedTheme === "light" || savedTheme === "dark") {
    document.documentElement.dataset.theme = savedTheme;
  }
} catch (e) {}

// Theme toggle: flips the current theme and saves it.
document.addEventListener("DOMContentLoaded", function () {
  var themeToggle = document.querySelector(".theme-toggle");
  if (!themeToggle) {
    return;
  }
  themeToggle.addEventListener("click", function () {
    var root = document.documentElement;
    var isDark = root.dataset.theme
      ? root.dataset.theme === "dark"
      : window.matchMedia("(prefers-color-scheme: dark)").matches;
    var nextTheme = isDark ? "light" : "dark";
    root.dataset.theme = nextTheme;
    try {
      localStorage.setItem("theme", nextTheme);
    } catch (e) {}
  });
});
