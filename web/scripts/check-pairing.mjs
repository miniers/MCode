import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import ts from 'typescript';

const code = 'a'.repeat(64);
const session = 'b'.repeat(64);
const board = { schemaVersion: 1, revision: 1, projects: [], tasks: [] };
const flush = async () => { for (let i = 0; i < 5; i++) await Promise.resolve(); };

function environment(href = 'http://localhost:9281/') {
  const values = new Map();
  const timers = new Map();
  const sockets = [];
  const states = [];
  const boards = [];
  const errors = [];
  const window = {
    location: new URL(href),
    addEventListener() {},
    removeEventListener() {},
    sessionStorage: {
      getItem: (key) => values.get(key) ?? null,
      setItem: (key, value) => values.set(key, value),
      removeItem: (key) => values.delete(key)
    },
    history: { replaceState: (_, __, url) => { window.location = new URL(url, window.location); } }
  };
  class Socket {
    static OPEN = 1;
    readyState = 0;
    requests = [];
    constructor() { sockets.push(this); }
    open() { this.readyState = Socket.OPEN; this.onopen?.({ currentTarget: this }); }
    send(value) { this.requests.push(JSON.parse(value)); }
    respond(result, error) {
      const request = this.requests.at(-1);
      this.onmessage?.({ currentTarget: this, data: JSON.stringify({ jsonrpc: '2.0', id: request.id, result, error }) });
    }
    close() { this.readyState = 3; this.onclose?.({ currentTarget: this }); }
  }
  const context = vm.createContext({
    window, URL, URLSearchParams, WebSocket: Socket,
    setTimeout: (callback) => { const id = Symbol(); timers.set(id, callback); return id; },
    clearTimeout: (id) => timers.delete(id)
  });
  const load = (file, imports = {}) => {
    const source = readFileSync(new URL(`../src/services/${file}.ts`, import.meta.url), 'utf8');
    const js = ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 } }).outputText;
    const module = { exports: {} };
    vm.runInContext(`(function(exports, require, module) { ${js}\n})`, context)(module.exports, (name) => {
      assert.ok(name in imports, `unexpected import: ${name}`);
      return imports[name];
    }, module);
    return module.exports;
  };
  const storage = load('storage');
  const { McodeRpcClient } = load('rpc', { './storage': storage });
  const client = (pairToken) => new McodeRpcClient({
    onConnectionChange: (state) => states.push(state),
    onBoardUpdated: (value) => boards.push(value),
    onAuthFailed: (error) => errors.push(error)
  }, pairToken);
  return { window, storage, client, sockets, states, boards, errors, timers };
}

const parsing = environment();
assert.equal(parsing.storage.parsePairingInput(` ${code.toUpperCase()} `).token, code);
assert.equal(parsing.storage.parsePairingInput(`http://localhost:9281/#pair=${code}`).token, code);
for (const input of [
  '', 'abc', 'javascript:alert(1)',
  `http://other-host:9281/#pair=${code}`,
  `https://localhost:9281/#pair=${code}`,
  `http://localhost:9282/#pair=${code}`,
  `http://user@localhost:9281/#pair=${code}`,
  `http://localhost:9281/?pair=${code}`,
  `http://localhost:9281/#pair=${code}&pair=${code}`,
  'http://localhost:9281/',
  'http://localhost:9281/#pair=bad'
]) {
  const result = parsing.storage.parsePairingInput(input);
  assert.equal(result.token, null);
  assert.ok(result.error);
  assert.ok(!result.error.includes(code));
}
for (const fragment of [`#pair=${code}`, '#pair=bad']) {
  const env = environment(`http://localhost:9281/${fragment}`);
  const result = env.storage.extractAndClearPairToken();
  assert.equal(env.window.location.hash, '');
  assert.equal(Boolean(result.token), fragment.endsWith(code));
}

const empty = environment();
empty.client(null).start();
assert.equal(empty.states.at(-1), 'unpaired');
assert.equal(empty.sockets.length, 0);

const success = environment();
const paired = success.client(code);
paired.start();
const socket = success.sockets[0];
socket.open();
assert.equal(socket.requests[0].method, 'session.pair');
assert.equal(socket.requests[0].params.token, code);
socket.respond({ sessionToken: session });
await flush();
assert.equal(success.storage.getSessionToken(), session);
assert.equal(socket.requests.at(-1).method, 'kanban.getBoard');
socket.respond(board);
await flush();
assert.equal(success.states.at(-1), 'connected');
assert.equal(success.boards.length, 1);
const staleMessage = socket.onmessage;
socket.close();
assert.equal(success.storage.getSessionToken(), session);
paired.dispose();
staleMessage({ currentTarget: socket, data: JSON.stringify({ jsonrpc: '2.0', method: 'kanban.taskUpdated', params: { ...board, revision: 100 } }) });
assert.equal(success.boards.length, 1);
assert.equal(success.timers.size, 0);

const canceled = environment();
const cancelClient = canceled.client(code);
cancelClient.start();
canceled.sockets[0].open();
canceled.sockets[0].respond({ sessionToken: session });
cancelClient.dispose();
await flush();
assert.equal(canceled.storage.getSessionToken(), null);
assert.ok(!canceled.states.includes('connected'));
assert.equal(canceled.timers.size, 0);

for (const errorCode of [-32001, -32603]) {
  const env = environment();
  env.storage.setSessionToken(session);
  const rpc = env.client(null);
  rpc.start();
  env.sockets[0].open();
  assert.equal(env.sockets[0].requests[0].method, 'session.authenticate');
  env.sockets[0].respond(undefined, { code: errorCode, message: 'authentication response' });
  await flush();
  assert.equal(env.storage.getSessionToken(), errorCode === -32001 ? null : session);
  assert.equal(env.states.at(-1), errorCode === -32001 ? 'auth_failed' : 'reconnecting');
  rpc.dispose();
}

const boardFailure = environment();
boardFailure.storage.setSessionToken(session);
const boardClient = boardFailure.client(null);
boardClient.start();
boardFailure.sockets[0].open();
boardFailure.sockets[0].respond({ authenticated: true });
await flush();
boardFailure.sockets[0].respond(undefined, { code: -32602, message: 'invalid board request' });
await flush();
assert.equal(boardFailure.storage.getSessionToken(), session);
assert.equal(boardFailure.states.at(-1), 'reconnecting');
boardClient.dispose();
console.log('配对检查通过：输入与同源校验、fragment 清理、配对/认证、断网保留、取消竞态和迟到通知。');
