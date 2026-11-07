// Run with: node scripts/check-device-session.mjs (after npm ci).
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import vm from 'node:vm';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const ts = createRequire(path.join(root, 'package.json'))('typescript');
const tokenKey = 'mcode_remote_session_token';
const identity = { deviceID: '95a01908-6b43-40be-8d84-057bb3fa6826', label: 'Test phone' };
const token = 'a'.repeat(64);
const thread = {
  id: 'thread-1', projectID: 'project-1', title: 'Test', provider: 'codex', model: null,
  effort: null, isRunning: false, isArchived: false, isPinned: false,
  isDormant: false, hasUnread: false, attention: 'none', lastActivityText: null,
  section: 'active', orderKey: '1',
  updatedAt: '2026-10-05T00:00:00Z', lastStatus: null, hasPendingRequest: false
};
const snapshot = (runID = 'run-1', sequence = 0) => ({
  threadID: thread.id, runID, sequence, thread,
  history: { threadID: thread.id, messages: [], hasMore: false, truncated: false }
});
const projection = (runID, sequence) => ({
  threadID: thread.id, runID, sequence, kind: 'projection',
  payload: { thread, messages: [], removedMessageIDs: [] }
});
const memoryStorage = (entries = [], fail = false) => {
  const values = new Map(entries);
  const access = () => { if (fail) throw new Error('Storage blocked'); };
  return {
    getItem(key) { access(); return values.get(key) ?? null; },
    setItem(key, value) { access(); values.set(key, value); },
    removeItem(key) { access(); values.delete(key); }
  };
};
const eventTarget = () => {
  const listeners = new Map();
  return {
    addEventListener(name, fn) { if (!listeners.has(name)) listeners.set(name, new Set()); listeners.get(name).add(fn); },
    removeEventListener(name, fn) { listeners.get(name)?.delete(fn); },
    emit(name) { for (const fn of listeners.get(name) ?? []) fn(); }
  };
};

// Exercise the production Context/RPC in an isolated HTTP page model; no app/user storage is touched.
function page(localStorage = memoryStorage(), sessionStorage = memoryStorage()) {
  const sockets = [], methods = [], slots = [], effects = [], modules = new Map();
  let cursor = 0, mounted = false, scheduled = false, provider;
  const env = {
    window: { ...eventTarget(), location: new URL('http://127.0.0.1:8765/'),
      localStorage, sessionStorage, innerHeight: 800, history: { replaceState() {} },
      matchMedia: () => ({ ...eventTarget(), matches: false }) },
    document: { ...eventTarget(), visibilityState: 'visible', documentElement: {
      classList: { add() {}, remove() {} }, style: { setProperty() {} }
    } }, navigator: { onLine: true }, console, URL, URLSearchParams,
    setTimeout, clearTimeout, setInterval, clearInterval, queueMicrotask
  };
  const rig = {
    methods, sockets, localStorage, sessionStorage, env, value: null,
    server(request) {
      switch (request.method) {
        case 'session.pair': return { result: { sessionToken: token, ...identity } };
        case 'session.authenticate': return { result: { authenticated: true, ...identity } };
        case 'session.revokeDevice': return { result: { deviceID: identity.deviceID, revoked: true } };
        case 'kanban.getBoard': return { result: { schemaVersion: 1, revision: 1, projects: [], tasks: [] } };
        case 'capabilities.get': return { result: { methods: ['threads.subscribe', 'threads.unsubscribe'] } };
        case 'projects.list': return { result: { revision: 1, projects: [] } };
        case 'threads.subscribe': return { result: { threadID: thread.id, subscribed: true, snapshot: snapshot() } };
        case 'threads.unsubscribe': return { result: { threadID: thread.id, unsubscribed: true } };
        default: throw new Error(`Unexpected method: ${request.method}`);
      }
    },
    async flush() { for (let i = 0; i < 80; i++) await Promise.resolve(); },
    mount() { mounted = true; provider = load(path.join(root, 'src/context/RemoteContext.tsx')).RemoteProvider; render(); },
    close() { mounted = false; for (const s of slots) s?.cleanup?.(); },
    event(value) { sockets.at(-1).receive({ jsonrpc: '2.0', method: 'threads.event', params: value }); }
  };
  const schedule = () => {
    if (!mounted || scheduled) return;
    scheduled = true;
    queueMicrotask(() => { scheduled = false; if (mounted) render(); });
  };
  const same = (a, b) => a && b && a.length === b.length && a.every((v, i) => Object.is(v, b[i]));
  const react = {
    createContext: () => ({ Provider: 'Provider' }),
    createElement: (_type, props) => { rig.value = props.value; return null; },
    useState(initial) {
      const i = cursor++;
      if (!slots[i]) slots[i] = { value: typeof initial === 'function' ? initial() : initial };
      return [slots[i].value, (next) => {
        const value = typeof next === 'function' ? next(slots[i].value) : next;
        if (!Object.is(value, slots[i].value)) { slots[i].value = value; schedule(); }
      }];
    },
    useRef(initial) { const i = cursor++; return slots[i] ?? (slots[i] = { current: initial }); },
    useCallback(fn, deps) {
      const i = cursor++;
      if (!same(slots[i]?.deps, deps)) slots[i] = { fn, deps };
      return slots[i].fn;
    },
    useMemo(fn, deps) {
      const i = cursor++;
      if (!same(slots[i]?.deps, deps)) slots[i] = { value: fn(), deps };
      return slots[i].value;
    },
    useEffect(fn, deps) {
      const i = cursor++;
      if (!same(slots[i]?.deps, deps)) effects.push(() => {
        slots[i]?.cleanup?.(); slots[i] = { deps, cleanup: fn() };
      });
    }
  };
  function render() {
    cursor = 0; provider({ children: null });
    for (const effect of effects.splice(0)) effect();
  }
  class Socket {
    static OPEN = 1;
    readyState = 0;
    constructor(url) {
      assert.ok(url === 'ws://127.0.0.1:8765/ws', 'WebSocket URL must contain no credential');
      sockets.push(this);
      queueMicrotask(() => { if (this.readyState === 0) { this.readyState = 1; void this.onopen?.({ currentTarget: this }); } });
    }
    send(raw) {
      const request = JSON.parse(raw); methods.push(request.method);
      queueMicrotask(() => {
        if (this.readyState !== Socket.OPEN) return;
        const reply = rig.server(request, this);
        if (reply) this.reply(request, reply);
      });
    }
    reply(request, reply) { this.receive({ jsonrpc: '2.0', id: request.id, ...reply }); }
    receive(message) { this.onmessage?.({ currentTarget: this, data: JSON.stringify(message) }); }
    close() { if (this.readyState === 3) return; this.readyState = 3; this.onclose?.({ currentTarget: this }); }
  }
  env.WebSocket = Socket;
  const context = vm.createContext(env);
  function load(file) {
    if (modules.has(file)) return modules.get(file).exports;
    const module = { exports: {} }; modules.set(file, module);
    const code = ts.transpileModule(fs.readFileSync(file, 'utf8'), { compilerOptions: {
      module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022,
      jsx: ts.JsxEmit.React, esModuleInterop: true
    } }).outputText;
    const require = (name) => {
      if (name === 'react') return { __esModule: true, default: react, ...react };
      const base = path.resolve(path.dirname(file), name);
      return load(fs.existsSync(`${base}.ts`) ? `${base}.ts` : `${base}.tsx`);
    };
    vm.runInContext(`(function(require,module,exports){${code}\n})`, context, { filename: file })(require, module, module.exports);
    return module.exports;
  }
  rig.storage = load(path.join(root, 'src/services/storage.ts'));
  return rig;
}

async function check(name, run) { await run(); console.log(`PASS: ${name}`); }
const rigs = [];
const makePage = (...args) => { const rig = page(...args); rigs.push(rig); return rig; };
try {
  await check('6 位授权码遵循配对契约并通过现有网页入口连接', async () => {
    const rig = makePage();
    const schema = JSON.parse(fs.readFileSync(path.join(root, '../docs/remote-control.schema.json'), 'utf8'));
    const rule = schema.$defs.SessionPairParams.properties.token;
    for (const value of ['000123', '123456', 'a'.repeat(64)]) {
      assert.equal(rig.storage.parsePairingInput(` ${value} `).token, value);
      assert.ok(value.length >= rule.minLength && value.length <= rule.maxLength);
      assert.ok(new RegExp(rule.pattern).test(value));
    }
    for (const value of ['12345', '1234567', '12a456']) {
      assert.equal(rig.storage.parsePairingInput(value).token, null);
      assert.ok(!new RegExp(rule.pattern).test(value));
    }
    assert.equal(rig.storage.parsePairingInput('http://other-host/#pair=000123').token, null);
    assert.equal(rig.storage.parsePairingInput('http://127.0.0.1:8765/#pair=000123').token, '000123');
    const normalServer = rig.server;
    rig.server = (request) => {
      if (request.method === 'session.pair') assert.equal(request.params.token, '000123');
      return normalServer(request);
    };
    rig.mount(); await rig.flush();
    rig.value.pairWithToken(rig.storage.parsePairingInput('000123').token);
    await rig.flush();
    assert.equal(rig.value.connectionState, 'connected');
  });

  await check('remembered HTTP page restore, temporary tab lifetime, legacy identity and policy migration', async () => {
    const persistent = memoryStorage();
    const first = makePage(persistent); first.mount(); await first.flush();
    assert.equal(first.value.rememberDevice, true);
    first.value.pairWithToken('b'.repeat(64)); await first.flush();
    assert.equal(first.value.connectionState, 'connected');
    assert.ok(persistent.getItem(tokenKey) === token); first.close();
    const reopened = makePage(persistent); reopened.mount(); await reopened.flush();
    assert.equal(reopened.value.connectionState, 'connected');
    assert.ok(reopened.methods.includes('session.authenticate') && !reopened.methods.includes('session.pair'));
    assert.equal(reopened.value.deviceIdentity.deviceID, identity.deviceID);
    reopened.value.setRememberDevice(false); await reopened.flush();
    assert.ok(persistent.getItem(tokenKey) === null && reopened.sessionStorage.getItem(tokenKey) === token);
    reopened.close();
    const closedTab = makePage(persistent); closedTab.mount(); await closedTab.flush();
    assert.equal(closedTab.value.connectionState, 'unpaired');
    closedTab.value.setRememberDevice(false); await closedTab.flush();
    closedTab.value.pairWithToken('b'.repeat(64)); await closedTab.flush();
    assert.ok(closedTab.sessionStorage.getItem(tokenKey) === token && persistent.getItem(tokenKey) === null);
    const legacy = makePage(memoryStorage(), memoryStorage([[tokenKey, token]]));
    legacy.mount(); await legacy.flush();
    assert.equal(legacy.value.rememberDevice, false);
    assert.equal(legacy.value.deviceIdentity.deviceID, identity.deviceID);
    legacy.value.setRememberDevice(true); await legacy.flush();
    assert.ok(legacy.sessionStorage.getItem(tokenKey) === null && legacy.localStorage.getItem(tokenKey) === token);
  });

  await check('storage read/write/delete failure is visible; active-page authentication survives', async () => {
    const rig = makePage(memoryStorage([], true), memoryStorage([], true));
    rig.mount(); await rig.flush(); assert.ok(rig.value.credentialNotice);
    rig.value.pairWithToken('b'.repeat(64)); await rig.flush();
    assert.equal(rig.value.connectionState, 'connected'); assert.match(rig.value.credentialNotice, /无法保存/);
    rig.sockets.at(-1).close(); rig.value.reconnect(); await rig.flush();
    assert.equal(rig.value.connectionState, 'connected');
    rig.value.clearAuth(); await rig.flush(); assert.match(rig.value.credentialNotice, /未能完全清理/);
  });

  await check('service worker never intercepts credentials, RPC, WebSocket or write requests', async () => {
    const listeners = new Map();
    const worker = { location: { origin: 'http://127.0.0.1:8765' },
      addEventListener: (name, fn) => listeners.set(name, fn) };
    vm.runInNewContext(fs.readFileSync(path.join(root, 'public/remote-sw.js'), 'utf8'), { self: worker, URL });
    for (const [url, method] of [['/ws', 'GET'], ['/rpc', 'GET'], ['/api', 'GET'],
      ['/?token=synthetic', 'GET'], ['/?session=synthetic', 'GET'], ['/?pair=synthetic', 'GET'], ['/', 'POST']]) {
      listeners.get('fetch')({ request: { url: worker.location.origin + url, method,
        headers: { get: () => null } }, respondWith: () => assert.fail('Sensitive request was cached') });
    }
  });

  await check('ordinary disconnect and non-auth RPC failure retain credentials; explicit auth failure clears both', async () => {
    const rig = makePage(memoryStorage([[tokenKey, token]])); rig.mount(); await rig.flush();
    rig.sockets.at(-1).close(); await rig.flush(); assert.ok(rig.localStorage.getItem(tokenKey) === token);
    rig.value.reconnect(); await rig.flush(); assert.equal(rig.value.connectionState, 'connected');
    const normalServer = rig.server;
    rig.server = (request) => request.method === 'kanban.getBoard'
      ? { error: { code: -32603, message: 'Unavailable' } } : normalServer(request);
    rig.value.reconnect(); await rig.flush(); assert.ok(rig.localStorage.getItem(tokenKey) === token);
    rig.sessionStorage.setItem(tokenKey, token);
    rig.server = (request) => request.method === 'session.authenticate'
      ? { error: { code: -32001, message: 'Unauthorized' } } : normalServer(request);
    rig.value.reconnect(); await rig.flush();
    assert.equal(rig.value.connectionState, 'auth_failed');
    assert.ok(rig.localStorage.getItem(tokenKey) === null && rig.sessionStorage.getItem(tokenKey) === null);
    const count = rig.sockets.length; rig.value.reconnect(); await rig.flush(); assert.equal(rig.sockets.length, count);
  });

  await check('online forget revokes self and stops reconnect; explicit failure retains credentials; offline/unknown clears locally', async () => {
    for (const outcome of ['success', 'failure', 'offline', 'unknown']) {
      const rig = makePage(memoryStorage([[tokenKey, token]])); rig.mount(); await rig.flush();
      const normalServer = rig.server;
      rig.server = (request, socket) => {
        if (request.method !== 'session.revokeDevice') return normalServer(request);
        assert.equal(request.params.deviceID, identity.deviceID);
        if (outcome === 'failure') return { error: { code: -32603, message: 'Unavailable' } };
        if (outcome === 'unknown') { socket.close(); return null; }
        socket.reply(request, normalServer(request)); socket.close(); return null;
      };
      if (outcome === 'offline') rig.env.navigator.onLine = false;
      await rig.value.forgetDevice(); await rig.flush();
      if (outcome === 'failure') {
        assert.equal(rig.value.connectionState, 'connected');
        assert.ok(rig.localStorage.getItem(tokenKey) === token); assert.match(rig.value.credentialNotice, /凭据已保留/);
      } else {
        assert.equal(rig.value.connectionState, 'unpaired'); assert.ok(rig.localStorage.getItem(tokenKey) === null);
        assert.match(rig.value.credentialNotice, outcome === 'success' ? /配对已撤销/ : /Mac.*撤销/);
        const count = rig.sockets.length;
        rig.value.reconnect(); rig.env.window.emit('online'); await rig.flush(); assert.equal(rig.sockets.length, count);
        if (outcome === 'offline') assert.ok(!rig.methods.includes('session.revokeDevice'));
      }
    }
  });

  await check('reconnect re-subscribes; threads.event gaps, run changes and buffer overflow trigger snapshots', async () => {
    const rig = makePage(memoryStorage([[tokenKey, token]])); rig.mount(); await rig.flush();
    const seen = [], snapshots = [];
    const handlers = { onSnapshot: s => snapshots.push(s), onEvent: e => seen.push(e), onState() {}, onError(e) { throw e; } };
    await rig.value.subscribeThread(thread.id, handlers).ready; await rig.flush();
    rig.event(projection('run-1', 1)); await rig.flush(); assert.equal(seen.length, 1);
    const normalServer = rig.server;
    let latest = snapshot('run-1', 3);
    rig.server = request => request.method === 'threads.subscribe'
      ? { result: { threadID: thread.id, subscribed: true, snapshot: latest } } : normalServer(request);
    rig.event(projection('run-1', 3)); await rig.flush();
    assert.equal(snapshots.at(-1).sequence, 3); assert.equal(seen.length, 1);
    latest = snapshot('run-2', 1); rig.event(projection('run-2', 1)); await rig.flush();
    assert.equal(snapshots.at(-1).runID, 'run-2');
    let held;
    rig.server = (request, socket) => {
      if (request.method === 'threads.subscribe') { held = { request, socket }; return null; }
      return normalServer(request);
    };
    rig.event(projection('run-2', 3)); await rig.flush();
    for (let sequence = 4; sequence < 140; sequence++) rig.event(projection('run-2', sequence));
    const before = snapshots.length;
    rig.server = request => request.method === 'threads.subscribe'
      ? { result: { threadID: thread.id, subscribed: true, snapshot: snapshot('run-2', 139) } } : normalServer(request);
    held.socket.reply(held.request, { result: { threadID: thread.id, subscribed: true, snapshot: snapshot('run-2', 139) } });
    await rig.flush(); assert.ok(snapshots.length >= before + 2, 'overflow must request another authoritative snapshot');
    const beforeReconnect = snapshots.length;
    rig.sockets.at(-1).close(); rig.value.reconnect(); await rig.flush();
    assert.equal(rig.value.connectionState, 'connected'); assert.ok(snapshots.length > beforeReconnect);
    rig.event(projection('run-2', 140)); await rig.flush(); assert.equal(seen.at(-1).sequence, 140);
  });
} finally {
  for (const rig of rigs) rig.close();
}
