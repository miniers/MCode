import { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteStatic } from './RemoteContext';
import { makeActionID } from './remoteGuards';
import type { RemoteCapabilityDeclaration } from '../types/contract';
import type { RemoteSwarmMergeDiffResult } from '../types/generated';
import type { RemoteSwarmState } from '../types/swarm';

/**
 * 校验 Mac 宿主是否声明了 threads.swarm 协作能力
 */
export function isSwarmCapabilitySupported(capabilities: RemoteCapabilityDeclaration | null): boolean {
  if (!capabilities) return false;
  return Boolean(
    capabilities.features?.includes('threads.swarm') ||
    capabilities.methods?.includes('threads.swarm') ||
    capabilities.methods?.includes('threads.swarm.get')
  );
}

/**
 * 便捷 Hook：获取当前连接是否支持编排
 */
export function useSwarmSupported(): boolean {
  const { capabilities } = useRemoteStatic();
  return isSwarmCapabilitySupported(capabilities);
}

export interface UseThreadSwarmResult {
  state: RemoteSwarmState | null;
  loading: boolean;
  error: Error | null;
  refetch: () => Promise<void>;
  setEnabled: (enabled: boolean) => Promise<void>;
  stopBee: (beeThreadID: string, overrideLeadThreadID?: string, actionID?: string) => Promise<void>;
  /** 宿主 methods 是否声明了批量方法（threads.swarm.stopAll / clearFinished）。 */
  stopAllSupported: boolean;
  clearFinishedSupported: boolean;
  /** 停止全部运行中的子代理；native 子代理异步停止，状态以 threads.swarm.changed 为准。 */
  stopAll: () => Promise<void>;
  /** 把已结束的子代理移出面板。 */
  clearFinished: () => Promise<void>;
  /** 把子代理的提问转交主线程回答；返回态按 stopAll 方式合并。 */
  forwardQuestion: (beeThreadID: string, requestID: string) => Promise<void>;
  /** 读取单个子代理待合并改动的 diff（只读，不改状态缓存）。 */
  getMergeDiff: (beeThreadID: string) => Promise<RemoteSwarmMergeDiffResult>;
  /** 批准子代理改动合入主工作副本。 */
  approveMerge: (beeThreadID: string) => Promise<void>;
  /** 放弃子代理改动。 */
  discardMerge: (beeThreadID: string) => Promise<void>;
}

/**
 * 最近一次已知的会话编排状态，按 threadID 缓存。
 * 进入会话时先用缓存渲染顶部条，避免异步返回前整条延迟出现把时间线往下推。
 * 只缓存内存态，不做持久化；不支持编排或 threadID 为空时不写不读。
 */
const swarmStateCache = new Map<string, RemoteSwarmState>();

/**
 * 跟踪单个会话的编排状态并在发生变更或重连时自动同步
 */
export function useThreadSwarm(threadID: string | null | undefined): UseThreadSwarmResult {
  const {
    capabilities,
    connectionState,
    isSyncing,
    getThreadSwarm,
    setThreadSwarm,
    stopSwarmBee,
    stopAllSwarmBees,
    clearFinishedSwarmBees,
    forwardQuestion: forwardSwarmQuestion,
    getMergeDiff: getSwarmMergeDiff,
    approveMerge: approveSwarmMerge,
    discardMerge: discardSwarmMerge,
    addSwarmListener
  } = useRemoteStatic();

  const swarmSupported = isSwarmCapabilitySupported(capabilities);
  const stopAllSupported = Boolean(capabilities?.methods?.includes('threads.swarm.stopAll'));
  const clearFinishedSupported = Boolean(capabilities?.methods?.includes('threads.swarm.clearFinished'));
  const isConnected = connectionState === 'connected';

  const [state, setState] = useState<RemoteSwarmState | null>(() =>
    threadID && swarmSupported ? swarmStateCache.get(threadID) ?? null : null
  );
  const [loading, setLoading] = useState(Boolean(swarmSupported && threadID && isConnected));
  const [error, setError] = useState<Error | null>(null);

  const fetchRequestIdRef = useRef(0);
  const stateRef = useRef<RemoteSwarmState | null>(null);
  stateRef.current = state;
  const currentThreadIDRef = useRef<string | null | undefined>(threadID);
  currentThreadIDRef.current = threadID;

  // threadID 切换时立即作废旧 fetch，不能把上一会话 isBee/bee 带入新会话
  const [lastThreadID, setLastThreadID] = useState<string | null | undefined>(threadID);
  if (threadID !== lastThreadID) {
    setLastThreadID(threadID);
    // 切到新会话时用该会话的缓存顶上，不能带出上一会话的 isBee/bee
    setState(threadID ? swarmStateCache.get(threadID) ?? null : null);
    setError(null);
    setLoading(Boolean(swarmSupported && threadID && isConnected));
    fetchRequestIdRef.current++;
  }

  // 状态与 threadID 同步后才写缓存：切换会话那一帧 state 仍属于旧会话，不能写到新 threadID 下
  useEffect(() => {
    if (!swarmSupported || !threadID || threadID !== lastThreadID) return;
    if (state) swarmStateCache.set(threadID, state);
  }, [lastThreadID, state, swarmSupported, threadID]);

  const fetchSwarm = useCallback(async () => {
    if (!swarmSupported || !threadID || !isConnected) {
      setLoading(false);
      return;
    }
    const currentRequestId = ++fetchRequestIdRef.current;
    setLoading(true);
    // 有缓存时先水合（能力晚到时也能立刻出条），请求照常发出刷新
    if (stateRef.current === null) {
      const cached = swarmStateCache.get(threadID);
      if (cached) setState(cached);
    }
    try {
      const result = await getThreadSwarm(threadID);
      if (currentRequestId === fetchRequestIdRef.current && currentThreadIDRef.current === threadID) {
        setState(result);
        setError(null);
      }
    } catch (err) {
      if (currentRequestId === fetchRequestIdRef.current && currentThreadIDRef.current === threadID) {
        const errorObj = err instanceof Error ? err : new Error(String(err));
        setError(errorObj);
      }
    } finally {
      if (currentRequestId === fetchRequestIdRef.current && currentThreadIDRef.current === threadID) {
        setLoading(false);
      }
    }
  }, [getThreadSwarm, swarmSupported, isConnected, threadID]);

  // 当没有能力或未指定 threadID 时重置状态为 null，且不触发任何请求
  useEffect(() => {
    if (!swarmSupported || !threadID) {
      setState(null);
      setLoading(false);
      setError(null);
      return;
    }
    // 挂载或 threadID 变化时立即获取
    void fetchSwarm();
  }, [fetchSwarm, swarmSupported, threadID]);

  // 断连或卸载时立即作废在途回调
  useEffect(() => {
    if (!isConnected) {
      fetchRequestIdRef.current++;
      setLoading(false);
    }
  }, [isConnected]);

  useEffect(() => {
    return () => {
      fetchRequestIdRef.current++;
    };
  }, []);

  // 重连/重新同步恢复信号（遵循会话订阅重连时序：连接为 connected 且 isSyncing 结束）
  const previousConnectedRef = useRef(isConnected);
  const previousSyncingRef = useRef(isSyncing);

  useEffect(() => {
    if (!swarmSupported || !threadID) return;

    const wasDisconnected = !previousConnectedRef.current && isConnected;
    const finishedSync = previousSyncingRef.current && !isSyncing && isConnected;

    previousConnectedRef.current = isConnected;
    previousSyncingRef.current = isSyncing;

    if (wasDisconnected || finishedSync) {
      void fetchSwarm();
    }
  }, [fetchSwarm, swarmSupported, isConnected, isSyncing, threadID]);

  // 监听并消费 threads.swarm.changed 通知；通知到达后旧查询不能回踩
  useEffect(() => {
    if (!swarmSupported || !threadID) return;

    const unsubscribe = addSwarmListener((incomingState) => {
      if (incomingState.threadID === threadID) {
        // 通知到达，作废旧查询，防止在途的旧 fetch 回踩覆盖最新通知
        fetchRequestIdRef.current++;
        setState(incomingState);
        setError(null);
        setLoading(false);
      }
    });

    return unsubscribe;
  }, [addSwarmListener, swarmSupported, threadID]);

  // 开启/关闭当前会话的编排
  const setEnabled = useCallback(
    async (enabled: boolean): Promise<void> => {
      if (!swarmSupported || !threadID) return;
      const actionID = makeActionID();
      try {
        const updated = await setThreadSwarm({ threadID, enabled, actionID });
        setState(updated);
        setError(null);
      } catch (err) {
        const errorObj = err instanceof Error ? err : new Error(String(err));
        setError(errorObj);
        throw errorObj;
      }
    },
    [swarmSupported, setThreadSwarm, threadID]
  );

  // 停止指定的子代理，支持传入真实的父会话 leadThreadID 与 actionID
  const stopBee = useCallback(
    async (beeThreadID: string, overrideLeadThreadID?: string, explicitActionID?: string): Promise<void> => {
      if (!swarmSupported || !threadID) return;
      const targetLeadThreadID = overrideLeadThreadID || (stateRef.current?.isBee && stateRef.current?.leadThreadID ? stateRef.current.leadThreadID : threadID);
      const actionID = explicitActionID || makeActionID();
      try {
        const updated = await stopSwarmBee({ threadID: targetLeadThreadID, beeThreadID: beeThreadID, actionID });
        setState(updated);
        setError(null);
      } catch (err) {
        const errorObj = err instanceof Error ? err : new Error(String(err));
        setError(errorObj);
        throw errorObj;
      }
    },
    [swarmSupported, stopSwarmBee, threadID]
  );

  // 批量方法只对主会话有效，返回的 RemoteSwarmState 直接合并（同 stopBee）；
  // stopAll 的子代理 status 可能仍是 running，不在本地改成 stopped，等 threads.swarm.changed 推送。
  const stopAll = useCallback(async (): Promise<void> => {
    if (!swarmSupported || !threadID || !stopAllSupported) return;
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    try {
      const updated = await stopAllSwarmBees({ threadID: targetLeadThreadID, actionID: makeActionID() });
      if (currentThreadIDRef.current === threadID) {
        setState(updated);
        setError(null);
      }
    } catch (err) {
      const errorObj = err instanceof Error ? err : new Error(String(err));
      setError(errorObj);
      throw errorObj;
    }
  }, [swarmSupported, stopAllSwarmBees, stopAllSupported, threadID]);

  const clearFinished = useCallback(async (): Promise<void> => {
    if (!swarmSupported || !threadID || !clearFinishedSupported) return;
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    try {
      const updated = await clearFinishedSwarmBees({ threadID: targetLeadThreadID, actionID: makeActionID() });
      if (currentThreadIDRef.current === threadID) {
        setState(updated);
        setError(null);
      }
    } catch (err) {
      const errorObj = err instanceof Error ? err : new Error(String(err));
      setError(errorObj);
      throw errorObj;
    }
  }, [clearFinishedSwarmBees, clearFinishedSupported, swarmSupported, threadID]);

  // 转交子代理提问：与 stopAll 同样把返回态合并回当前会话缓存
  const forwardQuestion = useCallback(async (beeThreadID: string, requestID: string): Promise<void> => {
    if (!swarmSupported || !threadID) return;
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    try {
      const updated = await forwardSwarmQuestion({ threadID: targetLeadThreadID, beeThreadID: beeThreadID, requestID, actionID: makeActionID() });
      if (currentThreadIDRef.current === threadID) {
        setState(updated);
        setError(null);
      }
    } catch (err) {
      const errorObj = err instanceof Error ? err : new Error(String(err));
      setError(errorObj);
      throw errorObj;
    }
  }, [forwardSwarmQuestion, swarmSupported, threadID]);

  // 读取待合并 diff：只读，不改编排状态缓存
  const getMergeDiff = useCallback(async (beeThreadID: string): Promise<RemoteSwarmMergeDiffResult> => {
    if (!swarmSupported || !threadID) throw new Error('当前会话不支持子代理改动审查');
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    return getSwarmMergeDiff({ threadID: targetLeadThreadID, beeThreadID: beeThreadID });
  }, [getSwarmMergeDiff, swarmSupported, threadID]);

  const approveMerge = useCallback(async (beeThreadID: string): Promise<void> => {
    if (!swarmSupported || !threadID) return;
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    try {
      const updated = await approveSwarmMerge({ threadID: targetLeadThreadID, beeThreadID: beeThreadID, actionID: makeActionID() });
      if (currentThreadIDRef.current === threadID) {
        setState(updated);
        setError(null);
      }
    } catch (err) {
      const errorObj = err instanceof Error ? err : new Error(String(err));
      setError(errorObj);
      throw errorObj;
    }
  }, [approveSwarmMerge, swarmSupported, threadID]);

  const discardMerge = useCallback(async (beeThreadID: string): Promise<void> => {
    if (!swarmSupported || !threadID) return;
    const targetLeadThreadID = stateRef.current?.isBee && stateRef.current.leadThreadID ? stateRef.current.leadThreadID : threadID;
    try {
      const updated = await discardSwarmMerge({ threadID: targetLeadThreadID, beeThreadID: beeThreadID, actionID: makeActionID() });
      if (currentThreadIDRef.current === threadID) {
        setState(updated);
        setError(null);
      }
    } catch (err) {
      const errorObj = err instanceof Error ? err : new Error(String(err));
      setError(errorObj);
      throw errorObj;
    }
  }, [discardSwarmMerge, swarmSupported, threadID]);

  return {
    state,
    loading,
    error,
    refetch: fetchSwarm,
    setEnabled,
    stopBee,
    stopAllSupported,
    clearFinishedSupported,
    stopAll,
    clearFinished,
    forwardQuestion,
    getMergeDiff,
    approveMerge,
    discardMerge
  };
}
