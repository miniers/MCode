// mcode Remote Kanban Service Worker
// 只缓存固定静态资源；在线优先，离线才回退到当前构建的应用壳。

const CACHE_PREFIX = 'mcode-remote-shell-';
const MANIFEST_PATH = '/remote-manifest.webmanifest';
const SHELL_ASSETS = [
  '/',
  '/index.html',
  '/remote-app.js',
  '/remote-app.css',
  '/remote-manifest.webmanifest',
  '/remote-sw.js',
  '/remote-boot.js',
  '/remote-icon.svg',
  '/apple-touch-icon-180.png',
  '/icon-192.png'
];
const NETWORK_TIMEOUT_MS = 2500;
// 懒加载 chunk 与字体落在 /assets/ 下并使用内容哈希文件名，名字变即内容变，可以安全地缓存优先。
const LAZY_ASSET_PATH = /^\/assets\/[A-Za-z0-9._-]+\.(?:js|woff2|woff|ttf)$/;
const SENSITIVE_HEADERS = ['authorization', 'cookie', 'x-session-token', 'x-pairing-token'];
let currentCacheName = null;

function cacheNameFor(buildID) {
  if (typeof buildID !== 'string' || !/^[A-Za-z0-9._-]{1,128}$/.test(buildID)) {
    throw new Error('Invalid mcode web build ID');
  }
  return `${CACHE_PREFIX}${buildID}`;
}

async function resolveCacheName() {
  if (currentCacheName) return currentCacheName;

  const response = await fetch(MANIFEST_PATH, { cache: 'no-store' });
  if (!response.ok) throw new Error('Unable to read mcode web manifest');
  const manifest = await response.json();
  currentCacheName = cacheNameFor(manifest.buildID);
  return currentCacheName;
}

async function cacheShell() {
  const cache = await caches.open(await resolveCacheName());
  await cache.addAll(SHELL_ASSETS);
}

self.addEventListener('install', (event) => {
  event.waitUntil(cacheShell().then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
  event.waitUntil((async () => {
    const cacheName = await resolveCacheName();
    const keys = await caches.keys();
    await Promise.all(
      keys
        .filter((key) => key.startsWith(CACHE_PREFIX) && key !== cacheName)
        .map((key) => caches.delete(key))
    );
    await self.clients.claim();
  })());
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);

  // 绝对不拦截或缓存 WebSocket、带凭据或带查询参数的请求。
  if (
    url.pathname === '/ws' ||
    req.headers.get('Upgrade') === 'websocket' ||
    SENSITIVE_HEADERS.some((name) => req.headers.get(name))
  ) {
    return;
  }

  // 只处理同源、无查询参数的固定静态白名单。
  if (req.method !== 'GET' || url.origin !== self.location.origin || url.search) {
    return;
  }

  // 懒加载 chunk 与字体：不在固定壳清单里，但同样要离线可用，命中缓存即返回。
  if (LAZY_ASSET_PATH.test(url.pathname)) {
    event.respondWith(handleLazyChunkRequest(req));
    return;
  }

  if (!SHELL_ASSETS.includes(url.pathname)) return;

  event.respondWith(handleShellRequest(event, req));
});

// 在线优先但不被慢网拖死：先并行发起网络请求并查缓存；网络在 NETWORK_TIMEOUT_MS 内返回就用新资源，
// 超时则立即回退到已缓存的应用壳，网络请求继续在后台完成并刷新缓存。无缓存时才一直等网络。
async function handleShellRequest(event, req) {
  const networkPromise = fetchAndCache(req);
  const cached = await cachedShell(req);

  if (!cached) {
    try {
      return await networkPromise;
    } catch {
      return offlineResponse();
    }
  }

  let timer = null;
  const timeout = new Promise((resolve) => {
    timer = setTimeout(() => resolve(null), NETWORK_TIMEOUT_MS);
  });
  try {
    const fresh = await Promise.race([networkPromise, timeout]);
    if (fresh) return fresh;
  } catch {
    // 网络失败：回退缓存
  } finally {
    clearTimeout(timer);
  }
  event.waitUntil(networkPromise.catch(() => {}));
  return cached;
}

// 懒加载 chunk 与字体：先查缓存，未命中再取网络并写入当前构建缓存；离线且无缓存时给离线提示。
async function handleLazyChunkRequest(req) {
  const cached = await cachedShell(req);
  if (cached) return cached;
  try {
    return await fetchAndCache(req);
  } catch {
    return offlineResponse();
  }
}

async function fetchAndCache(req) {
  const networkResponse = await fetch(req);
  if (networkResponse.ok && networkResponse.type === 'basic') {
    try {
      const cache = await caches.open(await resolveCacheName());
      await cache.put(req, networkResponse.clone());
    } catch {
      // 网络响应仍可直接返回；缓存失败不应影响在线页面。
    }
  }
  return networkResponse;
}

async function cachedShell(req) {
  const options = { ignoreVary: true };
  if (currentCacheName) {
    const hit = await caches.open(currentCacheName).then((cache) => cache.match(req, options));
    if (hit) return hit;
  }
  return caches.match(req, options);
}

function offlineResponse() {
  return new Response('mcode 当前离线，无法连接 Mac。', {
    status: 503,
    headers: {
      'Content-Type': 'text/plain; charset=utf-8',
      'Cache-Control': 'no-store'
    }
  });
}

// Web Push 消息接收与系统通知展示

// 推送载荷归一化：旧版本载荷没有 answerable/questionID/answers，缺省一律视为不可答。
function parsePushPayload(raw) {
  const payload = {
    title: 'mcode',
    body: '',
    threadID: null,
    messageID: null,
    requestID: null,
    silent: false,
    answerable: false,
    questionID: null,
    answers: []
  };

  if (!raw || typeof raw.json !== 'function') return payload;

  let data = null;
  try {
    data = raw.json();
  } catch {
    // 解析失败时回退为默认标题与空正文
    return payload;
  }
  if (!data || typeof data !== 'object') return payload;

  if (typeof data.title === 'string' && data.title.length > 0) {
    payload.title = data.title;
  }
  if (typeof data.body === 'string') {
    payload.body = data.body;
  }
  if (typeof data.threadID === 'string' && data.threadID.length > 0) {
    payload.threadID = data.threadID;
  }
  if (typeof data.messageID === 'string' && data.messageID.length > 0) {
    payload.messageID = data.messageID;
  }
  if (typeof data.requestID === 'string' && data.requestID.length > 0) {
    payload.requestID = data.requestID;
  }
  payload.silent = data.silent === true;

  // 答案按钮必须逐项合法，任何一项畸形都整体降级为只读通知，避免提交错误选项。
  const answers = normalizePushAnswers(data.answers);
  if (data.answerable === true && answers.length > 0) {
    payload.answerable = true;
    payload.questionID = typeof data.questionID === 'string' ? data.questionID : null;
    payload.answers = answers;
  }
  return payload;
}

// 只接受 Go 侧约定的 ans0/ans1 两个按钮；非法结构返回空数组。
function normalizePushAnswers(raw) {
  if (!Array.isArray(raw) || raw.length === 0 || raw.length > 2) return [];
  const answers = [];
  for (const item of raw) {
    if (!item || typeof item !== 'object') return [];
    if (typeof item.action !== 'string' || !/^ans[0-9]$/.test(item.action)) return [];
    if (typeof item.label !== 'string' || item.label.length === 0) return [];
    if (typeof item.value !== 'string' || item.value.length === 0) return [];
    answers.push({ action: item.action, label: item.label, value: item.value });
  }
  return answers;
}

// 同一请求的通知用 requestID 归并；旧载荷没有 requestID 时退回 threadID。
function pushNotificationTag(payload) {
  if (!payload || typeof payload !== 'object') return 'mcode';
  return payload.requestID || payload.threadID || 'mcode';
}

// 组装 showNotification 参数：只有可答通知才带按钮与常驻，其余与旧版本完全一致。
function buildPushNotificationOptions(payload) {
  const options = {
    body: payload.body || '',
    tag: pushNotificationTag(payload),
    // 静音通知不能 renotify，按规范会抛 TypeError
    renotify: payload.silent !== true,
    silent: payload.silent === true,
    data: {
      threadID: payload.threadID || null,
      messageID: payload.messageID || null,
      requestID: payload.requestID || null
    },
    icon: '/icon-192.png'
  };

  const answers = Array.isArray(payload.answers) ? payload.answers : [];
  if (payload.answerable === true && answers.length > 0) {
    options.requireInteraction = true;
    options.actions = answers
      .map((answer) => ({ action: 'answer:' + answer.action, title: answer.label }))
      .concat([{ action: 'open', title: '打开' }]);
    // 点击时按 action 查表，data 里必须保留原始 value。
    options.data.answerable = true;
    options.data.questionID = payload.questionID || null;
    options.data.answers = answers;
  }
  return options;
}

// 'answer:ans1' → { action, label, value }；'open'、空串、越界或畸形载荷 → null。
function resolveAnswerAction(action, answers) {
  if (typeof action !== 'string' || !action.startsWith('answer:')) return null;
  const key = action.slice('answer:'.length);
  for (const answer of normalizePushAnswers(answers)) {
    if (answer.action === key) return { action: answer.action, label: answer.label, value: answer.value };
  }
  return null;
}

self.addEventListener('push', (event) => {
  const payload = parsePushPayload(event.data);
  event.waitUntil(
    self.registration.showNotification(payload.title, buildPushNotificationOptions(payload))
  );
});

// 内联作答结果的通知文案：成功与 409 只是短暂确认，失败保留深链让用户点开处理。
function buildAnswerResultNotification(status, answer, data) {
  const source = data && typeof data === 'object' ? data : {};
  const threadID = typeof source.threadID === 'string' ? source.threadID : null;
  const messageID = typeof source.messageID === 'string' ? source.messageID : null;
  const requestID = typeof source.requestID === 'string' && source.requestID ? source.requestID : null;
  const label = answer && typeof answer.label === 'string' ? answer.label : '';
  const baseData = { threadID, messageID, requestID };

  if (status === 200) {
    return {
      title: '已回复',
      closeAfterMs: 4000,
      options: { body: label, tag: 'answered:' + (requestID || 'mcode'), silent: true, data: baseData, icon: '/icon-192.png' }
    };
  }
  if (status === 409) {
    return {
      title: '已在别处处理',
      closeAfterMs: 4000,
      options: {
        body: '这道题已经答复过了。',
        tag: 'answered:' + (requestID || 'mcode'),
        silent: true,
        data: baseData,
        icon: '/icon-192.png'
      }
    };
  }
  // 失败通知不带 actions：点击主体走原有打开逻辑，回到会话里重试。
  return {
    title: '回复失败，点开处理',
    closeAfterMs: 0,
    options: {
      body: '点这里打开会话再试一次。',
      tag: requestID || threadID || 'mcode',
      silent: true,
      data: baseData,
      icon: '/icon-192.png'
    }
  };
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// 从 IndexedDB 取页面写入的会话凭据；只有「记住此设备」的会话才有值。
function readPushSession() {
  return new Promise((resolve) => {
    if (typeof indexedDB === 'undefined') {
      resolve(null);
      return;
    }
    const open = indexedDB.open('mcode-sw-auth', 1);
    open.onupgradeneeded = () => {
      const db = open.result;
      if (!db.objectStoreNames.contains('session')) db.createObjectStore('session');
    };
    open.onerror = () => resolve(null);
    open.onblocked = () => resolve(null);
    open.onsuccess = () => {
      const db = open.result;
      let request;
      try {
        request = db.transaction('session', 'readonly').objectStore('session').get('current');
      } catch {
        db.close();
        resolve(null);
        return;
      }
      request.onsuccess = () => {
        const value = request.result;
        db.close();
        resolve(value && typeof value.token === 'string' ? value : null);
      };
      request.onerror = () => {
        db.close();
        resolve(null);
      };
    };
  });
}

async function closeNotificationsByTag(tag) {
  const notifications = await self.registration.getNotifications({ tag });
  for (const notification of notifications) notification.close();
}

// 在 Service Worker 内提交答案，全程不打开页面。
async function submitInlineAnswer(data, answer) {
  const source = data && typeof data === 'object' ? data : {};
  const threadID = typeof source.threadID === 'string' ? source.threadID : '';
  const requestID = typeof source.requestID === 'string' ? source.requestID : '';

  let status = 0;
  const session = await readPushSession();
  if (session && threadID && requestID) {
    try {
      const response = await fetch(new URL('/api/remote/answer', self.registration.scope), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: 'Bearer ' + session.token
        },
        credentials: 'omit',
        cache: 'no-store',
        body: JSON.stringify({ threadID, requestID, choice: answer.value })
      });
      status = response.status;
    } catch {
      // 网络失败按失败态处理，保留深链
      status = 0;
    }
  }

  const result = buildAnswerResultNotification(status, answer, source);
  await self.registration.showNotification(result.title, result.options);
  if (result.closeAfterMs > 0) {
    await delay(result.closeAfterMs);
    await closeNotificationsByTag(result.options.tag);
  }
}

// 点击通知：先处理内联作答按钮，其余关闭通知并聚焦/打开会话
self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const data = event.notification.data || {};
  const answer = resolveAnswerAction(event.action, data.answers);
  if (answer) {
    event.waitUntil(submitInlineAnswer(data, answer));
    return;
  }

  const threadID = data.threadID || null;
  const messageID = data.messageID || null;
  const requestID = data.requestID || null;

  event.waitUntil(
    (async () => {
      const windowClients = await self.clients.matchAll({
        type: 'window',
        includeUncontrolled: true
      });
      if (windowClients.length > 0) {
        const client = windowClients[0];
        await client.focus();
        client.postMessage({ type: 'mcode-open-thread', threadID, messageID, requestID });
      } else {
        // 旧推送没有 messageID/requestID，查询串为空，URL 与之前一致。
        const params = [];
        if (messageID) params.push('m=' + encodeURIComponent(messageID));
        if (requestID) params.push('r=' + encodeURIComponent(requestID));
        const query = params.length > 0 ? '?' + params.join('&') : '';
        await self.clients.openWindow(threadID ? '/#/t/' + encodeURIComponent(threadID) + query : '/');
      }
    })()
  );
});

// 页面侧发现请求已不在待处理时，按 requestID 关掉对应通知。
// 旧版本通知的 tag 是 threadID，所以同时比对 data.requestID。
self.addEventListener('message', (event) => {
  const data = event.data || {};
  if (data.type !== 'mcode-request-settled' || typeof data.requestID !== 'string') return;
  event.waitUntil(
    (async () => {
      const notifications = await self.registration.getNotifications();
      for (const notification of notifications) {
        if (notification.tag === data.requestID || (notification.data && notification.data.requestID === data.requestID)) {
          notification.close();
        }
      }
    })()
  );
});

