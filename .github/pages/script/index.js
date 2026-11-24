// Shared by index.html (en) and zh-TW/index.html. A module: it runs after the
// page is parsed, so the elements are already there. theme.js is not here;
// see theme.js.
import { setUpMenu } from "./menu.js";
import { setUpCopyButtons } from "./copy.js";
import { setUpReveal } from "./reveal.js";
import { setUpTabs } from "./tabs.js";
import { setUpHighlight } from "./highlight.js";

setUpMenu();
setUpCopyButtons();
setUpTabs();
setUpHighlight();
setUpReveal();
