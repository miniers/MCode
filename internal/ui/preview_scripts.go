package mcodeui

import "github.com/egoist/mygo"

const (
	previewConsoleMaxItems      = 200
	previewConsoleMaxEntryBytes = 8192
)

// 只在创建时配置；设置变化由 ensureWebView 换代重建，不增加绑定或可信源。
func previewWebViewOptions(untrusted, capture bool) mygo.WebViewOptions {
	script := previewLocationInstallJS + ";" + previewAnnotateInstallJS + ";"
	if capture {
		script += previewConsoleInstallJS
	}
	return mygo.WebViewOptions{Page: mygo.PageOptions{Untrusted: untrusted, PreloadScript: script}}
}

// 文档标识区分刷新后重置的序号；SPA 序号与控制台序号互不影响。
const previewLocationInstallJS = `(function () {
  if (window.__mcodePreview) return;
  var state = {document: Date.now().toString(36) + Math.random().toString(36), sequence: 0,
    locationSequence: 1, location: String(location.href), entries: []};
  window.__mcodePreview = state;
  var update = function () {
    state.location = String(location.href); state.locationSequence++;
    var picker = window.__mcodeAnnot;
    if (picker) {
      picker.active = false; picker.queue = [];
      if (picker.clearHover) picker.clearHover();
      if (document.documentElement) document.documentElement.style.cursor = "";
    }
  };
  ["pushState", "replaceState"].forEach(function (name) {
    var original = history[name];
    history[name] = function () { var result = original.apply(this, arguments); update(); return result; };
  });
  window.addEventListener("popstate", update);
  window.addEventListener("hashchange", update);
  state.drain = function (after, documentID) {
    return JSON.stringify({document: state.document, sequence: state.sequence,
      location: state.location, locationSequence: state.locationSequence,
      entries: state.entries.filter(function (entry) { return documentID !== state.document || entry.sequence > after; })});
  };
  state.clear = function () {
    state.entries = [];
    return state.drain(state.sequence, state.document);
  };
})()`

// 不调用 getter/toJSON；限制深度、属性数、参数数和 UTF-8 字节，避免遍历整棵 DOM 或循环对象。
const previewConsoleInstallJS = `(function () {
  var state = window.__mcodePreview;
  if (!state || state.consoleInstalled) return;
  state.consoleInstalled = true;
  var MAX_ITEMS = 200, MAX_ENTRY_BYTES = 8192, MAX_TEXT_BYTES = 4096;
  var clip = function (value, limit) {
    var text = String(value == null ? "" : value).slice(0, limit), out = "", bytes = 0;
    for (var ch of text) {
      var n = ch.codePointAt(0), size = n <= 127 ? 1 : n <= 2047 ? 2 : n <= 65535 ? 3 : 4;
      if (bytes + size > limit) break;
      out += ch; bytes += size;
    }
    return out;
  };
  var safe = function (value, seen, depth) {
    try {
      if (typeof value === "string") return clip(value, MAX_TEXT_BYTES);
      if (value === null || typeof value !== "object") return clip(String(value), MAX_TEXT_BYTES);
      if (seen.indexOf(value) >= 0) return "[Circular]";
      if (depth >= 3) return "[Object]";
      seen.push(value);
      var parts = [], count = 0;
      var message = Object.getOwnPropertyDescriptor(value, "message");
      if (message && typeof message.value === "string") parts.push(clip(message.value, MAX_TEXT_BYTES));
      for (var key in value) {
        if (++count > 32) { parts.push("…"); break; }
        var descriptor;
        try { descriptor = Object.getOwnPropertyDescriptor(value, key); } catch (e) { continue; }
        if (!descriptor) continue;
        var part = descriptor.get ? "[Getter]" : safe(descriptor.value, seen, depth + 1);
        parts.push(clip(key, 128) + ": " + part);
        if (parts.join(", ").length >= MAX_TEXT_BYTES) break;
      }
      seen.pop();
      return clip("{" + parts.join(", ") + "}", MAX_TEXT_BYTES);
    } catch (e) { return "[Unserializable]"; }
  };
  var push = function (kind, values, source, line, column) {
    try {
      var parts = [];
      for (var i = 0; i < Math.min(values.length, 32); i++) {
        parts.push(safe(values[i], [], 0));
        if (parts.join(" ").length >= MAX_TEXT_BYTES) break;
      }
      var entry = {sequence: ++state.sequence, time: new Date().toISOString(), kind: kind,
        message: clip(parts.join(" "), MAX_TEXT_BYTES), source: clip(source || "", 512), line: line || 0, column: column || 0};
      // JSON 转义也计入上限，控制字符的转义可使文本膨胀六倍。
      while (JSON.stringify(entry).length > MAX_ENTRY_BYTES / 3) {
        if (entry.message.length) entry.message = entry.message.slice(0, Math.floor(entry.message.length / 2));
        else { entry.source = ""; break; }
      }
      state.entries.push(entry);
      if (state.entries.length > MAX_ITEMS) state.entries.shift();
    } catch (e) {}
  };
  ["log", "info", "debug", "warn", "error"].forEach(function (level) {
    var original = console[level];
    console[level] = function () {
      var source = "", line = 0;
      try {
        var stack = String(new Error().stack || "").split("\n");
        var frame = stack[2] || stack[1] || "", match = frame.match(/(https?:\/\/.*?):(\d+):\d+\)?$/);
        if (match) { source = match[1]; line = Number(match[2]); }
      } catch (e) {}
      push(level, arguments, source, line, 0);
      return original.apply(this, arguments);
    };
  });
  window.addEventListener("error", function (event) {
    push("error", [event.message], event.filename, event.lineno, event.colno);
  });
  window.addEventListener("unhandledrejection", function (event) {
    push("error", [event.reason], "", 0, 0);
  });
})()`

const previewConsoleClearScript = `(function (documentID) {
  var state = window.__mcodePreview;
  if (state && documentID && documentID !== state.document) return state.drain(0, "");
  return state ? state.clear() : "null";
})`

// 交互只操作预装入口，不在轮询失败时重新安装监听器。
const previewAnnotateStartScript = `(function () {
  var state = window.__mcodeAnnot;
  if (!state) return "idle";
  state.active = true;
  if (document.documentElement) document.documentElement.style.cursor = "crosshair";
  return "annotating";
})()`

const previewAnnotateStopScript = `(function () {
  var state = window.__mcodeAnnot;
  if (!state) return "idle";
  state.active = false;
  try { if (state.clearHover) state.clearHover(); } catch (e) {}
  if (document.documentElement) document.documentElement.style.cursor = "";
  return "idle";
})()`

const previewAnnotateDrainScript = `(function () {
  var state = window.__mcodeAnnot;
  if (!state) return "null";
  var items = state.queue || [];
  state.queue = [];
  try { return JSON.stringify(items); } catch (e) { return "[]"; }
})()`

// 预装拾取器只注册监听器，模式开关由短 start/stop 调用控制。
const previewAnnotateInstallJS = `(function () {
  if (window.__mcodeAnnot) { return window.__mcodeAnnot; }
  var IMPLICIT_ROLE = {
    a: "link", button: "button", select: "combobox", textarea: "textbox", img: "img",
    nav: "navigation", main: "main", header: "banner", footer: "contentinfo",
    aside: "complementary", form: "form", table: "table", ul: "list", ol: "list",
    li: "listitem", h1: "heading", h2: "heading", h3: "heading", h4: "heading",
    h5: "heading", h6: "heading", details: "group", dialog: "dialog", fieldset: "group",
    progress: "progressbar", summary: "button", option: "option", p: "paragraph"
  };
  var state = { active: false, queue: [], hover: null, prevOutline: "", prevOffset: "", clearHover: null };
  var clip = function (raw, max) {
    var text = String(raw == null ? "" : raw).replace(/\s+/g, " ").trim();
    var chars = Array.from(text);
    return chars.length > max ? chars.slice(0, max).join("") : text;
  };
  var cssEscape = function (value) {
    if (window.CSS && typeof window.CSS.escape === "function") { return window.CSS.escape(value); }
    return String(value).replace(/[^a-zA-Z0-9_-]/g, function (ch) { return "\\" + ch; });
  };
  var stableClass = function (el) {
    var list = String((el.getAttribute && el.getAttribute("class")) || "").split(/\s+/);
    for (var i = 0; i < list.length; i++) {
      var name = list[i];
      if (!name || name.length > 32) { continue; }
      if (/^(is-|has-|js-|active|selected|hover|focus|open|closed)/.test(name)) { continue; }
      return name;
    }
    return "";
  };
  var selectorFor = function (el) {
    if (!el || el.nodeType !== 1) { return ""; }
    if (el.id) { return "#" + cssEscape(el.id); }
    var parts = [];
    var node = el;
    while (node && node.nodeType === 1 && node !== document.documentElement && parts.length < 5) {
      if (node.id) { parts.unshift("#" + cssEscape(node.id)); break; }
      var part = node.tagName.toLowerCase();
      var cls = stableClass(node);
      if (cls) { part += "." + cssEscape(cls); }
      var parent = node.parentElement;
      if (parent) {
        var sameTag = 0;
        var index = 0;
        for (var i = 0; i < parent.children.length; i++) {
          var sib = parent.children[i];
          if (sib.tagName !== node.tagName) { continue; }
          sameTag++;
          if (sib === node) { index = sameTag; }
        }
        if (sameTag > 1) { part += ":nth-of-type(" + index + ")"; }
      }
      parts.unshift(part);
      try {
        if (document.querySelectorAll(parts.join(" > ")).length === 1) { break; }
      } catch (e) {}
      node = parent;
    }
    return parts.join(" > ");
  };
  var roleFor = function (el) {
    var explicit = el.getAttribute && el.getAttribute("role");
    if (explicit) { return String(explicit).split(/\s+/)[0]; }
    var tag = el.tagName.toLowerCase();
    if (tag === "input") {
      var type = String(el.type || "text").toLowerCase();
      if (type === "checkbox") { return "checkbox"; }
      if (type === "radio") { return "radio"; }
      if (type === "button" || type === "submit" || type === "reset") { return "button"; }
      if (type === "search") { return "searchbox"; }
      if (type === "range") { return "slider"; }
      return "textbox";
    }
    return IMPLICIT_ROLE[tag] || "";
  };
  var nameFor = function (el) {
    var name = el.getAttribute("aria-label") || el.getAttribute("alt") || el.getAttribute("title") || "";
    var tag = el.tagName.toLowerCase();
    if (!name && (tag === "input" || tag === "textarea" || tag === "select")) {
      name = el.getAttribute("placeholder") || el.value || "";
    }
    if (!name) { name = el.innerText || el.textContent || ""; }
    return clip(name, 80);
  };
  var clearHover = function () {
    if (state.hover) {
      state.hover.style.outline = state.prevOutline;
      state.hover.style.outlineOffset = state.prevOffset;
    }
    state.hover = null;
  };
  state.clearHover = clearHover;
  document.addEventListener("mousemove", function (event) {
    if (!state.active) { return; }
    var el = event.target;
    if (!el || el.nodeType !== 1 || el === state.hover) { return; }
    clearHover();
    state.hover = el;
    state.prevOutline = el.style.outline;
    state.prevOffset = el.style.outlineOffset;
    el.style.outline = "2px solid #3b82f6";
    el.style.outlineOffset = "1px";
  }, true);
  document.addEventListener("click", function (event) {
    if (!state.active) { return; }
    var el = event.target;
    if (!el || el.nodeType !== 1) { return; }
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) { event.stopImmediatePropagation(); }
    var rect = el.getBoundingClientRect();
    state.queue.push({
      selector: selectorFor(el),
      tag: el.tagName ? el.tagName.toLowerCase() : "",
      role: roleFor(el),
      name: nameFor(el),
      text: clip(el.innerText || el.textContent || "", 200),
      url: String(location.href || ""),
      rect: {
        x: Math.round(rect.left),
        y: Math.round(rect.top),
        w: Math.round(rect.width),
        h: Math.round(rect.height)
      }
    });
    if (state.queue.length > 200) { state.queue.shift(); }
    clearHover();
  }, true);
  window.__mcodeAnnot = state;
  return state;
})()`
