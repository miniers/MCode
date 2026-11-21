// 无框架测试：web/ 没有测试运行器与 DOM；先用原生 renderToStaticMarkup 验证 Provider 行为与向后兼容，
// 再沿用 useCommandActivity.test.tsx 的做法在可控 hook 槽位下重渲染 RemoteProvider，验证高频变更时引用稳定性。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteThreadSummary, TaskItem } from '../types/contract';
import {
  RemoteProvider,
  useRemote,
  useRemoteActions,
  useRemoteMeta,
  type RemoteActionsContextType,
  type RemoteMetaContextType
} from './RemoteContext';
import type { RemoteContextType } from './remoteTypes';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}（期望为 ${String(expected)}，实际为 ${String(actual)}）`);
  }
}

function assertThrows(fn: () => void, expectedFragment: string, message: string): void {
  let caught: unknown = null;
  try {
    fn();
  } catch (error) {
    caught = error;
  }
  assert(caught instanceof Error, `${message}（未抛出错误）`);
  assert(
    caught.message.includes(expectedFragment),
    `${message}（错误信息为：${caught.message}）`
  );
}

function makeThread(id: string, title: string): RemoteThreadSummary {
  return {
    id,
    projectID: 'proj-1',
    title,
    provider: 'claude',
    model: null,
    effort: null,
    isRunning: true,
    isArchived: false,
    isPinned: false,
    isDormant: false,
    hasUnread: false,
    attention: '',
    lastActivityText: null,
    section: 'active',
    orderKey: '0',
    updatedAt: '2026-11-21T00:00:00Z',
    lastStatus: null,
    hasPendingRequest: false,
    taskID: null,
    goalID: null,
    runtimeMode: 'supervised',
    interactionMode: 'build',
    fastMode: false,
    worktreeRequested: false,
    swarmEnabled: false,
    swarmPairID: null,
    swarmBeesProvider: null,
    swarmBeesModel: null,
    swarmBeesEffort: null
  };
}

function makeTask(id: string, title: string): TaskItem {
  return {
    id,
    projectID: 'proj-1',
    threadID: null,
    reviewThreadID: null,
    goalID: null,
    dependsOn: [],
    title,
    notes: '',
    status: 'in_progress',
    priority: 'medium',
    updatedAt: '2026-11-21T00:00:00Z',
    blocked: false,
    reviewSummary: null,
    reviewFeedback: null,
    reviewStatus: null,
    canStart: false,
    canMove: true,
    canReview: false,
    canApprove: false,
    attentionFlags: [],
    displayColumn: 'in_progress',
    lastActivityText: null,
    attemptCount: 0,
    lastFailureReason: null,
    revision: 1
  };
}

// 1. Provider 外部调用应报错，防止静默退化。
function OutsideMetaProbe(): null {
  useRemoteMeta();
  return null;
}

function OutsideActionsProbe(): null {
  useRemoteActions();
  return null;
}

assertThrows(
  () => renderToStaticMarkup(<OutsideMetaProbe />),
  'useRemoteMeta 必须在 RemoteProvider 内部使用',
  '在 RemoteProvider 外调用 useRemoteMeta 应抛错'
);
assertThrows(
  () => renderToStaticMarkup(<OutsideActionsProbe />),
  'useRemoteActions 必须在 RemoteProvider 内部使用',
  '在 RemoteProvider 外调用 useRemoteActions 应抛错'
);

// 2. 原生 renderToStaticMarkup：验证 useRemoteMeta / useRemoteActions 与 useRemote 100% 向后兼容。
let initialMeta: RemoteMetaContextType | null = null;
let initialActions: RemoteActionsContextType | null = null;
let initialRemote: RemoteContextType | null = null;

function InitialSnapshotProbe(): React.ReactElement {
  initialMeta = useRemoteMeta();
  initialActions = useRemoteActions();
  initialRemote = useRemote();
  return <span>{initialMeta.connectionState}</span>;
}

const initialMarkup = renderToStaticMarkup(
  <RemoteProvider>
    <InitialSnapshotProbe />
  </RemoteProvider>
);
assert(initialMarkup === '<span>connecting</span>', `期望渲染 connecting，实际为 ${initialMarkup}`);
assert(initialMeta !== null && initialActions !== null && initialRemote !== null, '应在 Provider 内取到全部上下文');

const capturedMeta: RemoteMetaContextType = initialMeta;
const capturedActions: RemoteActionsContextType = initialActions;
const capturedRemote: RemoteContextType = initialRemote;

assert(capturedMeta.connectionState === 'connecting', '默认连接状态应为 connecting');
assert(Array.isArray(capturedMeta.providers), 'providers 应为数组');
assert(Array.isArray(capturedMeta.projects), 'projects 应为数组');
assert(capturedMeta.capabilities === null, '初始 capabilities 应为 null');
assert(capturedMeta.selectedProjectID === null, '初始 selectedProjectID 应为 null');

for (const key of Object.keys(capturedMeta) as Array<keyof RemoteMetaContextType>) {
  assert(
    Object.is(capturedRemote[key], capturedMeta[key]),
    `useRemote().${key} 应与 useRemoteMeta().${key} 保持一致`
  );
}
for (const key of Object.keys(capturedActions) as Array<keyof RemoteActionsContextType>) {
  assert(
    typeof capturedActions[key] === 'function',
    `useRemoteActions().${key} 应为函数`
  );
  assert(
    Object.is(capturedRemote[key], capturedActions[key]),
    `useRemote().${key} 应与 useRemoteActions().${key} 保持同一引用`
  );
}

// 3. 受控重渲染：验证高频状态（如 currentThread / inspectedTask）变更时，
//    useRemoteMeta / useRemoteActions 返回稳定引用，避免低频订阅组件无效重渲染。
function depsChanged(previous: readonly unknown[], next: readonly unknown[]): boolean {
  return previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
}

const stateSlots: unknown[] = [];
const setterSlots: Array<(next: unknown) => void> = [];
const refSlots: Array<{ current: unknown }> = [];
const memoSlots: Array<{ value: unknown; deps?: readonly unknown[] }> = [];
let stateIndex = 0;
let refIndex = 0;
let memoIndex = 0;

function fakeUseState<T>(initial: T | (() => T)): [T, React.Dispatch<React.SetStateAction<T>>] {
  const index = stateIndex++;
  if (!(index in stateSlots)) {
    stateSlots[index] = typeof initial === 'function' ? (initial as () => T)() : initial;
    setterSlots[index] = (next: unknown) => {
      stateSlots[index] = typeof next === 'function'
        ? (next as (prev: unknown) => unknown)(stateSlots[index])
        : next;
    };
  }
  return [stateSlots[index] as T, setterSlots[index] as React.Dispatch<React.SetStateAction<T>>];
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

function fakeUseEffect(): void {}

const originalUseState = React.useState;
const originalUseRef = React.useRef;
const originalUseMemo = React.useMemo;
const originalUseCallback = React.useCallback;
const originalUseEffect = React.useEffect;

Object.assign(React, {
  useState: fakeUseState,
  useRef: fakeUseRef,
  useMemo: fakeUseMemo,
  useCallback: fakeUseCallback,
  useEffect: fakeUseEffect
});

try {
  let latestMeta: RemoteMetaContextType | null = null;
  let latestActions: RemoteActionsContextType | null = null;
  let latestRemote: RemoteContextType | null = null;
  const counts = { meta: 0, actions: 0, full: 0 };

  function MetaSubscriber(): null {
    const meta = useRemoteMeta();
    if (meta !== latestMeta) {
      counts.meta += 1;
      latestMeta = meta;
    }
    return null;
  }

  function ActionsSubscriber(): null {
    const actions = useRemoteActions();
    if (actions !== latestActions) {
      counts.actions += 1;
      latestActions = actions;
    }
    return null;
  }

  function FullSubscriber(): null {
    const remote = useRemote();
    if (remote !== latestRemote) {
      counts.full += 1;
      latestRemote = remote;
    }
    return null;
  }

  const rerender = () => {
    stateIndex = 0;
    refIndex = 0;
    memoIndex = 0;
    renderToStaticMarkup(
      <RemoteProvider>
        <MetaSubscriber />
        <ActionsSubscriber />
        <FullSubscriber />
      </RemoteProvider>
    );
  };

  rerender();
  assertEqual(counts.meta, 1, '首次渲染应触发 MetaSubscriber');
  assertEqual(counts.actions, 1, '首次渲染应触发 ActionsSubscriber');
  assertEqual(counts.full, 1, '首次渲染应触发 FullSubscriber');
  const firstMetaRef = latestMeta;
  const firstActionsRef = latestActions;
  const firstRemoteRef = latestRemote;
  assert(firstActionsRef !== null, '首次渲染应拿到 actions');

  // 模拟高频变更 1：流式期间切换/刷新当前线程与检视任务
  (firstActionsRef as RemoteActionsContextType).setCurrentThread(makeThread('thread-1', '流式输出 #1'));
  (firstActionsRef as RemoteActionsContextType).setInspectedTask(makeTask('task-1', '任务 #1'));
  rerender();

  assertEqual(counts.full, 2, '高频状态变化时 useRemote 订阅方应更新');
  assert(latestRemote !== firstRemoteRef, '高频状态变化时 useRemote 返回对象引用应变化');
  assertEqual((latestRemote as RemoteContextType).currentThread?.id, 'thread-1', 'currentThread 应更新为 thread-1');
  assertEqual((latestRemote as RemoteContextType).inspectedTask?.id, 'task-1', 'inspectedTask 应更新为 task-1');
  assert(latestMeta === firstMetaRef, '高频状态变化时 useRemoteMeta 引用必须保持不变');
  assert(latestActions === firstActionsRef, '高频状态变化时 useRemoteActions 引用必须保持不变');
  assertEqual(counts.meta, 1, '高频状态变化时 MetaSubscriber 不应产生无效重渲染');
  assertEqual(counts.actions, 1, '高频状态变化时 ActionsSubscriber 不应产生无效重渲染');

  // 模拟高频变更 2：连续流式文本块触发 currentThread 摘要更新
  (firstActionsRef as RemoteActionsContextType).setCurrentThread(makeThread('thread-1', '流式输出 #2'));
  rerender();

  assertEqual(counts.full, 3, '连续高频变化时 useRemote 订阅方应再次更新');
  assert(latestMeta === firstMetaRef, '连续高频变化时 useRemoteMeta 引用仍应保持不变');
  assert(latestActions === firstActionsRef, '连续高频变化时 useRemoteActions 引用仍应保持不变');
  assertEqual(counts.meta, 1, '连续高频变化时 MetaSubscriber 仍不应重渲染');
  assertEqual(counts.actions, 1, '连续高频变化时 ActionsSubscriber 仍不应重渲染');

  // 低频元信息变更：切换 selectedProjectID 与 theme 时，useRemoteMeta 应更新而 useRemoteActions 保持稳定
  (firstActionsRef as RemoteActionsContextType).setSelectedProjectID('proj-2');
  (firstActionsRef as RemoteActionsContextType).setTheme('dark');
  rerender();

  assertEqual(counts.meta, 2, '低频 meta 字段变化时 MetaSubscriber 应更新');
  assert(latestMeta !== firstMetaRef, '低频 meta 字段变化时 useRemoteMeta 应返回新引用');
  assertEqual((latestMeta as RemoteMetaContextType).selectedProjectID, 'proj-2', 'selectedProjectID 应更新为 proj-2');
  assertEqual((latestMeta as RemoteMetaContextType).theme, 'dark', 'theme 应更新为 dark');
  assert(latestActions === firstActionsRef, '低频 meta 字段变化时 useRemoteActions 仍应保持稳定引用');
  assertEqual(counts.actions, 1, '低频 meta 字段变化时 ActionsSubscriber 不应重渲染');
} finally {
  Object.assign(React, {
    useState: originalUseState,
    useRef: originalUseRef,
    useMemo: originalUseMemo,
    useCallback: originalUseCallback,
    useEffect: originalUseEffect
  });
}
