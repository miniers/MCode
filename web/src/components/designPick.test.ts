// web/ 未接入 vitest 与 jsdom（测试由 scripts/run-tests.mjs 用 esbuild + node 执行），
// 这里用 node:vm 与轻量 DOM 桩验证 picker.js 的完整交互与 iframe 协议。
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import { strict as assert } from 'node:assert';
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:vm
import { createContext, runInContext } from 'node:vm';

const pickerScript = readFileSync(new URL('../../../internal/designpick/picker.js', import.meta.url), 'utf8');

function assertDeepEqual(actual: unknown, expected: unknown, message?: string): void {
  assert.deepEqual(JSON.parse(JSON.stringify(actual)), expected, message);
}

interface MockEvent {
  type: string;
  source?: unknown;
  data?: unknown;
  stopped: boolean;
  stopPropagation(): void;
}

class MockClassList {
  private set = new Set<string>();

  add(...names: string[]): void {
    for (const name of names) this.set.add(name);
  }

  remove(...names: string[]): void {
    for (const name of names) this.set.delete(name);
  }

  toggle(name: string, force?: boolean): boolean {
    if (force === undefined) {
      if (this.set.has(name)) {
        this.set.delete(name);
        return false;
      }
      this.set.add(name);
      return true;
    }
    if (force) {
      this.set.add(name);
      return true;
    }
    this.set.delete(name);
    return false;
  }

  contains(name: string): boolean {
    return this.set.has(name);
  }
}

class MockElement {
  tagName: string;
  id = '';
  type = '';
  innerHTML = '';
  textContent = '';
  style: Record<string, string> = {};
  classList = new MockClassList();
  children: MockElement[] = [];
  parentElement: MockElement | null = null;
  disabled = false;
  scrollCalls: Array<{ block?: string; behavior?: string }> = [];
  private attrs = new Map<string, string>();
  private listeners = new Map<string, Array<(e: MockEvent) => void>>();

  constructor(tagName: string, attrs?: Record<string, string>) {
    this.tagName = tagName.toUpperCase();
    if (attrs) {
      for (const [k, v] of Object.entries(attrs)) {
        this.attrs.set(k, v);
      }
    }
  }

  set className(val: string) {
    this.classList = new MockClassList();
    for (const part of val.split(/\s+/).filter(Boolean)) {
      this.classList.add(part);
    }
  }

  appendChild(child: MockElement): MockElement {
    child.parentElement = this;
    this.children.push(child);
    return child;
  }

  getAttribute(name: string): string | null {
    return this.attrs.get(name) ?? null;
  }

  setAttribute(name: string, value: string): void {
    this.attrs.set(name, String(value));
  }

  removeAttribute(name: string): void {
    this.attrs.delete(name);
  }

  hasAttribute(name: string): boolean {
    return this.attrs.has(name);
  }

  matches(selector: string): boolean {
    const attrMatch = /^\[([a-zA-Z0-9_-]+)\]$/.exec(selector);
    if (attrMatch) return this.hasAttribute(attrMatch[1]);
    if (selector.startsWith('.')) return this.classList.contains(selector.slice(1));
    return this.tagName === selector.toUpperCase();
  }

  closest(selector: string): MockElement | null {
    let cur: MockElement | null = this;
    while (cur) {
      if (cur.matches(selector)) return cur;
      cur = cur.parentElement;
    }
    return null;
  }

  querySelectorAll(selector: string): MockElement[] {
    const result: MockElement[] = [];
    const walk = (node: MockElement): void => {
      for (const child of node.children) {
        if (child.matches(selector)) {
          result.push(child);
        }
        walk(child);
      }
    };
    walk(this);
    return result;
  }

  querySelector(selector: string): MockElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  addEventListener(type: string, fn: (e: MockEvent) => void): void {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }

  click(): void {
    const event: MockEvent = {
      type: 'click',
      stopped: false,
      stopPropagation() {
        this.stopped = true;
      }
    };
    let cur: MockElement | null = this;
    while (cur) {
      for (const fn of cur.listeners.get('click') ?? []) {
        fn(event);
      }
      if (event.stopped) break;
      cur = cur.parentElement;
    }
  }

  scrollIntoView(options: { block?: string; behavior?: string }): void {
    this.scrollCalls.push(options);
  }
}

interface TimerTask {
  id: number;
  runAt: number;
  fn: () => void;
}

function createFakeTimers() {
  let now = 0;
  let nextId = 1;
  const tasks = new Map<number, TimerTask>();

  return {
    setTimeout(fn: () => void, delay = 0): number {
      const id = nextId++;
      tasks.set(id, { id, runAt: now + delay, fn });
      return id;
    },
    clearTimeout(id: number): void {
      tasks.delete(id);
    },
    advance(ms: number): void {
      const target = now + ms;
      while (true) {
        let nextTask: TimerTask | null = null;
        for (const task of tasks.values()) {
          if (task.runAt <= target && (!nextTask || task.runAt < nextTask.runAt)) {
            nextTask = task;
          }
        }
        if (!nextTask) break;
        tasks.delete(nextTask.id);
        now = nextTask.runAt;
        nextTask.fn();
      }
      now = target;
    }
  };
}

interface PickAPI {
  state(): {
    version: number;
    locked: boolean;
    changed: number;
    choices: Array<{
      question: string;
      option: string;
      recommended: boolean;
      options: string[];
    }>;
  };
  select(question: string, option: string): void;
  scrollTo(question: string): void;
  setReduceMotion(value: boolean): void;
  setLocked(value: boolean): void;
}

function createEnv(opts?: {
  withGroups?: boolean;
  iframeParent?: { postMessage(data: unknown, targetOrigin: string): void };
  prefersReducedMotion?: boolean;
}) {
  const timers = createFakeTimers();
  const root = new MockElement('html');
  const head = new MockElement('head');
  const body = new MockElement('body');
  root.appendChild(head);
  root.appendChild(body);

  let group1: MockElement | null = null;
  let group2: MockElement | null = null;
  let singleGroup: MockElement | null = null;
  let card1ClickCount = 0;

  if (opts?.withGroups !== false) {
    group1 = new MockElement('div', { 'data-mc-choice': '布局放在哪一边？' });
    const opt1A = new MockElement('div', { 'data-mc-option': 'A' });
    opt1A.addEventListener('click', () => {
      card1ClickCount += 1;
    });
    const opt1B = new MockElement('div', { 'data-mc-option': 'B', 'data-mc-recommended': '' });
    const opt1C = new MockElement('div', { 'data-mc-option': 'C' });
    group1.appendChild(opt1A);
    group1.appendChild(opt1B);
    group1.appendChild(opt1C);

    group2 = new MockElement('div', { 'data-mc-choice': '按钮何时显示？' });
    const opt2X = new MockElement('div', { 'data-mc-option': 'X' });
    const opt2Y = new MockElement('div', { 'data-mc-option': 'Y' });
    group2.appendChild(opt2X);
    group2.appendChild(opt2Y);

    singleGroup = new MockElement('div', { 'data-mc-choice': '只有一版的问题' });
    const optSingle = new MockElement('div', { 'data-mc-option': 'A' });
    singleGroup.appendChild(optSingle);

    body.appendChild(group1);
    body.appendChild(group2);
    body.appendChild(singleGroup);
  }

  const windowListeners = new Map<string, Array<(e: unknown) => void>>();

  const doc = {
    readyState: 'complete',
    documentElement: root,
    head,
    body,
    createElement(tag: string) {
      return new MockElement(tag);
    },
    getElementById(id: string) {
      const findById = (node: MockElement): MockElement | null => {
        if (node.id === id) return node;
        for (const c of node.children) {
          const hit = findById(c);
          if (hit) return hit;
        }
        return null;
      };
      return findById(root);
    },
    querySelectorAll(selector: string) {
      return root.querySelectorAll(selector);
    },
    addEventListener(_type: string, _fn: () => void) {}
  };

  const sandbox: Record<string, unknown> = {
    document: doc,
    setTimeout: timers.setTimeout,
    clearTimeout: timers.clearTimeout,
    getComputedStyle(el: MockElement) {
      return { position: el.style.position || 'static' };
    },
    matchMedia(query: string) {
      return {
        matches: query.includes('prefers-reduced-motion') && !!opts?.prefersReducedMotion
      };
    },
    addEventListener(type: string, fn: (e: unknown) => void) {
      const list = windowListeners.get(type) ?? [];
      list.push(fn);
      windowListeners.set(type, list);
    },
    dispatchWindowMessage(event: { source: unknown; data: unknown }) {
      for (const fn of windowListeners.get('message') ?? []) {
        fn(event);
      }
    }
  };
  sandbox.window = sandbox;

  Object.defineProperty(sandbox, 'parent', {
    configurable: true,
    enumerable: true,
    writable: true,
    value: opts?.iframeParent ?? sandbox
  });

  const ctx = createContext(sandbox);
  const run = () => runInContext(pickerScript, ctx);
  run();

  return {
    sandbox,
    doc,
    head,
    timers,
    group1,
    group2,
    singleGroup,
    getCard1ClickCount: () => card1ClickCount,
    rerun: run,
    pick: () => sandbox.__mcodePick as PickAPI
  };
}

// 1. 默认选中、忽略单方案组、样式与 DOM 注入
{
  const env = createEnv();
  const api = env.pick();
  const initial = api.state();

  assert.equal(initial.version, 0);
  assert.equal(initial.changed, 0);
  assertDeepEqual(initial.choices, [
    { question: '布局放在哪一边？', option: 'B', recommended: true, options: ['A', 'B', 'C'] },
    { question: '按钮何时显示？', option: 'X', recommended: false, options: ['X', 'Y'] }
  ]);

  const styleEl = env.doc.getElementById('mc-pick-style');
  if (!styleEl) throw new Error('应注入 style 标签');
  assert.ok(styleEl.textContent.includes('.mc-pick-btn'));
  assert.ok(styleEl.textContent.includes('var(--mc-accent, #0062CC)'));

  assert.equal(env.singleGroup?.querySelectorAll('.mc-pick-btn').length, 0, '单方案组不得注入圆钮');

  const group1Btns = env.group1!.querySelectorAll('.mc-pick-btn');
  assert.equal(group1Btns.length, 3);
  assert.equal(group1Btns[0].getAttribute('aria-label'), '选这版 A');
  assert.equal(group1Btns[0].getAttribute('aria-pressed'), 'false');
  assert.equal(group1Btns[1].getAttribute('aria-pressed'), 'true');
  assert.ok(group1Btns[1].innerHTML.includes('<svg'), '选中按钮应带对勾 SVG');
  assert.equal(group1Btns[0].innerHTML, '');

  const optA = env.group1!.children[0];
  const optB = env.group1!.children[1];
  assert.equal(optA.style.position, 'relative');
  assert.ok(optB.classList.contains('mc-pick-selected'));
  assert.ok(optA.classList.contains('mc-pick-dimmed'));

  // 2. 点击圆钮改选，stopPropagation 不触发外层点击，changed 与 version 递增
  group1Btns[0].click();
  assert.equal(env.getCard1ClickCount(), 0, '点击圆钮不得冒泡触发卡片自身点击逻辑');
  assert.equal(group1Btns[0].getAttribute('aria-pressed'), 'true');
  assert.equal(group1Btns[1].getAttribute('aria-pressed'), 'false');
  assert.ok(optA.classList.contains('mc-pick-selected'));
  assert.ok(optB.classList.contains('mc-pick-dimmed'));

  const afterClick = api.state();
  assert.equal(afterClick.version, 1);
  assert.equal(afterClick.changed, 1);
  assert.equal(afterClick.choices[0].option, 'A');
  assert.equal(afterClick.choices[0].recommended, false);

  // 3. select 无效参数或重复选当前项不变
  api.select('不存在的问题', 'A');
  api.select('布局放在哪一边？', 'Z');
  api.select('布局放在哪一边？', 'A');
  assert.equal(api.state().version, 1);
  assert.equal(api.state().changed, 1);

  // 4. scrollTo 加闪烁类并在推进 600ms 假时钟后移除；reduceMotion 时滚动不用 smooth
  api.scrollTo('按钮何时显示？');
  assertDeepEqual(env.group2!.scrollCalls[0], { block: 'center', behavior: 'smooth' });
  assert.ok(env.group2!.classList.contains('mc-pick-flash'));
  env.timers.advance(599);
  assert.ok(env.group2!.classList.contains('mc-pick-flash'));
  env.timers.advance(1);
  assert.equal(env.group2!.classList.contains('mc-pick-flash'), false);

  api.setReduceMotion(true);
  assert.ok(env.doc.documentElement.classList.contains('mc-pick-reduce-motion'));
  api.scrollTo('布局放在哪一边？');
  assertDeepEqual(env.group1!.scrollCalls[0], { block: 'center', behavior: 'auto' });

  // 5. 重复执行脚本不重复安装
  env.rerun();
  assert.equal(env.group1!.querySelectorAll('.mc-pick-btn').length, 3);
  assert.equal(api.state().version, 1);
  assert.equal(api.state().choices[0].option, 'A');
}

// 6. 没有有效组时不渲染 style，choices 为空
{
  const env = createEnv({ withGroups: false });
  assert.equal(env.doc.getElementById('mc-pick-style'), null);
  assertDeepEqual(env.pick().state(), { version: 0, locked: false, changed: 0, choices: [] });
}

// 7. iframe 场景下初始化与变化 postMessage，以及接收 parent message
{
  const posted: Array<{ data: unknown; origin: string }> = [];
  const fakeParent = {
    postMessage(data: unknown, origin: string) {
      posted.push({ data, origin });
    }
  };
  const env = createEnv({ iframeParent: fakeParent });

  assert.equal(posted.length, 1);
  assert.equal(posted[0].origin, '*');
  assertDeepEqual(posted[0].data, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 0,
      locked: false,
      changed: 0,
      choices: [
        { question: '布局放在哪一边？', option: 'B', recommended: true, options: ['A', 'B', 'C'] },
        { question: '按钮何时显示？', option: 'X', recommended: false, options: ['X', 'Y'] }
      ]
    }
  });

  const dispatch = env.sandbox.dispatchWindowMessage as (e: { source: unknown; data: unknown }) => void;

  // 非法来源忽略
  dispatch({ source: {}, data: { source: 'mcode-pick', type: 'select', question: '布局放在哪一边？', option: 'C' } });
  dispatch({ source: fakeParent, data: { source: 'other', type: 'select', question: '布局放在哪一边？', option: 'C' } });
  assert.equal(env.pick().state().version, 0);
  assert.equal(posted.length, 1);

  // 合法 select 消息
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'select', question: '布局放在哪一边？', option: 'C' } });
  assert.equal(env.pick().state().version, 1);
  assert.equal(env.pick().state().choices[0].option, 'C');
  assert.equal(posted.length, 2);
  assert.equal((posted[1].data as { state: { version: number } }).state.version, 1);

  // scrollTo / reduceMotion / getState 消息
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'reduceMotion', value: true } });
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'scrollTo', question: '布局放在哪一边？' } });
  assertDeepEqual(env.group1!.scrollCalls[0], { block: 'center', behavior: 'auto' });

  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'getState' } });
  assert.equal(posted.length, 3);

  // parent 消息 'lock' 锁定与解锁
  const group1Btns = env.group1!.querySelectorAll('.mc-pick-btn');
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'lock', value: true } });
  assert.equal(env.pick().state().locked, true);
  assert.equal(env.pick().state().version, 2);
  assert.equal(posted.length, 4);
  assert.equal((posted[3].data as { state: { locked: boolean; version: number } }).state.locked, true);
  assert.equal((posted[3].data as { state: { locked: boolean; version: number } }).state.version, 2);
  assert.equal(group1Btns[2].disabled, true);
  assert.equal(group1Btns[0].disabled, true);
  assert.ok(env.doc.documentElement.classList.contains('mc-pick-locked'));

  // 重复 lock 不递增 version、不回发 state
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'lock', value: true } });
  assert.equal(env.pick().state().version, 2);
  assert.equal(posted.length, 4);

  // 锁定后点击与 select 消息均无效
  group1Btns[0].click();
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'select', question: '布局放在哪一边？', option: 'A' } });
  assert.equal(env.pick().state().choices[0].option, 'C');
  assert.equal(env.pick().state().version, 2);
  assert.equal(posted.length, 4);

  // 解锁后回发 state，按钮恢复可用且可再选
  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'lock', value: false } });
  assert.equal(env.pick().state().locked, false);
  assert.equal(env.pick().state().version, 3);
  assert.equal(posted.length, 5);
  assert.equal((posted[4].data as { state: { locked: boolean; version: number } }).state.locked, false);
  assert.equal(group1Btns[2].disabled, false);
  assert.equal(env.doc.documentElement.classList.contains('mc-pick-locked'), false);

  dispatch({ source: fakeParent, data: { source: 'mcode-pick', type: 'select', question: '布局放在哪一边？', option: 'A' } });
  assert.equal(env.pick().state().choices[0].option, 'A');
  assert.equal(env.pick().state().version, 4);
  assert.equal(posted.length, 6);
}

// 8. 直接调用 setLocked 锁定、重复设置、禁用状态、无效选择与解锁
{
  const env = createEnv();
  const api = env.pick();
  const group1Btns = env.group1!.querySelectorAll('.mc-pick-btn');
  const styleEl = env.doc.getElementById('mc-pick-style');
  if (!styleEl) throw new Error('应注入 style 标签');
  assert.ok(styleEl.textContent.includes('.mc-pick-locked'));

  assert.equal(api.state().locked, false);
  assert.equal(api.state().version, 0);
  assert.equal(group1Btns[1].disabled, false);
  assert.equal(group1Btns[1].hasAttribute('disabled'), false);

  // 初始未锁定时设为 false 不增加 version
  api.setLocked(false);
  assert.equal(api.state().version, 0);

  // 锁定后状态、DOM disabled 与样式类
  api.setLocked(true);
  assert.equal(api.state().locked, true);
  assert.equal(api.state().version, 1);
  assert.ok(env.doc.documentElement.classList.contains('mc-pick-locked'));
  assert.equal(group1Btns[1].disabled, true);
  assert.equal(group1Btns[1].hasAttribute('disabled'), true);
  assert.equal(group1Btns[0].disabled, true);
  assert.ok(group1Btns[1].innerHTML.includes('<svg'));
  assert.ok(env.group1!.children[1].classList.contains('mc-pick-selected'));
  assert.ok(env.group1!.children[0].classList.contains('mc-pick-dimmed'));

  // 重复设为 true 不增加 version
  api.setLocked(true);
  assert.equal(api.state().version, 1);

  // 锁定后点击圆钮与直接调用 select 均无效，且不冒泡到卡片
  group1Btns[0].click();
  api.select('布局放在哪一边？', 'C');
  assert.equal(env.getCard1ClickCount(), 0);
  assert.equal(api.state().choices[0].option, 'B');
  assert.equal(api.state().changed, 0);
  assert.equal(api.state().version, 1);

  // 解锁后恢复并可再选
  api.setLocked(false);
  assert.equal(api.state().locked, false);
  assert.equal(api.state().version, 2);
  assert.equal(env.doc.documentElement.classList.contains('mc-pick-locked'), false);
  assert.equal(group1Btns[1].disabled, false);
  assert.equal(group1Btns[1].hasAttribute('disabled'), false);
  assert.equal(group1Btns[0].disabled, false);

  group1Btns[0].click();
  assert.equal(api.state().choices[0].option, 'A');
  assert.equal(api.state().changed, 1);
  assert.equal(api.state().version, 3);

  api.select('布局放在哪一边？', 'C');
  assert.equal(api.state().choices[0].option, 'C');
  assert.equal(api.state().version, 4);
}
