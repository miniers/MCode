import { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import { parseUnifiedDiffStats } from '../utils/diffStats';

/** 空闲状态下改动统计的刷新间隔，对应桌面端 threadChangeStatsTTL。 */
export const THREAD_CHANGE_STATS_TTL_MS = 15_000;

/** 定时器额外让出的一小段，避免与上一轮加载时间戳相等而空转。 */
const IDLE_REFRESH_SLACK_MS = 500;

export interface ThreadChangeStats {
  additions: number;
  deletions: number;
  files: number;
  loading: boolean;
}

const EMPTY_CHANGE_STATS: ThreadChangeStats = { additions: 0, deletions: 0, files: 0, loading: false };

interface ChangeStatsState {
  stats: ThreadChangeStats;
  /** 最近一次加载完成的时间戳；0 表示当前会话还没有加载过。 */
  loadedAt: number;
}

const INITIAL_STATE: ChangeStatsState = { stats: EMPTY_CHANGE_STATS, loadedAt: 0 };

/**
 * 当前会话的改动统计（+N −M）。
 *
 * 刷新规则对齐 internal/ui/composer_changes_tab.go 的 ensureThreadChangeStats：
 * - 切换会话：清零后立即重新拉取（即使正在运行）；
 * - 运行刚结束（isRunning 由 true 变 false）：强制刷新一次；
 * - 运行中：不刷新；
 * - 空闲超过 15 秒：重新拉取；
 * - 已有请求在途时不重复请求；切会话或卸载后丢弃过期响应；
 * - 宿主未声明 threads.getDiff 时返回全 0 且不发请求。
 */
export function useThreadChangeStats(threadID: string | null, isRunning: boolean): ThreadChangeStats {
  const { getThreadDiff, supportsMethod } = useRemoteStatic();
  const supported = supportsMethod('threads.getDiff');

  const [state, setState] = useState<ChangeStatsState>(INITIAL_STATE);

  const activeThreadRef = useRef<string | null>(null);
  const runningRef = useRef(false);
  const inflightRef = useRef(false);
  const loadedAtRef = useRef(0);
  const requestIDRef = useRef(0);

  const load = useCallback(
    async (tid: string) => {
      if (inflightRef.current) return;
      requestIDRef.current += 1;
      const requestID = requestIDRef.current;
      inflightRef.current = true;
      setState((prev) => ({ stats: { ...prev.stats, loading: true }, loadedAt: prev.loadedAt }));
      try {
        const result = await getThreadDiff(tid);
        if (requestIDRef.current !== requestID || activeThreadRef.current !== tid) return;
        const parsed = parseUnifiedDiffStats(result?.diff ?? '');
        loadedAtRef.current = Date.now();
        setState({ stats: { ...parsed, loading: false }, loadedAt: loadedAtRef.current });
      } catch {
        // 出错时清零且不提示，与桌面端一致；仍记录时间，避免失败请求被反复重试。
        if (requestIDRef.current !== requestID || activeThreadRef.current !== tid) return;
        loadedAtRef.current = Date.now();
        setState({ stats: EMPTY_CHANGE_STATS, loadedAt: loadedAtRef.current });
      } finally {
        if (requestIDRef.current === requestID) inflightRef.current = false;
      }
    },
    [getThreadDiff]
  );

  // 卸载时让在途请求的响应失效，避免对已卸载组件 setState。
  useEffect(
    () => () => {
      requestIDRef.current += 1;
      inflightRef.current = false;
    },
    []
  );

  const loadedAt = state.loadedAt;

  useEffect(() => {
    const wasRunning = runningRef.current;
    runningRef.current = isRunning;
    const tid = threadID && threadID.length > 0 ? threadID : null;
    const switched = activeThreadRef.current !== tid;
    activeThreadRef.current = tid;

    if (!tid || !supported) {
      // 无会话或宿主不支持 threads.getDiff：不请求，保持全 0。
      requestIDRef.current += 1;
      inflightRef.current = false;
      loadedAtRef.current = 0;
      setState((prev) => (prev === INITIAL_STATE ? prev : INITIAL_STATE));
      return;
    }

    if (switched) {
      // 切会话：先清掉旧统计，避免显示上一个会话的数字，再立即重拉。
      requestIDRef.current += 1;
      inflightRef.current = false;
      loadedAtRef.current = 0;
      setState(INITIAL_STATE);
      void load(tid);
      return;
    }

    if (wasRunning && !isRunning) {
      // 运行刚结束，改动可能已经变化，强制刷新一次。
      void load(tid);
      return;
    }
    if (isRunning) return;

    if (loadedAtRef.current === 0) return;
    const elapsed = Date.now() - loadedAtRef.current;
    const delay = Math.max(THREAD_CHANGE_STATS_TTL_MS - elapsed, 0) + IDLE_REFRESH_SLACK_MS;
    const timer = window.setTimeout(() => {
      if (activeThreadRef.current === tid && !runningRef.current && !inflightRef.current) {
        void load(tid);
      }
    }, delay);
    return () => window.clearTimeout(timer);
  }, [threadID, isRunning, supported, load, loadedAt]);

  return state.stats;
}
