import React from 'react';
import { useRemoteThreadSubscriptions } from './remoteThreadSubscription';
import { McodeRpcClient } from '../services/rpc';
import type {
  RemoteThreadStreamSnapshot,
  RemoteThreadSummary,
  ThreadsEventParams,
  ThreadsSubscribeParams,
  ThreadsSubscribeResult
} from '../types/contract';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

function assertEqual<T>(actual: T, expected: T, message: string): void {
  if (!Object.is(actual, expected)) {
    throw new Error(`${message}（期望为 ${String(expected)}，实际为 ${String(actual)}）`);
  }
}

const threadID = '00000000-0000-0000-0000-000000000001';

function makeSummary(title = 'Thread'): RemoteThreadSummary {
  return {
    id: threadID,
    projectID: '00000000-0000-0000-0000-000000000002',
    title,
    provider: 'claude',
    model: null,
    effort: null,
    isRunning: false,
    isArchived: false,
    isPinned: false,
    isDormant: false,
    hasUnread: false,
    attention: '',
    lastActivityText: null,
    section: 'inbox',
    orderKey: 'a',
    updatedAt: '2026-11-26T00:00:00Z',
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

function makeSnapshot(runID: string, sequence: number, title = 'Snap'): RemoteThreadStreamSnapshot {
  return {
    threadID,
    runID,
    sequence,
    thread: makeSummary(title),
    history: {
      threadID,
      messages: [],
      nextCursor: null,
      hasMore: false,
      truncated: false
    }
  };
}

function makeProjectionEvent(runID: string, sequence: number, text: string): ThreadsEventParams {
  return {
    threadID,
    runID,
    sequence,
    kind: 'projection',
    payload: {
      thread: makeSummary(`Step ${sequence}`),
      messages: [
        {
          id: `msg-${sequence}`,
          role: 'assistant',
          text,
          createdAt: '2026-11-26T00:00:01Z',
          turnIndex: 1
        }
      ],
      removedMessageIDs: []
    }
  };
}

// 1. 验证 McodeRpcClient.subscribeThread 参数序列化：带 runID 时传递 afterSequence（含 0）
{
  const win = (globalThis as unknown as { window?: { addEventListener?: () => void } }).window;
  if (win && !win.addEventListener) win.addEventListener = () => {};
  const sent: Array<{ method: string; params: Record<string, unknown> }> = [];
  const rpcClient = new McodeRpcClient(
    {
      onConnectionChange: () => {},
      onBoardUpdated: () => {},
      onAuthFailed: () => {},
      onCredentialsChanged: () => {}
    },
    null
  );
  (rpcClient as unknown as { sendRpc: (method: string, params: Record<string, unknown>) => Promise<ThreadsSubscribeResult> }).sendRpc =
    async (method, params) => {
      sent.push({ method, params });
      return { threadID, subscribed: true };
    };

  await rpcClient.subscribeThread({ threadID });
  await rpcClient.subscribeThread({ threadID, runID: 'run-1', afterSequence: 0 });

  assert(sent.length === 2, '应发送两次 threads.subscribe');
  assert(!('runID' in sent[0].params) && !('afterSequence' in sent[0].params), '首次订阅不应带 runID 或 afterSequence');
  assert(
    sent[1].params.runID === 'run-1' && sent[1].params.afterSequence === 0,
    '重连订阅应完整传递 runID 与 afterSequence=0'
  );
}

// 2. 验证 useRemoteThreadSubscriptions 的重连 replay、空 replay 与 snapshot 回退
const refSlots: Array<{ current: unknown }> = [];
const memoSlots: Array<{ value: unknown; deps?: readonly unknown[] }> = [];
let refIndex = 0;
let memoIndex = 0;

function depsChanged(previous: readonly unknown[], next: readonly unknown[]): boolean {
  return previous.length !== next.length || previous.some((value, index) => !Object.is(value, next[index]));
}

const originalUseRef = React.useRef;
const originalUseCallback = React.useCallback;

Object.assign(React, {
  useRef: <T,>(initial: T) => {
    const index = refIndex++;
    if (!(index in refSlots)) refSlots[index] = { current: initial };
    return refSlots[index] as React.MutableRefObject<T>;
  },
  useCallback: <T extends (...args: never[]) => unknown>(fn: T, deps: readonly unknown[]) => {
    const index = memoIndex++;
    const slot = memoSlots[index];
    if (slot && !depsChanged(slot.deps ?? [], deps)) return slot.value as T;
    memoSlots[index] = { value: fn, deps };
    return fn;
  }
});

try {
  const subscribeCalls: ThreadsSubscribeParams[] = [];
  const nextResponses: ThreadsSubscribeResult[] = [];
  const fakeClient = {
    isReady: true,
    subscribeThread: async (params: ThreadsSubscribeParams): Promise<ThreadsSubscribeResult> => {
      subscribeCalls.push(params);
      const next = nextResponses.shift();
      if (!next) throw new Error('missing mock response');
      return next;
    },
    unsubscribeThread: async () => ({ threadID, subscribed: false })
  } as unknown as McodeRpcClient;

  const clientRef = { current: fakeClient as McodeRpcClient | null };
  const publishedTitles: string[] = [];
  const hook = useRemoteThreadSubscriptions({
    clientRef,
    publishThreadSummary: (summary) => {
      publishedTitles.push(summary.title);
    }
  });

  const snapshots: RemoteThreadStreamSnapshot[] = [];
  const events: ThreadsEventParams[] = [];
  const states: string[] = [];
  const errors: Error[] = [];

  const handlers = {
    onSnapshot: (s: RemoteThreadStreamSnapshot) => snapshots.push(s),
    onEvent: (e: ThreadsEventParams) => events.push(e),
    onState: (s: 'subscribing' | 'subscribed' | 'disconnected' | 'error') => states.push(s),
    onError: (err: Error) => errors.push(err)
  };

  // 初次订阅：返回 snapshot (run-1, seq=1)
  nextResponses.push({
    threadID,
    subscribed: true,
    snapshot: makeSnapshot('run-1', 1)
  });
  const handle1 = hook.subscribeThread(threadID, handlers);
  await handle1.ready;

  assertEqual(errors.length, 0, `初始订阅不应出错: ${errors[0]?.message}`);
  assertEqual(snapshots.length, 1, '初始订阅应应用 snapshot');
  assertEqual(snapshots[0].sequence, 1, '初始订阅 snapshot sequence 应为 1');
  assert(subscribeCalls[0].runID === undefined, '初始订阅不应带 runID');

  // 收到 seq=2 实时事件
  hook.handleThreadEvent(fakeClient, makeProjectionEvent('run-1', 2, 'live-2'));
  assertEqual(events.length, 1, '应应用实时事件 seq=2');
  assertEqual(events[0].sequence, 2, '实时事件 sequence 应为 2');

  // 模拟断线并重连（同会话组件重新调用 subscribeThread 并传入断点水位）
  hook.markClientDisconnected(fakeClient);
  assert(states[states.length - 1] === 'disconnected', '断线后状态应变为 disconnected');

  nextResponses.push({
    threadID,
    subscribed: true,
    replay: [makeProjectionEvent('run-1', 3, 'replay-3'), makeProjectionEvent('run-1', 4, 'replay-4')]
  });
  const handle2 = hook.subscribeThread(threadID, handlers, { runID: 'run-1', afterSequence: 2 });
  await handle2.ready;

  assertEqual(errors.length, 0, `replay 重连不应出错: ${errors[0]?.message}`);
  assert(
    subscribeCalls[1].runID === 'run-1' && subscribeCalls[1].afterSequence === 2,
    '重连时应传入上次收到的 runID 与 afterSequence=2'
  );
  assertEqual(snapshots.length, 1, 'replay 命中时不应再触发 onSnapshot');
  assertEqual(events.length, 3, 'replay 应追平全部事件');
  assert(events[1].sequence === 3 && events[2].sequence === 4, 'replay 事件应按顺序经 onEvent 应用');
  assert(states[states.length - 1] === 'subscribed', 'replay 应用后状态应回到 subscribed');

  // 断线期间无新事件：服务端省略 replay 且不带 snapshot
  const sub = hook.activeSubscriptionRef.current!;
  hook.markClientDisconnected(fakeClient);
  nextResponses.push({
    threadID,
    subscribed: true
  });
  hook.requestResubscribe(sub);
  await new Promise((resolve) => setTimeout(resolve, 10));

  assertEqual(errors.length, 0, `空 replay 重连不应出错: ${errors[0]?.message}`);
  assert(
    subscribeCalls[2].runID === 'run-1' && subscribeCalls[2].afterSequence === 4,
    '二次重连应以 seq=4 作为 afterSequence'
  );
  assertEqual(snapshots.length, 1, '空 replay 不应触发 snapshot');
  assertEqual(events.length, 3, '空 replay 不应触发多余事件');
  assert(states[states.length - 1] === 'subscribed', '空 replay 后状态应为 subscribed');

  // 服务端不可补发时回退返回 snapshot
  hook.markClientDisconnected(fakeClient);
  nextResponses.push({
    threadID,
    subscribed: true,
    snapshot: makeSnapshot('run-2', 0, 'Fresh Run')
  });
  hook.requestResubscribe(sub);
  await new Promise((resolve) => setTimeout(resolve, 10));

  assertEqual(snapshots.length, 2, '回退到 snapshot 时应应用新快照');
  assertEqual(snapshots[1].runID, 'run-2', '新快照 runID 应为 run-2');
  assert(publishedTitles.includes('Fresh Run'), '应发布快照中的最新线程摘要');
} finally {
  Object.assign(React, {
    useRef: originalUseRef,
    useCallback: originalUseCallback
  });
}
