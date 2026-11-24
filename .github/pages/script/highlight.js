// Go code colors: wraps each part of the code in .code-block in a
// <span class="tok-…">. style/code-highlight.css gives each kind its color.
// It does not parse Go: it matches comments, strings, numbers and words, then
// sorts the words by name and by the character after them. The text stays
// the same, so the copy buttons still copy plain code.

var KEYWORDS = new Set([
  "break", "case", "chan", "const", "continue", "default", "defer", "else",
  "fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
  "map", "package", "range", "return", "select", "struct", "switch", "type",
  "var",
]);

var BUILTINS = new Set([
  "any", "bool", "byte", "error", "float32", "float64", "int", "int8",
  "int16", "int32", "int64", "rune", "string", "uint", "uint8", "uint16",
  "uint32", "uint64", "uintptr", "true", "false", "nil", "iota", "append",
  "cap", "clear", "close", "copy", "delete", "len", "make", "max", "min",
  "new", "panic", "print", "println", "recover",
]);

// One match is one token. The groups, in order: comment, string, number, word.
var TOKEN = /(\/\/[^\n]*|\/\*[\s\S]*?\*\/)|("(?:\\.|[^"\\\n])*"|`[^`]*`|'(?:\\.|[^'\\\n])*')|(\b\d[\d_]*(?:\.\d+)?\b)|(\b[A-Za-z_]\w*\b)/g;

function escape(text) {
  return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function span(kind, text) {
  return '<span class="tok-' + kind + '">' + escape(text) + "</span>";
}

// Returns the code as HTML, with each token in its span.
function highlightGo(code) {
  var html = "";
  var last = 0;
  var match;
  TOKEN.lastIndex = 0;
  while ((match = TOKEN.exec(code)) !== null) {
    html += escape(code.slice(last, match.index));
    var text = match[0];
    var next = code.charAt(match.index + text.length);
    if (match[1]) {
      html += span("comment", text);
    } else if (match[2]) {
      html += span("string", text);
    } else if (match[3]) {
      html += span("number", text);
    } else if (KEYWORDS.has(text)) {
      html += span("keyword", text);
    } else if (BUILTINS.has(text)) {
      html += span("builtin", text);
    } else if (next === "(") {
      html += span("func", text);
    } else if (next === ".") {
      html += span("package", text);
    } else {
      html += escape(text);
    }
    last = match.index + text.length;
  }
  return html + escape(code.slice(last));
}

export function setUpHighlight() {
  document.querySelectorAll(".code-block pre code").forEach(function (code) {
    code.innerHTML = highlightGo(code.textContent);
  });
}
