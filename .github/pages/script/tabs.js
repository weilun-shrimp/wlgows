// Tabs: in each [data-tabs] box, a click on a [role="tab"] button shows its
// panel (named in aria-controls) and hides the others. Arrow keys move
// between the tabs.
export function setUpTabs() {
  document.querySelectorAll("[data-tabs]").forEach(function (box) {
    var tabs = Array.prototype.slice.call(box.querySelectorAll('[role="tab"]'));

    function select(tab) {
      tabs.forEach(function (other) {
        var selected = other === tab;
        other.setAttribute("aria-selected", selected ? "true" : "false");
        other.tabIndex = selected ? 0 : -1;
        document.getElementById(other.getAttribute("aria-controls")).hidden = !selected;
      });
    }

    tabs.forEach(function (tab, index) {
      tab.addEventListener("click", function () {
        select(tab);
      });
      tab.addEventListener("keydown", function (event) {
        var step = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
        if (step) {
          var next = tabs[(index + step + tabs.length) % tabs.length];
          select(next);
          next.focus();
        }
      });
    });

    box.classList.add("tabs-ready");

    var first = tabs.filter(function (tab) {
      return tab.getAttribute("aria-selected") === "true";
    })[0] || tabs[0];
    select(first);
  });
}
