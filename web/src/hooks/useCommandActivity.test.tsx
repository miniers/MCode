// 现有测试环境没有 DOM 与测试框架：沿用 BackgroundTasksRow.test.tsx 的做法，
// 临时替换 React 的 useState/useEffect/useContext，手动重渲染，覆盖轮询与清空时序。
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import type { CommandActivityState } from '../utils/commandActivity';
import { useCommandActivity } from './useCommandActivity';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

let states: Record<string, CommandActivityState> = {};
let effectIndex = 0;
const effectSlots: { deps?: readonly unknown[]; cleanup?: () => void }[] = [];

function setStates(next: unknown): void {
  states = typeof next === 'function'
    ? (next as (previous: Record<string, CommandActivityState>) => Record<string, CommandActivityState>)(states)
    : (next as Record<string, CommandActivityState>);
}

function depsChanged(previous: readonly unknown[], next: readonly unknown[]): boolean {
  return previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
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

const calls: string[] = [];
const process = {
  pid: 1,
  ppid: 0,
  startTime: new Date().toISOString(),
  cpuTimeMs: 100,
  rss: 1024,
  command: 'node server.js'
};

async function callRemote<T>(method: string, params?: unknown): Promise<T> {
  calls.push(method);
  const threadID = (params as { threadID?: string } | undefined)?.threadID ?? '';
  return (threadID === 'thread-1'
    ? { commands: [{ messageID: 'cmd-1', processes: [process] }] }
    : { commands: [] }) as T;
}

const fakeContext = {
  callRemote,
  supportsMethod: (method: string) => method === 'threads.commandActivity',
  onRemoteNotification: () => () => {}
};

const originalUseState = React.useState;
const originalUseEffect = React.useEffect;
const originalUseContext = React.useContext;
Object.assign(React, {
  useState: () => [states, setStates],
  useEffect: fakeUseEffect,
  useContext: () => fakeContext
});

const originalDocument = (globalThis as { document?: unknown }).document;
const originalSetInterval = globalThis.setInterval;
const originalClearInterval = globalThis.clearInterval;
Object.assign(globalThis, {
  document: { visibilityState: 'visible', addEventListener: () => {}, removeEventListener: () => {} },
  setInterval: () => 0,
  clearInterval: () => {}
});

let threadID = 'thread-1';
let enabled = true;

function Probe(): null {
  useCommandActivity(threadID, enabled);
  return null;
}

function render(): void {
  effectIndex = 0;
  renderToStaticMarkup(<Probe />);
}

async function flush(): Promise<void> {
  for (let index = 0; index < 4; index += 1) await Promise.resolve();
}

try {
  render();
  await flush();
  assertEqual(calls[0], 'threads.commandActivity', '轮询方法');
  assertEqual(states['cmd-1']?.history.length, 1, '首次采样后的走势点数');

  // 收起后台任务再展开：历史走势保留，不被清空。
  enabled = false;
  render();
  assertEqual(states['cmd-1']?.history.length, 1, '收起后保留的走势点数');

  enabled = true;
  render();
  assertEqual(states['cmd-1']?.history.length, 1, '展开后立即保留的走势点数');
  await flush();
  assertEqual(states['cmd-1']?.history.length, 2, '展开后继续采样的走势点数');

  // 切换线程：旧线程的走势清空，新线程没有数据时保持为空。
  threadID = 'thread-2';
  render();
  assertEqual(Object.keys(states).length, 0, '切换线程后清空的状态数');
  await flush();
  assertEqual(Object.keys(states).length, 0, '新线程无数据时的状态数');
} finally {
  Object.assign(React, { useState: originalUseState, useEffect: originalUseEffect, useContext: originalUseContext });
  Object.assign(globalThis, {
    document: originalDocument,
    setInterval: originalSetInterval,
    clearInterval: originalClearInterval
  });
}
