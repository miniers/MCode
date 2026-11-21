// 测试环境无 DOM 与 Jest：沿用 useCommandActivity.test.tsx 的做法，
// 临时替换 React hooks 并用 renderToStaticMarkup 驱动 useLiveLogStream 完整生命周期。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { ConnectionState } from '../types/contract';
import type { RemoteLogChunk, RemoteLogsEventNotificationParams, RemoteLogsSubscribeResult } from '../types/generated';
import { MAX_LOG_CHUNKS, useLiveLogStream, type UseLiveLogStreamResult } from './useLiveLogStream';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

const stateSlots: unknown[] = [];
const refSlots: { current: unknown }[] = [];
const callbackSlots: { fn: unknown; deps?: readonly unknown[] }[] = [];
const effectSlots: { deps?: readonly unknown[]; cleanup?: () => void }[] = [];
let stateIndex = 0;
let refIndex = 0;
let callbackIndex = 0;
let effectIndex = 0;

function depsChanged(previous: readonly unknown[], next: readonly unknown[]): boolean {
  return previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
}

function fakeUseState<T>(initial: T | (() => T)): [T, (next: T | ((prev: T) => T)) => void] {
  const index = stateIndex++;
  if (!(index in stateSlots)) {
    stateSlots[index] = typeof initial === 'function' ? (initial as () => T)() : initial;
  }
  const setState = (next: T | ((prev: T) => T)) => {
    const prev = stateSlots[index] as T;
    stateSlots[index] = typeof next === 'function' ? (next as (p: T) => T)(prev) : next;
  };
  return [stateSlots[index] as T, setState];
}

function fakeUseRef<T>(initial: T): { current: T } {
  const index = refIndex++;
  if (!(index in refSlots)) {
    refSlots[index] = { current: initial };
  }
  return refSlots[index] as { current: T };
}

function fakeUseCallback<T>(fn: T, deps?: readonly unknown[]): T {
  const index = callbackIndex++;
  const slot = callbackSlots[index];
  if (slot && deps && slot.deps && !depsChanged(slot.deps, deps)) {
    return slot.fn as T;
  }
  callbackSlots[index] = { fn, deps };
  return fn;
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

const callLog: Array<{ method: string; params: unknown }> = [];
const orderLog: string[] = [];
const notificationListeners = new Map<string, Set<(params: unknown) => void>>();

let connectionState: ConnectionState = 'connected';
let subscribeSnapshot: RemoteLogsSubscribeResult = {
  threadID: 'thread-1',
  sequence: 1,
  streams: [
    {
      streamID: 'stream-1',
      source: 'tool',
      title: 'npm test',
      status: 'running',
      startedAt: '2026-11-21T00:00:00Z'
    }
  ],
  chunks: [
    {
      seq: 1,
      streamID: 'stream-1',
      source: 'tool',
      text: 'line 1\n',
      ts: '2026-11-21T00:00:01Z'
    }
  ],
  truncated: false
};

async function callRemote<T>(method: string, params?: unknown): Promise<T> {
  callLog.push({ method, params });
  orderLog.push(`call:${method}`);
  if (method === 'logs.subscribe') {
    return subscribeSnapshot as T;
  }
  if (method === 'logs.unsubscribe') {
    return { unsubscribed: true } as T;
  }
  throw new Error(`未预期的 RPC 方法: ${method}`);
}

function onRemoteNotification(method: string, handler: (params: unknown) => void): () => void {
  orderLog.push(`on:${method}`);
  let set = notificationListeners.get(method);
  if (!set) {
    set = new Set();
    notificationListeners.set(method, set);
  }
  set.add(handler);
  return () => {
    orderLog.push(`off:${method}`);
    set?.delete(handler);
  };
}

function emitLogsEvent(params: RemoteLogsEventNotificationParams): void {
  const handlers = notificationListeners.get('logs.event');
  if (!handlers) return;
  for (const handler of handlers) {
    handler(params);
  }
}

const originalUseState = React.useState;
const originalUseRef = React.useRef;
const originalUseCallback = React.useCallback;
const originalUseEffect = React.useEffect;
const originalUseContext = React.useContext;

Object.assign(React, {
  useState: fakeUseState,
  useRef: fakeUseRef,
  useCallback: fakeUseCallback,
  useEffect: fakeUseEffect,
  useContext: () => ({
    callRemote,
    supportsMethod: (method: string) => method === 'logs.subscribe' || method === 'logs.unsubscribe',
    onRemoteNotification,
    connectionState
  })
});

let threadID: string | null = 'thread-1';
let enabled = true;
let latest: UseLiveLogStreamResult | null = null;

function Probe(): null {
  latest = useLiveLogStream(threadID, enabled);
  return null;
}

function render(): UseLiveLogStreamResult {
  stateIndex = 0;
  refIndex = 0;
  callbackIndex = 0;
  effectIndex = 0;
  renderToStaticMarkup(<Probe />);
  if (!latest) throw new Error('Probe 未返回结果');
  return latest;
}

async function flush(): Promise<void> {
  for (let i = 0; i < 6; i += 1) await Promise.resolve();
}

try {
  // 1. 首次订阅：先注册 logs.event 再调用 logs.subscribe 拉取快照
  render();
  await flush();
  const initial = render();

  assertEqual(orderLog[0], 'on:logs.event', '先注册 logs.event 监听');
  assertEqual(orderLog[1], 'call:logs.subscribe', '再调用 logs.subscribe');
  assertEqual(initial.streams.length, 1, '初始 stream 数');
  assertEqual(initial.chunks.length, 1, '初始 chunk 数');
  assertEqual(initial.chunks[0]?.text, 'line 1\n', '初始 chunk 内容');
  assertEqual(initial.supported, true, '能力支持状态');

  // 2. logs.event 增量推送：去重 seq <= 1，追加 seq = 2，并合并更新 stream 状态
  emitLogsEvent({
    threadID: 'thread-1',
    sequence: 2,
    stream: {
      streamID: 'stream-1',
      source: 'tool',
      title: 'npm test',
      status: 'done',
      startedAt: '2026-11-21T00:00:00Z',
      finishedAt: '2026-11-21T00:00:02Z'
    },
    chunks: [
      { seq: 1, streamID: 'stream-1', source: 'tool', text: 'dup\n', ts: '2026-11-21T00:00:01Z' },
      { seq: 2, streamID: 'stream-1', source: 'tool', text: 'line 2\n', ts: '2026-11-21T00:00:02Z' }
    ]
  });
  const afterEvent = render();
  assertEqual(afterEvent.chunks.length, 2, '去重后 chunk 数');
  assertEqual(afterEvent.chunks[1]?.text, 'line 2\n', '新增 chunk 内容');
  assertEqual(afterEvent.streams[0]?.status, 'done', '更新后的 stream 状态');

  // 3. 忽略非当前会话的 logs.event
  emitLogsEvent({
    threadID: 'thread-other',
    sequence: 3,
    chunks: [{ seq: 3, streamID: 'stream-other', source: 'script', text: 'ignored\n', ts: '2026-11-21T00:00:03Z' }]
  });
  assertEqual(render().chunks.length, 2, '忽略其他线程通知后的 chunk 数');

  // 4. clearLocal() 只清空本地视图，且后续到达的旧 seq 不回放、新 seq 继续追加
  afterEvent.clearLocal();
  const afterClear = render();
  assertEqual(afterClear.chunks.length, 0, '清空后的 chunk 数');
  assertEqual(afterClear.streams.length, 1, '清空后保留的 stream 数');

  emitLogsEvent({
    threadID: 'thread-1',
    sequence: 3,
    chunks: [
      { seq: 2, streamID: 'stream-1', source: 'tool', text: 'old\n', ts: '2026-11-21T00:00:02Z' },
      { seq: 3, streamID: 'stream-1', source: 'tool', text: 'line 3\n', ts: '2026-11-21T00:00:03Z' }
    ]
  });
  const afterNewChunk = render();
  assertEqual(afterNewChunk.chunks.length, 1, '清空后追加新 seq 的 chunk 数');
  assertEqual(afterNewChunk.chunks[0]?.text, 'line 3\n', '清空后追加的新 chunk 文本');

  // 5. 有界缓冲：超过 1000 条截断保留最新 1000 条
  const burst: RemoteLogChunk[] = Array.from({ length: MAX_LOG_CHUNKS + 5 }, (_, idx) => ({
    seq: 10 + idx,
    streamID: 'stream-1',
    source: 'tool',
    text: `burst-${idx}\n`,
    ts: '2026-11-21T00:00:04Z'
  }));
  emitLogsEvent({
    threadID: 'thread-1',
    sequence: 10 + MAX_LOG_CHUNKS + 4,
    chunks: burst
  });
  const afterBurst = render();
  assertEqual(afterBurst.chunks.length, MAX_LOG_CHUNKS, '有界缓冲上限');
  assertEqual(afterBurst.truncated, true, '超出有界缓冲后的截断标记');

  // 6. 断线重连后自动重新 subscribe
  connectionState = 'reconnecting';
  render();
  assertEqual(callLog.some((item) => item.method === 'logs.unsubscribe'), true, '断线时清理退订');
  assertEqual(notificationListeners.get('logs.event')?.size ?? 0, 0, '断线时注销通知监听');

  const subCallsBeforeReconnect = callLog.filter((item) => item.method === 'logs.subscribe').length;
  connectionState = 'connected';
  render();
  await flush();
  render();
  const subCallsAfterReconnect = callLog.filter((item) => item.method === 'logs.subscribe').length;
  assertEqual(subCallsAfterReconnect, subCallsBeforeReconnect + 1, '重连回到 connected 后重新订阅');

  // 7. 关闭面板（enabled = false）：注销通知并调用 logs.unsubscribe
  const unsubCallsBeforeDisable = callLog.filter((item) => item.method === 'logs.unsubscribe').length;
  enabled = false;
  render();
  const unsubCallsAfterDisable = callLog.filter((item) => item.method === 'logs.unsubscribe').length;
  assertEqual(unsubCallsAfterDisable, unsubCallsBeforeDisable + 1, '关闭时调用 logs.unsubscribe');
  assertEqual(notificationListeners.get('logs.event')?.size ?? 0, 0, '关闭后 logs.event 监听应清空');
} finally {
  Object.assign(React, {
    useState: originalUseState,
    useRef: originalUseRef,
    useCallback: originalUseCallback,
    useEffect: originalUseEffect,
    useContext: originalUseContext
  });
}
