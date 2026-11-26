import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { Dispatch, MutableRefObject, SetStateAction } from 'react';
import type { useRemote, RemoteThreadSubscriptionHandle, RemoteThreadSubscriptionHandlers } from '../../context/RemoteContext';
import { mergeThreadSummary } from '../../context/remoteGuards';
import type { RemoteForwardedContext, RemoteThreadProjectionPayload, RemoteThreadStreamSnapshot, RemoteThreadSummary } from '../../types/contract';
import { isEndedStatus, mergeLatestHistoryPage, mergeThreadMessages } from '../ThreadTimeline';
import type { ThreadMessage } from '../ThreadTimeline';
import { createFrameCoalescer } from '../../utils/frameCoalescer';

type Remote = ReturnType<typeof useRemote>;
type Ref<T> = MutableRefObject<T>;
type Setter<T> = Dispatch<SetStateAction<T>>;
const PAGE_SIZE = 50;
const POLL_INTERVAL_MS = 2500;

// 分开调用状态、历史和订阅 hook，保持 ThreadView 中跨块 effect 的注册顺序。
export function useThreadStream() {
  const [messages, setMessages] = useState<ThreadMessage[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [forwardedContext, setForwardedContext] = useState<RemoteForwardedContext | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [historyInitialDone, setHistoryInitialDone] = useState(false);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [streamState, setStreamState] = useState<'disabled' | 'subscribing' | 'subscribed' | 'disconnected' | 'error'>('disabled');
  const messagesRef = useRef<ThreadMessage[]>([]);
  const frameCoalescerRef = useRef<ReturnType<typeof createFrameCoalescer> | null>(null);
  if (frameCoalescerRef.current === null) {
    frameCoalescerRef.current = createFrameCoalescer(() => setMessages(messagesRef.current));
  }
  const frameCoalescer = frameCoalescerRef.current;
  const pendingHistoryScrollRef = useRef<{ scrollHeight: number; scrollTop: number; key: string | null; offset: number } | null>(null);
  const historySyncFrameRef = useRef(0);
  const lastFailedRequestRef = useRef<{ cursor: string | null; append: boolean } | null>(null);
  const requestIdRef = useRef(0);
  const loadingMoreRef = useRef(false);
  const streamStateRef = useRef(streamState);
  const streamThreadIDRef = useRef<string | null>(null);
  const streamRunIDRef = useRef<string | null>(null);
  const streamSequenceRef = useRef<number>(-1);
  const streamedMessageSequenceRef = useRef(new Map<string, number>());
  const removedStreamMessageIDsRef = useRef(new Set<string>());
  const subscriptionHandleRef = useRef<RemoteThreadSubscriptionHandle | null>(null);
  streamStateRef.current = streamState;
  return {
    messages, setMessages, nextCursor, setNextCursor, hasMore, setHasMore,
    truncated, setTruncated, forwardedContext, setForwardedContext,
    isLoading, setIsLoading, isLoadingMore, setIsLoadingMore,
    errorMessage, setErrorMessage, historyInitialDone, setHistoryInitialDone,
    streamError, setStreamError, streamState, setStreamState, messagesRef, frameCoalescer,
    pendingHistoryScrollRef, historySyncFrameRef, lastFailedRequestRef, requestIdRef,
    loadingMoreRef, streamStateRef, streamThreadIDRef, streamRunIDRef, streamSequenceRef,
    streamedMessageSequenceRef, removedStreamMessageIDsRef, subscriptionHandleRef
  };
}

type Stream = ReturnType<typeof useThreadStream>;

// 历史读取期间收到的增量和删除优先于旧历史；run 切换后当前投影全部受保护。
export function protectedHistoryMessageIDs(
  currentSequences: ReadonlyMap<string, number>,
  removedIDs: ReadonlySet<string>,
  requestSequences: ReadonlyMap<string, number>,
  requestRunID: string | null,
  currentRunID: string | null
): Set<string> {
  return new Set([
    ...[...currentSequences]
      .filter(([id, sequence]) => requestRunID !== currentRunID || requestSequences.get(id) !== sequence)
      .map(([id]) => id),
    ...removedIDs
  ]);
}


interface HistoryOptions {
  stream: Stream;
  thread: RemoteThreadSummary;
  endPin: () => void;
  getThreadHistory: Remote['getThreadHistory'];
  scrollRef: Ref<HTMLDivElement | null>;
  mountedRef: Ref<boolean>;
  setPollError: Setter<string | null>;
  setLatestSummary: Setter<RemoteThreadSummary>;
}

export function useThreadStreamHistory({ stream, thread, endPin, getThreadHistory, scrollRef, mountedRef, setPollError, setLatestSummary }: HistoryOptions) {
  const {
    messages, messagesRef, frameCoalescer, pendingHistoryScrollRef, historySyncFrameRef,
    lastFailedRequestRef, requestIdRef, loadingMoreRef, streamThreadIDRef, streamRunIDRef,
    streamSequenceRef, streamedMessageSequenceRef, removedStreamMessageIDsRef, setMessages,
    setNextCursor, setHasMore, setTruncated, setForwardedContext, setErrorMessage,
    setHistoryInitialDone, setIsLoading, setIsLoadingMore
  } = stream;
  const loadPage = useCallback(
    async (cursor: string | null, append: boolean) => {
      const requestId = ++requestIdRef.current;
      loadingMoreRef.current = append;
      if (append) {
        // 加载更早历史是用户主动操作，结束初始贴底，避免被拉回底部
        endPin();
        setIsLoadingMore(true);
      } else setIsLoading(true);

      try {
        const page = await getThreadHistory(thread.id, cursor, PAGE_SIZE);
        if (requestId !== requestIdRef.current) return;

        lastFailedRequestRef.current = null;
        const incoming = (Array.isArray(page.messages) ? page.messages : []) as ThreadMessage[];
        const protectedMessageIDs = new Set([
          ...streamedMessageSequenceRef.current.keys(),
          ...removedStreamMessageIDsRef.current
        ]);
        const merged = mergeThreadMessages(
          append ? messagesRef.current : messagesRef.current.filter((message) => protectedMessageIDs.has(message.id) && !removedStreamMessageIDsRef.current.has(message.id)),
          incoming.filter((message) => !protectedMessageIDs.has(message.id))
        );
        if (append && scrollRef.current) {
          const viewportTop = scrollRef.current.getBoundingClientRect().top;
          const viewportBottom = viewportTop + scrollRef.current.clientHeight;
          const anchor = Array.from(scrollRef.current.querySelectorAll<HTMLElement>('[data-item-key]')).find((element) => {
            const bounds = element.getBoundingClientRect();
            return bounds.height > 0 && bounds.bottom > viewportTop && bounds.top < viewportBottom;
          });
          pendingHistoryScrollRef.current = {
            scrollHeight: scrollRef.current.scrollHeight,
            scrollTop: scrollRef.current.scrollTop,
            key: anchor?.dataset.itemKey ?? null,
            offset: anchor ? anchor.getBoundingClientRect().top - viewportTop : 0
          };
        }
        messagesRef.current = merged;
        frameCoalescer.cancel();
        setMessages(merged);
        setNextCursor(page.nextCursor ?? null);
        setHasMore(Boolean(page.hasMore));
        setTruncated((prev) => (append ? prev || Boolean(page.truncated) : Boolean(page.truncated)));
        if (page.forwardedContext) {
          setForwardedContext(page.forwardedContext);
        } else if (!append && !page.hasMore) {
          setForwardedContext(null);
        }
        setErrorMessage(null);
        if (!append) setHistoryInitialDone(true);
      } catch (err) {
        if (requestId !== requestIdRef.current) return;
        lastFailedRequestRef.current = { cursor, append };
        setErrorMessage(err instanceof Error ? err.message : '加载会话历史失败');
        if (!append) setHistoryInitialDone(false);
      } finally {
        if (requestId === requestIdRef.current) {
          loadingMoreRef.current = false;
          setIsLoading(false);
          setIsLoadingMore(false);
        }
      }
    },
    [endPin, frameCoalescer, getThreadHistory, thread.id]
  );

  useLayoutEffect(() => {
    const pending = pendingHistoryScrollRef.current;
    pendingHistoryScrollRef.current = null;
    const container = scrollRef.current;
    if (!pending || !container) return;
    container.scrollTop = pending.scrollTop + (container.scrollHeight - pending.scrollHeight);
    if (!pending.key) return;
    // 虚拟列表对新插入的行先用估算高度，实测后总高度会变；接下来几帧按锚点行把阅读位置校正回来。
    // 循环不随 messages 变化取消：加载完成后紧跟着的流式更新不应打断校正。
    let frames = 40;
    window.clearTimeout(historySyncFrameRef.current);
    // 用户一旦开始滑动就停止校正，避免把手指拉回去。
    const stop = () => { frames = 0; };
    container.addEventListener('touchstart', stop, { passive: true, once: true });
    container.addEventListener('wheel', stop, { passive: true, once: true });
    const syncAnchor = () => {
      if (frames <= 0) return;
      const node = Array.from(container.querySelectorAll<HTMLElement>('[data-item-key]'))
        .find((element) => element.dataset.itemKey === pending.key);
      if (node) {
        const diff = node.getBoundingClientRect().top - container.getBoundingClientRect().top - pending.offset;
        if (Math.abs(diff) > 1) container.scrollTop += diff;
      }
      frames -= 1;
      if (frames > 0) {
        historySyncFrameRef.current = window.setTimeout(syncAnchor, 16);
      } else {
        historySyncFrameRef.current = 0;
        container.removeEventListener('touchstart', stop);
        container.removeEventListener('wheel', stop);
      }
    };
    historySyncFrameRef.current = window.setTimeout(syncAnchor, 16);
  }, [messages]);

  useEffect(() => () => window.clearTimeout(historySyncFrameRef.current), [thread.id]);

  const refreshLatestPage = useCallback(
    async (reportError = true): Promise<ThreadMessage[] | null> => {
      const requestId = requestIdRef.current;
      const requestRunID = streamRunIDRef.current;
      const messageSequences = new Map(streamedMessageSequenceRef.current);
      try {
        const page = await getThreadHistory(thread.id, null, PAGE_SIZE);
        if (requestId !== requestIdRef.current || !mountedRef.current) return null;
        const previous = messagesRef.current;
        const protectedMessageIDs = protectedHistoryMessageIDs(
          streamedMessageSequenceRef.current, removedStreamMessageIDsRef.current,
          messageSequences, requestRunID, streamRunIDRef.current
        );
        const { messages: merged, gap, shouldUpdateCursor } = mergeLatestHistoryPage(
          previous,
          page,
          protectedMessageIDs
        );
        messagesRef.current = merged;
        frameCoalescer.cancel();
        setMessages(merged);
        // 实时刷新不回退已经加载到更早历史的游标。
        if (shouldUpdateCursor) {
          setNextCursor(page.nextCursor ?? null);
          setHasMore(Boolean(page.hasMore));
        }
        setTruncated((previous) => previous || Boolean(page.truncated) || Boolean(gap));
        if (page.forwardedContext) {
          setForwardedContext(page.forwardedContext);
        }
        setHistoryInitialDone(true);
        setErrorMessage(null);
        lastFailedRequestRef.current = null;
        setPollError(null);
        return merged;
      } catch (err) {
        if (reportError && requestId === requestIdRef.current && mountedRef.current) {
          setPollError(err instanceof Error ? err.message : '刷新会话历史失败');
        }
        return null;
      }
    },
    [frameCoalescer, getThreadHistory, thread.id]
  );

  const applyStreamSnapshot = useCallback(
    (snapshot: RemoteThreadStreamSnapshot): void => {
      if (!mountedRef.current || snapshot.threadID !== thread.id) return;
      streamThreadIDRef.current = snapshot.threadID;
      if (streamRunIDRef.current !== snapshot.runID) {
        streamRunIDRef.current = snapshot.runID;
        streamSequenceRef.current = snapshot.sequence;
        streamedMessageSequenceRef.current.clear();
        removedStreamMessageIDsRef.current.clear();
      } else {
        streamSequenceRef.current = Math.max(streamSequenceRef.current, snapshot.sequence);
      }

      const protectedMessageIDs = new Set(
        [...streamedMessageSequenceRef.current]
          .filter(([, sequence]) => sequence > snapshot.sequence)
          .map(([id]) => id)
      );
      const previous = messagesRef.current;
      const { messages: merged, gap, shouldUpdateCursor } = mergeLatestHistoryPage(
        previous,
        snapshot.history,
        protectedMessageIDs
      );
      messagesRef.current = merged;
      frameCoalescer.cancel();
      setMessages(merged);

      for (const message of snapshot.history.messages) {
        const existingSequence = streamedMessageSequenceRef.current.get(message.id);
        if (existingSequence === undefined || existingSequence <= snapshot.sequence) {
          streamedMessageSequenceRef.current.set(message.id, snapshot.sequence);
          removedStreamMessageIDsRef.current.delete(message.id);
        }
      }
      if (shouldUpdateCursor) {
        setNextCursor(snapshot.history.nextCursor ?? null);
        setHasMore(Boolean(snapshot.history.hasMore));
      }
      setTruncated((previousValue) => previousValue || Boolean(snapshot.history.truncated) || Boolean(gap));
      if (snapshot.history.forwardedContext) {
        setForwardedContext(snapshot.history.forwardedContext);
      } else if (!snapshot.history.hasMore && shouldUpdateCursor) {
        setForwardedContext(null);
      }
      setHistoryInitialDone(true);
      setIsLoading(false);
      setErrorMessage(null);
      setPollError(null);
      setLatestSummary((previousValue) =>
        previousValue.id === snapshot.thread.id
          ? mergeThreadSummary(previousValue, snapshot.thread, false)
          : snapshot.thread
      );
    },
    [frameCoalescer, thread.id]
  );

  const applyStreamProjection = useCallback(
    (payload: RemoteThreadProjectionPayload, sequence: number, runID: string): void => {
      if (!mountedRef.current || streamRunIDRef.current !== runID) return;
      streamThreadIDRef.current = thread.id;
      streamSequenceRef.current = Math.max(streamSequenceRef.current, sequence);
      const removedIDs = new Set(payload.removedMessageIDs);
      for (const id of removedIDs) {
        streamedMessageSequenceRef.current.delete(id);
        removedStreamMessageIDsRef.current.add(id);
      }

      const additions = payload.messages.filter((message) => !removedIDs.has(message.id)) as ThreadMessage[];
      for (const message of additions) {
        streamedMessageSequenceRef.current.set(message.id, sequence);
        removedStreamMessageIDsRef.current.delete(message.id);
      }

      const merged = mergeThreadMessages(
        messagesRef.current.filter((message) => !removedIDs.has(message.id)),
        additions
      );
      messagesRef.current = merged;
      frameCoalescer.schedule();
      setLatestSummary((previous) =>
        previous.id === payload.thread.id
          ? mergeThreadSummary(previous, payload.thread, false)
          : payload.thread
      );
      setHistoryInitialDone(true);
      setIsLoading(false);
      setErrorMessage(null);
      setPollError(null);
    },
    [frameCoalescer]
  );

  const loadEarlierHistory = useCallback(
    () => loadPage(stream.nextCursor, true),
    [loadPage, stream.nextCursor]
  );
  return { loadPage, loadEarlierHistory, refreshLatestPage, applyStreamSnapshot, applyStreamProjection };
}

interface PollingOptions {
  stream: Stream;
  eventsSupported: boolean;
  isConnected: boolean;
  mountedRef: Ref<boolean>;
  isConnectedRef: Ref<boolean>;
  pollTimerRef: Ref<number | null>;
  pollGenerationRef: Ref<number>;
  pollInFlightRef: Ref<boolean>;
  pollTargetTurnIDRef: Ref<string | null>;
  pollCycleRef: Ref<() => Promise<void>>;
  refreshOnceRef: Ref<() => Promise<void>>;
  refreshPendingRef: Ref<boolean>;
  clearRefreshRetry: () => void;
  scheduleRefreshRetry: () => void;
  setIsPolling: Setter<boolean>;
  setPollError: Setter<string | null>;
  refreshLatestPage: (reportError?: boolean) => Promise<ThreadMessage[] | null>;
  refreshLatestState: () => Promise<RemoteThreadSummary | null>;
}

export function useThreadStreamPolling({
  stream, eventsSupported, isConnected, mountedRef, isConnectedRef, pollTimerRef,
  pollGenerationRef, pollInFlightRef, pollTargetTurnIDRef, pollCycleRef, refreshOnceRef,
  refreshPendingRef, clearRefreshRetry, scheduleRefreshRetry, setIsPolling, setPollError,
  refreshLatestPage, refreshLatestState
}: PollingOptions) {
  const { messagesRef, loadingMoreRef, streamStateRef } = stream;
  const pausePolling = useCallback(() => {
    if (pollTimerRef.current !== null) {
      window.clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    if (mountedRef.current) setIsPolling(false);
    clearRefreshRetry();
  }, [clearRefreshRetry]);

  const schedulePoll = useCallback(
    (delay = POLL_INTERVAL_MS) => {
      if (
        !mountedRef.current ||
        !isConnectedRef.current ||
        (eventsSupported && streamStateRef.current === 'subscribed') ||
        document.visibilityState !== 'visible' ||
        navigator.onLine === false
      ) {
        pausePolling();
        return;
      }
      if (pollTimerRef.current !== null) window.clearTimeout(pollTimerRef.current);
      setIsPolling(true);
      const generation = pollGenerationRef.current;
      pollTimerRef.current = window.setTimeout(() => {
        pollTimerRef.current = null;
        if (generation !== pollGenerationRef.current || !mountedRef.current) return;
        void pollCycleRef.current();
      }, delay);
    },
    [eventsSupported, isConnected, pausePolling]
  );

  const runPoll = useCallback(async () => {
    if (
      !mountedRef.current ||
      !isConnectedRef.current ||
      (eventsSupported && streamStateRef.current === 'subscribed') ||
      document.visibilityState !== 'visible' ||
      navigator.onLine === false ||
      pollInFlightRef.current
    ) {
      return;
    }
    if (loadingMoreRef.current) {
      schedulePoll();
      return;
    }

    const generation = pollGenerationRef.current;
    pollInFlightRef.current = true;
    setIsPolling(true);
    try {
      const incoming = await refreshLatestPage(true);
      if (generation !== pollGenerationRef.current || !mountedRef.current) return;
      if (!incoming) {
        pausePolling();
        return;
      }

      const summary = await refreshLatestState();
      if (generation !== pollGenerationRef.current || !mountedRef.current) return;
      const targetTurnID = pollTargetTurnIDRef.current;
      const targetEnded =
        !targetTurnID ||
        messagesRef.current.some(
          (message) => message.turnID === targetTurnID && isEndedStatus(message.turnStatus)
        );

      if (summary && !summary.isRunning && targetEnded) {
        pollTargetTurnIDRef.current = null;
        pausePolling();
        return;
      }
      schedulePoll();
    } catch (err) {
      if (generation === pollGenerationRef.current && mountedRef.current) {
        setPollError(err instanceof Error ? err.message : '刷新会话状态失败，请稍后重试');
        pausePolling();
        // 轮询失败不再永久停止：按退避重试，refreshOnce 成功后会在运行中重新开始轮询。
        scheduleRefreshRetry();
      }
    } finally {
      pollInFlightRef.current = false;
    }
  }, [eventsSupported, isConnected, pausePolling, refreshLatestPage, refreshLatestState, scheduleRefreshRetry, schedulePoll]);

  pollCycleRef.current = runPoll;

  const beginPolling = useCallback(
    (targetTurnID?: string | null) => {
      if (targetTurnID !== undefined) pollTargetTurnIDRef.current = targetTurnID;
      if (!isConnected || document.visibilityState !== 'visible' || navigator.onLine === false) {
        pausePolling();
        return;
      }
      setPollError(null);
      schedulePoll(0);
    },
    [isConnected, pausePolling, schedulePoll]
  );

  const refreshOnce = useCallback(async () => {
    if (
      !mountedRef.current ||
      !isConnectedRef.current ||
      document.visibilityState !== 'visible' ||
      navigator.onLine === false
    ) {
      return;
    }
    // 请求在途时记下这次意图，等在途请求结束后补一次，避免恢复时的重试被守卫吞掉。
    if (pollInFlightRef.current || loadingMoreRef.current) {
      refreshPendingRef.current = true;
      return;
    }
    refreshPendingRef.current = false;
    const generation = pollGenerationRef.current;
    pollInFlightRef.current = true;
    let succeeded = false;
    try {
      const incoming = await refreshLatestPage(true);
      if (!mountedRef.current) return;
      if (!incoming) {
        // 历史页拉取失败（refreshLatestPage 已写入 pollError）或请求已过期：按退避重试。
        if (generation === pollGenerationRef.current) scheduleRefreshRetry();
        return;
      }
      const summary = await refreshLatestState();
      if (generation !== pollGenerationRef.current || !mountedRef.current) return;
      succeeded = true;
      clearRefreshRetry();
      setPollError(null);
      const targetTurnID = pollTargetTurnIDRef.current;
      const targetEnded = Boolean(
        targetTurnID &&
          messagesRef.current.some(
            (message) => message.turnID === targetTurnID && isEndedStatus(message.turnStatus)
          )
      );
      if (summary?.isRunning || (targetTurnID && !targetEnded)) beginPolling();
      else if (targetEnded) {
        pollTargetTurnIDRef.current = null;
        pausePolling();
      }
    } catch (err) {
      if (mountedRef.current) {
        setPollError(err instanceof Error ? err.message : '刷新会话状态失败，请稍后重试');
        scheduleRefreshRetry();
      }
    } finally {
      pollInFlightRef.current = false;
      if (refreshPendingRef.current) {
        refreshPendingRef.current = false;
        if (succeeded) void refreshOnceRef.current();
      }
    }
  }, [
    beginPolling,
    clearRefreshRetry,
    isConnected,
    pausePolling,
    refreshLatestPage,
    refreshLatestState,
    scheduleRefreshRetry
  ]);

  refreshOnceRef.current = refreshOnce;

  return { pausePolling, beginPolling, refreshOnce };
}

interface SubscriptionOptions {
  stream: Stream;
  thread: RemoteThreadSummary;
  eventsSupported: boolean;
  isConnected: boolean;
  mountedRef: Ref<boolean>;
  pollTargetTurnIDRef: Ref<string | null>;
  pausePolling: () => void;
  applyStreamSnapshot: (snapshot: RemoteThreadStreamSnapshot) => void;
  applyStreamProjection: (payload: RemoteThreadProjectionPayload, sequence: number, runID: string) => void;
  subscribeThread: Remote['subscribeThread'];
  unsubscribeThread: Remote['unsubscribeThread'];
  retryThreadSubscription: Remote['retryThreadSubscription'];
}

export function useThreadStreamSubscription({
  stream, thread, eventsSupported, isConnected, mountedRef, pollTargetTurnIDRef,
  pausePolling, applyStreamSnapshot, applyStreamProjection, subscribeThread,
  unsubscribeThread, retryThreadSubscription
}: SubscriptionOptions) {
  const {
    streamStateRef,
    streamThreadIDRef,
    streamRunIDRef,
    streamSequenceRef,
    setStreamState,
    setStreamError,
    subscriptionHandleRef
  } = stream;
  useEffect(() => {
    let active = true;
    if (!eventsSupported) {
      streamStateRef.current = 'disabled';
      setStreamState('disabled');
      setStreamError(null);
      return () => {
        active = false;
      };
    }
    if (!isConnected) {
      streamStateRef.current = 'disconnected';
      setStreamState('disconnected');
      return () => {
        active = false;
      };
    }

    const handlers: RemoteThreadSubscriptionHandlers = {
      onSnapshot: (snapshot) => {
        if (!active) return;
        applyStreamSnapshot(snapshot);
        if (!snapshot.thread.isRunning) {
          pollTargetTurnIDRef.current = null;
          pausePolling();
        }
      },
      onEvent: (event) => {
        if (!active) return;
        applyStreamProjection(event.payload, event.sequence, event.runID);
        if (!event.payload.thread.isRunning) {
          pollTargetTurnIDRef.current = null;
          pausePolling();
        }
      },
      onState: (state) => {
        if (!active) return;
        streamStateRef.current = state;
        setStreamState(state);
        if (state === 'subscribed') {
          setStreamError(null);
          pausePolling();
        }
      },
      onError: (error) => {
        if (!active) return;
        setStreamError(error.message);
      }
    };

    const resume =
      streamThreadIDRef.current === thread.id &&
      streamRunIDRef.current &&
      streamSequenceRef.current >= 0
        ? { runID: streamRunIDRef.current, afterSequence: streamSequenceRef.current }
        : undefined;
    const handle = subscribeThread(thread.id, handlers, resume);
    subscriptionHandleRef.current = handle;
    void handle.ready.catch((err) => {
      if (!active) return;
      const error = err instanceof Error ? err : new Error('会话实时订阅失败，请重试');
      setStreamError(error.message);
      streamStateRef.current = 'error';
      setStreamState('error');
    });

    return () => {
      active = false;
      if (subscriptionHandleRef.current?.subscriptionID === handle.subscriptionID) {
        subscriptionHandleRef.current = null;
      }
      unsubscribeThread(thread.id, handle.subscriptionID);
    };
  }, [
    applyStreamProjection,
    applyStreamSnapshot,
    eventsSupported,
    isConnected,
    pausePolling,
    subscribeThread,
    thread.id,
    unsubscribeThread
  ]);

  const retryStreamSubscription = useCallback(() => {
    setStreamError(null);
    void retryThreadSubscription().catch((err) => {
      if (!mountedRef.current) return;
      setStreamError(err instanceof Error ? err.message : '重试实时会话同步失败');
    });
  }, [mountedRef, retryThreadSubscription, setStreamError]);
  return { retryStreamSubscription };
}
