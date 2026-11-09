// 移动首页会话列表：按宿主 section/orderKey 投影，交互动作同时提供可见按钮。

import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { createPortal, flushSync } from 'react-dom';
import type { RemoteThreadSummary } from '../types/contract';
import { useRemote } from '../context/RemoteContext';
import { useRemoteCall } from '../context/useRemoteCall';
import { searchMessages, type RemoteMessageSearchHit } from '../context/remoteMessageSearchApi';
import { isThreadBusy } from '../context/remoteGuards';
import { dampOffset, SWIPE_OPEN_THRESHOLD } from '../hooks/threadSwipe';
import { haptic, shouldHapticOnCross } from '../utils/haptic';
import '../styles/mobile-press.css';
import { useSheetDrag } from '../hooks/useSheetDrag';
import {
  ArchiveBoxIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ClockIcon,
  EllipsisHorizontalIcon,
  ExclamationTriangleIcon,
  MessageIcon,
  PinIcon,
  PlusIcon,
  SearchIcon,
  XMarkIcon
} from './Icons';
import {
  BulkThreadsSheet,
  useBulkThreadsApi,
  type BulkThreadsAction
} from './BulkThreadsSheet';
import { useProjectAutomations } from '../context/remoteAutomationApi';
import { useAttentionInbox } from '../context/remoteAttentionApi';
import {
  stewardActionErrorText,
  useRemoteStewardApi
} from '../context/remoteStewardApi';
import {
  threadRowStatus,
  threadRowStatusAriaLabel,
  type ThreadRowStatus
} from '../utils/threadRowStatus';
import { stewardAvatarText, stewardLabel } from '../utils/stewardIdentity';
import { useRemoteSettingsApi } from '../context/remoteSettingsApi';
import { LazyBoundary } from './LazyFallback';
import { visibleFilterChips, type ThreadFilter } from '../utils/homeFilterChips';
import { readHomeQuery, shouldCollapseOnBlur, writeHomeQuery } from '../utils/homeSearch';

// 自动化弹层只在用户点开时渲染。
// BulkThreadsSheet 不懒加载：同一模块里的 useBulkThreadsApi 在列表主体里同步使用，
// 保留那份导入就会把整个模块拉回主包。
const AutomationSheet = React.lazy(() =>
  import('./AutomationSheet').then((module) => ({ default: module.AutomationSheet }))
);

// 筛选 chip 的中文名，空态标题与 chip 文案保持一致。
const FILTER_LABELS: Record<ThreadFilter, string> = {
  all: '全部',
  attention: '等你',
  running: '运行中',
  failed: '失败',
  dormant: '已休眠',
  archived: '已归档'
};

// 批量整理完成后提示条的停留时间。
const BULK_NOTICE_TIMEOUT_MS = 8000;

// 活跃会话默认先显示 5 条；列表滚到接近底部时自动再追加 10 条，直到全部展开（不再提供「收起」）。
const ACTIVE_PAGE_INIT = 5;
const ACTIVE_PAGE_STEP = 10;
// 已休眠会话的本地分页步长，与服务端分页互不干扰。
const DORMANT_PAGE_STEP = 25;

export function threadRowSurfaceClass(isSelected?: boolean): string {
  return isSelected
    ? 'bg-screen bg-[linear-gradient(var(--mc-overlay-selected),var(--mc-overlay-selected))]'
    : 'bg-screen';
}

interface ThreadListProps {
  selectedThreadID?: string | null;
  headerActionsSlot?: HTMLElement | null;
  headerLeadingSlot?: HTMLElement | null;
  projectID: string | null;
  onSelectThread: (thread: RemoteThreadSummary) => void;
  onCreateDraft: (projectID: string) => void;
}

interface ThreadRowProps {
  isSelected?: boolean;
  thread: RemoteThreadSummary;
  projectName?: string;
  projectScoped: boolean;
  onOpen: (thread: RemoteThreadSummary) => void;
  onMenu: (thread: RemoteThreadSummary) => void;
  onDormant: (thread: RemoteThreadSummary) => void;
  showTrailingDivider?: boolean;
  isOpen: boolean;
  onSwipeOpen: (threadID: string) => void;
  onClose: () => void;
}

interface DormantThreadRowProps {
  isSelected?: boolean;
  thread: RemoteThreadSummary;
  projectName?: string;
  onOpen: (thread: RemoteThreadSummary) => void;
  onMenu: (thread: RemoteThreadSummary) => void;
  onDormant: (thread: RemoteThreadSummary) => void;
  showTrailingDivider?: boolean;
  isOpen: boolean;
  onSwipeOpen: (threadID: string) => void;
  onClose: () => void;
}

function getThreadStatus(thread: RemoteThreadSummary): { label: string; className: string } | null {
  if (thread.attention === 'approval') {
    return { label: '待授权', className: 'text-warning font-medium' };
  }
  if (thread.attention === 'input') {
    return { label: '待回答', className: 'text-[#4f46e5] dark:text-[#a5b4fc] font-medium' };
  }
  if (thread.attention === 'failed') {
    return { label: '失败', className: 'text-danger font-medium' };
  }
  if (isThreadBusy(thread)) {
    return { label: '运行中', className: 'text-[#0284c7] dark:text-[#38bdf8] font-medium' };
  }
  return null;
}

// 行尾单一状态位：外观照桌面端 renderSidebarStatusSlot（internal/ui/sidebar.go:1024）——
// 待审批=警示色圆点、等待后台任务=主色时钟加「后台 N」、未读=主色圆点、置顶=图钉、
// 最近活动=相对时间文本，同一时刻只出一个。
// 读屏文字由行的 aria-label 兜住（threadRowStatusAriaLabel 列出全部状态），这里对读屏隐藏。
function ThreadRowStatusSlot({ status }: { status: ThreadRowStatus }) {
  if (status.kind === 'pending') {
    return <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-warning" aria-hidden="true" />;
  }
  if (status.kind === 'waiting') {
    return (
      <span
        className="inline-flex shrink-0 items-center gap-0.5 text-label tabular-nums text-[var(--primary)]"
        aria-hidden="true"
      >
        <ClockIcon className="h-3 w-3 shrink-0" />
        {status.count === 1 ? '后台' : `后台 ${status.count}`}
      </span>
    );
  }
  if (status.kind === 'unread') {
    return <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-primary" aria-hidden="true" />;
  }
  if (status.kind === 'pinned') {
    return <PinIcon className="h-3 w-3 shrink-0 text-muted" aria-hidden="true" />;
  }
  if (status.kind === 'activity') {
    return <span className="shrink-0 text-label text-muted tabular-nums">{status.text}</span>;
  }
  return null;
}

function sameDay(first: Date, second: Date): boolean {
  return first.getFullYear() === second.getFullYear() &&
    first.getMonth() === second.getMonth() &&
    first.getDate() === second.getDate();
}

function activeDateLabel(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '最近';
  const now = new Date();
  if (sameDay(date, now)) return '今天';
  const yesterday = new Date(now);
  yesterday.setDate(now.getDate() - 1);
  if (sameDay(date, yesterday)) return '昨天';
  return `${date.getMonth() + 1} 月 ${date.getDate()} 日`;
}

// threadActivityTime 返回会话行排序用的“用户活跃时间”时间戳：用户最后一次主动发消息的时间
// （lastUserMessageAt），缺失或无法解析时回退到 updatedAt。助手回复、改名等只动 updatedAt 的
// 操作不会把会话顶到前面。
function threadActivityTime(thread: RemoteThreadSummary): number {
  const raw = thread.lastUserMessageAt ?? thread.updatedAt;
  const at = raw ? Date.parse(raw) : Number.NaN;
  return Number.isNaN(at) ? 0 : at;
}

// sortByActivityDesc 按用户活跃时间倒序排列；时间相同保持传入顺序（稳定排序）。
function sortByActivityDesc(threads: RemoteThreadSummary[]): RemoteThreadSummary[] {
  return threads.sort((first, second) => threadActivityTime(second) - threadActivityTime(first));
}

function isAttention(thread: RemoteThreadSummary): boolean {
  return thread.attention === 'approval' || thread.attention === 'input';
}

function matchesFilter(thread: RemoteThreadSummary, filter: ThreadFilter): boolean {
  if (filter === 'attention') return isAttention(thread);
  if (filter === 'running') return isThreadBusy(thread);
  if (filter === 'failed') return thread.attention === 'failed';
  if (filter === 'dormant') return thread.isDormant;
  return true;
}

// 行组件加 React.memo：列表刷新时未变化的会话在 mergeThreadSummary 里保持原引用，行本身不必重渲染。
const ArchivedThreadRow = React.memo(function ArchivedThreadRow({
  isSelected,
  thread,
  projectName,
  onOpen,
  onMenu,
  showTrailingDivider = true
}: {
  isSelected?: boolean;
  thread: RemoteThreadSummary;
  projectName?: string;
  onOpen: (thread: RemoteThreadSummary) => void;
  onMenu: (thread: RemoteThreadSummary) => void;
  showTrailingDivider?: boolean;
}) {
  const rowStatus = threadRowStatus(thread);
  const rowStatusLabel = threadRowStatusAriaLabel(thread);
  return (
    <div className="thread-row-wrap">
      <div className={`relative z-10 flex min-h-[52px] items-center gap-2 ${threadRowSurfaceClass(isSelected)} gutter-x`}>
        <button
          type="button"
          aria-current={isSelected ? 'true' : undefined}
          aria-label={`打开已归档会话 ${thread.title || '未命名会话'}${rowStatusLabel ? `，${rowStatusLabel}` : ''}`}
          className="flex min-w-0 flex-1 items-center gap-2 py-2 text-left"
          onClick={() => onOpen(thread)}
        >
          {projectName && (
            <span className="truncate text-footnote text-muted opacity-40 shrink-0 max-w-[100px]">{projectName}</span>
          )}
          <span className="truncate text-footnote text-muted font-normal flex-1">{thread.title || '未命名会话'}</span>
          <ThreadRowStatusSlot status={rowStatus} />
        </button>
        <button type="button" className="icon-button" onClick={() => onMenu(thread)} aria-label="更多操作" title="更多">
          <EllipsisHorizontalIcon className="h-4 w-4" />
        </button>
      </div>
      {showTrailingDivider && <div className="gutter-ml h-px bg-border-subtle" />}
    </div>
  );
});

function ThreadSectionHeading({ title }: { title: string }) {
  return (
    <div className="flex items-center gap-2.5 gutter-x pt-4 pb-1 text-label font-medium text-muted">
      <span className="shrink-0">{title}</span>
      <span className="h-px flex-1 bg-border-subtle" aria-hidden="true" />
    </div>
  );
}

// 跳转目标可能还没加载到列表里（搜索结果、自动化运行记录等，甚至属于其它项目）：
// App 只用 id 路由，这里补一份最小摘要占位，避免为了跳转先去拉全量列表。
function threadSummaryStub(
  threadID: string,
  projectID: string,
  title: string,
  updatedAt: string
): RemoteThreadSummary {
  return {
    id: threadID,
    projectID,
    title,
    provider: '',
    model: null,
    effort: null,
    isRunning: false,
    isArchived: false,
    isPinned: false,
    isDormant: false,
    hasUnread: false,
    attention: '',
    lastActivityText: null,
    section: '',
    orderKey: '',
    updatedAt,
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
    swarmBeesEffort: null,
    isProvisional: true
  };
}

function messageHitThreadStub(hit: RemoteMessageSearchHit, projectID: string): RemoteThreadSummary {
  return threadSummaryStub(hit.threadID, projectID, hit.threadTitle, hit.createdAt);
}

// 总管固定行：放在当前项目区最上面，点了打开总管主会话。
// 行上没有归档 / 置顶等会话操作（桌面端的总管行同样只是一行入口），徽标口径同 internal/ui/sidebar_steward.go：
// 显示待处理收件箱里未处理的事项数。只有在选中项目且宿主声明 steward.open 时才会挂载，不会多出轮询。
const StewardFixedRow = React.memo(function StewardFixedRow({
  projectID,
  label,
  avatar,
  opening,
  running,
  onOpen
}: {
  projectID: string;
  label: string;
  avatar: string;
  /** 正在打开总管会话：按钮禁用并转圈。 */
  opening: boolean;
  /** 总管会话正在回复：只转圈，仍允许点开。 */
  running: boolean;
  onOpen: () => void;
}) {
  const busy = opening || running;
  const openLabel = opening
    ? '正在打开总管会话'
    : running
      ? '打开总管会话（正在回复）'
      : '打开总管会话';
  const { unresolvedCount } = useAttentionInbox(projectID);
  return (
    <section className="gutter-x pt-2 pb-1" aria-label="总管">
      <div className="flex min-h-[52px] w-full min-w-0 items-center gap-1">
        <button
          type="button"
          onClick={onOpen}
          disabled={opening}
          aria-label={openLabel}
          className="flex min-h-[44px] min-w-0 flex-1 items-center gap-2 rounded-lg py-1.5 text-left transition-colors hover:bg-row-hover disabled:opacity-50"
        >
          <span
            className="grid h-8 w-8 shrink-0 place-items-center rounded-full bg-primary text-micro font-bold text-white"
            aria-hidden="true"
          >
            {busy ? <span className="loading-spinner loading-spinner--small" /> : avatar}
          </span>
          <span className="min-w-0 flex-1 truncate text-footnote font-medium text-foreground">{label}</span>
          {unresolvedCount > 0 && (
            <span
              className="shrink-0 rounded-full bg-danger px-1.5 text-mc-caption font-semibold leading-[18px] text-white"
              aria-label={`${unresolvedCount} 条待处理`}
            >
              {unresolvedCount > 99 ? '99+' : unresolvedCount}
            </span>
          )}
        </button>
      </div>
    </section>
  );
});

// 消息正文片段里把命中的关键词标出来（不区分大小写，逐段拼接不重排原文）。
function MessageHitSnippet({ text, needle }: { text: string; needle: string }) {
  const target = needle.toLowerCase();
  const lower = text.toLowerCase();
  if (!target) return <>{text}</>;
  const parts: React.ReactNode[] = [];
  let cursor = 0;
  let index = lower.indexOf(target);
  while (index >= 0) {
    if (index > cursor) parts.push(text.slice(cursor, index));
    parts.push(
      <mark key={`m-${index}`} className="rounded bg-[var(--user-bubble)] px-0.5 text-[var(--foreground)]">
        {text.slice(index, index + target.length)}
      </mark>
    );
    cursor = index + target.length;
    index = lower.indexOf(target, cursor);
  }
  if (cursor < text.length) parts.push(text.slice(cursor));
  return <>{parts}</>;
}

const ThreadRow = React.memo(function ThreadRow({
  isSelected,
  thread,
  projectName,
  onOpen,
  onMenu,
  onDormant,
  showTrailingDivider = true,
  isOpen,
  onSwipeOpen,
  onClose
}: ThreadRowProps) {
  const [offset, setOffset] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  // offset 是渲染状态，pointermove 需要同步的上一帧值来判断是否刚越过吸附阈值。
  const offsetRef = useRef(0);
  const pointerStartRef = useRef<{ x: number; y: number } | null>(null);
  const longPressTimerRef = useRef<number | null>(null);
  const longPressTriggeredRef = useRef(false);

  const clearLongPress = () => {
    if (longPressTimerRef.current !== null) {
      window.clearTimeout(longPressTimerRef.current);
      longPressTimerRef.current = null;
    }
  };

  useEffect(() => clearLongPress, []);

  useEffect(() => {
    offsetRef.current = offset;
  }, [offset]);

  useEffect(() => {
    if (!isOpen) {
      setOffset(0);
    } else {
      setOffset(-84);
    }
  }, [isOpen]);

  const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    pointerStartRef.current = { x: event.clientX, y: event.clientY };
    longPressTriggeredRef.current = false;
    setIsDragging(false);
    clearLongPress();
    longPressTimerRef.current = window.setTimeout(() => {
      longPressTriggeredRef.current = true;
      onMenu(thread);
    }, 520);
  };

  const handlePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const start = pointerStartRef.current;
    if (!start) return;
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (Math.abs(dy) > 12 && Math.abs(dy) > Math.abs(dx)) {
      clearLongPress();
      return;
    }
    if (Math.abs(dx) > 6) {
      clearLongPress();
      setIsDragging(true);
      const baseOffset = isOpen ? -84 : 0;
      const nextOffset = dampOffset(baseOffset + dx);
      // 越过吸附阈值的那一下打一次触感；退回阈值内再越过才算新的一次。
      if (shouldHapticOnCross(offsetRef.current, nextOffset, -SWIPE_OPEN_THRESHOLD)) {
        haptic('light');
      }
      offsetRef.current = nextOffset;
      setOffset(nextOffset);
    }
  };

  const handlePointerUp = (event: React.PointerEvent<HTMLDivElement>) => {
    clearLongPress();
    const start = pointerStartRef.current;
    pointerStartRef.current = null;
    setIsDragging(false);

    if (!start) return;
    const dx = event.clientX - start.x;
    const wasDrag = Math.abs(dx) > 6;

    if (isOpen) {
      if (dx > 20) {
        setOffset(0);
        onClose();
      } else if (!wasDrag && !longPressTriggeredRef.current) {
        setOffset(0);
        onClose();
      } else {
        setOffset(-84);
      }
    } else {
      if (offset <= -SWIPE_OPEN_THRESHOLD) {
        setOffset(-84);
        onSwipeOpen(thread.id);
      } else {
        setOffset(0);
        onClose();
        if (!wasDrag && !longPressTriggeredRef.current) {
          onOpen(thread);
        }
      }
    }
  };

  const handlePointerCancel = () => {
    clearLongPress();
    pointerStartRef.current = null;
    setIsDragging(false);
    setOffset(isOpen ? -84 : 0);
  };

  const status = getThreadStatus(thread);
  const rowStatus = threadRowStatus(thread);
  const rowStatusLabel = threadRowStatusAriaLabel(thread);
  const branch = (thread as { branch?: string }).branch;
  const modelOrProvider = thread.model || thread.provider;
  const hasLine3 = Boolean(branch || modelOrProvider || thread.taskID || thread.goalID);

  return (
    <div className="thread-row-wrap">
      <button
        type="button"
        aria-label={thread.isDormant ? '取消休眠' : '休眠'}
        className={`absolute top-0 right-0 bottom-px z-0 flex w-[84px] items-center justify-center gap-1.5 text-xs font-semibold active:opacity-90 transition-opacity ${
          thread.isDormant
            ? 'bg-amber-500/20 text-amber-600 dark:text-amber-400 border-l border-amber-500/30'
            : 'bg-primary text-white'
        }`}
        onClick={(event) => {
          event.stopPropagation();
          onClose();
          onDormant(thread);
        }}
      >
        <CheckCircleIcon className="h-4 w-4" />
        <span>{thread.isDormant ? '取消休眠' : '休眠'}</span>
      </button>
      <div
        role="button"
        tabIndex={0}
        aria-label={`打开会话 ${thread.title || '未命名会话'}${rowStatusLabel ? `，${rowStatusLabel}` : ''}`}
        aria-current={isSelected ? 'true' : undefined}
        className={`relative z-10 min-h-[76px] ${threadRowSurfaceClass(isSelected)} gutter-x py-2.5 transition-colors hover:bg-row-hover active:bg-row-hover touch-pan-y${isDragging ? ' thread-row--dragging' : ''}`}
        style={{
          transform: `translateX(${offset}px)`,
          transition: isDragging ? 'none' : 'transform 280ms cubic-bezier(.22,1,.36,1)'
        }}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerCancel}
        onContextMenu={(event) => {
          event.preventDefault();
          onMenu(thread);
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            if (isOpen) {
              setOffset(0);
              onClose();
            } else {
              onOpen(thread);
            }
          }
        }}
      >
        {/* Line 1: 左侧项目名 + 行首状态（运行中/待授权/待回答/失败，对应桌面端的行首图标位）；
            行尾只保留 threadRowStatus 选出的一个状态位：待审批 > 未读 > 置顶 > 最近活动。 */}
        <div className="flex items-center justify-between gap-2 min-w-0">
          <div className="flex items-center gap-1.5 min-w-0 flex-1">
            <span className="truncate text-footnote text-muted font-normal">
              {projectName || '会话'}
            </span>
            {status && (
              <span className={`shrink-0 text-label tabular-nums ${status.className}`}>
                {status.label}
              </span>
            )}
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            <ThreadRowStatusSlot status={rowStatus} />
          </div>
        </div>

        {/* Line 2: title (16/23, medium, max 2 lines) */}
        <h3 className="mt-1 text-body font-medium text-foreground line-clamp-2 text-left">
          {thread.title || '未命名会话'}
        </h3>

        {/* Line 3: model/provider and task/goal badges, branch in monospace */}
        {hasLine3 && (
          <div className="mt-1 flex flex-wrap items-center gap-2 text-label text-muted">
            {branch && (
              <span className="font-mono text-label text-muted">{branch}</span>
            )}
            {branch && modelOrProvider && <span aria-hidden="true">·</span>}
            {modelOrProvider && (
              <span>{modelOrProvider}</span>
            )}
            {thread.taskID && (
              <span className="rounded bg-subtle-strong px-1.5 py-0.5 text-micro font-medium text-muted">
                任务
              </span>
            )}
            {thread.goalID && (
              <span className="rounded bg-subtle-strong px-1.5 py-0.5 text-micro font-medium text-muted">
                目标
              </span>
            )}
          </div>
        )}
      </div>
      {showTrailingDivider && (
        <div className="gutter-ml h-px bg-border-subtle" />
      )}
    </div>
  );
});

const DormantThreadRow = React.memo(function DormantThreadRow({
  isSelected,
  thread,
  projectName,
  onOpen,
  onMenu,
  onDormant,
  showTrailingDivider = true,
  isOpen,
  onSwipeOpen,
  onClose
}: DormantThreadRowProps) {
  const [offset, setOffset] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  // offset 是渲染状态，pointermove 需要同步的上一帧值来判断是否刚越过吸附阈值。
  const offsetRef = useRef(0);
  const pointerStartRef = useRef<{ x: number; y: number } | null>(null);
  const longPressTimerRef = useRef<number | null>(null);
  const longPressTriggeredRef = useRef(false);

  const clearLongPress = () => {
    if (longPressTimerRef.current !== null) {
      window.clearTimeout(longPressTimerRef.current);
      longPressTimerRef.current = null;
    }
  };

  useEffect(() => clearLongPress, []);

  useEffect(() => {
    offsetRef.current = offset;
  }, [offset]);

  useEffect(() => {
    if (!isOpen) {
      setOffset(0);
    } else {
      setOffset(-84);
    }
  }, [isOpen]);

  const handlePointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    if (event.pointerType === 'mouse' && event.button !== 0) return;
    pointerStartRef.current = { x: event.clientX, y: event.clientY };
    longPressTriggeredRef.current = false;
    setIsDragging(false);
    clearLongPress();
    longPressTimerRef.current = window.setTimeout(() => {
      longPressTriggeredRef.current = true;
      onMenu(thread);
    }, 520);
  };

  const handlePointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    const start = pointerStartRef.current;
    if (!start) return;
    const dx = event.clientX - start.x;
    const dy = event.clientY - start.y;
    if (Math.abs(dy) > 12 && Math.abs(dy) > Math.abs(dx)) {
      clearLongPress();
      return;
    }
    if (Math.abs(dx) > 6) {
      clearLongPress();
      setIsDragging(true);
      const baseOffset = isOpen ? -84 : 0;
      const nextOffset = dampOffset(baseOffset + dx);
      // 越过吸附阈值的那一下打一次触感；退回阈值内再越过才算新的一次。
      if (shouldHapticOnCross(offsetRef.current, nextOffset, -SWIPE_OPEN_THRESHOLD)) {
        haptic('light');
      }
      offsetRef.current = nextOffset;
      setOffset(nextOffset);
    }
  };

  const handlePointerUp = (event: React.PointerEvent<HTMLDivElement>) => {
    clearLongPress();
    const start = pointerStartRef.current;
    pointerStartRef.current = null;
    setIsDragging(false);

    if (!start) return;
    const dx = event.clientX - start.x;
    const wasDrag = Math.abs(dx) > 6;

    if (isOpen) {
      if (dx > 20) {
        setOffset(0);
        onClose();
      } else if (!wasDrag && !longPressTriggeredRef.current) {
        setOffset(0);
        onClose();
      } else {
        setOffset(-84);
      }
    } else {
      if (offset <= -SWIPE_OPEN_THRESHOLD) {
        setOffset(-84);
        onSwipeOpen(thread.id);
      } else {
        setOffset(0);
        onClose();
        if (!wasDrag && !longPressTriggeredRef.current) {
          onOpen(thread);
        }
      }
    }
  };

  const handlePointerCancel = () => {
    clearLongPress();
    pointerStartRef.current = null;
    setIsDragging(false);
    setOffset(isOpen ? -84 : 0);
  };

  const rowStatus = threadRowStatus(thread);
  const rowStatusLabel = threadRowStatusAriaLabel(thread);

  return (
    <div className="thread-row-wrap">
      <button
        type="button"
        aria-label="取消休眠"
        className="absolute top-0 right-0 bottom-px z-0 flex w-[84px] items-center justify-center gap-1.5 bg-amber-500/20 text-amber-600 dark:text-amber-400 border-l border-amber-500/30 text-xs font-semibold active:opacity-90 transition-opacity"
        onClick={(event) => {
          event.stopPropagation();
          onClose();
          onDormant(thread);
        }}
      >
        <CheckCircleIcon className="h-4 w-4" />
        <span>取消休眠</span>
      </button>
      <div
        role="button"
        tabIndex={0}
        aria-label={`打开已休眠会话 ${thread.title || '未命名会话'}${rowStatusLabel ? `，${rowStatusLabel}` : ''}`}
        aria-current={isSelected ? 'true' : undefined}
        className={`relative z-10 flex h-[44px] min-h-[44px] items-center justify-between gap-2 ${threadRowSurfaceClass(isSelected)} gutter-x transition-colors hover:bg-row-hover active:bg-row-hover touch-pan-y${isDragging ? ' thread-row--dragging' : ''}`}
        style={{
          transform: `translateX(${offset}px)`,
          transition: isDragging ? 'none' : 'transform 280ms cubic-bezier(.22,1,.36,1)'
        }}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={handlePointerUp}
        onPointerCancel={handlePointerCancel}
        onContextMenu={(event) => {
          event.preventDefault();
          onMenu(thread);
        }}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            if (isOpen) {
              setOffset(0);
              onClose();
            } else {
              onOpen(thread);
            }
          }
        }}
      >
        <div className="flex min-w-0 flex-1 items-center gap-2">
          {projectName && (
            <span className="truncate text-footnote text-muted opacity-40 shrink-0 max-w-[100px]">
              {projectName}
            </span>
          )}
          <span className="truncate text-footnote text-muted font-normal flex-1">
            {thread.title || '未命名会话'}
          </span>
        </div>
        <span className="ml-2 flex shrink-0 items-center gap-1.5">
          <ThreadRowStatusSlot status={rowStatus} />
        </span>
      </div>
      {showTrailingDivider && (
        <div className="gutter-ml h-px bg-border-subtle" />
      )}
    </div>
  );
});

function FilterChip({
  label,
  count,
  selected,
  onClick,
  tone = ''
}: {
  label: string;
  count?: number;
  selected: boolean;
  onClick: () => void;
  tone?: string;
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      className={`filter-chip ${selected ? 'filter-chip--active' : ''} ${tone}`}
    >
      <span>{label}</span>
      {count !== undefined && <span className="filter-chip__count">{count}</span>}
    </button>
  );
}

function HomeMoreMenu({
  automationAvailable,
  automationUnreadTotal,
  canBulkOrganize,
  filter,
  onAutomation,
  onBulkOrganize,
  onFilterChange
}: {
  automationAvailable: boolean;
  automationUnreadTotal: number;
  canBulkOrganize: boolean;
  filter: ThreadFilter;
  onAutomation: () => void;
  onBulkOrganize: () => void;
  onFilterChange: (filter: ThreadFilter) => void;
}) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: PointerEvent) => {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [open]);

  return (
    <div ref={containerRef} className="relative">
      <button type="button" className="icon-button relative" aria-label="更多" title="更多" aria-haspopup="menu" aria-expanded={open} onClick={() => setOpen((value) => !value)}>
        <EllipsisHorizontalIcon className="h-4 w-4" />
        {automationUnreadTotal > 0 && <span className="absolute right-0 top-0 h-2 w-2 rounded-full bg-[var(--primary)]" aria-hidden="true" />}
      </button>
      {open && (
        <div className="scope-menu scope-menu--end animate-fadeIn" role="menu">
          {automationAvailable && (
            <button type="button" className="scope-menu__item" role="menuitem" onClick={() => { setOpen(false); onAutomation(); }}>
              <span className="scope-menu__title">自动化</span>
              {automationUnreadTotal > 0 && (
                <span className="ml-1 inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-[var(--primary)] px-1 text-mc-caption font-bold leading-none text-white" aria-label={`${automationUnreadTotal} 条未读运行记录`}>
                  {automationUnreadTotal > 99 ? '99+' : automationUnreadTotal}
                </span>
              )}
            </button>
          )}
          {canBulkOrganize && (
            <button type="button" className="scope-menu__item" role="menuitem" onClick={() => { setOpen(false); onBulkOrganize(); }}>
              <span className="scope-menu__title">整理</span>
            </button>
          )}
          <button type="button" className="scope-menu__item" role="menuitemradio" aria-checked={filter === 'archived'} onClick={() => { setOpen(false); onFilterChange(filter === 'archived' ? 'all' : 'archived'); }}>
            <span className="scope-menu__title">已归档</span>
            {filter === 'archived' && <span className="scope-menu__check">✓</span>}
          </button>
        </div>
      )}
    </div>
  );
}

// 底部哨兵：滚到距列表底部约 200px 时自动加载下一页。
// 内容不够长、哨兵仍停在视口内时，revision 变化会重建观察器继续触发下一页；
// 若同一 revision 下反复触发（服务端没有返回新数据），最多重试 SENTINEL_MAX_RETRY 次后停手，避免空转。
const SENTINEL_ROOT_MARGIN = '0px 0px 200px 0px';
const SENTINEL_MAX_RETRY = 3;

interface LoadMoreSentinelProps {
  rootRef: React.RefObject<HTMLDivElement | null>;
  revision: number;
  loading?: boolean;
  onVisible: () => void;
}

const LoadMoreSentinel: React.FC<LoadMoreSentinelProps> = ({
  rootRef,
  revision,
  loading = false,
  onVisible
}) => {
  const sentinelRef = useRef<HTMLDivElement | null>(null);
  const onVisibleRef = useRef(onVisible);
  onVisibleRef.current = onVisible;
  const retryRef = useRef(0);

  // 列表内容变长即视为一次有效加载，重置重试计数。
  useEffect(() => {
    retryRef.current = 0;
  }, [revision]);

  useEffect(() => {
    const element = sentinelRef.current;
    if (!element || loading || typeof IntersectionObserver === 'undefined') return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        if (retryRef.current >= SENTINEL_MAX_RETRY) return;
        retryRef.current += 1;
        onVisibleRef.current();
      },
      { root: rootRef.current ?? null, rootMargin: SENTINEL_ROOT_MARGIN }
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, [loading, revision, rootRef]);

  return (
    <div
      ref={sentinelRef}
      className="gutter-x flex items-center justify-center gap-2 py-3 text-xs text-muted"
      aria-live="polite"
    >
      {loading && (
        <>
          <span className="loading-spinner loading-spinner--small" />
          <span>正在加载…</span>
        </>
      )}
    </div>
  );
};

const HomeSearch: React.FC<{
  query: string;
  onQueryChange: (query: string) => void;
  listScrollRef: React.RefObject<HTMLDivElement>;
  header: Element;
}> = ({ query, onQueryChange, listScrollRef, header }) => {
  const [expanded, setExpanded] = useState(() => query.trim() !== '');
  const [position, setPosition] = useState({ left: 0, right: 0 });
  const buttonRef = useRef<HTMLButtonElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useLayoutEffect(() => {
    if (!expanded) return;
    const updatePosition = () => {
      const button = buttonRef.current;
      if (!button) return;
      setPosition({
        left: button.getBoundingClientRect().left - header.getBoundingClientRect().left,
        right: parseFloat(window.getComputedStyle(header).paddingRight) || 0
      });
    };
    updatePosition();
    window.addEventListener('resize', updatePosition);
    // 顶栏宽度变化（侧栏拖拽、窗口分屏）时 resize 不一定触发，顶栏与锚点父容器的尺寸变化都要重算。
    const observer = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(updatePosition) : null;
    if (observer) {
      observer.observe(header);
      const anchor = buttonRef.current?.parentElement;
      if (anchor) observer.observe(anchor);
    }
    return () => {
      window.removeEventListener('resize', updatePosition);
      observer?.disconnect();
    };
  }, [expanded, header]);

  useEffect(() => {
    if (!expanded) return;
    const container = listScrollRef.current;
    const onScroll = () => {
      if (shouldCollapseOnBlur(query)) {
        setExpanded(false);
        inputRef.current?.blur();
      }
    };
    container?.addEventListener('scroll', onScroll);
    return () => container?.removeEventListener('scroll', onScroll);
  }, [expanded, listScrollRef, query]);

  const clear = () => {
    onQueryChange('');
    setExpanded(false);
    inputRef.current?.blur();
  };

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        className="icon-button"
        aria-label="搜索会话"
        aria-expanded={expanded}
        onClick={() => {
          flushSync(() => setExpanded(true));
          inputRef.current?.focus();
        }}
      >
        <SearchIcon className="h-4 w-4" />
      </button>
      {createPortal(
        <div className="header-search" data-open={expanded} aria-hidden={!expanded} style={position}>
          <SearchIcon className="h-4 w-4 shrink-0 text-muted" />
          <input
            ref={inputRef}
            type="search"
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            onBlur={() => { if (shouldCollapseOnBlur(query)) setExpanded(false); }}
            onKeyDown={(event) => {
              if (event.key === 'Enter') event.currentTarget.blur();
              if (event.key === 'Escape') clear();
            }}
            placeholder="搜索会话"
            aria-label="搜索会话标题"
            enterKeyHint="search"
            autoComplete="off"
            tabIndex={expanded ? 0 : -1}
          />
          <button type="button" className="search-field__clear" aria-label="清除搜索" tabIndex={expanded ? 0 : -1} onMouseDown={(event) => event.preventDefault()} onClick={clear}>
            <XMarkIcon className="h-3.5 w-3.5" />
          </button>
        </div>,
        header
      )}
    </>
  );
};

export const ThreadList: React.FC<ThreadListProps> = ({
  selectedThreadID,
  headerActionsSlot,
  headerLeadingSlot,
  projectID,
  onSelectThread,
  onCreateDraft,
}) => {
  const {
    threads,
    threadsLoading,
    threadsLoadingMore,
    threadsHasMore,
    threadsError,
    loadThreads,
    loadMoreThreads,
    listArchivedThreads,
    resyncThreads,
    projects,
    capabilities,
    connectionState,
    isSyncing,
    updateThread,
    environmentID
  } = useRemote();

  const [query, setQuery] = useState(() => {
    try { return typeof window === 'undefined' ? '' : readHomeQuery(window.sessionStorage); } catch { return ''; }
  });
  useEffect(() => {
    try { writeHomeQuery(window.sessionStorage, query); } catch { /* 存储不可用时忽略。 */ }
  }, [query]);
  const [filter, setFilter] = useState<ThreadFilter>('all');
  const [openRowID, setOpenRowID] = useState<string | null>(null);
  const [dormantExpanded, setDormantExpanded] = useState(false);
  const [dormantLimit, setDormantLimit] = useState(10);
  // 活跃会话折叠：按项目 id（全部项目用 'all'）记住当前展示条数
  const [activeLimits, setActiveLimits] = useState<Record<string, number>>({});
  const [menuThread, setMenuThread] = useState<RemoteThreadSummary | null>(null);
  // 会话操作 sheet 支持拖把手关闭：只在把手起手，内容区滚动不参与。
  const actionSheetRef = useRef<HTMLElement | null>(null);
  const actionSheetHandleRef = useRef<HTMLDivElement | null>(null);
  const closeActionSheet = useCallback(() => setMenuThread(null), []);
  useSheetDrag(actionSheetRef, actionSheetHandleRef, closeActionSheet, menuThread !== null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [actionThreadID, setActionThreadID] = useState<string | null>(null);
  const [archivedThreads, setArchivedThreads] = useState<RemoteThreadSummary[]>([]);
  const [archivedLoading, setArchivedLoading] = useState(false);
  const [archivedError, setArchivedError] = useState<string | null>(null);
  // 归档列表至少成功加载过一次后，chip 计数就常驻显示（切换筛选也保留）。
  const [archivedLoaded, setArchivedLoaded] = useState(false);
  const archivedRequestRef = useRef(0);
  const [createSheetOpen, setCreateSheetOpen] = useState(false);
  const [createProjectID, setCreateProjectID] = useState(projectID ?? '');
  const [bulkOpen, setBulkOpen] = useState(false);
  const [automationOpen, setAutomationOpen] = useState(false);
  const [bulkNotice, setBulkNotice] = useState<{
    message: string;
    undoToken?: string;
    action?: BulkThreadsAction;
  } | null>(null);
  const [bulkUndoing, setBulkUndoing] = useState(false);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  // 消息内容搜索：仅在宿主声明 messages.search 且输入超过 1 个字时启用
  const { call: remoteCall, supportsMethod } = useRemoteCall();
  const messageSearchSupported = supportsMethod('messages.search');
  const messageNeedle = query.trim();
  const [messageHits, setMessageHits] = useState<RemoteMessageSearchHit[]>([]);
  const [messageHitsLoading, setMessageHitsLoading] = useState(false);
  const [messageHitsError, setMessageHitsError] = useState<string | null>(null);
  // 宿主支持时显示总管入口。
  const stewardApi = useRemoteStewardApi();
  // 称呼与头像以设置为准：昵称留空显示「总管」，头像留空取昵称首字（无昵称时「总」）。
  // 宿主没声明 settings.get 时快照为空，自然落到默认称呼。
  const stewardSettings = useRemoteSettingsApi();
  const stewardNameValue = stewardSettings.snapshot?.values['steward.name'];
  const stewardAvatarValue = stewardSettings.snapshot?.values['steward.avatar'];
  const stewardNickname = typeof stewardNameValue === 'string' ? stewardNameValue : '';
  const stewardCustomAvatar = typeof stewardAvatarValue === 'string' ? stewardAvatarValue : '';
  const stewardDisplayLabel = stewardLabel(stewardNickname);
  const stewardDisplayAvatar = stewardAvatarText(stewardNickname, stewardCustomAvatar);
  const [stewardOpenBusy, setStewardOpenBusy] = useState(false);

  const bulkApi = useBulkThreadsApi();
  // 自动化面板入口只在宿主声明 automations.list 且已选项目时出现；未读数直接复用列表 hook。
  const automationSupported = Boolean(projectID && supportsMethod('automations.list'));
  const { unreadTotal: automationUnreadTotal } = useProjectAutomations(
    automationSupported ? projectID : null
  );

  const projectName = projects.find((project) => project.id === projectID)?.name;
  const isConnected = connectionState === 'connected';
  const { supportsOpen } = stewardApi;

  const handleOpen = useCallback((thread: RemoteThreadSummary) => {
    setOpenRowID(null);
    if (thread.hasUnread) {
      void updateThread(thread.id, { read: true }).catch(() => undefined);
    }
    onSelectThread(thread);
  }, [onSelectThread, updateThread]);

  // 总管固定行：打开总管主会话（没有就由宿主新建），再按会话 ID 跳到它。
  const handleOpenSteward = useCallback(async () => {
    if (!projectID || !supportsOpen || stewardOpenBusy) return;
    setStewardOpenBusy(true);
    setActionError(null);
    try {
      const threadID = await stewardApi.openSteward(projectID);
      if (!threadID) throw new Error('宿主没有返回总管会话');
      const known = threads.find((thread) => thread.id === threadID);
      handleOpen(
        known ?? { ...threadSummaryStub(threadID, projectID, stewardDisplayLabel, new Date().toISOString()), hasUnread: true }
      );
    } catch (error) {
      setActionError(stewardActionErrorText(error, '打开总管会话失败'));
    } finally {
      setStewardOpenBusy(false);
    }
  }, [projectID, supportsOpen, stewardOpenBusy, stewardApi, handleOpen, threads, stewardDisplayLabel]);

  const canCreate = Boolean(
    capabilities?.methods.includes('threads.create') &&
    capabilities.methods.includes('threads.actionStatus')
  );
  const canBulkOrganize = Boolean(
    projectID && capabilities?.methods.includes('threads.bulk.preview')
  );

  useEffect(() => {
    setCreateProjectID(projectID ?? '');
    setDormantExpanded(false);
    setDormantLimit(10);
    setOpenRowID(null);
    setBulkOpen(false);
    setAutomationOpen(false);
    setArchivedLoaded(false);
  }, [projectID]);

  // 批量整理结果提示条约 8 秒后自动消失，撤销后重新计时。
  useEffect(() => {
    if (!bulkNotice) return;
    const timer = setTimeout(() => setBulkNotice(null), BULK_NOTICE_TIMEOUT_MS);
    return () => clearTimeout(timer);
  }, [bulkNotice]);

  const handleBulkUndo = useCallback(async () => {
    const token = bulkNotice?.undoToken;
    if (!bulkApi || !token || !projectID || bulkUndoing) return;
    setBulkUndoing(true);
    try {
      await bulkApi.undoBulkThreads({ projectID, token });
      setBulkNotice({ message: '已撤销' });
    } catch (error) {
      setActionError(error instanceof Error ? error.message : '撤销失败');
    } finally {
      setBulkUndoing(false);
    }
  }, [bulkApi, bulkNotice, bulkUndoing, projectID]);

  useEffect(() => {
    setOpenRowID(null);
  }, [filter]);

  // 消息内容搜索：停止输入 300ms 后发起，旧请求的结果直接丢弃
  useEffect(() => {
    if (!messageSearchSupported || messageNeedle.length < 2) {
      setMessageHits([]);
      setMessageHitsLoading(false);
      setMessageHitsError(null);
      return;
    }
    let cancelled = false;
    setMessageHitsLoading(true);
    const timer = window.setTimeout(() => {
      void searchMessages(remoteCall, messageNeedle, 20)
        .then((hits) => {
          if (cancelled) return;
          setMessageHits(hits);
          setMessageHitsError(null);
        })
        .catch((error: unknown) => {
          if (cancelled) return;
          setMessageHits([]);
          setMessageHitsError(error instanceof Error ? error.message : '搜索消息内容失败');
        })
        .finally(() => {
          if (!cancelled) setMessageHitsLoading(false);
        });
    }, 300);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [messageNeedle, messageSearchSupported, remoteCall]);

  // 已归档不进主列表：进入「已归档」或范围变化时单独拉取，每次进入都重新取。
  const loadArchived = useCallback(async () => {
    const requestId = ++archivedRequestRef.current;
    setArchivedLoading(true);
    setArchivedError(null);
    try {
      const list = await listArchivedThreads(projectID ?? undefined);
      if (requestId !== archivedRequestRef.current) return;
      setArchivedThreads(list);
      setArchivedLoaded(true);
    } catch (error) {
      if (requestId !== archivedRequestRef.current) return;
      setArchivedError(error instanceof Error ? error.message : '加载已归档会话失败');
    } finally {
      if (requestId === archivedRequestRef.current) setArchivedLoading(false);
    }
  }, [listArchivedThreads, projectID]);

  const inArchivedView = filter === 'archived' && isConnected;
  useEffect(() => {
    if (!inArchivedView) {
      archivedRequestRef.current++;
      setArchivedLoading(false);
      return;
    }
    setArchivedThreads([]);
    void loadArchived();
    // 只在进入归档视图/范围变化时重取；loadArchived 随 capabilities 变化不应反复触发。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inArchivedView, projectID]);

  useEffect(() => {
    const key = `mcode_remote_scroll:${environmentID}:${projectID ?? 'all'}`;
    const element = scrollRef.current;
    if (!element) return;
    try {
      element.scrollTop = Number(window.sessionStorage.getItem(key) ?? 0);
    } catch {
      // 滚动位置是便利状态，存储失败不影响列表。
    }
    const save = () => {
      try {
        window.sessionStorage.setItem(key, String(element.scrollTop));
      } catch {
        // 忽略
      }
    };
    element.addEventListener('scroll', save, { passive: true });
    return () => element.removeEventListener('scroll', save);
  }, [environmentID, projectID]);

  const scopedThreads = useMemo(
    () =>
      threads.filter(
        (thread) =>
          !thread.isArchived &&
          !Boolean(
            (thread as { isHelper?: boolean }).isHelper ||
            (thread as { parentThreadID?: string | null }).parentThreadID
          ) &&
          (!projectID || thread.projectID === projectID)
      ),
    [projectID, threads]
  );

  // 总管会话正在回复：靠宿主下发的 isSteward 标记认主会话，不再按标题匹配；
  // 运行态取会话摘要里与普通会话行同源的 isRunning。
  const stewardRunning = useMemo(
    () => scopedThreads.some((thread) => thread.isSteward === true && thread.isRunning),
    [scopedThreads]
  );

  const counts = useMemo(() => ({
    attention: scopedThreads.filter(isAttention).length,
    running: scopedThreads.filter(isThreadBusy).length,
    failed: scopedThreads.filter((thread) => thread.attention === 'failed').length,
    dormant: scopedThreads.filter((thread) => thread.isDormant).length
  }), [scopedThreads]);

  const filteredThreads = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    const rows = scopedThreads
      .filter((thread) => !normalizedQuery || thread.title.toLocaleLowerCase().includes(normalizedQuery))
      .filter((thread) => matchesFilter(thread, filter));
    // 全部项目范围下各项目的 orderKey 互不可比（都是各自项目内的序），改按用户最后一条消息时间倒序；
    // 单项目范围保留宿主 orderKey，手动排序仍然生效。
    if (!projectID) return sortByActivityDesc(rows);
    return rows.sort((first, second) => (first.orderKey < second.orderKey ? -1 : first.orderKey > second.orderKey ? 1 : 0));
  }, [filter, projectID, query, scopedThreads]);

  const activeThreads = filteredThreads.filter((thread) => !thread.isDormant);
  const dormantThreads = filteredThreads.filter((thread) => thread.isDormant);
  const visibleDormantThreads = dormantThreads.slice(0, dormantLimit);

  // 活跃会话默认只露前几条；置顶与「等你」的行始终完整显示、不计入折叠。
  // 搜索时结果集本身很小，不折叠。
  const activeLimitKey = projectID ?? 'all';
  const activeLimit = activeLimits[activeLimitKey] ?? ACTIVE_PAGE_INIT;
  const activeWindow = useMemo(() => {
    const pinned = activeThreads.filter((thread) => thread.section === 'pinned');
    const attention = activeThreads.filter((thread) => thread.section === 'attention');
    const rest = activeThreads.filter(
      (thread) => thread.section !== 'pinned' && thread.section !== 'attention'
    );
    if (query.trim() !== '' || rest.length <= activeLimit) {
      return { threads: activeThreads, hidden: 0 };
    }
    return {
      threads: [...pinned, ...attention, ...rest.slice(0, activeLimit)],
      hidden: rest.length - activeLimit
    };
  }, [activeThreads, activeLimit, query]);

  // 活跃会话：哨兵可见时再多显示 10 条，hidden 归零后哨兵不再渲染。
  const loadMoreActive = useCallback(() => {
    setActiveLimits((limits) => ({
      ...limits,
      [activeLimitKey]: activeLimit + ACTIVE_PAGE_STEP
    }));
  }, [activeLimit, activeLimitKey]);

  // 已休眠会话：先展开本地已缓存的下一批，本地到底后再向服务端要下一页。
  const loadMoreDormant = useCallback(() => {
    if (threadsLoadingMore) return;
    if (dormantLimit < dormantThreads.length) {
      setDormantLimit((limit) => limit + DORMANT_PAGE_STEP);
    } else if (threadsHasMore) {
      void loadMoreThreads();
    }
  }, [dormantLimit, dormantThreads.length, loadMoreThreads, threadsHasMore, threadsLoadingMore]);

  const allActive = useMemo(() => {
    const visibleActive = activeWindow.threads;
    if (projectID) {
      return [
        { title: '置顶', rows: visibleActive.filter((thread) => thread.section === 'pinned') },
        { title: '活跃', rows: visibleActive.filter((thread) => thread.section !== 'pinned') }
      ].filter((section) => section.rows.length > 0);
    }

    const sections: Array<{ title: string; rows: RemoteThreadSummary[] }> = [];
    const attention = visibleActive.filter((thread) => thread.section === 'attention');
    const pinned = visibleActive.filter((thread) => thread.section === 'pinned');
    if (attention.length > 0) sections.push({ title: '等你', rows: attention });
    if (pinned.length > 0) sections.push({ title: '置顶', rows: pinned });

    const byDate = new Map<string, RemoteThreadSummary[]>();
    for (const thread of visibleActive.filter(
      (candidate) => candidate.section !== 'attention' && candidate.section !== 'pinned'
    )) {
      const title = activeDateLabel(thread.lastUserMessageAt ?? thread.updatedAt);
      const rows = byDate.get(title) ?? [];
      rows.push(thread);
      byDate.set(title, rows);
    }
    for (const [title, rows] of byDate) sections.push({ title, rows });
    return sections;
  }, [activeWindow, projectID]);

  const archivedVisible = useMemo(() => {
    const normalizedQuery = query.trim().toLocaleLowerCase();
    const rows = archivedThreads.filter(
      (thread) => !normalizedQuery || thread.title.toLocaleLowerCase().includes(normalizedQuery)
    );
    // 与主列表一致：全部项目范围按用户最后一条消息时间倒序。
    return projectID ? rows : sortByActivityDesc(rows);
  }, [archivedThreads, projectID, query]);

  const handleOpenMessageHit = useCallback(
    (hit: RemoteMessageSearchHit) => {
      const known = [...threads, ...archivedThreads].find((item) => item.id === hit.threadID);
      if (known) {
        handleOpen(known);
        return;
      }
      onSelectThread(messageHitThreadStub(hit, projectID ?? ''));
    },
    [archivedThreads, handleOpen, onSelectThread, projectID, threads]
  );

  // 自动化运行记录里的「打开会话」：列表里有就复用 handleOpen，没有就跳占位摘要。
  const handleOpenAutomationThread = useCallback(
    (threadID: string) => {
      const known = [...threads, ...archivedThreads].find((item) => item.id === threadID);
      setAutomationOpen(false);
      if (known) {
        handleOpen(known);
        return;
      }
      onSelectThread(threadSummaryStub(threadID, projectID ?? '', '自动化运行', ''));
    },
    [archivedThreads, handleOpen, onSelectThread, projectID, threads]
  );

  // 传给行的滑开/关闭回调稳定化：行组件是 React.memo，内联箭头会让 memo 失效。
  const handleRowSwipeOpen = useCallback((threadID: string) => {
    setOpenRowID(threadID);
  }, []);

  const handleRowClose = useCallback(() => {
    setOpenRowID(null);
  }, []);

  const handleDormant = useCallback(async (thread: RemoteThreadSummary) => {
    if (!isConnected || isSyncing || actionThreadID) return;
    setOpenRowID(null);
    setActionThreadID(thread.id);
    setActionError(null);
    try {
      await updateThread(thread.id, { dormant: !thread.isDormant });
    } catch (error) {
      setActionError(error instanceof Error ? error.message : '休眠会话失败');
    } finally {
      setActionThreadID(null);
    }
  }, [actionThreadID, isConnected, isSyncing, updateThread]);

  const handleThreadAction = async (
    thread: RemoteThreadSummary,
    action: 'pin' | 'dormant' | 'archive' | 'unarchive'
  ) => {
    if (!isConnected || isSyncing || actionThreadID) return;
    setOpenRowID(null);
    setActionThreadID(thread.id);
    setActionError(null);
    try {
      if (action === 'pin') await updateThread(thread.id, { pinned: !thread.isPinned });
      if (action === 'dormant') await updateThread(thread.id, { dormant: !thread.isDormant });
      if (action === 'archive') await updateThread(thread.id, { archived: true });
      if (action === 'unarchive') {
        await updateThread(thread.id, { archived: false });
        setArchivedThreads((rows) => rows.filter((row) => row.id !== thread.id));
        void resyncThreads();
      }
      setMenuThread(null);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : action === 'unarchive' ? '取消归档失败' : '会话操作失败');
    } finally {
      setActionThreadID(null);
    }
  };

  return (
    <div className="home-list">
      {headerActionsSlot && createPortal(
        <HomeMoreMenu
          automationAvailable={Boolean(automationSupported && projectID)}
          automationUnreadTotal={automationUnreadTotal}
          canBulkOrganize={canBulkOrganize}
          filter={filter}
          onAutomation={() => setAutomationOpen(true)}
          onBulkOrganize={() => setBulkOpen(true)}
          onFilterChange={setFilter}
        />,
        headerActionsSlot
      )}
      {headerLeadingSlot && headerLeadingSlot.closest('.app-header__inner') && createPortal(
        <HomeSearch query={query} onQueryChange={setQuery} listScrollRef={scrollRef} header={headerLeadingSlot.closest('.app-header__inner')!} />,
        headerLeadingSlot
      )}
      {(() => {
          const chips = visibleFilterChips(filter, counts);
          return chips.length > 0 && (
            <div className="home-toolbar"><div className="filter-row" role="toolbar" aria-label="会话筛选">
              {chips.includes('all') && <FilterChip label="全部" count={scopedThreads.length} selected={filter === 'all'} onClick={() => setFilter('all')} />}
              {chips.includes('attention') && <FilterChip label="等你" count={counts.attention} selected={filter === 'attention'} onClick={() => setFilter('attention')} tone="filter-chip--warning" />}
              {chips.includes('running') && <FilterChip label="运行中" count={counts.running} selected={filter === 'running'} onClick={() => setFilter('running')} tone="filter-chip--running" />}
              {chips.includes('failed') && <FilterChip label="失败" count={counts.failed} selected={filter === 'failed'} onClick={() => setFilter('failed')} tone="filter-chip--danger" />}
              {chips.includes('archived') && <FilterChip label="已归档" count={archivedLoaded ? archivedThreads.length : undefined} selected={filter === 'archived'} onClick={() => setFilter('archived')} />}
            </div></div>
          );
        })()}

      {bulkNotice && (
        <div className="inline-alert" role="status" aria-live="polite">
          <span className="min-w-0 select-text break-words">{bulkNotice.message}</span>
          {bulkNotice.undoToken && (
            <button
              type="button"
              className="inline-alert__action"
              onClick={() => void handleBulkUndo()}
              disabled={bulkUndoing}
            >
              {bulkUndoing ? '撤销中…' : '撤销'}
            </button>
          )}
        </div>
      )}

      {threadsError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <span className="flex min-w-0 items-center gap-2"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" />{threadsError}</span>
          <button type="button" className="inline-alert__action" onClick={() => void loadThreads(projectID)}>重试</button>
        </div>
      )}

      {actionError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <span className="min-w-0 break-words">{actionError}</span>
          <button type="button" className="inline-alert__action" onClick={() => setActionError(null)}>关闭</button>
        </div>
      )}

      <div ref={scrollRef} className="home-list__scroll">
        {projectID && supportsOpen && isConnected && filter !== 'archived' && !messageNeedle && (
          <StewardFixedRow
            projectID={projectID}
            label={stewardDisplayLabel}
            avatar={stewardDisplayAvatar}
            opening={stewardOpenBusy}
            running={stewardRunning}
            onOpen={() => void handleOpenSteward()}
          />
        )}
        {messageSearchSupported && messageNeedle.length > 1 && (
          <section className="gutter-x pt-3 pb-1" aria-label="消息内容搜索结果">
            <div className="flex items-center gap-2 pb-1 text-label font-medium text-muted">
              <span className="shrink-0">消息内容</span>
              <span className="shrink-0 text-mc-caption">
                {messageHitsLoading ? '搜索中…' : `${messageHits.length} 条`}
              </span>
              <span className="h-px flex-1 bg-border-subtle" aria-hidden="true" />
            </div>
            {messageHitsError ? (
              <p role="alert" className="px-1 py-2 text-xs text-[var(--danger-foreground)]">
                {messageHitsError}
              </p>
            ) : !messageHitsLoading && messageHits.length === 0 ? (
              <p className="px-1 py-2 text-xs text-muted">没有包含该关键词的消息。</p>
            ) : (
              <ul className="space-y-1">
                {messageHits.map((hit) => (
                  <li key={`${hit.threadID}:${hit.messageID}`}>
                    <button
                      type="button"
                      onClick={() => handleOpenMessageHit(hit)}
                      className="w-full rounded-xl px-2 py-1.5 text-left transition hover:bg-[var(--user-bubble)] active:opacity-75"
                    >
                      <span className="flex items-center gap-1.5 text-mc-caption text-muted">
                        <span className="min-w-0 truncate text-[var(--foreground)]">
                          {hit.threadTitle || '未命名会话'}
                        </span>
                        <span className="shrink-0">{hit.role === 'user' ? '我' : '助手'}</span>
                      </span>
                      <span className="mt-0.5 line-clamp-2 block text-xs text-muted">
                        <MessageHitSnippet text={hit.snippet} needle={messageNeedle} />
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>
        )}
        {filter === 'archived' ? (
          archivedLoading ? (
            <div className="empty-state" role="status" aria-live="polite">
              <span className="loading-spinner" />
              <span>正在加载已归档会话…</span>
            </div>
          ) : archivedError ? (
            <div className="inline-alert inline-alert--danger" role="alert">
              <span className="flex min-w-0 items-center gap-2"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" />{archivedError}</span>
              <button type="button" className="inline-alert__action" onClick={() => void loadArchived()}>重试</button>
            </div>
          ) : archivedVisible.length === 0 ? (
            <div className="empty-state">
              <div className="empty-state__icon"><ArchiveBoxIcon className="h-6 w-6" /></div>
              <h2>{query ? '没有匹配的已归档会话' : '暂无已归档会话'}</h2>
              <p>{query ? '试试其他标题。' : '在会话菜单里归档的会话会出现在这里。'}</p>
            </div>
          ) : (
            <div className="thread-list" aria-live="polite">
              <section className="dormant-section" aria-label="已归档会话">
                {archivedVisible.map((thread) => (
                  <ArchivedThreadRow
                    key={thread.id}
                    thread={thread}
                    isSelected={thread.id === selectedThreadID}
                    projectName={!projectID ? projects.find((project) => project.id === thread.projectID)?.name : undefined}
                    onOpen={handleOpen}
                    onMenu={setMenuThread}
                  />
                ))}
              </section>
            </div>
          )
        ) : threadsLoading && threads.length === 0 ? (
          <div className="empty-state" role="status" aria-live="polite">
            <span className="loading-spinner" />
            <span>正在加载会话…</span>
          </div>
        ) : filteredThreads.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state__icon"><MessageIcon className="h-6 w-6" /></div>
            <h2>{query ? '没有匹配的会话' : filter !== 'all' ? `暂无${FILTER_LABELS[filter]}会话` : projectName ? `${projectName} 暂无会话` : '还没有会话'}</h2>
            <p>{query ? '试试其他标题或筛选条件。' : filter !== 'all' ? '切换到「全部」查看其他会话。' : '从 Mac 开始一次对话，它会实时出现在这里。'}</p>
            {canCreate && projects.length > 0 && filter === 'all' && (
              <button type="button" className="primary-button" onClick={() => setCreateSheetOpen(true)}>
                <PlusIcon className="h-4 w-4" />新建会话
              </button>
            )}
          </div>
        ) : (
          <div className="thread-list" aria-live="polite">
            {filter === 'dormant' ? (
              <section className="dormant-section" aria-label="已休眠会话">
                {visibleDormantThreads.map((thread) => (
                  <DormantThreadRow
                    key={thread.id}
                    thread={thread}
                    isSelected={thread.id === selectedThreadID}
                    projectName={!projectID ? projects.find((project) => project.id === thread.projectID)?.name : undefined}
                    onOpen={handleOpen}
                    onMenu={setMenuThread}
                    onDormant={handleDormant}
                    isOpen={openRowID === thread.id}
                    onSwipeOpen={handleRowSwipeOpen}
                    onClose={handleRowClose}
                  />
                ))}
                {(dormantLimit < dormantThreads.length || threadsHasMore) && (
                  <LoadMoreSentinel
                    rootRef={scrollRef}
                    revision={visibleDormantThreads.length}
                    loading={threadsLoadingMore}
                    onVisible={loadMoreDormant}
                  />
                )}
              </section>
            ) : (
              <>
                {allActive.map((section) => (
                  <section key={section.title} aria-label={section.title}>
                    <ThreadSectionHeading title={section.title} />
                    {section.rows.map((thread) => (
                      <ThreadRow
                        key={thread.id}
                        thread={thread}
                        isSelected={thread.id === selectedThreadID}
                        projectName={!projectID ? projects.find((project) => project.id === thread.projectID)?.name : undefined}
                        projectScoped={Boolean(projectID)}
                        onOpen={handleOpen}
                        onMenu={setMenuThread}
                        onDormant={handleDormant}
                        isOpen={openRowID === thread.id}
                        onSwipeOpen={handleRowSwipeOpen}
                        onClose={handleRowClose}
                      />
                    ))}
                  </section>
                ))}

                {activeWindow.hidden > 0 && (
                  <LoadMoreSentinel
                    rootRef={scrollRef}
                    revision={activeWindow.hidden}
                    onVisible={loadMoreActive}
                  />
                )}

                {(dormantThreads.length > 0 || threadsHasMore) && (
                  <section className="dormant-section" aria-label="已休眠">
                    <div className="gutter-x">
                      <button type="button" className="dormant-toggle" onClick={() => setDormantExpanded((expanded) => !expanded)} aria-expanded={dormantExpanded}>
                        <span className="dormant-toggle__label"><ChevronDownIcon className={`h-3.5 w-3.5 transition-transform ${dormantExpanded ? 'rotate-180' : ''}`} />已休眠</span>
                        <span className="dormant-toggle__count">{dormantThreads.length}{threadsHasMore ? '+' : ''}</span>
                      </button>
                    </div>
                    {dormantExpanded && (
                      <>
                        {visibleDormantThreads.map((thread) => (
                          <DormantThreadRow
                            key={thread.id}
                            thread={thread}
                            isSelected={thread.id === selectedThreadID}
                            projectName={!projectID ? projects.find((project) => project.id === thread.projectID)?.name : undefined}
                            onOpen={handleOpen}
                            onMenu={setMenuThread}
                            onDormant={handleDormant}
                            isOpen={openRowID === thread.id}
                            onSwipeOpen={handleRowSwipeOpen}
                            onClose={handleRowClose}
                          />
                        ))}
                        {(dormantLimit < dormantThreads.length || threadsHasMore) && (
                          <LoadMoreSentinel
                            rootRef={scrollRef}
                            revision={visibleDormantThreads.length}
                            loading={threadsLoadingMore}
                            onVisible={loadMoreDormant}
                          />
                        )}
                      </>
                    )}
                  </section>
                )}
              </>
            )}
          </div>
        )}
      </div>

      {menuThread && (
        <div className="sheet-layer" role="presentation" onMouseDown={() => setMenuThread(null)}>
          <section ref={actionSheetRef} className="thread-action-sheet" role="dialog" aria-modal="true" aria-labelledby="thread-actions-title" onMouseDown={(event) => event.stopPropagation()}>
            <div ref={actionSheetHandleRef} className="sheet-handle" aria-hidden="true" />
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="eyebrow">会话操作</p>
                <h2 id="thread-actions-title" className="sheet-title truncate">{menuThread.title || '未命名会话'}</h2>
              </div>
              <button type="button" className="icon-button" onClick={() => setMenuThread(null)} aria-label="关闭菜单" title="关闭"><XMarkIcon className="h-4 w-4" /></button>
            </div>
            <div className="sheet-actions">
              {menuThread.isArchived ? (
                <button type="button" className="sheet-action" onClick={() => void handleThreadAction(menuThread, 'unarchive')} disabled={actionThreadID === menuThread.id}>
                  <ArchiveBoxIcon className="h-4 w-4" /><span>取消归档</span>
                </button>
              ) : (
                <>
                  <button type="button" className="sheet-action" onClick={() => void handleThreadAction(menuThread, 'pin')} disabled={actionThreadID === menuThread.id}>
                    <PinIcon className="h-4 w-4" /><span>{menuThread.isPinned ? '取消置顶' : '置顶'}</span>
                  </button>
                  <button type="button" className="sheet-action" onClick={() => void handleThreadAction(menuThread, 'dormant')} disabled={actionThreadID === menuThread.id}>
                    <CheckCircleIcon className="h-4 w-4" /><span>{menuThread.isDormant ? '取消休眠' : '休眠'}</span>
                  </button>
                  <button type="button" className="sheet-action sheet-action--danger" onClick={() => void handleThreadAction(menuThread, 'archive')} disabled={actionThreadID === menuThread.id}>
                    <ArchiveBoxIcon className="h-4 w-4" /><span>归档</span>
                  </button>
                </>
              )}
            </div>
          </section>
        </div>
      )}

      {createSheetOpen && (
        <div className="sheet-layer" role="presentation" onMouseDown={() => setCreateSheetOpen(false)}>
          <section className="create-sheet" role="dialog" aria-modal="true" aria-labelledby="create-thread-title" onMouseDown={(event) => event.stopPropagation()}>
            <div className="sheet-handle" aria-hidden="true" />
            <div className="flex items-center justify-between gap-3">
              <div><p className="eyebrow">新建</p><h2 id="create-thread-title" className="sheet-title">新会话</h2></div>
              <button type="button" className="icon-button" onClick={() => setCreateSheetOpen(false)} aria-label="关闭新建会话" title="关闭"><XMarkIcon className="h-4 w-4" /></button>
            </div>
            <label className="settings-label" htmlFor="new-thread-project">项目</label>
            <select id="new-thread-project" value={createProjectID} onChange={(event) => setCreateProjectID(event.target.value)} className="sheet-select">
              <option value="">选择一个项目</option>
              {projects.map((project) => <option key={project.id} value={project.id}>{project.name}</option>)}
            </select>
            <button type="button" className="primary-button primary-button--wide" onClick={() => { setCreateSheetOpen(false); onCreateDraft(createProjectID); }} disabled={!createProjectID || !canCreate || !isConnected || isSyncing}>
              继续编辑
            </button>
            {!canCreate && <p className="settings-note">当前 Mac 未声明新建会话能力。</p>}
          </section>
        </div>
      )}

      {automationOpen && projectID && (
        <LazyBoundary label="正在加载自动化…">
          <AutomationSheet
            projectID={projectID}
            onClose={() => setAutomationOpen(false)}
            onOpenThread={handleOpenAutomationThread}
          />
        </LazyBoundary>
      )}

      {bulkOpen && projectID && (
        <BulkThreadsSheet
          projectID={projectID}
          onClose={() => setBulkOpen(false)}
          onDone={(message, undoToken, action) => {
            setBulkOpen(false);
            setBulkNotice({ message, undoToken, action });
          }}
        />
      )}

    </div>
  );
};
