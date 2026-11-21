import { useStableLabel } from './timeline/useStableLabel';
import { previewToolOutput } from './timeline/previewLines';
import { fetchToolOutput, needsToolOutputFetch } from './timeline/toolOutputFetch';
import { useRemoteCall } from '../context/useRemoteCall';
import { SheetFrame } from './sheet/SheetFrame';
import { useCommandActivity } from '../hooks/useCommandActivity';
import { CommandActivityView } from './timeline/CommandActivityView';
import type { CommandActivityState } from '../utils/commandActivity';
import React from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import type { TaskItem } from '../types/contract';
import {
  ArrowPathIcon,
  CheckCircleIcon,
  ChevronRightIcon,
  CircleIcon,
  ClockIcon,
  DocumentTextIcon,
  ExclamationTriangleIcon,
  LockIcon,
  XMarkIcon
} from './Icons';
import { formatRelativeTime } from '../utils/date';
import { createPortal } from 'react-dom';
import { useLongPress } from '../hooks/useLongPress';
import { useSheetDrag } from '../hooks/useSheetDrag';
import { haptic } from '../utils/haptic';
import '../styles/mobile-press.css';
import { MessageMedia, type ReadThreadMedia } from './MessageMedia';
import { lastCodeBlock, parseSwarmReport, splitSwarmFence } from '../utils/markdown';
import { MarkdownView, type MarkdownHighlight } from './MarkdownView';
import { FIND_ACTIVE_SELECTOR, highlightRanges, type FindMatch } from '../utils/findHighlight';
import { SwarmDispatchCapsules, SwarmReportCapsules } from './SwarmCapsules';
import type { SwarmDispatchReportIndex } from './SwarmCapsules';
import { reportSectionKey } from './SwarmCapsules';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import { RunningGlyph } from './timeline/RunningGlyph';
import { ToolEntryDiff, parseToolEditDiff, summarizeToolDiff } from './timeline/ToolEntryDiff';
import { toolFilePath } from './timeline/toolFilePath';
import { TurnFinishedBar } from './timeline/TurnFinishedBar';
import { TurnFilesCard, type TurnEditedFile } from './timeline/TurnFilesCard';
import {
  BackgroundTasksRow,
  type BackgroundTaskCommand
} from './timeline/BackgroundTasksRow';
import { PlanDecisionMessage } from './timeline/PlanDecisionMessage';
import { useRemoteBackgroundTaskApi } from '../context/remoteBackgroundTaskApi';
import { PlanReaderSheet } from './PlanReaderSheet';
import {
  PLAN_OUTLINE_MAX_ITEMS,
  PLAN_PREVIEW_MAX_LINES,
  planNeedsReader,
  planOutline,
  planPreviewLines
} from '../utils/planOutline';
import { QuestionAnswerCard } from './timeline/QuestionAnswerCard';
import { parseQuestionAnswer } from '../utils/questionAnswer';
import { todoProgressFromMessage } from '../utils/todoProgress';
import {
  buildToolSegments,
  isToolCallEntry,
  toolSegmentKeyForMessage,
  toolSegmentOpen,
  toolSegmentOpenKey,
  toolSegmentPreviewText
} from './timeline/toolSegments';
import { TimelineErrorActions } from './thread/TimelineErrorActions';
import { reasoningHeaderTitle } from './timeline/stewardTimeline';
import { stewardTasksByTurnEnd, type StewardTurnTasks } from './timeline/stewardTasks';
import { StewardTaskCard } from './timeline/StewardTaskCard';
import {
  SwarmReportFullText,
  parseSwarmReportArchiveFields,
  type SwarmReportFullItem
} from './timeline/SwarmReportFullText';
import {
  AUTO_COLLAPSE_CHANGED_EVENT,
  getStoredAutoCollapse
} from '../services/storage';
import {
  StewardDecisionCard,
  StewardReply,
  StewardResultMessage,
  latestDecisionAnswers,
  parseStewardDecision,
  parseStewardResult
} from './timeline/StewardMessages';

import {
  buildSwarmReportIndex, buildTruncatedReportIndex, collectBackgroundCommands,
  countWorkCategories, isAssistantMessage, isEndedStatus, isThinkingEntry,
  classifyToolName, workSummaryText, turnWorkHeaderLabel, toolSegmentHeaderText, runningSwarmBees,
  workRecordHasFailure, timelineItemKey, timelineItemContainsMessage,
  truncatedReportsOfDispatch, isMergedSwarmReportRow, messageKindLabel,
  isFailedToolEntry, isBlockedToolEntry,
  COLLAPSE_THRESHOLD, COLLAPSE_PREVIEW_LENGTH
} from './timeline/projection';

import type { ThreadMessage, CopyVariant, CopyState, TimelineItem } from './timeline/projection';

export {
  COLLAPSE_THRESHOLD, COLLAPSE_PREVIEW_LENGTH, STEWARD_SILENT_MARKER,
  buildSwarmReportIndex, buildTruncatedReportIndex, buildTimelineItems, collectBackgroundCommands,
  countWorkCategories, formatShortDuration, formatTurnElapsedSeconds, isAssistantMessage,
  isEndedStatus, isHiddenTimelineMessage, isThinkingEntry, isWorkEntry, mergeLatestHistoryPage,
  mergeThreadMessages, normalizeToolNameKey, classifyToolName, workSummaryText, workElapsedMs,
  turnWorkHeaderLabel, toolSegmentHeaderText, runningSwarmBees, workRecordHasFailure,
  timelineItemKey, timelineItemContainsMessage, truncatedReportKey, truncatedReportsOfDispatch,
  isMergedSwarmReportRow, messageKindLabel
} from './timeline/projection';
export type {
  MessageKind, TurnStatus, ThreadMessage, CopyVariant, CopyState, WorkRecordItem, TimelineItem,
  WorkToolCategory, WorkCategoryCounts
} from './timeline/projection';


// 工作记录展开后的条目是平铺行，靠分隔线区分，不再各自套一张卡片。
export const WORK_ENTRY_ROW_CLASS = 'px-1 py-2 space-y-1 text-xs text-[var(--foreground)]';


export const workRecordExpandedMap = new Map<string, boolean>();


/**
 * 「自动折叠思考过程」偏好的订阅 hook：写偏好时 storage 会派发自定义事件，
 * 时间线据此立即重算默认开合状态，不需要重载页面。
 */
export function useAutoCollapseReasoning(): boolean {
  const [autoCollapse, setAutoCollapse] = React.useState<boolean>(() => getStoredAutoCollapse());
  React.useEffect(() => {
    const handle = () => setAutoCollapse(getStoredAutoCollapse());
    window.addEventListener(AUTO_COLLAPSE_CHANGED_EVENT, handle);
    return () => window.removeEventListener(AUTO_COLLAPSE_CHANGED_EVENT, handle);
  }, []);
  return autoCollapse;
}

/** 回合折叠项的实际开合状态：用户显式设置过就以设置为准，否则关闭自动折叠时默认展开。 */
export function workRecordExpanded(threadID: string, turnID: string, autoCollapse: boolean): boolean {
  return workRecordExpandedMap.get(`${threadID}:${turnID}`) ?? !autoCollapse;
}

/**
 * 把「未显式设置过」的回合先按当前默认开合状态写入模块级 map，再交给调用方的切换函数：
 * 这样手动点一下一定是从当前看到的状态翻到反面，不会因为默认为展开而第一次点击无效。
 */
function seedWorkRecordExpanded(threadID: string, turnID: string, autoCollapse: boolean): void {
  const key = `${threadID}:${turnID}`;
  if (!workRecordExpandedMap.has(key)) workRecordExpandedMap.set(key, !autoCollapse);
}

/** 回合折叠项的展开/收起回调：先补种默认状态，再调上游的切换实现。 */
function useWorkRecordHandlers(
  threadID: string,
  autoCollapse: boolean,
  onToggleWorkRecord: (turnID: string) => void
): { toggleWorkRecord: (turnID: string) => void } {
  const toggleWorkRecord = React.useCallback(
    (turnID: string) => {
      seedWorkRecordExpanded(threadID, turnID, autoCollapse);
      onToggleWorkRecord(turnID);
    },
    [threadID, autoCollapse, onToggleWorkRecord]
  );
  return { toggleWorkRecord };
}

/**
 * 与桌面端 isHiddenTimelineMessage 一致：总管唤醒这类系统输入不产生时间线行，
 * 整轮正文只有静默标记的助手回复也不产生行。判定必须在构建 items 这一层做，
 * 不能靠渲染返回空元素——外层容器仍在，父容器的 gap 会留下一段空白。
 */


type ErrorKindIconKey = 'lock' | 'clock' | 'document' | 'refresh' | 'stop' | 'error';

interface ErrorKindInfo {
  icon: ErrorKindIconKey;
  title: string;
  hint: string;
}

// 后端上报的失败类别（errKind）对应的标题与一句处理提示；未知或空值回落到原有「错误提示」。
const ERROR_KIND_INFO: Record<string, ErrorKindInfo> = {
  auth_required: {
    icon: 'lock',
    title: '需要登录',
    hint: '请在 Mac 端终端重新登录该 CLI 后再发送。'
  },
  rate_limited: {
    icon: 'clock',
    title: '额度或限流',
    hint: '请求被限流或额度已用完，稍等片刻后重试。'
  },
  context_exhausted: {
    icon: 'document',
    title: '上下文已满',
    hint: '建议压缩上下文或新开一个会话。'
  },
  max_turns: {
    icon: 'refresh',
    title: '达到最大轮数',
    hint: '本轮已达单轮最大往返次数，可以继续发送消息。'
  },
  cancelled: {
    icon: 'stop',
    title: '已取消',
    hint: '本轮已被取消，可以重新发送。'
  },
  api_error: {
    icon: 'error',
    title: '接口错误',
    hint: '接口调用失败，稍后可重试。'
  }
};

function renderErrorKindIcon(icon: ErrorKindIconKey): React.ReactNode {
  const cls = 'w-3.5 h-3.5 shrink-0';
  switch (icon) {
    case 'lock':
      return (
        <span aria-hidden="true" className="inline-flex shrink-0">
          <LockIcon className={cls} />
        </span>
      );
    case 'clock':
      return (
        <span aria-hidden="true" className="inline-flex shrink-0">
          <ClockIcon className={cls} />
        </span>
      );
    case 'document':
      return (
        <span aria-hidden="true" className="inline-flex shrink-0">
          <DocumentTextIcon className={cls} />
        </span>
      );
    case 'refresh':
      return (
        <span aria-hidden="true" className="inline-flex shrink-0">
          <ArrowPathIcon className={cls} />
        </span>
      );
    case 'stop':
      return (
        <svg className={cls} fill="currentColor" viewBox="0 0 24 24" aria-hidden="true">
          <rect x="6" y="6" width="12" height="12" rx="2" />
        </svg>
      );
    case 'error':
      return (
        <span aria-hidden="true" className="inline-flex shrink-0">
          <ExclamationTriangleIcon className={cls} />
        </span>
      );
  }
}

function errorKindInfo(errKind?: string | null): ErrorKindInfo | null {
  const key = (errKind ?? '').trim().toLowerCase();
  if (!key) return null;
  return ERROR_KIND_INFO[key] ?? null;
}

function messageCardClass(message: ThreadMessage): string {
  if (message.kind === 'warning') {
    return 'bg-[var(--card)] border-[var(--border)] text-[var(--warning-foreground)]';
  }
  if (message.kind === 'error') {
    // 用户主动取消不是故障，弱化成次要文字色
    if ((message.errKind ?? '').trim().toLowerCase() === 'cancelled') {
      return 'bg-[var(--card)] border-[var(--border)] text-[var(--foreground-muted)]';
    }
    return 'bg-[var(--card)] border-[var(--border)] text-[var(--danger-foreground)]';
  }
  if (message.kind === 'plan' || message.kind === 'todo') {
    return 'bg-[var(--card)] border-[var(--border)] text-[var(--foreground)]';
  }
  return 'bg-[var(--card)] border-[var(--border)] text-[var(--foreground)]';
}

function messageLayoutClass(message: ThreadMessage): string {
  const isUser = message.kind === 'user' || message.role === 'user';
  const isAssistant = isAssistantMessage(message);
  if (isUser) return 'ml-auto max-w-[85%] rounded-[20px] p-3.5';
  if (isAssistant) return 'max-w-full border-0 p-0';
  return 'rounded-2xl border p-3.5';
}

export const MarkdownContent: React.FC<{ text: string; prefix?: string }> = ({ text }) => {
  return <MarkdownView text={text} />;
};

const WorkEntryOutput: React.FC<{ text: string; highlight?: MarkdownHighlight; math: boolean; preview: boolean; threadID: string; messageID: string; outputBytes?: number }> = ({ text, highlight, math, preview, threadID, messageID, outputBytes }) => {
  const { call } = useRemoteCall();
  const [expanded, setExpanded] = React.useState(false);
  const [loading, setLoading] = React.useState(false);
  const [archivedText, setArchivedText] = React.useState<string | null>(null);
  const needsFetch = needsToolOutputFetch(outputBytes, text);
  const displayedText = expanded && archivedText !== null ? archivedText : text;
  const result = previewToolOutput(displayedText, expanded);
  const expandable = (preview && result.hidden > 0) || (needsFetch && archivedText === null) || archivedText !== null;
  const toggle = async () => {
    if (loading) return;
    if (expanded) { setExpanded(false); return; }
    if (needsFetch && archivedText === null) {
      setLoading(true);
      try {
        const fetched = await fetchToolOutput((params) => call('threads.toolOutput', params), threadID, messageID);
        setArchivedText(fetched);
        setExpanded(true);
      } catch { /* 失败后保留截断内容和重试入口 */ } finally { setLoading(false); }
    } else { setExpanded(true); }
  };
  return <>
    <MarkdownView text={preview ? result.text : displayedText} highlight={highlight} math={math} />
    {expandable && <button type="button" disabled={loading} onClick={() => void toggle()} className="px-2 py-1 text-xs text-[var(--primary)] hover:underline disabled:opacity-50">{loading ? '加载中' : expanded ? '收起' : result.hidden > 0 ? `展开 ${result.hidden} 行` : '展开全文'}</button>}
  </>;
};

export { parseSwarmEntries } from './MarkdownView';
export type { SwarmEntryParsed } from './MarkdownView';

interface MessageCopyMenuProps {
  messageID: string;
  /** 完整原文，折叠长消息时也不是截断的可见文本 */
  fullText: string;
  label: string;
  /** 仅助手回复提供「复制最后代码块」 */
  allowLastCode: boolean;
  /** 菜单向上展开（助手回复靠近底部输入区）；否则向下 */
  openUp?: boolean;
  copyState: CopyState;
  onCopyMessage: (messageID: string, text: string, variant: CopyVariant) => void;
  /** 最后一轮用户提示词才有：撤回本轮 */
  onRecall?: () => void;
}

// 复制小菜单：点按钮展开，点外部或按 Esc 关闭；结果沿用 copyState 在按钮上显示。
const MessageCopyMenu: React.FC<MessageCopyMenuProps> = ({
  messageID,
  fullText,
  label,
  allowLastCode,
  openUp = false,
  copyState,
  onCopyMessage,
  onRecall
}) => {
  const [open, setOpen] = React.useState(false);
  const rootRef = React.useRef<HTMLDivElement | null>(null);
  const hasCode = React.useMemo(
    () => open && allowLastCode && lastCodeBlock(fullText) !== null,
    [allowLastCode, fullText, open]
  );

  React.useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
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

  const choose = (variant: CopyVariant) => {
    setOpen(false);
    onCopyMessage(messageID, fullText, variant);
  };

  const isCopied = copyState?.status === 'copied';
  const isError = copyState?.status === 'error';
  const itemClass =
    'block w-full min-h-[40px] px-3 text-left text-xs font-medium text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] active:opacity-80';

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={label}
        className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition active:scale-95"
      >
        {isCopied
          ? '已复制'
          : isError
          ? copyState?.message
            ? `失败: ${copyState.message}`
            : '复制失败'
          : '复制'}
      </button>
      {open && (
        <div
          role="menu"
          className={`absolute right-0 z-20 ${openUp ? 'bottom-full mb-1' : 'top-full mt-1'} min-w-[148px] overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--card)] shadow-lg`}
        >
          <button type="button" role="menuitem" className={itemClass} onClick={() => choose('plain')}>
            复制消息
          </button>
          <button type="button" role="menuitem" className={itemClass} onClick={() => choose('markdown')}>
            复制为 Markdown
          </button>
          {hasCode && (
            <button type="button" role="menuitem" className={itemClass} onClick={() => choose('lastCode')}>
              复制最后代码块
            </button>
          )}
          {onRecall && (
            <button
              type="button"
              role="menuitem"
              className={`${itemClass} border-t border-[var(--border)] text-[var(--danger-foreground)]`}
              onClick={() => {
                setOpen(false);
                onRecall();
              }}
            >
              撤回本轮
            </button>
          )}
        </div>
      )}
    </div>
  );
};

export interface MessageMoreMenuItem {
  key: string;
  label: string;
  onSelect: () => void;
  /** 破坏性操作（撤销此轮改动）用警示色 */
  danger?: boolean;
}

export function quoteMessageMenuItem(
  isAssistant: boolean,
  canQuote: boolean,
  messageID: string,
  onQuote: (messageID: string) => void
): MessageMoreMenuItem | null {
  return isAssistant && canQuote
    ? { key: 'quote', label: '引用', onSelect: () => onQuote(messageID) }
    : null;
}

/** 消息菜单上的附加操作：由 ThreadView 提供，缺省时对应按钮不渲染 */
export interface MessageMenuActions {
  /** 引用到回复：把消息正文按桌面 quoteIntoDraft 的格式插进输入框草稿（timeline_menu.go:35） */
  onQuote: (messageID: string) => void;
  /** 编辑用户消息：改完发送到分支/新建的会话（timeline_menu.go:40） */
  onEdit?: (messageID: string) => void;
  /** 撤销此轮改动：需要宿主声明 threads.revert.* 才传（timeline_menu.go:55） */
  onRevertTurn?: (messageID: string, turnID: string) => void;
  /** 发送到主会话：仅子会话传（timeline_menu.go:63） */
  onSendToParent?: (messageID: string) => void;
}

// 「更多」小菜单：收纳撤销此轮改动、发送到主会话这类次要操作，交互与复制菜单一致。
const MessageMoreMenu: React.FC<{ items: MessageMoreMenuItem[]; openUp?: boolean }> = ({
  items,
  openUp = false
}) => {
  const [open, setOpen] = React.useState(false);
  const rootRef = React.useRef<HTMLDivElement | null>(null);

  React.useEffect(() => {
    if (!open) return;
    const handlePointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
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

  if (items.length === 0) return null;

  const itemClass =
    'block w-full min-h-[40px] px-3 text-left text-xs font-medium text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] active:opacity-80';

  return (
    <div ref={rootRef} className="relative">
      <button
        type="button"
        onClick={() => setOpen((value) => !value)}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label="更多消息操作"
        className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition active:scale-95"
      >
        更多
      </button>
      {open && (
        <div
          role="menu"
          className={`absolute right-0 z-20 ${openUp ? 'bottom-full mb-1' : 'top-full mt-1'} min-w-[148px] overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--card)] shadow-lg`}
        >
          {items.map((item) => (
            <button
              key={item.key}
              type="button"
              role="menuitem"
              className={`${itemClass} ${item.danger ? 'text-[var(--danger-foreground)]' : ''}`}
              onClick={() => {
                setOpen(false);
                item.onSelect();
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
};

const BranchIcon: React.FC<{ className?: string }> = ({ className = 'w-4 h-4' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="2" stroke="currentColor">
    <path strokeLinecap="round" strokeLinejoin="round" d="M7 6a2 2 0 1 0 0-4 2 2 0 0 0 0 4Zm0 12a2 2 0 1 0 0-4 2 2 0 0 0 0 4Zm10-6a2 2 0 1 0 0-4 2 2 0 0 0 0 4ZM7 6v8m0-4a5 5 0 0 1 5-5h3" />
  </svg>
);

/** 查找定位的当前命中：与 findMatches 返回的 FindMatch 同源，这里只用得到消息与消息内序号 */
export type ActiveFindMatch = Pick<FindMatch, 'messageID' | 'occurrence'>;

/** 长消息折叠判定用的正文：助手消息先剥离末尾的派单围栏（与 TimelineEntry 里的 bodyText 同源） */
function messageBodyText(message: ThreadMessage): string {
  const fullText = message.text || '';
  const isAssistant = isAssistantMessage(message);
  const split = isAssistant ? splitSwarmFence(fullText) : null;
  return split ? split.body : fullText;
}

/**
 * 当前命中是否要先展开这条消息才看得见：
 * - 已展开就不用动；
 * - 派单汇报 / 派单任务的胶囊默认折叠，里面有命中就要展开；
 * - 长消息只在命中落在预览窗口之外时展开；命中就在可见的那段里时保持原折叠状态，避免为可见文字也撑开整条消息。
 */
function messageNeedsFindExpand(
  message: ThreadMessage,
  query: string,
  caseSensitive: boolean,
  occurrence: number,
  expanded: boolean
): boolean {
  if (expanded) return false;
  if (message.kind === 'swarm_report' || message.kind === 'swarm_task') return true;
  const body = messageBodyText(message);
  if (body.length <= COLLAPSE_THRESHOLD) return false;
  const ranges = highlightRanges(body, query, caseSensitive);
  const target = ranges[occurrence];
  // 计数口径对不上（命中跨了 Markdown 标记）时宁可变长展开，保证当前命中一定可见
  if (!target) return true;
  return target.start >= COLLAPSE_PREVIEW_LENGTH;
}

/**
 * 当前命中滚动：先让外层把条目滚进视口（虚拟列表用 scrollToIndex），再等带 data-find-active 的字渲染出来，
 * 把它滚到视口中间。列表是虚拟的且展开会改变高度，所以这里做有限次重试，而不是只找一个帧。
 */
function useActiveFindScroll(
  messageID: string | null,
  occurrence: number | null,
  scrollRoot: { current: HTMLDivElement | null } | undefined,
  scrollToItem: ((messageID: string) => void) | null
): void {
  const scrollToItemRef = React.useRef(scrollToItem);
  scrollToItemRef.current = scrollToItem;
  // 只在命中变化时重新定位，同一条命中的重复渲染不打断用户滚动
  const targetKey = messageID === null ? '' : `${messageID}:${occurrence ?? 0}`;
  React.useEffect(() => {
    if (targetKey === '' || messageID === null) return;
    scrollToItemRef.current?.(messageID);
    let attempt = 0;
    let timer = 0;
    let cancelled = false;
    const findActiveMark = (): HTMLElement | null => {
      const root: ParentNode = scrollRoot?.current ?? document;
      return root.querySelector<HTMLElement>(FIND_ACTIVE_SELECTOR);
    };
    const tick = () => {
      if (cancelled) return;
      const target = findActiveMark();
      if (target) {
        target.scrollIntoView({ block: 'center', behavior: 'smooth' });
        return;
      }
      if (attempt >= 8) return;
      attempt += 1;
      timer = window.setTimeout(tick, 100);
    };
    timer = window.setTimeout(tick, 80);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [targetKey, messageID, scrollRoot]);
}

interface TimelineEntryProps {
  activity: Record<string, CommandActivityState>;
  threadID: string;
  item: TimelineItem;
  expanded: boolean;
  copyState: CopyState;
  /** 每分钟递增，仅用于刷新「x 分钟前」这类相对时间 */
  minuteTick: number;
  onToggleExpanded: (messageID: string) => void;
  onToggleWorkRecord: (turnID: string) => void;
  /** 回合内各段（连续工具调用）的开合状态表，键由 toolSegmentOpenKey 生成 */
  segmentOpenState: Record<string, boolean>;
  /** 切换一段的开合；defaultOpen 是这段当前的默认开合状态，未显式设置过时从它翻转 */
  onToggleToolSegment: (key: string, defaultOpen: boolean) => void;
  /** 「自动折叠思考过程」偏好：段的默认开合与回合折叠一致 */
  autoCollapse: boolean;
  onCopyMessage: (messageID: string, text: string, variant: CopyVariant) => void;
  canBranch: boolean;
  onBranchMessage?: (messageID: string) => void;
  /** 消息菜单的附加操作（引用、编辑、撤销、发回父会话）；缺省时对应按钮不渲染 */
  messageMenu?: MessageMenuActions;
  /** 可撤回的消息 id（时间线最后一条用户提示词）；与 onRecallTurn 同时给出才显示入口 */
  recallMessageID?: string;
  onRecallTurn?: () => void;
  /** 宿主声明 threads.media 时才传入；缺省则不渲染消息里的图片 */
  readThreadMedia?: ReadThreadMedia;
  /** 子代理状态：派单胶囊靠它匹配 bee 并显示状态与耗时 */
  swarmState?: RemoteSwarmState | null;
  onOpenSwarmBee?: (threadID: string) => void;
  onOpenSwarmBees?: () => void;
  /** 时间线预算的「发起胶囊 → 汇报」映射；并进去的汇报不再单独占一行 */
  swarmReportIndex?: SwarmDispatchReportIndex;
  /** 被截断汇报的存档索引（名字 + 任务 → bee ID）：并进胶囊的汇报也要能展开全文 */
  swarmTruncatedReports?: Record<string, SwarmReportFullItem>;
  /** 搜索定位命中后的短暂高亮环（由外层容器渲染，这里参与 memo 比较） */
  highlighted: boolean;
  /** 运行中工具条目的停止入口；宿主未声明该能力时不传 */
  onKillRunningCommand?: (messageID: string) => void;
  /** 会话内查找关键词（已 trim）：非空时正文里每一处命中都包 mark */
  findQuery?: string;
  /** 查找是否区分大小写，缺省不区分（与 findMatches 的默认口径一致） */
  findCaseSensitive?: boolean;
  /** 当前命中的消息 id：只有这条消息里的第 findActiveOccurrence 处标为当前命中 */
  findActiveMessageID?: string | null;
  /** 当前命中在该消息内的序号（从 0 开始，与 findMatches 的 occurrence 同源） */
  findActiveOccurrence?: number | null;
  /** 时间线最后一条可见消息的 id：错误卡片的「接着干」只在最新一条上自动重试 */
  lastMessageID?: string | null;
  latestActionMessage?: boolean;
  /** 当前是否还在跑；错误卡片的自动重试要等这一轮跑完再开始 */
  isRunning?: boolean;
  /** 错误卡片的续跑入口；宿主没接线时不传，错误卡片因此不渲染按钮 */
  onContinue?: (prompt: string) => Promise<void> | void;
  /**
   * 回合改动文件卡片打开文件：返回 true 表示已打开（工作区预览），
   * 返回 false 或未传时点击文件改为行内展开本回合差量。
   */
  onOpenFile?: (path: string) => boolean;
  decisionAnswers: Record<string, string>;
  onSendStewardMessage?: (text: string) => void | Promise<unknown>;
  onAnswerStewardDecision?: (id: string, answer: string) => void | Promise<unknown>;
  onOpenThread?: (threadID: string) => void;
  resultTask?: TaskItem;
}

/**
 * 时间线上最后一条可见消息的 id：由调用方按 items 从后往前找，工作记录取其中最后一条。
 * 隐藏消息在传给本组件之前已经过滤掉，所以这里拿到的是用户能看到的最后一条。
 */
function lastVisibleMessageID(items: TimelineItem[]): string | null {
  for (let index = items.length - 1; index >= 0; index -= 1) {
    const item = items[index];
    if (item.type === 'message') return item.message.id;
    if (item.type === 'workRecord' && item.entries.length > 0) {
      return item.entries[item.entries.length - 1].id;
    }
  }
  return null;
}

function latestActionMessageIDs(items: TimelineItem[]): Set<string> {
  const result = new Set<string>();
  let user = false;
  let assistant = false;
  for (let index = items.length - 1; index >= 0 && (!user || !assistant); index -= 1) {
    const item = items[index];
    if (item.type !== 'message' || item.message.kind === 'plan') continue;
    const message = item.message;
    if (!user && (message.kind === 'user' || message.role === 'user') && !parseSwarmReport(message.text || '')) {
      result.add(message.id);
      user = true;
    } else if (!assistant && isAssistantMessage(message)) {
      result.add(message.id);
      assistant = true;
    }
  }
  return result;
}

/** 回合改动文件卡片的相等性：路径与增删行数都没变就不用重渲染。 */
function sameTurnFiles(left: TurnEditedFile[], right: TurnEditedFile[]): boolean {
  if (left === right) return true;
  if (left.length !== right.length) return false;
  return left.every((file, index) => {
    const other = right[index];
    return (
      other !== undefined &&
      file.path === other.path &&
      file.additions === other.additions &&
      file.deletions === other.deletions
    );
  });
}

function sameTimelineItem(left: TimelineItem, right: TimelineItem): boolean {
  if (left === right) return true;
  if (left.type === 'message' && right.type === 'message') return left.message === right.message;
  if (left.type === 'workRecord' && right.type === 'workRecord') {
    return (
      left.turnID === right.turnID &&
      left.turnIndex === right.turnIndex &&
      left.turnStatus === right.turnStatus &&
      left.turnStartedAt === right.turnStartedAt &&
      left.turnCompletedAt === right.turnCompletedAt &&
      left.entries.length === right.entries.length &&
      left.entries.every((entry, index) => entry === right.entries[index])
    );
  }
  if (left.type === 'turnFinished' && right.type === 'turnFinished') {
    return (
      left.turnID === right.turnID &&
      left.turnIndex === right.turnIndex &&
      left.stats.durationMs === right.stats.durationMs &&
      left.stats.toolCount === right.stats.toolCount &&
      left.stats.failedCount === right.stats.failedCount &&
      left.stats.endedAt === right.stats.endedAt &&
      sameTurnFiles(left.files, right.files)
    );
  }
  return false;
}

interface PlanMessageCardProps {
  message: ThreadMessage;
  highlight?: MarkdownHighlight;
}

/**
 * 计划消息卡片：长计划只给大纲 + 截断预览，「阅读全文」打开 PlanReaderSheet（对齐桌面端 renderPlanCard）。
 * 开合状态放在卡片内部：TimelineEntry 里已经有一堆分支，不能再在它里面条件式调用 hooks。
 */
const PlanMessageCard: React.FC<PlanMessageCardProps> = ({ message, highlight }) => {
  const [readerOpen, setReaderOpen] = React.useState(false);
  const closeReader = React.useCallback(() => setReaderOpen(false), []);
  const text = message.text || '';
  const hasText = text.trim().length > 0;
  const truncated = hasText && planNeedsReader(text);
  const headings = React.useMemo(
    () => (truncated ? planOutline(text, PLAN_OUTLINE_MAX_ITEMS) : []),
    [text, truncated]
  );
  const preview = React.useMemo(
    () => (truncated ? planPreviewLines(text, PLAN_PREVIEW_MAX_LINES) : text),
    [text, truncated]
  );

  return (
    <article
      key={message.id}
      title={formatRelativeTime(message.createdAt)}
      className={`${messageLayoutClass(message)} space-y-2 min-w-0 overflow-hidden mb-4 ${messageCardClass(message)}`}
    >
      <div className="flex items-center justify-between gap-2 text-mc-caption">
        <span className="font-semibold text-[var(--foreground)]">{messageKindLabel(message)}</span>
      </div>

      <div className="min-w-0 space-y-2">
        {!hasText ? (
          <p className="text-xs text-[var(--foreground-muted)]">（空消息）</p>
        ) : (
          <>
            {headings.length > 0 && (
              <ul className="space-y-0.5" aria-label="计划大纲">
                {headings.map((heading, index) => (
                  <li
                    key={`${heading.text}-${index}`}
                    className="flex min-w-0 items-baseline gap-1.5 text-xs text-[var(--foreground-muted)]"
                    style={{ paddingLeft: Math.min(Math.max(heading.level - 1, 0), 2) * 10 }}
                  >
                    <span
                      className="h-1 w-1 shrink-0 rounded-full bg-current opacity-50"
                      aria-hidden="true"
                    />
                    <span className="min-w-0 truncate">{heading.text}</span>
                  </li>
                ))}
              </ul>
            )}
            <MarkdownView text={preview} highlight={highlight} />
            {truncated && (
              <p className="text-mc-caption text-[var(--foreground-muted)]">
                内容较长，只显示大纲与前 {PLAN_PREVIEW_MAX_LINES} 行
              </p>
            )}
          </>
        )}
      </div>

      {truncated && (
        <div className="flex justify-end">
          <button
            type="button"
            onClick={() => setReaderOpen(true)}
            aria-label="阅读计划全文"
            className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground)] transition active:scale-95"
          >
            阅读全文
          </button>
        </div>
      )}

      {readerOpen && (
        <PlanReaderSheet title="完整计划" eyebrow="实施计划" text={text} onClose={closeReader} />
      )}
    </article>
  );
};
PlanMessageCard.displayName = 'PlanMessageCard';

// 运行中回合的组头每秒刷新一次；非运行状态不注册定时器，卸载时清理。
function useRunningNowMs(active: boolean): number {
  const [nowMs, setNowMs] = React.useState(() => Date.now());
  React.useEffect(() => {
    if (!active) return;
    setNowMs(Date.now());
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [active]);
  return nowMs;
}

// 窄屏判定（W-3）：宽度小于 768 视为手机，常驻操作按钮改为长按菜单。
const NARROW_SCREEN_QUERY = '(max-width: 767px)';

function useIsNarrowScreen(): boolean {
  const [narrow, setNarrow] = React.useState(
    () => typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia(NARROW_SCREEN_QUERY).matches
  );
  React.useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia(NARROW_SCREEN_QUERY);
    const onChange = (event: MediaQueryListEvent) => setNarrow(event.matches);
    setNarrow(query.matches);
    query.addEventListener('change', onChange);
    return () => query.removeEventListener('change', onChange);
  }, []);
  return narrow;
}

interface MessageActionSheetItem {
  key: string;
  label: string;
  danger?: boolean;
  onSelect: () => void;
}

interface MessageActionSheetProps {
  title: string;
  items: MessageActionSheetItem[];
  onClose: () => void;
}

// 长按消息后的底部菜单：复用 index.css 的 thread-action-sheet 卡片样式。
const MessageActionSheet: React.FC<MessageActionSheetProps> = ({ title, items, onClose }) => {
  const sheetRef = React.useRef<HTMLElement | null>(null);
  const handleRef = React.useRef<HTMLDivElement | null>(null);
  useSheetDrag(sheetRef, handleRef, onClose, true);

  return createPortal(
    <SheetFrame onRequestClose={onClose} size="menu" cardRef={sheetRef} labelledBy="message-actions-title">
        <div ref={handleRef} className="sheet-handle" aria-hidden="true" />
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="eyebrow">消息操作</p>
            <h2 id="message-actions-title" className="sheet-title truncate">{title}</h2>
          </div>
          <button type="button" className="icon-button" onClick={onClose} aria-label="关闭消息操作" title="关闭">
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>
        <div className="sheet-actions">
          {items.map((item) => (
            <button
              key={item.key}
              type="button"
              className={`sheet-action${item.danger ? ' sheet-action--danger' : ''}`}
              onClick={() => {
                onClose();
                item.onSelect();
              }}
            >
              {item.label}
            </button>
          ))}
        </div>
    </SheetFrame>,
    document.body
  );
};

// 行级 memo：流式输出只会让变化的消息重新渲染，其余行（含 Markdown 解析结果）保持原样。
export const TimelineEntry = React.memo<TimelineEntryProps>((props) => {
  const {
    threadID,
    activity,
    item,
    expanded,
    copyState,
    onToggleExpanded,
    onToggleWorkRecord,
    segmentOpenState,
    onToggleToolSegment,
    autoCollapse,
    onCopyMessage,
    canBranch,
    onBranchMessage,
    messageMenu,
    recallMessageID,
    onRecallTurn,
    readThreadMedia,
    swarmState,
    onOpenSwarmBee,
    onOpenSwarmBees,
    swarmReportIndex,
    swarmTruncatedReports,
    onKillRunningCommand,
    findQuery,
    findCaseSensitive,
    findActiveMessageID,
    findActiveOccurrence,
    lastMessageID,
    latestActionMessage = true,
    isRunning = false,
    onContinue,
    onOpenFile,
    decisionAnswers,
    onSendStewardMessage,
    onAnswerStewardDecision,
    onOpenThread,
    resultTask
  } = props;
  // 长按菜单（W-3）：只对窄屏、有正文的用户消息生效；长按成立先给中档触感。
  const narrowScreen = useIsNarrowScreen();
  const [messageSheetOpen, setMessageSheetOpen] = React.useState(false);
  const isUserMessageItem = item.type === 'message' && (item.message.kind === 'user' || item.message.role === 'user');
  const longPress = useLongPress(
    () => {
      haptic('medium');
      setMessageSheetOpen(true);
    },
    narrowScreen && isUserMessageItem && (item.type === 'message' ? (item.message.text ?? '').trim().length > 0 : false)
  );
  const findHighlightQuery = (findQuery ?? '').trim();
  // 高亮对象按消息复用同一份引用：否则流式渲染时 MarkdownView 会因新对象而重解析整段 Markdown
  const highlightBase = React.useMemo<Pick<MarkdownHighlight, 'query' | 'caseSensitive'> | null>(
    () => (findHighlightQuery === '' ? null : { query: findHighlightQuery, caseSensitive: Boolean(findCaseSensitive) }),
    [findHighlightQuery, findCaseSensitive]
  );
  const highlightPlain = React.useMemo<MarkdownHighlight | null>(
    () => (highlightBase ? { ...highlightBase, activeOccurrence: null } : null),
    [highlightBase]
  );
  const highlightActive = React.useMemo<MarkdownHighlight | null>(
    () => (highlightBase ? { ...highlightBase, activeOccurrence: findActiveOccurrence ?? null } : null),
    [highlightBase, findActiveOccurrence]
  );
  // 不传高亮时返回 undefined：MarkdownView 的 highlight 是可选 prop，null 不合类型
  const highlightFor = (messageID: string): MarkdownHighlight | undefined =>
    highlightPlain === null
      ? undefined
      : messageID === findActiveMessageID
      ? highlightActive ?? undefined
      : highlightPlain ?? undefined;

  // B1 运行中计时：只有工作记录处于 running 时才每秒刷新
  const workRecordRunning = item.type === 'workRecord' && item.turnStatus === 'running';
  const workRecordNowMs = useRunningNowMs(workRecordRunning);
  const rawWorkHeaderLabel = item.type === 'workRecord' ? turnWorkHeaderLabel(item.entries, workRecordRunning, workRecordNowMs, item.turnStartedAt, item.turnCompletedAt) : '';
  const stableWorkHeaderLabel = useStableLabel(rawWorkHeaderLabel);

  if (item.type === 'turnFinished') {
    return (
      <React.Fragment key={timelineItemKey(item)}>
        <TurnFinishedBar stats={item.stats} />
        <TurnFilesCard files={item.files} onOpenFile={onOpenFile} />
      </React.Fragment>
    );
  }

  if (item.type === 'workRecord') {
    const isExpanded = expanded;
    const workHeaderLabel = workRecordRunning ? stableWorkHeaderLabel : rawWorkHeaderLabel;
    const workSummary = workSummaryText(countWorkCategories(item.entries));
    const workStatusLabel =
      item.turnStatus === 'running'
        ? '正在执行'
        : item.turnStatus === 'completed'
        ? '已完成'
        : item.turnStatus === 'interrupted'
        ? '已停止'
        : item.turnStatus === 'failed'
        ? '执行失败'
        : '工作记录';
    // 运行中如果已经有一条命令在跑，动效交给工具行；否则让组头文字自己脉动。
    // 这里不认 background：它只出现在已结束的回合里，不该把组头当成还在等它结束。
    const shimmerHeader =
      workRecordRunning && !item.entries.some((entry) => entry.toolStatus === 'running');
    return (
      <div
        key={`work-${item.turnID}`}
        className="mb-4 rounded-2xl border border-[var(--border)] bg-[var(--card)] p-3 space-y-2 min-w-0"
      >
        <button
          type="button"
          data-work-record-id={item.turnID}
          onClick={() => onToggleWorkRecord(item.turnID)}
          aria-expanded={isExpanded}
          aria-label={isExpanded ? `收起第 ${item.turnIndex} 轮工作记录` : `展开第 ${item.turnIndex} 轮工作记录`}
          title={`${workStatusLabel} · ${workSummary}`}
          className="w-full flex items-center justify-between gap-2 text-left min-h-[38px] rounded-xl px-1 hover:bg-[var(--card-alt,var(--card))] transition"
        >
          <div className="flex items-center gap-2 min-w-0 text-mc-body text-[var(--foreground-muted)]">
            <span className="shrink-0">
              {item.turnStatus === 'running' ? (
                <RunningGlyph size={12} label="工作记录执行中" />
              ) : item.turnStatus === 'completed' ? (
                <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
              ) : item.turnStatus === 'interrupted' ? (
                <CircleIcon className="w-4 h-4 text-[var(--warning-foreground)]" />
              ) : item.turnStatus === 'failed' ? (
                <XMarkIcon className="w-4 h-4 text-[var(--danger-foreground)]" />
              ) : (
                <CircleIcon className="w-4 h-4 opacity-40" />
              )}
            </span>
            <span
              className={`font-medium truncate ${
                shimmerHeader ? 'animate-pulse motion-reduce:animate-none' : ''
              }`}
            >
              {workHeaderLabel}
            </span>
            {workRecordHasFailure(item.entries) && (
              <span className="shrink-0 text-[var(--danger-foreground)]" title="工具执行失败">
                <ExclamationTriangleIcon className="w-3.5 h-3.5" />
              </span>
            )}
          </div>
          <div className="flex items-center gap-1.5 shrink-0 text-[var(--foreground-muted)] text-mc-footnote">
            <ChevronRightIcon
              className={`w-3.5 h-3.5 transition-transform duration-200 ${
                isExpanded ? 'rotate-90' : ''
              }`}
            />
          </div>
        </button>

        {isExpanded && (
          <div
            data-expanded-work-record={item.turnID}
            className="relative pt-2 border-t border-[var(--border)] space-y-2.5"
          >
            {buildToolSegments(item.entries).map((segment) => {
              const segmentKey = toolSegmentOpenKey(threadID, segment.key);
              const segmentIsOpen = toolSegmentOpen(segmentOpenState, threadID, segment.key, !autoCollapse);
              // 最新一段就是整条会话的最后一条活动行时算「运行中」，与桌面端 toolGroupLive 同口径
              const segmentRunning =
                Boolean(isRunning) && segment.entries[segment.entries.length - 1]?.id === lastMessageID;
              const segmentFailed = segment.entries.some((entry) => isFailedToolEntry(entry));
              return (
                <div key={`segment-${segment.key}`} className="space-y-1.5">
                  {segment.collapsible && (
                    <button
                      type="button"
                      data-tool-segment-id={segmentKey}
                      onClick={() => onToggleToolSegment(segmentKey, !autoCollapse)}
                      aria-expanded={segmentIsOpen}
                      aria-label={segmentIsOpen ? '收起这段工具调用' : '展开这段工具调用'}
                      className="w-full flex items-center justify-between gap-2 text-left min-h-[30px] rounded-lg px-1.5 hover:bg-[var(--card-alt,var(--card))] transition"
                    >
                      <span className="flex items-center gap-1.5 min-w-0 text-mc-footnote text-[var(--foreground-muted)]">
                        {segmentRunning ? (
                          <RunningGlyph size={11} label="这段工具调用进行中" />
                        ) : segmentFailed ? (
                          <XMarkIcon className="w-3.5 h-3.5 shrink-0 text-[var(--danger-foreground)]" />
                        ) : (
                          <CheckCircleIcon className="w-3.5 h-3.5 shrink-0 text-[var(--primary)]" />
                        )}
                        <span className="truncate">{toolSegmentHeaderText(segment.entries, segmentRunning)}</span>
                      </span>
                      <ChevronRightIcon
                        className={`w-3.5 h-3.5 shrink-0 transition-transform duration-200 ${
                          segmentIsOpen ? 'rotate-90' : ''
                        }`}
                      />
                    </button>
                  )}
                  {(segment.collapsible ? segmentIsOpen : true) && (
                    <div className="divide-y divide-[var(--border)]">
                      {segment.entries.map((entry) => {
                        const isToolEntry = isToolCallEntry(entry);
                        const toolName = (entry.toolName || '').trim();
                        const kindLabel = isToolEntry
                          ? toolName || '工具调用'
                          : entry.kind === 'thinking'
                          ? '思考过程'
                          : '系统提示';
                        // A8：思考行结束后在标题后缀原文第一行的要旨（流式中取最新一句），空文本不加尾巴
                        const isThinkingRow = !isToolEntry && isThinkingEntry(entry);
                        const thinkingStreaming =
                          segmentRunning && segment.entries[segment.entries.length - 1]?.id === entry.id;
                        const rowLabel = isThinkingRow
                          ? reasoningHeaderTitle(entry.text || '', thinkingStreaming)
                          : kindLabel;
                        const blocked = isToolEntry && isBlockedToolEntry(entry);
                        const failed = isToolEntry && isFailedToolEntry(entry);
                        // background 表示 provider 托管的后台任务仍在跑（回合已结束），显示层按运行中渲染图标与动效。
                        const running =
                          isToolEntry && (entry.toolStatus === 'running' || entry.toolStatus === 'background');
                        // 文件编辑类工具尝试解析出差量；解析不出就回落到纯文本
                        const diffFiles = isToolEntry ? parseToolEditDiff(entry.toolName, entry.toolInput) : null;
                        const filePath = isToolEntry ? toolFilePath(entry.toolName, entry.toolInput) : null;
                        const diffSummary = diffFiles ? summarizeToolDiff(diffFiles) : null;
                        // 待办类工具（TodoWrite / update_plan）：进度与输入框上方的胶囊共用同一份解析
                        const todoProgress = isToolEntry
                          ? todoProgressFromMessage({ role: 'tool', toolInput: entry.toolInput, text: entry.text })
                          : null;
                        const labelClass = blocked
                          ? 'border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)]'
                          : failed
                          ? 'border-[var(--danger-foreground)] bg-[var(--card)] text-[var(--danger-foreground)]'
                          : 'border-[var(--border)] bg-[var(--card)]';

                        return (
                          <div
                            key={entry.id}
                            className={WORK_ENTRY_ROW_CLASS}
                          >
                            <div className="flex items-center justify-between gap-2 text-mc-caption text-[var(--foreground-muted)]">
                              <span className="flex items-center gap-1.5 min-w-0">
                                {running ? (
                                  <RunningGlyph size={11} label={`${kindLabel}运行中`} />
                                ) : blocked ? (
                                  <CircleIcon className="w-3.5 h-3.5 shrink-0 opacity-60" />
                                ) : failed ? (
                                  <XMarkIcon className="w-3.5 h-3.5 shrink-0 text-[var(--danger-foreground)]" />
                                ) : isToolEntry ? (
                                  <CheckCircleIcon className="w-3.5 h-3.5 shrink-0 text-[var(--primary)]" />
                                ) : null}
                                <span className={`px-1.5 py-0.5 rounded font-medium border truncate ${labelClass}`}>
                                  <span className={isThinkingRow && thinkingStreaming ? 'text-shimmer' : undefined}>{rowLabel}</span>
                                </span>
                                {filePath && (onOpenFile ? (
                                  <button type="button" className="min-w-0 truncate font-mono hover:underline" title={filePath} onClick={(event) => { event.stopPropagation(); onOpenFile(filePath); }}>
                                    {filePath.split(/[\\/]/).pop()}
                                  </button>
                                ) : (
                                  <span className="min-w-0 truncate font-mono" title={filePath}>{filePath.split(/[\\/]/).pop()}</span>
                                ))}
                                {diffSummary && (
                                  <span className="shrink-0 font-mono">
                                    <span className="text-emerald-600 dark:text-emerald-400">+{diffSummary.additions}</span>
                                    <span className="mx-0.5 opacity-50">/</span>
                                    <span className="text-rose-600 dark:text-rose-400">-{diffSummary.deletions}</span>
                                  </span>
                                )}
                                {blocked && <span className="shrink-0 opacity-80">已被 hook 拦截</span>}
                                {todoProgress && (
                                  <span
                                    className="shrink-0 rounded border border-[var(--border)] px-1 py-0.5 font-mono"
                                    title="待办完成进度"
                                  >
                                    {todoProgress.done}/{todoProgress.total}
                                  </span>
                                )}
                              </span>
                              <span className="flex items-center gap-2 shrink-0">
                                {/* 后台任务由 provider 托管，宿主不能单独中止，只对 running 给「停止」入口。 */}
                                {entry.toolStatus === 'running' && onKillRunningCommand && (
                                  <button
                                    type="button"
                                    onClick={() => onKillRunningCommand(entry.id)}
                                    aria-label="停止这条命令"
                                    className="min-h-[24px] px-1.5 rounded-md border border-[var(--border)] bg-[var(--card)] text-mc-caption text-[var(--foreground-muted)] hover:text-[var(--danger-foreground)] transition"
                                  >
                                    停止
                                  </button>
                                )}
                                <span>
                                  {formatRelativeTime(entry.createdAt)}
                                </span>
                              </span>
                            </div>
                            <div
                              className={`min-w-0 space-y-1.5 ${
                                isToolEntry ? 'text-mc-footnote text-[var(--foreground-muted)]' : 'text-mc-body'
                              }`}
                            >
                              {entry.toolStatus === 'running' && classifyToolName(entry.toolName) === 'command' && <CommandActivityView activity={activity[entry.id]} />}
                              <WorkEntryOutput text={entry.text || ''} highlight={highlightFor(entry.id)} math={!isThinkingRow} preview={isToolEntry && ['read', 'search'].includes(classifyToolName(entry.toolName))} threadID={threadID} messageID={entry.id} outputBytes={entry.outputBytes} />
                              {diffFiles && <ToolEntryDiff files={diffFiles} />}
                              {readThreadMedia && entry.media && entry.media.length > 0 && (
                                <MessageMedia
                                  threadID={threadID}
                                  messageID={entry.id}
                                  media={entry.media}
                                  readThreadMedia={readThreadMedia}
                                  size="large"
                                />
                              )}
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                  {segment.collapsible && !segmentIsOpen && segmentRunning && (
                    <p className="pl-2 pr-1 truncate text-mc-caption text-[var(--foreground-muted)] opacity-80">
                      {toolSegmentPreviewText(segment.entries)}
                    </p>
                  )}
                </div>
              );
            })}
            <p className="pl-2 pr-1 text-mc-caption text-[var(--foreground-muted)] opacity-80">
              {workSummary}
            </p>
          </div>
        )}
      </div>
    );
  }

  const message = item.message;

  if ((message.kind as string) === 'plan_decision') return <PlanDecisionMessage text={message.text} />;

  // 总管协议消息必须在普通助手分支之前处理；解析失败则继续落到下面的原文渲染。
  if (message.kind === 'steward_decision_answer') return null;
  if (message.kind === 'steward_reply' && message.role === 'assistant') {
    return <StewardReply key={message.id} message={message} />;
  }
  if (message.kind === 'steward_decision' && message.role === 'assistant') {
    const decision = parseStewardDecision(message.text);
    if (decision) {
      return (
        <StewardDecisionCard
          key={message.id}
          message={message}
          answer={decisionAnswers[decision.id]}
          onSend={onSendStewardMessage}
          onAnswer={onAnswerStewardDecision}
        />
      );
    }
  }
  if (message.kind === 'steward_result' && message.role === 'assistant') {
    const result = parseStewardResult(message.text);
    if (result) {
      return <StewardResultMessage key={message.id} message={message} onOpenThread={onOpenThread} task={resultTask} />;
    }
  }

  if (message.kind === 'swarm_report') {
    const isExpanded = expanded;

    return (
      <div
        key={message.id}
        className="my-2 min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--border)] bg-[var(--card)] p-2.5 transition"
      >
        <button
          type="button"
          onClick={() => onToggleExpanded(message.id)}
          aria-expanded={isExpanded}
          aria-label={isExpanded ? '收起子代理汇报' : '展开子代理汇报'}
          className="w-full flex items-center justify-between gap-2 text-left min-h-[38px] px-1.5 rounded-xl hover:bg-[var(--card-alt,var(--card))] transition"
        >
          <div className="flex items-center gap-2 min-w-0">
            <span className="w-2 h-2 rounded-full bg-[var(--primary)] shrink-0" />
            <span className="text-xs font-semibold text-[var(--foreground)]">
              子代理汇报
            </span>
            <span className="text-mc-caption text-[var(--foreground-muted)] shrink-0">
              · {formatRelativeTime(message.createdAt)}
            </span>
          </div>
          <div className="flex items-center gap-1.5 shrink-0 text-[var(--foreground-muted)] text-mc-caption">
            <span>{isExpanded ? '收起' : '展开'}</span>
            <ChevronRightIcon
              className={`w-3.5 h-3.5 transition-transform duration-200 ${
                isExpanded ? 'rotate-90' : ''
              }`}
            />
          </div>
        </button>

        {isExpanded && (
          <div className="mt-2 min-w-0 max-w-full overflow-hidden border-t border-[var(--border)] px-1 text-xs space-y-2">
            {!message.text?.trim() ? (
              <p className="text-[var(--foreground-muted)]">（无报告内容）</p>
            ) : (
              <MarkdownView text={message.text || ''} highlight={highlightFor(message.id)} />
            )}
          </div>
        )}
      </div>
    );
  }

  if (message.kind === 'swarm_task') {
    const isExpanded = expanded;

    return (
      <div key={message.id} className="my-2 flex flex-col items-start min-w-0 max-w-full">
        <button
          type="button"
          onClick={() => onToggleExpanded(message.id)}
          aria-expanded={isExpanded}
          aria-label={isExpanded ? '收起主代理下达的任务' : '展开主代理下达的任务'}
          className="min-h-[34px] inline-flex items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--user-bubble)] px-3 py-1.5 text-xs font-medium text-[var(--foreground)] shadow-sm hover:opacity-90 transition active:scale-95"
        >
          <span>主代理下达的任务</span>
          <ChevronRightIcon
            className={`w-3.5 h-3.5 transition-transform duration-200 ${
              isExpanded ? 'rotate-90' : ''
            }`}
          />
        </button>

        {isExpanded && (
          <div className="mt-2 w-full max-w-full overflow-hidden p-3 rounded-2xl bg-[var(--card)] border border-[var(--border)] text-xs text-[var(--foreground)] whitespace-pre-wrap [overflow-wrap:anywhere] leading-relaxed select-text">
            {message.text || '（无任务内容）'}
          </div>
        )}
      </div>
    );
  }

  if (message.kind === 'host_event') {
    return (
      <div
        key={message.id}
        className="my-2 min-w-0 max-w-full rounded-2xl border border-dashed border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 py-2.5 text-[var(--foreground-muted)]"
      >
        <div className="flex items-center gap-1.5 text-mc-caption">
          <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--foreground-muted)]" />
          <span className="font-medium">宿主转来</span>
          <span className="shrink-0">· {formatRelativeTime(message.createdAt)}</span>
        </div>
        <div className="mt-1.5 min-w-0 max-w-full overflow-hidden text-xs leading-relaxed text-[var(--foreground)]">
          {message.text?.trim() ? (
            <MarkdownView text={message.text} highlight={highlightFor(message.id)} />
          ) : (
            <p>（无事件内容）</p>
          )}
        </div>
      </div>
    );
  }

  // 计划消息：长计划走大纲 + 预览 + 「阅读全文」，短计划仍然直接全文显示（todo 不受影响）。
  // 放在用户/助手分支之前：计划消息的 role 不固定，别被这两类分支抢走。
  if (message.kind === 'plan') {
    return <PlanMessageCard key={message.id} message={message} highlight={highlightFor(message.id)} />;
  }

  const fullText = message.text || '';
  const isUser = message.kind === 'user' || message.role === 'user';
  const isAssistant = isAssistantMessage(message);
  // 派单块总在回复末尾，且常常超出折叠上限：先剥离再对正文做折叠判断
  const swarmSplit = isAssistant ? splitSwarmFence(fullText) : null;
  const bodyText = swarmSplit ? swarmSplit.body : fullText;
  const hasSwarmCapsules = Boolean(swarmSplit && (swarmSplit.entries.length > 0 || swarmSplit.pending));
  const isLong = bodyText.length > COLLAPSE_THRESHOLD;
  const isExpanded = expanded;
  const visibleText = isLong && !isExpanded ? bodyText.slice(0, COLLAPSE_PREVIEW_LENGTH) : bodyText;
  const hasText = visibleText.trim().length > 0;
  // 错误类消息按 errKind 给出更具体的标题与提示，未知类别保持原样
  const errorInfo = message.kind === 'error' ? errorKindInfo(message.errKind) : null;
  const totalLines = bodyText.split('\n').length;
  const visibleLines = visibleText.split('\n').length;
  const isEnded = isEndedStatus(message.turnStatus);
  const hasNonEmptyText = bodyText.trim().length > 0;
  const hasMedia = Boolean(message.media && message.media.length > 0);
  const showCopyButton = Boolean(message.isFinalResponse) && isEnded && isAssistant && hasNonEmptyText;
  // 已发送的用户消息即可复制，不受 isEnded 限制；流式未结束的助手消息仍不显示
  const showUserCopy = isUser && hasNonEmptyText;
  // 引用到回复：所有有正文的消息都能引用，正文按桌面 quoteForReply 的格式插进草稿（timeline_menu.go:35）
  const canQuote = hasNonEmptyText && Boolean(messageMenu);
  // 撤销此轮改动：只在这条是该回合最后一条助手答复时出现（与桌面 timeline_menu.go:55 的条件一致）
  const revertTurnID = isAssistant && message.isFinalResponse && message.turnID ? message.turnID : null;
  // 发送到主会话：仅子会话里对助手答复开放（桌面 timeline_menu.go:63 的 sendToParentThread）
  const canSendToParent = isAssistant && hasNonEmptyText && Boolean(messageMenu?.onSendToParent);
  // 「更多」只收次要操作；助手引用只加到最终答复的菜单。
  const moreItems: MessageMoreMenuItem[] = [];
  const quoteItem = quoteMessageMenuItem(
    isAssistant,
    showCopyButton && canQuote,
    message.id,
    (messageID) => messageMenu?.onQuote(messageID)
  );
  if (quoteItem) moreItems.push(quoteItem);
  if (revertTurnID && messageMenu?.onRevertTurn) {
    moreItems.push({
      key: 'revert-turn',
      label: '撤销此轮改动',
      danger: true,
      onSelect: () => messageMenu?.onRevertTurn?.(message.id, revertTurnID)
    });
  }
  if (canSendToParent) {
    moreItems.push({
      key: 'send-to-parent',
      label: '发送到主会话',
      onSelect: () => messageMenu?.onSendToParent?.(message.id)
    });
  }

  if (isUser) {
    // 子代理汇报以用户消息的形式写入，改用可展开的汇报胶囊
    const swarmReports = parseSwarmReport(fullText);
    if (swarmReports) {
      // 已经并进发起胶囊的汇报不再单独占一行；整条都并进去了就不渲染这一行
      const archiveFields = parseSwarmReportArchiveFields(fullText);
      const remainingReports: SwarmReportFullItem[] = [];
      swarmReports.forEach((report, index) => {
        if (swarmReportIndex?.consumedReports[reportSectionKey(message.id, index)]) return;
        const fields = archiveFields[index];
        remainingReports.push({
          report,
          beeID: fields?.beeID ?? '',
          truncated: Boolean(fields?.truncated)
        });
      });
      if (remainingReports.length === 0) return null;
      return (
        <div key={message.id} className="my-2 flex max-w-full flex-col items-start gap-1 min-w-0">
          <SwarmReportCapsules
            reports={remainingReports.map((entry) => entry.report)}
            swarmState={swarmState}
            onOpenBee={onOpenSwarmBee}
          />
          {/* 被截断的汇报只带开头一段：这里给「展开全文」按需读存档 */}
          <SwarmReportFullText threadID={threadID} items={remainingReports} />
          <span className="text-mc-footnote text-[var(--foreground-muted)] px-1">
            {formatRelativeTime(message.createdAt)}
          </span>
        </div>
      );
    }

    // 长按菜单项：与原来常驻按钮一一对应，只收当前消息可用的操作。
    const sheetItems: MessageActionSheetItem[] = [];
    if (showUserCopy) {
      sheetItems.push({
        key: 'copy-plain',
        label: '复制',
        onSelect: () => onCopyMessage(message.id, fullText, 'plain')
      });
      sheetItems.push({
        key: 'copy-markdown',
        label: '复制为 Markdown',
        onSelect: () => onCopyMessage(message.id, fullText, 'markdown')
      });
    }
    if (onRecallTurn && message.id === recallMessageID) {
      sheetItems.push({
        key: 'recall-turn',
        label: '撤回本轮',
        danger: true,
        onSelect: () => onRecallTurn()
      });
    }
    if (canQuote) {
      sheetItems.push({ key: 'quote', label: '引用', onSelect: () => messageMenu?.onQuote(message.id) });
    }
    if (messageMenu?.onEdit) {
      sheetItems.push({ key: 'edit', label: '编辑', onSelect: () => messageMenu?.onEdit?.(message.id) });
    }

    return (
      <React.Fragment key={message.id}>
        <div className="group flex flex-col items-end mb-4 min-w-0" title={formatRelativeTime(message.createdAt)} {...longPress.handlers}>
          <div
            className="rounded-[20px] px-3.5 py-2.5 bg-[var(--user-bubble)] text-[16px] leading-[23px] text-[var(--foreground)] max-w-[85%] min-w-0 break-words select-text [overflow-wrap:anywhere]"
            style={longPress.pressed ? { backgroundColor: 'var(--press)' } : undefined}
          >
            <div data-long-press-ignore="true">
              {!hasText ? (
                !hasMedia && <p className="text-[var(--foreground-muted)]">（空消息）</p>
              ) : (
                <MarkdownView text={visibleText} highlight={highlightFor(message.id)} math={false} />
              )}
              {hasMedia && readThreadMedia && (
                <div className={hasText ? 'mt-2' : ''}>
                  <MessageMedia
                    threadID={threadID}
                    messageID={message.id}
                    media={message.media}
                    readThreadMedia={readThreadMedia}
                    size="compact"
                  />
                </div>
              )}
            </div>
          </div>
          {isLong && (
            <button
              type="button"
              onClick={() => onToggleExpanded(message.id)}
              aria-expanded={isExpanded}
              aria-label={
                isExpanded ? `收起第 ${message.turnIndex} 条消息` : `展开第 ${message.turnIndex} 条消息的完整内容`
              }
              className="mt-1 min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground)] transition active:scale-95"
            >
              {isExpanded ? '收起' : `展开 · 还有 ${Math.max(0, totalLines - visibleLines)} 行`}
            </button>
          )}
          <div className={`flex items-center justify-end gap-2 mt-1 px-1 min-w-0 ${!narrowScreen && !latestActionMessage && !copyState ? '[@media(hover:hover)]:opacity-0 group-hover:opacity-100 focus-within:opacity-100 [&:has([aria-expanded=true])]:opacity-100 transition-opacity motion-reduce:transition-none' : ''}`}>
            {!narrowScreen && canQuote && (
              <button
                type="button"
                onClick={() => messageMenu?.onQuote(message.id)}
                aria-label="引用到回复"
                title="引用到回复"
                className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition active:scale-95"
              >
                引用
              </button>
            )}
            {!narrowScreen && messageMenu?.onEdit && (
              <button
                type="button"
                onClick={() => messageMenu?.onEdit?.(message.id)}
                aria-label="编辑后重发"
                title="编辑后重发"
                className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition active:scale-95"
              >
                编辑
              </button>
            )}
            {!narrowScreen && (showUserCopy || (onRecallTurn && message.id === recallMessageID)) && (
              <MessageCopyMenu
                messageID={message.id}
                fullText={fullText}
                label="复制这条消息"
                allowLastCode={false}
                copyState={copyState}
                onCopyMessage={onCopyMessage}
                onRecall={
                  onRecallTurn && message.id === recallMessageID ? onRecallTurn : undefined
                }
              />
            )}
          </div>
        </div>
        {messageSheetOpen && sheetItems.length > 0 && (
          <MessageActionSheet
            title={`第 ${message.turnIndex} 条消息`}
            items={sheetItems}
            onClose={() => setMessageSheetOpen(false)}
          />
        )}
      </React.Fragment>
    );
  }

  if (isAssistant) {
    return (
      <article
        key={message.id}
        title={formatRelativeTime(message.createdAt)}
        className="group mb-5 w-full bg-transparent border-0 p-0 text-[16px] leading-[23px] text-[var(--foreground)] min-w-0"
      >
        <div className="min-w-0 max-w-full space-y-2">
          {!hasText ? (
            // 只有派单块的回复不显示“空答复”，胶囊紧跟着渲染
            !hasSwarmCapsules && <p className="text-sm text-[var(--foreground-muted)]">（空答复）</p>
          ) : (
            <MarkdownView text={visibleText} highlight={highlightFor(message.id)} />
          )}
          {swarmSplit && hasSwarmCapsules && (
            <SwarmDispatchCapsules
              entries={swarmSplit.entries}
              pending={swarmSplit.pending}
              pendingCount={swarmSplit.pendingCount}
              messageID={message.id}
              reportIndex={swarmReportIndex}
              swarmState={swarmState}
              onOpenBee={onOpenSwarmBee}
              onOpenBees={onOpenSwarmBees}
            />
          )}
          {/* 汇报并进发起胶囊后没有自己的消息行：被截断的那些在这里给「展开全文」 */}
          {swarmSplit && hasSwarmCapsules && (
            <SwarmReportFullText
              threadID={threadID}
              items={truncatedReportsOfDispatch(
                message.id,
                swarmSplit.entries,
                swarmReportIndex,
                swarmTruncatedReports
              )}
            />
          )}
          {hasMedia && readThreadMedia && (
            <MessageMedia
              threadID={threadID}
              messageID={message.id}
              media={message.media}
              readThreadMedia={readThreadMedia}
              size="large"
            />
          )}
          {isLong && (
            <p className="text-mc-footnote text-[var(--foreground-muted)]">
              {isExpanded ? '已展开完整内容' : `内容较长，仅显示前 ${COLLAPSE_PREVIEW_LENGTH} 个字符`}
            </p>
          )}
        </div>

        <div className={`flex items-center justify-end gap-2 min-w-0 mt-2 ${!narrowScreen && !latestActionMessage && !copyState ? '[@media(hover:hover)]:opacity-0 group-hover:opacity-100 focus-within:opacity-100 [&:has([aria-expanded=true])]:opacity-100 transition-opacity motion-reduce:transition-none' : ''}`}>
          {isLong && (
            <button
              type="button"
              onClick={() => onToggleExpanded(message.id)}
              aria-expanded={isExpanded}
              aria-label={
                isExpanded
                  ? `收起第 ${message.turnIndex} 条消息`
                  : `展开第 ${message.turnIndex} 条消息的完整内容`
              }
              className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground)] transition active:scale-95"
            >
              {isExpanded ? '收起' : '展开全部'}
            </button>
          )}

          {showCopyButton && (
            <>
              {canBranch && onBranchMessage && (
                <button
                  type="button"
                  onClick={() => onBranchMessage(message.id)}
                  aria-label="分支到新会话"
                  title="分支到新会话"
                  className="min-h-[32px] px-2 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition active:scale-95 inline-flex items-center justify-center"
                >
                  <BranchIcon className="w-3.5 h-3.5" />
                </button>
              )}
              <MessageMoreMenu items={moreItems} openUp />
              <MessageCopyMenu
                messageID={message.id}
                fullText={fullText}
                label={`复制第 ${message.turnIndex} 轮答复`}
                allowLastCode
                openUp
                copyState={copyState}
                onCopyMessage={onCopyMessage}
              />
            </>
          )}
        </div>
      </article>
    );
  }

  if (message.kind === 'question_answer') {
    const record = parseQuestionAnswer(fullText);
    // 解析失败时落到下面的普通系统消息卡片，正文原样显示，不因为一条脏消息丢内容。
    if (record) return <QuestionAnswerCard key={message.id} record={record} />;
  }

  return (
    <article
      key={message.id}
      title={formatRelativeTime(message.createdAt)}
      className={`${messageLayoutClass(message)} space-y-2 min-w-0 overflow-hidden mb-4 ${messageCardClass(message)}`}
    >
      <div className="flex items-center justify-between gap-2 text-mc-caption">
        <span className="inline-flex items-center gap-1.5 font-semibold text-[var(--foreground)]">
          {errorInfo ? (
            <>
              {renderErrorKindIcon(errorInfo.icon)}
              <span>{errorInfo.title}</span>
            </>
          ) : (
            messageKindLabel(message)
          )}
        </span>
      </div>

      <div className="min-w-0 space-y-2">
        {!hasText ? (
          <p className="text-xs text-[var(--foreground-muted)]">（空消息）</p>
        ) : (
          <MarkdownView text={visibleText} highlight={highlightFor(message.id)} math={!isThinkingEntry(message)} />
        )}
        {errorInfo && <p className="text-xs text-[var(--foreground-muted)]">{errorInfo.hint}</p>}
        {errorInfo && onContinue ? (
          <TimelineErrorActions
            message={message}
            isLast={Boolean(lastMessageID) && lastMessageID === message.id}
            isRunning={isRunning}
            onContinue={onContinue}
            threadID={threadID}
          />
        ) : null}
        {hasMedia && readThreadMedia && (
          <MessageMedia
            threadID={threadID}
            messageID={message.id}
            media={message.media}
            readThreadMedia={readThreadMedia}
            size="large"
          />
        )}
        {isLong && (
          <p className="text-mc-caption text-[var(--foreground-muted)]">
            {isExpanded
              ? '已展开完整内容'
              : `内容较长，仅显示前 ${COLLAPSE_PREVIEW_LENGTH} 个字符`}
          </p>
        )}
      </div>

      <div className="flex items-center justify-end gap-2 min-w-0">
        {isLong && (
          <button
            type="button"
            onClick={() => onToggleExpanded(message.id)}
            aria-expanded={isExpanded}
            aria-label={
              isExpanded
                ? `收起第 ${message.turnIndex} 条消息`
                : `展开第 ${message.turnIndex} 条消息的完整内容`
            }
            className="min-h-[32px] px-2.5 rounded-lg text-xs font-medium border border-[var(--border)] bg-[var(--card)] hover:bg-[var(--card-alt,var(--card))] text-[var(--foreground)] transition active:scale-95"
          >
            {isExpanded ? '收起' : '展开全部'}
          </button>
        )}
      </div>
    </article>
  );
}, (previous, next) =>
  previous.threadID === next.threadID &&
  previous.activity === next.activity &&
  sameTimelineItem(previous.item, next.item) &&
  previous.expanded === next.expanded &&
  previous.copyState === next.copyState &&
  previous.minuteTick === next.minuteTick &&
  previous.canBranch === next.canBranch &&
  previous.onToggleExpanded === next.onToggleExpanded &&
  previous.onToggleWorkRecord === next.onToggleWorkRecord &&
  previous.segmentOpenState === next.segmentOpenState &&
  previous.onToggleToolSegment === next.onToggleToolSegment &&
  previous.autoCollapse === next.autoCollapse &&
  previous.onCopyMessage === next.onCopyMessage &&
  previous.onBranchMessage === next.onBranchMessage &&
  previous.messageMenu === next.messageMenu &&
  previous.recallMessageID === next.recallMessageID &&
  previous.onRecallTurn === next.onRecallTurn &&
  previous.readThreadMedia === next.readThreadMedia &&
  previous.swarmState === next.swarmState &&
  previous.onOpenSwarmBee === next.onOpenSwarmBee &&
  previous.onOpenSwarmBees === next.onOpenSwarmBees &&
  previous.swarmReportIndex === next.swarmReportIndex &&
  previous.swarmTruncatedReports === next.swarmTruncatedReports &&
  previous.highlighted === next.highlighted &&
  previous.onKillRunningCommand === next.onKillRunningCommand &&
  previous.findQuery === next.findQuery &&
  previous.findCaseSensitive === next.findCaseSensitive &&
  previous.findActiveMessageID === next.findActiveMessageID &&
  previous.findActiveOccurrence === next.findActiveOccurrence &&
  previous.lastMessageID === next.lastMessageID &&
  previous.latestActionMessage === next.latestActionMessage &&
  previous.isRunning === next.isRunning &&
  previous.onContinue === next.onContinue &&
  previous.onOpenFile === next.onOpenFile &&
  previous.resultTask === next.resultTask &&
  previous.onOpenThread === next.onOpenThread &&
  previous.decisionAnswers === next.decisionAnswers &&
  previous.onSendStewardMessage === next.onSendStewardMessage &&
  previous.onAnswerStewardDecision === next.onAnswerStewardDecision
);
TimelineEntry.displayName = 'TimelineEntry';

/**
 * 把滚动容器里带 data-message-id 的消息挪到视口顶部附近（提问导航轨/列表跳转用）。
 * 找不到元素时返回 false，调用方可以下一帧再试。
 */
function nudgeMessageToTop(container: HTMLElement | null, messageID: string, offset = 8): boolean {
  if (!container) return false;
  const escaped = messageID.replace(/["\\]/g, '\\$&');
  const element = container.querySelector<HTMLElement>(`[data-message-id="${escaped}"]`);
  if (!element) return false;
  const delta = element.getBoundingClientRect().top - container.getBoundingClientRect().top - offset;
  if (Math.abs(delta) > 2) container.scrollTop += delta;
  return true;
}

interface ThreadTimelineProps {
  threadID: string;
  items: TimelineItem[];
  expandedIds: Record<string, boolean>;
  copyState: CopyState;
  onToggleExpanded: (messageID: string) => void;
  onToggleWorkRecord: (turnID: string) => void;
  onCopyMessage: (messageID: string, text: string, variant: CopyVariant) => void;
  canBranch?: boolean;
  onBranchMessage?: (messageID: string) => void;
  /** 消息菜单的附加操作（引用、编辑、撤销、发回父会话）；缺省时对应按钮不渲染 */
  messageMenu?: MessageMenuActions;
  /** 可撤回的消息 id（时间线最后一条用户提示词）；与 onRecallTurn 同时给出才显示入口 */
  recallMessageID?: string;
  onRecallTurn?: () => void;
  readThreadMedia?: ReadThreadMedia;
  /** 子代理群状态：派单/汇报胶囊靠它显示状态、耗时和跳转 */
  swarmState?: RemoteSwarmState | null;
  onOpenSwarmBee?: (threadID: string) => void;
  onOpenSwarmBees?: () => void;
  /** 停止一个运行中的工具命令；宿主未声明该能力时不传 */
  onKillRunningCommand?: (messageID: string) => void;
  /** 搜索定位命中的消息 id：变为非空时滚到该条并短暂高亮 */
  focusMessageID?: string | null;
  /**
   * 外部请求把某条消息滚到视口顶部附近（提问导航轨的点击/列表选择、推送点开）：nonce 变化即重新执行。
   * flash 为真时滚到后额外闪一次底色（推送直达），与 focusMessageID 的居中定位互不影响。
   */
  scrollToMessageRequest?: { messageID: string; nonce: number; flash?: boolean } | null;
  /** 会话内查找关键词；非空时正文里每一处命中都高亮成 mark 元素 */
  findQuery?: string;
  /** 查找是否区分大小写，缺省不区分 */
  findCaseSensitive?: boolean;
  /** 当前命中：它的消息会自动展开（长消息预览之外、回合工作记录、汇报胶囊）并滚到视口中间 */
  activeFindMatch?: ActiveFindMatch | null;
  /** 当前是否还在跑；错误卡片的自动重试要等这一轮跑完再开始，缺省当没在跑 */
  isRunning?: boolean;
  /** 错误卡片的「接着干」：发一条从中断处继续的提示；不传则不渲染错误卡片里的按钮 */
  onContinue?: (prompt: string) => Promise<void> | void;
  /**
   * 回合改动文件卡片打开文件：返回 true 表示已打开（工作区预览），
   * 返回 false 或未传时点击文件改为行内展开本回合差量。
   */
  onOpenFile?: (path: string) => boolean;
  scrollRef?: { current: HTMLDivElement | null };
  /** 发送后锚定期间在时间线底部垫出的空白（px）：锚点消息要能停在视口顶部，默认 0 */
  bottomPadding?: number;
  /** 是否为总管会话。 */
  isSteward?: boolean;
  stewardTasks?: TaskItem[];
  /** 总管决策卡选择/忽略时发送一条普通用户消息。 */
  onSendStewardMessage?: (text: string) => void | Promise<unknown>;
  onAnswerStewardDecision?: (id: string, answer: string) => void | Promise<unknown>;
  /** 总管结果卡的「打开任务」跳转。 */
  onOpenThread?: (threadID: string) => void;
}

interface VirtualTimelineProps extends ThreadTimelineProps {
  activity: Record<string, CommandActivityState>;
  onActivityExpanded: (expanded: boolean) => void;
  scrollRef: { current: HTMLDivElement | null };
  /** 当前需要绘高亮环的条目 key（由 ThreadTimeline 控制生命周期） */
  highlightedKey?: string | null;
  /** 推送点开的消息所在条目 key：给它闪一次底色（由 ThreadTimeline 控制生命周期） */
  flashKey?: string | null;
  /** 「发起胶囊 → 汇报」映射：由 ThreadTimeline 算好传入，汇报行不再单独占一行 */
  swarmReportIndex?: SwarmDispatchReportIndex;
  /** 被截断汇报的存档索引：并进胶囊的汇报靠它找回全文入口 */
  swarmTruncatedReports?: Record<string, SwarmReportFullItem>;
  /** 回合内各段（连续工具调用）的开合状态表 */
  segmentOpenState: Record<string, boolean>;
  /** 切换一段的开合 */
  onToggleToolSegment: (key: string, defaultOpen: boolean) => void;
  /** B7 时间线末尾的后台任务清单：由 ThreadTimeline 统一筛好传进来 */
  backgroundCommands: BackgroundTaskCommand[];
  runningBees: RemoteSwarmBee[];
  decisionAnswers: Record<string, string>;
  turnTasks: Map<number, StewardTurnTasks>;
}

function TurnTasks({ group, onOpenThread }: { group?: StewardTurnTasks; onOpenThread?: (threadID: string) => void }) {
  if (!group) return null;
  return <div className="mt-3 space-y-2">
    {group.created.map((task) => <StewardTaskCard key={task.id} task={task} onOpenThread={onOpenThread} />)}
    {group.amended.map((task) => <StewardTaskCard key={task.id} compact task={task} onOpenThread={onOpenThread} />)}
  </div>;
}

function resultTaskForItem(item: TimelineItem, tasks?: TaskItem[]): TaskItem | undefined {
  if (item.type !== 'message' || item.message.kind !== 'steward_result') return undefined;
  const result = parseStewardResult(item.message.text);
  if (!result) return undefined;
  return tasks?.find((task) => (result.task && task.id === result.task) || (result.thread && task.threadID === result.thread));
}

const VirtualTimeline: React.FC<VirtualTimelineProps> = ({
  threadID,
  activity,
  onActivityExpanded,
  items,
  expandedIds,
  copyState,
  onToggleExpanded,
  onToggleWorkRecord,
  segmentOpenState,
  onToggleToolSegment,
  onCopyMessage,
  canBranch = false,
  onBranchMessage,
  recallMessageID,
  onRecallTurn,
  readThreadMedia,
  swarmState,
  onOpenSwarmBee,
  onOpenSwarmBees,
  messageMenu,
  swarmReportIndex,
  swarmTruncatedReports,
  onKillRunningCommand,
  focusMessageID,
  scrollToMessageRequest,
  highlightedKey,
  flashKey,
  scrollRef,
  findQuery,
  findCaseSensitive,
  activeFindMatch,
  isRunning,
  onContinue,
  bottomPadding = 0,
  onOpenFile,
  backgroundCommands,
  runningBees,
  decisionAnswers,
  turnTasks,
  stewardTasks,
  onSendStewardMessage,
  onAnswerStewardDecision,
  onOpenThread
}) => {
  const [minuteTick, setMinuteTick] = React.useState(0);
  React.useEffect(() => {
    const timer = window.setInterval(() => setMinuteTick((tick) => tick + 1), 60_000);
    return () => window.clearInterval(timer);
  }, []);

  const autoCollapse = useAutoCollapseReasoning();
  const backgroundTaskApi = useRemoteBackgroundTaskApi();
  const { toggleWorkRecord } = useWorkRecordHandlers(threadID, autoCollapse, onToggleWorkRecord);
  // 时间线最后一条可见消息：错误卡片的自动重试只认它
  const lastMessageID = React.useMemo(() => lastVisibleMessageID(items), [items]);
  const latestActionIDs = React.useMemo(() => latestActionMessageIDs(items), [items]);

  const listRef = React.useRef<HTMLDivElement | null>(null);
  const [scrollMargin, setScrollMargin] = React.useState(0);

  React.useLayoutEffect(() => {
    if (!listRef.current || !scrollRef.current) return;
    const offset = Math.round(
      listRef.current.getBoundingClientRect().top -
        scrollRef.current.getBoundingClientRect().top +
        scrollRef.current.scrollTop
    );
    if (offset !== scrollMargin) {
      setScrollMargin(offset);
    }
  });

  const virtualizer = useVirtualizer({
    // B7 的聚合行作为虚拟列表末尾的额外一项，高度由 measureElement 实测
    count: items.length + (backgroundCommands.length > 0 || runningBees.length > 0 ? 1 : 0),
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 160,
    overscan: 6,
    gap: 12,
    scrollMargin,
    getItemKey: (index) => {
      if (index >= items.length) return 'background-tasks';
      const item = items[index];
      if (!item) return index;
      return timelineItemKey(item);
    }
  });

  const itemsRef = React.useRef(items);
  React.useEffect(() => {
    itemsRef.current = items;
  });

  // 搜索命中：等列表完成本轮渲染后把目标条目滚到视口中间
  React.useEffect(() => {
    if (!focusMessageID) return;
    const index = itemsRef.current.findIndex((item) => timelineItemContainsMessage(item, focusMessageID));
    if (index < 0) return;
    const timer = window.setTimeout(() => {
      virtualizer.scrollToIndex(index, { align: 'center' });
    }, 60);
    return () => window.clearTimeout(timer);
  }, [focusMessageID]);

  // 提问导航轨的跳转：先 scrollToIndex 把目标条目送进视口，再按 DOM 实测位置把消息本身贴到顶部
  const scrollRequestMessageID = scrollToMessageRequest?.messageID ?? null;
  const scrollRequestNonce = scrollToMessageRequest?.nonce ?? 0;
  React.useEffect(() => {
    if (!scrollRequestMessageID) return;
    const index = itemsRef.current.findIndex((item) => timelineItemContainsMessage(item, scrollRequestMessageID));
    if (index < 0) return;
    let frame = 0;
    let attempts = 0;
    const align = () => {
      attempts += 1;
      if (nudgeMessageToTop(scrollRef.current, scrollRequestMessageID)) return;
      // 目标条目还没渲染出来（虚拟列表刚滚动完）：再等一帧
      if (attempts < 4) frame = window.requestAnimationFrame(align);
    };
    const timer = window.setTimeout(() => {
      virtualizer.scrollToIndex(index, { align: 'start' });
      frame = window.requestAnimationFrame(align);
    }, 40);
    return () => {
      window.clearTimeout(timer);
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [scrollRequestMessageID, scrollRequestNonce]);

  // 当前查找命中：先让虚拟列表把条目滚进来，带 data-find-active 的字渲染出来后再精确居中
  const activeFindMessageID = activeFindMatch?.messageID ?? null;
  const activeFindOccurrence = activeFindMatch?.occurrence ?? null;
  useActiveFindScroll(activeFindMessageID, activeFindOccurrence, scrollRef, (messageID) => {
    const index = itemsRef.current.findIndex((item) => timelineItemContainsMessage(item, messageID));
    if (index >= 0) virtualizer.scrollToIndex(index, { align: 'center' });
  });

  const renderEntry = (item: TimelineItem) => {
    const key = timelineItemKey(item);
    const highlighted = highlightedKey === key;
    const flashing = flashKey === key;
    const expanded =
      item.type === 'workRecord'
        ? workRecordExpanded(threadID, item.turnID, autoCollapse)
        : item.type === 'message'
        ? Boolean(expandedIds[item.message.id])
        : false;
    const copy = item.type === 'message' && copyState?.id === item.message.id ? copyState : null;
    // 恒用外层容器包裹：高亮切换只改 class，避免 TimelineEntry 重新挂载
    return (
      <div
        key={key}
        data-message-id={item.type === 'message' ? item.message.id : undefined}
        className={`min-w-0 rounded-2xl transition-shadow duration-300 ${
          highlighted ? 'ring-2 ring-[var(--primary)]' : ''
        } ${flashing ? 'push-target-flash' : ''}`}
      >
        <TimelineEntry
          activity={activity}
          threadID={threadID}
          item={item}
          expanded={expanded}
          copyState={copy}
          minuteTick={minuteTick}
          onToggleExpanded={onToggleExpanded}
          onToggleWorkRecord={toggleWorkRecord}
          segmentOpenState={segmentOpenState}
          onToggleToolSegment={onToggleToolSegment}
          autoCollapse={autoCollapse}
          onCopyMessage={onCopyMessage}
          canBranch={canBranch}
          onBranchMessage={onBranchMessage}
          messageMenu={messageMenu}
          recallMessageID={recallMessageID}
          onRecallTurn={onRecallTurn}
          readThreadMedia={readThreadMedia}
          swarmState={swarmState}
          onOpenSwarmBee={onOpenSwarmBee}
          onOpenSwarmBees={onOpenSwarmBees}
          swarmReportIndex={swarmReportIndex}
          highlighted={highlighted}
          onKillRunningCommand={onKillRunningCommand}
          findQuery={findQuery}
          findCaseSensitive={findCaseSensitive}
          findActiveMessageID={activeFindMessageID}
          findActiveOccurrence={activeFindOccurrence}
          lastMessageID={lastMessageID}
          latestActionMessage={item.type === 'message' && latestActionIDs.has(item.message.id)}
          swarmTruncatedReports={swarmTruncatedReports}
          isRunning={isRunning}
          onOpenFile={onOpenFile}
          onContinue={onContinue}
          decisionAnswers={decisionAnswers}
          onSendStewardMessage={onSendStewardMessage}
          onAnswerStewardDecision={onAnswerStewardDecision}
          onOpenThread={onOpenThread}
          resultTask={resultTaskForItem(item, stewardTasks)}
        />
      </div>
    );
  };

  const virtualItems = virtualizer.getVirtualItems();

  return (
    <div
      ref={listRef}
      style={{
        height: virtualizer.getTotalSize() + bottomPadding,
        position: 'relative',
        width: '100%'
      }}
    >
      <div
        style={{
          position: 'absolute',
          top: 0,
          left: 0,
          width: '100%',
          transform: `translateY(${(virtualItems[0]?.start ?? 0) - scrollMargin}px)`
        }}
        className="flex flex-col gap-3"
      >
        {virtualItems.map((virtualItem) => {
          if (virtualItem.index >= items.length) {
            return (
              <div
                key={virtualItem.key}
                data-index={virtualItem.index}
                data-item-key={String(virtualItem.key)}
                ref={virtualizer.measureElement}
              >
                <BackgroundTasksRow
                  activity={activity}
                  onActivityExpanded={onActivityExpanded}
                  threadID={threadID}
                  taskApi={backgroundTaskApi}
                  commands={backgroundCommands}
                  bees={runningBees}
                  waiting={!isRunning}
                  onOpenSwarmBee={onOpenSwarmBee}
                />
              </div>
            );
          }
          const item = items[virtualItem.index];
          if (!item) return null;
          return (
            <div
              key={virtualItem.key}
              data-index={virtualItem.index}
              data-item-key={String(virtualItem.key)}
              ref={virtualizer.measureElement}
            >
              {renderEntry(item)}
              <TurnTasks group={turnTasks.get(virtualItem.index)} onOpenThread={onOpenThread} />
            </div>
          );
        })}
      </div>
    </div>
  );
};

export const ThreadTimeline: React.FC<ThreadTimelineProps> = ({
  threadID,
  items,
  expandedIds,
  copyState,
  onToggleExpanded,
  onToggleWorkRecord,
  onCopyMessage,
  canBranch = false,
  onBranchMessage,
  recallMessageID,
  onRecallTurn,
  readThreadMedia,
  swarmState,
  onOpenSwarmBee,
  onOpenSwarmBees,
  messageMenu,
  onKillRunningCommand,
  focusMessageID,
  scrollToMessageRequest,
  scrollRef,
  findQuery,
  findCaseSensitive,
  activeFindMatch,
  isRunning,
  onContinue,
  bottomPadding = 0,
  isSteward = false,
  stewardTasks,
  onOpenFile,
  onSendStewardMessage,
  onAnswerStewardDecision,
  onOpenThread
}) => {
  const autoCollapse = useAutoCollapseReasoning();
  const backgroundTaskApi = useRemoteBackgroundTaskApi();
  const { toggleWorkRecord } = useWorkRecordHandlers(threadID, autoCollapse, onToggleWorkRecord);

  // 回合内各段（连续工具调用）的开合：按段内首条消息的稳定 key 记，流式更新时不会丢
  const [segmentOpenState, setSegmentOpenState] = React.useState<Record<string, boolean>>({});
  const toggleToolSegment = React.useCallback((key: string, defaultOpen: boolean) => {
    setSegmentOpenState((previous) => ({
      ...previous,
      [key]: !(previous[key] ?? defaultOpen)
    }));
  }, []);
  const itemsRef = React.useRef(items);
  React.useEffect(() => {
    itemsRef.current = items;
  });

  const [minuteTick, setMinuteTick] = React.useState(0);
  React.useEffect(() => {
    const timer = window.setInterval(() => setMinuteTick((tick) => tick + 1), 60_000);
    return () => window.clearInterval(timer);
  }, []);

  // 搜索命中的条目加高亮环，约 1.5 秒后自动消失
  const [highlightedKey, setHighlightedKey] = React.useState<string | null>(null);
  React.useEffect(() => {
    if (!focusMessageID) {
      setHighlightedKey(null);
      return;
    }
    const target = itemsRef.current.find((item) => timelineItemContainsMessage(item, focusMessageID));
    if (!target) return;
    const key = timelineItemKey(target);
    setHighlightedKey(key);
    const timer = window.setTimeout(() => {
      setHighlightedKey((current) => (current === key ? null : current));
    }, 1500);
    // 非虚拟分支没有 virtualizer：下一帧把目标条目滚到视口中间（虚拟分支由 scrollToIndex 负责）
    let frame = 0;
    if (!scrollRef) {
      frame = window.requestAnimationFrame(() => {
        const selector = `[data-timeline-key="${key.replace(/["\\]/g, '\\$&')}"]`;
        const element = document.querySelector<HTMLElement>(selector);
        element?.scrollIntoView({ block: 'center', behavior: 'smooth' });
      });
    }
    return () => {
      window.clearTimeout(timer);
      if (frame) window.cancelAnimationFrame(frame);
    };
  }, [focusMessageID]);

  // 推送点开的消息：滚到后闪一次底色（1.4s ease-out 淡出），生命周期与查找的 ring 高亮互不影响
  const [flashKey, setFlashKey] = React.useState<string | null>(null);
  React.useEffect(() => {
    const messageID = scrollToMessageRequest?.messageID ?? '';
    if (!scrollToMessageRequest?.flash || !messageID) {
      setFlashKey(null);
      return;
    }
    const target = itemsRef.current.find((item) => timelineItemContainsMessage(item, messageID));
    if (!target) return; // 目标消息还没加载：静默放弃
    const key = timelineItemKey(target);
    setFlashKey(key);
    const timer = window.setTimeout(() => {
      setFlashKey((current) => (current === key ? null : current));
    }, 1500);
    return () => window.clearTimeout(timer);
  }, [scrollToMessageRequest?.messageID, scrollToMessageRequest?.nonce, scrollToMessageRequest?.flash]);

  // 一次遍历把汇报配到发起胶囊上：只在这里算一次，胶囊自身不再扫时间线
  const swarmReportIndex = React.useMemo(() => buildSwarmReportIndex(items), [items]);
  const decisionAnswers = React.useMemo(
    () => latestDecisionAnswers(items.flatMap((item) => item.type === 'message' ? [item.message] : item.type === 'workRecord' ? item.entries : [])),
    [items]
  );
  // 汇报全并进发起胶囊的消息整行跳过，虚拟列表的 count 与索引都基于这份可见列表
  const visibleItems = React.useMemo(
    () => items.filter((item) =>
      item.type !== 'message' ||
      (item.message.kind !== 'steward_decision_answer' && !isMergedSwarmReportRow(item, swarmReportIndex))
    ),
    [items, swarmReportIndex]
  );
  const turnTasks = React.useMemo(
    () => isSteward ? stewardTasksByTurnEnd(visibleItems, stewardTasks ?? []) : new Map<number, StewardTurnTasks>(),
    [isSteward, visibleItems, stewardTasks]
  );
  // 时间线最后一条可见消息（已跳过隐藏消息与并进胶囊的汇报行）
  const lastMessageID = React.useMemo(() => lastVisibleMessageID(visibleItems), [visibleItems]);
  const latestActionIDs = React.useMemo(() => latestActionMessageIDs(visibleItems), [visibleItems]);
  // B7 后台任务：命令只算「回合已结束、命令仍在跑」，bee 只算运行中；都为 0 时聚合行不渲染
  const backgroundCommands = React.useMemo(
    () => collectBackgroundCommands(visibleItems, Boolean(isRunning)),
    [visibleItems, isRunning]
  );
  const [activityExpanded, setActivityExpanded] = React.useState(false);
  const hasRunningCommand = items.some((item) => {
    const entries = item.type === 'workRecord' ? item.entries : item.type === 'message' ? [item.message] : [];
    return entries.some((entry) => isToolCallEntry(entry) && entry.toolStatus === 'running' && classifyToolName(entry.toolName) === 'command' && !backgroundCommands.some((command) => command.id === entry.id));
  });
  const activity = useCommandActivity(threadID, hasRunningCommand || activityExpanded);
  const runningBees = React.useMemo(() => runningSwarmBees(swarmState), [swarmState]);
  // 被截断汇报的存档索引：用原始时间线算，并进胶囊的汇报行本身已被过滤掉了
  const swarmTruncatedReports = React.useMemo(() => buildTruncatedReportIndex(items), [items]);

  // 当前命中若落在长消息的预览窗口之外、回合工作记录折叠区或汇报胶囊里，先把那层壳展开，
  // 否则带 data-find-active 的字根本没有渲染出来，滚动也找不到它。
  const activeFindMessageID = activeFindMatch?.messageID ?? null;
  const activeFindOccurrence = activeFindMatch?.occurrence ?? null;
  const findQueryValue = (findQuery ?? '').trim();
  const findCaseSensitiveValue = Boolean(findCaseSensitive);
  const expandedForFindRef = React.useRef<string | null>(null);
  React.useEffect(() => {
    if (!activeFindMessageID) {
      expandedForFindRef.current = null;
      return;
    }
    // 同一条消息的不同命中可能落在不同位置：只有「已为这次命中展开过」才跳过
    const requestKey = `${activeFindMessageID}:${activeFindOccurrence ?? 0}`;
    if (expandedForFindRef.current === requestKey) return;
    const item = itemsRef.current.find((entry) => timelineItemContainsMessage(entry, activeFindMessageID));
    if (!item) return;
    if (item.type === 'message') {
      const message = item.message;
      if (
        messageNeedsFindExpand(
          message,
          findQueryValue,
          findCaseSensitiveValue,
          activeFindOccurrence ?? 0,
          Boolean(expandedIds[message.id])
        )
      ) {
        onToggleExpanded(message.id);
        expandedForFindRef.current = requestKey;
      }
      return;
    }
    if (item.type === 'workRecord') {
      const turnOpen = workRecordExpanded(threadID, item.turnID, autoCollapse);
      if (!turnOpen) toggleWorkRecord(item.turnID);
      // 回合展开了还不够：命中所在的那一段若还折着，也一并展开
      const segmentKey = toolSegmentKeyForMessage(item.entries, activeFindMessageID);
      if (segmentKey && !toolSegmentOpen(segmentOpenState, threadID, segmentKey, !autoCollapse)) {
        toggleToolSegment(toolSegmentOpenKey(threadID, segmentKey), !autoCollapse);
      }
      expandedForFindRef.current = requestKey;
    }
  }, [
    activeFindMessageID,
    activeFindOccurrence,
    findQueryValue,
    findCaseSensitiveValue,
    expandedIds,
    threadID,
    autoCollapse,
    segmentOpenState,
    toggleToolSegment,
    onToggleExpanded,
    toggleWorkRecord
  ]);

  // 非虚拟分支没有 virtualizer：滚动定位只靠 scrollIntoView
  useActiveFindScroll(activeFindMessageID, activeFindOccurrence, scrollRef, null);

  // 提问导航轨的跳转：非虚拟分支按 DOM 直接定位到消息顶部（虚拟分支由 VirtualTimeline 处理，避免两套滚动互相抢）
  const scrollRequestMessageID = scrollToMessageRequest?.messageID ?? null;
  const scrollRequestNonce = scrollToMessageRequest?.nonce ?? 0;
  React.useEffect(() => {
    if (!scrollRequestMessageID || scrollRef) return;
    const timer = window.setTimeout(() => {
      const escaped = scrollRequestMessageID.replace(/["\\]/g, '\\$&');
      const element = document.querySelector<HTMLElement>(`[data-message-id="${escaped}"]`);
      element?.scrollIntoView({ block: 'start', behavior: 'smooth' });
    }, 40);
    return () => window.clearTimeout(timer);
  }, [scrollRequestMessageID, scrollRequestNonce, scrollRef]);

  if (scrollRef) {
    return (
      <VirtualTimeline
        threadID={threadID}
        items={visibleItems}
        expandedIds={expandedIds}
        copyState={copyState}
        onToggleExpanded={onToggleExpanded}
        onToggleWorkRecord={onToggleWorkRecord}
        segmentOpenState={segmentOpenState}
        onToggleToolSegment={toggleToolSegment}
        onCopyMessage={onCopyMessage}
        canBranch={canBranch}
        onBranchMessage={onBranchMessage}
        messageMenu={messageMenu}
        recallMessageID={recallMessageID}
        onRecallTurn={onRecallTurn}
        readThreadMedia={readThreadMedia}
        swarmState={swarmState}
        onOpenSwarmBee={onOpenSwarmBee}
        onOpenSwarmBees={onOpenSwarmBees}
        swarmReportIndex={swarmReportIndex}
        onKillRunningCommand={onKillRunningCommand}
        focusMessageID={focusMessageID}
        scrollToMessageRequest={scrollToMessageRequest}
        highlightedKey={highlightedKey}
        flashKey={flashKey}
        swarmTruncatedReports={swarmTruncatedReports}
        scrollRef={scrollRef}
        findQuery={findQuery}
        findCaseSensitive={findCaseSensitive}
        activeFindMatch={activeFindMatch}
        isRunning={isRunning}
        onContinue={onContinue}
        onOpenFile={onOpenFile}
        bottomPadding={bottomPadding}
        isSteward={isSteward}
        stewardTasks={isSteward ? stewardTasks : undefined}
        turnTasks={turnTasks}
        activity={activity}
        onActivityExpanded={setActivityExpanded}
        backgroundCommands={backgroundCommands}
        runningBees={runningBees}
          decisionAnswers={decisionAnswers}
          onSendStewardMessage={onSendStewardMessage}
          onAnswerStewardDecision={onAnswerStewardDecision}
          onOpenThread={onOpenThread}
      />
    );
  }

  const renderEntry = (item: TimelineItem) => {
    const key = timelineItemKey(item);
    const highlighted = highlightedKey === key;
    const expanded =
      item.type === 'workRecord'
        ? workRecordExpanded(threadID, item.turnID, autoCollapse)
        : item.type === 'message'
        ? Boolean(expandedIds[item.message.id])
        : false;
    const copy = item.type === 'message' && copyState?.id === item.message.id ? copyState : null;
    // 恒用外层容器包裹：高亮切换只改 class，避免 TimelineEntry 重新挂载
    return (
      <div
        key={key}
        data-timeline-key={key}
        data-message-id={item.type === 'message' ? item.message.id : undefined}
        className={`min-w-0 rounded-2xl transition-shadow duration-300 ${
          highlighted ? 'ring-2 ring-[var(--primary)]' : ''
        } ${flashKey === key ? 'push-target-flash' : ''}`}
      >
        <TimelineEntry
          activity={activity}
          threadID={threadID}
          item={item}
          expanded={expanded}
          copyState={copy}
          minuteTick={minuteTick}
          onToggleExpanded={onToggleExpanded}
          onToggleWorkRecord={toggleWorkRecord}
          segmentOpenState={segmentOpenState}
          onToggleToolSegment={toggleToolSegment}
          autoCollapse={autoCollapse}
          onCopyMessage={onCopyMessage}
          canBranch={canBranch}
          onBranchMessage={onBranchMessage}
          messageMenu={messageMenu}
          recallMessageID={recallMessageID}
          onRecallTurn={onRecallTurn}
          readThreadMedia={readThreadMedia}
          swarmState={swarmState}
          onOpenSwarmBee={onOpenSwarmBee}
          onOpenSwarmBees={onOpenSwarmBees}
          swarmReportIndex={swarmReportIndex}
          highlighted={highlighted}
          onKillRunningCommand={onKillRunningCommand}
          findQuery={findQuery}
          findCaseSensitive={findCaseSensitive}
          findActiveMessageID={activeFindMessageID}
          findActiveOccurrence={activeFindOccurrence}
          lastMessageID={lastMessageID}
          latestActionMessage={item.type === 'message' && latestActionIDs.has(item.message.id)}
          swarmTruncatedReports={swarmTruncatedReports}
          isRunning={isRunning}
          onOpenFile={onOpenFile}
          onContinue={onContinue}
           decisionAnswers={decisionAnswers}
           onSendStewardMessage={onSendStewardMessage}
           onAnswerStewardDecision={onAnswerStewardDecision}
           onOpenThread={onOpenThread}
          resultTask={resultTaskForItem(item, isSteward ? stewardTasks : undefined)}
        />
      </div>
    );
  };

  return (
    <>
      {visibleItems.map((item, index) => <React.Fragment key={timelineItemKey(item)}>{renderEntry(item)}<TurnTasks group={turnTasks.get(index)} onOpenThread={onOpenThread} /></React.Fragment>)}
      <BackgroundTasksRow
        activity={activity}
        onActivityExpanded={setActivityExpanded}
        threadID={threadID}
        taskApi={backgroundTaskApi}
        commands={backgroundCommands}
        bees={runningBees}
        waiting={!isRunning}
        onOpenSwarmBee={onOpenSwarmBee}
      />
      {/* 发送后锚定期间垫出的一屏空白：锚点消息靠它停在视口顶部，解除时归零 */}
      {bottomPadding > 0 && <div aria-hidden="true" style={{ height: bottomPadding }} />}
    </>
  );
};
