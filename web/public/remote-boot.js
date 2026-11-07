// mcode 启动诊断。普通脚本，先于模块脚本执行：主脚本加载或解析失败时它自己的错误处理不会运行，
// 页面只剩空白，这里把原因写到页面上。保持 ES5 写法，旧系统也能执行。
(function () {
  var RECOVERED_KEY = 'mcode-boot-recovered';

  var currentTitle = '';
  var currentDetail = '';
  var probeDetail = '';
  var cspClues = [];
  var hasError = false;

  function buildContent() {
    var worker = 'serviceWorker' in navigator && navigator.serviceWorker.controller ? '是' : '否';
    var text = (currentTitle || 'mcode 页面出错') + '\n' +
      navigator.userAgent + '\n' +
      'Service Worker 接管：' + worker + '\n\n' +
      String(currentDetail).slice(0, 2000);

    if (cspClues.length > 0) {
      text += '\n\n【CSP 策略线索（仅供排查参考，非确定根因）】';
      for (var i = 0; i < cspClues.length; i++) {
        text += '\n' + cspClues[i];
      }
    }

    if (probeDetail) {
      text += '\n\n' + probeDetail;
    }

    return text;
  }

  function renderError() {
    if (!hasError) return;
    setTimeout(function () {
      var root = document.getElementById('root');
      // React 首次渲染会清掉占位；占位还在或根节点已空，说明页面没有内容可看。
      // 如果 React 已成功渲染内容，不要覆盖页面。
      if (!root || (root.childElementCount > 0 && !document.getElementById('boot-placeholder'))) return;

      var box = document.getElementById('boot-error');
      if (!box) {
        box = document.createElement('pre');
        box.id = 'boot-error';
        box.style.cssText = 'margin:0;padding:24px 16px;font:12px/1.5 ui-monospace,monospace;white-space:pre-wrap;word-break:break-all;color:#d14343;user-select:text;-webkit-user-select:text';
        root.appendChild(box);
      }
      box.textContent = buildContent();
    }, 0);
  }

  function show(title, detail) {
    hasError = true;
    if (!currentTitle) {
      currentTitle = title;
      currentDetail = detail;
    } else {
      currentDetail = String(currentDetail) + '\n\n' + title + ':\n' + String(detail);
    }
    renderError();
  }

  function describe(reason) {
    if (reason && reason.stack) return reason.name + ': ' + reason.message + '\n' + reason.stack;
    return String(reason);
  }

  // 旧的 Service Worker 可能挡在主脚本前面：清掉它和缓存后重载一次，仍失败再报错。
  function recover() {
    try {
      if (!('serviceWorker' in navigator) || !navigator.serviceWorker.controller) return false;
      if (sessionStorage.getItem(RECOVERED_KEY)) return false;
      sessionStorage.setItem(RECOVERED_KEY, '1');
    } catch (error) {
      return false;
    }
    var reload = function () { location.reload(); };
    navigator.serviceWorker.getRegistrations()
      .then(function (registrations) {
        return Promise.all(registrations.map(function (registration) { return registration.unregister(); }));
      })
      .then(function () { return 'caches' in window ? caches.keys() : []; })
      .then(function (keys) {
        return Promise.all(keys.map(function (key) { return caches.delete(key); }));
      })
      .then(reload, reload);
    return true;
  }

  // 懒加载 chunk：/assets/ 下带内容哈希的 js，名字不合规的一律不当作启动资源。
  var ASSET_CHUNK_PATH = /^\/assets\/[A-Za-z0-9._-]+\.js$/;

  function isAssetChunkUrl(value) {
    if (!value || typeof value !== 'string') return false;
    try {
      var a = document.createElement('a');
      a.href = value;
      if (a.protocol !== location.protocol || a.host !== location.host) return false;
      var path = a.pathname.charAt(0) === '/' ? a.pathname : '/' + a.pathname;
      return ASSET_CHUNK_PATH.test(path);
    } catch (e) {
      return false;
    }
  }

  // 通过锚点元素解析 URL 并严格限定同源、已知静态脚本路径 /remote-app.js、/remote-boot.js
  // 或 /assets/ 下的懒加载 chunk
  function getValidProbeUrl(src) {
    if (!src || typeof src !== 'string') return null;
    try {
      var a = document.createElement('a');
      a.href = src;
      if (a.protocol !== location.protocol || a.host !== location.host) {
        return null;
      }
      var path = a.pathname.charAt(0) === '/' ? a.pathname : '/' + a.pathname;
      if (path !== '/remote-app.js' && path !== '/remote-boot.js' && !ASSET_CHUNK_PATH.test(path)) {
        return null;
      }
      return a.protocol + '//' + a.host + path;
    } catch (e) {
      return null;
    }
  }

  // 换版本后旧 chunk 已不存在：先走现有的自愈路径（清 SW 与缓存后重载），
  // 没有 SW 接管时直接重载一次，仍失败再在本页报错。
  function recoverChunk() {
    if (recover()) return true;
    try {
      if (sessionStorage.getItem(RECOVERED_KEY)) return false;
      sessionStorage.setItem(RECOVERED_KEY, '1');
    } catch (e) {
      return false;
    }
    location.reload();
    return true;
  }

  // 动态 import 失败在浏览器上表现为 TypeError，文案各家不一，同时看地址与文案两类特征。
  function isChunkLoadFailure(reason) {
    if (!reason) return false;
    var message = reason.message ? String(reason.message) : String(reason);
    if (/\/assets\/[A-Za-z0-9._-]+\.js/.test(message)) return true;
    return /dynamically imported module|Importing a module script failed|error loading dynamically imported module/i.test(message);
  }

  var probed = false;

  function probeScript(url) {
    if (probed) return;
    probed = true;

    if (typeof fetch !== 'function') return;

    var controller = null;
    if (typeof AbortController === 'function') {
      try {
        controller = new AbortController();
      } catch (e) {}
    }

    var settled = false;
    var timedOut = false;

    var timer = setTimeout(function () {
      if (settled) return;
      timedOut = true;
      if (controller) {
        try {
          controller.abort();
        } catch (e) {}
      } else {
        settled = true;
        probeDetail = [
          '【补充诊断（失败后第 2 次探测 GET，仅供排查，不代表原始请求状态）】',
          '探测目标：' + url,
          '探测结果：请求超时（超过 10 秒未响应）',
          '说明：请求等待超过 10 秒；这是失败后的第 2 次请求探测，其结果不能证明原始请求也成功或失败。'
        ].join('\n');
        renderError();
      }
    }, 10000);

    var options = {
      method: 'GET',
      cache: 'no-store'
    };
    if (controller && controller.signal) {
      options.signal = controller.signal;
    }

    fetch(url, options)
      .then(function (res) {
        var status = res.status + (res.statusText ? ' ' + res.statusText : '');
        var contentType = res.headers.get('content-type') || '(未提供)';
        var contentEncoding = res.headers.get('content-encoding') || '(未提供)';
        var contentLength = res.headers.get('content-length') || '(未提供)';

        return res.text()
          .then(function (text) {
            if (settled) return;
            settled = true;
            clearTimeout(timer);

            probeDetail = [
              '【补充诊断（失败后第 2 次探测 GET，仅供排查，不代表原始请求状态）】',
              '探测目标：' + url,
              'HTTP 状态：' + status,
              'Content-Type：' + contentType,
              'Content-Encoding：' + contentEncoding,
              'Content-Length（传输长度）：' + contentLength,
              '正文是否完整读取：是',
              '解码后正文长度：' + text.length + ' 字符',
              '说明：Content-Length 为响应头声明的传输大小（gzip/br 等压缩时为压缩大小）；正文已由 fetch 完整读取解码（' + text.length + ' 字符）。这是失败后的第 2 次探测请求，其结果不能证明原始请求也成功或失败。'
            ].join('\n');
            renderError();
          })
          .catch(function (bodyErr) {
            if (settled) return;
            settled = true;
            clearTimeout(timer);

            var errDesc = timedOut ? '请求超时（10 秒）' : (bodyErr && bodyErr.message ? bodyErr.message : String(bodyErr));
            probeDetail = [
              '【补充诊断（失败后第 2 次探测 GET，仅供排查，不代表原始请求状态）】',
              '探测目标：' + url,
              'HTTP 状态：' + status,
              'Content-Type：' + contentType,
              'Content-Encoding：' + contentEncoding,
              'Content-Length（传输长度）：' + contentLength,
              '正文是否完整读取：否（读取正文中断）',
              '读取失败原因：' + errDesc,
              '说明：响应头已返回，但读取正文时传输中断或失败。这是失败后的第 2 次探测请求，其结果不能证明原始请求也成功或失败。'
            ].join('\n');
            renderError();
          });
      })
      .catch(function (fetchErr) {
        if (settled) return;
        settled = true;
        clearTimeout(timer);

        var errDesc = timedOut ? '请求超时（10 秒）' : (fetchErr && fetchErr.message ? fetchErr.message : String(fetchErr));
        probeDetail = [
          '【补充诊断（失败后第 2 次探测 GET，仅供排查，不代表原始请求状态）】',
          '探测目标：' + url,
          '探测结果：网络请求失败',
          '失败原因：' + errDesc,
          '排查线索：无法连接服务器、网络中断、TLS/证书校验失败或被客户端拦截',
          '说明：这是失败后的第 2 次探测请求，其结果不能证明原始请求也成功或失败。'
        ].join('\n');
        renderError();
      });
  }

  function isStartupCspViolation(event) {
    if (!event) return false;
    var directive = event.effectiveDirective || event.violatedDirective || '';
    var uri = event.blockedURI || event.blockedUri || '';
    if (uri.indexOf('/remote-app.js') !== -1 || uri.indexOf('/remote-boot.js') !== -1 || isAssetChunkUrl(uri)) {
      return true;
    }
    if (directive.indexOf('script') !== -1 || directive.indexOf('connect') !== -1 || directive.indexOf('default') !== -1) {
      return true;
    }
    return false;
  }

  function onSecurityPolicyViolation(event) {
    if (!isStartupCspViolation(event)) return;
    var directive = event.effectiveDirective || event.violatedDirective || '(未知指令)';
    var uri = event.blockedURI || event.blockedUri || '(未知资源)';
    uri = getValidProbeUrl(uri) || ((uri === 'inline' || uri === 'eval') ? uri : '(非启动静态资源，地址已隐藏)');
    var disposition = event.disposition ? ' [' + event.disposition + ']' : '';
    var clue = '指令：' + directive + disposition + '，被阻止资源：' + uri;
    for (var i = 0; i < cspClues.length; i++) {
      if (cspClues[i] === clue) return;
    }
    if (cspClues.length < 5) {
      cspClues.push(clue);
      if (hasError) {
        renderError();
      }
    }
  }

  if (typeof document !== 'undefined' && document.addEventListener) {
    document.addEventListener('securitypolicyviolation', onSecurityPolicyViolation);
  }
  if (typeof window !== 'undefined' && window.addEventListener) {
    window.addEventListener('securitypolicyviolation', onSecurityPolicyViolation);
  }

  window.addEventListener('error', function (event) {
    var target = event.target;
    if (target && target !== window && target.tagName === 'SCRIPT') {
      var isChunk = isAssetChunkUrl(target.src);
      if (!(isChunk ? recoverChunk() : recover())) {
        show(isChunk ? 'mcode 页面资源加载失败' : 'mcode 页面脚本加载失败', (target.src || '') +
          (isChunk ? '\n页面资源可能已更新或暂时不可用，请刷新页面后重新加载。' : ''));
        var probeUrl = getValidProbeUrl(target.src);
        if (probeUrl) {
          probeScript(probeUrl);
        }
      }
      return;
    }
    if (target && target !== window) return;
    var where = event.filename ? '\n' + event.filename + ':' + event.lineno + ':' + event.colno : '';
    show('mcode 页面出错', (event.error ? describe(event.error) : event.message) + where);
  }, true);

  window.addEventListener('unhandledrejection', function (event) {
    if (isChunkLoadFailure(event.reason)) {
      if (recoverChunk()) return;
      show('mcode 页面资源加载失败', '页面资源可能已更新或暂时不可用，请刷新页面后重新加载。\n' + describe(event.reason));
      return;
    }
    show('mcode 页面出错', describe(event.reason));
  });

  // 页面成功加载后清掉自愈标记，否则同一标签页里再遇到脚本加载失败就不会再自愈。
  window.addEventListener('load', function () {
    setTimeout(function () {
      if (!hasError) {
        try { sessionStorage.removeItem(RECOVERED_KEY); } catch (e) {}
      }
    }, 3000);
  });
})();
