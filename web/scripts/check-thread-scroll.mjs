// Run with: node scripts/check-thread-scroll.mjs (npm ci and ego-browser required).
// Uses the real ThreadView with isolated mock data; no host RPC or user storage.
import { createServer } from 'vite';
import react from '@vitejs/plugin-react';
import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('..', import.meta.url));

const fixture = `
import React, { useState } from 'react';
import { createRoot } from 'react-dom/client';
import { ThreadView } from '/src/components/ThreadView.tsx';
import '/src/index.css';
const summaries = Object.fromEntries(['A', 'B'].map(id => [id, {
  id, projectID: 'project', title: id, provider: 'codex', model: null, effort: null,
  isRunning: false, isArchived: false, isPinned: false, isDormant: true,
  hasUnread: false, attention: 'none', lastActivityText: null, section: 'dormant',
  orderKey: id, updatedAt: '2026-10-06T00:00:00Z', lastStatus: null,
  hasPendingRequest: false, taskID: null, goalID: null, runtimeMode: 'supervised',
  interactionMode: 'build', fastMode: false, worktreeRequested: false, swarmEnabled: false, swarmPairID: null
}]));
const messages = (id, older = false) => Array.from({length: 50}, (_, i) => ({
  id: id + '-' + (older ? 'old-' : '') + i, role: i % 2 ? 'assistant' : 'user',
  text: id + '-' + (older ? 'OLD-' : '') + i + '\\n\\n' + '正文阅读内容 '.repeat(24),
  createdAt: new Date(Date.UTC(2026, 9, older ? 5 : 6, 0, i)).toISOString(),
  turnIndex: (older ? 0 : 50) + i, kind: i % 2 ? 'assistant' : 'user',
  isFinalResponse: Boolean(i % 2)
}));
const history = (id, older = false) => ({threadID: id, messages: messages(id, older),
  nextCursor: older ? null : 'old', hasMore: !older, truncated: false});
const handlers = new Map(), readyAt = new Map(), streamReady = new Set();
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const noop = async () => {};
const rig = window.rig = {moreDelay: 500, summaries, handlers,
  snapshot(id, empty = false) {
    streamReady.add(id);
    handlers.get(id)?.onSnapshot({threadID: id, thread: summaries[id],
      runID: id, sequence: 1, history: {...history(id), messages: empty ? [] : messages(id).slice(-25)}});
  },
  event(id, sequence = 2) {
    if (!streamReady.has(id)) rig.snapshot(id);
    handlers.get(id)?.onEvent({threadID: id, runID: id, sequence, kind: 'projection',
      payload: {thread: summaries[id], messages: [{...messages(id).at(-1),
        text: '流式消息 ' + '新增正文 '.repeat(sequence * 30)}], removedMessageIDs: []}});
  },
  scroller: () => document.querySelector('div.relative.flex-1.min-h-0.overflow-y-auto'),
  grow() {
    const el = rig.scroller()?.firstElementChild;
    if (el) { const child = document.createElement('div'); child.style.height = '700px'; el.appendChild(child); }
  },
  metrics() {
    const el = rig.scroller();
    return el ? {top: el.scrollTop, height: el.scrollHeight, client: el.clientHeight,
      gap: el.scrollHeight - el.scrollTop - el.clientHeight} : null;
  }
};
window.__scrollRemote = new Proxy({
  connectionState: 'connected', isSyncing: false,
  capabilities: {methods: ['threads.subscribe'], features: ['threads.events']},
  threads: Object.values(summaries), currentThread: null, threadActions: [],
  requestSnapshots: {}, queueSnapshots: {}, projects: [], providers: [],
  providersLoading: false, providersError: null, board: null,
  async getThreadHistory(id, cursor) {
    await delay(cursor ? rig.moreDelay : Math.max(0, (readyAt.get(id) || 0) - performance.now()));
    if (cursor) {
      const el = rig.scroller(), viewport = el.getBoundingClientRect();
      rig.anchor = Array.from(el.querySelectorAll('[data-index]')).find(node => {
        const bounds = node.getBoundingClientRect();
        return bounds.height > 0 && bounds.bottom > viewport.top && bounds.top < viewport.bottom;
      });
      rig.anchorLabel = rig.anchor.textContent.match(/A-(OLD-)?\\d+/)?.[0];
      rig.beforeOlderResult = {offset:rig.anchor.getBoundingClientRect().top-viewport.top,top:el.scrollTop};
    }
    return history(id, Boolean(cursor));
  },
  refreshThread: async id => summaries[id],
  subscribeThread(id, callbacks) {
    handlers.set(id, callbacks); streamReady.delete(id); callbacks.onState('subscribed');
    return {subscriptionID: id, ready: Promise.resolve()};
  },
  unsubscribeThread(id) {handlers.delete(id);}
}, {get: (target, key) => key in target ? target[key] : noop});
function App() {
  const [id, setID] = useState(null);
  rig.open = (next, firstDelay = 0) => {readyAt.set(next, performance.now() + firstDelay); setID(next);};
  rig.close = () => setID(null);
  return id ? React.createElement(ThreadView, {thread:summaries[id], onBack:rig.close}) : React.createElement('div', null, '测试会话列表');
}
document.documentElement.style.setProperty('--app-height', '844px');
createRoot(document.getElementById('root')).render(React.createElement(App));
`;
const server = await createServer({configFile: false, root,
  plugins: [react(), {
    name: 'scroll-fixture',
    resolveId(id) {if (id === '/__scroll_fixture.tsx') return '\0scroll-fixture.tsx';},
    transform(code, id) {
      if (id.endsWith('/src/context/RemoteContext.tsx')) return 'export const useRemote = () => window.__scrollRemote; export const useRemoteStatic = useRemote;';
    },
    enforce: 'pre',
    load(id) {
      if (id === '\0scroll-fixture.tsx') return fixture;
    },
    configureServer(server) {
      server.middlewares.use('/__scroll_check', async (_req, res) => {
        res.setHeader('Content-Type', 'text/html');
        res.end(await server.transformIndexHtml('/__scroll_check', '<html><bee><meta name="viewport" content="width=device-width,initial-scale=1"></bee><body><div id="root" style="height:844px;display:flex;flex-direction:column"></div><script type="module" src="/__scroll_fixture.tsx"></script></body></html>'));
      });
    }
  }], server: {host: '127.0.0.1', port: 0}});
await server.listen();
const url = 'http://127.0.0.1:' + server.httpServer.address().port + '/__scroll_check';

async function regression(url, space) {
  const assert = (await import('node:assert/strict')).default;
  const task = await taskSpace(space), page = task.page('p1');
  let checks = 0;
  const pass = label => { checks++; console.log('PASS ' + label); };
  const metrics = () => page.evaluate(() => window.rig.metrics());
  const fresh = async () => {
    await page.goto(url);
    await page.waitForFunction(() => Boolean(window.rig?.open));
  };
  const latest = async id => {
    await page.waitForFunction(id => {
      const el = window.rig.scroller();
      return el?.textContent.includes(id + '-49') && el.scrollHeight > 2000 &&
        el.scrollHeight - el.scrollTop - el.clientHeight < 2;
    }, id);
  };
  await page.cdp('Emulation.setDeviceMetricsOverride', {width:390,height:844,deviceScaleFactor:1,mobile:true});
  // The virtualized timeline relies on scroll events and ResizeObserver, which a background tab does not deliver.
  await page.cdp('Page.bringToFront');
  await page.cdp('Emulation.setFocusEmulationEnabled', {enabled:true});
  await fresh();
  await page.evaluate(() => window.rig.open('A'));
  await latest('A'); pass('first valid layout');
  await page.waitForTimeout(2200);
  await page.evaluate(() => window.rig.grow());
  await latest('A'); pass('content grows after 2.2 seconds');
  await page.waitForTimeout(3000);
  await page.evaluate(() => window.rig.grow());
  await latest('A'); pass('content grows after 5.2 seconds');
  await page.click('loc=css:button[aria-label="返回会话列表"]');
  await page.waitForFunction(() => !window.rig.scroller());
  await page.evaluate(() => window.rig.open('A'));
  await latest('A'); pass('return and reopen');
  for (const [id, ms] of [['B',1000],['A',2000]]) {
    const started = Date.now();
    await page.evaluate(({id,ms}) => window.rig.open(id,ms), {id,ms});
    await latest(id);
    assert.ok(Date.now() - started >= ms - 100);
    pass('switch to ' + id + ' with ' + ms + 'ms history');
  }
  await fresh();
  await page.evaluate(() => window.rig.open('A',2000));
  await page.waitForFunction(() => window.rig.handlers.has('A'));
  await page.evaluate(() => window.rig.snapshot('A'));
  await latest('A');
  await page.mouse.move(195,350); await page.mouse.wheel(0,-15000);
  await page.waitForFunction(() => window.rig.metrics().top < 100);
  await page.waitForTimeout(2100);
  assert.ok((await metrics()).gap > 80); pass('delayed history cannot revive cancelled pin');
  const top = (await metrics()).top;
  await page.evaluate(() => { window.rig.event('A'); window.rig.snapshot('A'); window.rig.grow(); });
  // The timeline is virtualized: far from the bottom the streamed message is not mounted.
  await page.waitForTimeout(1300);
  assert.ok((await metrics()).gap > 80 && Math.abs((await metrics()).top - top) < 2);
  pass('stream snapshot and resize preserve upward reading');

  await fresh(); await page.evaluate(() => window.rig.open('A')); await latest('A');
  await page.mouse.move(195,350); await page.mouse.wheel(0,-120);
  await page.waitForFunction(() => window.rig.metrics().gap > 80);
  await page.evaluate(() => { window.rig.event('A'); window.rig.snapshot('A'); window.rig.grow(); });
  await page.waitForFunction(() => document.body.textContent.includes('流式消息'));
  await page.waitForTimeout(1300);
  assert.ok((await metrics()).gap > 80); pass('upward scroll just over 80px pauses follow');

  await fresh(); await page.evaluate(() => window.rig.open('A')); await latest('A');
  await page.evaluate(() => {
    window.rig.scroller().dispatchEvent(new PointerEvent('pointerdown', {bubbles:true}));
    window.rig.grow();
  });
  await page.waitForTimeout(1300); await latest('A');
  await page.evaluate(() => window.rig.grow()); await latest('A');
  pass('stationary touch does not cancel pin');

  for (const viaButton of [false, true]) {
    await fresh(); await page.evaluate(() => window.rig.open('A')); await latest('A');
    await page.mouse.move(195,350); await page.mouse.wheel(0,-600);
    await page.waitForFunction(() => window.rig.metrics().gap > 80);
    await page.evaluate(() => window.rig.grow()); await page.waitForTimeout(600);
    assert.ok((await metrics()).gap > 80);
    if (viaButton) await page.click('loc=css:button[aria-label="跳到最新"]');
    else await page.mouse.wheel(0,15000);
    await page.waitForFunction(() => window.rig.metrics().gap < 24);
    await page.evaluate(() => { window.rig.event('A'); window.rig.snapshot('A'); window.rig.grow(); });
    await page.waitForFunction(() => document.body.textContent.includes('流式消息'));
    const atBottom = () => page.waitForFunction(() => window.rig.metrics().gap < 2);
    await page.waitForTimeout(600); await atBottom();
    await page.evaluate(() => window.rig.grow()); await atBottom();
    pass('returning to the bottom resumes follow ' + (viaButton ? 'via jump button' : 'by scrolling'));
  }

  for (const native of [false,true]) {
    await fresh(); await page.evaluate(() => window.rig.open('A')); await latest('A');
    await page.evaluate(enabled => {
      const el = window.rig.scroller();
      if (!enabled) el.style.overflowAnchor = 'none';
      window.rig.moreDelay = 3000;
      Array.from(el.querySelectorAll('button')).find(n => n.textContent.includes('加载更早的消息')).click();
      el.scrollTop = 4500;
    }, native);
    await page.waitForFunction(() => document.body.textContent.includes('正在加载更早的消息'));
    // The wheel must land while the page is still loading; a wheel after the load is the reader's own
    // scroll, which the anchor correction deliberately leaves alone.
    const beforeWheel = (await metrics()).top;
    await page.mouse.move(195,350); await page.mouse.wheel(0,-200);
    await page.waitForFunction(top => !window.rig.beforeOlderResult && Math.abs(window.rig.metrics().top - top) > 100, beforeWheel);
    await page.waitForFunction(() => Boolean(window.rig.beforeOlderResult) &&
      !document.body.textContent.includes('正在加载更早的消息'));
    await page.waitForTimeout(1000);
    // Virtualized rows are remounted when the range shifts, so the anchor is found again by its label.
    const anchor = await page.evaluate(() => {
      const node = Array.from(window.rig.scroller().querySelectorAll('[data-index]'))
        .find(n => n.textContent.match(/A-(OLD-)?\d+/)?.[0] === window.rig.anchorLabel);
      return {present:Boolean(node), label:window.rig.anchorLabel,
        delta:node ? node.getBoundingClientRect().top - window.rig.scroller().getBoundingClientRect().top -
          window.rig.beforeOlderResult.offset : null};
    });
    assert.ok(anchor.present && Math.abs(anchor.delta) < 2, JSON.stringify(anchor));
    await page.evaluate(() => { window.rig.event('A'); window.rig.snapshot('A'); window.rig.grow(); });
    await page.waitForTimeout(1300); assert.ok((await metrics()).gap > 80);
    pass('older page anchor and reading, native anchoring ' + native);
  }

  await fresh(); await page.evaluate(() => window.rig.open('A')); await latest('A');
  await page.evaluate(() => {window.rig.snapshot('A');window.rig.handlers.get('A').onEvent({threadID:'A',runID:'A',sequence:10,kind:'projection',
    payload:{thread:window.rig.summaries.A,removedMessageIDs:[],messages:[{id:'work',role:'tool',kind:'tool',
      turnID:'work-turn',turnIndex:100,turnStatus:'completed',createdAt:'2026-10-06T01:00:00Z',
      text:'工作过程\n'.repeat(300)}]}});});
  await page.waitForSelector('loc=css:button[data-work-record-id="work-turn"]');
  await page.click('loc=css:button[data-work-record-id="work-turn"]');
  await page.waitForFunction(() => document.querySelector('[data-work-record-id="work-turn"]')?.getAttribute('aria-expanded') === 'true');
  await page.evaluate(() => { window.rig.event('A',11); window.rig.grow(); });
  await page.waitForTimeout(1300); assert.ok((await metrics()).gap > 80);
  pass('expanded work record cancels pin');
  await task.finish({keep:[]});
  console.log('PASS ' + checks + ' scroll regression scenarios');
}

try {
  const space = process.argv[2] ? Number(process.argv[2]) : 'mcode scroll regression';
  await new Promise((resolve, reject) => {
    const child = execFile('ego-browser', ['nodejs'],
      {cwd:root,timeout:120000,maxBuffer:1024*1024}, error => error ? reject(error) : resolve());
    child.stdin.end(regression.toString() + '\nawait regression(' + JSON.stringify(url) + ',' + JSON.stringify(space) + ');\n');
    child.stdout.on('data', chunk => process.stdout.write(chunk));
    child.stderr.on('data', chunk => process.stderr.write(chunk));
  });
} catch (error) {
  process.stderr.write(JSON.stringify({code:error.code,signal:error.signal,killed:error.killed}) + '\n');
  process.exitCode = 1;
} finally {
  await server.close();
}
