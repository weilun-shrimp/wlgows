// Sections fade in and slide up as they scroll into view, then their items
// (cards, features, layers, stats, charts, tables, code, examples, guides)
// one after another. Each section shows once, then stays. The hidden start is
// set here, not in the HTML, so the page still shows every section when this
// script does not run.
export function setUpReveal() {
  var sections = document.querySelectorAll(".section");
  var lessMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (lessMotion || !("IntersectionObserver" in window)) {
    return;
  }

  var observer = new IntersectionObserver(function (entries) {
    entries.forEach(function (entry) {
      if (entry.isIntersecting) {
        entry.target.classList.add("revealed");
        observer.unobserve(entry.target);
      }
    });
  }, { rootMargin: "0px 0px -10% 0px" }); // shows once its top passes 90% of the window height

  sections.forEach(function (section) {
    section.classList.add("reveal");
    section.querySelectorAll(".card, .feature, .layer, .stat, .bar-chart, .table-card, .code-tabs, .example, .guide").forEach(function (item, index) {
      item.classList.add("reveal-item");
      item.style.setProperty("--reveal-index", index);
    });
    observer.observe(section);
  });
}
