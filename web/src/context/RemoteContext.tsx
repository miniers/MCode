// 远程控制与看板状态 React Context

import React, {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useEffect,
  useRef,
  useState
} from 'react';
import {
  Board,
  ConnectionState,
  RemoteCapabilityDeclaration,
  RemoteProjectSummary,
  RemoteRequestsChangedParams,
  RemoteRequestsListResult,
  RemoteThreadsQueueChangedParams,
  RemoteThreadsQueueListResult,
  RemoteThreadSummary,
  TaskItem,
  ThreadsMoveParams,
  ThreadsUpdateParams,
  ThreadsChangedParams
} from '../types/contract';
import type { RemoteSwarmState } from '../types/swarm';
import { McodeRpcClient, RpcError } from '../services/rpc';
import { closeRequestNotification } from '../services/pushNotifications';
import {
  getStoredTheme,
  getEnvironmentID,
  readEnvironmentUIState,
  readSessionCredentials,
  setStoredTheme,
  writeEnvironmentUIState
} from '../services/storage';
import type {
  DeviceIdentity,
  SessionCredentials
} from '../services/storage';
import type { SessionNewPairingCodeResult } from '../types/generated';
import { isThreadSummary, mergeThreadSummary } from './remoteGuards';
import { useRemoteBoardActions } from './remoteBoard';
import { useRemoteConnection } from './remoteConnection';
import { useRemoteThreadActions } from './remoteThreadActions';
import { useRemoteThreadSubscriptions } from './remoteThreadSubscription';
import { useRemoteCommands } from './remoteCommands';
import { useRemoteProviderApi } from './remoteProviderApi';
import { useRemoteQueueApi } from './remoteQueueApi';
import { useRemoteSwarmApi } from './remoteSwarmApi';
import { useRemoteThreadMenuApi } from './remoteThreadMenuApi';
import { useRemoteWorkspaceApi } from './remoteWorkspaceApi';
import { useRemoteScriptsApi } from './remoteScriptsApi';
import { useRemoteBulkApi } from './remoteBulkApi';
import type { RemoteContextType, RemoteProviderListWithCatalog, RemoteThreadAction } from './remoteTypes';
import type { RemoteSwarmComposerInfo } from '../types/contract';
export type {
  RemoteThreadAction,
  RemoteThreadSubscriptionHandle,
  RemoteThreadSubscriptionHandlers
} from './remoteTypes';

export type RemoteThreadsContextType = Pick<RemoteContextType, 'threads' | 'currentThread'>;
export type RemoteStaticContextType = Omit<RemoteContextType, 'threads' | 'currentThread'>;

const RemoteStaticContext = createContext<RemoteStaticContextType | null>(null);
const RemoteThreadsContext = createContext<RemoteThreadsContextType | null>(null);

const THREADS_RESYNC_DELAY_MS = 500;

/** 通用通知订阅条目：保留脱挂能力，便于客户端重建后重新挂载。 */
type RemoteNotificationEntry = {
  method: string;
  handler: (params: unknown) => void;
  client: McodeRpcClient | null;
  detach: (() => void) | null;
};

export const RemoteProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [initialCredentials] = useState(readSessionCredentials);
  const [rememberDevice, setRememberDeviceState] = useState(initialCredentials.credentials?.rememberDevice ?? true);
  const [deviceIdentity, setDeviceIdentity] = useState<DeviceIdentity | null>(initialCredentials.credentials?.deviceIdentity ?? null);
  const [credentialNotice, setCredentialNotice] = useState<string | null>(initialCredentials.notice);
  const [forgetInProgress, setForgetInProgress] = useState(false);
  const [board, setBoard] = useState<Board | null>(null);
  const [connectionState, setConnectionState] = useState<ConnectionState>('connecting');
  const [connectionMessage, setConnectionMessage] = useState<string>('正在初始化连接...');
  const [authError, setAuthError] = useState<string | null>(null);
  const [pairingError, setPairingError] = useState<string | null>(null);
  const [pairingInProgress, setPairingInProgress] = useState(false);
  const environmentID = getEnvironmentID();
  const [selectedProjectID, setSelectedProjectIDState] = useState<string | null>(
    () => readEnvironmentUIState(environmentID).selectedProjectID
  );
  const [theme, setThemeState] = useState<'system' | 'light' | 'dark'>(getStoredTheme);
  const [activeActionTaskID, setActiveActionTaskID] = useState<string | null>(null);
  const [actionLoadingText, setActionLoadingText] = useState<string | null>(null);
  const [isSyncing, setIsSyncing] = useState(false);
  const [inspectedTask, setInspectedTask] = useState<TaskItem | null>(null);
  const [projects, setProjects] = useState<RemoteProjectSummary[]>([]);
  const [projectsLoading, setProjectsLoading] = useState(false);
  const [projectsError, setProjectsError] = useState<string | null>(null);
  const [threads, setThreads] = useState<RemoteThreadSummary[]>([]);
  const [threadsLoading, setThreadsLoading] = useState(false);
  const [threadsLoadingMore, setThreadsLoadingMore] = useState(false);
  const [threadsHasMore, setThreadsHasMore] = useState(false);
  const [threadsError, setThreadsError] = useState<string | null>(null);

  const [capabilities, setCapabilities] = useState<RemoteCapabilityDeclaration | null>(null);
  const [providers, setProviders] = useState<RemoteProviderListWithCatalog>([]);
  const [swarm, setSwarm] = useState<RemoteSwarmComposerInfo | null>(null);
  const [providersLoading, setProvidersLoading] = useState(false);
  const [providersError, setProvidersError] = useState<string | null>(null);
  const [currentThread, setCurrentThread] = useState<RemoteThreadSummary | null>(null);
  const [openThreadError, setOpenThreadError] = useState<string | null>(null);
  const [requestSnapshots, setRequestSnapshots] = useState<Record<string, RemoteRequestsListResult>>({});
  const [queueSnapshots, setQueueSnapshots] = useState<Record<string, RemoteThreadsQueueListResult>>({});

  const rpcClientRef = useRef<McodeRpcClient | null>(null);
  const credentialsRef = useRef<SessionCredentials | null>(initialCredentials.credentials);
  const rememberDeviceRef = useRef(rememberDevice);
  const forgetInProgressRef = useRef(false);
  const forgetRequestRef = useRef(0);
  const isSyncingRef = useRef(false);
  const connectionStateRef = useRef<ConnectionState>('connecting');
  const projectsRevisionRef = useRef<number | null>(null);
  const projectsTriggerRevisionRef = useRef<number | null>(null);
  const projectsFetchingRef = useRef(false);
  const projectsFetchPromiseRef = useRef<Promise<void> | null>(null);
  const projectsRef = useRef<RemoteProjectSummary[]>([]);
  const threadsRef = useRef<RemoteThreadSummary[]>([]);
  const threadsRequestRef = useRef(0);
  const threadsGenerationRef = useRef(0);
  const openThreadRequestRef = useRef(0);
  const threadsProjectIDRef = useRef<string | null>(null);
  const threadsCursorRef = useRef<string | null>(null);
  const threadsLoadingMoreRef = useRef(false);
  // 静默同步与单会话刷新各用独立计数器，互不作废 loadThreads 的在途请求。
  const resyncRequestRef = useRef(0);
  const resyncTimerRef = useRef<number | null>(null);
  const scheduleThreadsResyncRef = useRef<() => void>(() => {});
  const refreshRequestRef = useRef(0);
  const currentThreadRef = useRef<RemoteThreadSummary | null>(currentThread);
  const requestSnapshotsRef = useRef<Record<string, RemoteRequestsListResult>>({});
  const refreshThreadRef = useRef<
    (threadID: string) => Promise<RemoteThreadSummary | null>
  >(async () => null);
  const refreshThreadsForProjectRef = useRef<
    (
      projectID: string,
      openThreadID?: string,
      forceOpen?: boolean
    ) => Promise<RemoteThreadSummary | null>
  >(async () => null);
  const capabilitiesRequestedRef = useRef(false);
  const providersRequestedRef = useRef(false);
  const activeSyncPromiseRef = useRef<Promise<void> | null>(null);
  const syncGenerationRef = useRef(0);

  // 供生命周期恢复探测读取最新的连接状态（避免闭包捕获旧值）
  useEffect(() => {
    connectionStateRef.current = connectionState;
  }, [connectionState]);

  useEffect(() => {
    currentThreadRef.current = currentThread;
  }, [currentThread]);

  const setCurrentThreadFromContext = useCallback((thread: RemoteThreadSummary | null) => {
    openThreadRequestRef.current++;
    setOpenThreadError(null);
    currentThreadRef.current = thread;
    setCurrentThread(thread);
  }, []);

  /**
   * 把摘要发布到列表行和 currentThread。
   * 默认保留列表行原有的 section/orderKey；只有 listOrder 为 true（摘要来自宿主按列表布局
   * 计算的 threads.update 结果）且当前是「全部项目」范围时才接受摘要里的排序字段。
   * currentThread 始终完整合并。
   */
  const publishThreadSummary = useCallback((
    summary: RemoteThreadSummary,
    options?: { listOrder?: boolean }
  ): void => {
    if (!isThreadSummary(summary)) return;

    const keepListOrder = !(options?.listOrder === true && threadsProjectIDRef.current === null);
    const nextThreads = threadsRef.current.map((thread) =>
      thread.id === summary.id ? mergeThreadSummary(thread, summary, keepListOrder) : thread
    );
    if (nextThreads.some((thread, index) => thread !== threadsRef.current[index])) {
      threadsGenerationRef.current++;
      threadsRef.current = nextThreads;
      setThreads(nextThreads);
    }

    const currentThreadValue = currentThreadRef.current;
    if (currentThreadValue?.id === summary.id) {
      const nextCurrent = { ...currentThreadValue, ...summary };
      currentThreadRef.current = nextCurrent;
      setCurrentThread(nextCurrent);
    }
  }, []);

  const cancelThreadsResync = useCallback((): void => {
    resyncRequestRef.current++;
    refreshRequestRef.current++;
    threadsGenerationRef.current++;
    if (resyncTimerRef.current !== null) {
      window.clearTimeout(resyncTimerRef.current);
      resyncTimerRef.current = null;
    }
  }, []);

  useEffect(() => cancelThreadsResync, [cancelThreadsResync]);

  /**
   * 静默同步当前范围的会话列表：不清空列表、不设置 threadsLoading，失败也不提示。
   * 新结果替换未休眠行和第一页已休眠行；已经通过 loadMoreThreads 加载、但不在新结果里的
   * 已休眠行追加在后面。范围变了、loadThreads 开始了新请求、列表在请求期间发生变更或更新的同步已发出时丢弃结果，
   * 它自己不会作废 loadThreads。返回新结果的第一页；丢弃或失败时返回 null。
   */
  const resyncThreads = useCallback(async (): Promise<RemoteThreadSummary[] | null> => {
    const client = rpcClientRef.current;
    if (!client?.isReady) return null;
    const projectID = threadsProjectIDRef.current;
    const listRequestId = threadsRequestRef.current;
    const requestId = ++resyncRequestRef.current;
    const generation = threadsGenerationRef.current;

    try {
      const res = await client.listThreads({
        projectID: projectID ?? undefined,
        includeArchived: false
      });
      if (
        rpcClientRef.current !== client ||
        requestId !== resyncRequestRef.current ||
        listRequestId !== threadsRequestRef.current ||
        threadsProjectIDRef.current !== projectID
      ) {
        return null;
      }
      if (threadsGenerationRef.current !== generation) {
        scheduleThreadsResyncRef.current();
        return null;
      }
      const incoming = Array.isArray(res.threads) ? res.threads : [];
      const incomingIDs = new Set(incoming.map((thread) => thread.id));
      const loadedDormant = res.hasMore && res.nextCursor
        ? threadsRef.current.filter(
            (thread) =>
              thread.isDormant &&
              !thread.isArchived &&
              !incomingIDs.has(thread.id) &&
              (projectID === null || thread.projectID === projectID)
          )
        : [];
      const next = [...incoming, ...loadedDormant];
      threadsGenerationRef.current++;
      threadsRef.current = next;
      threadsCursorRef.current = loadedDormant.length > 0
        ? (threadsCursorRef.current ?? res.nextCursor ?? null)
        : (res.nextCursor ?? null);
      setThreads(next);
      setThreadsHasMore(Boolean(threadsCursorRef.current));
      setThreadsError(null);
      return incoming;
    } catch {
      return null;
    }
  }, []);

  // 已有定时器时不重置：持续推送（流式输出）不能无限推迟同步。
  const scheduleThreadsResync = useCallback((): void => {
    if (resyncTimerRef.current !== null) return;
    resyncTimerRef.current = window.setTimeout(() => {
      resyncTimerRef.current = null;
      void resyncThreads();
    }, THREADS_RESYNC_DELAY_MS);
  }, [resyncThreads]);

  scheduleThreadsResyncRef.current = scheduleThreadsResync;

  /**
   * 宿主的 threads.changed 只按「全部项目」的活动布局计算 section/orderKey。
   * 单项目范围下（布局 rank 不同、没有 attention 分组）不能用它覆盖已有行的排序字段，
   * 已有行只合并其他字段，再安排一次静默同步取回项目布局的排序。
   */
  const handleThreadsChanged = useCallback((change: ThreadsChangedParams): void => {
    const scoped = threadsProjectIDRef.current !== null;
    const removed = new Set(change.removedThreadIDs);
    const nextThreads = threadsRef.current.filter((thread) => !removed.has(thread.id));
    for (const summary of change.threads) {
      if (removed.has(summary.id)) continue;
      const index = nextThreads.findIndex((thread) => thread.id === summary.id);
      if (index >= 0) {
        nextThreads[index] = scoped
          ? mergeThreadSummary(nextThreads[index], summary, true)
          : summary;
      } else if (
        threadsProjectIDRef.current === null ||
        threadsProjectIDRef.current === summary.projectID
      ) {
        nextThreads.push(
          scoped ? { ...summary, section: summary.isPinned ? 'pinned' : 'active' } : summary
        );
      }
    }
    if (
      nextThreads.length !== threadsRef.current.length ||
      nextThreads.some((thread, index) => thread !== threadsRef.current[index])
    ) {
      threadsGenerationRef.current++;
      threadsRef.current = nextThreads;
      setThreads(nextThreads);
      if (scoped) scheduleThreadsResync();
    }

    const current = currentThreadRef.current;
    if (current) {
      const changed = change.threads.find((thread) => thread.id === current.id);
      if (changed) {
        currentThreadRef.current = changed;
        setCurrentThread(changed);
      } else if (removed.has(current.id)) {
        currentThreadRef.current = null;
        setCurrentThread(null);
      }
    }
  }, [scheduleThreadsResync]);

  const handleRequestsChanged = useCallback((change: RemoteRequestsChangedParams): void => {
    // change.requests 是当前全部待处理，不是增量：旧快照里有、新快照里没有的就是已处理。
    const previous = requestSnapshotsRef.current[change.threadID];
    if (previous) {
      const pending = new Set(change.requests.map((request) => request.requestID));
      for (const request of previous.requests) {
        if (!pending.has(request.requestID)) closeRequestNotification(request.requestID);
      }
    }
    const next = { ...requestSnapshotsRef.current, [change.threadID]: change };
    requestSnapshotsRef.current = next;
    setRequestSnapshots(next);
  }, []);

  const handleQueueChanged = useCallback((change: RemoteThreadsQueueChangedParams): void => {
    setQueueSnapshots((previous) => {
      const current = previous[change.threadID];
      if (current && change.revision < current.revision) return previous;
      return { ...previous, [change.threadID]: change };
    });
  }, []);

  const swarmListenersRef = useRef(new Set<(state: RemoteSwarmState) => void>());
  const handleSwarmChanged = useCallback((state: RemoteSwarmState): void => {
    for (const listener of swarmListenersRef.current) {
      try {
        listener(state);
      } catch {
        // 忽略通知回调异常
      }
    }
  }, []);

  const {
    activeSubscriptionRef: activeThreadSubscriptionRef,
    handleThreadEvent,
    markClientDisconnected,
    requestResubscribe: requestThreadResubscribe,
    stopActiveSubscription: stopActiveThreadSubscription,
    subscribeThread,
    retryThreadSubscription,
    unsubscribeThread
  } = useRemoteThreadSubscriptions({
    clientRef: rpcClientRef,
    publishThreadSummary
  });

  const threadActionController = useRemoteThreadActions({
    clientRef: rpcClientRef,
    connectionState,
    connectionStateRef,
    isSyncingRef,
    capabilities,
    projectsRef,
    threadsRef,
    currentThreadRef,
    // 创建/动作查询用它判断请求期间用户是否切换了会话；列表加载不算导航。
    threadsRequestRef: openThreadRequestRef,
    refreshThreadRef,
    refreshThreadsForProjectRef
  });

  const { threadActions, reset: resetThreadActions, createThread, sendThreadMessage, checkThreadAction, dismissThreadAction } =
    threadActionController;

  const { commandSnapshots, loadThreadCommands, reset: resetCommands } = useRemoteCommands({
    clientRef: rpcClientRef,
    capabilities
  });

  const {
    moveTask,
    startTask,
    reviewTask,
    getDiff,
    createTask,
    createGoal,
    startGoal,
    pauseGoal,
    cancelGoal,
    replanGoal,
    retryGoalTask,
    skipGoalTask
  } = useRemoteBoardActions({
    clientRef: rpcClientRef,
    connectionStateRef,
    isSyncingRef,
    capabilities,
    setActiveActionTaskID,
    setActionLoadingText
  });

  const getThreadDiff = useCallback(
    async (threadID: string, turnID?: string) => {
      const client = rpcClientRef.current;
      if (!client) throw new Error('Remote client not connected');
      return client.getThreadDiff({ threadID, turnID });
    },
    [rpcClientRef]
  );

  // 同步更新 inspectedTask 当 board 推送更新时
  useEffect(() => {
    if (board && inspectedTask) {
      const updated = board.tasks.find((t) => t.id === inspectedTask.id);
      if (updated) {
        setInspectedTask(updated);
      } else {
        setInspectedTask(null);
      }
    }
  }, [board, inspectedTask]);

  // 主题切换应用
  const setTheme = useCallback((newTheme: 'system' | 'light' | 'dark') => {
    setThemeState(newTheme);
    setStoredTheme(newTheme);
  }, []);

  const setSelectedProjectID = useCallback((projectID: string | null) => {
    setSelectedProjectIDState(projectID);
    writeEnvironmentUIState(environmentID, { selectedProjectID: projectID });
  }, [environmentID]);

  useEffect(() => {
    const root = document.documentElement;
    const applyTheme = () => {
      const isDark =
        theme === 'dark' ||
        (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches);
      if (isDark) {
        root.classList.add('dark');
      } else {
        root.classList.remove('dark');
      }
    };

    applyTheme();

    if (theme === 'system') {
      const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)');
      const listener = () => applyTheme();
      mediaQuery.addEventListener('change', listener);
      return () => mediaQuery.removeEventListener('change', listener);
    }
  }, [theme]);

  const resetForNewClient = useCallback((pairToken: boolean) => {
    projectsRevisionRef.current = null;
    projectsTriggerRevisionRef.current = null;
    projectsFetchingRef.current = false;
    projectsFetchPromiseRef.current = null;
    projectsRef.current = [];
    threadsRequestRef.current++;
    cancelThreadsResync();
    threadsProjectIDRef.current = null;
    threadsCursorRef.current = null;
    threadsLoadingMoreRef.current = false;
    resetThreadActions();
    resetCommands();
    capabilitiesRequestedRef.current = false;
    providersRequestedRef.current = false;
    threadsRef.current = [];
    currentThreadRef.current = null;
    requestSnapshotsRef.current = {};
    setRequestSnapshots({});
    setQueueSnapshots({});
    setProjects([]);
    setProjectsError(null);
    setProjectsLoading(false);
    setThreads([]);
    setThreadsError(null);
    setThreadsLoading(false);
    setThreadsLoadingMore(false);
    setThreadsHasMore(false);
    setCapabilities(null);
    setProviders([]);
    setSwarm(null);
    setProvidersError(null);
    setProvidersLoading(false);
    setCurrentThread(null);
    setPairingError(null);
    setPairingInProgress(pairToken);

    if (pairToken) {
      setBoard(null);
      setInspectedTask(null);
      setSelectedProjectID(null);
      setAuthError(null);
      setActiveActionTaskID(null);
      setActionLoadingText(null);
      setConnectionState('connecting');
      setConnectionMessage('正在初始化连接...');
    }
  }, [cancelThreadsResync, resetCommands, resetThreadActions]);

  const resetAfterAuthFailure = useCallback(() => {
    setPairingInProgress(false);
    isSyncingRef.current = false;
    setIsSyncing(false);
    setBoard(null);
    setInspectedTask(null);
    setSelectedProjectID(null);
    setActiveActionTaskID(null);
    setActionLoadingText(null);
    projectsRef.current = [];
    setProjects([]);
    setProjectsError(null);
    setProjectsLoading(false);
    threadsRequestRef.current++;
    threadsRef.current = [];
    setThreads([]);
    setThreadsError(null);
    setThreadsLoading(false);
    cancelThreadsResync();
    threadsCursorRef.current = null;
    threadsLoadingMoreRef.current = false;
    setThreadsLoadingMore(false);
    setThreadsHasMore(false);
    resetThreadActions();
    resetCommands();
    currentThreadRef.current = null;
    requestSnapshotsRef.current = {};
    setRequestSnapshots({});
    setQueueSnapshots({});
    setCapabilities(null);
    providersRequestedRef.current = false;
    setProviders([]);
    setSwarm(null);
    setProvidersError(null);
    setProvidersLoading(false);
    setCurrentThread(null);
  }, [cancelThreadsResync, resetCommands, resetThreadActions]);

  const resetAfterClearAuth = useCallback(() => {
    setBoard(null);
    setAuthError(null);
    setPairingError(null);
    setPairingInProgress(false);
    setConnectionState('unpaired');
    connectionStateRef.current = 'unpaired';
    setConnectionMessage('配对凭据已清理，请在 Mac 桌面端重新扫码');
    setInspectedTask(null);
    setSelectedProjectID(null);
    setActiveActionTaskID(null);
    setActionLoadingText(null);
    isSyncingRef.current = false;
    setIsSyncing(false);
    activeSyncPromiseRef.current = null;
    syncGenerationRef.current++;
    threadsRequestRef.current++;
    cancelThreadsResync();
    threadsProjectIDRef.current = null;
    threadsCursorRef.current = null;
    threadsLoadingMoreRef.current = false;
    resetThreadActions();
    resetCommands();
    threadsRef.current = [];
    currentThreadRef.current = null;
    requestSnapshotsRef.current = {};
    setRequestSnapshots({});
    setQueueSnapshots({});
    projectsRevisionRef.current = null;
    projectsTriggerRevisionRef.current = null;
    projectsFetchingRef.current = false;
    projectsFetchPromiseRef.current = null;
    projectsRef.current = [];
    capabilitiesRequestedRef.current = false;
    providersRequestedRef.current = false;
    setProjects([]);
    setProjectsError(null);
    setProjectsLoading(false);
    setThreads([]);
    setThreadsError(null);
    setThreadsLoading(false);
    setThreadsLoadingMore(false);
    setThreadsHasMore(false);
    setCapabilities(null);
    setProviders([]);
    setSwarm(null);
    setProvidersError(null);
    setProvidersLoading(false);
    setCurrentThread(null);
  }, [cancelThreadsResync, resetCommands, resetThreadActions]);

  const {
    clearAuth,
    forgetDevice,
    pairWithToken,
    reconnect,
    refreshBoard,
    setRememberDevice
  } = useRemoteConnection({
    clientRef: rpcClientRef,
    credentialsRef,
    rememberDeviceRef,
    connectionStateRef,
    forgetInProgressRef,
    forgetRequestRef,
    isSyncingRef,
    activeSyncPromiseRef,
    syncGenerationRef,
    stopActiveSubscription: stopActiveThreadSubscription,
    markClientDisconnected,
    handleThreadEvent,
    handleThreadsChanged,
    handleRequestsChanged,
    handleQueueChanged,
    handleSwarmChanged,
    resetForNewClient,
    resetAfterAuthFailure,
    resetAfterClearAuth,
    setRememberDeviceState,
    setDeviceIdentity,
    setCredentialNotice,
    setForgetInProgress,
    setBoard,
    setConnectionState,
    setConnectionMessage,
    setAuthError,
    setPairingError,
    setPairingInProgress,
    setIsSyncing
  });

  const newPairingCode = useCallback(async (): Promise<SessionNewPairingCodeResult> => {
    const client = rpcClientRef.current;
    if (!client?.isReady || connectionStateRef.current !== 'connected') {
      throw new Error('当前未与 Mac 建立可用连接');
    }
    if (capabilities && !capabilities.methods.includes('session.newPairingCode')) {
      throw new Error('当前宿主未声明主屏幕配对能力');
    }
    return client.newPairingCode();
  }, [capabilities, connectionStateRef]);

  const renameDevice = useCallback(async (name: string): Promise<void> => {
    const client = rpcClientRef.current;
    if (!client?.isReady || connectionStateRef.current !== 'connected') {
      throw new Error('当前未与 Mac 建立可用连接');
    }
    if (capabilities && !capabilities.methods.includes('session.renameDevice')) {
      throw new Error('当前宿主未声明设备改名能力，请升级 Mac 端');
    }
    await client.renameDevice(name);
  }, [capabilities, connectionStateRef]);

  const {
    listProviders,
    rememberProviderSelection,
    getComposerQuota,
    getPushPublicKey,
    subscribePush,
    unsubscribePush
  } = useRemoteProviderApi({
    clientRef: rpcClientRef,
    capabilities,
    setProviders,
    setSwarm,
    setProvidersLoading,
    setProvidersError
  });

  const branchThread = useCallback(
    async (params: { threadID: string; messageID: string; actionID: string }): Promise<{ threadID: string }> => {
      const client = rpcClientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，操作已禁用');
      }
      if (!capabilities || !capabilities.methods.includes('threads.branch')) {
        throw new Error('当前宿主未声明 threads.branch 能力');
      }
      return client.branchThread(params);
    },
    [capabilities, connectionStateRef]
  );

  const forwardThread = useCallback(
    async (params: { threadID: string; actionID: string }): Promise<{ threadID: string }> => {
      const client = rpcClientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，操作已禁用');
      }
      if (
        !capabilities ||
        !capabilities.methods.includes('threads.continue') ||
        !capabilities.features?.includes('threads.continue')
      ) {
        throw new Error('当前宿主未声明 threads.continue 能力');
      }
      const result = await client.forwardThread(params);
      if (rpcClientRef.current !== client) throw new Error('连接已更换，请在 Mac 端确认转发结果');
      return result;
    },
    [capabilities, connectionStateRef]
  );

  // WebSocket 仍然可用但页面经历后台/前台切换时，重新取得实时订阅快照。
  useEffect(() => {
    const resubscribeIfNeeded = () => {
      if (document.visibilityState !== 'visible' || navigator.onLine === false) return;
      const subscription = activeThreadSubscriptionRef.current;
      if (!subscription) return;
      requestThreadResubscribe(subscription);
    };

    document.addEventListener('visibilitychange', resubscribeIfNeeded);
    window.addEventListener('pageshow', resubscribeIfNeeded);
    window.addEventListener('online', resubscribeIfNeeded);
    return () => {
      document.removeEventListener('visibilitychange', resubscribeIfNeeded);
      window.removeEventListener('pageshow', resubscribeIfNeeded);
      window.removeEventListener('online', resubscribeIfNeeded);
    };
  }, [requestThreadResubscribe]);

  useEffect(() => {
    if (connectionState !== 'connected') return;
    const subscription = activeThreadSubscriptionRef.current;
    if (subscription && subscription.state !== 'subscribed') {
      requestThreadResubscribe(subscription);
    }
  }, [connectionState, requestThreadResubscribe]);

  /**
   * 拉取宿主已加载项目。同一触发 revision 不重复拉取，也不堆叠并发请求。
   */
  const loadProjects = useCallback(async (triggerRevision?: number) => {
    const client = rpcClientRef.current;
    if (!client) return;
    if (projectsFetchingRef.current) {
      await projectsFetchPromiseRef.current;
      return;
    }
    if (
      triggerRevision !== undefined &&
      projectsTriggerRevisionRef.current === triggerRevision &&
      projectsRevisionRef.current !== null
    ) {
      return;
    }

    projectsFetchingRef.current = true;
    if (triggerRevision !== undefined) projectsTriggerRevisionRef.current = triggerRevision;
    setProjectsLoading(true);
    const fetchPromise = (async () => {
      try {
        const res = await client.listProjects();
        if (rpcClientRef.current !== client) return;
        if (projectsRevisionRef.current !== null && res.revision < projectsRevisionRef.current) {
          return;
        }
        projectsRevisionRef.current = res.revision;
        const list = Array.isArray(res.projects) ? res.projects : [];
        projectsRef.current = list;
        setProjects(list);
        setProjectsError(null);
      } catch (err) {
        if (rpcClientRef.current !== client) return;
        setProjectsError(err instanceof Error ? err.message : '加载项目列表失败');
      } finally {
        if (rpcClientRef.current === client) {
          projectsFetchingRef.current = false;
          setProjectsLoading(false);
        }
      }
    })();
    projectsFetchPromiseRef.current = fetchPromise;
    try {
      await fetchPromise;
    } finally {
      if (projectsFetchPromiseRef.current === fetchPromise) {
        projectsFetchPromiseRef.current = null;
      }
    }
  }, []);

  /**
   * 拉取指定项目的会话列表；切换项目时先清空上一个项目的数据，避免残留。
   */
  const loadThreads = useCallback(async (projectID?: string | null) => {
    const client = rpcClientRef.current;
    cancelThreadsResync();
    const requestId = ++threadsRequestRef.current;
    const generation = ++threadsGenerationRef.current;
    threadsProjectIDRef.current = projectID ?? null;
    threadsCursorRef.current = null;
    threadsLoadingMoreRef.current = false;

    threadsRef.current = [];
    setThreads([]);
    setThreadsError(null);
    setThreadsLoading(true);
    setThreadsLoadingMore(false);
    setThreadsHasMore(false);

    try {
      if (!client) {
        setThreadsError('未连接到 Mac 端，无法加载会话列表');
        return;
      }
      // 宿主未声明 threads.list 时不发请求，也不假装可用
      if (capabilities && !capabilities.methods.includes('threads.list')) {
        setThreadsError('当前宿主未声明会话列表能力 (threads.list)，无法列出会话');
        return;
      }
      const res = await client.listThreads({
        projectID: projectID ?? undefined,
        includeArchived: false
      });
      if (rpcClientRef.current !== client || requestId !== threadsRequestRef.current) return;
      if (threadsGenerationRef.current !== generation) {
        scheduleThreadsResync();
        return;
      }
      const list = Array.isArray(res.threads) ? res.threads : [];
      threadsGenerationRef.current++;
      threadsCursorRef.current = res.nextCursor ?? null;
      threadsRef.current = list;
      setThreads(list);
      setThreadsHasMore(Boolean(res.hasMore && res.nextCursor));
    } catch (err) {
      if (requestId !== threadsRequestRef.current) return;
      setThreadsError(err instanceof Error ? err.message : '加载会话列表失败');
    } finally {
      if (requestId === threadsRequestRef.current) {
        setThreadsLoading(false);
      }
    }
  }, [capabilities, cancelThreadsResync, scheduleThreadsResync]);

  const loadMoreThreads = useCallback(async () => {
    const client = rpcClientRef.current;
    const cursor = threadsCursorRef.current;
    if (!client || !cursor || threadsLoadingMoreRef.current) return;

    const requestId = threadsRequestRef.current;
    const projectID = threadsProjectIDRef.current;
    const generation = threadsGenerationRef.current;
    threadsLoadingMoreRef.current = true;
    setThreadsLoadingMore(true);
    try {
      const res = await client.listThreads({
        projectID: projectID ?? undefined,
        includeArchived: false,
        cursor
      });
      if (
        rpcClientRef.current !== client ||
        requestId !== threadsRequestRef.current ||
        threadsProjectIDRef.current !== projectID
      ) {
        return;
      }
      if (threadsGenerationRef.current !== generation) {
        scheduleThreadsResync();
        return;
      }
      const existingIDs = new Set(threadsRef.current.map((thread) => thread.id));
      const incoming = (Array.isArray(res.threads) ? res.threads : []).filter(
        (thread) => !existingIDs.has(thread.id) && !thread.isArchived
      );
      const next = [...threadsRef.current, ...incoming];
      threadsGenerationRef.current++;
      threadsRef.current = next;
      setThreads(next);
      threadsCursorRef.current = res.nextCursor ?? null;
      setThreadsHasMore(Boolean(res.hasMore && res.nextCursor));
    } catch (err) {
      if (requestId === threadsRequestRef.current) {
        setThreadsError(err instanceof Error ? err.message : '加载更多会话失败');
      }
    } finally {
      threadsLoadingMoreRef.current = false;
      setThreadsLoadingMore(false);
    }
  }, [scheduleThreadsResync]);

  /**
   * 取回某个项目的最新会话摘要。范围正是该项目时走静默同步更新列表（保留已加载的已休眠分页），
   * 否则只返回摘要，不动当前列表和分页游标。它只用自己的计数器，不会作废在途的 loadThreads。
   */
  const refreshThreadsForProject = useCallback(
    async (
      projectID: string,
      openThreadID?: string,
      forceOpen = false
    ): Promise<RemoteThreadSummary | null> => {
      const client = rpcClientRef.current;
      if (!client) return null;
      const requestId = ++refreshRequestRef.current;
      const syncsList = threadsProjectIDRef.current === projectID;

      try {
        let list: RemoteThreadSummary[] | null;
        if (syncsList) {
          list = await resyncThreads();
        } else {
          const res = await client.listThreads({ projectID, includeArchived: false });
          list = Array.isArray(res.threads) ? res.threads : [];
        }
        if (rpcClientRef.current !== client || requestId !== refreshRequestRef.current || !list) {
          return null;
        }

        const refreshed = list.find((thread) => thread.id === openThreadID) ?? null;
        if (
          syncsList &&
          refreshed &&
          openThreadID &&
          threadsProjectIDRef.current === projectID &&
          (forceOpen || currentThreadRef.current?.id === openThreadID)
        ) {
          currentThreadRef.current = refreshed;
          setCurrentThread(refreshed);
        }
        return refreshed;
      } catch {
        return null;
      }
    },
    [resyncThreads]
  );

  const refreshThread = useCallback(
    async (threadID: string): Promise<RemoteThreadSummary | null> => {
      const current =
        (currentThreadRef.current?.id === threadID ? currentThreadRef.current : null) ??
        threadsRef.current.find((thread) => thread.id === threadID) ??
        null;
      if (!current) return null;
      return refreshThreadsForProject(current.projectID, threadID);
    },
    [refreshThreadsForProject]
  );

  const updateThread = useCallback(
    async (
      threadID: string,
      changes: Omit<ThreadsUpdateParams, 'threadID'>
    ): Promise<RemoteThreadSummary> => {
      const client = rpcClientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法更新会话');
      const result = await client.updateThread({ threadID, ...changes });
      // threads.update 的摘要按「全部项目」的活动布局计算，只在该范围下用于排序；
      // 单项目范围下排序以项目布局为准，靠一次静默同步立即取回。
      publishThreadSummary(result.thread, { listOrder: true });
      if (threadsProjectIDRef.current !== null) void resyncThreads();
      return result.thread;
    },
    [publishThreadSummary, resyncThreads]
  );

  /**
   * 取回已归档会话（不写入主列表 state：resyncThreads 会整体替换主列表）。
   * 宿主把已归档行以 section 'archived' 追加在结果里；已结算行可能分页，最多跟随几页游标。
   */
  const listArchivedThreads = useCallback(
    async (projectID?: string): Promise<RemoteThreadSummary[]> => {
      const client = rpcClientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法加载已归档会话');
      if (capabilities && !capabilities.methods.includes('threads.list')) {
        throw new Error('当前宿主未声明会话列表能力 (threads.list)，无法列出会话');
      }
      const archived: RemoteThreadSummary[] = [];
      const seen = new Set<string>();
      let cursor: string | undefined;
      for (let page = 0; page < 10; page++) {
        const res = await client.listThreads({ projectID, includeArchived: true, cursor });
        for (const thread of Array.isArray(res.threads) ? res.threads : []) {
          if (thread.section === 'archived' && !seen.has(thread.id)) {
            seen.add(thread.id);
            archived.push(thread);
          }
        }
        if (!res.hasMore || !res.nextCursor) break;
        cursor = res.nextCursor;
      }
      return archived;
    },
    [capabilities]
  );

  const moveThread = useCallback(
    async (params: ThreadsMoveParams): Promise<RemoteThreadSummary> => {
      const client = rpcClientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法移动会话');
      const result = await client.moveThread(params);
      // threads.move 的摘要按项目布局计算；无论哪个范围都触发静默同步取回正确排序。
      publishThreadSummary(result.thread);
      void resyncThreads();
      return result.thread;
    },
    [publishThreadSummary, resyncThreads]
  );

  refreshThreadRef.current = refreshThread;
  refreshThreadsForProjectRef.current = refreshThreadsForProject;

  const openThreadByID = useCallback(async (threadID: string): Promise<void> => {
    const requestId = ++openThreadRequestRef.current;
    setOpenThreadError(null);
    const found = threadsRef.current.find((t) => t.id === threadID) ?? null;
    if (found) {
      currentThreadRef.current = found;
      setCurrentThread(found);
    }
    if (found || !capabilities?.methods.includes('threads.get')) return;

    const client = rpcClientRef.current;
    if (!client || connectionStateRef.current !== 'connected') {
      setOpenThreadError('未连接到 Mac');
      return;
    }
    try {
      const result = await client.getThread(threadID);
      if (rpcClientRef.current !== client || requestId !== openThreadRequestRef.current) return;
      if (!isThreadSummary(result.thread) || result.thread.id !== threadID) {
        throw new Error('会话不存在或无法访问');
      }
      currentThreadRef.current = result.thread;
      setCurrentThread(result.thread);
    } catch (err) {
      if (rpcClientRef.current !== client || requestId !== openThreadRequestRef.current) return;
      setOpenThreadError(err instanceof RpcError && err.code === -32602
        ? '会话不存在或无法访问'
        : err instanceof Error ? err.message : '会话不存在或无法访问');
    }
  }, [capabilities]);

  const openThread = useCallback((threadID: string) => {
    void openThreadByID(threadID);
  }, [openThreadByID]);

  const closeThread = useCallback((leaveProject = false) => {
    openThreadRequestRef.current++;
    setOpenThreadError(null);
    if (leaveProject) {
      threadsProjectIDRef.current = null;
      threadsRequestRef.current++;
      threadsGenerationRef.current++;
      threadsCursorRef.current = null;
      cancelThreadsResync();
    }
    currentThreadRef.current = null;
    setCurrentThread(null);
  }, [cancelThreadsResync]);

  const getThreadHistory = useCallback(
    async (threadID: string, cursor?: string | null, limit?: number) => {
      const client = rpcClientRef.current;
      if (!client) {
        throw new Error('未连接到 Mac 端，无法读取会话历史');
      }
      const page = await client.getThreadHistory(threadID, cursor, limit);
      if (rpcClientRef.current !== client) throw new Error('连接已更换，请重新读取会话历史');
      return page;
    },
    []
  );

  const remoteNotificationEntriesRef = useRef<Set<RemoteNotificationEntry>>(new Set());

  /** 把已登记的通用通知订阅挂到指定客户端；已挂在该客户端上的不动，避免重复订阅。 */
  const attachRemoteNotifications = useCallback((client: McodeRpcClient): void => {
    for (const entry of remoteNotificationEntriesRef.current) {
      if (entry.client === client) continue;
      entry.detach?.();
      entry.client = client;
      entry.detach = client.onNotification(entry.method, entry.handler);
    }
  }, []);

  // 客户端可能在订阅之后才创建（子组件 effect 先于 Provider effect 执行），连接建立后补挂一次。
  useEffect(() => {
    const client = rpcClientRef.current;
    if (client) attachRemoteNotifications(client);
  }, [connectionState, attachRemoteNotifications]);

  /** 通用 RPC 调用：按方法名直接调用宿主，无需为每个新方法扩展上下文。 */
  const callRemote = useCallback(
    async <T,>(method: string, params?: unknown, timeoutMs?: number): Promise<T> => {
      const client = rpcClientRef.current;
      if (!client || !client.isReady) {
        throw new Error('未连接到 Mac');
      }
      return client.call<T>(method, params, timeoutMs);
    },
    [rpcClientRef]
  );

  /** 宿主能力清单是否声明了某方法；与 useRemoteMethodSupport 同口径：清单未到位时按可用处理。 */
  const supportsMethod = useCallback(
    (method: string) => (capabilities ? capabilities.methods.includes(method) : true),
    [capabilities]
  );

  /** 订阅任意方法的服务端通知，返回取消订阅函数。 */
  const onRemoteNotification = useCallback(
    (method: string, handler: (params: unknown) => void): (() => void) => {
      const entry: RemoteNotificationEntry = { method, handler, client: null, detach: null };
      const client = rpcClientRef.current;
      if (client) {
        entry.client = client;
        entry.detach = client.onNotification(method, handler);
      }
      remoteNotificationEntriesRef.current.add(entry);
      return () => {
        remoteNotificationEntriesRef.current.delete(entry);
        entry.detach?.();
        entry.detach = null;
        entry.client = null;
      };
    },
    [rpcClientRef]
  );

  const {
    listRequests,
    respondToRequest,
    interruptThread,
    killRunningCommand,
    listThreadQueue,
    updateThreadQueue,
    removeThreadQueue,
    sendThreadQueueNow,
    moveThreadQueue,
    interruptAndSendThread,
    editThreadQueueDraft,
    recallTurn,
    compactThread,
    forwardSwarmQuestion,
    subscribeCompactSuggestion,
    uploadAttachment
  } = useRemoteQueueApi({
    clientRef: rpcClientRef,
    capabilities,
    requestSnapshotsRef,
    setRequestSnapshots,
    setQueueSnapshots
  });

  const {
    getThreadSwarm,
    setThreadSwarm,
    stopSwarmBee,
    stopAllSwarmBees,
    clearFinishedSwarmBees,
    getSwarmTiers,
    setSwarmTiers,
    forwardQuestion,
    getMergeDiff,
    approveMerge,
    discardMerge
  } = useRemoteSwarmApi({ clientRef: rpcClientRef, capabilities });

  const {
    setThreadSwarmMode,
    setThreadSwarmPlansOnly,
    setThreadSwarmBeeTiers,
    regenerateThreadTitle,
    limitProviderText,
    previewWorktreeMove,
    executeWorktreeMove
  } = useRemoteThreadMenuApi({ clientRef: rpcClientRef, capabilities });

  const {
    getThreadGitStatus,
    getThreadGitBranches,
    listThreadFiles,
    readThreadFile,
    searchThreadFiles,
    readThreadMedia
  } = useRemoteWorkspaceApi({ clientRef: rpcClientRef, capabilities });

  const {
    listThreadScripts,
    runThreadScript,
    getThreadScriptRunStatus,
    setThreadScriptFavorite,
    saveScripts
  } = useRemoteScriptsApi({ clientRef: rpcClientRef, capabilities });

  const {
    previewBulkThreads,
    archiveBulkThreads,
    markDormantBulkThreads,
    markBulkThreadsRead,
    undoBulkThreads
  } = useRemoteBulkApi({ clientRef: rpcClientRef, capabilities });

  const checkThreadActionUnified = useCallback(
    async (actionID: string): Promise<RemoteThreadAction> => {
      try {
        return await checkThreadAction(actionID);
      } catch (err) {
        const client = rpcClientRef.current;
        if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
          throw err;
        }
        if (!capabilities?.methods.includes('threads.actionStatus')) {
          throw new Error('当前宿主未声明 threads.actionStatus 能力，无法查询动作状态');
        }
        const result = await client.getThreadActionStatus(actionID);
        if (rpcClientRef.current !== client) throw new Error('连接已更换，请重新查询动作状态');
        const stateError = result.error?.message ??
          (result.state === 'unknown'
            ? '操作状态未知，请在 Mac 端核对'
            : result.state === 'failed'
            ? '远程动作执行失败'
            : undefined);
        return {
          actionID,
          kind: 'send',
          projectID: '',
          state: result.state,
          threadID: result.threadID,
          error: stateError
        };
      }
    },
    [capabilities, checkThreadAction]
  );

  const addSwarmListener = useCallback((listener: (state: RemoteSwarmState) => void): (() => void) => {
    swarmListenersRef.current.add(listener);
    return () => {
      swarmListenersRef.current.delete(listener);
    };
  }, []);

  /**
   * 连接可用后获取一次能力声明；未声明时回退为仅看板模式。
   */
  useEffect(() => {
    if (connectionState !== 'connected') return;
    if (capabilitiesRequestedRef.current) return;
    const client = rpcClientRef.current;
    if (!client) return;

    capabilitiesRequestedRef.current = true;
    client
      .getCapabilities()
      .then((declaration) => {
        if (rpcClientRef.current !== client) return;
        setCapabilities(declaration);
      })
      .catch(() => {
        if (rpcClientRef.current !== client) return;
        // 能力表不可用时按「未知」处理：不展示项目页入口；下次重连再试一次
        capabilitiesRequestedRef.current = false;
        setCapabilities(null);
      });
  }, [connectionState]);

  useEffect(() => {
    if (connectionState !== 'connected' || !capabilities?.methods.includes('providers.list')) return;
    if (providersRequestedRef.current) return;
    providersRequestedRef.current = true;
    void listProviders().catch(() => {
      providersRequestedRef.current = false;
    });
  }, [capabilities, connectionState, listProviders]);

  // 连接建立后拉取一次项目列表
  useEffect(() => {
    if (connectionState !== 'connected') return;
    void loadProjects();
  }, [connectionState, loadProjects]);

  // 看板 revision 前进说明宿主侧项目/会话集可能变化，按 revision 去重后再拉取
  useEffect(() => {
    if (!board) return;
    void loadProjects(board.revision);
  }, [board, loadProjects]);

  const staticValue = useMemo<RemoteStaticContextType>(() => ({
    board,
    connectionState,
    connectionMessage,
    authError,
    pairingError,
    pairingInProgress,
    rememberDevice,
    setRememberDevice,
    deviceIdentity,
    renameDevice,
    credentialNotice,
    forgetInProgress,
    forgetDevice,
    selectedProjectID,
    setSelectedProjectID,
    theme,
    setTheme,
    activeActionTaskID,
    actionLoadingText,
    isSyncing,
    inspectedTask,
    setInspectedTask,
    projects,
    projectsLoading,
    projectsError,
    environmentID,
    threadsLoading,
    threadsLoadingMore,
    threadsHasMore,
    threadsError,
    threadActions,
    capabilities,
    providers,
    swarm,
    providersLoading,
    providersError,
    openThreadError,
    setCurrentThread: setCurrentThreadFromContext,
    requestSnapshots,
    queueSnapshots,
    callRemote,
    supportsMethod,
    onRemoteNotification,
    reconnect,
    pairWithToken,
    newPairingCode,
    clearAuth,
    refreshBoard,
    moveTask,
    startTask,
    reviewTask,
    getDiff,
    getThreadDiff,
    createTask,
    createGoal,
    startGoal,
    pauseGoal,
    cancelGoal,
    replanGoal,
    retryGoalTask,
    skipGoalTask,
    loadProjects,
    loadThreads,
    loadMoreThreads,
    resyncThreads,
    listArchivedThreads,
    updateThread,
    moveThread,
    createThread,
    sendThreadMessage,
    checkThreadAction: checkThreadActionUnified,
    dismissThreadAction,
    refreshThread,
    subscribeThread,
    retryThreadSubscription,
    unsubscribeThread,
    openThread,
    openThreadByID,
    closeThread,
    getThreadHistory,
    listProviders,
    rememberProviderSelection,
    getComposerQuota,
    getPushPublicKey,
    subscribePush,
    unsubscribePush,
    branchThread,
    forwardThread,
    commandSnapshots,
    loadThreadCommands,
    listRequests,
    respondToRequest,
    interruptThread,
    killRunningCommand,
    listThreadQueue,
    updateThreadQueue,
    removeThreadQueue,
    sendThreadQueueNow,
    moveThreadQueue,
    interruptAndSendThread,
    editThreadQueueDraft,
    recallTurn,
    compactThread,
    forwardSwarmQuestion,
    subscribeCompactSuggestion,
    uploadAttachment,
    getThreadSwarm,
    setThreadSwarm,
    stopSwarmBee,
    stopAllSwarmBees,
    clearFinishedSwarmBees,
    getSwarmTiers,
    setSwarmTiers,
    forwardQuestion,
    getMergeDiff,
    approveMerge,
    discardMerge,
    addSwarmListener,
    setThreadSwarmMode,
    setThreadSwarmPlansOnly,
    setThreadSwarmBeeTiers,
    regenerateThreadTitle,
    limitProviderText,
    previewWorktreeMove,
    executeWorktreeMove,
    getThreadGitStatus,
    getThreadGitBranches,
    listThreadFiles,
    readThreadFile,
    searchThreadFiles,
    readThreadMedia,
    listThreadScripts,
    runThreadScript,
    getThreadScriptRunStatus,
    setThreadScriptFavorite,
    saveScripts,
    previewBulkThreads,
    archiveBulkThreads,
    markDormantBulkThreads,
    markBulkThreadsRead,
    undoBulkThreads,
  }), [
    board, connectionState, connectionMessage, authError, pairingError, pairingInProgress, rememberDevice, setRememberDevice, deviceIdentity, renameDevice, credentialNotice, forgetInProgress, forgetDevice, selectedProjectID, setSelectedProjectID, theme, setTheme, activeActionTaskID, actionLoadingText, isSyncing, inspectedTask, setInspectedTask, projects, projectsLoading, projectsError, environmentID, threadsLoading, threadsLoadingMore, threadsHasMore, threadsError, threadActions, capabilities, providers, swarm, providersLoading, providersError, openThreadError, setCurrentThreadFromContext, requestSnapshots, queueSnapshots, callRemote, supportsMethod, onRemoteNotification, reconnect, pairWithToken, newPairingCode, clearAuth, refreshBoard, moveTask, startTask, reviewTask, getDiff, getThreadDiff, createTask, createGoal, startGoal, pauseGoal, cancelGoal, replanGoal, retryGoalTask, skipGoalTask, loadProjects, loadThreads, loadMoreThreads, resyncThreads, listArchivedThreads, updateThread, moveThread, createThread, sendThreadMessage, checkThreadActionUnified, dismissThreadAction, refreshThread, subscribeThread, retryThreadSubscription, unsubscribeThread, openThread, openThreadByID, closeThread, getThreadHistory, listProviders, rememberProviderSelection, getComposerQuota, getPushPublicKey, subscribePush, unsubscribePush, branchThread, forwardThread, commandSnapshots, loadThreadCommands, listRequests, respondToRequest, interruptThread, killRunningCommand, listThreadQueue, updateThreadQueue, removeThreadQueue, sendThreadQueueNow, moveThreadQueue, interruptAndSendThread, editThreadQueueDraft, recallTurn, compactThread, forwardSwarmQuestion, subscribeCompactSuggestion, uploadAttachment, getThreadSwarm, setThreadSwarm, stopSwarmBee, stopAllSwarmBees, clearFinishedSwarmBees, getSwarmTiers, setSwarmTiers, forwardQuestion, getMergeDiff, approveMerge, discardMerge, addSwarmListener, setThreadSwarmMode, setThreadSwarmPlansOnly, setThreadSwarmBeeTiers, regenerateThreadTitle, limitProviderText, previewWorktreeMove, executeWorktreeMove, getThreadGitStatus, getThreadGitBranches, listThreadFiles, readThreadFile, searchThreadFiles, readThreadMedia, listThreadScripts, runThreadScript, getThreadScriptRunStatus, setThreadScriptFavorite, previewBulkThreads, archiveBulkThreads, markDormantBulkThreads, markBulkThreadsRead, undoBulkThreads
  ]);

  const threadsValue = useMemo<RemoteThreadsContextType>(() => ({ threads, currentThread }), [threads, currentThread]);

  return (
    <RemoteStaticContext.Provider value={staticValue}>
      <RemoteThreadsContext.Provider value={threadsValue}>
        {children}
      </RemoteThreadsContext.Provider>
    </RemoteStaticContext.Provider>
  );
};

export function useRemoteStatic(): RemoteStaticContextType {
  const context = useContext(RemoteStaticContext);
  if (!context) {
    throw new Error('useRemoteStatic 必须在 RemoteProvider 内部使用');
  }
  return context;
}

export function useRemote(): RemoteContextType {
  const staticContext = useContext(RemoteStaticContext);
  const threadsContext = useContext(RemoteThreadsContext);
  const merged = useMemo<RemoteContextType | null>(
    () => (staticContext && threadsContext ? { ...staticContext, ...threadsContext } : null),
    [staticContext, threadsContext]
  );
  if (!staticContext || !threadsContext || !merged) {
    throw new Error('useRemote 必须在 RemoteProvider 内部使用');
  }
  return merged;
}
