// 会话内容页：渲染会话消息历史，按游标分页加载更早内容，并按能力声明支持续聊、请求答复和停止。

import React, { lazy, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import {
  RemoteThreadProjectionPayload,
  RemoteThreadStreamSnapshot,
  RemoteThreadSummary,
  RemoteAttachmentUploadResult,
  RemoteComposerOptions
} from '../types/contract';
import { useRemote } from '../context/RemoteContext';
import { makeActionID, isThreadBusy, mergeThreadSummary } from '../context/remoteGuards';
import { isPreviewableImageFile } from '../context/remoteImages';
import { RpcError } from '../services/rpc';
import type {
  RemoteThreadAction,
  RemoteThreadSubscriptionHandle,
  RemoteThreadSubscriptionHandlers
} from '../context/RemoteContext';
import {
  ExclamationTriangleIcon,
  ChevronRightIcon,
  ArrowPathIcon,
  ChevronDownIcon,
  XMarkIcon
} from './Icons';
import {
  ThreadTimeline,
  buildTimelineItems,
  isEndedStatus,
  isWorkEntry,
  mergeLatestHistoryPage,
  mergeThreadMessages,
  isHiddenTimelineMessage,
  workRecordExpandedMap
} from './ThreadTimeline';
import type { CopyVariant, MessageMenuActions, ThreadMessage } from './ThreadTimeline';
import { backgroundWaitCount, composerPlaceholderFor } from '../utils/backgroundWait';
import { resumeRetryDelay } from '../utils/resumeRefresh';
import { lastCodeBlock, toPlainText } from '../utils/markdown';
import { modelDisplayName } from '../utils/modelLabel';
import { ThreadComposer, composerAttachmentLimit } from './ThreadComposer';
import type { ThreadComposerHandle } from './ThreadComposer';
import { LazyBoundary } from './LazyFallback';
import { useThreadSwarm, isSwarmCapabilitySupported } from '../context/remoteSwarm';
import { ConfirmDialog } from './ConfirmDialog';
import { ThreadViewHeader } from './thread/ThreadViewHeader';
import { menuBaseForRoute } from './thread/menu/menuRouteOpen';
import type { ThreadMenuPage, ThreadMenuPageHandle, ThreadMenuPageSpec } from './thread/menu/menuPages';
import type { SaveTaskBodyProps } from './thread/SaveTaskForm';
import { stewardPendingDecision } from './timeline/stewardTimeline';
import { QueuedFollowUpSection } from './thread/QueuedFollowUpSection';
import { ContinueTurnPill } from './thread/ContinueTurnPill';
import { QuestionMinimap, buildQuestionMinimapData } from './thread/QuestionMinimap';
import { TodoProgressPill } from './thread/TodoProgressPill';
import {
  isUnresolvedAction,
  linkedTaskColumnLabel,
  getHelperStatusDisplay
} from './thread/threadHelpers';
import { useInitialPinToLatest } from './thread/useInitialPinToLatest';
import { collapseWorkRecordState, pickExpandedWorkRecordTurnID } from './thread/expandedWorkRecord';
import { useSendAnchor } from './thread/useSendAnchor';
import { usePinSentMessage } from './thread/usePinSentMessage';
import { useHelperStop } from './thread/useHelperStop';
import { useThreadMetaActions } from './thread/useThreadMetaActions';
import { useThreadShareActions } from './thread/useThreadShareActions';
import { useRemoteRevertApi } from '../context/remoteRevertApi';
import { RevertTurnSheet } from './thread/RevertTurnSheet';
import { EditMessageSheet } from './thread/EditMessageSheet';
import type { RuntimeCompactSuggestionParams } from '../types/generated';
import { clearThreadDraft, loadThreadDraft, saveThreadDraft } from '../services/draftStorage';
import { changedComposerOptions, rememberParamsForChange, summaryComposerOptions } from '../utils/composerOptions';
import type { ComposerSelectionMemoryParams } from '../utils/composerOptions';
import { localCommandName, parseDelegation } from '../utils/slashCommands';
import { composerHistoryEntries } from '../utils/composerHistory';
import { findMatches } from '../utils/findHighlight';
import type { FindMatch } from '../utils/findHighlight';
import { isErrorMessage } from '../utils/continuation';
import { stripReferencedThreadBlock } from '../utils/threadMention';
import { useTurnElapsedSeconds } from '../hooks/useTurnElapsedSeconds';
import { splitSessionViewFor, useRemoteSplitApi } from '../context/remoteSplitApi';
import { countUnreadMessages, createUnreadAnchor, formatUnreadCount } from '../utils/unreadCount';
import { createFrameCoalescer } from '../utils/frameCoalescer';
import type { UnreadAnchor } from '../utils/unreadCount';

// 抽屉与请求面板只在用户打开（或确实有待处理请求）时才渲染：懒加载拆出它们的代码，会话页首屏不必等这些包。
// load* 常量单独提出来，供预取（打开菜单时后台下载二级抽屉的 chunk）复用同一份 import。
const loadSwarmBeesSheet = () => import('./SwarmBeesSheet');
const loadThreadReviewSheet = () => import('./ThreadReviewSheet');
const loadGitStatusSheet = () => import('./GitStatusSheet');
const loadScriptsSheet = () => import('./ScriptsSheet');
const loadFileBrowserSheet = () => import('./FileBrowserSheet');
const loadThreadMenuSheet = () => import('./thread/ThreadMenuSheet');
const loadBeeMenuSheet = () => import('./thread/BeeMenuSheet');
const loadBranchConfirmDialog = () => import('./thread/BranchConfirmDialog');
const loadForwardConfirmDialog = () => import('./thread/ForwardConfirmDialog');
const loadSaveTaskForm = () => import('./thread/SaveTaskForm');

// 菜单栈只挂内容体：外壳、标题栏与遮罩统一由 ThreadMenuSheet 负责
const SwarmBeesSheetBody = lazy(() =>
  loadSwarmBeesSheet().then((module) => ({ default: module.SwarmBeesSheetBody }))
);
const ThreadReviewSheetBody = lazy(() =>
  loadThreadReviewSheet().then((module) => ({ default: module.ThreadReviewSheetBody }))
);
const GitStatusSheetBody = lazy(() =>
  loadGitStatusSheet().then((module) => ({ default: module.GitStatusSheetBody }))
);
const ScriptsSheetBody = lazy(() =>
  loadScriptsSheet().then((module) => ({ default: module.ScriptsSheetBody }))
);
const FileBrowserSheetBody = lazy(() =>
  loadFileBrowserSheet().then((module) => ({ default: module.FileBrowserSheetBody }))
);
// 待处理请求面板（内部还有 DiffViewer / PlanReaderSheet）只在有请求时才出现，同样按需加载。
const RemoteRequestPanel = lazy(() =>
  import('./RemoteRequestPanel').then((module) => ({ default: module.RemoteRequestPanel }))
);
// 会话页的菜单 sheet、确认弹窗与查找条都只在用户点开后挂载：一起拆出 ThreadView 自身的块，首屏不必等这些代码。
const ThreadMenuSheet = lazy(() =>
  loadThreadMenuSheet().then((module) => ({ default: module.ThreadMenuSheet }))
);
const BeeMenuSheet = lazy(() =>
  loadBeeMenuSheet().then((module) => ({ default: module.BeeMenuSheet }))
);
const BranchConfirmDialog = lazy(() =>
  loadBranchConfirmDialog().then((module) => ({ default: module.BranchConfirmDialog }))
);
const ForwardConfirmBody = lazy(() =>
  loadForwardConfirmDialog().then((module) => ({ default: module.ForwardConfirmBody }))
);
const SaveTaskBody = lazy(() =>
  loadSaveTaskForm().then((module) => ({ default: module.SaveTaskBody }))
);

// 存为看板任务页：SaveTaskBody 的 onClose 只把 saveTaskOpen 置 false（取消与保存成功都走这里），
// 等这个标记真正落下后再统一出栈一次；若直接把出栈函数传进去，保存成功的路径会连退两层把菜单关掉。
type SaveTaskPageProps = Omit<SaveTaskBodyProps, 'onClose'> & {
  saveTaskOpen: boolean;
  onDismiss: () => void;
};

const SaveTaskPage = React.forwardRef<ThreadMenuPageHandle, SaveTaskPageProps>(
  function SaveTaskPage({ saveTaskOpen, onDismiss, setSaveTaskOpen, ...bodyProps }, ref) {
    const previousOpenRef = useRef(saveTaskOpen);
    useEffect(() => {
      const wasOpen = previousOpenRef.current;
      previousOpenRef.current = saveTaskOpen;
      if (wasOpen && !saveTaskOpen) onDismiss();
    }, [saveTaskOpen, onDismiss]);
    return (
      <SaveTaskBody
        {...bodyProps}
        ref={ref}
        setSaveTaskOpen={setSaveTaskOpen}
        onClose={() => setSaveTaskOpen(false)}
      />
    );
  }
);
const SwarmQuestionBanner = lazy(() =>
  import('./thread/SwarmQuestionBanner').then((module) => ({ default: module.SwarmQuestionBanner }))
);
const ThreadFindBar = lazy(() =>
  import('./thread/ThreadFindBar').then((module) => ({ default: module.ThreadFindBar }))
);

interface ThreadViewProps {
  thread: RemoteThreadSummary;
  onBack?: () => void;
  subview?: 'agents' | 'review' | 'git' | 'files' | 'scripts' | null;
  filePath?: string;
  /** 推送点开时的一次性定位：messageID 定位消息，requestID 定位审批面板；nonce 变化即重新执行 */
  jumpRequest?: { messageID?: string; requestID?: string; nonce: number } | null;
}

// 单页请求条数固定，实际上限由宿主决定
const PAGE_SIZE = 50;

// 草稿按会话隔离：内存 Map 作前置缓存，首次读取时回读 draftStorage（刷新后仍在），写入防抖落盘。
const threadDrafts = new Map<string, string>();
// 仅来自持久化、本次运行尚未编辑的草稿；未决发送动作的正文恢复优先于它。
const restoredDraftIDs = new Set<string>();
const DRAFT_PERSIST_DELAY_MS = 250;

function readThreadDraft(threadID: string): string | undefined {
  const cached = threadDrafts.get(threadID);
  if (cached !== undefined) return cached;
  const stored = loadThreadDraft(threadID);
  if (stored === null) return undefined;
  threadDrafts.set(threadID, stored);
  restoredDraftIDs.add(threadID);
  return stored;
}
const POLL_INTERVAL_MS = 2500;

export const ThreadView: React.FC<ThreadViewProps> = ({ thread, onBack, subview, filePath, jumpRequest }) => {
  const {
    getThreadHistory,
    board,
    connectionState,
    isSyncing,
    capabilities,
    threads,
    currentThread,
    threadActions,
    sendThreadMessage,
    checkThreadAction,
    dismissThreadAction,
    refreshThread,
    updateThread,
    reconnect,
    requestSnapshots,
    listRequests,
    respondToRequest,
    interruptThread,
    killRunningCommand,
    queueSnapshots,
    listThreadQueue,
    interruptAndSendThread,
    recallTurn,
    compactThread,
    forwardSwarmQuestion,
    subscribeCompactSuggestion,
    uploadAttachment,
    projects,
    providers,
    providersLoading,
    providersError,
    subscribeThread,
    retryThreadSubscription,
    unsubscribeThread,
    branchThread,
    forwardThread,
    readThreadMedia,
    createTask,
    refreshBoard,
    loadThreads,
    createThread,
    rememberProviderSelection
  } = useRemote();
  const isConnected = connectionState === 'connected';

  const [messages, setMessages] = useState<ThreadMessage[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [historyInitialDone, setHistoryInitialDone] = useState(false);
  const [latestSummary, setLatestSummary] = useState<RemoteThreadSummary>(thread);
  const [draft, setDraft] = useState(() => readThreadDraft(thread.id) ?? '');
  const [attachments, setAttachments] = useState<RemoteAttachmentUploadResult[]>([]);
  /** 输入区的图片缩略图：附件上传结果里没有可直接渲染的地址，用本地 object URL 兜底 */
  const [attachmentPreviews, setAttachmentPreviews] = useState<Record<string, string>>({});
  const [isUploadingAttachment, setIsUploadingAttachment] = useState(false);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);
  const [sendError, setSendError] = useState<string | null>(null);
  const [actionQueryError, setActionQueryError] = useState<string | null>(null);
  const [pollError, setPollError] = useState<string | null>(null);
  // 跟进队列快照轮询失败：队列组件内部只管自己的动作错误，读取失败在这里显示
  const [queuePollError, setQueuePollError] = useState<string | null>(null);
  const [streamError, setStreamError] = useState<string | null>(null);
  const [streamState, setStreamState] = useState<'disabled' | 'subscribing' | 'subscribed' | 'disconnected' | 'error'>('disabled');
  const [isPolling, setIsPolling] = useState(false);
  const [isSending, setIsSending] = useState(false);
  const [checkingActionID, setCheckingActionID] = useState<string | null>(null);
  const [requestLoadError, setRequestLoadError] = useState<string | null>(null);
  const [interruptState, setInterruptState] = useState<'idle' | 'stopping'>('idle');
  const [interruptError, setInterruptError] = useState<string | null>(null);
  // 「终止此命令」：正在终止的消息 id 与失败提示
  const [killingCommandID, setKillingCommandID] = useState<string | null>(null);
  const [killCommandError, setKillCommandError] = useState<string | null>(null);
  // 会话内查找：关键词、是否区分大小写与当前命中游标（命中项按时间顺序，逐字计数）
  const [findOpen, setFindOpen] = useState(false);
  const [findQuery, setFindQuery] = useState('');
  const [findCaseSensitive, setFindCaseSensitive] = useState(false);
  const [findCursor, setFindCursor] = useState(0);
  // 总管决策条：用户关掉过的答复 id；点击标题时用一个短命的 id 借用时间线的定位能力
  const [dismissedDecisionIDs, setDismissedDecisionIDs] = useState<Set<string>>(() => new Set());
  const [decisionFocusID, setDecisionFocusID] = useState<string | null>(null);
  const decisionFocusTimerRef = useRef<number | null>(null);
  // 用户在本端点了停止且已成功：继续胶囊据此显示「上一轮已中断」，新一轮开始或再次发送时清除
  const [locallyInterrupted, setLocallyInterrupted] = useState(false);
  const [interruptAndSendState, setInterruptAndSendState] = useState<'idle' | 'sending'>('idle');
  const pendingInterruptSendRef = useRef<{ actionID: string; text: string; attachmentIDs: string } | null>(null);
  // 撤回本轮 / 转交提问 / 压缩这类内联动作的错误，统一显示在输入框上方的错误条
  const [threadActionError, setThreadActionError] = useState<string | null>(null);
  const [recallConfirmOpen, setRecallConfirmOpen] = useState(false);
  const [isRecalling, setIsTakingBack] = useState(false);
  // 宿主的上下文压缩建议（threads.compactSuggestion）
  const [compactSuggestion, setCompactSuggestion] = useState<RuntimeCompactSuggestionParams | null>(null);
  const [compactState, setCompactState] = useState<'idle' | 'compacting'>('idle');
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  // 跨过滚动区底边的展开工作记录：非空时右下角胶囊多出「收起」段。
  const [activeExpandedTurnID, setActiveExpandedTurnID] = useState<string | null>(null);
  // 「跳到最新」按钮的未读基线：按钮第一次出现（离开底部）时记录，回到底部或切会话时清空。
  const [unreadAnchor, setUnreadAnchor] = useState<UnreadAnchor | null>(null);
  const unreadCount = useMemo(
    () =>
      unreadAnchor && unreadAnchor.threadID === thread.id
        ? countUnreadMessages(messages, unreadAnchor)
        : 0,
    [messages, thread.id, unreadAnchor]
  );
  const [menuSheetOpen, setMenuSheetOpen] = useState(false);
  // 菜单栈底页：深链接/刷新/时间线入口直接以该详情页为栈底；菜单关闭时重置回主页
  const [menuInitialPage, setMenuInitialPage] = useState<ThreadMenuPageSpec>({ kind: 'main' });
  // 路由同步 effect 只看菜单当前是否已打开：已打开时入栈由菜单自己负责，不能再重设栈底
  const menuSheetOpenRef = useRef(menuSheetOpen);
  menuSheetOpenRef.current = menuSheetOpen;
  // 头部 ⋯ 按钮：宽屏时菜单以它为锚点定位
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  // 子代理页的精简「…」菜单（返回主会话/停止子代理/审阅待合并/复制链接）
  const [beeMenuSheetOpen, setBeeMenuSheetOpen] = useState(false);
  // 预取：打开「…」菜单后，用户多半会继续点二级抽屉，后台把它们的 chunk 先下载好。
  useEffect(() => {
    if (!menuSheetOpen) return;
    for (const load of [
      loadSwarmBeesSheet,
      loadThreadReviewSheet,
      loadGitStatusSheet,
      loadScriptsSheet,
      loadFileBrowserSheet,
      loadSaveTaskForm,
      loadForwardConfirmDialog,
    ]) {
      void load().catch(() => {});
    }
  }, [menuSheetOpen]);
  // 菜单本身在空闲时预取，不与会话页首屏争带宽。
  useEffect(() => {
    let cancelled = false;
    const prefetch = () => {
      if (cancelled) return;
      void loadThreadMenuSheet().catch(() => {});
      void loadBeeMenuSheet().catch(() => {});
    };
    if (typeof window.requestIdleCallback === 'function') {
      const idleHandle = window.requestIdleCallback(prefetch);
      return () => {
        cancelled = true;
        window.cancelIdleCallback(idleHandle);
      };
    }
    const timer = window.setTimeout(prefetch, 1500);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, []);
  const [composerOptions, setComposerOptions] = useState<RemoteComposerOptions>(() => summaryComposerOptions(thread));
  // 用户在本会话里改过输入区选择后，不再被宿主摘要覆盖
  const composerOptionsDirtyRef = useRef(false);
  // 记忆写入以 ref 保存的当前选择为准，避免回调拿到过期值
  const composerOptionsRef = useRef(composerOptions);
  const modelUpdatePendingRef = useRef<string | null>(null);
  const composerThreadIDRef = useRef(thread.id);
  composerThreadIDRef.current = thread.id;
  composerOptionsRef.current = composerOptions;
  const rememberSelectionSupported = Boolean(capabilities?.methods.includes('providers.rememberSelection'));

  /** 同步选择到宿主记忆（与桌面端共用一份）；失败只记日志，不影响发送。 */
  const rememberSelection = useCallback((params: ComposerSelectionMemoryParams) => {
    if (!rememberSelectionSupported) return;
    void rememberProviderSelection(params).catch((error) => {
      console.warn('同步模型选择记忆失败', error);
    });
  }, [rememberProviderSelection, rememberSelectionSupported]);

  const updateComposerOptions = useCallback(
    (next: RemoteComposerOptions | ((previous: RemoteComposerOptions) => RemoteComposerOptions)) => {
      const previous = composerOptionsRef.current;
      const resolved = typeof next === 'function' ? next(previous) : next;
      composerOptionsDirtyRef.current = true;
      composerOptionsRef.current = resolved;
      setComposerOptions(resolved);
      // 只有用户交互触发的变更才写记忆；useEffect 里的摘要同步不走这里
      const params = rememberParamsForChange(previous, resolved, latestSummary.projectID);
      if (params) rememberSelection(params);
    },
    [latestSummary.projectID, rememberSelection]
  );
  const updateRunningComposerOptions = useCallback((next: RemoteComposerOptions) => {
    const previous = composerOptionsRef.current;
    if (!latestSummary.isRunning) {
      updateComposerOptions(next);
      return;
    }
    if (next.provider !== previous.provider && next.provider && next.provider !== latestSummary.provider) {
      setSendError('运行中无法更换 Agent');
      return;
    }
    const modelChanged = next.model !== previous.model;
    const effortChanged = next.effort !== previous.effort;
    if (!modelChanged && !effortChanged) {
      updateComposerOptions(next);
      return;
    }
    if (modelUpdatePendingRef.current === thread.id) return;
    modelUpdatePendingRef.current = thread.id;
    const wasDirty = composerOptionsDirtyRef.current;
    updateComposerOptions(next);
    const changes = modelChanged ? { model: next.model } : { effort: next.effort ?? '' };
    void updateThread(thread.id, changes).then((summary) => {
      if (composerThreadIDRef.current !== thread.id) return;
      const current = composerOptionsRef.current;
      const resolved = { ...current, provider: summary.provider, model: summary.model ?? undefined, effort: summary.effort ?? undefined };
      composerOptionsRef.current = resolved;
      setComposerOptions(resolved);
      composerOptionsDirtyRef.current = current !== next;
    }).catch((error: unknown) => {
      if (composerThreadIDRef.current !== thread.id) return;
      const current = composerOptionsRef.current;
      const reverted = { ...current, provider: previous.provider, model: previous.model, effort: previous.effort };
      composerOptionsRef.current = reverted;
      setComposerOptions(reverted);
      composerOptionsDirtyRef.current = wasDirty || current !== next;
      setSendError(error instanceof Error ? error.message : '更新模型失败');
    }).finally(() => {
      if (modelUpdatePendingRef.current === thread.id) modelUpdatePendingRef.current = null;
    });
  }, [latestSummary.isRunning, latestSummary.provider, thread.id, updateComposerOptions, updateThread]);
  // 宿主摘要变化（Mac 上改了模型/权限/编排等）时同步输入区；用户改过则以用户选择为准
  useEffect(() => {
    if (composerOptionsDirtyRef.current) return;
    setComposerOptions(summaryComposerOptions(latestSummary));
  }, [
    latestSummary.provider,
    latestSummary.model,
    latestSummary.effort,
    latestSummary.runtimeMode,
    latestSummary.interactionMode,
    latestSummary.fastMode,
    latestSummary.worktreeRequested,
    latestSummary.swarmEnabled,
    latestSummary.swarmPairID,
    latestSummary.swarmBeesProvider,
    latestSummary.swarmBeesModel,
    latestSummary.swarmBeesEffort
  ]);
  const [expansionRev, setExpansionRev] = useState(0);

  const {
    state: swarmState,
    loading: swarmLoading,
    error: swarmError,
    refetch: refetchSwarm,
    setEnabled: setSwarmEnabled,
    stopBee: stopSwarmBee,
    stopAll: stopAllSwarmBees,
    clearFinished: clearFinishedSwarmBees,
    stopAllSupported: swarmStopAllSupported,
    clearFinishedSupported: swarmClearFinishedSupported,
    forwardQuestion: forwardSwarmBeeQuestion,
    getMergeDiff: getSwarmMergeDiff,
    approveMerge: approveSwarmMerge,
    discardMerge: discardSwarmMerge
  } = useThreadSwarm(thread.id);
  const swarmSupported = isSwarmCapabilitySupported(capabilities);
  // 合并审查与转交提问都属于可选能力：未声明时对应按钮不渲染
  const swarmMergeApprovalSupported = Boolean(
    capabilities?.methods?.includes('threads.swarm.mergeDiff') &&
    capabilities?.methods?.includes('threads.swarm.approveMerge') &&
    capabilities?.methods?.includes('threads.swarm.discardMerge')
  );
  const swarmForwardQuestionSupported = Boolean(
    capabilities?.methods?.includes('threads.swarm.forwardQuestion')
  );
  const reviewSupported = Boolean(
    capabilities?.features?.includes('threads.review') &&
    capabilities?.methods?.includes('threads.getDiff')
  );
  const gitSupported = Boolean(
    capabilities?.features?.includes('git.readonly') &&
    capabilities?.methods?.includes('git.status') &&
    capabilities?.methods?.includes('git.branches')
  );
  const scriptsSupported = Boolean(
    capabilities?.features?.includes('scripts.run') &&
    capabilities?.methods?.includes('scripts.list')
  );
  const filesSupported = Boolean(
    capabilities?.features?.includes('files.read') &&
    capabilities?.methods?.includes('files.list') &&
    capabilities?.methods?.includes('files.read') &&
    capabilities?.methods?.includes('files.search')
  );
  const mediaSupported = Boolean(
    capabilities?.features?.includes('threads.media') &&
    capabilities?.methods?.includes('threads.media.read')
  );
  const [contextBarOpen, setContextBarOpen] = useState(false);
  const swarmBees = swarmState?.bees ?? [];
  const swarmRunningCount = swarmBees.filter((bee) => bee.status === 'running').length;
  // 有子代理提问且尚未转交主线程时，胶囊上额外挂一个“?”角标
  const swarmPendingQuestionCount = swarmBees.filter((bee) => bee.pendingQuestion && !bee.pendingQuestion.forwarded).length;
  // 排队中的子代理也算未结束，用于“是否仍在进行”的判断与进度文案
  const swarmQueuedCount = swarmState?.queued ?? 0;

  // 抽屉路由只做原地替换：iOS 主屏幕 PWA 里 location.hash 赋值与 history.back() 会触发整页过渡，
  // 且上一历史项不一定是当前会话，导致闪屏、抽屉关不掉。
  const replaceThreadRoute = useCallback((suffix = '') => {
    const hash = `#/t/${encodeURIComponent(thread.id)}${suffix}`;
    if (window.location.hash !== hash) {
      window.history.replaceState(window.history.state, '', hash);
    }
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  }, [thread.id]);

  const replaceThreadByID = useCallback((threadID: string) => {
    const hash = `#/t/${encodeURIComponent(threadID)}`;
    if (window.location.hash !== hash) {
      window.history.replaceState(window.history.state, '', hash);
    }
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  }, []);

  const handleFilePathChange = useCallback((path: string) => {
    replaceThreadRoute(`/files${path ? `/${encodeURIComponent(path)}` : ''}`);
  }, [replaceThreadRoute]);

  const {
    helperNowMs,
    helperStopConfirmOpen,
    setHelperStopConfirmOpen,
    helperStopAction,
    setHelperStopAction,
    isCheckingHelperStop,
    handleConfirmHelperStop,
    handleCheckHelperStop
  } = useHelperStop({
    swarmState,
    threadID: thread.id,
    isConnected,
    refetchSwarm,
    stopSwarmBee,
    checkThreadAction
  });

  // 本地命令已执行 / #模型已委派 的结果提示条（这两种结果没有新轮次，靠这条告诉用户发生了什么）
  const [commandNotice, setCommandNotice] = useState<
    { kind: 'local'; text: string } | { kind: 'delegated'; threadID: string } | null
  >(null);
  const commandNoticeTimerRef = useRef<number | null>(null);

  // 时间线消息菜单：撤销此轮改动 / 编辑后重发 / 引用到回复 / 发回父会话。
  // 引用要把引用块写进草稿并聚焦输入框，所以输入区要开一个命令式入口（insertDraft）。
  const revertApi = useRemoteRevertApi();
  const composerRef = useRef<ThreadComposerHandle | null>(null);
  const [revertTarget, setRevertTarget] = useState<{ messageID: string; turnID: string } | null>(null);
  const [editTarget, setEditTarget] = useState<{ messageID: string; text: string } | null>(null);
  // 这些操作都不产生新轮次，结果用一条提示条告知（不受子代理页限制）
  const [menuNotice, setMenuNotice] = useState<string | null>(null);

  // 底部高度测量：跳到最新和悬浮胶囊动态依附在输入区上方
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const [bottomHeight, setBottomHeight] = useState(80);

  useEffect(() => {
    const el = bottomRef.current;
    if (!el) return;
    const update = () => {
      setBottomHeight(el.offsetHeight);
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);


  const requestIdRef = useRef(0);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const contentRef = useRef<HTMLDivElement | null>(null);
  // 展开的工作记录跨过滚动区底边时，右下角胶囊才给出「收起」段。
  const recomputeActiveExpanded = useCallback(() => {
    const el = scrollRef.current;
    if (!el) {
      setActiveExpandedTurnID(null);
      return;
    }
    const viewportBottom = el.getBoundingClientRect().bottom - 8;
    const records = Array.from(el.querySelectorAll<HTMLElement>('[data-expanded-work-record]')).map((node) => {
      const rect = node.getBoundingClientRect();
      return { turnID: node.dataset.expandedWorkRecord ?? '', top: rect.top, bottom: rect.bottom };
    });
    setActiveExpandedTurnID(pickExpandedWorkRecordTurnID(records, viewportBottom));
  }, []);
  // 展开/收起与流式增长都会改内容高度，用 ResizeObserver 重算一次（与底部高度测量同一种模式）。
  useLayoutEffect(() => {
    const content = contentRef.current;
    if (!content) return;
    recomputeActiveExpanded();
    const observer = new ResizeObserver(() => recomputeActiveExpanded());
    observer.observe(content);
    return () => observer.disconnect();
  }, [recomputeActiveExpanded, thread.id]);
  // 决策条的跳转 id 约 300ms 后被清空，卸载时把未触发的定时器一起清掉
  useEffect(
    () => () => {
      if (decisionFocusTimerRef.current !== null) window.clearTimeout(decisionFocusTimerRef.current);
    },
    []
  );
  // 发送后锚定期间置为 true：贴底跟随据此暂停，锚定解除后恢复（见 thread/useSendAnchor.ts）
  const messagesRef = useRef<ThreadMessage[]>([]);
  const frameCoalescerRef = useRef<ReturnType<typeof createFrameCoalescer> | null>(null);
  if (frameCoalescerRef.current === null) {
    frameCoalescerRef.current = createFrameCoalescer(() => setMessages(messagesRef.current));
  }
  const frameCoalescer = frameCoalescerRef.current;
  const sendAnchorHoldRef = useRef(false);
  const { end: endPin, resume: resumePin } = useInitialPinToLatest(
    thread.id,
    scrollRef,
    contentRef,
    messages,
    sendAnchorHoldRef
  );
  const pinSentMessage = usePinSentMessage();
  const anchorMissingRef = useRef<(messageID: string) => void>(() => {});
  const [queueRevealRequest, setQueueRevealRequest] = useState<{ followUpID: string; nonce: number } | null>(null);
  const sendAnchor = useSendAnchor({
    threadID: thread.id,
    messages,
    scrollRef,
    endPin,
    resumePin,
    suspendedRef: sendAnchorHoldRef,
    onAnchorMissing: (messageID) => anchorMissingRef.current(messageID),
    enabled: pinSentMessage
  });
  const { arm: armSendAnchor, release: releaseSendAnchor } = sendAnchor;
  const revealSentMessage = useCallback(() => {
    if (pinSentMessage) {
      flushSync(() => frameCoalescer.flushNow());
      armSendAnchor();
    } else resumePin();
  }, [armSendAnchor, frameCoalescer, pinSentMessage, resumePin]);
  const pendingHistoryScrollRef = useRef<{ scrollHeight: number; scrollTop: number; key: string | null; offset: number } | null>(null);
  const historySyncFrameRef = useRef(0);
  const pendingWorkRecordScrollRef = useRef<{ threadID: string; turnID: string } | null>(null);
  const lastFailedRequestRef = useRef<{ cursor: string | null; append: boolean } | null>(null);

  useEffect(() => {
    if (!showJumpToLatest) {
      // 回到底部（或按钮收起）即清零：清掉基线后未读数自然是 0。
      setUnreadAnchor((previous) => (previous === null ? previous : null));
      return;
    }
    // 按钮出现时以当时最后一条消息为基线；已计数条数用于锚点消息被乐观替换时的回退估算。
    setUnreadAnchor((previous) =>
      previous && previous.threadID === thread.id
        ? previous
        : createUnreadAnchor(thread.id, messagesRef.current)
    );
  }, [showJumpToLatest, thread.id]);
  const draftRef = useRef(draft);
  const attachmentsRef = useRef(attachments);
  const attachmentPreviewURLsRef = useRef<Record<string, string>>({});
  const draftVersionRef = useRef(0);
  const draftPersistTimerRef = useRef<number | null>(null);
  const pendingDraftPersistRef = useRef<{ threadID: string; text: string } | null>(null);
  const sendInFlightRef = useRef(false);
  const sendAttemptRef = useRef(
    new Map<string, { text: string; version: number }>()
  );
  const acceptedActionsRef = useRef(new Set<string>());
  const loadingMoreRef = useRef(false);
  const pollTimerRef = useRef<number | null>(null);
  const pollGenerationRef = useRef(0);
  const pollInFlightRef = useRef(false);
  const pollTargetTurnIDRef = useRef<string | null>(null);
  const streamStateRef = useRef(streamState);
  const streamRunIDRef = useRef<string | null>(null);
  const streamedMessageSequenceRef = useRef(new Map<string, number>());
  const removedStreamMessageIDsRef = useRef(new Set<string>());
  const subscriptionHandleRef = useRef<RemoteThreadSubscriptionHandle | null>(null);
  const mountedRef = useRef(true);
  const isConnectedRef = useRef(isConnected);
  const pollCycleRef = useRef<() => Promise<void>>(async () => {});
  const refreshOnceRef = useRef<() => Promise<void>>(async () => {});
  const refreshPendingRef = useRef(false);
  const retryTimerRef = useRef<number | null>(null);
  const retryAttemptRef = useRef(0);

  isConnectedRef.current = isConnected;
  streamStateRef.current = streamState;
  attachmentsRef.current = attachments;

  // 恢复/重连后的同步失败按 1s、2s、4s 退避重试；成功后归零，用完才保留 pollError。
  const clearRefreshRetry = useCallback(() => {
    if (retryTimerRef.current !== null) {
      window.clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    retryAttemptRef.current = 0;
  }, []);

  const scheduleRefreshRetry = useCallback(() => {
    if (retryTimerRef.current !== null) return;
    const next = retryAttemptRef.current + 1;
    const delay = resumeRetryDelay(next);
    if (delay === null) return;
    retryAttemptRef.current = next;
    retryTimerRef.current = window.setTimeout(() => {
      retryTimerRef.current = null;
      if (!mountedRef.current) return;
      void refreshOnceRef.current();
    }, delay);
  }, []);

  // 阅读辅助状态，按消息 id 记录
  const [expandedIds, setExpandedIds] = useState<Record<string, boolean>>({});
  const [copyState, setCopyState] = useState<{ id: string; status: 'copied' | 'error'; message?: string } | null>(null);
  const copyTimerRef = useRef<number | null>(null);

  const canSend =
    Boolean(capabilities?.methods.includes('threads.sendMessage') &&
      capabilities.methods.includes('threads.actionStatus'));
  const eventsSupported = Boolean(capabilities?.features?.includes('threads.events'));
  const requestsSupported = Boolean(
    capabilities?.methods.includes('requests.list') && capabilities.methods.includes('requests.respond')
  );
  const interruptSupported = Boolean(capabilities?.methods.includes('threads.interrupt'));
  // 单条命令终止：宿主声明后才能给工具行显示「终止此命令」
  const killRunningCommandSupported = Boolean(
    capabilities?.methods.includes('threads.killRunningCommand')
  );
  const queueSupported = Boolean(
    capabilities?.features?.includes('threads.queue') &&
      capabilities.methods.includes('threads.queue.list')
  );
  const interruptAndSendSupported = Boolean(
    queueSupported && capabilities?.methods.includes('threads.queue.interruptAndSend')
  );
  const composerOptionsSupported = Boolean(capabilities?.features?.includes('threads.composerOptions'));
  const attachmentsSupported = Boolean(
    capabilities?.features?.includes('threads.attachments') && capabilities.methods.includes('attachments.upload')
  );
  const attachmentLimit = composerAttachmentLimit(capabilities?.attachmentLimit);
  // `/` 命令、本地命令与 `#模型` 委派的结果变体都由 threads.commands 管
  const commandsSupported = Boolean(
    capabilities?.features?.includes('threads.commands') && capabilities.methods.includes('threads.commands')
  );
  const forwardSupported = Boolean(
    capabilities?.features?.includes('threads.continue') && capabilities.methods.includes('threads.continue')
  );
  const saveTaskSupported = Boolean(
    capabilities?.features?.includes('kanban.createTaskFromThread') &&
    capabilities.features.includes('kanban.linking') &&
    capabilities.methods.includes('kanban.createTask')
  );
  // 撤回本轮：宿主要能收回最后一轮用户提示词
  const recallSupported = Boolean(capabilities?.methods.includes('threads.turn.recall'));
  // 压缩：宿主要能直接执行 /compact（等同于桌面的本地命令）
  const compactSupported = Boolean(capabilities?.methods.includes('threads.compact'));
  const forwardQuestionSupported = Boolean(capabilities?.methods.includes('threads.swarm.forwardQuestion'));
  const requestSnapshot = requestSnapshots[thread.id];
  const queueSnapshot = queueSnapshots[thread.id];
  const contextSummary =
    threads.find((candidate) => candidate.id === thread.id) ??
    (currentThread?.id === thread.id ? currentThread : null);
  // 分叉来源：标题取自已加载的会话列表，源会话不在列表里时显示「分叉自其它会话」
  const forkedFromThreadID = latestSummary.forkedFromThreadID ?? null;
  const isContinuation = !latestSummary.forkedFromMessageID;
  const forkedFromTitle = forkedFromThreadID
    ? (threads.find((candidate) => candidate.id === forkedFromThreadID)?.title ?? '').trim()
    : '';
  const linkedTask = latestSummary.taskID
    ? board?.tasks.find((task) => task.id === latestSummary.taskID) ?? null
    : null;
  const linkedGoal = linkedTask?.goalID
    ? board?.goals.find((goal) => goal.id === linkedTask.goalID) ?? null
    : latestSummary.goalID
    ? board?.goals.find((goal) => goal.id === latestSummary.goalID) ?? null
    : null;
  const sendActions = useMemo(
    () => (threadActions ?? []).filter(
        (action) => action.kind === 'send' && action.threadID === thread.id
      ),
    [thread.id, threadActions]
  );
  const latestSendAction = sendActions.length > 0 ? sendActions[sendActions.length - 1] : null;
  const unresolvedSendAction = [...sendActions].reverse().find(isUnresolvedAction) ?? null;

  // 撤回本轮只对“最后一条仍是用户提示词”的时间线开放（与桌面端 canRecallTurn 一致）
  const recallMessageID = useMemo(() => {
    if (!recallSupported || messages.length === 0) return undefined;
    const last = messages[messages.length - 1];
    const isPlainUserMessage = last.kind === 'user' || (!last.kind && last.role === 'user');
    return isPlainUserMessage ? last.id : undefined;
  }, [messages, recallSupported]);

  const getActiveTurnID = useCallback((messages: ThreadMessage[]) => {
    if (!latestSummary.isRunning) return null;
    const endedTurnIDs = new Set<string>();
    for (const message of messages) {
      if (message.turnID && isEndedStatus(message.turnStatus)) {
        endedTurnIDs.add(message.turnID);
      }
    }
    for (let i = messages.length - 1; i >= 0; i--) {
      const turnID = messages[i].turnID;
      if (turnID) {
        if (endedTurnIDs.has(turnID)) {
          // 最新的有 turnID 的消息已属于结束轮次，当前新轮次尚未在消息中出现 turnID
          break;
        }
        return turnID;
      }
    }
    // 历史还没载入时，待处理请求带着当前轮次
    const pendingRequestTurnID = requestSnapshot?.requests.find((request) => request.turnID)?.turnID;
    if (pendingRequestTurnID && !endedTurnIDs.has(pendingRequestTurnID)) {
      return pendingRequestTurnID;
    }
    return null;
  }, [latestSummary.isRunning, requestSnapshot]);
  const activeTurnID = useMemo(() => getActiveTurnID(messages), [getActiveTurnID, messages]);

  const turnElapsedSeconds = useTurnElapsedSeconds({
    threadID: thread.id,
    isRunning: latestSummary.isRunning,
    lastUserMessageAt: latestSummary.lastUserMessageAt,
    messages,
    pendingTurnID: activeTurnID
  });

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
        const protectedMessageIDs = new Set([
          ...[...streamedMessageSequenceRef.current]
            .filter(([id, sequence]) => requestRunID !== streamRunIDRef.current || messageSequences.get(id) !== sequence)
            .map(([id]) => id),
          ...removedStreamMessageIDsRef.current
        ]);
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
      if (streamRunIDRef.current !== snapshot.runID) {
        streamRunIDRef.current = snapshot.runID;
        streamedMessageSequenceRef.current.clear();
        removedStreamMessageIDsRef.current.clear();
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

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      frameCoalescer.cancel();
      pollGenerationRef.current += 1;
      if (pollTimerRef.current !== null) {
        window.clearTimeout(pollTimerRef.current);
        pollTimerRef.current = null;
      }
      clearRefreshRetry();
    };
  }, [clearRefreshRetry, frameCoalescer]);

  useEffect(() => {
    const flushHidden = () => {
      if (document.visibilityState === 'hidden') frameCoalescer.flushNow();
    };
    document.addEventListener('visibilitychange', flushHidden);
    return () => document.removeEventListener('visibilitychange', flushHidden);
  }, [frameCoalescer]);

  const flushDraftPersist = useCallback(() => {
    if (draftPersistTimerRef.current !== null) {
      window.clearTimeout(draftPersistTimerRef.current);
      draftPersistTimerRef.current = null;
    }
    const pending = pendingDraftPersistRef.current;
    pendingDraftPersistRef.current = null;
    if (pending) saveThreadDraft(pending.threadID, pending.text);
  }, []);

  const scheduleDraftPersist = useCallback((threadID: string, text: string) => {
    // 同一会话内合并；切换会话时先落盘旧会话的待写草稿。
    if (pendingDraftPersistRef.current && pendingDraftPersistRef.current.threadID !== threadID) flushDraftPersist();
    pendingDraftPersistRef.current = { threadID, text };
    if (draftPersistTimerRef.current !== null) window.clearTimeout(draftPersistTimerRef.current);
    draftPersistTimerRef.current = window.setTimeout(flushDraftPersist, DRAFT_PERSIST_DELAY_MS);
  }, [flushDraftPersist]);

  // 已提交/已清空的草稿：取消待写并删除持久化副本。
  const discardPersistedDraft = useCallback((threadID: string) => {
    if (pendingDraftPersistRef.current?.threadID === threadID) {
      pendingDraftPersistRef.current = null;
      if (draftPersistTimerRef.current !== null) {
        window.clearTimeout(draftPersistTimerRef.current);
        draftPersistTimerRef.current = null;
      }
    }
    restoredDraftIDs.delete(threadID);
    clearThreadDraft(threadID);
  }, []);

  // 输入区图片缩略图：只给图片建 object URL，移除附件、切换会话、发送成功和卸载时都释放
  const setAttachmentPreview = useCallback((attachmentID: string, url: string) => {
    attachmentPreviewURLsRef.current = { ...attachmentPreviewURLsRef.current, [attachmentID]: url };
    setAttachmentPreviews(attachmentPreviewURLsRef.current);
  }, []);

  const releaseAttachmentPreviews = useCallback((attachmentIDs: string[]) => {
    const next = { ...attachmentPreviewURLsRef.current };
    for (const attachmentID of attachmentIDs) {
      const url = next[attachmentID];
      if (!url) continue;
      URL.revokeObjectURL(url);
      delete next[attachmentID];
    }
    attachmentPreviewURLsRef.current = next;
    setAttachmentPreviews(next);
  }, []);

  const releaseAllAttachmentPreviews = useCallback(() => {
    if (Object.keys(attachmentPreviewURLsRef.current).length === 0) return;
    Object.values(attachmentPreviewURLsRef.current).forEach((url) => URL.revokeObjectURL(url));
    attachmentPreviewURLsRef.current = {};
    setAttachmentPreviews({});
  }, []);

  // 卸载（或组件重建）时释放所有本地缩略图，并取消待执行的备注栏滚动
  useEffect(() => () => {
    Object.values(attachmentPreviewURLsRef.current).forEach((url) => URL.revokeObjectURL(url));
    attachmentPreviewURLsRef.current = {};
  }, []);

  // 页面进入后台/关闭或组件卸载时同步落盘，避免防抖窗口内的输入丢失。
  useEffect(() => {
    const handleVisibility = () => {
      if (document.visibilityState === 'hidden') flushDraftPersist();
    };
    document.addEventListener('visibilitychange', handleVisibility);
    window.addEventListener('pagehide', flushDraftPersist);
    return () => {
      document.removeEventListener('visibilitychange', handleVisibility);
      window.removeEventListener('pagehide', flushDraftPersist);
      flushDraftPersist();
    };
  }, [flushDraftPersist]);

  useLayoutEffect(() => {
    requestIdRef.current++;
    pollGenerationRef.current += 1;
    pendingHistoryScrollRef.current = null;
    if (pollTimerRef.current !== null) {
      window.clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    pollTargetTurnIDRef.current = null;
    pollInFlightRef.current = false;
    refreshPendingRef.current = false;
    clearRefreshRetry();
    setIsPolling(false);
    flushDraftPersist();
    const savedDraft = readThreadDraft(thread.id) ?? '';
    draftRef.current = savedDraft;
    draftVersionRef.current = 0;
    setDraft(savedDraft);
    setAttachments([]);
    releaseAllAttachmentPreviews();
    setAttachmentError(null);
    composerOptionsDirtyRef.current = false;
    setComposerOptions(summaryComposerOptions(thread));
    setShowJumpToLatest(false);
    setUnreadAnchor(null);
    setLatestSummary(thread);
    setSendError(null);
    setActionQueryError(null);
    setPollError(null);
    setStreamError(null);
    streamStateRef.current = 'disabled';
    setStreamState('disabled');
    streamRunIDRef.current = null;
    streamedMessageSequenceRef.current.clear();
    removedStreamMessageIDsRef.current.clear();
    frameCoalescer.cancel();
    messagesRef.current = [];
    setMessages([]);
    setNextCursor(null);
    setHasMore(false);
    setTruncated(false);
    setErrorMessage(null);
    setIsLoading(true);
    setHistoryInitialDone(false);
    setExpandedIds({});
    setActiveExpandedTurnID(null);
    setCopyState(null);
    setRequestLoadError(null);
    setInterruptState('idle');
    setInterruptError(null);
    setInterruptAndSendState('idle');
    pendingInterruptSendRef.current = null;
    void loadPage(null, false);
  }, [clearRefreshRetry, frameCoalescer, loadPage, releaseAllAttachmentPreviews, thread.id]);

  useEffect(() => {
    setLatestSummary((previous) => (previous.id === thread.id ? mergeThreadSummary(previous, thread, false) : thread));
  }, [thread]);

  useEffect(() => {
    if (!queueSupported || !isConnected) return;
    setQueuePollError(null);
    void listThreadQueue(thread.id).catch((err) => {
      if (mountedRef.current) setQueuePollError(err instanceof Error ? err.message : '加载跟进队列失败');
    });
  }, [isConnected, listThreadQueue, queueSupported, thread.id]);


  // 卸载时清掉复制反馈定时器
  useEffect(() => {
    return () => {
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    };
  }, []);

  const toggleExpanded = useCallback((messageId: string) => {
    endPin();
    setExpandedIds((prev) => ({ ...prev, [messageId]: !prev[messageId] }));
  }, [endPin]);

  const toggleWorkRecord = useCallback(
    (turnId: string) => {
      endPin();
      const key = `${thread.id}:${turnId}`;
      const next = !(workRecordExpandedMap.get(key) ?? false);
      workRecordExpandedMap.set(key, next);
      // 模块级展开状态用于跨会话切换保留，设上限避免长期使用无限增长。
      if (workRecordExpandedMap.size > 500) {
        const oldest = workRecordExpandedMap.keys().next().value;
        if (oldest !== undefined) workRecordExpandedMap.delete(oldest);
      }
      setExpansionRev((v) => v + 1);
    },
    [endPin, thread.id]
  );

  const collapseWorkRecord = useCallback(
    (turnID: string) => {
      endPin();
      pendingWorkRecordScrollRef.current = { threadID: thread.id, turnID };
      // 显式收起：默认展开时该键可能还没设置过，交给 toggle 会翻成展开。
      collapseWorkRecordState(workRecordExpandedMap, `${thread.id}:${turnID}`);
      setExpansionRev((v) => v + 1);
    },
    [endPin, thread.id]
  );

  useLayoutEffect(() => {
    const pending = pendingWorkRecordScrollRef.current;
    pendingWorkRecordScrollRef.current = null;
    const container = scrollRef.current;
    if (!pending || pending.threadID !== thread.id || !container) return;
    const header = Array.from(container.querySelectorAll<HTMLButtonElement>('[data-work-record-id]'))
      .find((element) => element.dataset.workRecordId === pending.turnID);
    if (!header) return;
    container.scrollTop += header.getBoundingClientRect().top - container.getBoundingClientRect().top - 8;
    header.focus({ preventScroll: true });
    setShowJumpToLatest(container.scrollHeight - container.scrollTop - container.clientHeight > 200);
  }, [expansionRev, thread.id]);

  const updateDraft = useCallback(
    (value: string) => {
      draftVersionRef.current += 1;
      draftRef.current = value;
      threadDrafts.set(thread.id, value);
      restoredDraftIDs.delete(thread.id);
      scheduleDraftPersist(thread.id, value);
      setDraft(value);
    },
    [scheduleDraftPersist, thread.id]
  );

  const handleAddAttachments = useCallback(async (files: FileList | null) => {
    if (!attachmentsSupported || !files || files.length === 0 || isUploadingAttachment) return;
    const incoming = Array.from(files);
    if (attachmentsRef.current.length + incoming.length > attachmentLimit) {
      setAttachmentError(`每条消息最多添加 ${attachmentLimit} 个附件`);
      return;
    }

    setAttachmentError(null);
    setIsUploadingAttachment(true);
    const uploaded: RemoteAttachmentUploadResult[] = [];
    try {
      let totalBytes = attachmentsRef.current.reduce((sum, attachment) => sum + attachment.byteCount, 0);
      for (const file of incoming) {
        const result = await uploadAttachment(file);
        totalBytes += result.byteCount;
        if (totalBytes > 8 * 1024 * 1024) throw new Error('文件总大小不能超过 8 MiB');
        if (isPreviewableImageFile(file)) {
          setAttachmentPreview(result.attachmentID, URL.createObjectURL(file));
        }
        uploaded.push(result);
      }
      setAttachments((previous) => [...previous, ...uploaded]);
      draftVersionRef.current += 1;
    } catch (err) {
      releaseAttachmentPreviews(uploaded.map((attachment) => attachment.attachmentID));
      setAttachmentError(err instanceof Error ? err.message : '附件上传失败');
    } finally {
      setIsUploadingAttachment(false);
    }
  }, [attachmentLimit, attachmentsSupported, isUploadingAttachment, releaseAttachmentPreviews, setAttachmentPreview, uploadAttachment]);

  const removeAttachment = useCallback((attachmentID: string) => {
    setAttachments((previous) => previous.filter((attachment) => attachment.attachmentID !== attachmentID));
    releaseAttachmentPreviews([attachmentID]);
    draftVersionRef.current += 1;
    setAttachmentError(null);
  }, [releaseAttachmentPreviews]);

  const clearSubmittedDraft = useCallback(
    (action: RemoteThreadAction) => {
      const attempt = sendAttemptRef.current.get(action.actionID);
      // 恢复的草稿已去掉会话引用资料块，屏障比较两边都先剥掉资料块才会对得上
      const canClear = attempt
        ? draftVersionRef.current === attempt.version
        : stripReferencedThreadBlock(draftRef.current) === stripReferencedThreadBlock(action.text ?? '');
      if (canClear) {
        draftVersionRef.current += 1;
        draftRef.current = '';
        threadDrafts.delete(thread.id);
        discardPersistedDraft(thread.id);
        setDraft('');
        setAttachments([]);
        releaseAllAttachmentPreviews();
        setAttachmentError(null);
      }
    },
    [discardPersistedDraft, releaseAllAttachmentPreviews, thread.id]
  );

  const clearDraftIfUnchanged = useCallback((text: string, version: number) => {
    if (draftVersionRef.current !== version || draftRef.current !== text) return;
    draftVersionRef.current += 1;
    draftRef.current = '';
    threadDrafts.delete(thread.id);
    discardPersistedDraft(thread.id);
    setDraft('');
    setAttachments([]);
    releaseAllAttachmentPreviews();
    setAttachmentError(null);
  }, [discardPersistedDraft, releaseAllAttachmentPreviews, thread.id]);

  const refreshLatestState = useCallback(async (): Promise<RemoteThreadSummary | null> => {
    const summary = await refreshThread(thread.id);
    if (!summary) throw new Error('无法确认最新会话状态，请刷新后再发送');
    if (summary && mountedRef.current) {
      setLatestSummary((previous) => (previous.id === summary.id ? mergeThreadSummary(previous, summary, false) : summary));
    }
    return summary;
  }, [refreshThread, thread.id]);

  const pausePolling = useCallback(() => {
    if (pollTimerRef.current !== null) {
      window.clearTimeout(pollTimerRef.current);
      pollTimerRef.current = null;
    }
    if (mountedRef.current) setIsPolling(false);
    clearRefreshRetry();
  }, [clearRefreshRetry]);

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

    const handle = subscribeThread(thread.id, handlers);
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

  useEffect(() => {
    if (contextSummary) {
      setLatestSummary((previous) =>
        previous.id === contextSummary.id ? mergeThreadSummary(previous, contextSummary, false) : contextSummary
      );
    }
  }, [contextSummary]);

  useEffect(() => {
    if (!requestsSupported || !isConnected) return;
    let active = true;
    setRequestLoadError(null);
    void listRequests(thread.id).catch((err) => {
      if (active) setRequestLoadError(err instanceof Error ? err.message : '加载待处理请求失败');
    });
    return () => {
      active = false;
    };
  }, [isConnected, listRequests, requestsSupported, thread.id]);

  useEffect(() => {
    if (!latestSummary.isRunning) setInterruptState('idle');
  }, [latestSummary.isRunning]);

  // 用未决 action 的正文恢复用户可能离开页面前输入的内容。
  // 优先级：本次运行内已编辑的草稿 > 未决 action 正文 > 持久化草稿。
  useEffect(() => {
    if (!unresolvedSendAction?.text) return;
    // 资料块是发送时才拼上的背景，恢复草稿时要去掉，输入框里只留用户正文。
    const restored = stripReferencedThreadBlock(unresolvedSendAction.text);
    if (restored === '') return;
    if (threadDrafts.has(thread.id) && !restoredDraftIDs.has(thread.id)) return;
    restoredDraftIDs.delete(thread.id);
    threadDrafts.set(thread.id, restored);
    draftRef.current = restored;
    scheduleDraftPersist(thread.id, restored);
    setDraft(restored);
  }, [scheduleDraftPersist, thread.id, unresolvedSendAction?.actionID, unresolvedSendAction?.text]);

  useEffect(() => {
    if (
      !historyInitialDone ||
      !latestSummary.isRunning ||
      (eventsSupported && streamState === 'subscribed')
    ) return;
    beginPolling();
  }, [beginPolling, eventsSupported, historyInitialDone, latestSummary.isRunning, streamState]);

  useEffect(() => {
    if (!latestSendAction || latestSendAction.state !== 'accepted' ||
        acceptedActionsRef.current.has(latestSendAction.actionID)) return;
    acceptedActionsRef.current.add(latestSendAction.actionID);
    clearSubmittedDraft(latestSendAction);
    // 本地命令与 #模型委派没有 turnID：不等待轮次，只给结果提示（摘要已由 context 刷新）
    const queuedCommand = latestSendAction.queued ? localCommandName(latestSendAction.text ?? '') : null;
    if (latestSendAction.delegatedThreadID) {
      setCommandNotice({ kind: 'delegated', threadID: latestSendAction.delegatedThreadID });
    } else if (latestSendAction.localCommand) {
      setCommandNotice({
        kind: 'local',
        text: latestSendAction.localCommand === 'plan'
          ? '已执行 /plan，计划模式已切换'
          : '已执行 /compact，正在压缩上下文'
      });
    } else if (queuedCommand) {
      setCommandNotice({ kind: 'local', text: `已排队 /${queuedCommand}，本轮结束后执行` });
    }
    beginPolling(latestSendAction.turnID ?? null);
  }, [beginPolling, clearSubmittedDraft, latestSendAction]);

  // 结果提示条：本地命令提示几秒后自动收起，委派提示保留到用户收起或切换会话
  useEffect(() => {
    if (commandNoticeTimerRef.current !== null) {
      window.clearTimeout(commandNoticeTimerRef.current);
      commandNoticeTimerRef.current = null;
    }
    if (commandNotice?.kind === 'local') {
      commandNoticeTimerRef.current = window.setTimeout(() => {
        commandNoticeTimerRef.current = null;
        setCommandNotice(null);
      }, 5000);
    }
    return () => {
      if (commandNoticeTimerRef.current !== null) {
        window.clearTimeout(commandNoticeTimerRef.current);
        commandNoticeTimerRef.current = null;
      }
    };
  }, [commandNotice]);
  useEffect(() => {
    setCommandNotice(null);
    // 会话内查找条只属于打开它的会话
    setFindOpen(false);
    setFindQuery('');
    setFindCursor(0);
    // 转发 / 存为任务的对话框与幂等 actionID 只属于打开它们的会话
    forwardActionRef.current = null;
    setForwardConfirmOpen(false);
    setForwardError(null);
    setSaveTaskOpen(false);
    setSaveTaskError(null);
  }, [thread.id]);

  // 队列编辑租借、撤回确认与压缩建议都只属于当前会话
  useEffect(() => {
    setThreadActionError(null);
    setRecallConfirmOpen(false);
    setCompactSuggestion(null);
    setCompactState('idle');
  }, [thread.id]);

  // 宿主压缩建议：只处理当前会话，连接重建后重新订阅（监听器挂在连接实例上）
  useEffect(() => {
    if (!compactSupported || !isConnected) return;
    return subscribeCompactSuggestion((offer) => {
      if (offer.threadID !== thread.id) return;
      setCompactSuggestion(offer);
    });
  }, [compactSupported, isConnected, subscribeCompactSuggestion, thread.id]);

  // 页面恢复、网络恢复和重新连接只触发一次串行读取，前台运行中的会话随后继续轮询。
  useEffect(() => {
    const refreshOnResume = () => {
      if (document.visibilityState === 'visible' && navigator.onLine !== false) void refreshOnce();
      else pausePolling();
    };
    document.addEventListener('visibilitychange', refreshOnResume);
    window.addEventListener('pageshow', refreshOnResume);
    window.addEventListener('online', refreshOnResume);
    window.addEventListener('offline', pausePolling);
    return () => {
      document.removeEventListener('visibilitychange', refreshOnResume);
      window.removeEventListener('pageshow', refreshOnResume);
      window.removeEventListener('online', refreshOnResume);
      window.removeEventListener('offline', pausePolling);
    };
  }, [pausePolling, refreshOnce]);

  useEffect(() => {
    if (isConnected && historyInitialDone) {
      // 重连成功先清掉上一次失败留下的提示和退避计数，再重新同步。
      clearRefreshRetry();
      setPollError(null);
      void refreshOnce();
    } else if (!isConnected) pausePolling();
  }, [clearRefreshRetry, historyInitialDone, isConnected, pausePolling, refreshOnce]);

  const handleCheckAction = useCallback(
    async (action: RemoteThreadAction) => {
      if (checkingActionID || !isConnected) return;
      setActionQueryError(null);
      setCheckingActionID(action.actionID);
      try {
        const updated = await checkThreadAction(action.actionID);
        if (!mountedRef.current) return;
        if (updated.state === 'accepted') {
          clearSubmittedDraft(updated);
          if (updated.kind === 'send') beginPolling(updated.turnID ?? null);
        }
      } catch (err) {
        setActionQueryError(err instanceof Error ? err.message : '查询发送结果失败');
      } finally {
        if (mountedRef.current) setCheckingActionID(null);
      }
    },
    [beginPolling, checkThreadAction, checkingActionID, clearSubmittedDraft, isConnected]
  );

  const handleDismissAction = useCallback(
    (action: RemoteThreadAction) => {
      dismissThreadAction(action.actionID);
      sendAttemptRef.current.delete(action.actionID);
      setActionQueryError(null);
    },
    [dismissThreadAction]
  );

  const handleInterrupt = useCallback(async () => {
    const activeTurnID = getActiveTurnID(messagesRef.current);
    if (!interruptSupported || !isConnected || !activeTurnID || interruptState === 'stopping') return;
    setInterruptError(null);
    setInterruptState('stopping');
    try {
      const result = await interruptThread({ threadID: thread.id, turnID: activeTurnID });
      // 停成功：下一轮开始前在输入框上方给出「继续」胶囊
      setLocallyInterrupted(true);
      if (result.state === 'already_stopped') setInterruptState('idle');
    } catch (err) {
      setInterruptState('idle');
      setInterruptError(err instanceof Error ? err.message : '停止会话失败，请刷新后重试');
    }
  }, [getActiveTurnID, interruptState, interruptSupported, interruptThread, isConnected, thread.id]);

  // 终止单条命令：只杀该工具调用对应的子进程，不中断本轮；killed 为 0 说明命令已自己结束
  const handleKillRunningCommand = useCallback(
    async (messageID: string) => {
      if (!killRunningCommandSupported || killingCommandID) return;
      setKillCommandError(null);
      setKillingCommandID(messageID);
      try {
        const result = await killRunningCommand({ threadID: thread.id, messageID });
        if (!mountedRef.current) return;
        if (result.killed === 0) setCommandNotice({ kind: 'local', text: '命令已结束' });
      } catch (err) {
        if (!mountedRef.current) return;
        setKillCommandError(err instanceof Error ? err.message : '终止命令失败，请稍后重试');
      } finally {
        if (mountedRef.current) setKillingCommandID(null);
      }
    },
    [killRunningCommand, killRunningCommandSupported, killingCommandID, thread.id]
  );

  const handleRemoteRespond = useCallback(async (params: Parameters<typeof respondToRequest>[0]): Promise<void> => {
    await respondToRequest(params);
  }, [respondToRequest]);

  // 把子代理的提问转交主线程回答（actionID 生成方式同 stopBee 等动作）
  const handleForwardSwarmQuestion = useCallback(async (beeThreadID: string, requestID: string) => {
    if (!forwardQuestionSupported || !isConnected) return;
    setThreadActionError(null);
    try {
      await forwardSwarmQuestion({
        threadID: thread.id,
        beeThreadID: beeThreadID,
        requestID,
        actionID: makeActionID()
      });
      void refetchSwarm();
    } catch (err) {
      setThreadActionError(err instanceof Error ? err.message : '转交子代理提问失败');
    }
  }, [forwardSwarmQuestion, forwardQuestionSupported, isConnected, refetchSwarm, thread.id]);

  // 撤回本轮：收回最近一轮尚未产生回复的提示词，并放回输入框（草稿非空时不覆盖）
  const handleRecallTurn = useCallback(async () => {
    if (!recallSupported || !isConnected || isRecalling) return;
    setRecallConfirmOpen(false);
    setIsTakingBack(true);
    setThreadActionError(null);
    try {
      const result = await recallTurn(thread.id);
      if (!mountedRef.current) return;
      if (result.prompt && !draftRef.current.trim()) updateDraft(result.prompt);
      await refreshThread(thread.id);
      await loadPage(null, false);
    } catch (err) {
      if (mountedRef.current) {
        setThreadActionError(err instanceof Error ? err.message : '撤回本轮失败');
      }
    } finally {
      if (mountedRef.current) setIsTakingBack(false);
    }
  }, [isConnected, isRecalling, loadPage, refreshThread, recallSupported, recallTurn, thread.id, updateDraft]);

  const openRecallConfirm = useCallback(() => setRecallConfirmOpen(true), []);

  const handleCompactNow = useCallback(async () => {
    if (!compactSupported || !isConnected || compactState === 'compacting') return;
    setCompactState('compacting');
    setThreadActionError(null);
    try {
      await compactThread(thread.id);
      if (!mountedRef.current) return;
      setCompactSuggestion(null);
      setCommandNotice({ kind: 'local', text: '已执行 /compact，正在压缩上下文' });
      void refreshThread(thread.id);
    } catch (err) {
      if (mountedRef.current) {
        setThreadActionError(err instanceof Error ? err.message : '压缩上下文失败');
      }
    } finally {
      if (mountedRef.current) setCompactState('idle');
    }
  }, [compactState, compactSupported, compactThread, isConnected, refreshThread, thread.id]);

  const handleInterruptAndSend = useCallback(async (overrideText?: string): Promise<boolean> => {
    const draftText = draftRef.current;
    const text = overrideText ?? draftText;
    const uploadedAttachments = attachmentsRef.current;
    if (
      !interruptAndSendSupported ||
      !isConnected ||
      !latestSummary.isRunning ||
      interruptAndSendState === 'sending' ||
      // 本地命令判定看用户原文，不看展开后的正文（展开会拼上一段背景资料）
      (commandsSupported && localCommandName(draftText) !== null) ||
      (!text.trim() && uploadedAttachments.length === 0) ||
      latestSummary.hasPendingRequest ||
      isUploadingAttachment
    ) return false;

    const submittedVersion = draftVersionRef.current;
    setInterruptError(null);
    setInterruptAndSendState('sending');
    const attachmentIDs = attachmentsSupported ? uploadedAttachments.map((attachment) => attachment.attachmentID) : [];
    // 超时后同一份内容沿用原 actionID 重试，宿主按 actionID 幂等，不会重复发送（按草稿原文比对）。
    const pending = pendingInterruptSendRef.current;
    const actionID = pending && pending.text === draftText && pending.attachmentIDs === attachmentIDs.join(',')
      ? pending.actionID
      : makeActionID();
    let accepted = false;
    try {
      revealSentMessage();
      const result = await interruptAndSendThread(thread.id, text, attachmentIDs, actionID);
      pendingInterruptSendRef.current = null;
      clearDraftIfUnchanged(draftText, submittedVersion);
      beginPolling(result.turnID ?? getActiveTurnID(messagesRef.current));
      void refreshThread(thread.id);
      accepted = true;
    } catch (err) {
      releaseSendAnchor(true);
      if (err instanceof RpcError) {
        pendingInterruptSendRef.current = null;
        setInterruptError(err.message || '中断并发送失败，草稿已保留');
      } else {
        pendingInterruptSendRef.current = { actionID, text: draftText, attachmentIDs: attachmentIDs.join(',') };
        setInterruptError('中断并发送结果待确认：草稿已保留，再次点击将沿用同一动作确认，不会重复发送。');
      }
    } finally {
      setInterruptAndSendState('idle');
    }
    return accepted;
  }, [getActiveTurnID, attachmentsSupported, beginPolling, clearDraftIfUnchanged, commandsSupported, interruptAndSendState, interruptAndSendSupported, interruptAndSendThread, isConnected, isUploadingAttachment, latestSummary.hasPendingRequest, latestSummary.isRunning, refreshThread, releaseSendAnchor, revealSentMessage, thread.id]);

  const handleSend = useCallback(async (overrideText?: string): Promise<boolean> => {
    // 草稿原文：清理草稿、乐观消息、失败恢复都以它为准；overrideText 只用于真正发出去的文本
    const draftText = draftRef.current;
    const submittedText = overrideText ?? draftText;
    const submittedAttachments = attachmentsRef.current;
    const summary = latestSummary;
    if (
      !canSend ||
      (!submittedText.trim() && submittedAttachments.length === 0) ||
      !isConnected ||
      isSyncing ||
      isLoading ||
      !historyInitialDone ||
      Boolean(errorMessage) ||
      Boolean(pollError) ||
      (Boolean(pollTargetTurnIDRef.current) && !summary.isRunning) ||
      summary.isArchived ||
      summary.hasPendingRequest ||
      unresolvedSendAction ||
      sendInFlightRef.current
    ) {
      return false;
    }

    const submittedVersion = draftVersionRef.current;
    sendInFlightRef.current = true;
    setIsSending(true);
    setSendError(null);
    setActionQueryError(null);
    try {
      // 宿主声明 threads.compact 时，`/compact` 直接走压缩 RPC，不再当作普通消息发出
      // 本地命令看用户原文，不看展开后的正文（会话引用展开会拼上一段背景资料）
      if (compactSupported && localCommandName(draftText) === 'compact') {
        await compactThread(thread.id);
        if (!mountedRef.current) return true;
        clearDraftIfUnchanged(draftText, submittedVersion);
        setCommandNotice({ kind: 'local', text: '已执行 /compact，正在压缩上下文' });
        void refreshThread(thread.id);
        return true;
      }
      const params: Parameters<typeof sendThreadMessage>[2] = {};
      if (summary.isRunning) params.whenRunning = 'queue';
      if (composerOptionsSupported) {
        // 宿主会按 options 重新校验模型并重置 provider 会话，只发送相对摘要发生变化的选择。
        const options = changedComposerOptions(composerOptions, summaryComposerOptions(summary));
        if (Object.keys(options).length > 0) params.options = options;
      }
      if (attachmentsSupported) params.attachments = submittedAttachments.map((attachment) => attachment.attachmentID);
      // 真正发出才锚定：排队等本轮结束的消息不会马上出现在时间线里，不设锚点
      if (params.whenRunning !== 'queue') revealSentMessage();
      const action = await sendThreadMessage(thread.id, submittedText, params);
      if (!mountedRef.current) return true;
      // 记草稿原文：清理草稿时的兜底比对（draftRef.current === action.text）依赖它
      sendAttemptRef.current.set(action.actionID, { text: draftText, version: submittedVersion });
      if (action.state === 'accepted') {
        if (action.queued && action.followUpID) {
          const followUpID = action.followUpID;
          setQueueRevealRequest((previous) => ({ followUpID, nonce: (previous?.nonce ?? 0) + 1 }));
        }
        composerOptionsDirtyRef.current = false;
        clearSubmittedDraft(action);
        beginPolling(action.turnID ?? null);
        return true;
      }
      if (action.state === 'failed') {
        releaseSendAnchor(true);
        setSendError(action.error || '发送失败，草稿已保留');
      }
      return false;
    } catch (err) {
      releaseSendAnchor(true);
      if (mountedRef.current) setSendError(err instanceof Error ? err.message : '发送消息失败，草稿已保留');
      return false;
    } finally {
      sendInFlightRef.current = false;
      if (mountedRef.current) setIsSending(false);
    }
  }, [
    revealSentMessage,
    releaseSendAnchor,
    beginPolling,
    canSend,
    clearDraftIfUnchanged,
    clearSubmittedDraft,
    compactSupported,
    compactThread,
    composerOptions,
    composerOptionsSupported,
    errorMessage,
    attachmentsSupported,
    historyInitialDone,
    isConnected,
    isLoading,
    isSyncing,
    latestSummary,
    pollError,
    refreshThread,
    sendThreadMessage,
    thread.id,
    unresolvedSendAction
  ]);

  const copyMessage = useCallback(async (messageId: string, text: string, variant: CopyVariant) => {
    try {
      // text 始终是完整原文（折叠长消息时也不是截断的可见文本）
      let payload = text;
      if (variant === 'plain') {
        payload = toPlainText(text);
      } else if (variant === 'lastCode') {
        const code = lastCodeBlock(text);
        if (code === null) throw new Error('这条消息里没有代码块');
        payload = code;
      }
      if (!navigator.clipboard?.writeText) {
        throw new Error('当前浏览器环境不支持剪贴板写入');
      }
      await navigator.clipboard.writeText(payload);
      setCopyState({ id: messageId, status: 'copied' });
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = window.setTimeout(() => {
        copyTimerRef.current = null;
        setCopyState(null);
      }, 1500);
    } catch (err) {
      const errorText = err instanceof Error ? err.message : '复制失败';
      setCopyState({ id: messageId, status: 'error', message: errorText });
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = window.setTimeout(() => {
        copyTimerRef.current = null;
        setCopyState(null);
      }, 2500);
    }
  }, []);

  const isStewardThread = thread.isSteward === true;
  const timelineItems = useMemo(
    () => buildTimelineItems(messages, { isSteward: isStewardThread }),
    [isStewardThread, messages]
  );
  // 提问导航轨：只基于已加载的用户提问，少于 4 条时组件自身不渲染
  const questionMinimapData = useMemo(() => buildQuestionMinimapData(messages), [messages]);
  // 时间线跳转请求：导航轨点击与推送点开共用一条通道；nonce 递增保证重复点同一条也能重新定位
  // （与 focusMessageID 的定位互不影响）。flash 为真时时间线滚到后额外闪一次底色。
  const [scrollJumpRequest, setScrollJumpRequest] = useState<{
    messageID: string;
    nonce: number;
    flash?: boolean;
  } | null>(null);
  anchorMissingRef.current = (messageID) => {
    setScrollJumpRequest((previous) => ({ messageID, nonce: (previous?.nonce ?? 0) + 1, flash: false }));
  };
  const jumpToQuestion = useCallback((messageID: string) => {
    setScrollJumpRequest((previous) => ({ messageID, nonce: (previous?.nonce ?? 0) + 1 }));
  }, []);
  // 推送点开的审批卡：滚动与高亮都落在审批面板上（卡片本身没有暴露 requestID 的 DOM 标识）。
  const requestPanelRef = useRef<HTMLDivElement | null>(null);
  const [requestPanelFlashNonce, setRequestPanelFlashNonce] = useState(0);
  useEffect(() => {
    if (requestPanelFlashNonce === 0) return;
    const timer = window.setTimeout(() => setRequestPanelFlashNonce(0), 1500);
    return () => window.clearTimeout(timer);
  }, [requestPanelFlashNonce]);
  // 推送点开的一次性定位：目标还没加载时不报错，等消息或审批快照到达后再试。
  const consumedPushJumpNonceRef = useRef(0);
  useEffect(() => {
    if (!jumpRequest) return;
    if (jumpRequest.nonce === consumedPushJumpNonceRef.current) return;
    const targetMessageID = jumpRequest.messageID ?? '';
    const targetRequestID = jumpRequest.requestID ?? '';
    const messageReady = !targetMessageID || messages.length > 0;
    const requestReady =
      !targetRequestID ||
      Boolean(requestSnapshot?.requests.some((request) => request.requestID === targetRequestID));
    if (!messageReady || !requestReady) return;
    consumedPushJumpNonceRef.current = jumpRequest.nonce;
    if (targetMessageID) {
      setScrollJumpRequest((previous) => ({
        messageID: targetMessageID,
        nonce: (previous?.nonce ?? 0) + 1,
        flash: true
      }));
    }
    if (targetRequestID) {
      requestPanelRef.current?.scrollIntoView({ block: 'center', behavior: 'smooth' });
      setRequestPanelFlashNonce((previous) => previous + 1);
    }
  }, [jumpRequest, messages.length, requestSnapshot]);
  // 总管最后一条答复在请用户拍板时，时间线上方常驻一条决策条
  const pendingDecision = useMemo(
    () => (isStewardThread ? stewardPendingDecision(messages) : null),
    [isStewardThread, messages]
  );
  const dismissDecision = useCallback((messageID: string) => {
    setDismissedDecisionIDs((previous) => {
      const next = new Set(previous);
      next.add(messageID);
      return next;
    });
  }, []);
  const jumpToDecision = useCallback((messageID: string) => {
    setDecisionFocusID(messageID);
    if (decisionFocusTimerRef.current !== null) window.clearTimeout(decisionFocusTimerRef.current);
    // 300ms 后清空：focusMessageID 变化才会重新触发定位，清空后重复点击仍能再滚一次
    decisionFocusTimerRef.current = window.setTimeout(() => {
      decisionFocusTimerRef.current = null;
      setDecisionFocusID(null);
    }, 300);
  }, []);
  // 快捷回复只在会话确实有可见消息时出现：口径与时间线一致，滤掉总管唤醒与不产生行的静默回复
  const hasVisibleMessages = useMemo(
    () => messages.some((message) => !isHiddenTimelineMessage(message)),
    [messages]
  );
  // 总管会话的分割入口：显示与禁用规则与桌面 splitSessionViewFor 一致，
  // 断线时同样禁用（桌面是本地进程内调用，Web 断线点击必然失败）。
  const { supported: splitSupported, splitSession } = useRemoteSplitApi();
  const splitView = useMemo(
    () =>
      splitSessionViewFor({
        isSteward: isStewardThread,
        hasMessages: hasVisibleMessages,
        busy: isSending || isThreadBusy(latestSummary) || !isConnected
      }),
    [isStewardThread, hasVisibleMessages, isSending, latestSummary, isConnected]
  );
  const handleSplitSession = useCallback(async () => {
    if (!splitSupported || !splitView.show || splitView.disabled) return;
    setThreadActionError(null);
    try {
      await splitSession(thread.id);
      if (!mountedRef.current) return;
      setCommandNotice({ kind: 'local', text: '已分割，下一条消息不再带上面的历史' });
      // 分割线是宿主新增的一条消息，订阅不一定推过来，主动重拉最新一页让它立刻出现。
      void loadPage(null, false);
    } catch (err) {
      // 宿主已经把错误翻成中文原文（运行中 / 空会话 / 线程不存在），原样显示
      if (mountedRef.current) setThreadActionError(err instanceof Error ? err.message : '分割会话失败');
    }
  }, [splitSupported, splitView.show, splitView.disabled, splitSession, thread.id, loadPage]);
  // 输入框方向键历史：按时间顺序的用户消息原文（空草稿按 ↑ 翻上一条）
  // 对应 internal/ui/composer_history.go:15 的 composerHistoryEntries
  const composerHistoryEntryList = useMemo(() => composerHistoryEntries(messages), [messages]);
  // 会话内查找命中：逐字计数（一条消息里出现 3 次算 3 处）；先滤掉工作记录，与旧版只查正文的跳转范围一致
  const findHits = useMemo(() => {
    if (!findOpen || findQuery.trim() === '') return [] as FindMatch[];
    return findMatches(
      messages.filter((message) => !isWorkEntry(message)),
      findQuery,
      findCaseSensitive
    );
  }, [findCaseSensitive, findOpen, findQuery, messages]);
  // 关键词、大小写开关或会话变化后游标可能越界，取值时直接夹到 [0, total-1]
  const findCursorIndex = findHits.length === 0 ? 0 : Math.min(Math.max(findCursor, 0), findHits.length - 1);
  const findFocusMessageID = findOpen && findHits.length > 0 ? findHits[findCursorIndex].messageID : null;
  // 决策条跳转与查找共用同一套定位：查找命中优先，否则用决策条临时设定的 id
  const focusMessageID = findFocusMessageID ?? decisionFocusID;
  // 交给时间线定位并加亮的那一处命中（同一条消息内的第 occurrence 处）
  const activeFindMatch =
    findOpen && findHits.length > 0
      ? { messageID: findHits[findCursorIndex].messageID, occurrence: findHits[findCursorIndex].occurrence }
      : null;

  const closeFind = useCallback(() => {
    setFindOpen(false);
    setFindQuery('');
    setFindCursor(0);
  }, []);

  const toggleFind = useCallback(() => {
    if (findOpen) {
      closeFind();
      return;
    }
    setFindQuery('');
    setFindCursor(0);
    setFindOpen(true);
  }, [closeFind, findOpen]);

  const handleFindQueryChange = useCallback((value: string) => {
    setFindQuery(value);
    setFindCursor(0);
  }, []);

  // 切大小写开关等于换了一套命中，游标归零重算
  const handleFindCaseSensitiveChange = useCallback((value: boolean) => {
    setFindCaseSensitive(value);
    setFindCursor(0);
  }, []);

  const stepFind = useCallback((delta: number) => {
    setFindCursor((cursor) => {
      const total = findHits.length;
      if (total === 0) return 0;
      const next = (Math.min(cursor, total - 1) + delta) % total;
      return next < 0 ? next + total : next;
    });
  }, [findHits.length]);
  const handleFindPrevious = useCallback(() => stepFind(-1), [stepFind]);
  const handleFindNext = useCallback(() => stepFind(1), [stepFind]);

  // 新一轮开始运行后，本端「已中断」标记自动失效（再次发送也会在发起处清除）
  useEffect(() => {
    if (latestSummary.isRunning) setLocallyInterrupted(false);
  }, [latestSummary.isRunning]);

  // 错误卡片的「继续」与继续胶囊共用的续跑发送：只发一条续跑提示，不动草稿，也不走会清草稿的普通发送路径
  const handleContinueTurn = useCallback(
    async (prompt: string) => {
      const text = prompt.trim();
      if (!text || !isConnected || sendInFlightRef.current) return;
      sendInFlightRef.current = true;
      setIsSending(true);
      setSendError(null);
      setActionQueryError(null);
      try {
        const params: Parameters<typeof sendThreadMessage>[2] = {};
        if (composerOptionsSupported) {
          // 与普通发送一致：只带上相对摘要发生变化的选择
          const options = changedComposerOptions(composerOptions, summaryComposerOptions(latestSummary));
          if (Object.keys(options).length > 0) params.options = options;
        }
        // 继续胶囊/错误重试也是一次用户主动发送：同样在发出前建锚定窗口
        revealSentMessage();
        const action = await sendThreadMessage(thread.id, text, params);
        if (!mountedRef.current) return;
        if (action.state === 'accepted') {
          setLocallyInterrupted(false);
          beginPolling(action.turnID ?? null);
        } else if (action.state === 'failed') {
          releaseSendAnchor(true);
          setSendError(action.error || '继续失败，请稍后重试');
        }
      } catch (err) {
        releaseSendAnchor(true);
        if (mountedRef.current) {
          setSendError(err instanceof Error ? err.message : '继续失败，请稍后重试');
        }
      } finally {
        sendInFlightRef.current = false;
        if (mountedRef.current) setIsSending(false);
      }
    },
    [
      revealSentMessage,
      beginPolling,
      composerOptions,
      composerOptionsSupported,
      isConnected,
      latestSummary,
      releaseSendAnchor,
      sendThreadMessage,
      thread.id
    ]
  );

  // 继续胶囊：最后一条可见消息是不是错误卡片，决定失败后还要不要再给胶囊（错误卡片自己已经带「继续」）
  const lastVisibleMessage = messages.length > 0 ? messages[messages.length - 1] : undefined;
  const lastVisibleMessageIsError = isErrorMessage(lastVisibleMessage);
  // 草稿非空（含附件）时不显示继续胶囊，与桌面端 internal/ui/composer.go:364 的 hasContent 一致
  const composerHasContent = draft.trim() !== '' || attachments.length > 0;
  // 跟进队列编辑组借出时需要把输入框当前草稿与附件一起寄存给宿主
  const queueComposerDraft = useMemo(
    () => ({ text: draft, attachmentIDs: attachments.map((attachment) => attachment.attachmentID) }),
    [attachments, draft]
  );

  // 运行中宿主拒绝 `#模型 正文` 委派，也不进入队列：草稿一旦是委派写法就禁用发送
  const runningDelegation = Boolean(
    commandsSupported && latestSummary.isRunning && parseDelegation(draft, providers)
  );
  const sendBlockedReason = !isConnected
    ? '当前未连接到 Mac，发送已暂停'
    : isSyncing
    ? '正在同步最新状态，请稍候'
    : isLoading || !historyInitialDone
    ? '正在读取会话历史'
    : swarmSupported && swarmLoading && swarmState === null
    ? '正在确认会话状态'
    : errorMessage
    ? '会话历史读取失败，请先重试'
    : latestSummary.isArchived
    ? '已归档会话不能继续发送'
    : latestSummary.hasPendingRequest
    ? requestsSupported ? '请先处理上方待处理请求' : 'Mac 端有待处理请求，请先在 Mac 端处理'
    : unresolvedSendAction
    ? '发送结果待确认，请查询结果，不要重复发送'
    : runningDelegation
    ? '运行中无法委派给其他模型，请等本轮结束后再发送'
    : pollError
    ? '会话状态同步失败，请先刷新'
    : pollTargetTurnIDRef.current && !latestSummary.isRunning
    ? '上一条消息仍在等待 Mac 完成本轮'
    : isUploadingAttachment
    ? '正在上传图片'
    : isSending
    ? '正在提交发送请求'
    : null;
  // 空草稿只禁用发送键，不在输入框下方占一行提示。
  const sendDisabled = Boolean(sendBlockedReason) || (draft.trim().length === 0 && attachments.length === 0);
  const submitLabel = latestSummary.isRunning ? '排队' : '发送';
  const interruptAndSendDisabled = Boolean(
    !interruptAndSendSupported ||
    !isConnected ||
    !latestSummary.isRunning ||
    interruptAndSendState === 'sending' ||
    isUploadingAttachment ||
    latestSummary.hasPendingRequest ||
    Boolean(unresolvedSendAction) ||
    (!draft.trim() && attachments.length === 0)
  );
  const projectName = useMemo(() => {
    return projects?.find((p) => p.id === latestSummary.projectID)?.name || '项目';
  }, [projects, latestSummary.projectID]);
  // 时间线回合文件卡片只拿得到工具里的路径（相对工作目录或绝对路径），Web 端只有项目根目录可用
  const projectPath = useMemo(() => {
    return projects?.find((p) => p.id === latestSummary.projectID)?.path || '';
  }, [projects, latestSummary.projectID]);
  // 点击回合改动文件：相对路径直接开文件预览；绝对路径要先落在项目目录内，否则交给卡片展开行内差量
  const handleOpenTimelineFile = useCallback(
    (path: string): boolean => {
      const trimmed = path.trim();
      if (!trimmed) return false;
      let relative = trimmed;
      if (trimmed.startsWith('/')) {
        const root = projectPath.replace(/\/+$/, '');
        if (!root || !trimmed.startsWith(`${root}/`)) return false;
        relative = trimmed.slice(root.length + 1);
      }
      if (!relative) return false;
      replaceThreadRoute(`/files/${encodeURIComponent(relative)}`);
      return true;
    },
    [projectPath, replaceThreadRoute]
  );
  const branchOrWorkspace = (latestSummary as any).branch || (latestSummary as any).workspace;
  const subtitle = branchOrWorkspace ? `${projectName} · ${branchOrWorkspace}` : projectName;
  const quotaScope = useMemo(() => ({ threadID: thread.id }), [thread.id]);

  const swarmAvailable = swarmState?.swarmAvailable ?? true;
  const swarmEnabled = swarmState?.swarmEnabled ?? latestSummary.swarmEnabled ?? false;
  // 本次选了另一个组合时，发送后会话一定开着编排
  const swarmShownOn = composerOptions.swarmPairID && composerOptions.swarmPairID !== (latestSummary.swarmPairID ?? undefined)
    ? true
    : swarmEnabled;

  const handleSwarmToggled = useCallback((enabled: boolean) => {
    rememberSelection({ projectID: latestSummary.projectID, swarmEnabled: enabled });
  }, [latestSummary.projectID, rememberSelection]);

  const {
    isRenaming,
    setIsRenaming,
    renameDraft,
    setRenameDraft,
    isUpdatingThread,
    unarchiveError,
    isTogglingSwarm,
    swarmInlineError,
    handleTogglePin,
    handleToggleDormant,
    handleArchive,
    handleUnarchive,
    handleRename,
    handleToggleSwarm
  } = useThreadMetaActions({
    latestSummary,
    setLatestSummary,
    updateThread,
    refreshThread,
    menuSheetOpen,
    setMenuSheetOpen,
    swarmAvailable,
    setSwarmEnabled,
    // 编排开关是用户主动改动：切换成功后写入宿主记忆（与桌面端共用）
    onSwarmToggled: handleSwarmToggled
  });

  /** 关闭整个菜单：收起菜单、重置栈底，并把详情页路由清回会话根。 */
  const handleCloseMenu = useCallback(() => {
    setMenuSheetOpen(false);
    setIsRenaming(false);
    setMenuInitialPage({ kind: 'main' });
    if (subview) replaceThreadRoute();
  }, [subview, replaceThreadRoute]);

  /** 菜单内入栈/出栈时同步 hash；null 表示回到会话根路由。 */
  const handleMenuNavigate = useCallback((suffix: string | null) => {
    replaceThreadRoute(suffix ?? '');
  }, [replaceThreadRoute]);

  // 不论从哪个入口关闭菜单（关闭按钮、系统返回、菜单内部动作），都把栈底复位为主页。
  // 只在真正“打开 → 关闭”的跳变时重置：首帧与深链接同一次提交里打开菜单时不动新栈底。
  const previousMenuSheetOpenRef = useRef(menuSheetOpen);
  useEffect(() => {
    if (previousMenuSheetOpenRef.current && !menuSheetOpen) setMenuInitialPage({ kind: 'main' });
    previousMenuSheetOpenRef.current = menuSheetOpen;
  }, [menuSheetOpen]);

  // 切换会话（含转发成功进入新会话）时收起菜单，并把栈底复位为主页。
  // 必须声明在“路由 → 栈底”之前：深链接到另一会话的详情页时，先收再按新路由重新打开。
  const previousThreadIDRef = useRef(thread.id);
  useEffect(() => {
    if (previousThreadIDRef.current === thread.id) return;
    previousThreadIDRef.current = thread.id;
    setMenuSheetOpen(false);
    // ref 在 render 里同步赋值，此刻仍是旧值 true；不就地复位，“路由 → 栈底”effect 会读到 true 而直接返回。
    menuSheetOpenRef.current = false;
    setMenuInitialPage({ kind: 'main' });
  }, [thread.id]);

  // 路由 → 栈底：深链接、刷新、时间线入口进入详情页时，菜单以该页为栈底打开。
  // 菜单已打开时不干预（入栈由菜单自己同步 hash）；subview 变空也不关菜单（那是从详情页返回主菜单）。
  useEffect(() => {
    const base = menuBaseForRoute(subview, filePath, {
      reviewSupported,
      gitSupported,
      scriptsSupported,
      filesSupported
    });
    if (!base || menuSheetOpenRef.current) return;
    setMenuInitialPage(base);
    setMenuSheetOpen(true);
  }, [subview, filePath, reviewSupported, gitSupported, scriptsSupported, filesSupported]);

  const shareActionsBlocked = !isConnected || isSyncing;

  const {
    branchConfirmMessageID,
    setBranchConfirmMessageID,
    isBranching,
    branchError,
    setBranchError,
    handleConfirmBranch,
    setForwardConfirmOpen,
    isForwarding,
    forwardError,
    setForwardError,
    forwardActionRef,
    saveTaskOpen,
    setSaveTaskOpen,
    saveTaskTitle,
    setSaveTaskTitle,
    saveTaskNotes,
    setSaveTaskNotes,
    isSavingTask,
    saveTaskError,
    setSaveTaskError,
    handleConfirmForward,
    handleOpenSaveTask,
    handleSubmitSaveTask,
    handleBranchMessage
  } = useThreadShareActions({
    threadID: thread.id,
    projectID: latestSummary.projectID,
    threadTitle: latestSummary.title,
    shareActionsBlocked,
    branchThread,
    forwardThread,
    createTask,
    refreshBoard,
    refreshThread,
    loadThreads
  });

  // 引用到回复：按桌面 quoteIntoDraft 的格式插进草稿末尾并聚焦（timeline_menu.go:35）
  const handleQuoteMessage = useCallback((messageID: string) => {
    const message = messagesRef.current.find((entry) => entry.id === messageID);
    if (!message) return;
    composerRef.current?.insertDraft(message.text || '');
  }, []);

  // 编辑后重发：弹框预填原文，提交时才分支/新建会话（timeline_menu.go:40）
  const handleEditMessage = useCallback((messageID: string) => {
    const message = messagesRef.current.find((entry) => entry.id === messageID);
    if (!message) return;
    setEditTarget({ messageID, text: message.text || '' });
  }, []);

  // 撤销此轮改动：先弹预览，确认后才真正落盘（timeline_menu.go:55）
  const handleRevertTurn = useCallback((messageID: string, turnID: string) => {
    setRevertTarget({ messageID, turnID });
  }, []);

  const handleTurnReverted = useCallback(
    (result: { restorableCount: number; keptCount: number }) => {
      setMenuNotice(
        `已还原 ${result.restorableCount} 个文件${
          result.keptCount > 0 ? `，保留 ${result.keptCount} 个文件` : ''
        }`
      );
      void refreshThread(thread.id);
    },
    [refreshThread, thread.id]
  );

  // 编辑后重发：分支到该消息之前最近的助手最终答复，再在新会话里发改后的文本；
  // 之前没有助手答复时新建会话（timeline_menu.go:321 的 submitInlineEdit）。
  const handleSubmitEditedMessage = useCallback(
    async (text: string) => {
      if (!editTarget) return;
      const messages = messagesRef.current;
      const index = messages.findIndex((entry) => entry.id === editTarget.messageID);
      const anchorID =
        index <= 0
          ? null
          : [...messages.slice(0, index)]
              .reverse()
              .find((entry) => entry.role === 'assistant' && entry.isFinalResponse)?.id ?? null;
      let targetThreadID: string;
      if (anchorID) {
        const res = await branchThread({
          threadID: thread.id,
          messageID: anchorID,
          actionID: makeActionID()
        });
        if (!res?.threadID) throw new Error('Mac 端未返回新会话 ID');
        targetThreadID = res.threadID;
      } else {
        const projectID = latestSummary.projectID;
        if (!projectID) throw new Error('未找到会话所属项目，无法新建会话');
        const created = await createThread(projectID);
        if (created.state !== 'accepted' || !created.threadID) {
          throw new Error(created.error || '新建会话结果待确认，请稍后重试');
        }
        targetThreadID = created.threadID;
      }
      const sent = await sendThreadMessage(targetThreadID, text);
      if (sent.state === 'failed') throw new Error(sent.error || 'Mac 端未接受请求');
      setEditTarget(null);
      void loadThreads(latestSummary.projectID || null);
      window.location.hash = `#/t/${encodeURIComponent(targetThreadID)}`;
    },
    [
      branchThread,
      createThread,
      editTarget,
      latestSummary.projectID,
      loadThreads,
      sendThreadMessage,
      thread.id
    ]
  );

  // 发送到主会话：子会话才有父会话，文本格式与桌面 parentReportText 一致（timeline_menu.go:63）
  const parentThreadID = swarmState?.isBee ? swarmState.leadThreadID || null : null;
  const handleSendToParent = useCallback(
    async (messageID: string) => {
      if (!parentThreadID) return;
      const message = messagesRef.current.find((entry) => entry.id === messageID);
      if (!message) return;
      const title = (latestSummary.title || '').trim() || '未命名会话';
      const text = `[来自子会话 ${title}] ${message.text || ''}`;
      try {
        const action = await sendThreadMessage(parentThreadID, text);
        if (action.state === 'failed') throw new Error(action.error || 'Mac 端未接受请求');
        setMenuNotice('已发送到主会话');
      } catch (err) {
        setMenuNotice(`发送到主会话失败：${err instanceof Error ? err.message : '未知错误'}`);
      }
    },
    [latestSummary.title, parentThreadID, sendThreadMessage]
  );

  // 传给时间线的消息菜单动作：对象保持稳定，避免整条时间线因新对象重渲染
  const messageMenu = useMemo<MessageMenuActions>(
    () => ({
      onQuote: handleQuoteMessage,
      onEdit: canSend ? handleEditMessage : undefined,
      onRevertTurn: revertApi.supported ? handleRevertTurn : undefined,
      onSendToParent: parentThreadID ? handleSendToParent : undefined
    }),
    [
      canSend,
      handleEditMessage,
      handleQuoteMessage,
      handleRevertTurn,
      handleSendToParent,
      parentThreadID,
      revertApi.supported
    ]
  );

  const handleCopyMessage = useCallback((messageID: string, text: string, variant: CopyVariant) => {
    void copyMessage(messageID, text, variant);
  }, [copyMessage]);
  const canBranchMessages = Boolean(
    (!capabilities?.methods || capabilities.methods.includes('threads.branch')) && !swarmState?.isBee
  );

  // 输入区回调与 scope 稳定化：配合 ThreadComposer 的 React.memo，流式增量到来时输入区不再跟着重渲染。
  const composerCommandScope = useMemo(() => ({ threadID: thread.id }), [thread.id]);
  const handleToggleSwarmForComposer = useCallback(
    (on: boolean) => {
      void handleToggleSwarm(on);
    },
    [handleToggleSwarm]
  );
  const handleInterruptAndSendForComposer = useCallback(
    (overrideText?: string) => handleInterruptAndSend(overrideText),
    [handleInterruptAndSend]
  );
  const handleAddAttachmentsForComposer = useCallback(
    (files: FileList | null) => {
      void handleAddAttachments(files);
    },
    [handleAddAttachments]
  );
  const handleSendForComposer = useCallback(
    (overrideText?: string) => handleSend(overrideText),
    [handleSend]
  );
  const handleStopForComposer = useCallback(() => {
    void handleInterrupt();
  }, [handleInterrupt]);

  // 子代理页：输入区的停止按钮走「停止子代理」确认流程，可用条件与原先底部停止按钮一致
  const helperStopDisabled =
    Boolean(swarmState?.isBee) &&
    (!swarmState?.bee?.canStop || !isConnected || helperStopAction?.state === 'submitting');
  const handleOpenHelperStopConfirm = useCallback(() => {
    setHelperStopConfirmOpen(true);
  }, [setHelperStopConfirmOpen]);

  // 子代理页头部状态：文字沿用 getHelperStatusDisplay，圆点配同色系
  const helperStatusView = useMemo(() => {
    const status = swarmState?.bee?.status;
    const display = getHelperStatusDisplay(status);
    const dot =
      status === 'running'
        ? 'bg-sky-500 animate-pulse'
        : status === 'completed'
        ? 'bg-slate-400 dark:bg-zinc-500'
        : status === 'failed'
        ? 'bg-rose-500'
        : 'bg-[var(--foreground-muted)]';
    return { label: display.label, className: display.className, dot };
  }, [swarmState?.bee?.status]);

  // 头部耗时与 getHelperDurationText 同源（startedAt → now/finishedAt），只是换成 m:ss
  const helperElapsedSeconds = useMemo(() => {
    const bee = swarmState?.bee;
    if (!bee?.startedAt) return 0;
    const startMs = new Date(bee.startedAt).getTime();
    if (Number.isNaN(startMs)) return 0;
    const endMs =
      bee.status === 'running'
        ? helperNowMs
        : bee.finishedAt
        ? new Date(bee.finishedAt).getTime()
        : startMs;
    if (Number.isNaN(endMs)) return 0;
    return Math.max(0, Math.floor((endMs - startMs) / 1000));
  }, [swarmState?.bee, helperNowMs]);

  // 子代理页的「待合并」角标与菜单项：跳到主会话并打开子代理面板（合并审阅在面板里）
  const handleOpenLeadAgents = useCallback(() => {
    const leadThreadID = swarmState?.leadThreadID;
    if (!leadThreadID) return;
    const hash = `#/t/${encodeURIComponent(leadThreadID)}/agents`;
    if (window.location.hash !== hash) {
      window.history.replaceState(window.history.state, '', hash);
    }
    window.dispatchEvent(new HashChangeEvent('hashchange'));
  }, [swarmState?.leadThreadID]);

  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const distanceFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
    setShowJumpToLatest(distanceFromBottom > 96);
    recomputeActiveExpanded();
  }, [recomputeActiveExpanded]);

  // 首帧还没有子代理状态时按会话摘要预留等高占位，避免顶部条延迟出现把时间线往下推
  const swarmPending = swarmSupported && swarmState === null && swarmLoading;
  const reserveBeeBar = swarmPending && Boolean(latestSummary.isHelper);
  const reserveSwarmChip = swarmPending && !latestSummary.isHelper && Boolean(latestSummary.swarmBusy);

  const showFloatingStatus =
    !isConnected ||
    isSyncing ||
    isPolling ||
    streamState === 'subscribing' ||
    latestSummary.isRunning ||
    swarmRunningCount + swarmQueuedCount > 0;

  // 菜单内二级页：内容体由页面栈渲染，外壳（标题/返回/宽度）归菜单自己管。
  // 所以每个分支只包一层 inline 的 LazyBoundary，不能再套 sheet 遮罩。
  const renderExternalPage = ({
    page,
    ref,
    onClose
  }: {
    page: ThreadMenuPage;
    ref: React.Ref<ThreadMenuPageHandle>;
    onClose: () => void;
  }) => {
    switch (page.kind) {
      case 'agents':
        return (
          <LazyBoundary label="正在加载子代理…">
            <SwarmBeesSheetBody
              ref={ref}
              onClose={onClose}
              state={swarmState}
              onStopBee={stopSwarmBee}
              onStopAll={swarmStopAllSupported ? stopAllSwarmBees : undefined}
              onClearFinished={swarmClearFinishedSupported ? clearFinishedSwarmBees : undefined}
              onForwardQuestion={swarmForwardQuestionSupported ? forwardSwarmBeeQuestion : undefined}
              getMergeDiff={swarmMergeApprovalSupported ? getSwarmMergeDiff : undefined}
              onApproveMerge={swarmMergeApprovalSupported ? approveSwarmMerge : undefined}
              onDiscardMerge={swarmMergeApprovalSupported ? discardSwarmMerge : undefined}
              onOpenThread={replaceThreadByID}
              providers={providers}
            />
          </LazyBoundary>
        );
      case 'review':
        if (!reviewSupported) return null;
        return (
          <LazyBoundary label="正在加载改动…">
            <ThreadReviewSheetBody
              ref={ref}
              onClose={onClose}
              threadID={thread.id}
              threadTitle={latestSummary.title}
              isHelper={Boolean(swarmState?.isBee)}
              linkedTask={linkedTask}
              onAppendToDraft={(feedback) => {
                const current = draftRef.current;
                updateDraft(current.trim() ? `${current}\n\n${feedback}` : feedback);
              }}
            />
          </LazyBoundary>
        );
      case 'git':
        return (
          <LazyBoundary label="正在加载 Git 状态…">
            <GitStatusSheetBody
              ref={ref}
              onClose={onClose}
              threadID={thread.id}
              threadTitle={latestSummary.title}
            />
          </LazyBoundary>
        );
      case 'scripts':
        return (
          <LazyBoundary label="正在加载脚本…">
            <ScriptsSheetBody
              ref={ref}
              onClose={onClose}
              threadID={thread.id}
              threadTitle={latestSummary.title}
              projectID={latestSummary.projectID}
            />
          </LazyBoundary>
        );
      case 'files':
        return (
          <LazyBoundary label="正在加载文件…">
            <FileBrowserSheetBody
              ref={ref}
              onClose={onClose}
              threadID={thread.id}
              threadTitle={latestSummary.title}
              initialPath={page.filePath ?? ''}
              onPathChange={handleFilePathChange}
              onReference={canSend && !swarmState?.isBee ? (path) => {
                const current = draftRef.current;
                updateDraft(`${current}${current && !/\s$/.test(current) ? ' ' : ''}@${path} `);
                onClose();
              } : undefined}
            />
          </LazyBoundary>
        );
      case 'forward':
        return (
          <LazyBoundary label="正在加载转发…">
            <ForwardConfirmBody
              ref={ref}
              isForwarding={isForwarding}
              shareActionsBlocked={shareActionsBlocked}
              forwardError={forwardError}
              setForwardConfirmOpen={setForwardConfirmOpen}
              setForwardError={setForwardError}
              handleConfirmForward={handleConfirmForward}
              onClose={onClose}
            />
          </LazyBoundary>
        );
      case 'save-task':
        return (
          <LazyBoundary label="正在加载存为任务…">
            <SaveTaskPage
              ref={ref}
              saveTaskOpen={saveTaskOpen}
              onDismiss={onClose}
              saveTaskTitle={saveTaskTitle}
              setSaveTaskTitle={setSaveTaskTitle}
              saveTaskNotes={saveTaskNotes}
              setSaveTaskNotes={setSaveTaskNotes}
              saveTaskError={saveTaskError}
              isSavingTask={isSavingTask}
              shareActionsBlocked={shareActionsBlocked}
              setSaveTaskOpen={setSaveTaskOpen}
              handleSubmitSaveTask={handleSubmitSaveTask}
            />
          </LazyBoundary>
        );
      default:
        return null;
    }
  };

  const unreadLabel = formatUnreadCount(unreadCount);

  return (
    <div className="flex-1 flex flex-col min-h-0 overflow-hidden relative w-full">
      {/* 头部：返回键、标题、副标题（项目 · 分支/工作区）、⋯ 菜单 */}
      <ThreadViewHeader
        onBack={onBack}
        latestSummary={latestSummary}
        subtitle={subtitle}
        showFloatingStatus={showFloatingStatus}
        isConnected={isConnected}
        connectionState={connectionState}
        reconnect={reconnect}
        turnElapsedSeconds={turnElapsedSeconds}
        swarmState={swarmState}
        beeStatusLabel={helperStatusView.label}
        beeStatusClassName={helperStatusView.className}
        beeStatusDotClassName={helperStatusView.dot}
        beeElapsedSeconds={helperElapsedSeconds}
        beeMenuOpen={beeMenuSheetOpen}
        setBeeMenuOpen={setBeeMenuSheetOpen}
        setIsRenaming={setIsRenaming}
        setRenameDraft={setRenameDraft}
        setMenuSheetOpen={setMenuSheetOpen}
        menuButtonRef={menuButtonRef}
        findOpen={findOpen}
        onToggleFind={toggleFind}
        threadID={thread.id}
        isRunning={latestSummary.isRunning}
        onOpenChanges={reviewSupported ? () => replaceThreadRoute('/review') : undefined}
      />

      {/* 会话内查找条：在头部下方展开，命中项由时间线 focusMessageID 定位 */}
      {findOpen && (
        <LazyBoundary label="正在加载查找条…">
          <ThreadFindBar
            query={findQuery}
            onQueryChange={handleFindQueryChange}
            current={findHits.length === 0 ? 0 : findCursorIndex + 1}
            total={findHits.length}
            caseSensitive={findCaseSensitive}
            onCaseSensitiveChange={handleFindCaseSensitiveChange}
            onPrevious={handleFindPrevious}
            onNext={handleFindNext}
            onClose={closeFind}
          />
        </LazyBoundary>
      )}

      {/* 子代理只读提示条 */}
      {(swarmState?.isBee || reserveBeeBar) && (
        <div className="shrink-0 flex items-center justify-between px-3 sm:px-4 py-2 border-b border-[var(--border)] bg-[var(--user-bubble)] text-xs">
          <div className="flex items-center gap-2 min-w-0 font-medium text-[var(--foreground)]">
            <span className="w-2 h-2 rounded-full bg-[var(--primary)] shrink-0" />
            <span className="truncate">
              子代理 {swarmState?.bee?.name || latestSummary.title || ''}
              {swarmState?.bee?.tier ? ` · ${swarmState.bee.tier}` : ''}
              {swarmState?.bee?.readOnly ? ' · 只读' : ''}
            </span>
            {/* 改动待合并 / 有未转交的提问：角标直接带出下一步动作 */}
            {swarmState?.bee?.mergeState === 'pending' && (
              <button
                type="button"
                onClick={handleOpenLeadAgents}
                title="回到主会话的子代理面板审阅合并"
                className="shrink-0 px-1.5 py-0.5 rounded-full text-mc-caption font-semibold bg-amber-100 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400 active:scale-95 transition"
              >
                待合并
              </button>
            )}
            {swarmState?.bee?.pendingQuestion?.forwarded === false && (
              <button
                type="button"
                onClick={() => {
                  // 不直接转交：跳回主会话，由主会话的提问条统一处理
                  if (swarmState?.leadThreadID) replaceThreadByID(swarmState.leadThreadID);
                }}
                title="回到主会话回答提问"
                className="shrink-0 px-1.5 py-0.5 rounded-full text-mc-caption font-semibold bg-sky-100 text-sky-700 dark:bg-sky-950/40 dark:text-sky-400 active:scale-95 transition"
              >
                有提问
              </button>
            )}
          </div>
          {swarmState?.leadThreadID && (
            <a
              href={`#/t/${encodeURIComponent(swarmState.leadThreadID!)}`}
              onClick={(event) => {
                event.preventDefault();
                replaceThreadByID(swarmState.leadThreadID!);
              }}
              className="text-[var(--primary)] hover:underline font-semibold shrink-0 ml-2"
            >
              返回主会话
            </a>
          )}
        </div>
      )}

      {/* 会话上下文条：关联任务与子代理合并为单行，详情默认折叠 */}
      {((!swarmState?.isBee && (swarmBees.length > 0 || swarmQueuedCount > 0)) || linkedTask || reserveSwarmChip) && (
        <div className="shrink-0 border-b border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 sm:px-4">
          <div className="flex min-h-[40px] items-center gap-2 text-xs">
            {linkedTask ? (
              <>
                <a
                  href={`#/task/${encodeURIComponent(linkedTask.id)}`}
                  className="min-w-0 flex-1 truncate font-semibold text-[var(--primary)] hover:underline"
                >
                  任务：{linkedTask.title}
                </a>
                <span className="thread-status shrink-0">{linkedTaskColumnLabel(linkedTask.displayColumn)}</span>
              </>
            ) : (
              <span className="min-w-0 flex-1" />
            )}
            {!swarmState?.isBee && (swarmBees.length > 0 || swarmQueuedCount > 0) && (
              <button
                type="button"
                onClick={() => replaceThreadRoute('/agents')}
                aria-label={`子代理：${swarmRunningCount} 个运行中，共 ${swarmBees.length} 个${swarmQueuedCount > 0 ? `，${swarmQueuedCount} 个排队中` : ''}${swarmPendingQuestionCount > 0 ? `，${swarmPendingQuestionCount} 个在等待回答` : ''}，查看详情`}
                className="inline-flex min-h-[32px] shrink-0 items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-semibold text-[var(--foreground)] hover:bg-[var(--user-bubble)]"
              >
                <span
                  className={`h-2 w-2 shrink-0 rounded-full ${
                    swarmRunningCount > 0 || swarmQueuedCount > 0
                      ? 'animate-pulse bg-[var(--primary)]'
                      : 'bg-[var(--foreground-muted)]'
                  }`}
                  aria-hidden="true"
                />
                <span>子代理 {swarmRunningCount}/{swarmBees.length}{swarmQueuedCount > 0 ? `，${swarmQueuedCount} 个排队中` : ''}</span>
                {swarmPendingQuestionCount > 0 && (
                  <span
                    aria-hidden="true"
                    className="grid h-4 min-w-[16px] place-items-center rounded-full bg-rose-500 px-1 text-mc-caption font-bold leading-none text-white"
                  >
                    ?
                  </span>
                )}
                <ChevronRightIcon className="h-3 w-3 text-[var(--foreground-muted)]" />
              </button>
            )}
            {reserveSwarmChip && (
              // 首帧占位：尺寸与真实按钮一致，状态返回后原地替换
              <button
                type="button"
                disabled
                aria-label="正在读取子代理状态"
                className="inline-flex min-h-[32px] shrink-0 items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-semibold text-[var(--foreground)] hover:bg-[var(--user-bubble)]"
              >
                <span className="h-2 w-2 shrink-0 animate-pulse rounded-full bg-[var(--primary)]" aria-hidden="true" />
                <span>子代理 …</span>
                <ChevronRightIcon className="h-3 w-3 text-[var(--foreground-muted)]" />
              </button>
            )}
            {linkedTask && (
              <button
                type="button"
                onClick={() => setContextBarOpen((open) => !open)}
                aria-expanded={contextBarOpen}
                aria-label={contextBarOpen ? '收起任务详情' : '展开任务详情'}
                className="inline-grid h-8 w-8 shrink-0 place-items-center rounded-lg text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)]"
              >
                <ChevronRightIcon className={`h-3.5 w-3.5 transition-transform ${contextBarOpen ? 'rotate-90' : ''}`} />
              </button>
            )}
          </div>
          {linkedTask && contextBarOpen && (
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pb-2 text-xs">
              {linkedGoal && (
                <a href={`#/goal/${encodeURIComponent(linkedGoal.id)}`} className="text-[var(--foreground-muted)] hover:text-[var(--primary)] hover:underline">
                  目标：{linkedGoal.title} · {linkedGoal.progress.done}/{linkedGoal.progress.total}
                </a>
              )}
              <span className="ml-auto flex items-center gap-3">
                <a href={`#/task/${encodeURIComponent(linkedTask.id)}`} className="text-[var(--primary)] hover:underline">Diff</a>
                {linkedTask.canApprove && (
                  <a href={`#/task/${encodeURIComponent(linkedTask.id)}`} className="font-semibold text-[var(--primary)] hover:underline">验收</a>
                )}
                {linkedTask.canReview && (
                  <a href={`#/task/${encodeURIComponent(linkedTask.id)}`} className="font-semibold text-[var(--warning-foreground)] hover:underline">退回</a>
                )}
              </span>
            </div>
          )}
        </div>
      )}

      {/* 时间线消息滚动区：外圈用来定位提问导航轨，轨道本身不随内容滚动 */}
      <div className="relative flex-1 min-h-0 flex">
        <div
          ref={scrollRef}
          onScroll={handleScroll}
          className="relative flex-1 min-w-0 overflow-y-auto p-3 sm:p-4"
          style={{
            overscrollBehavior: 'contain',
            WebkitOverflowScrolling: 'touch'
          }}
        >
        {/* 宽屏下正文限宽并居中，与底部输入区同列；滚动条仍贴窗口最右侧 */}
        <div ref={contentRef} className="space-y-3 w-full max-w-[880px] mx-auto">
          {/* 总管决策条：需要用户拍板的答复常驻在顶部，点标题跳过去，也可以手动关掉 */}
          {pendingDecision && !dismissedDecisionIDs.has(pendingDecision.id) && (
            <div
              role="status"
              className="sticky top-0 z-20 flex items-center gap-2 rounded-xl border border-[var(--warning-foreground)] bg-[var(--card)] px-3 py-2 shadow-sm"
            >
              <ExclamationTriangleIcon className="w-4 h-4 shrink-0 text-[var(--warning-foreground)]" />
              <span className="shrink-0 text-mc-footnote font-semibold text-[var(--warning-foreground)]">需要你决定</span>
              <button
                type="button"
                onClick={() => jumpToDecision(pendingDecision.id)}
                title={pendingDecision.title}
                className="min-w-0 flex-1 truncate text-left text-mc-body text-[var(--foreground)] hover:underline"
              >
                {pendingDecision.title}
              </button>
              <button
                type="button"
                aria-label="关闭"
                onClick={() => dismissDecision(pendingDecision.id)}
                className="shrink-0 grid place-items-center w-6 h-6 rounded-md text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] transition"
              >
                <XMarkIcon className="w-3.5 h-3.5" />
              </button>
            </div>
          )}
          {/* 分叉来源：细线夹着来源会话标题，可点击跳回源会话 */}
          {forkedFromThreadID && (
            <div className="flex items-center gap-2 pb-1 text-mc-caption text-[var(--foreground-muted)]">
              <span className="h-px flex-1 bg-[var(--border)]" />
              {forkedFromTitle ? (
                <button
                  type="button"
                  onClick={() => {
                    if (forkedFromThreadID) replaceThreadByID(forkedFromThreadID);
                  }}
                  title="打开来源会话"
                  className="max-w-[70%] shrink-0 truncate rounded-md px-1.5 py-0.5 text-[var(--primary)] transition hover:bg-[var(--user-bubble)] active:opacity-75"
                >
                  {isContinuation ? `继续自「${forkedFromTitle}」` : `分叉自「${forkedFromTitle}」`}
                </button>
              ) : (
                <span className="shrink-0 px-1.5 py-0.5">
                  {isContinuation ? '继续自其它会话' : '分叉自其它会话'}
                </span>
              )}
              <span className="h-px flex-1 bg-[var(--border)]" />
            </div>
          )}
          {/* 历史截断提示：作为顶部弱化提示行 */}
          {truncated && (
            <div
              role="status"
              className="py-1.5 px-3 rounded-lg bg-[var(--card-alt,var(--card))] border border-[var(--border)] text-center text-mc-footnote text-[var(--foreground-muted)] flex items-center justify-center gap-1.5 shrink-0"
            >
              <ExclamationTriangleIcon className="w-3.5 h-3.5 text-[var(--warning-foreground)] shrink-0" />
              <span>部分消息因长度上限被截断，完整内容请在 Mac 桌面端查看。</span>
            </div>
          )}

          {/* 历史读取失败提示（当没有已读消息时展示为空/错误状态） */}
          {errorMessage && timelineItems.length === 0 && (
            <div className="flex flex-col items-center justify-center text-center p-8 mt-6 bg-[var(--card)] rounded-3xl border border-[var(--border)] space-y-3">
              <div className="w-12 h-12 rounded-2xl bg-[var(--user-bubble)] text-[var(--danger-foreground)] flex items-center justify-center text-xl font-bold">
                ⚠️
              </div>
              <h3 className="text-base font-semibold text-[var(--foreground)]">会话历史加载失败</h3>
              <p className="text-xs text-[var(--foreground-muted)] max-w-sm">{errorMessage}</p>
              <button
                type="button"
                onClick={() => {
                  const req = lastFailedRequestRef.current;
                  void loadPage(req ? req.cursor : null, req ? req.append : false);
                }}
                className="min-h-[36px] px-4 rounded-xl text-xs font-semibold bg-[var(--primary)] text-white hover:opacity-90 transition active:scale-95"
              >
                重试
              </button>
            </div>
          )}

          {/* 加载中 */}
          {isLoading && timelineItems.length === 0 && !errorMessage && (
            <div
              role="status"
              aria-live="polite"
              className="flex flex-col items-center justify-center gap-3 py-14 text-xs text-[var(--foreground-muted)]"
            >
              <span className="w-6 h-6 border-2 border-[var(--primary)] border-t-transparent rounded-full animate-spin" />
              <span>正在加载会话历史...</span>
            </div>
          )}

          {/* 空会话 */}
          {!isLoading && !errorMessage && timelineItems.length === 0 && (
            <div className="flex flex-col items-center justify-center text-center p-8 mt-6 bg-[var(--card)] rounded-3xl border border-[var(--border)] space-y-3">
              <div className="w-12 h-12 rounded-2xl bg-[var(--user-bubble)] flex items-center justify-center text-xl font-bold">
                🗒️
              </div>
              <h3 className="text-base font-semibold text-[var(--foreground)]">
                该会话暂无消息
              </h3>
              <p className="text-xs text-[var(--foreground-muted)] max-w-sm">
                会话历史为空，或在当前 Mac 端会话数据中不可读。
              </p>
            </div>
          )}

          {/* 更早历史入口：历史按最新页优先，翻页向上取更早的消息 */}
          {hasMore && (
            <button
              type="button"
              disabled={isLoadingMore || !nextCursor}
              onClick={() => void loadPage(nextCursor, true)}
              className="w-full min-h-[44px] px-4 py-2.5 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground)] transition flex items-center justify-center gap-2 disabled:opacity-40"
            >
              {isLoadingMore ? (
                <>
                  <span className="w-3.5 h-3.5 border-2 border-[var(--primary)] border-t-transparent rounded-full animate-spin" />
                  <span>正在加载更早的消息...</span>
                </>
              ) : (
                <>
                  <ArrowPathIcon className="w-4 h-4" />
                  <span>加载更早的消息</span>
                </>
              )}
            </button>
          )}

          {/* 已到会话开头 */}
          {!hasMore && timelineItems.length > 0 && (
            <div className="text-center text-mc-caption text-[var(--foreground-muted)] pb-1">
              已到会话开头
            </div>
          )}

          <ThreadTimeline
            threadID={thread.id}
            items={timelineItems}
            expandedIds={expandedIds}
            copyState={copyState}
            onToggleExpanded={toggleExpanded}
            onToggleWorkRecord={toggleWorkRecord}
            onCopyMessage={handleCopyMessage}
            canBranch={canBranchMessages}
            onBranchMessage={handleBranchMessage}
            messageMenu={messageMenu}
            readThreadMedia={mediaSupported ? readThreadMedia : undefined}
            scrollRef={scrollRef}
            swarmState={swarmState}
            onOpenSwarmBee={replaceThreadByID}
            onOpenSwarmBees={() => replaceThreadRoute('/agents')}
            recallMessageID={recallMessageID}
            onRecallTurn={recallSupported ? openRecallConfirm : undefined}
            onKillRunningCommand={killRunningCommandSupported ? handleKillRunningCommand : undefined}
            focusMessageID={focusMessageID}
            scrollToMessageRequest={scrollJumpRequest}
            activeFindMatch={activeFindMatch}
            findQuery={findQuery}
            findCaseSensitive={findCaseSensitive}
            isRunning={latestSummary.isRunning}
            onContinue={handleContinueTurn}
            onOpenFile={filesSupported ? handleOpenTimelineFile : undefined}
            bottomPadding={sendAnchor.bottomPadding}
            isSteward={isStewardThread}
          />

        </div>
      </div>

        {/* 提问导航轨：贴在时间线右缘，窄屏是一条细轨 + 点击弹出提问列表 */}
        <QuestionMinimap data={questionMinimapData} containerRef={scrollRef} onJump={jumpToQuestion} />
      </div>

      {/* 右下角浮动胶囊：收起展开的工作记录与跳到最新共用一个容器，避免两个浮层各自定位对不齐 */}
      {(activeExpandedTurnID || showJumpToLatest) && (
        <div
          className="absolute z-30 flex h-9 items-center overflow-hidden rounded-full border border-[var(--border)] bg-[var(--card)] shadow-md text-[var(--foreground)] transition duration-[var(--mc-dur-base)] motion-reduce:transition-none"
          style={{
            bottom: `${bottomHeight + 10}px`,
            // 定位父元素即本视图根容器，与滚动区同宽：宽屏贴到居中正文列（880px）右外侧，窄屏回退为 right-4（1rem）
            right: 'max(1rem, calc((100% - 880px) / 2 - 3.5rem))'
          }}
        >
          {activeExpandedTurnID && (
            <button
              type="button"
              onClick={() => collapseWorkRecord(activeExpandedTurnID)}
              aria-label="收起工作记录"
              className="flex h-9 items-center gap-1.5 px-3 text-xs text-[var(--foreground-muted)] transition hover:bg-[var(--card-alt,var(--card))] hover:text-[var(--foreground)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--primary)]"
            >
              <ChevronRightIcon className="w-3.5 h-3.5 -rotate-90" />
              <span>收起</span>
            </button>
          )}
          {activeExpandedTurnID && showJumpToLatest && (
            <span className="h-4 w-px shrink-0 bg-[var(--border)]" aria-hidden="true" />
          )}
          {showJumpToLatest && (
            <button
              type="button"
              onClick={() => {
                // 解除发送后锚定并直接贴到底：流式输出期间不会被中途增长的内容甩在后面。
                releaseSendAnchor(true);
                setShowJumpToLatest(false);
                setUnreadAnchor(null);
              }}
              aria-label={unreadLabel ? `跳到最新，${unreadLabel}` : '跳到最新'}
              className={`flex h-9 items-center justify-center transition hover:bg-[var(--card-alt,var(--card))] active:scale-95 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-[var(--primary)] ${
                unreadLabel ? 'px-3.5 gap-1.5 text-xs font-semibold' : 'w-9'
              }`}
            >
              <ChevronDownIcon className="w-4 h-4 shrink-0" />
              {unreadLabel ? <span className="whitespace-nowrap">{unreadLabel}</span> : null}
            </button>
          )}
        </div>
      )}


      {/* 底部输入区与状态卡片容器 */}
      <div
        ref={bottomRef}
        className="shrink-0 flex flex-col min-w-0"
        style={{
          paddingLeft: 'max(0px, var(--sal))',
          paddingRight: 'max(0px, var(--sar))'
        }}
      >
        {/* 待处理授权与提问卡片 */}
        {requestSnapshot && (
          <div
            ref={requestPanelRef}
            className={`shrink-0 min-w-0 ${requestPanelFlashNonce > 0 ? 'push-target-flash' : ''}`}
          >
            <LazyBoundary>
              <RemoteRequestPanel
                threadID={thread.id}
                requests={requestSnapshot.requests}
                isConnected={isConnected}
                skipSupported={Boolean(capabilities?.features?.includes('requests.skip'))}
                onRespond={handleRemoteRespond}
                onRefresh={async () => {
                  await listRequests(thread.id);
                }}
              />
            </LazyBoundary>
          </div>
        )}

        {/* 跟进队列（位于输入区上方、RemoteRequestPanel 下方）：分组编辑、撤回、立即发送、插话与上下移都在组件内 */}
        {!swarmState?.isBee && (
          <>
            <QueuedFollowUpSection
              key={thread.id}
              threadID={thread.id}
              followUps={queueSnapshot?.followUps ?? []}
              revealRequest={queueRevealRequest}
              isRunning={latestSummary.isRunning}
              provider={latestSummary.provider}
              composerDraft={queueComposerDraft}
              onRestoreDraft={updateDraft}
            />
            {queuePollError && (
              <div className="w-full max-w-[880px] mx-auto px-4 py-1 text-xs text-[var(--danger-foreground)] break-words">
                {queuePollError}
              </div>
            )}
          </>
        )}

        {/* 待办进度胶囊：最新一份待办清单没做完时常驻在输入框上方，点开展开完整清单 */}
        {!swarmState?.isBee && <TodoProgressPill messages={messages} isRunning={latestSummary.isRunning} />}

        {/* 继续胶囊：上一轮被中断或失败后，在输入框上方一键续跑（草稿非空、运行中、子代理页都不显示） */}
        {!swarmState?.isBee && canSend && !latestSummary.isRunning && !composerHasContent && (
          <div className="w-full max-w-[880px] mx-auto px-4">
            <ContinueTurnPill
              lastStatus={latestSummary.lastStatus}
              isRunning={latestSummary.isRunning}
              locallyInterrupted={locallyInterrupted}
              lastMessageID={lastVisibleMessage?.id}
              lastMessageIsError={lastVisibleMessageIsError}
              onContinue={handleContinueTurn}
              threadID={thread.id}
            />
          </div>
        )}

        {/* 消息菜单操作的结果提示（引用、撤销、发回父会话）：子代理页也要显示 */}
        {menuNotice && (
          <div
            role="status"
            className="shrink-0 max-h-24 overflow-y-auto px-4 py-1.5 text-xs flex items-center justify-between gap-2 border-t border-[var(--border)] bg-[var(--card-alt,var(--card))] text-[var(--foreground)]"
          >
            <span className="min-w-0 break-words">{menuNotice}</span>
            <button
              type="button"
              onClick={() => setMenuNotice(null)}
              aria-label="收起提示"
              className="min-h-[32px] min-w-[32px] inline-flex items-center justify-center rounded-full text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] active:scale-95 transition"
            >
              <XMarkIcon className="w-3.5 h-3.5" />
            </button>
          </div>
        )}

        {/* 本地命令已执行 / #模型已委派：这两种结果没有新轮次，用提示条说明 */}
        {!swarmState?.isBee && commandNotice && (
          <div
            role="status"
            className="shrink-0 max-h-24 overflow-y-auto px-4 py-1.5 text-xs flex items-center justify-between gap-2 border-t border-[var(--border)] bg-[var(--card-alt,var(--card))] text-[var(--foreground)]"
          >
            <span className="min-w-0 break-words">
              {commandNotice.kind === 'delegated'
                ? '已委派给其他模型的子代理，它会独立运行，结果不会出现在本会话里。'
                : commandNotice.text}
            </span>
            <div className="flex items-center gap-1.5 shrink-0">
              {commandNotice.kind === 'delegated' && (
                <button
                  type="button"
                  onClick={() => {
                    window.location.hash = `#/t/${encodeURIComponent(commandNotice.threadID)}`;
                  }}
                  className="min-h-[32px] px-3 rounded-full font-medium bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--primary)] active:scale-95 transition"
                >
                  查看子代理
                </button>
              )}
              <button
                type="button"
                onClick={() => setCommandNotice(null)}
                aria-label="收起提示"
                className="min-h-[32px] min-w-[32px] inline-flex items-center justify-center rounded-full text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] active:scale-95 transition"
              >
                <XMarkIcon className="h-3.5 w-3.5" />
              </button>
            </div>
          </div>
        )}

        {/* 发送结果待确认：超时不自动重放，由用户查询或收起 */}
        {canSend && latestSendAction && latestSendAction.state !== 'accepted' && (
          <div
            role={isUnresolvedAction(latestSendAction) ? 'alert' : 'status'}
            className={`shrink-0 max-h-24 overflow-y-auto px-4 py-1.5 text-xs flex items-center justify-between gap-2 border-t border-[var(--border)] bg-[var(--card-alt,var(--card))] ${
              latestSendAction.state === 'failed'
                ? 'text-[var(--danger-foreground)]'
                : isUnresolvedAction(latestSendAction)
                ? 'text-[var(--warning-foreground)]'
                : 'text-[var(--foreground-muted)]'
            }`}
          >
            <span className="min-w-0 break-words">
              {latestSendAction.state === 'submitting'
                ? '正在向 Mac 提交消息...'
                : latestSendAction.state === 'pending'
                ? '消息已提交，发送结果待确认。'
                : latestSendAction.state === 'unknown'
                ? '暂时无法确认发送结果，确认前不要重复发送。'
                : `发送失败：${latestSendAction.error || 'Mac 端未接受请求'}，草稿已保留。`}
            </span>
            {isUnresolvedAction(latestSendAction) ? (
              <button
                type="button"
                onClick={() => void handleCheckAction(latestSendAction)}
                disabled={checkingActionID === latestSendAction.actionID || !isConnected}
                className="min-h-[32px] px-3 rounded-full font-medium shrink-0 bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--foreground)] disabled:opacity-45 active:scale-95 transition"
              >
                {checkingActionID === latestSendAction.actionID ? '查询中...' : '查询结果'}
              </button>
            ) : latestSendAction.state === 'failed' ? (
              <button
                type="button"
                onClick={() => handleDismissAction(latestSendAction)}
                className="min-h-[32px] px-3 rounded-full font-medium shrink-0 bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--foreground)] active:scale-95 transition"
              >
                收起
              </button>
            ) : null}
          </div>
        )}

        {/* 实时同步失败：可手动重试订阅 */}
        {eventsSupported && streamError && (
          <div role="alert" className="shrink-0 max-h-24 overflow-y-auto px-4 py-1.5 text-xs flex items-center justify-between gap-2 border-t border-[var(--border)] bg-[var(--card-alt,var(--card))] text-[var(--danger-foreground)]">
            <span className="min-w-0 break-words">实时会话同步失败：{streamError}</span>
            <button
              type="button"
              disabled={streamState === 'subscribing'}
              onClick={() => {
                setStreamError(null);
                void retryThreadSubscription().catch((err) => {
                  if (!mountedRef.current) return;
                  setStreamError(err instanceof Error ? err.message : '重试实时会话同步失败');
                });
              }}
              className="min-h-[32px] px-3 rounded-full font-medium shrink-0 bg-[var(--subtle-strong,var(--user-bubble))] text-[var(--foreground)] disabled:opacity-45 active:scale-95 transition"
            >
              {streamState === 'subscribing' ? '重试中...' : '重试'}
            </button>
          </div>
        )}

        {/* 子代理提问：主会话里直接去回答，或交给主线程回答 */}
        {!swarmState?.isBee && swarmBees.some((bee) => Boolean(bee.pendingQuestion)) && (
          <LazyBoundary label="正在加载提问…">
            <SwarmQuestionBanner
              bees={swarmBees}
              onAnswer={replaceThreadByID}
              onForward={(beeThreadID, requestID) => void handleForwardSwarmQuestion(beeThreadID, requestID)}
            />
          </LazyBoundary>
        )}

        {/* 上下文压缩建议（threads.compactSuggestion） */}
        {!swarmState?.isBee && compactSupported && compactSuggestion && (
          <div
            role="status"
            className="shrink-0 px-4 py-1.5 text-xs flex items-center justify-between gap-2 border-t border-[var(--border)] bg-[var(--card)] text-[var(--foreground)]"
          >
            <span className="min-w-0 break-words">
              上下文已用 {compactSuggestion.usedTokens.toLocaleString()} tokens，建议压缩
            </span>
            <div className="flex items-center gap-1.5 shrink-0">
              <button
                type="button"
                onClick={() => void handleCompactNow()}
                disabled={compactState === 'compacting'}
                className="min-h-[28px] rounded-lg bg-[var(--primary)] px-3 font-medium text-white disabled:opacity-45 active:scale-95 transition"
              >
                {compactState === 'compacting' ? '压缩中…' : '压缩'}
              </button>
              <button
                type="button"
                onClick={() => setCompactSuggestion(null)}
                className="min-h-[28px] rounded-lg border border-[var(--border)] bg-[var(--card)] px-3 font-medium text-[var(--foreground)] active:scale-95 transition"
              >
                忽略
              </button>
            </div>
          </div>
        )}

        {/* 发送/动作错误提示：位于输入框正上方 */}
        {(sendError || attachmentError || actionQueryError || interruptError || killCommandError || threadActionError || (requestsSupported && requestLoadError)) && (
          <div role="alert" className="shrink-0 max-h-24 overflow-y-auto px-4 py-1.5 text-xs text-[var(--danger-foreground)] bg-[var(--card-alt,var(--card))] border-t border-[var(--border)] text-center break-words">
            {sendError || attachmentError || actionQueryError || interruptError || killCommandError || threadActionError || `无法读取待处理请求：${requestLoadError}`}
          </div>
        )}

        {/* 子代理页：停止操作状态核对提示（位于输入框上方；不显示时整块不渲染） */}
        {swarmState?.isBee && helperStopAction && helperStopAction.state !== 'accepted' && (
          <div className="shrink-0 border-t border-[var(--border)] bg-[var(--card)] px-4 py-2">
            <div
              role="status"
              className={`px-3 py-1.5 rounded-xl text-xs flex items-center justify-between gap-2 ${
                helperStopAction.state === 'failed'
                  ? 'bg-rose-50 dark:bg-rose-950/30 text-rose-600 dark:text-rose-400 border border-rose-200 dark:border-rose-900/40'
                  : helperStopAction.state === 'submitting'
                  ? 'bg-sky-50 dark:bg-sky-950/30 text-sky-600 dark:text-sky-400 border border-sky-200 dark:border-sky-900/40'
                  : 'bg-amber-50 dark:bg-amber-950/30 text-amber-700 dark:text-amber-400 border border-amber-200 dark:border-amber-900/40'
              }`}
            >
              <span className="min-w-0 truncate">
                {helperStopAction.state === 'submitting'
                  ? '正在向 Mac 端停止子代理…'
                  : helperStopAction.state === 'unknown' || helperStopAction.state === 'pending'
                  ? (helperStopAction.error || '停止结果待确认，请核对状态。')
                  : (helperStopAction.error || '停止失败')}
              </span>
              <div className="flex items-center gap-1.5 shrink-0">
                {(helperStopAction.state === 'unknown' || helperStopAction.state === 'pending') && (
                  <button
                    type="button"
                    onClick={() => void handleCheckHelperStop()}
                    disabled={isCheckingHelperStop || !isConnected}
                    className="min-h-[28px] px-2.5 rounded-lg font-medium bg-[var(--card)] text-[var(--foreground)] border border-[var(--border)] disabled:opacity-45 active:scale-95 transition"
                  >
                    {isCheckingHelperStop ? '核对中…' : '核对结果'}
                  </button>
                )}
                {helperStopAction.state === 'failed' && (
                  <button
                    type="button"
                    onClick={() => setHelperStopAction(null)}
                    className="min-h-[28px] px-2.5 rounded-lg font-medium bg-[var(--card)] text-[var(--foreground)] border border-[var(--border)] active:scale-95 transition"
                  >
                    收起
                  </button>
                )}
              </div>
            </div>
          </div>
        )}

        {/* 底部输入框或辅助只读状态条 */}
        {swarmSupported && swarmError && swarmState === null ? (
          <div
            className="shrink-0 border-t border-[var(--border)] bg-[var(--card)] px-4 py-3 text-center text-xs text-[var(--foreground-muted)] flex flex-col items-center justify-center gap-2"
            style={{ paddingBottom: 'calc(var(--sab, 0px) + 0.75rem)' }}
          >
            <span className="text-[var(--danger-foreground)]">读取会话协作状态失败：{swarmError.message}</span>
            <button
              type="button"
              onClick={() => void refetchSwarm()}
              className="min-h-[32px] px-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] text-xs font-semibold text-[var(--foreground)] hover:bg-[var(--user-bubble)] transition active:scale-95"
            >
              重试
            </button>
          </div>
        ) : canSend ? (
          <div className="w-full max-w-[880px] mx-auto">
            <ThreadComposer
              ref={composerRef}
              threadID={thread.id}
              commandScope={composerCommandScope}
              quotaScope={quotaScope}
              swarmBeeTiers={latestSummary.swarmBeeTiers}
              draft={draft}
              isSending={isSending}
              sendError={sendError || attachmentError}
              sendBlockedReason={sendBlockedReason}
              isPolling={isPolling}
              sendDisabled={sendDisabled}
              hasMessages={hasVisibleMessages}
              onSplitSession={splitSupported && splitView.show ? handleSplitSession : undefined}
              splitSessionDisabled={splitView.disabled}
              attachments={attachments}
              attachmentPreviews={attachmentPreviews}
              isUploadingAttachment={isUploadingAttachment}
              submitLabel={submitLabel}
              composerOptions={composerOptionsSupported ? composerOptions : undefined}
              providers={providers}
              composerOptionsDisabled={latestSummary.isRunning || isSending || Boolean(unresolvedSendAction)}
              modelOptionsDisabled={isSending || Boolean(unresolvedSendAction)}
              runningProviderID={latestSummary.isRunning ? latestSummary.provider : undefined}
              composerOptionsMessage={composerOptionsSupported
                ? providersLoading
                  ? '正在读取 Provider 目录…'
                  : providersError
                  ? `Provider 目录读取失败：${providersError}`
                  : null
                : '当前宿主未声明 composer options，将使用 Mac 默认设置。'}
              attachmentsSupported={attachmentsSupported}
              attachmentLimit={attachmentLimit}
              onComposerOptionsChange={composerOptionsSupported ? updateRunningComposerOptions : undefined}
              swarmEnabled={swarmState?.isBee ? undefined : swarmShownOn}
              swarmToggleDisabled={
                !swarmAvailable || isTogglingSwarm || (swarmLoading && swarmState === null)
              }
              swarmPairsAllowed={!swarmState?.isBee}
              onToggleSwarm={
                swarmState?.isBee || !(swarmSupported && composerOptionsSupported)
                  ? undefined
                  : handleToggleSwarmForComposer
              }
              onInterruptAndSend={
                interruptAndSendSupported && latestSummary.isRunning ? handleInterruptAndSendForComposer : undefined
              }
              interruptAndSendDisabled={interruptAndSendDisabled}
              isInterrupting={interruptAndSendState === 'sending'}
              onDraftChange={updateDraft}
              onAddAttachments={handleAddAttachmentsForComposer}
              onRemoveAttachment={removeAttachment}
              onSend={handleSendForComposer}
              historyEntries={composerHistoryEntryList}
              placeholder={composerPlaceholderFor(backgroundWaitCount(latestSummary))}
              isRunning={latestSummary.isRunning}
              onStop={swarmState?.isBee ? handleOpenHelperStopConfirm : handleStopForComposer}
              stopDisabled={helperStopDisabled}
              lockedModelLabel={
                swarmState?.isBee
                  ? modelDisplayName(
                      providers,
                      swarmState.bee?.provider ?? latestSummary.provider,
                      swarmState.bee?.model || latestSummary.model
                    ) || undefined
                  : undefined
              }
            />
          </div>
        ) : (
          <div
            className="shrink-0 border-t border-[var(--border)] bg-[var(--screen)] px-4 py-3 text-center text-mc-caption text-[var(--foreground-muted)]"
            style={{ paddingBottom: 'var(--composer-bottom-inset, 26px)' }}
          >
            当前 Mac 宿主未声明发送消息能力，保持只读浏览。
          </div>
        )}
      </div>

      {/* 子代理页精简菜单（返回主会话/停止子代理/审阅待合并/复制链接） */}
      {swarmState?.isBee && (
        <LazyBoundary label="正在加载菜单…" variant="sheet" sheetSize="menu">
          <BeeMenuSheet
            isOpen={beeMenuSheetOpen}
            beeName={swarmState.bee?.name || latestSummary.title}
            threadID={thread.id}
            canStop={Boolean(swarmState.bee?.canStop && isConnected)}
            stopSubmitting={helperStopAction?.state === 'submitting'}
            hasPendingMerge={swarmState.bee?.mergeState === 'pending'}
            hasLeadThread={Boolean(swarmState.leadThreadID)}
            onClose={() => setBeeMenuSheetOpen(false)}
            onBackToLead={() => {
              setBeeMenuSheetOpen(false);
              const leadThreadID = swarmState?.leadThreadID;
              if (leadThreadID) replaceThreadByID(leadThreadID);
            }}
            onStop={() => {
              setBeeMenuSheetOpen(false);
              handleOpenHelperStopConfirm();
            }}
            onReviewMerge={() => {
              setBeeMenuSheetOpen(false);
              handleOpenLeadAgents();
            }}
          />
        </LazyBoundary>
      )}

      {/* 菜单 sheet */}
      {menuSheetOpen && (
        <LazyBoundary label="正在加载菜单…" variant="sheet" sheetSize="menu">
          <ThreadMenuSheet
            latestSummary={latestSummary}
            branchOrWorkspace={branchOrWorkspace}
            isRenaming={isRenaming}
            setIsRenaming={setIsRenaming}
            renameDraft={renameDraft}
            setRenameDraft={setRenameDraft}
            isUpdatingThread={isUpdatingThread}
            handleRename={handleRename}
            composerOptions={composerOptions}
            composerOptionsSupported={composerOptionsSupported}
            updateComposerOptions={updateComposerOptions}
            swarmSupported={swarmSupported}
            reviewSupported={reviewSupported}
            gitSupported={gitSupported}
            filesSupported={filesSupported}
            scriptsSupported={scriptsSupported}
            initialPage={menuInitialPage}
            onNavigateRoute={handleMenuNavigate}
            onCloseMenu={handleCloseMenu}
            menuButtonRef={menuButtonRef}
            renderExternalPage={renderExternalPage}
            swarmState={swarmState}
            swarmAvailable={swarmAvailable}
            swarmEnabled={swarmEnabled}
            isTogglingSwarm={isTogglingSwarm}
            swarmInlineError={swarmInlineError}
            handleToggleSwarm={handleToggleSwarm}
            handleTogglePin={handleTogglePin}
            handleToggleDormant={handleToggleDormant}
            handleArchive={handleArchive}
            handleUnarchive={handleUnarchive}
            saveTaskSupported={saveTaskSupported}
            forwardSupported={forwardSupported}
            shareActionsBlocked={shareActionsBlocked}
            handleOpenSaveTask={handleOpenSaveTask}
            unarchiveError={unarchiveError}
            setForwardError={setForwardError}
          />
        </LazyBoundary>
      )}

      {/* 从回复分支确认弹窗 */}
      {branchConfirmMessageID && (
        <LazyBoundary label="正在加载确认弹窗…" variant="sheet" sheetSize="menu">
          <BranchConfirmDialog
            isBranching={isBranching}
            branchError={branchError}
            setBranchConfirmMessageID={setBranchConfirmMessageID}
            setBranchError={setBranchError}
            handleConfirmBranch={handleConfirmBranch}
          />
        </LazyBoundary>
      )}

      {/* 转发、存为任务与子代理/审阅/Git/脚本/文件页都由菜单页面栈的二级页承载（见 renderExternalPage） */}

      {/* 撤回本轮确认弹窗 */}
      <ConfirmDialog
        isOpen={recallConfirmOpen}
        title="撤回本轮"
        message="将收回本轮尚未产生回复的提示词，放回输入框重新编辑。"
        confirmLabel="撤回"
        confirmVariant="danger"
        isLoading={isRecalling}
        onConfirm={() => void handleRecallTurn()}
        onCancel={() => {
          if (!isRecalling) setRecallConfirmOpen(false);
        }}
      />

      {/* 辅助页停止确认弹窗 */}
      <ConfirmDialog
        isOpen={helperStopConfirmOpen}
        title="停止子代理"
        message={`确定要停止“${swarmState?.bee?.name || latestSummary.title || '当前子代理'}”的执行吗？已完成的工作将保留。`}
        confirmLabel="停止"
        confirmVariant="danger"
        isLoading={helperStopAction?.state === 'submitting'}
        onConfirm={() => void handleConfirmHelperStop()}
        onCancel={() => {
          if (helperStopAction?.state !== 'submitting') {
            setHelperStopConfirmOpen(false);
          }
        }}
      />

      {/* 撤销此轮改动：先预览后确认（对应桌面 revert_dialog.go） */}
      {revertTarget && (
        <RevertTurnSheet
          isOpen
          threadID={thread.id}
          turnID={revertTarget.turnID}
          isRunning={latestSummary.isRunning}
          onClose={() => setRevertTarget(null)}
          onReverted={handleTurnReverted}
        />
      )}

      {/* 编辑用户消息后重发：提交后分支/新建会话再发送 */}
      {editTarget && (
        <EditMessageSheet
          isOpen
          initialText={editTarget.text}
          onSubmit={handleSubmitEditedMessage}
          onClose={() => setEditTarget(null)}
        />
      )}
    </div>
  );
};
