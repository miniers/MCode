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
  try { if (state.cancelDrag) state.cancelDrag(); } catch (e) {}
  if (document.documentElement) document.documentElement.style.cursor = "";
  return "idle";
})()`

const previewAnnotateDrainScript = `(function () {
  var state = window.__mcodeAnnot;
  if (!state) return "null";
  var items = state.queue || [];
  state.queue = [];
  try { return JSON.stringify({items: items, active: state.active, hover: state.hoverPID || 0}); } catch (e) { return "null"; }
})()`

const previewAnnotateClearMarksScript = `window.__mcodeAnnot && window.__mcodeAnnot.clearMarks();`
const previewAnnotateUnhighlightScript = `window.__mcodeAnnot && window.__mcodeAnnot.unhighlightMark();`
const previewAnnotateHideOverlayScript = `window.__mcodeAnnot && window.__mcodeAnnot.setOverlayHidden(true);`
const previewAnnotateShowOverlayScript = `window.__mcodeAnnot && window.__mcodeAnnot.setOverlayHidden(false);`

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
  var HANDLE_STYLES = {
    n:  "top:-4px;left:8px;right:8px;height:8px;cursor:ns-resize;",
    s:  "bottom:-4px;left:8px;right:8px;height:8px;cursor:ns-resize;",
    w:  "left:-4px;top:8px;bottom:8px;width:8px;cursor:ew-resize;",
    e:  "right:-4px;top:8px;bottom:8px;width:8px;cursor:ew-resize;",
    nw: "left:-6px;top:-6px;width:12px;height:12px;cursor:nwse-resize;",
    ne: "right:-6px;top:-6px;width:12px;height:12px;cursor:nesw-resize;",
    sw: "left:-6px;bottom:-6px;width:12px;height:12px;cursor:nesw-resize;",
    se: "right:-6px;bottom:-6px;width:12px;height:12px;cursor:nwse-resize;"
  };
  var state = { active: false, queue: [], hover: null, hoverPID: 0, prevOutline: "", prevOffset: "", clearHover: null, cancelDrag: null, nextPID: 0 };
  var host = document.createElement("div");
  host.style.cssText = "all:initial!important;position:fixed!important;inset:0!important;pointer-events:none!important;z-index:2147483647!important;";
  var shadow = host.attachShadow({mode: "closed"});
  var marquee = document.createElement("div");
  marquee.style.cssText = "position:fixed;display:none;box-sizing:border-box;border:1.5px dashed #2563eb;background:rgba(37,99,235,0.08);pointer-events:none;border-radius:2px;";
  shadow.appendChild(marquee);
  var marks = new Map();
  var highlighted = 0;
  var pendingFrame = 0;
  var overlayHidden = false;
  var manipulating = false;
  var suppressClickUntil = 0;
  var pointer = { active: false, id: 0, startX: 0, startY: 0, dragging: false, target: null };
  var ensureHost = function () {
    if (!host.isConnected && document.documentElement) document.documentElement.appendChild(host);
  };
  var viewportSize = function () {
    return {
      w: window.innerWidth || (document.documentElement && document.documentElement.clientWidth) || 0,
      h: window.innerHeight || (document.documentElement && document.documentElement.clientHeight) || 0
    };
  };
  var positionMark = function (mark) {
    if (mark.kind === "region") {
      var sx = window.scrollX || window.pageXOffset || 0;
      var sy = window.scrollY || window.pageYOffset || 0;
      var vx = Math.round(mark.docRect.x - sx);
      var vy = Math.round(mark.docRect.y - sy);
      var vw = Math.round(mark.docRect.w);
      var vh = Math.round(mark.docRect.h);
      mark.rect = { x: vx, y: vy, w: vw, h: vh };
      mark.box.style.display = "block";
      mark.box.style.left = vx + "px";
      mark.box.style.top = vy + "px";
      mark.box.style.width = vw + "px";
      mark.box.style.height = vh + "px";
      return;
    }
    var rect = mark.element.getBoundingClientRect();
    mark.rect = { x: rect.left, y: rect.top, w: rect.width, h: rect.height };
    mark.box.style.display = mark.element.isConnected ? "block" : "none";
    mark.box.style.left = rect.left + "px";
    mark.box.style.top = rect.top + "px";
    mark.box.style.width = rect.width + "px";
    mark.box.style.height = rect.height + "px";
  };
  var schedulePositions = function () {
    if (pendingFrame || !marks.size) return;
    pendingFrame = requestAnimationFrame(function () {
      pendingFrame = 0;
      marks.forEach(positionMark);
    });
  };
  window.addEventListener("scroll", schedulePositions, true);
  window.addEventListener("resize", schedulePositions);
  state.removeMark = function (pid) {
    var mark = marks.get(pid);
    if (mark) mark.box.remove();
    marks.delete(pid);
    state.queue = state.queue.filter(function (item) { return item.pid !== pid; });
    if (highlighted === pid) highlighted = 0;
    if (state.hoverPID === pid) state.hoverPID = 0;
  };
  state.clearMarks = function () {
    marks.forEach(function (mark) { mark.box.remove(); });
    marks.clear();
    state.queue = [];
    highlighted = 0;
    state.hoverPID = 0;
    marquee.style.display = "none";
    if (pendingFrame) cancelAnimationFrame(pendingFrame);
    pendingFrame = 0;
  };
  state.unhighlightMark = function () {
    var mark = marks.get(highlighted);
    if (mark) { mark.box.style.color = "#2563eb"; mark.box.style.borderWidth = "2px"; }
    highlighted = 0;
  };
  state.highlightMark = function (pid) {
    state.unhighlightMark();
    var mark = marks.get(pid);
    if (mark) { mark.box.style.color = "#ea580c"; mark.box.style.borderWidth = "3px"; highlighted = pid; }
  };
  state.setLabels = function (pids) {
    if (!Array.isArray(pids)) return;
    var keep = new Set(pids);
    var queued = [];
    var queuedSet = new Set();
    var q = state.queue || [];
    for (var j = 0; j < q.length; j++) {
      var qpid = q[j] && q[j].pid;
      if (qpid && !keep.has(qpid) && !queuedSet.has(qpid)) {
        queuedSet.add(qpid);
        queued.push(qpid);
      }
    }
    Array.from(marks.keys()).forEach(function (pid) {
      if (!keep.has(pid) && !queuedSet.has(pid)) state.removeMark(pid);
    });
    for (var i = 0; i < pids.length; i++) {
      var mark = marks.get(pids[i]);
      if (mark && mark.number) mark.number.textContent = String(i + 1);
    }
    for (var k = 0; k < queued.length; k++) {
      var qmark = marks.get(queued[k]);
      if (qmark && qmark.number) qmark.number.textContent = String(pids.length + k + 1);
    }
  };
  state.setOverlayHidden = function (hidden) {
    overlayHidden = Boolean(hidden);
    if (overlayHidden) {
      host.style.setProperty("display", "none", "important");
      state.hoverPID = 0;
      if (state.clearHover) state.clearHover();
      marquee.style.display = "none";
    } else {
      host.style.removeProperty("display");
    }
  };
  state.rectsFor = function (pids) {
    var vp = viewportSize();
    if (!Array.isArray(pids)) return { vw: vp.w, rects: {} };
    var sx = window.scrollX || window.pageXOffset || 0;
    var sy = window.scrollY || window.pageYOffset || 0;
    var rects = {};
    for (var i = 0; i < pids.length; i++) {
      var pid = pids[i];
      var mark = marks.get(pid);
      if (!mark) continue;
      if (mark.kind === "region") {
        rects[String(pid)] = {
          x: Math.round(mark.docRect.x - sx),
          y: Math.round(mark.docRect.y - sy),
          w: Math.round(mark.docRect.w),
          h: Math.round(mark.docRect.h)
        };
      } else if (mark.element && mark.element.isConnected) {
        var r = mark.element.getBoundingClientRect();
        rects[String(pid)] = {
          x: r.left,
          y: r.top,
          w: r.width,
          h: r.height
        };
      }
    }
    return { vw: vp.w, rects: rects };
  };
  var createMarkBox = function (isRegion) {
    ensureHost();
    var box = document.createElement("div");
    box.style.cssText = "position:absolute;box-sizing:border-box;border:2px " + (isRegion ? "dashed" : "solid") + " currentColor;color:#2563eb;pointer-events:none;";
    var badge = document.createElement("span");
    badge.style.cssText = "position:absolute;left:-2px;top:-2px;min-width:20px;height:20px;box-sizing:border-box;padding:0 4px;background:currentColor;border-radius:3px;font:600 12px/20px system-ui;text-align:center;" + (isRegion ? "pointer-events:auto;cursor:grab;user-select:none;touch-action:none;z-index:2;" : "");
    var number = document.createElement("span");
    number.textContent = String(marks.size + 1);
    number.style.color = "white";
    badge.appendChild(number);
    box.appendChild(badge);
    shadow.appendChild(box);
    return { box: box, badge: badge, number: number };
  };
  var pushRegionUpdate = function (mark) {
    if (!marks.has(mark.pid)) return;
    positionMark(mark);
    var vp = viewportSize();
    state.queue.push({
      pid: mark.pid,
      kind: "region",
      update: true,
      url: String(location.href || ""),
      vw: vp.w,
      rect: {
        x: Math.round(mark.rect.x),
        y: Math.round(mark.rect.y),
        w: Math.round(mark.rect.w),
        h: Math.round(mark.rect.h)
      }
    });
  };
  var bindWindowDrag = function (onMove, onDone) {
    var cleanup = function () {
      window.removeEventListener("pointermove", onMove, true);
      window.removeEventListener("pointerup", onUp, true);
      window.removeEventListener("pointercancel", onUp, true);
      manipulating = false;
      suppressClickUntil = Date.now() + 400;
    };
    var onUp = function () {
      cleanup();
      onDone();
    };
    window.addEventListener("pointermove", onMove, true);
    window.addEventListener("pointerup", onUp, true);
    window.addEventListener("pointercancel", onUp, true);
  };
  var attachRegionHandles = function (mark) {
    var MIN_SIZE = 8;
    mark.badge.addEventListener("pointerdown", function (e) {
      if (e.button !== 0 || overlayHidden) return;
      e.preventDefault();
      e.stopPropagation();
      if (e.stopImmediatePropagation) e.stopImmediatePropagation();
      clearHover();
      manipulating = true;
      var startX = e.clientX, startY = e.clientY;
      var orig = { x: mark.docRect.x, y: mark.docRect.y, w: mark.docRect.w, h: mark.docRect.h };
      bindWindowDrag(function (ev) {
        mark.docRect.x = Math.max(0, orig.x + Math.round(ev.clientX - startX));
        mark.docRect.y = Math.max(0, orig.y + Math.round(ev.clientY - startY));
        positionMark(mark);
      }, function () {
        if (mark.docRect.x !== orig.x || mark.docRect.y !== orig.y) pushRegionUpdate(mark);
      });
    });
    Object.keys(HANDLE_STYLES).forEach(function (dir) {
      var handle = document.createElement("span");
      handle.style.cssText = "position:absolute;box-sizing:border-box;pointer-events:auto;touch-action:none;z-index:1;display:flex;align-items:center;justify-content:center;" + HANDLE_STYLES[dir];
      if (dir.length === 2) {
        var knob = document.createElement("span");
        knob.style.cssText = "width:7px;height:7px;background:#fff;border:1.5px solid currentColor;border-radius:1.5px;box-sizing:border-box;pointer-events:none;";
        handle.appendChild(knob);
      }
      handle.addEventListener("pointerdown", function (e) {
        if (e.button !== 0 || overlayHidden) return;
        e.preventDefault();
        e.stopPropagation();
        if (e.stopImmediatePropagation) e.stopImmediatePropagation();
        clearHover();
        manipulating = true;
        var startX = e.clientX, startY = e.clientY;
        var orig = { x: mark.docRect.x, y: mark.docRect.y, w: mark.docRect.w, h: mark.docRect.h };
        bindWindowDrag(function (ev) {
          var dx = Math.round(ev.clientX - startX);
          var dy = Math.round(ev.clientY - startY);
          var left = orig.x, top = orig.y, right = orig.x + orig.w, bottom = orig.y + orig.h;
          if (dir.indexOf("w") >= 0) left = Math.max(0, Math.min(right - MIN_SIZE, orig.x + dx));
          if (dir.indexOf("e") >= 0) right = Math.max(left + MIN_SIZE, orig.x + orig.w + dx);
          if (dir.indexOf("n") >= 0) top = Math.max(0, Math.min(bottom - MIN_SIZE, orig.y + dy));
          if (dir.indexOf("s") >= 0) bottom = Math.max(top + MIN_SIZE, orig.y + orig.h + dy);
          mark.docRect = { x: left, y: top, w: right - left, h: bottom - top };
          positionMark(mark);
        }, function () {
          var r = mark.docRect;
          if (r.x !== orig.x || r.y !== orig.y || r.w !== orig.w || r.h !== orig.h) pushRegionUpdate(mark);
        });
      });
      mark.box.appendChild(handle);
    });
  };
  var addMark = function (pid, element) {
    var ui = createMarkBox(false);
    var mark = {pid: pid, kind: "element", element: element, box: ui.box, badge: ui.badge, number: ui.number, rect: null};
    marks.set(pid, mark);
    positionMark(mark);
  };
  var addRegionMark = function (pid, rect) {
    var ui = createMarkBox(true);
    var sx = window.scrollX || window.pageXOffset || 0;
    var sy = window.scrollY || window.pageYOffset || 0;
    var mark = {
      pid: pid,
      kind: "region",
      docRect: { x: rect.x + sx, y: rect.y + sy, w: rect.w, h: rect.h },
      rect: { x: rect.x, y: rect.y, w: rect.w, h: rect.h },
      box: ui.box,
      badge: ui.badge,
      number: ui.number
    };
    attachRegionHandles(mark);
    marks.set(pid, mark);
    positionMark(mark);
  };
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
  var cancelDrag = function () {
    if (pointer.active) {
      try { if (document.documentElement && document.documentElement.releasePointerCapture) document.documentElement.releasePointerCapture(pointer.id); } catch (e) {}
    }
    pointer.active = false;
    pointer.dragging = false;
    pointer.target = null;
    marquee.style.display = "none";
  };
  var hitTestMark = function (cx, cy) {
    if (overlayHidden || !marks.size) return 0;
    var entries = Array.from(marks.entries());
    var limit = Math.min(entries.length, 32);
    for (var i = entries.length - 1; i >= entries.length - limit; i--) {
      var pid = entries[i][0];
      var mark = entries[i][1];
      var r = mark.rect;
      if (!r && mark.element && mark.element.isConnected) {
        var br = mark.element.getBoundingClientRect();
        r = { x: br.left, y: br.top, w: br.width, h: br.height };
      }
      if (!r || r.w <= 0 || r.h <= 0) continue;
      if (cx >= r.x && cx <= r.x + r.w && cy >= r.y && cy <= r.y + r.h) return pid;
    }
    return 0;
  };
  var clampToViewport = function (x, y) {
    var vp = viewportSize();
    return {
      x: Math.max(0, Math.min(vp.w, x)),
      y: Math.max(0, Math.min(vp.h, y)),
      vw: vp.w
    };
  };
  state.clearHover = clearHover;
  state.cancelDrag = cancelDrag;
  document.addEventListener("keydown", function (event) {
    if (!state.active || event.key !== "Escape") return;
    state.active = false;
    clearHover();
    cancelDrag();
    if (document.documentElement) document.documentElement.style.cursor = "";
    event.preventDefault();
    event.stopImmediatePropagation();
  }, true);
  document.addEventListener("mousemove", function (event) {
    if (overlayHidden) {
      state.hoverPID = 0;
      return;
    }
    if (!pointer.active && !manipulating) {
      state.hoverPID = hitTestMark(event.clientX, event.clientY);
    }
    if (!state.active || pointer.dragging || manipulating) { return; }
    var el = event.target;
    if (!el || el.nodeType !== 1 || el === host || el === state.hover) { return; }
    clearHover();
    state.hover = el;
    state.prevOutline = el.style.outline;
    state.prevOffset = el.style.outlineOffset;
    el.style.outline = "2px solid #3b82f6";
    el.style.outlineOffset = "1px";
  }, true);
  document.addEventListener("mouseleave", function () {
    state.hoverPID = 0;
  }, true);
  window.addEventListener("blur", function () {
    state.hoverPID = 0;
    cancelDrag();
  });
  document.addEventListener("pointerdown", function (event) {
    if (!state.active || overlayHidden || manipulating || event.button !== 0) { return; }
    if (event.target === host) { return; }
    var el = event.target;
    if (!el || el.nodeType !== 1) { return; }
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) { event.stopImmediatePropagation(); }
    suppressClickUntil = 0;
    pointer = {
      active: true,
      id: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      dragging: false,
      target: el
    };
  }, true);
  window.addEventListener("pointermove", function (event) {
    if (!pointer.active || event.pointerId !== pointer.id) { return; }
    var dx = event.clientX - pointer.startX;
    var dy = event.clientY - pointer.startY;
    if (!pointer.dragging && Math.hypot(dx, dy) > 6) {
      pointer.dragging = true;
      clearHover();
      ensureHost();
      try {
        if (document.documentElement && document.documentElement.setPointerCapture) {
          document.documentElement.setPointerCapture(pointer.id);
        }
      } catch (e) {}
    }
    if (!pointer.dragging) { return; }
    var a = clampToViewport(pointer.startX, pointer.startY);
    var b = clampToViewport(event.clientX, event.clientY);
    var x = Math.round(Math.min(a.x, b.x));
    var y = Math.round(Math.min(a.y, b.y));
    var w = Math.round(Math.abs(b.x - a.x));
    var h = Math.round(Math.abs(b.y - a.y));
    marquee.style.display = "block";
    marquee.style.left = x + "px";
    marquee.style.top = y + "px";
    marquee.style.width = w + "px";
    marquee.style.height = h + "px";
  }, true);
  window.addEventListener("pointerup", function (event) {
    if (!pointer.active || event.pointerId !== pointer.id) { return; }
    var wasDragging = pointer.dragging;
    var startX = pointer.startX;
    var startY = pointer.startY;
    var fallbackTarget = pointer.target;
    cancelDrag();
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) { event.stopImmediatePropagation(); }
    suppressClickUntil = Date.now() + 400;
    if (!state.active || overlayHidden) { return; }
    if (wasDragging) {
      var a = clampToViewport(startX, startY);
      var b = clampToViewport(event.clientX, event.clientY);
      var x = Math.round(Math.min(a.x, b.x));
      var y = Math.round(Math.min(a.y, b.y));
      var w = Math.round(Math.abs(b.x - a.x));
      var h = Math.round(Math.abs(b.y - a.y));
      if (w < 1 || h < 1) { return; }
      var pid = ++state.nextPID;
      var rect = { x: x, y: y, w: w, h: h };
      addRegionMark(pid, rect);
      state.queue.push({
        pid: pid,
        kind: "region",
        selector: "",
        tag: "",
        role: "",
        name: "",
        text: "",
        url: String(location.href || ""),
        vw: b.vw,
        rect: rect
      });
    } else {
      var el = document.elementFromPoint ? document.elementFromPoint(event.clientX, event.clientY) : null;
      if (!el || el === host || (el === document.documentElement && fallbackTarget && fallbackTarget !== document.documentElement)) {
        el = fallbackTarget;
      }
      if (!el || el.nodeType !== 1 || el === host) { return; }
      var er = el.getBoundingClientRect();
      var epid = ++state.nextPID;
      var vp = viewportSize();
      addMark(epid, el);
      state.queue.push({
        pid: epid,
        kind: "element",
        selector: selectorFor(el),
        tag: el.tagName ? el.tagName.toLowerCase() : "",
        role: roleFor(el),
        name: nameFor(el),
        text: clip(el.innerText || el.textContent || "", 200),
        url: String(location.href || ""),
        vw: vp.w,
        rect: {
          x: Math.round(er.left),
          y: Math.round(er.top),
          w: Math.round(er.width),
          h: Math.round(er.height)
        }
      });
    }
    // 与本地列表保持同一上限，drain 前被淘汰的标记也必须消失。
    if (marks.size > 32) state.removeMark(marks.keys().next().value);
    clearHover();
  }, true);
  window.addEventListener("pointercancel", function (event) {
    if (!pointer.active || event.pointerId !== pointer.id) { return; }
    cancelDrag();
  }, true);
  document.addEventListener("click", function (event) {
    if (Date.now() <= suppressClickUntil) {
      suppressClickUntil = 0;
      event.preventDefault();
      event.stopPropagation();
      if (event.stopImmediatePropagation) { event.stopImmediatePropagation(); }
      return;
    }
    if (!state.active) { return; }
    event.preventDefault();
    event.stopPropagation();
    if (event.stopImmediatePropagation) { event.stopImmediatePropagation(); }
  }, true);
  window.__mcodeAnnot = state;
  return state;
})()`
