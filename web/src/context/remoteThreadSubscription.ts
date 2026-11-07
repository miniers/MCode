import { useCallback, useRef } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type {
  RemoteThreadSummary,
  ThreadsEventParams
} from '../types/contract';
import {
  isRecord,
  isValidStreamSnapshot,
  MAX_THREAD_EVENT_BUFFER
} from './remoteGuards';
import type {
  RemoteThreadSubscriptionHandle,
  RemoteThreadSubscriptionHandlers
} from './remoteTypes';

interface ActiveThreadSubscription {
  threadID: string;
  client: McodeRpcClient;
  subscriptionID: number;
  handlers: RemoteThreadSubscriptionHandlers;
  runID: string | null;
  sequence: number;
  buffer: ThreadsEventParams[];
  bufferOverflowed: boolean;
  needsResubscribe: boolean;
  resubscribeInFlight: boolean;
  operationID: number;
  state: 'subscribing' | 'subscribed' | 'disconnected' | 'error';
}

interface ClientRef {
  current: McodeRpcClient | null;
}

interface UseRemoteThreadSubscriptionOptions {
  clientRef: ClientRef;
  publishThreadSummary: (summary: RemoteThreadSummary, options?: { listOrder?: boolean }) => void;
}

export function useRemoteThreadSubscriptions({
  clientRef,
  publishThreadSummary
}: UseRemoteThreadSubscriptionOptions) {
  const activeSubscriptionRef = useRef<ActiveThreadSubscription | null>(null);
  const subscriptionQueueRef = useRef<Promise<void>>(Promise.resolve());
  const subscriptionGenerationRef = useRef(0);
  const handleThreadEventRef = useRef<(client: McodeRpcClient, event: ThreadsEventParams) => void>(
    () => {}
  );

  const enqueueOperation = useCallback(<T,>(operation: () => Promise<T>): Promise<T> => {
    const next = subscriptionQueueRef.current.then(operation, operation);
    subscriptionQueueRef.current = next.then(() => undefined, () => undefined);
    return next;
  }, []);

  const stopSubscription = useCallback(
    (subscription: ActiveThreadSubscription): Promise<void> => {
      if (activeSubscriptionRef.current === subscription) {
        activeSubscriptionRef.current = null;
      }
      subscription.operationID++;
      subscription.runID = null;
      subscription.sequence = -1;
      subscription.buffer = [];
      subscription.bufferOverflowed = false;
      subscription.needsResubscribe = false;
      subscription.state = 'disconnected';
      subscription.handlers.onState('disconnected');

      return enqueueOperation(async () => {
        if (!subscription.client.isReady) return;
        try {
          await subscription.client.unsubscribeThread({ threadID: subscription.threadID });
        } catch {
          // 断线或关闭页面时取消订阅只是 best-effort。
        }
      });
    },
    [enqueueOperation]
  );

  const runSubscribe = useCallback(
    async (subscription: ActiveThreadSubscription, client: McodeRpcClient): Promise<void> => {
      if (subscription.resubscribeInFlight) return;
      subscription.resubscribeInFlight = true;
      subscription.state = 'subscribing';
      subscription.handlers.onState('subscribing');
      const operationID = ++subscription.operationID;

      try {
        await enqueueOperation(async () => {
          if (
            activeSubscriptionRef.current !== subscription ||
            subscription.operationID !== operationID ||
            clientRef.current !== client ||
            !client.isReady
          ) {
            return;
          }

          const result = await client.subscribeThread({ threadID: subscription.threadID });
          if (
            activeSubscriptionRef.current !== subscription ||
            subscription.operationID !== operationID ||
            clientRef.current !== client ||
            !client.isReady
          ) {
            return;
          }
          if (
            !isRecord(result) ||
            result.threadID !== subscription.threadID ||
            result.subscribed !== true ||
            !isValidStreamSnapshot(result.snapshot)
          ) {
            throw new Error('Mac 端未返回有效的会话实时快照，请重试');
          }

          const snapshot = result.snapshot;
          if (snapshot.threadID !== subscription.threadID) {
            throw new Error('Mac 端返回了其他会话的快照，请重试');
          }
          subscription.runID = snapshot.runID;
          subscription.sequence = snapshot.sequence;
          subscription.state = 'subscribed';
          publishThreadSummary(snapshot.thread);
          subscription.handlers.onSnapshot(snapshot);
          subscription.handlers.onState('subscribed');

          const buffered = subscription.buffer
            .splice(0, subscription.buffer.length)
            .sort((left, right) => left.sequence - right.sequence);
          const overflowed = subscription.bufferOverflowed;
          subscription.bufferOverflowed = false;
          for (const event of buffered) {
            if (event.runID !== snapshot.runID) {
              subscription.needsResubscribe = true;
              continue;
            }
            handleThreadEventRef.current(client, event);
          }
          if (overflowed) subscription.needsResubscribe = true;
        });
      } catch (err) {
        if (activeSubscriptionRef.current === subscription) {
          subscription.state = 'error';
          subscription.needsResubscribe = false;
          const error = err instanceof Error ? err : new Error('会话实时订阅失败，请重试');
          subscription.handlers.onError(error);
          subscription.handlers.onState('error');
        }
        throw err;
      } finally {
        subscription.resubscribeInFlight = false;
        if (
          activeSubscriptionRef.current === subscription &&
          subscription.needsResubscribe &&
          clientRef.current === client &&
          client.isReady
        ) {
          subscription.needsResubscribe = false;
          void runSubscribe(subscription, client).catch(() => {});
        }
      }
    },
    [clientRef, enqueueOperation, publishThreadSummary]
  );

  const requestResubscribe = useCallback(
    (subscription: ActiveThreadSubscription): void => {
      if (activeSubscriptionRef.current !== subscription) return;
      subscription.needsResubscribe = true;
      if (subscription.resubscribeInFlight) return;
      const client = subscription.client;
      if (clientRef.current !== client || !client.isReady) {
        subscription.state = 'disconnected';
        subscription.handlers.onState('disconnected');
        return;
      }
      subscription.needsResubscribe = false;
      void runSubscribe(subscription, client).catch(() => {});
    },
    [clientRef, runSubscribe]
  );

  const bufferEvent = useCallback((subscription: ActiveThreadSubscription, event: ThreadsEventParams) => {
    if (subscription.buffer.length >= MAX_THREAD_EVENT_BUFFER) {
      subscription.buffer = [];
      subscription.bufferOverflowed = true;
      return;
    }
    subscription.buffer.push(event);
  }, []);

  const handleThreadEvent = useCallback(
    (client: McodeRpcClient, event: ThreadsEventParams): void => {
      const subscription = activeSubscriptionRef.current;
      if (
        !subscription ||
        subscription.client !== client ||
        clientRef.current !== client ||
        !client.isReady ||
        event.threadID !== subscription.threadID
      ) {
        return;
      }

      if (subscription.state !== 'subscribed' || !subscription.runID) {
        bufferEvent(subscription, event);
        return;
      }

      if (event.runID !== subscription.runID) {
        bufferEvent(subscription, event);
        requestResubscribe(subscription);
        return;
      }
      if (event.sequence <= subscription.sequence) return;
      if (event.kind === 'resync') {
        publishThreadSummary(event.payload.thread);
        requestResubscribe(subscription);
        return;
      }
      if (event.sequence > subscription.sequence + 1) {
        bufferEvent(subscription, event);
        requestResubscribe(subscription);
        return;
      }

      subscription.sequence = event.sequence;
      publishThreadSummary(event.payload.thread);
      subscription.handlers.onEvent(event);
    },
    [bufferEvent, clientRef, publishThreadSummary, requestResubscribe]
  );

  handleThreadEventRef.current = handleThreadEvent;

  const stopActiveSubscription = useCallback((): Promise<void> => {
    subscriptionGenerationRef.current++;
    const subscription = activeSubscriptionRef.current;
    return subscription ? stopSubscription(subscription) : Promise.resolve();
  }, [stopSubscription]);

  const markClientDisconnected = useCallback((client: McodeRpcClient): void => {
    const subscription = activeSubscriptionRef.current;
    if (!subscription || subscription.client !== client) return;
    subscription.operationID++;
    subscription.runID = null;
    subscription.sequence = -1;
    subscription.state = 'disconnected';
    subscription.handlers.onState('disconnected');
  }, []);

  const subscribeThread = useCallback(
    (threadID: string, handlers: RemoteThreadSubscriptionHandlers): RemoteThreadSubscriptionHandle => {
      const subscriptionID = ++subscriptionGenerationRef.current;
      const previous = activeSubscriptionRef.current;
      const previousStop = previous ? stopSubscription(previous) : Promise.resolve();

      const ready = (async () => {
        await previousStop;
        if (subscriptionGenerationRef.current !== subscriptionID) return;

        const client = clientRef.current;
        if (!client || !client.isReady) {
          const error = new Error('当前未与 Mac 建立有效连接，无法订阅会话');
          handlers.onError(error);
          handlers.onState('error');
          throw error;
        }

        const subscription: ActiveThreadSubscription = {
          threadID,
          subscriptionID,
          client,
          handlers,
          runID: null,
          sequence: -1,
          buffer: [],
          bufferOverflowed: false,
          needsResubscribe: false,
          resubscribeInFlight: false,
          operationID: 0,
          state: 'subscribing'
        };
        activeSubscriptionRef.current = subscription;
        handlers.onState('subscribing');
        await runSubscribe(subscription, client);
      })();

      return { subscriptionID, ready };
    },
    [clientRef, runSubscribe, stopSubscription]
  );

  const retryThreadSubscription = useCallback(async (): Promise<void> => {
    const subscription = activeSubscriptionRef.current;
    if (!subscription) throw new Error('当前没有可重试的会话实时订阅');
    const client = subscription.client;
    if (clientRef.current !== client || !client.isReady) {
      throw new Error('当前未与 Mac 建立有效连接，无法重试订阅');
    }
    subscription.buffer = [];
    subscription.bufferOverflowed = false;
    subscription.needsResubscribe = false;
    await runSubscribe(subscription, client);
  }, [clientRef, runSubscribe]);

  const unsubscribeThread = useCallback(
    (threadID: string, subscriptionID?: number): void => {
      const subscription = activeSubscriptionRef.current;
      if (
        !subscription ||
        subscription.threadID !== threadID ||
        (subscriptionID !== undefined && subscription.subscriptionID !== subscriptionID)
      ) {
        return;
      }
      subscriptionGenerationRef.current++;
      void stopSubscription(subscription);
    },
    [stopSubscription]
  );

  return {
    activeSubscriptionRef,
    handleThreadEvent,
    markClientDisconnected,
    requestResubscribe,
    stopActiveSubscription,
    subscribeThread,
    retryThreadSubscription,
    unsubscribeThread
  };
}
