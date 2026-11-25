// 使用 react-dom/server 与 React hook 桩校验看稿层在 SSR 与交互状态流转下的核心契约，不引入额外测试框架。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteSwarmBee } from '../types/swarm';
import {
  DesignPreviewOverlay,
  type DesignFinalizeOptions,
  type DesignPreviewOverlayProps
} from './DesignPreviewOverlay';

function assertIncludes(haystack: string, needle: string, label: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`${label}：期望包含 ${needle}，实际为 ${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string, label: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`${label}：期望不包含 ${needle}，实际为 ${haystack}`);
  }
}

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label}：期望为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

const baseBee: RemoteSwarmBee = {
  threadID: 'bee-1',
  index: 0,
  name: 'Design Bee',
  task: '设计页面',
  kind: 'native',
  origin: 'delegated',
  status: 'completed',
  toolCalls: 3,
  tokens: 1200,
  startedAt: '2026-01-01T00:00:00Z',
  canOpen: true,
  canStop: false,
  designFiles: ['.mcode/design/home.html']
};

const noopAsync = async () => {};
const noopPreview = async () => '/p/token/home.html';

// 1. iframe sandbox 必须正好是 allow-scripts，不含 allow-same-origin；非 awaitingFinalize 时不出现定稿按钮
const normalHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: false }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(normalHtml, 'sandbox="allow-scripts"', 'iframe sandbox 属性');
assertExcludes(normalHtml, 'allow-same-origin', '禁止 allow-same-origin');
assertIncludes(normalHtml, 'home', '去掉目录与 .html 的标题');
assertExcludes(normalHtml, '>定稿<', '非待定稿态不渲染定稿按钮');
assertExcludes(normalHtml, 'role="tablist"', '单份稿件不渲染切换标签');

// 2. awaitingFinalize 为真时显示定稿按钮
const awaitingHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: true }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(awaitingHtml, '>定稿<', '待定稿态渲染定稿按钮');

// 3. 多份稿子时出现切换标签
const multiHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, designFiles: ['.mcode/design/home.html', '.mcode/design/detail.html'] }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(multiHtml, 'role="tablist"', '多份稿件出现 tablist');
assertIncludes(multiHtml, 'role="tab"', '多份稿件出现 tab');
assertIncludes(multiHtml, 'detail', '多份稿件显示第二个文件名');

// 4. finalizeRequested 为 true 时：运行中显示「定稿中…」，非运行显示不可点击的「✓ 已定稿」标记
const requestedRunningHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: true, finalizeRequested: true, status: 'running' }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(requestedRunningHtml, '定稿中…', '已定稿且运行中显示定稿中加载态');
assertExcludes(requestedRunningHtml, '>定稿<', '已定稿态不渲染定稿按钮');
assertExcludes(requestedRunningHtml, '✓ 已定稿', '运行中不渲染已定稿标记');

const requestedDoneHtml = renderToStaticMarkup(
  <DesignPreviewOverlay
    threadID="bee-1"
    bee={{ ...baseBee, awaitingFinalize: true, finalizeRequested: true, status: 'completed' }}
    activePath=".mcode/design/home.html"
    onSelectFile={() => {}}
    onClose={() => {}}
    openThreadPreview={noopPreview}
    onSendFeedback={noopAsync}
    onFinalizeDesign={noopAsync}
  />
);

assertIncludes(requestedDoneHtml, '✓ 已定稿', '已定稿且非运行显示已定稿标记');

// 确认「✓ 已定稿」渲染为不可点击的 span 而非 button
if (/<button[^>]*>[^<]*✓ 已定稿/.test(requestedDoneHtml)) {
  throw new Error(`✓ 已定稿不应为 button，实际为 ${requestedDoneHtml}`);
}

// 5. 驱动 hooks 与消息协议：只接受来自 iframe 的消息、胶囊文字、定稿传出 choices 与 note、finalizeRequested 发送 lock
function findElements(
  node: React.ReactNode,
  predicate: (el: React.ReactElement) => boolean,
  acc: React.ReactElement[] = []
): React.ReactElement[] {
  if (node === null || node === undefined || typeof node === 'boolean' || typeof node === 'string' || typeof node === 'number') {
    return acc;
  }
  if (Array.isArray(node)) {
    for (const item of node) findElements(item, predicate, acc);
    return acc;
  }
  if (React.isValidElement(node)) {
    if (predicate(node)) acc.push(node);
    const props = node.props as { children?: React.ReactNode } | undefined;
    if (props?.children !== undefined) {
      findElements(props.children, predicate, acc);
    }
  }
  return acc;
}

function collectText(node: React.ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(collectText).join('');
  if (React.isValidElement(node)) {
    const props = node.props as { children?: React.ReactNode } | undefined;
    return collectText(props?.children);
  }
  return '';
}

const stateSlots: unknown[] = [];
const refSlots: { current: unknown }[] = [];
const memoSlots: { value: unknown; deps?: readonly unknown[] }[] = [];
const effectSlots: { deps?: readonly unknown[]; cleanup?: () => void }[] = [];
let stateIndex = 0;
let refIndex = 0;
let memoIndex = 0;
let effectIndex = 0;

function depsChanged(previous: readonly unknown[], next: readonly unknown[]): boolean {
  return previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
}

function fakeUseState<T>(initial: T | (() => T)): [T, React.Dispatch<React.SetStateAction<T>>] {
  const index = stateIndex++;
  if (!(index in stateSlots)) {
    stateSlots[index] = typeof initial === 'function' ? (initial as () => T)() : initial;
  }
  const setState: React.Dispatch<React.SetStateAction<T>> = (next) => {
    const prev = stateSlots[index] as T;
    stateSlots[index] = typeof next === 'function' ? (next as (p: T) => T)(prev) : next;
  };
  return [stateSlots[index] as T, setState];
}

function fakeUseRef<T>(initial: T): React.MutableRefObject<T> {
  const index = refIndex++;
  if (!(index in refSlots)) {
    refSlots[index] = { current: initial };
  }
  return refSlots[index] as React.MutableRefObject<T>;
}

function fakeUseMemo<T>(factory: () => T, deps?: readonly unknown[]): T {
  const index = memoIndex++;
  const slot = memoSlots[index];
  if (slot && deps && slot.deps && !depsChanged(slot.deps, deps)) {
    return slot.value as T;
  }
  const value = factory();
  memoSlots[index] = { value, deps };
  return value;
}

function fakeUseCallback<T extends (...args: never[]) => unknown>(fn: T, deps: readonly unknown[]): T {
  return fakeUseMemo(() => fn, deps);
}

function fakeUseEffect(callback: () => void | (() => void), deps?: readonly unknown[]): void {
  const index = effectIndex++;
  const slot = effectSlots[index] ?? (effectSlots[index] = {});
  if (deps && slot.deps && !depsChanged(slot.deps, deps)) return;
  slot.cleanup?.();
  const cleanup = callback();
  slot.cleanup = typeof cleanup === 'function' ? cleanup : undefined;
  slot.deps = deps;
}

async function flushMicrotasks(): Promise<void> {
  for (let i = 0; i < 6; i += 1) await Promise.resolve();
}

const originalUseState = React.useState;
const originalUseRef = React.useRef;
const originalUseMemo = React.useMemo;
const originalUseCallback = React.useCallback;
const originalUseEffect = React.useEffect;
const originalAddEventListener = window.addEventListener;
const originalRemoveEventListener = window.removeEventListener;
const originalMatchMedia = window.matchMedia;

const windowListeners = new Map<string, Set<(event: unknown) => void>>();
const postedToIframe: Array<{ msg: unknown; origin: string }> = [];
const fakeIframeWindow = {
  postMessage(msg: unknown, origin: string) {
    postedToIframe.push({ msg, origin });
  }
};
const otherWindow = { postMessage() {} };
const fakeIframeEl = { contentWindow: fakeIframeWindow };

Object.assign(React, {
  useState: fakeUseState,
  useRef: fakeUseRef,
  useMemo: fakeUseMemo,
  useCallback: fakeUseCallback,
  useEffect: fakeUseEffect
});

window.addEventListener = ((type: string, listener: (event: unknown) => void) => {
  let set = windowListeners.get(type);
  if (!set) {
    set = new Set();
    windowListeners.set(type, set);
  }
  set.add(listener);
}) as typeof window.addEventListener;

window.removeEventListener = ((type: string, listener: (event: unknown) => void) => {
  windowListeners.get(type)?.delete(listener);
}) as typeof window.removeEventListener;

window.matchMedia = ((query: string) => ({
  matches: query.includes('prefers-reduced-motion'),
  media: query,
  addEventListener() {},
  removeEventListener() {}
})) as unknown as typeof window.matchMedia;

function emitWindowMessage(source: unknown, data: unknown): void {
  const handlers = windowListeners.get('message');
  if (!handlers) return;
  for (const h of handlers) {
    h({ source, data });
  }
}

try {
  let finalizedPayload: DesignFinalizeOptions | undefined;
  let currentProps: DesignPreviewOverlayProps = {
    threadID: 'bee-1',
    bee: { ...baseBee, awaitingFinalize: true, finalizeRequested: false, status: 'completed' },
    activePath: '.mcode/design/home.html',
    onSelectFile: () => {},
    onClose: () => {},
    openThreadPreview: noopPreview,
    onSendFeedback: noopAsync,
    onFinalizeDesign: async (options) => {
      finalizedPayload = options;
    }
  };

  const renderOverlay = (): React.ReactElement => {
    stateIndex = 0;
    refIndex = 0;
    memoIndex = 0;
    effectIndex = 0;
    const tree = DesignPreviewOverlay(currentProps) as React.ReactElement;
    const iframes = findElements(tree, (el) => el.type === 'iframe');
    if (iframes[0]) {
      const refObj = (iframes[0] as unknown as { ref?: { current: unknown } }).ref;
      if (refObj && typeof refObj === 'object') {
        refObj.current = fakeIframeEl;
      }
    }
    return tree;
  };

  renderOverlay();
  await flushMicrotasks();
  let tree = renderOverlay();

  // 非 iframe.contentWindow 与非 mcode-pick 消息必须被忽略
  emitWindowMessage(otherWindow, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 0,
      changed: 1,
      locked: false,
      choices: [{ question: '布局', option: 'B', recommended: false, options: ['A', 'B'] }]
    }
  });
  emitWindowMessage(fakeIframeWindow, {
    source: 'other-source',
    type: 'state',
    state: {
      version: 0,
      changed: 1,
      locked: false,
      choices: [{ question: '布局', option: 'B', recommended: false, options: ['A', 'B'] }]
    }
  });
  tree = renderOverlay();
  assertEqual(
    findElements(tree, (el) => el.type === 'button' && (el.props as { 'aria-haspopup'?: string })['aria-haspopup'] === 'dialog').length,
    0,
    '非目标来源消息不显示进度胶囊'
  );

  // 来自 iframe 的首个 state：发送 reduceMotion，胶囊显示「3 项推荐」
  emitWindowMessage(fakeIframeWindow, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 0,
      changed: 0,
      locked: false,
      choices: [
        { question: '列表行数', option: 'A', recommended: true, options: ['A', 'B', 'C'] },
        { question: '侧栏位置', option: 'A', recommended: true, options: ['A', 'B'] },
        { question: '按钮时机', option: 'B', recommended: true, options: ['A', 'B'] }
      ]
    }
  });
  assertEqual(postedToIframe.length, 1, '收到首个 state 后发送 reduceMotion');
  assertEqual((postedToIframe[0].msg as { type?: string }).type, 'reduceMotion', 'reduceMotion 消息类型');
  assertEqual((postedToIframe[0].msg as { value?: boolean }).value, true, 'reduceMotion 按媒体查询发送');
  assertEqual(postedToIframe[0].origin, '*', 'postMessage targetOrigin 为 *');

  tree = renderOverlay();
  const initialCapsule = findElements(
    tree,
    (el) => el.type === 'button' && (el.props as { 'aria-haspopup'?: string })['aria-haspopup'] === 'dialog'
  )[0];
  assertIncludes(collectText(initialCapsule), '3 项推荐', '全推荐时胶囊文字');

  // 改选 1 题后胶囊显示「已改 1/3」
  emitWindowMessage(fakeIframeWindow, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 1,
      changed: 1,
      locked: false,
      choices: [
        { question: '列表行数', option: 'B', recommended: false, options: ['A', 'B', 'C'] },
        { question: '侧栏位置', option: 'A', recommended: true, options: ['A', 'B'] },
        { question: '按钮时机', option: 'B', recommended: true, options: ['A', 'B'] }
      ]
    }
  });
  tree = renderOverlay();
  const changedCapsule = findElements(
    tree,
    (el) => el.type === 'button' && (el.props as { 'aria-haspopup'?: string })['aria-haspopup'] === 'dialog'
  )[0];
  assertIncludes(collectText(changedCapsule), '已改 1/3', '改选后胶囊文字');

  // 输入意见并定稿：传出 choices 与 note，成功后清空输入框
  const textarea = findElements(tree, (el) => el.type === 'textarea')[0];
  (textarea.props as { onChange: (e: { target: { value: string } }) => void }).onChange({
    target: { value: '请把按钮调大' }
  });
  tree = renderOverlay();
  const finalizeBtn = findElements(
    tree,
    (el) => el.type === 'button' && collectText(el) === '定稿'
  )[0];
  (finalizeBtn.props as { onClick: () => void }).onClick();
  await flushMicrotasks();

  assertEqual(finalizedPayload?.note, '请把按钮调大', '定稿传出 note');
  assertEqual(finalizedPayload?.choices?.length, 3, '定稿传出 choices 数量');
  assertEqual(finalizedPayload?.choices?.[0]?.question, '列表行数', 'choices 第一题题干');
  assertEqual(finalizedPayload?.choices?.[0]?.option, 'B', 'choices 第一题选值');
  assertEqual(finalizedPayload?.choices?.[0]?.recommended, false, 'choices 第一题 recommended');

  tree = renderOverlay();
  const clearedTextarea = findElements(tree, (el) => el.type === 'textarea')[0];
  assertEqual((clearedTextarea.props as { value: string }).value, '', '定稿成功后清空输入框');

  // finalizeRequested 为 true 时：隐藏胶囊、显示「✓ 已定稿」并发送 lock；收到 locked:false 新 state 时重发 lock
  postedToIframe.length = 0;
  currentProps = {
    ...currentProps,
    bee: { ...baseBee, awaitingFinalize: false, finalizeRequested: true, status: 'completed' }
  };
  tree = renderOverlay();

  assertEqual(
    findElements(tree, (el) => el.type === 'button' && (el.props as { 'aria-haspopup'?: string })['aria-haspopup'] === 'dialog').length,
    0,
    'finalizeRequested 时不显示进度胶囊'
  );
  assertIncludes(collectText(tree), '✓ 已定稿', 'finalizeRequested 且非运行显示已定稿');
  assertEqual(postedToIframe.length, 1, 'finalizeRequested 立即发送 lock');
  assertEqual((postedToIframe[0].msg as { type?: string }).type, 'lock', 'lock 消息类型');
  assertEqual((postedToIframe[0].msg as { value?: boolean }).value, true, 'lock value 为 true');
  assertEqual(postedToIframe[0].origin, '*', 'lock targetOrigin 为 *');

  emitWindowMessage(fakeIframeWindow, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 0,
      changed: 0,
      locked: false,
      choices: [{ question: '列表行数', option: 'A', recommended: true, options: ['A', 'B', 'C'] }]
    }
  });
  assertEqual(postedToIframe.length, 2, '刷新后收到 locked:false 新 state 时重发 lock');
  assertEqual((postedToIframe[1].msg as { type?: string }).type, 'lock', '重发消息为 lock');

  emitWindowMessage(fakeIframeWindow, {
    source: 'mcode-pick',
    type: 'state',
    state: {
      version: 0,
      changed: 0,
      locked: true,
      choices: [{ question: '列表行数', option: 'A', recommended: true, options: ['A', 'B', 'C'] }]
    }
  });
  assertEqual(postedToIframe.length, 2, '已 locked:true 时不再重发 lock');
} finally {
  for (const slot of effectSlots) {
    slot.cleanup?.();
  }
  Object.assign(React, {
    useState: originalUseState,
    useRef: originalUseRef,
    useMemo: originalUseMemo,
    useCallback: originalUseCallback,
    useEffect: originalUseEffect
  });
  window.addEventListener = originalAddEventListener;
  window.removeEventListener = originalRemoveEventListener;
  window.matchMedia = originalMatchMedia;
}
