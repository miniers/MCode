// 无框架测试：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { RemoteProvider } from '../context/RemoteContext';
import { DiffViewer, getRevertHunkRanges } from './DiffViewer';
import { parseFilesFromUnifiedDiff } from '../utils/diff';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertEqualJSON(actual: unknown, expected: unknown, what: string): void {
  const actualText = JSON.stringify(actual);
  const expectedText = JSON.stringify(expected);
  if (actualText !== expectedText) {
    throw new Error(`期望${what}为 ${expectedText}，实际为 ${actualText}`);
  }
}

const lf = String.fromCharCode(10);
const diffText = [
  'diff --git a/a.go b/a.go', '--- a/a.go', '+++ a/a.go', '@@ -1,5 +1,5 @@',
  ' one', '-two', '+TWO', ' three', '-four', '+FOUR', ' five'
].join(lf);

const file = parseFilesFromUnifiedDiff(diffText)[0];
assertEqualJSON(
  getRevertHunkRanges(file.lines).map(({ startLine, lineCount }) => [startLine, lineCount]),
  [[2, 1], [4, 1]],
  '分离改动的服务端行坐标'
);
const deletion = parseFilesFromUnifiedDiff([
  'diff --git a/a.go b/a.go', '--- a/a.go', '+++ a/a.go', '@@ -1 +0,0 @@', '-old'
].join(lf))[0];
assertEqualJSON(
  getRevertHunkRanges(deletion.lines).map(({ startLine, lineCount }) => [startLine, lineCount]),
  [[1, 0]],
  '文件开头删除的服务端行坐标'
);

const html = renderToStaticMarkup(
  <RemoteProvider>
    <DiffViewer diffText={diffText} />
  </RemoteProvider>
);
assertEqual(html.includes('aria-label="撤销此块"'), false, '未提供回调时是否渲染撤销按钮');
assertEqual(
  /<button\b[^>]*type="button"[^>]*aria-expanded="true"[^>]*>[\s\S]*?a\.go/.test(html),
  true,
  '文件标题应为带 aria-expanded 的 button'
);

// 后发请求先返回时保留后发结果：第一次 getDiff 晚于第二次返回，不覆盖后发结果。
{
  const stateSlots: unknown[] = [];
  const refSlots: { current: unknown }[] = [];
  const memoSlots: { value: unknown; deps?: readonly unknown[] }[] = [];
  const effectSlots: { deps?: readonly unknown[]; cleanup?: () => void }[] = [];
  let stateIndex = 0;
  let refIndex = 0;
  let memoIndex = 0;
  let effectIndex = 0;

  const depsChanged = (previous: readonly unknown[], next: readonly unknown[]): boolean =>
    previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));

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

  interface DeferredDiff {
    resolve: (res: { diff: string; truncated: boolean }) => void;
    promise: Promise<{ diff: string; truncated: boolean }>;
  }
  const deferredByTask = new Map<string, DeferredDiff>();
  const mockGetDiff = (taskID: string): Promise<{ diff: string; truncated: boolean }> => {
    let resolve!: (res: { diff: string; truncated: boolean }) => void;
    const promise = new Promise<{ diff: string; truncated: boolean }>((res) => {
      resolve = res;
    });
    deferredByTask.set(taskID, { resolve, promise });
    return promise;
  };

  const renderTaskDiff = (taskID: string): string => {
    stateIndex = 0;
    refIndex = 0;
    memoIndex = 0;
    effectIndex = 0;
    return renderToStaticMarkup(<DiffViewer taskID={taskID} />);
  };

  const flushMicrotasks = async (): Promise<void> => {
    for (let i = 0; i < 6; i += 1) await Promise.resolve();
  };

  const originalUseState = React.useState;
  const originalUseRef = React.useRef;
  const originalUseMemo = React.useMemo;
  const originalUseCallback = React.useCallback;
  const originalUseEffect = React.useEffect;
  const originalUseContext = React.useContext;

  Object.assign(React, {
    useState: fakeUseState,
    useRef: fakeUseRef,
    useMemo: fakeUseMemo,
    useCallback: fakeUseCallback,
    useEffect: fakeUseEffect,
    useContext: () => ({ getDiff: mockGetDiff })
  });

  try {
    renderTaskDiff('task-1');
    renderTaskDiff('task-2');

    const firstDiff = [
      'diff --git a/first.go b/first.go', '--- a/first.go', '+++ b/first.go', '@@ -1 +1 @@', '-old1', '+new1'
    ].join(lf);
    const secondDiff = [
      'diff --git a/second.go b/second.go', '--- a/second.go', '+++ b/second.go', '@@ -1 +1 @@', '-old2', '+new2'
    ].join(lf);

    deferredByTask.get('task-2')!.resolve({ diff: secondDiff, truncated: false });
    await flushMicrotasks();

    deferredByTask.get('task-1')!.resolve({ diff: firstDiff, truncated: false });
    await flushMicrotasks();

    const finalHtml = renderTaskDiff('task-2');
    assertEqual(finalHtml.includes('second.go'), true, '后发请求先返回时应展示后发结果');
    assertEqual(finalHtml.includes('first.go'), false, '先发晚返回的旧请求不得覆盖后发结果');
  } finally {
    Object.assign(React, {
      useState: originalUseState,
      useRef: originalUseRef,
      useMemo: originalUseMemo,
      useCallback: originalUseCallback,
      useEffect: originalUseEffect,
      useContext: originalUseContext
    });
  }
}
