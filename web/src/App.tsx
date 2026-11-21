import { CreateMenu } from './app/CreateMenu';
import { CreateSheet } from './app/CreateSheet';
import { useHashRoute } from './app/route';
import { PairingScreen } from './app/PairingScreen';
import { SheetFrame } from './components/sheet/SheetFrame';
// mcode 移动首页：默认会话视图，保留看板作为同一首页的第二个视图。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { RemoteProvider, useRemote } from './context/RemoteContext';
import { useEdgeSwipeBack } from './hooks/useEdgeSwipeBack';
import { useSheetDrag } from './hooks/useSheetDrag';
import { Header, type HomeView } from './components/Header';
import { ThreadList } from './components/ThreadList';
import { SecureEntryHint } from './components/SecureEntryHint';
import { PushEnableHint } from './components/PushEnableHint';
import { LazyBoundary } from './components/LazyFallback';
import { ExclamationTriangleIcon, PlusIcon, XMarkIcon } from './components/Icons';
import { useRemoteCall, useRemoteNotification } from './context/useRemoteCall';
import { PROJECTS_ADD_METHOD, PROJECTS_CHANGED_METHOD } from './context/remoteProjectApi';
import { MarkdownImageProvider } from './context/MarkdownImageContext';
import { setPendingBadge } from './services/pushNotifications';
import { loadThreadDraftView, loadThreadView, prefetchThreadViews } from './utils/lazyThreadViews';
import { clearHomeQuery } from './utils/homeSearch';
import { useIsWideSheet } from './components/thread/menu/useIsWideSheet';
import { usesSplitLayout, threadOpenMode } from './utils/splitLayout';
import { chatsProject, pickableProjects } from './utils/builtinProjects';

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
const AutomationPanel = React.lazy(() =>
  import('./components/AutomationSheet').then((module) => ({ default: module.AutomationPanel }))
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

function getPageBuildID(): string {
  return document.querySelector('meta[name="mcode-build-id"]')?.getAttribute('content') || 'dev';
}

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
  const isWide = useIsWideSheet();
  const split = usesSplitLayout(isWide, route.kind);
  const threadNavigationOptions = threadOpenMode(isWide, route.kind) === 'replace' ? { replace: true } : undefined;
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
  const [headerActionsSlot, setHeaderActionsSlot] = useState<HTMLDivElement | null>(null);
  const [headerLeadingSlot, setHeaderLeadingSlot] = useState<HTMLDivElement | null>(null);

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
  const automationsRoute = route.kind === 'automations';
  const activeView: HomeView = boardRoute ? 'board' : automationsRoute ? 'automations' : 'sessions';
  const settingsOpen = route.kind === 'settings';
  const settingsPageID = route.kind === 'settings' ? route.page ?? null : null;
  const isHomeOrBoard = route.kind === 'home' || route.kind === 'board';
  const isTopLevelTab = isHomeOrBoard || automationsRoute;

  // 新建会话 sheet 只接管把手拖动关闭（W-4）；非首页启用左边缘右滑返回（W-8）。
  const createSheetRef = useRef<HTMLElement | null>(null);
  const createSheetHandleRef = useRef<HTMLDivElement | null>(null);
  const closeCreateSheet = useCallback(() => setCreateKind(null), []);
  useSheetDrag(createSheetRef, createSheetHandleRef, closeCreateSheet, createKind === 'thread');
  useEdgeSwipeBack({
    enabled: !isTopLevelTab && !showPairingScreen && !split,
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
    setCreateProjectID(selectedProjectID ?? chatsProject(projects)?.id ?? projects[0]?.id ?? '');
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
      .then(() => navigate(`#/new/${encodeURIComponent(projectID)}`, threadNavigationOptions));
  };

  const handleNewThread = () => {
    prefetchThreadViews();
    const chats = chatsProject(projects);
    if (selectedProjectID) {
      navigate(`#/new/${encodeURIComponent(selectedProjectID)}`, threadNavigationOptions);
    } else if (chats) {
      navigate(`#/new/${encodeURIComponent(chats.id)}`, threadNavigationOptions);
    } else if (projects.length === 1) {
      navigate(`#/new/${encodeURIComponent(projects[0].id)}`, threadNavigationOptions);
    } else {
      setCreateProjectID(chatsProject(projects)?.id ?? projects[0]?.id ?? '');
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

  let detailPane: React.ReactNode = null;
  if (route.kind === 'thread') {
    const jumpRequest = threadJump && threadJump.threadID === route.threadID
      ? { messageID: threadJump.messageID, requestID: threadJump.requestID, nonce: threadJump.nonce }
      : null;
    const canGetThread = capabilities?.methods.includes('threads.get');
    const openingError = canGetThread ? connectionState !== 'connected' ? '未连接到 Mac' : openThreadError : null;
    const disconnected = connectionState !== 'connected';
    const reconnecting = connectionState === 'connecting' || connectionState === 'reconnecting';
    const openingStatusText = disconnected
      ? reconnecting ? connectionMessage || '正在重连…' : canGetThread ? '未连接到 Mac' : connectionMessage || '正在打开会话…'
      : openingError || (canGetThread ? '正在打开会话…' : connectionMessage || '正在打开会话…');
    if (currentThread && (currentThread.id === route.threadID || (canGetThread && !openingError))) {
      detailPane = (
        <LazyBoundary variant="screen" label="正在加载会话…" skeleton="thread">
          <MarkdownImageProvider threadID={currentThread.id}>
            <ThreadView
              key={split ? currentThread.id : undefined}
              thread={currentThread}
              onBack={split ? undefined : () => goBack('#/')}
              subview={route.subview}
              filePath={route.filePath}
              jumpRequest={jumpRequest}
              onOpenTask={(taskID) => navigate(`#/task/${encodeURIComponent(taskID)}`)}
            />
          </MarkdownImageProvider>
        </LazyBoundary>
      );
    } else {
      const threadID = route.threadID;
      detailPane = (
        <div className="empty-state" role="status">
          {(disconnected ? reconnecting || !canGetThread : !openingError) && <span className="loading-spinner" />}
          <span>{openingStatusText}</span>
          {disconnected && (
            <button type="button" className="primary-button" disabled={reconnecting} onClick={() => reconnect()}>
              重新连接
            </button>
          )}
          {!disconnected && canGetThread && openingError && (
            <button type="button" className="primary-button" onClick={() => {
              requestedThreadIDRef.current = threadID;
              void openThreadByID(threadID);
            }}>重试</button>
          )}
          {!split && <button type="button" className="secondary-button" onClick={() => goBack('#/')}>返回会话列表</button>}
        </div>
      );
    }
  } else if (route.kind === 'draft') {
    const project = projects.find((candidate) => candidate.id === route.projectID);
    detailPane = project ? (
      <LazyBoundary variant="screen" label="正在加载新会话…" skeleton="thread">
        <ThreadDraftView
          project={project}
          onBack={split ? undefined : () => goBack('#/')}
          onOpenThread={(threadID) => navigate(`#/t/${encodeURIComponent(threadID)}`, { replace: true })}
        />
      </LazyBoundary>
    ) : (
      <div className="empty-state" role="status">
        <span className="loading-spinner" />
        <span>{connectionMessage || '正在读取项目…'}</span>
        {!split && <button type="button" className="secondary-button" onClick={() => goBack('#/')}>返回会话列表</button>}
      </div>
    );
  }
  if (!split && (route.kind === 'thread' || route.kind === 'draft')) {
    return <div className="app-shell mobile-page">{buildNotice}{detailPane}</div>;
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
  const automationsScreen = (
    <LazyBoundary label="正在加载自动化…">
      <AutomationPanel
        projectID={selectedProjectID}
        initialAutomationID={route.kind === 'automations' ? route.automationID ?? null : null}
        onSelectAutomation={(id) =>
          navigate(id ? '#/automations/' + encodeURIComponent(id) : '#/automations', { replace: true })
        }
        onOpenThread={(threadID) => {
          prefetchThreadViews();
          navigate(`#/t/${encodeURIComponent(threadID)}`);
        }}
      />
    </LazyBoundary>
  );
  const sessionsScreen = (
    <>
      <SecureEntryHint />
      <PushEnableHint />
      <ThreadList
        headerActionsSlot={headerActionsSlot}
        headerLeadingSlot={headerLeadingSlot}
        projectID={selectedProjectID}
        selectedThreadID={split && route.kind === 'thread' ? route.threadID : null}
        onSelectThread={(thread) => {
          // 点会话行时立即预取，不等空闲回调。
          prefetchThreadViews();
          navigate(`#/t/${encodeURIComponent(thread.id)}`, threadNavigationOptions);
        }}
        onCreateDraft={(projectID) => {
          prefetchThreadViews();
          navigate(`#/new/${encodeURIComponent(projectID)}`, threadNavigationOptions);
        }}
      />
    </>
  );

  const sidebar = (
    <>
      <Header
        actionsSlotRef={setHeaderActionsSlot}
        leadingSlotRef={setHeaderLeadingSlot}
        activeView={activeView}
        onSwitchView={(view) => {
          if (view !== 'sessions') {
            try { clearHomeQuery(window.sessionStorage); } catch { /* 存储不可用时忽略。 */ }
          }
          const target = view === 'board' ? '#/board' : view === 'automations' ? '#/automations' : '#/';
          navigate(target, { replace: true });
        }}
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
      <main className={`app-main${split || settingsOpen ? '' : ' mobile-page'}`}>
        {boardRoute ? boardScreen : automationsRoute ? automationsScreen : sessionsScreen}
      </main>

      {(isHomeOrBoard || split) && (
        <CreateMenu split={split} fabMenuOpen={fabMenuOpen} setFabMenuOpen={setFabMenuOpen}
          handleNewThread={handleNewThread} canCreateTask={canCreateTask} canCreateGoal={canCreateGoal}
          kanbanWritesBlocked={kanbanWritesBlocked} setCreateKind={setCreateKind} setCreateTitle={setCreateTitle}
          setCreateNotes={setCreateNotes} setCreateError={setCreateError} />
      )}

    </>
  );

  return (
    <div className={split ? 'app-shell split-shell' : 'app-shell'}>
      {split ? (
        <>
          <aside className="split-sidebar">{sidebar}</aside>
          <section className="split-detail">{detailPane}</section>
        </>
      ) : sidebar}
      {createKind === 'thread' && (
        <SheetFrame onRequestClose={() => setCreateKind(null)} size="bare" cardClassName="create-sheet" cardRef={createSheetRef} labelledBy="create-thread-title">
            <div ref={createSheetHandleRef} className="sheet-handle" aria-hidden="true" />
            <div className="flex items-center justify-between gap-3">
              <div><p className="eyebrow">新建</p><h2 id="create-thread-title" className="sheet-title">新会话</h2></div>
              <button type="button" className="icon-button" onClick={() => setCreateKind(null)} aria-label="关闭新建会话" title="关闭"><XMarkIcon className="h-4 w-4" /></button>
            </div>
            <label className="settings-label mt-4" htmlFor="app-new-thread-project">项目</label>
            <select id="app-new-thread-project" value={createProjectID} onChange={(event) => setCreateProjectID(event.target.value)} className="sheet-select">
              <option value="">选择一个项目</option>
              {pickableProjects(projects).map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
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
                navigate(`#/new/${encodeURIComponent(targetProjectID)}`, threadNavigationOptions);
              }}
              disabled={!createProjectID}
            >
              继续编辑
            </button>
          </SheetFrame>
      )}

      <CreateSheet createKind={createKind} setCreateKind={setCreateKind} createBusy={createBusy}
        createProjectID={createProjectID} setCreateProjectID={setCreateProjectID} projects={board?.projects ?? projects}
        createTitle={createTitle} setCreateTitle={setCreateTitle} createNotes={createNotes} setCreateNotes={setCreateNotes}
        createError={createError} kanbanWritesBlocked={kanbanWritesBlocked} submitCreate={submitCreate} />

      {addProjectOpen && canAddProject && (
        <LazyBoundary>
          <AddProjectSheet onClose={() => setAddProjectOpen(false)} onAdded={(project) => handleProjectAdded(project.id)} />
        </LazyBoundary>
      )}

      {route.kind === 'task' && (inspectedTask ? (
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
      ) : (
        <SheetFrame
          onRequestClose={() => {
            setInspectedTask(null);
            goBack('#/board');
          }}
          size="bare"
          cardClassName="settings-sheet flex flex-col outline-none w-full max-w-full min-w-0 overflow-x-hidden"
          ariaLabel="任务详情"
        >
          <div className="sheet-handle shrink-0" aria-hidden="true" />
          <div className="flex shrink-0 items-start justify-between gap-3">
            <div>
              <p className="eyebrow">任务详情</p>
              <h2 className="mt-1 text-headline font-bold text-foreground">
                {!board || isSyncing ? '正在加载任务详情…' : '未找到该任务'}
              </h2>
            </div>
            <button
              type="button"
              onClick={() => {
                setInspectedTask(null);
                goBack('#/board');
              }}
              aria-label="关闭"
              title="关闭"
              className="icon-button shrink-0"
            >
              <XMarkIcon className="h-4 w-4" />
            </button>
          </div>
          <div className="empty-state py-8">
            {!board || isSyncing ? (
              <>
                <span className="loading-spinner" />
                <p>正在同步看板任务数据，请稍候…</p>
              </>
            ) : (
              <p>该任务可能已被删除，或尚未同步到当前看板。</p>
            )}
            <button
              type="button"
              className="secondary-button mt-2"
              onClick={() => {
                setInspectedTask(null);
                navigate('#/board');
              }}
            >
              返回看板
            </button>
          </div>
        </SheetFrame>
      ))}
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
