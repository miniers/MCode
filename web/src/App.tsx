// mcode 移动首页：默认会话视图，保留看板作为同一首页的第二个视图。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { RemoteProvider, useRemote, useRemoteStatic } from './context/RemoteContext';
import { useEdgeSwipeBack } from './hooks/useEdgeSwipeBack';
import {
  resolveNavDirection,
  shouldSkipNavTransition,
  startMobilePageTransition,
  type NavDirection
} from './hooks/mobileNavTransition';
import { useSheetDrag } from './hooks/useSheetDrag';
import { Header } from './components/Header';
import { ThreadList } from './components/ThreadList';
import { SecureEntryHint } from './components/SecureEntryHint';
import { PushEnableHint } from './components/PushEnableHint';
import { LazyBoundary } from './components/LazyFallback';
import { ExclamationTriangleIcon, MessageIcon, PlusIcon, SparklesIcon, XMarkIcon } from './components/Icons';
import { useRemoteCall, useRemoteNotification } from './context/useRemoteCall';
import { PROJECTS_ADD_METHOD, PROJECTS_CHANGED_METHOD } from './context/remoteProjectApi';
import { MarkdownImageProvider } from './context/MarkdownImageContext';
import { parsePairingInput } from './services/storage';
import { setPendingBadge } from './services/pushNotifications';
import { loadThreadDraftView, loadThreadView, prefetchThreadViews } from './utils/lazyThreadViews';
import { parseThreadRoute } from './utils/threadRoute';

// 会话页会把时间线、输入框、markdown 与 highlight.js 一起带进来，首屏只加载会话列表，
// 所以 ThreadView / ThreadDraftView 改为按需加载；连接可用后在空闲时预取，打开基本不用等。
const ThreadView = React.lazy(() => loadThreadView().then((module) => ({ default: module.ThreadView })));
const ThreadDraftView = React.lazy(() =>
  loadThreadDraftView().then((module) => ({ default: module.ThreadDraftView }))
);

// 看板与任务/目标详情弹窗只在进入看板路由后才会用到，默认会话列表不需要它们，
// 因此做成懒加载组件：打开首页时不下载，进入 #/board、#/task、#/goal 时才加载。
const KanbanBoard = React.lazy(() =>
  import('./components/KanbanBoard').then((module) => ({ default: module.KanbanBoard }))
);
const TaskDetailModal = React.lazy(() =>
  import('./components/TaskDetailModal').then((module) => ({ default: module.TaskDetailModal }))
);
const GoalDetailModal = React.lazy(() =>
  import('./components/TaskDetailModal').then((module) => ({ default: module.GoalDetailModal }))
);
// 新建项目抽屉也只在首页点「添加项目」时才渲染，同样懒加载。
const AddProjectSheet = React.lazy(() =>
  import('./components/AddProjectSheet').then((module) => ({ default: module.AddProjectSheet }))
);

type Route =
  | { kind: 'home' }
  | { kind: 'board' }
  | { kind: 'settings'; page?: string }
  | { kind: 'draft'; projectID: string }
  | {
      kind: 'thread';
      threadID: string;
      subview?: 'agents' | 'review' | 'git' | 'files' | 'scripts';
      filePath?: string;
      /** 推送点开时要定位的消息与审批请求，仅用于一次性跳转。 */
      messageID?: string;
      requestID?: string;
    }
  | { kind: 'task'; taskID: string }
  | { kind: 'goal'; goalID: string };

function parseRoute(hash: string): Route {
  const value = hash.replace(/^#/, '') || '/';
  if (value === '/board') return { kind: 'board' };
  if (value === '/settings') return { kind: 'settings' };
  // #/settings/<页面 id>：直接打开某个 tab，tab 切换用 replace 写，不压浏览历史。
  const settingsPageMatch = value.match(/^\/settings\/([^/]+)$/);
  if (settingsPageMatch) {
    try {
      return { kind: 'settings', page: decodeURIComponent(settingsPageMatch[1]) };
    } catch {
      return { kind: 'settings' };
    }
  }
  const draftMatch = value.match(/^\/new\/([^/]+)$/);
  if (draftMatch) {
    try {
      return { kind: 'draft', projectID: decodeURIComponent(draftMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const taskMatch = value.match(/^\/task\/([^/]+)$/);
  if (taskMatch) {
    try {
      return { kind: 'task', taskID: decodeURIComponent(taskMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const goalMatch = value.match(/^\/goal\/([^/]+)$/);
  if (goalMatch) {
    try {
      return { kind: 'goal', goalID: decodeURIComponent(goalMatch[1]) };
    } catch {
      return { kind: 'home' };
    }
  }
  const agentsMatch = value.match(/^\/t\/([^/]+)\/agents$/);
  if (agentsMatch) {
    try {
      return { kind: 'thread', threadID: decodeURIComponent(agentsMatch[1]), subview: 'agents' };
    } catch {
      return { kind: 'home' };
    }
  }
  const reviewMatch = value.match(/^\/t\/([^/]+)\/review$/);
  if (reviewMatch) {
    try {
      return { kind: 'thread', threadID: decodeURIComponent(reviewMatch[1]), subview: 'review' };
    } catch {
      return { kind: 'home' };
    }
  }
  const workspaceMatch = value.match(/^\/t\/([^/]+)\/(git|files|scripts)(?:\/([^/]+))?$/);
  if (workspaceMatch && (workspaceMatch[2] === 'files' || !workspaceMatch[3])) {
    try {
      return {
        kind: 'thread',
        threadID: decodeURIComponent(workspaceMatch[1]),
        subview: workspaceMatch[2] as 'git' | 'files' | 'scripts',
        filePath: workspaceMatch[3] ? decodeURIComponent(workspaceMatch[3]) : undefined
      };
    } catch {
      return { kind: 'home' };
    }
  }
  // 推送点开：#/t/<id>?m=<messageID>&r=<requestID>，查询参数只负责定位。
  const threadRoute = parseThreadRoute(value);
  if (threadRoute) {
    return {
      kind: 'thread',
      threadID: threadRoute.threadID,
      messageID: threadRoute.messageID,
      requestID: threadRoute.requestID
    };
  }
  return { kind: 'home' };
}

function currentDepth(): number {
  const state = window.history.state;
  if (
    state &&
    typeof state === 'object' &&
    typeof state.mcodeDepth === 'number' &&
    Number.isInteger(state.mcodeDepth) &&
    state.mcodeDepth >= 0
  ) {
    return state.mcodeDepth;
  }
  return 0;
}

function useHashRoute(): [
  Route,
  (hash: string, options?: { replace?: boolean }) => void,
  (fallback: string, options?: { skipTransition?: boolean }) => void,
  (hash: string) => void
] {
  const [route, setRoute] = useState<Route>(() => parseRoute(window.location.hash));
  const routeRef = useRef(route);
  const hashRef = useRef(window.location.hash);
  const skipTransitionRef = useRef(false);
  const transitionRef = useRef<ViewTransition | null>(null);

  const commitRoute = useCallback(
    (hash: string, direction: NavDirection, options?: { skipTransition?: boolean }) => {
      const next = parseRoute(hash);
      const prev = routeRef.current;
      const apply = () => {
        routeRef.current = next;
        hashRef.current = hash;
        setRoute(next);
      };
      // 同时只跑一次过场：上一段还没结束就直接换页，避免动画叠在一起。
      if (options?.skipTransition || transitionRef.current || shouldSkipNavTransition(prev, next)) {
        apply();
        return;
      }
      const transition = startMobilePageTransition(direction, () => flushSync(apply));
      if (!transition) return;
      transitionRef.current = transition;
      void transition.finished
        .catch(() => undefined)
        .finally(() => {
          if (transitionRef.current === transition) transitionRef.current = null;
        });
    },
    []
  );

  useEffect(() => {
    const sync = () => {
      const hash = window.location.hash;
      if (hash === hashRef.current) return;
      const direction = resolveNavDirection(routeRef.current, parseRoute(hash));
      const skipTransition = skipTransitionRef.current;
      skipTransitionRef.current = false;
      commitRoute(hash, direction, { skipTransition });
    };
    window.addEventListener('hashchange', sync);
    window.addEventListener('popstate', sync);
    return () => {
      window.removeEventListener('hashchange', sync);
      window.removeEventListener('popstate', sync);
    };
  }, [commitRoute]);

  const navigate = (hash: string, options?: { replace?: boolean }) => {
    if (window.location.hash === hash) {
      const next = parseRoute(hash);
      routeRef.current = next;
      hashRef.current = hash;
      setRoute(next);
      return;
    }
    const direction = resolveNavDirection(routeRef.current, parseRoute(hash), options?.replace);
    if (options?.replace) {
      window.history.replaceState({ mcodeDepth: currentDepth() }, '', hash);
    } else {
      window.history.pushState({ mcodeDepth: currentDepth() + 1 }, '', hash);
    }
    commitRoute(hash, direction);
  };

  const goBack = (fallback: string, options?: { skipTransition?: boolean }) => {
    if (currentDepth() > 0) {
      if (options?.skipTransition) {
        // 边缘滑回已经在做位移动画，popstate 到达时不要再叠一次 View Transition。
        skipTransitionRef.current = true;
        window.setTimeout(() => {
          skipTransitionRef.current = false;
        }, 600);
      }
      window.history.back();
      return;
    }
    if (window.location.hash === fallback) return;
    window.history.replaceState({ mcodeDepth: currentDepth() }, '', fallback);
    commitRoute(fallback, 'pop', { skipTransition: options?.skipTransition });
  };

  // 清掉 hash 上的查询参数（推送定位用完即弃）：不压历史、不触发 hashchange、不走过场。
  const replaceHash = useCallback((hash: string) => {
    if (window.location.hash === hash) return;
    window.history.replaceState({ mcodeDepth: currentDepth() }, '', hash);
    hashRef.current = hash;
  }, []);

  return [route, navigate, goBack, replaceHash];
}

function getPageBuildID(): string {
  return document.querySelector('meta[name="mcode-build-id"]')?.getAttribute('content') || 'dev';
}

const PairingScreen: React.FC = () => {
  const {
    connectionState,
    connectionMessage,
    authError,
    pairingError,
    pairingInProgress,
    rememberDevice,
    setRememberDevice,
    credentialNotice,
    pairWithToken,
    clearAuth
  } = useRemoteStatic();
  const [pairingInput, setPairingInput] = useState('');
  const [pairingInputError, setPairingInputError] = useState<string | null>(null);

  const pairingBusy = pairingInProgress && (connectionState === 'connecting' || connectionState === 'reconnecting');
  const pairingFeedback = pairingInputError || pairingError || (connectionState === 'auth_failed' ? authError : null);
  const showStatus = pairingInProgress && connectionState !== 'auth_failed';

  const submitPairing = (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (pairingBusy) return;
    const result = parsePairingInput(pairingInput);
    if (!result.token) {
      setPairingInputError(result.error || '配对码无效');
      return;
    }
    setPairingInputError(null);
    pairWithToken(result.token);
  };

  return (
    <main className="pairing-screen">
      <section className="pairing-panel" aria-labelledby="pairing-title">
        <div className="pairing-brand" aria-hidden="true"><span>D</span></div>
        <p className="eyebrow">mcode</p>
        <h1 id="pairing-title" className="pairing-title">
          {connectionState === 'auth_failed' ? '配对凭据已失效' : '连接你的 Mac'}
        </h1>
        <p className="pairing-copy">
          {connectionState === 'auth_failed'
            ? '使用 Mac 设置中刚生成的配对码重新建立连接。'
            : '输入 Mac 设置中显示的配对码，或粘贴完整配对链接。'}
        </p>

        {credentialNotice && <p className="inline-alert inline-alert--warning" role="status">{credentialNotice}</p>}

        <form onSubmit={submitPairing} className="pairing-form" aria-busy={pairingBusy}>
          <label htmlFor="pairing-input" className="settings-label">配对码或配对链接</label>
          <input
            id="pairing-input"
            type="text"
            value={pairingInput}
            onChange={(event) => {
              setPairingInput(event.target.value);
              if (pairingInputError) setPairingInputError(null);
            }}
            placeholder="输入 6 位授权码、64 位配对码或完整链接"
            autoComplete="off"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            enterKeyHint="go"
            aria-invalid={Boolean(pairingFeedback)}
            className="pairing-input select-text"
          />
          <label className="settings-checkbox">
            <input type="checkbox" checked={rememberDevice} onChange={(event) => setRememberDevice(event.target.checked)} disabled={pairingBusy} />
            <span>记住此设备</span>
          </label>
          {pairingFeedback && <p className="settings-error" role="alert">{pairingFeedback}</p>}
          {showStatus && <p className="settings-note" role="status" aria-live="polite">{connectionState === 'reconnecting' ? `正在重试连接。${connectionMessage ? ` ${connectionMessage}` : ''}` : '正在连接 Mac，请稍候。'}</p>}
          <div className="pairing-actions">
            <button type="submit" className="primary-button primary-button--wide" disabled={pairingBusy || !pairingInput.trim()}>{pairingBusy ? '配对中…' : '连接'}</button>
            <button type="button" className="secondary-button" onClick={() => { setPairingInput(''); setPairingInputError(null); clearAuth(); }}>清除</button>
          </div>
        </form>

        <div className="pairing-help">
          <span className="pairing-help__icon"><MessageIcon className="h-4 w-4" /></span>
          <div><p className="font-semibold">在 Mac 获取配对码</p><p>设置 → 远程控制 → 配对新设备。</p></div>
        </div>
      </section>
    </main>
  );
};

const AppContent: React.FC = () => {
  const {
    board,
    connectionState,
    connectionMessage,
    isSyncing,
    pairingInProgress,
    capabilities,
    selectedProjectID,
    projects,
    threads,
    currentThread,
    openThreadError,
    inspectedTask,
    setInspectedTask,
    loadThreads,
    resyncThreads,
    openThread,
    openThreadByID,
    closeThread,
    createTask,
    createGoal,
    loadProjects,
    reconnect
  } = useRemote();
  const [route, navigate, goBack, replaceHash] = useHashRoute();
  const requestedThreadIDRef = useRef<string | null>(null);
  // 推送点开的一次性定位请求：nonce 让同一目标被再次点开时也能重新定位。
  const [threadJump, setThreadJump] = useState<{
    threadID: string;
    messageID?: string;
    requestID?: string;
    nonce: number;
  } | null>(null);
  const threadJumpNonceRef = useRef(0);
  const previousRouteKindRef = useRef(route.kind);
  const previousConnectionStateRef = useRef(connectionState);
  const [pageBuildID] = useState(getPageBuildID);

  const [fabMenuOpen, setFabMenuOpen] = useState(false);
  const [createKind, setCreateKind] = useState<'task' | 'goal' | 'thread' | null>(null);
  const [createProjectID, setCreateProjectID] = useState(selectedProjectID ?? '');
  const [createTitle, setCreateTitle] = useState('');
  const [createNotes, setCreateNotes] = useState('');
  const [createError, setCreateError] = useState<string | null>(null);
  const [createBusy, setCreateBusy] = useState(false);
  const [addProjectOpen, setAddProjectOpen] = useState(false);
  // 只有宿主声明了 projects.add 才给入口，避免在不支持的 Mac 端点了没反应。
  const { supportsMethod } = useRemoteCall();
  const canAddProject = supportsMethod(PROJECTS_ADD_METHOD);

  const showPairingScreen = pairingInProgress || connectionState === 'unpaired' || connectionState === 'auth_failed';
  const boardRoute = route.kind === 'board' || route.kind === 'task' || route.kind === 'goal';
  const activeView = boardRoute ? 'board' : 'sessions';
  const settingsOpen = route.kind === 'settings';
  const settingsPageID = route.kind === 'settings' ? route.page ?? null : null;
  const isHomeOrBoard = route.kind === 'home' || route.kind === 'board';

  // 新建会话 sheet 只接管把手拖动关闭（W-4）；非首页启用左边缘右滑返回（W-8）。
  const createSheetRef = useRef<HTMLElement | null>(null);
  const createSheetHandleRef = useRef<HTMLDivElement | null>(null);
  const closeCreateSheet = useCallback(() => setCreateKind(null), []);
  useSheetDrag(createSheetRef, createSheetHandleRef, closeCreateSheet, createKind === 'thread');
  useEdgeSwipeBack({
    enabled: !isHomeOrBoard && !showPairingScreen,
    onCommit: () => goBack(boardRoute ? '#/board' : '#/', { skipTransition: true })
  });

  const canLink = !capabilities || capabilities.features.includes('kanban.linking');
  const canGoals = !capabilities || capabilities.features.includes('kanban.goals');
  const canCreateTask = Boolean(canLink && (!capabilities || capabilities.methods.includes('kanban.createTask')));
  const canCreateGoal = Boolean(canGoals && (!capabilities || capabilities.methods.includes('goals.create')));
  // 看板写入与连接状态绑定：离线或同步中不允许提交，避免在状态未确认时重复创建。
  const kanbanWritesBlocked = connectionState !== 'connected' || isSyncing;

  // 连上 Mac 后空闲预取会话页：从列表点进会话时通常已经下载完，只剩渲染。
  useEffect(() => {
    if (connectionState === 'connected') prefetchThreadViews();
  }, [connectionState]);

  useEffect(() => {
    setCreateProjectID(selectedProjectID ?? projects[0]?.id ?? '');
  }, [projects, selectedProjectID]);

  useEffect(() => {
    if (!fabMenuOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setFabMenuOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [fabMenuOpen]);

  // 宿主添加项目后会广播 projects.changed：静默刷新项目列表，断线时刷新失败直接忽略。
  useRemoteNotification(
    PROJECTS_CHANGED_METHOD,
    () => {
      void loadProjects().catch(() => undefined);
    },
    canAddProject
  );

  const handleProjectAdded = (projectID: string) => {
    setAddProjectOpen(false);
    // 先刷新列表再跳转：项目页按 ID 找项目，列表还没更新会停在加载态。
    void loadProjects()
      .catch(() => undefined)
      .then(() => navigate(`#/new/${encodeURIComponent(projectID)}`));
  };

  const handleNewThread = () => {
    prefetchThreadViews();
    if (selectedProjectID) {
      navigate(`#/new/${encodeURIComponent(selectedProjectID)}`);
    } else if (projects.length === 1) {
      navigate(`#/new/${encodeURIComponent(projects[0].id)}`);
    } else {
      setCreateProjectID(projects[0]?.id ?? '');
      setCreateKind('thread');
    }
  };

  const submitCreate = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!createKind || !createProjectID || createBusy) return;
    if (kanbanWritesBlocked) {
      setCreateError('当前未连接或正在同步，暂时无法创建，请稍后重试');
      return;
    }
    const value = createKind === 'task' ? createTitle.trim() : createNotes.trim();
    if (!value) {
      setCreateError(createKind === 'task' ? '请输入任务标题' : '请输入目标描述');
      return;
    }
    setCreateBusy(true);
    setCreateError(null);
    try {
      if (createKind === 'task') {
        await createTask(createProjectID, value, createNotes);
      } else if (createKind === 'goal') {
        await createGoal(createProjectID, value);
      }
      setCreateKind(null);
      setCreateTitle('');
      setCreateNotes('');
      if (route.kind === 'home') {
        navigate('#/board');
      }
    } catch (error) {
      setCreateError(error instanceof Error ? error.message : '创建失败');
    } finally {
      setCreateBusy(false);
    }
  };

  const inspectedProjectName = inspectedTask?.projectID
    ? board?.projects.find((project) => project.id === inspectedTask.projectID)?.name
    : undefined;
  const inspectedGoal = route.kind === 'goal'
    ? board?.goals.find((goal) => goal.id === route.goalID) ?? null
    : null;
  const buildMismatch = Boolean(
    connectionState === 'connected' && capabilities?.webBuildID && capabilities.webBuildID !== pageBuildID
  );

  useEffect(() => {
    if (connectionState !== 'connected' || !capabilities?.methods.includes('threads.list')) return;
    void loadThreads(selectedProjectID);
  }, [capabilities, connectionState, loadThreads, selectedProjectID]);

  // 断线重连成功后清掉「已请求过」的标记：本次提交里后面的路由 effect 会看到空 ref，
  // 从而重新调用 openThreadByID，锁屏回来不用再手动返回列表重进会话。
  useEffect(() => {
    const previousConnectionState = previousConnectionStateRef.current;
    if (previousConnectionState !== 'connected' && connectionState === 'connected') {
      requestedThreadIDRef.current = null;
    }
    previousConnectionStateRef.current = connectionState;
  }, [connectionState]);

  useEffect(() => {
    const leftThread = previousRouteKindRef.current === 'thread' && route.kind !== 'thread';
    previousRouteKindRef.current = route.kind;
    if (route.kind === 'thread') {
      if (requestedThreadIDRef.current !== route.threadID) {
        requestedThreadIDRef.current = null;
      }
      const target = threads.find((thread) => thread.id === route.threadID);
      if (target && currentThread?.id !== target.id) {
        requestedThreadIDRef.current = route.threadID;
        openThread(target.id);
      } else if (!target && currentThread?.id !== route.threadID) {
        if (capabilities?.methods.includes('threads.get')) {
          if (connectionState === 'connected' && requestedThreadIDRef.current !== route.threadID) {
            requestedThreadIDRef.current = route.threadID;
            void openThreadByID(route.threadID);
          }
        } else if (currentThread) {
          closeThread();
        }
      }
    } else {
      if (requestedThreadIDRef.current || currentThread) closeThread();
      requestedThreadIDRef.current = null;
      // 返回列表后静默同步一次，让顺序与宿主一致；断线重连期间由 loadThreads 重新加载，不在此请求。
      if (leftThread && connectionState === 'connected' && capabilities?.methods.includes('threads.list')) {
        void resyncThreads();
      }
    }
  }, [capabilities, closeThread, connectionState, currentThread, openThread, openThreadByID, resyncThreads, route, threads]);

  // 应用角标只反映当前列表里的待处理会话数；不支持 App Badge 的环境静默忽略。
  useEffect(() => {
    setPendingBadge(threads.filter((thread) => thread.hasPendingRequest).length);
  }, [threads]);

  useEffect(() => {
    if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
    const handleServiceWorkerMessage = (event: MessageEvent) => {
      if (event.data?.type === 'mcode-open-thread' && typeof event.data.threadID === 'string') {
        const threadID = event.data.threadID;
        const messageID = typeof event.data.messageID === 'string' ? event.data.messageID : '';
        const requestID = typeof event.data.requestID === 'string' ? event.data.requestID : '';
        const query = [
          messageID ? `m=${encodeURIComponent(messageID)}` : '',
          requestID ? `r=${encodeURIComponent(requestID)}` : ''
        ]
          .filter(Boolean)
          .join('&');
        // 已经停在该会话时只换 URL 不压历史；定位由下面的路由 effect 统一分发。
        const replace = route.kind === 'thread' && route.threadID === threadID;
        navigate(`#/t/${encodeURIComponent(threadID)}${query ? `?${query}` : ''}`, replace ? { replace: true } : undefined);
        void openThreadByID(threadID);
      }
    };
    navigator.serviceWorker.addEventListener('message', handleServiceWorkerMessage);
    return () => {
      navigator.serviceWorker.removeEventListener('message', handleServiceWorkerMessage);
    };
  }, [navigate, openThreadByID, route]);

  useEffect(() => {
    if (route.kind !== 'thread' || (!route.messageID && !route.requestID)) {
      // 普通导航（含返回列表）清掉旧的定位请求，避免下次进入同一会话又跳一次。
      setThreadJump(null);
      return;
    }
    threadJumpNonceRef.current += 1;
    setThreadJump({
      threadID: route.threadID,
      messageID: route.messageID,
      requestID: route.requestID,
      nonce: threadJumpNonceRef.current
    });
    // 定位只用一次：去掉 hash 里的查询串，刷新或分享时不再跳。
    if (window.location.hash.includes('?')) {
      replaceHash(window.location.hash.split('?')[0]);
    }
  }, [route, replaceHash]);

  useEffect(() => {
    if (route.kind === 'task') {
      setInspectedTask(board?.tasks.find((task) => task.id === route.taskID) ?? null);
    } else if (route.kind !== 'board') {
      setInspectedTask(null);
    }
  }, [board, route, setInspectedTask]);

  const buildNotice = buildMismatch ? (
    <div className="build-mismatch" role="alert">
      <span className="flex min-w-0 items-center gap-2"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" />页面与 Mac 端版本不一致。</span>
      <button type="button" onClick={() => window.location.reload()}>重新加载</button>
    </div>
  ) : null;

  if (showPairingScreen) return <PairingScreen />;

  if (route.kind === 'thread') {
    const jumpRequest =
      threadJump && threadJump.threadID === route.threadID
        ? { messageID: threadJump.messageID, requestID: threadJump.requestID, nonce: threadJump.nonce }
        : null;
    if (currentThread?.id !== route.threadID) {
      const canGetThread = capabilities?.methods.includes('threads.get');
      const openingError = canGetThread
        ? connectionState !== 'connected' ? '未连接到 Mac' : openThreadError
        : null;
      const disconnected = connectionState !== 'connected';
      const reconnecting = connectionState === 'connecting' || connectionState === 'reconnecting';
      // 断线时给出明确文案与重连入口；重连进行中按钮置灰，避免重复发起。
      const openingStatusText = disconnected
        ? reconnecting
          ? connectionMessage || '正在重连…'
          : canGetThread ? '未连接到 Mac' : connectionMessage || '正在打开会话…'
        : openingError || (canGetThread ? '正在打开会话…' : connectionMessage || '正在打开会话…');
      if (currentThread && canGetThread && !openingError) {
        return (
          <div className="app-shell mobile-page">
            {buildNotice}
            <LazyBoundary variant="screen" label="正在加载会话…" skeleton="thread">
              <MarkdownImageProvider threadID={currentThread?.id ?? null}>
                <ThreadView
                  thread={currentThread}
                  onBack={() => goBack('#/')}
                  subview={route.subview}
                  filePath={route.filePath}
                  jumpRequest={jumpRequest}
                />
              </MarkdownImageProvider>
            </LazyBoundary>
          </div>
        );
      }
      return (
        <div className="app-shell mobile-page">
          {buildNotice}
          <div className="empty-state" role="status">
            {(disconnected ? reconnecting || !canGetThread : !openingError) && <span className="loading-spinner" />}
            <span>{openingStatusText}</span>
            {disconnected && (
              <button type="button" className="primary-button" disabled={reconnecting} onClick={() => reconnect()}>
                重新连接
              </button>
            )}
            {!disconnected && canGetThread && openingError && (
              <button
                type="button"
                className="primary-button"
                onClick={() => {
                  requestedThreadIDRef.current = route.threadID;
                  void openThreadByID(route.threadID);
                }}
              >
                重试
              </button>
            )}
            <button type="button" className="secondary-button" onClick={() => goBack('#/')}>返回会话列表</button>
          </div>
        </div>
      );
    }
    return (
      <div className="app-shell mobile-page">
        {buildNotice}
        <LazyBoundary variant="screen" label="正在加载会话…" skeleton="thread">
          <MarkdownImageProvider threadID={currentThread?.id ?? null}>
            <ThreadView
              thread={currentThread}
              onBack={() => goBack('#/')}
              subview={route.subview}
              filePath={route.filePath}
              jumpRequest={jumpRequest}
            />
          </MarkdownImageProvider>
        </LazyBoundary>
      </div>
    );
  }

  if (route.kind === 'draft') {
    const project = projects.find((candidate) => candidate.id === route.projectID);
    return (
      <div className="app-shell mobile-page">
        {buildNotice}
        {project ? (
          <LazyBoundary variant="screen" label="正在加载新会话…" skeleton="thread">
            <ThreadDraftView
              project={project}
              onBack={() => goBack('#/')}
              onOpenThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}`, { replace: true })}
            />
          </LazyBoundary>
        ) : (
          <div className="empty-state" role="status">
            <span className="loading-spinner" />
            <span>{connectionMessage || '正在读取项目…'}</span>
            <button type="button" className="secondary-button" onClick={() => goBack('#/')}>返回会话列表</button>
          </div>
        )}
      </div>
    );
  }

  const boardScreen = (
    <LazyBoundary label="正在加载看板…">
      <KanbanBoard
        onSelectTask={(task) => {
          setInspectedTask(task);
          navigate(`#/task/${encodeURIComponent(task.id)}`);
        }}
        onSelectGoal={(goal) => navigate(`#/goal/${encodeURIComponent(goal.id)}`)}
      />
    </LazyBoundary>
  );
  const sessionsScreen = (
    <>
      <SecureEntryHint />
      <PushEnableHint />
      <ThreadList
        projectID={selectedProjectID}
        onSelectThread={(thread) => {
          // 点会话行时立即预取，不等空闲回调。
          prefetchThreadViews();
          navigate(`#/t/${encodeURIComponent(thread.id)}`);
        }}
        onCreateDraft={(projectID) => {
          prefetchThreadViews();
          navigate(`#/new/${encodeURIComponent(projectID)}`);
        }}
      />
    </>
  );

  return (
    <div className="app-shell">
      <Header
        activeView={activeView}
        onSwitchView={(view) => navigate(view === 'board' ? '#/board' : '#/', { replace: true })}
        settingsOpen={settingsOpen}
        settingsPageID={settingsPageID}
        onSettingsPageChange={(pageID) => {
          // 切 tab 一律 replace：设置里用系统返回手势或浏览器返回键会直接关闭设置。
          navigate(pageID ? `#/settings/${encodeURIComponent(pageID)}` : '#/settings', {
            replace: true
          });
        }}
        onOpenSettings={() => navigate('#/settings')}
        onCloseSettings={() => goBack('#/')}
      />
      {buildNotice}
      <main className={`app-main${settingsOpen ? '' : ' mobile-page'}`}>
        {boardRoute ? boardScreen : sessionsScreen}
      </main>

      {isHomeOrBoard && (
        <>
          <button
            type="button"
            className="fab-button"
            onClick={() => setFabMenuOpen((open) => !open)}
            aria-label="新建"
            aria-expanded={fabMenuOpen}
            title="新建"
          >
            <PlusIcon className={`h-6 w-6 text-white transition-transform ${fabMenuOpen ? 'rotate-45' : ''}`} />
          </button>
          {fabMenuOpen && (
            <div
              className="fixed inset-0 z-30"
              onClick={() => setFabMenuOpen(false)}
            >
              <div
                className="fixed z-40 min-w-44 overflow-hidden rounded-2xl border border-border bg-card p-1.5 shadow-2xl animate-fadeIn"
                style={{
                  right: 'max(1rem, calc(1rem + var(--sar)))',
                  bottom: 'calc(env(safe-area-inset-bottom, 0px) + 5rem)'
                }}
                onClick={(event) => event.stopPropagation()}
                role="menu"
                aria-label="新建操作"
              >
                <button
                  type="button"
                  className="flex w-full items-center gap-2.5 rounded-xl px-3.5 py-3 text-sm font-medium text-foreground hover:bg-row-hover active:bg-row-hover"
                  onClick={() => {
                    setFabMenuOpen(false);
                    handleNewThread();
                  }}
                  role="menuitem"
                >
                  <MessageIcon className="h-4 w-4 text-primary" />
                  <span>新会话</span>
                </button>
                {canCreateTask && (
                  <button
                    type="button"
                    className="flex w-full items-center gap-2.5 rounded-xl px-3.5 py-3 text-sm font-medium text-foreground hover:bg-row-hover active:bg-row-hover disabled:opacity-40 disabled:pointer-events-none"
                    onClick={() => {
                      setFabMenuOpen(false);
                      setCreateKind('task');
                      setCreateTitle('');
                      setCreateNotes('');
                      setCreateError(null);
                    }}
                    disabled={kanbanWritesBlocked}
                    role="menuitem"
                  >
                    <PlusIcon className="h-4 w-4 text-primary" />
                    <span>新任务</span>
                  </button>
                )}
                {canCreateGoal && (
                  <button
                    type="button"
                    className="flex w-full items-center gap-2.5 rounded-xl px-3.5 py-3 text-sm font-medium text-foreground hover:bg-row-hover active:bg-row-hover disabled:opacity-40 disabled:pointer-events-none"
                    onClick={() => {
                      setFabMenuOpen(false);
                      setCreateKind('goal');
                      setCreateNotes('');
                      setCreateError(null);
                    }}
                    disabled={kanbanWritesBlocked}
                    role="menuitem"
                  >
                    <SparklesIcon className="h-4 w-4 text-primary" />
                    <span>新目标</span>
                  </button>
                )}
              </div>
            </div>
          )}
        </>
      )}

      {createKind === 'thread' && (
        <div className="sheet-layer" role="presentation" onMouseDown={() => setCreateKind(null)}>
          <section ref={createSheetRef} className="create-sheet" role="dialog" aria-modal="true" aria-labelledby="create-thread-title" onMouseDown={(event) => event.stopPropagation()}>
            <div ref={createSheetHandleRef} className="sheet-handle" aria-hidden="true" />
            <div className="flex items-center justify-between gap-3">
              <div><p className="eyebrow">新建</p><h2 id="create-thread-title" className="sheet-title">新会话</h2></div>
              <button type="button" className="icon-button" onClick={() => setCreateKind(null)} aria-label="关闭新建会话" title="关闭"><XMarkIcon className="h-4 w-4" /></button>
            </div>
            <label className="settings-label mt-4" htmlFor="app-new-thread-project">项目</label>
            <select id="app-new-thread-project" value={createProjectID} onChange={(event) => setCreateProjectID(event.target.value)} className="sheet-select">
              <option value="">选择一个项目</option>
              {projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
            </select>
            {canAddProject && (
              <button
                type="button"
                className="secondary-button mt-2 w-full"
                onClick={() => {
                  setCreateKind(null);
                  setAddProjectOpen(true);
                }}
              >
                <PlusIcon className="h-4 w-4" />
                <span>添加项目</span>
              </button>
            )}
            <button
              type="button"
              className="primary-button primary-button--wide mt-4"
              onClick={() => {
                const targetProjectID = createProjectID;
                setCreateKind(null);
                navigate(`#/new/${encodeURIComponent(targetProjectID)}`);
              }}
              disabled={!createProjectID}
            >
              继续编辑
            </button>
          </section>
        </div>
      )}

      {(createKind === 'task' || createKind === 'goal') && (
        <div className="sheet-layer" role="presentation" onMouseDown={() => !createBusy && setCreateKind(null)}>
          <section className="create-sheet" role="dialog" aria-modal="true" aria-labelledby="create-kanban-title" onMouseDown={(event) => event.stopPropagation()}>
            <div className="sheet-handle" aria-hidden="true" />
            <div className="flex items-center justify-between gap-3">
              <div><p className="eyebrow">看板</p><h2 id="create-kanban-title" className="sheet-title">{createKind === 'task' ? '新任务' : '新目标'}</h2></div>
              <button type="button" className="icon-button" onClick={() => setCreateKind(null)} disabled={createBusy} aria-label="关闭" title="关闭"><XMarkIcon className="h-4 w-4" /></button>
            </div>
            <form className="mt-4 space-y-3" onSubmit={submitCreate}>
              <label className="settings-label" htmlFor="app-kanban-create-project">项目</label>
              <select id="app-kanban-create-project" value={createProjectID} onChange={(event) => setCreateProjectID(event.target.value)} className="sheet-select" disabled={createBusy}>
                <option value="">选择一个项目</option>
                {(board?.projects ?? projects).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
              </select>
              {createKind === 'task' ? (
                <>
                  <label className="settings-label" htmlFor="app-kanban-task-title">标题</label>
                  <input id="app-kanban-task-title" value={createTitle} onChange={(event) => setCreateTitle(event.target.value)} className="pairing-input" placeholder="要完成什么？" disabled={createBusy} autoFocus />
                  <label className="settings-label" htmlFor="app-kanban-task-notes">说明</label>
                  <textarea id="app-kanban-task-notes" value={createNotes} onChange={(event) => setCreateNotes(event.target.value)} className="pairing-input min-h-24 resize-y" placeholder="可选" disabled={createBusy} />
                </>
              ) : (
                <>
                  <label className="settings-label" htmlFor="app-kanban-goal-prompt">目标描述</label>
                  <textarea id="app-kanban-goal-prompt" value={createNotes} onChange={(event) => setCreateNotes(event.target.value)} className="pairing-input min-h-28 resize-y" placeholder="一句话描述希望 Mac 完成的目标" disabled={createBusy} autoFocus />
                  <p className="settings-note">目标使用 Mac 默认策略：串行、先预览、自动验收、最多两次尝试。</p>
                </>
              )}
              {createError && <p className="settings-error" role="alert">{createError}</p>}
              <button type="submit" className="primary-button primary-button--wide" disabled={createBusy || kanbanWritesBlocked || !createProjectID || (createKind === 'task' ? !createTitle.trim() : !createNotes.trim())} data-host-actionable="true">
                {createBusy ? '提交中…' : '创建'}
              </button>
            </form>
          </section>
        </div>
      )}

      {addProjectOpen && canAddProject && (
        <LazyBoundary>
          <AddProjectSheet onClose={() => setAddProjectOpen(false)} onAdded={(project) => handleProjectAdded(project.id)} />
        </LazyBoundary>
      )}

      {inspectedTask && route.kind === 'task' && (
        <LazyBoundary label="正在加载任务详情…">
          <TaskDetailModal
            task={inspectedTask}
            projectName={inspectedProjectName}
            onClose={() => {
              setInspectedTask(null);
              goBack('#/board');
            }}
            onOpenThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}`)}
            onOpenReviewThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}`)}
            onOpenGoal={(goalID) => navigate(`#/goal/${encodeURIComponent(goalID)}`)}
          />
        </LazyBoundary>
      )}
      {inspectedGoal && route.kind === 'goal' && (
        <LazyBoundary label="正在加载目标详情…">
          <GoalDetailModal
            goal={inspectedGoal}
            projectName={board?.projects.find((project) => project.id === inspectedGoal.projectID)?.name}
            tasks={board?.tasks ?? []}
            onClose={() => goBack('#/board')}
            onOpenTask={(task) => {
              setInspectedTask(task);
              navigate(`#/task/${encodeURIComponent(task.id)}`);
            }}
            onOpenThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}`)}
            onOpenReviewThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}/review`)}
          />
        </LazyBoundary>
      )}
    </div>
  );
};

export const App: React.FC = () => (
  <RemoteProvider>
    <AppContent />
  </RemoteProvider>
);

export default App;
