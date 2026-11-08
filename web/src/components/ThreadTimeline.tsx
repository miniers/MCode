import React from 'react';
import { useVirtualizer } from '@tanstack/react-virtual';
import type {
  RemoteThreadHistoryPage,
  RemoteThreadMessage
} from '../types/contract';
import {
  CheckCircleIcon,
  ChevronRightIcon,
  CircleIcon,
  ExclamationTriangleIcon,
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
import type { SwarmEntryParsed, SwarmReportSection } from '../utils/markdown';
import type { RemoteSwarmBee, RemoteSwarmState } from '../types/swarm';
import { MarkdownView, type MarkdownHighlight } from './MarkdownView';
import { FIND_ACTIVE_SELECTOR, highlightRanges, type FindMatch } from '../utils/findHighlight';
import { SwarmDispatchCapsules, SwarmReportCapsules } from './SwarmCapsules';
import type { SwarmDispatchReportIndex } from './SwarmCapsules';
import { dispatchEntryKey, matchCandidate, reportSectionKey } from './SwarmCapsules';
import { RunningGlyph } from './timeline/RunningGlyph';
import { ToolEntryDiff, isFileEditToolName, parseToolEditDiff, summarizeToolDiff } from './timeline/ToolEntryDiff';
import { TurnFinishedBar, type TurnFinishedStats } from './timeline/TurnFinishedBar';
import { TurnFilesCard, collectTurnEditedFiles, type TurnEditedFile } from './timeline/TurnFilesCard';
import {
  BackgroundTasksRow,
  commandSnippet,
  type BackgroundTaskCommand
} from './timeline/BackgroundTasksRow';
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

export const COLLAPSE_THRESHOLD = 4000;
export const COLLAPSE_PREVIEW_LENGTH = 4000;
// 工作记录展开后的条目是平铺行，靠分隔线区分，不再各自套一张卡片。
export const WORK_ENTRY_ROW_CLASS = 'px-1 py-2 space-y-1 text-xs text-[var(--foreground)]';

export type MessageKind =
  | 'user'
  | 'assistant'
  | 'tool'
  | 'thinking'
  | 'info'
  | 'warning'
  | 'error'
  | 'plan'
  | 'todo'
  | 'question_answer'
  | 'swarm_report'
  | 'swarm_task'
  | 'host_event'
  | 'steward_wake'
  | 'steward_reply'
  | 'steward_decision'
  | 'steward_decision_answer'
  | 'steward_result';

export type TurnStatus = 'running' | 'completed' | 'interrupted' | 'failed';

export type ThreadMessage = Omit<RemoteThreadMessage, 'kind'> & {
  turnID?: string;
  kind?: MessageKind;
  turnStatus?: TurnStatus;
  turnStartedAt?: string;
  turnCompletedAt?: string;
  isFinalResponse?: boolean;
};

/** 复制变体：纯文本 / 原始 Markdown / 最后一个代码块 */
export type CopyVariant = 'plain' | 'markdown' | 'lastCode';

export type CopyState = { id: string; status: 'copied' | 'error'; message?: string } | null;

/** 一个回合的工作记录（过程条目）分组，单独命名以便静默回合条目引用。 */
export interface WorkRecordItem {
  type: 'workRecord';
  turnID: string;
  turnIndex: number;
  turnStatus?: TurnStatus;
  turnStartedAt?: string;
  turnCompletedAt?: string;
  entries: ThreadMessage[];
}

export type TimelineItem =
  | {
      type: 'message';
      message: ThreadMessage;
    }
  | WorkRecordItem
  | {
      type: 'turnFinished';
      turnID: string;
      turnIndex: number;
      stats: TurnFinishedStats;
      /** 本回合改动的文件：渲染在统计条下方，空数组时不渲染卡片 */
      files: TurnEditedFile[];
    };

/** 条目在虚拟列表与高亮定位里使用的稳定 key。 */
function timelineItemKey(item: TimelineItem): string {
  switch (item.type) {
    case 'workRecord':
      return `work-${item.turnID}`;
    case 'turnFinished':
      return `turn-finished-${item.turnID}`;
    default:
      return item.message.id;
  }
}

/** 条目里是否包含某条消息：普通消息条目、工作记录分组内的过程条目，或静默回合内任一记录。 */
function timelineItemContainsMessage(item: TimelineItem, messageID: string): boolean {
  if (item.type === 'message') return item.message.id === messageID;
  if (item.type === 'workRecord') return item.entries.some((entry) => entry.id === messageID);
  return false;
}

/** 派单只可能出现在带围栏的助手消息里：先用一条正则跳过绝大多数正文，再交给 splitSwarmFence 解析 */
const SWARM_FENCE_HINT = /`{3,}[ \t]*(?:swarm|hydra)/i;

/** 时间线上的一个派单条目：itemIndex 用来只在汇报之前找发起胶囊 */
interface DispatchRef extends SwarmEntryParsed {
  key: string;
  itemIndex: number;
}

/** 时间线上的一段子代理汇报 */
interface ReportRef {
  key: string;
  report: SwarmReportSection;
  itemIndex: number;
}

/**
 * 把时间线里的子代理汇报段配到对应的发起胶囊上：同一个 bee 只留发起胶囊一行，汇报正文并进它的展开区。
 * 派单一定出现在汇报之前，所以只在汇报之前、取时间线上最近的一条；配不上的汇报不标记消耗，
 * 调用方继续按原来的方式单独渲染，信息不丢。
 */
function buildSwarmReportIndex(items: TimelineItem[]): SwarmDispatchReportIndex {
  const dispatches: DispatchRef[] = [];
  const reports: ReportRef[] = [];

  items.forEach((item, itemIndex) => {
    if (item.type !== 'message') return;
    const message = item.message;
    const text = message.text || '';
    // 宿主转来的事件虽然以 user 角色落库，但不是用户在说话，不参与子代理汇报配对。
    if (message.kind === 'host_event') return;
    if (message.kind === 'user' || message.role === 'user') {
      const sections = parseSwarmReport(text);
      if (!sections) return;
      sections.forEach((report, index) => {
        reports.push({ key: reportSectionKey(message.id, index), report, itemIndex });
      });
      return;
    }
    const isAssistant = message.kind === 'assistant' || (!message.kind && message.role === 'assistant');
    if (!isAssistant || !SWARM_FENCE_HINT.test(text)) return;
    splitSwarmFence(text).entries.forEach((entry, index) => {
      dispatches.push({ ...entry, key: dispatchEntryKey(message.id, index), itemIndex });
    });
  });

  const reportsByEntry: Record<string, SwarmReportSection[]> = {};
  const consumedReports: Record<string, boolean> = {};
  if (dispatches.length === 0 || reports.length === 0) return { reportsByEntry, consumedReports };

  const paired = new Set<string>();
  const claimed = new Set<string>();
  const attach = (report: ReportRef, entry: DispatchRef) => {
    paired.add(report.key);
    claimed.add(entry.key);
    consumedReports[report.key] = true;
    const bucket = reportsByEntry[entry.key];
    if (bucket) bucket.push(report.report);
    else reportsByEntry[entry.key] = [report.report];
  };
  /** 汇报之前、时间线上最近的候选 */
  const nearestBefore = (candidates: DispatchRef[], position: number): DispatchRef | null => {
    let best: DispatchRef | null = null;
    for (const candidate of candidates) {
      if (candidate.itemIndex >= position) continue;
      if (!best || candidate.itemIndex > best.itemIndex) best = candidate;
    }
    return best;
  };

  // 一：任务文本完全一致（汇报标题里的任务就来自派单），且还没配过
  for (const report of reports) {
    const task = report.report.task.trim();
    if (!task) continue;
    const entry = nearestBefore(
      dispatches.filter((candidate) => !claimed.has(candidate.key) && candidate.task.trim() === task),
      report.itemIndex
    );
    if (entry) attach(report, entry);
  }

  // 二：同一个 bee 先后报过「运行中」「已完成」：任务一致时并回已配过的那枚发起胶囊
  for (const report of reports) {
    if (paired.has(report.key)) continue;
    const task = report.report.task.trim();
    if (!task) continue;
    const entry = nearestBefore(
      dispatches.filter((candidate) => candidate.task.trim() === task),
      report.itemIndex
    );
    if (entry) attach(report, entry);
  }

  // 三：任务文本有出入时退回名字/包含匹配（与胶囊找 bee 同一套规则），仍取汇报之前最近的一条
  for (const report of reports) {
    if (paired.has(report.key)) continue;
    let chosen: DispatchRef | null = null;
    for (const candidate of dispatches) {
      if (candidate.itemIndex >= report.itemIndex || claimed.has(candidate.key)) continue;
      if (!matchCandidate(report.report, [candidate])) continue;
      if (!chosen || candidate.itemIndex > chosen.itemIndex) chosen = candidate;
    }
    if (chosen) attach(report, chosen);
  }

  return { reportsByEntry, consumedReports };
}

/** 被截断汇报在索引里的 key：并进发起胶囊后只剩 SwarmReportSection，靠名字与任务找回它的 bee ID */
function truncatedReportKey(report: SwarmReportSection): string {
  return `${report.name.trim()}\u0000${report.task.trim()}`;
}

/**
 * 被截断的汇报才在正文里带 bee ID：按消息把它抽出来建索引，
 * 并进发起胶囊的汇报不再占自己的消息行，只能靠名字与任务找回全文入口。
 */
function buildTruncatedReportIndex(items: TimelineItem[]): Record<string, SwarmReportFullItem> {
  const index: Record<string, SwarmReportFullItem> = {};
  for (const item of items) {
    if (item.type !== 'message') continue;
    const message = item.message;
    if (message.kind === 'host_event') continue;
    const text = message.text || '';
    const sections = parseSwarmReport(text);
    if (!sections) continue;
    const fields = parseSwarmReportArchiveFields(text);
    sections.forEach((report, sectionIndex) => {
      const archive = fields[sectionIndex];
      if (!archive?.truncated || !archive.beeID) return;
      index[truncatedReportKey(report)] = { report, beeID: archive.beeID, truncated: true };
    });
  }
  return index;
}

/** 一条助手消息派出去的 bee 里、汇报被截断的那些：胶囊里只有开头一段，这里给「展开全文」 */
function truncatedReportsOfDispatch(
  messageID: string,
  entries: SwarmEntryParsed[],
  reportIndex: SwarmDispatchReportIndex | undefined,
  truncated: Record<string, SwarmReportFullItem> | undefined
): SwarmReportFullItem[] {
  if (!reportIndex || !truncated) return [];
  const result: SwarmReportFullItem[] = [];
  entries.forEach((_entry, entryIndex) => {
    const reports = reportIndex.reportsByEntry[dispatchEntryKey(messageID, entryIndex)] ?? [];
    for (const report of reports) {
      const full = truncated[truncatedReportKey(report)];
      if (full && !result.some((item) => item.beeID === full.beeID)) result.push(full);
    }
  });
  return result;
}

/**
 * 这条消息是不是只剩已并入发起胶囊的汇报段：这样的行整行跳过，
 * 否则外层包裹 div 仍在，父容器的 gap 会留下一段空白。
 */
function isMergedSwarmReportRow(item: TimelineItem, index?: SwarmDispatchReportIndex): boolean {
  if (item.type !== 'message') return false;
  const message = item.message;
  if (message.kind === 'host_event') return false;
  if (message.kind !== 'user' && message.role !== 'user') return false;
  const reports = parseSwarmReport(message.text || '');
  if (!reports) return false;
  return reports.every((_report, reportIndex) =>
    Boolean(index?.consumedReports[reportSectionKey(message.id, reportIndex)])
  );
}

export const workRecordExpandedMap = new Map<string, boolean>();

/** 总管被唤醒却没有需要用户知道的事时，整轮回复的唯一正文标记（与桌面端 stewardSilentMarker 一致）。 */
export const STEWARD_SILENT_MARKER = '[[静默]]';

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
export function isHiddenTimelineMessage(message: ThreadMessage): boolean {
  if (message.role === 'user') return message.kind === 'steward_wake';
  if (message.role !== 'assistant') return false;
  const text = (message.text ?? '').trim();
  if (text === STEWARD_SILENT_MARKER) return true;
  // 流式输出时正文可能只是标记的前缀（如 “[[静”）：先不显示，避免闪现半截标记。
  // Web 消息没有 isFinal 字段，用「回合仍在运行且不是最终答复」代表尚未定稿。
  const streaming = !message.isFinalResponse && message.turnStatus === 'running';
  return streaming && text.length > 0 && STEWARD_SILENT_MARKER.startsWith(text);
}

function isAssistantMessage(message: ThreadMessage): boolean {
  if (message.kind === 'assistant' || (!message.kind && message.role === 'assistant')) return true;
  return message.role === 'assistant' && (
    message.kind === 'steward_reply' ||
    message.kind === 'steward_decision' ||
    message.kind === 'steward_result'
  );
}

/** 是否是思考行（与桌面端 isReasoningMessage/countToolGroupCategories 的口径一致）。 */
export function isThinkingEntry(message: ThreadMessage): boolean {
  return message.kind === 'thinking' || (!message.kind && message.role === 'thinking');
}

// 工具名归类表，对齐桌面端 internal/ui/timeline_rows.go 的 readToolNames / searchToolNames / commandToolNames
export const READ_TOOL_NAMES = new Set([
  'read',
  'readfile',
  'fileread',
  'readfiles',
  'readtextfile',
  'readmultiplefiles',
  'readfilecontent',
  'getfilecontents',
  'getfilecontent',
  'view',
  'viewfile',
  'openfile',
  'cat'
]);

export const SEARCH_TOOL_NAMES = new Set([
  'grep',
  'rg',
  'ripgrep',
  'grepsearch',
  'search',
  'searchfiles',
  'filesearch',
  'find',
  'findfiles',
  'glob',
  'codebasesearch'
]);

export const COMMAND_TOOL_NAMES = new Set([
  'bash',
  'shell',
  'execcommand',
  'command',
  'runcommand',
  'terminal',
  'localshell',
  'exec',
  'execute'
]);

/** 规整工具名：小写、去掉 MCP 前缀与命名空间、去掉 _ 与 -（对齐桌面端 toolNameKey）。 */
export function normalizeToolNameKey(name?: string): string {
  let key = (name ?? '').trim().toLowerCase();
  const scope = key.lastIndexOf('__');
  if (scope !== -1) key = key.slice(scope + 2);
  let cut = -1;
  for (const separator of ['.', '/', ':']) {
    const index = key.lastIndexOf(separator);
    if (index > cut) cut = index;
  }
  if (cut !== -1) key = key.slice(cut + 1);
  return key.replace(/_/g, '').replace(/-/g, '');
}

export type WorkToolCategory = 'command' | 'edit' | 'read' | 'search' | 'other';

/** 按工具名归类，供回合推进组头的分类汇总使用（对齐桌面端 classifyToolCategory）。 */
export function classifyToolName(name?: string): WorkToolCategory {
  const key = normalizeToolNameKey(name);
  if (!key) return 'other';
  if (COMMAND_TOOL_NAMES.has(key)) return 'command';
  if (isFileEditToolName(name)) return 'edit';
  if (READ_TOOL_NAMES.has(key)) return 'read';
  if (SEARCH_TOOL_NAMES.has(key)) return 'search';
  return 'other';
}

/** 回合内过程条目的分类计数，字段与桌面端 toolGroupCounts 一一对应。 */
export interface WorkCategoryCounts {
  thoughts: number;
  commands: number;
  edits: number;
  reads: number;
  searches: number;
  others: number;
  total: number;
}

/** 统计一组过程条目（含思考行）里各类别的出现次数。 */
export function countWorkCategories(entries: ThreadMessage[]): WorkCategoryCounts {
  const counts: WorkCategoryCounts = {
    thoughts: 0,
    commands: 0,
    edits: 0,
    reads: 0,
    searches: 0,
    others: 0,
    total: 0
  };
  for (const entry of entries) {
    counts.total += 1;
    if (isThinkingEntry(entry)) {
      counts.thoughts += 1;
      continue;
    }
    switch (classifyToolName(entry.toolName)) {
      case 'command':
        counts.commands += 1;
        break;
      case 'edit':
        counts.edits += 1;
        break;
      case 'read':
        counts.reads += 1;
        break;
      case 'search':
        counts.searches += 1;
        break;
      default:
        counts.others += 1;
    }
  }
  return counts;
}

/** 把分类计数拼成中文摘要，计数为 0 的类别省略（对齐桌面端 toolGroupSummaryText）。 */
export function workSummaryText(counts: WorkCategoryCounts): string {
  const parts: string[] = [];
  if (counts.thoughts > 0) parts.push(`思考 ${counts.thoughts} 次`);
  if (counts.commands > 0) parts.push(`运行 ${counts.commands} 条命令`);
  if (counts.edits > 0) parts.push(`编辑 ${counts.edits} 个文件`);
  if (counts.reads > 0) parts.push(`读取 ${counts.reads} 个文件`);
  if (counts.searches > 0) parts.push(`搜索 ${counts.searches} 次`);
  if (counts.others > 0) parts.push(`调用 ${counts.others} 个工具`);
  if (parts.length === 0) return `已运行 ${counts.total} 个工具`;
  return parts.join(' · ');
}

/** 耗时短文本：10 秒内保留一位小数，不足 1 分钟取整秒，超过 1 分钟拆成分秒（对齐桌面端 formatShortDuration）。 */
export function formatShortDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs < 0) return '';
  if (durationMs < 60_000) {
    const seconds = durationMs / 1000;
    if (seconds < 10) return `${seconds.toFixed(1)}s`;
    return `${Math.round(seconds)}s`;
  }
  const minutes = Math.floor(durationMs / 60_000);
  const seconds = Math.floor((durationMs % 60_000) / 1000);
  if (seconds === 0) return `${minutes}m`;
  return `${minutes}m${seconds}s`;
}

/** 组内条目耗时：第一条的创建时间到最后一条的完成（缺完成时间时用创建）时间，单位毫秒。 */
export function workElapsedMs(entries: ThreadMessage[]): number {
  if (entries.length === 0) return 0;
  const start = Date.parse(entries[0].createdAt);
  const last = entries[entries.length - 1];
  const end = Date.parse(last.toolCompletedAt ?? last.createdAt);
  if (Number.isNaN(start) || Number.isNaN(end) || end <= start) return 0;
  return end - start;
}

/** 回合耗时（整数秒）：不足 1 分钟显示 16s，超过 1 分钟拆成分秒（对齐桌面端 formatTurnElapsedSeconds）。 */
export function formatTurnElapsedSeconds(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs < 0) return '';
  const totalSeconds = Math.floor(durationMs / 1000);
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return seconds === 0 ? `${minutes}m` : `${minutes}m${seconds}s`;
}

/** 运行中回合的已处理耗时：第一条工作条目的创建时间到现在；起始时间不可解析时返回 null。 */
function runningWorkElapsedMs(entries: ThreadMessage[], nowMs: number): number | null {
  const first = entries[0];
  if (!first) return null;
  const start = Date.parse(first.createdAt);
  if (Number.isNaN(start)) return null;
  const end = Number.isFinite(nowMs) ? nowMs : Date.now();
  return Math.max(0, end - start);
}

/**
 * 回合工作记录组头：「已处理 16s」（运行中）/「用时 42s」（结束后）。
 * 没有可显示的耗时就不写数字；计数与状态放进 Tooltip 和展开体尾部。
 */
export function turnWorkHeaderLabel(
  entries: ThreadMessage[],
  running: boolean,
  nowMs: number = Date.now(),
  turnStartedAt?: string,
  turnCompletedAt?: string
): string {
  const start = Date.parse(turnStartedAt ?? '');
  const end = running ? nowMs : Date.parse(turnCompletedAt ?? '');
  const elapsedMs = Number.isFinite(start) && start > 0 && Number.isFinite(end) && end >= start
    ? end - start
    : running ? runningWorkElapsedMs(entries, nowMs) : workElapsedMs(entries);
  const elapsed = elapsedMs === null || elapsedMs <= 0 ? '' : formatTurnElapsedSeconds(elapsedMs);
  if (running) return elapsed === '' ? '已处理' : `已处理 ${elapsed}`;
  return elapsed === '' ? '用时' : `用时 ${elapsed}`;
}

/** 组内仍在运行的工具条目（不看类别：只要还在跑就在组头提示）。 */
function runningToolEntries(entries: ThreadMessage[]): ThreadMessage[] {
  // 只认 running：background 是 provider 托管的后台任务，已脱离本轮，组头不再等它结束，
  // 把它们算进来会让「工作中 · 正在运行 …」一直挂着。
  return entries.filter((entry) => isToolCallEntry(entry) && entry.toolStatus === 'running');
}

/**
 * 回合内一段活动条目的组头摘要：「用时 3.2s · 思考 1 次 · 运行 2 条命令」；
 * 运行中的段前置「工作中 · 」，并在末尾实时追加「· 正在运行 <命令片段>」，多条时给「等 N 条」。
 */
export function toolSegmentHeaderText(entries: ThreadMessage[], running: boolean): string {
  const summary = workSummaryText(countWorkCategories(entries));
  if (running) {
    const runningTools = runningToolEntries(entries);
    const base = `工作中 · ${summary}`;
    const last = runningTools[runningTools.length - 1];
    if (!last) return base;
    const snippet = commandSnippet(last.toolName, last.toolInput);
    if (!snippet) return base;
    const suffix = runningTools.length > 1 ? ` 等 ${runningTools.length} 条` : '';
    return `${base} · 正在运行 ${snippet}${suffix}`;
  }
  const elapsedMs = workElapsedMs(entries);
  if (elapsedMs >= 1000) return `用时 ${formatShortDuration(elapsedMs)} · ${summary}`;
  return summary;
}

/** 回合是否已经结束；没有 turnStatus 的老消息按「会话不在跑」兜底（对齐桌面端 B7 口径）。 */
function turnWorkEnded(status: TurnStatus | undefined, isRunning: boolean): boolean {
  if (status === undefined) return !isRunning;
  return isEndedStatus(status);
}

/**
 * B7 后台命令：回合已结束、命令仍在跑的命令类工具。当前回合还在跑的命令归 A9 的组头，不在这里重复。
 * provider 托管的后台任务（toolStatus='background'）也列进来，且不受回合是否结束限制（它们本就跨回合存活）；
 * 但只读：宿主拿不到输出、也不能单独中止，所以不提供停止入口。
 */
export function collectBackgroundCommands(
  items: readonly TimelineItem[],
  isRunning: boolean
): BackgroundTaskCommand[] {
  const commands: BackgroundTaskCommand[] = [];
  const seen = new Set<string>();
  const push = (entry: ThreadMessage, turnEnded: boolean) => {
    if (!isToolCallEntry(entry)) return;
    if (classifyToolName(entry.toolName) !== 'command') return;
    // background 由 provider 托管：跨回合存活，只读列出；running 仍要求回合已结束
    if (entry.toolStatus === 'background') {
      if (seen.has(entry.id)) return;
      seen.add(entry.id);
      commands.push({
        id: entry.id,
        snippet: commandSnippet(entry.toolName, entry.toolInput),
        status: 'background',
        startedAt: entry.createdAt
      });
      return;
    }
    if (entry.toolStatus !== 'running' || !turnEnded) return;
    if (seen.has(entry.id)) return;
    seen.add(entry.id);
    commands.push({
      id: entry.id,
      snippet: commandSnippet(entry.toolName, entry.toolInput),
      status: 'running'
    });
  };
  for (const item of items) {
    if (item.type === 'workRecord') {
      const ended = turnWorkEnded(item.turnStatus, isRunning);
      item.entries.forEach((entry) => push(entry, ended));
      continue;
    }
    if (item.type === 'message') {
      push(item.message, turnWorkEnded(item.message.turnStatus, isRunning));
    }
  }
  return commands;
}

/** B7 子智能体：只算运行中的 bee，排队与已结束都不计。 */
export function runningSwarmBees(state?: RemoteSwarmState | null): RemoteSwarmBee[] {
  return (state?.bees ?? []).filter((bee) => bee.status === 'running');
}

/** 组内是否有失败的工具调用（blocked 不算失败）。 */
export function workRecordHasFailure(entries: ThreadMessage[]): boolean {
  return entries.some((entry) => isFailedToolEntry(entry));
}

export function isWorkEntry(message: ThreadMessage): boolean {
  if (message.kind) {
    return message.kind === 'tool' || message.kind === 'thinking' || message.kind === 'info';
  }
  return message.role === 'tool';
}

export function isEndedStatus(status?: string | null): boolean {
  return status === 'completed' || status === 'interrupted' || status === 'failed';
}

// 与桌面端 isToolBlockedByHook 一致：显式 blocked，或工具输出就是 hook 报错
function isBlockedToolEntry(entry: ThreadMessage): boolean {
  if (entry.toolStatus === 'blocked') return true;
  const text = entry.text || '';
  const firstLine = text.split('\n').find((line) => line.trim().length > 0)?.trim() ?? '';
  return firstLine.startsWith('PreToolUse:') && firstLine.includes(' hook error:');
}

// 与桌面端 isToolFailed 一致：blocked 不算失败
function isFailedToolEntry(entry: ThreadMessage): boolean {
  return entry.toolStatus === 'failed' && !isBlockedToolEntry(entry);
}

export function mergeThreadMessages(previous: ThreadMessage[], incoming: ThreadMessage[]): ThreadMessage[] {
  const byID = new Map<string, ThreadMessage>();
  for (const message of previous) byID.set(message.id, message);
  for (const message of incoming) {
    const old = byID.get(message.id);
    if (old && old.turnID && old.turnID === message.turnID) {
      byID.set(message.id, {
        ...message,
        turnStatus: isEndedStatus(old.turnStatus) && !isEndedStatus(message.turnStatus) ? old.turnStatus : (message.turnStatus ?? old.turnStatus),
        turnStartedAt: message.turnStartedAt ?? old.turnStartedAt,
        turnCompletedAt: old.turnCompletedAt ?? message.turnCompletedAt
      });
    } else byID.set(message.id, message);
  }

  return Array.from(byID.values()).sort((left, right) => {
    // 宿主时间戳只到秒，同一秒内的消息以宿主给的历史位置 seq 为准
    if (left.seq !== undefined && right.seq !== undefined && left.seq !== right.seq) return left.seq - right.seq;
    const createdAtOrder = left.createdAt.localeCompare(right.createdAt);
    if (createdAtOrder !== 0) return createdAtOrder;
    if (left.turnIndex !== right.turnIndex) return left.turnIndex - right.turnIndex;
    return left.id.localeCompare(right.id);
  });
}

export function mergeLatestHistoryPage(
  previous: ThreadMessage[],
  page: RemoteThreadHistoryPage,
  protectedMessageIDs: Set<string>
): {
  messages: ThreadMessage[];
  gap: boolean;
  shouldUpdateCursor: boolean;
} {
  const incoming = (Array.isArray(page.messages) ? page.messages : []) as ThreadMessage[];
  const incomingIDs = new Set(incoming.map((message) => message.id));
  const firstDate = incoming[0]?.createdAt;
  const retained = previous.filter((message) =>
    protectedMessageIDs.has(message.id) ||
    (page.hasMore && firstDate && message.createdAt <= firstDate && !incomingIDs.has(message.id))
  );
  const gap = page.hasMore && previous.length > 0 && incoming.length > 0 &&
    !previous.some((message) => incomingIDs.has(message.id));
  const usableIncoming = mergeThreadMessages(previous, incoming)
    .filter((message) => incomingIDs.has(message.id) && !protectedMessageIDs.has(message.id));
  return {
    messages: mergeThreadMessages(retained, usableIncoming),
    gap,
    shouldUpdateCursor: !page.hasMore || previous.length === 0
  };
}

export function buildTimelineItems(
  messages: ThreadMessage[],
  _options?: { isSteward?: boolean }
): TimelineItem[] {
  const finalAssistantTexts = new Set(
    messages
      .filter((message) =>
        message.isFinalResponse &&
        isAssistantMessage(message) &&
        message.text.trim().length > 0
      )
      .map((message) => `${message.turnID ?? message.turnIndex}:${message.text.trim()}`)
  );
  // 总管唤醒这类系统输入与「[[静默]]」轮次在构建层就丢掉：不能靠渲染返回空元素，否则外层容器会留下空白。
  const visibleMessages = messages
    .filter((message) => !isHiddenTimelineMessage(message))
    .filter((message) => {
      const isAssistant = isAssistantMessage(message);
      if (!isAssistant || message.isFinalResponse || message.text.trim().length === 0) return true;
      return !finalAssistantTexts.has(`${message.turnID ?? message.turnIndex}:${message.text.trim()}`);
    });
  const workEntriesByTurn = new Map<string, ThreadMessage[]>();
  const turnStatusByTurn = new Map<string, TurnStatus>();
  // 回合统计：首条消息到最后一次活动（含工具完成时间）的跨度，以及工具调用与失败次数
  const turnSpanByTurn = new Map<
    string,
    { start: number; end: number; toolCount: number; failedCount: number }
  >();

  const turnTimes = new Map<string, { start?: string; end?: string }>();
  for (const message of messages) {
    if (!message.turnID) continue;
    const times = turnTimes.get(message.turnID) ?? {};
    const start = Date.parse(message.turnStartedAt ?? '');
    const end = Date.parse(message.turnCompletedAt ?? '');
    if (Number.isFinite(start) && start > 0 && (!times.start || start < Date.parse(times.start))) times.start = message.turnStartedAt;
    if (Number.isFinite(end) && end > 0 && (!times.end || end < Date.parse(times.end))) times.end = message.turnCompletedAt;
    if (message.turnStatus && (isEndedStatus(message.turnStatus) || !isEndedStatus(turnStatusByTurn.get(message.turnID)))) {
      turnStatusByTurn.set(message.turnID, message.turnStatus);
    }
    if (times.end && !isEndedStatus(turnStatusByTurn.get(message.turnID))) turnStatusByTurn.set(message.turnID, 'completed');
    turnTimes.set(message.turnID, times);
  }
  for (const message of visibleMessages) {
    if (message.turnID) {
      const createdAt = Date.parse(message.createdAt);
      if (!Number.isNaN(createdAt)) {
        const completedAt = message.toolCompletedAt ? Date.parse(message.toolCompletedAt) : NaN;
        const span =
          turnSpanByTurn.get(message.turnID) ?? {
            start: createdAt,
            end: createdAt,
            toolCount: 0,
            failedCount: 0
          };
        span.start = Math.min(span.start, createdAt);
        span.end = Math.max(span.end, createdAt, Number.isNaN(completedAt) ? createdAt : completedAt);
        if (message.kind === 'tool' || (!message.kind && message.role === 'tool')) {
          span.toolCount += 1;
          if (message.toolStatus === 'failed') span.failedCount += 1;
        }
        turnSpanByTurn.set(message.turnID, span);
      }
      if (isWorkEntry(message)) {
        const list = workEntriesByTurn.get(message.turnID) ?? [];
        list.push(message);
        workEntriesByTurn.set(message.turnID, list);
      }
    }
  }

  const items: TimelineItem[] = [];
  const emittedWorkRecordTurns = new Set<string>();

  for (const message of visibleMessages) {
    if (!message.turnID) {
      items.push({ type: 'message', message });
      continue;
    }

    if (isWorkEntry(message)) {
      if (!emittedWorkRecordTurns.has(message.turnID)) {
        emittedWorkRecordTurns.add(message.turnID);
        const entries = workEntriesByTurn.get(message.turnID) ?? [];
        if (entries.length > 0) {
          const turnStatus = turnStatusByTurn.get(message.turnID);
          items.push({
            type: 'workRecord',
            turnID: message.turnID,
            turnIndex: message.turnIndex,
            turnStatus,
            turnStartedAt: turnTimes.get(message.turnID)?.start,
            turnCompletedAt: turnTimes.get(message.turnID)?.end,
            entries
          });
        }
      }
    } else {
      items.push({ type: 'message', message });
    }
  }

  // 已结束的回合在该回合最后一条条目下方插入统计条；倒序插入避免下标位移
  const lastIndexByTurn = new Map<string, number>();
  items.forEach((item, index) => {
    if (item.type === 'message') {
      if (item.message.turnID) lastIndexByTurn.set(item.message.turnID, index);
    } else if (item.type === 'workRecord') {
      lastIndexByTurn.set(item.turnID, index);
    }
  });
  const finishedItems: Array<{ index: number; item: TimelineItem }> = [];
  lastIndexByTurn.forEach((index, turnID) => {
    if (!isEndedStatus(turnStatusByTurn.get(turnID))) return;
    const times = turnTimes.get(turnID);
    const start = Date.parse(times?.start ?? '');
    const end = Date.parse(times?.end ?? '');
    const authoritative = Number.isFinite(start) && Number.isFinite(end) && end >= start;
    const span = turnSpanByTurn.get(turnID);
    if (!span && !authoritative) return;
    const anchor = items[index];
    const turnIndex =
      anchor.type === 'message'
        ? anchor.message.turnIndex
        : anchor.type === 'workRecord'
        ? anchor.turnIndex
        : 0;
    finishedItems.push({
      index,
      item: {
        type: 'turnFinished',
        turnID,
        turnIndex,
        stats: {
          durationMs: authoritative ? end - start : Math.max(0, span!.end - span!.start),
          toolCount: span?.toolCount ?? 0,
          failedCount: span?.failedCount ?? 0,
          endedAt: new Date(authoritative ? end : span!.end).toISOString()
        },
        // 与桌面端一致：改动文件取自本回合工具消息里的文件编辑输入
        files: collectTurnEditedFiles(workEntriesByTurn.get(turnID) ?? [])
      }
    });
  });
  finishedItems.sort((left, right) => right.index - left.index);
  for (const finished of finishedItems) {
    items.splice(finished.index + 1, 0, finished.item);
  }

  return items;
}

function roleLabel(role: string): string {
  switch (role) {
    case 'user':
      return '你';
    case 'assistant':
      return '助手';
    case 'system':
      return '系统';
    case 'tool':
      return '工具';
    default:
      return role || '消息';
  }
}

function messageKindLabel(message: ThreadMessage): string {
  switch (message.kind) {
    case 'user':
      return '你';
    case 'assistant':
      return message.isFinalResponse ? '助手 · 答复' : '助手';
    case 'plan':
      return '📋 实施计划';
    case 'todo':
      return '☑️ 待办事项';
    case 'question_answer':
      return '已答复的提问';
    case 'warning':
      return '⚠️ 警告提示';
    case 'error':
      return '❌ 错误提示';
    case 'tool':
      return '工具';
    case 'thinking':
      return reasoningHeaderTitle(message.text || '', message.turnStatus === 'running');
    case 'info':
      return '系统提示';
    case 'swarm_report':
      return '子代理汇报';
    case 'swarm_task':
      return '主代理下达的任务';
    case 'host_event':
      return '宿主转来';
    default:
      return roleLabel(message.role);
  }
}

interface ErrorKindInfo {
  title: string;
  hint: string;
}

// 后端上报的失败类别（errKind）对应的标题与一句处理提示；未知或空值回落到原有「错误提示」。
const ERROR_KIND_INFO: Record<string, ErrorKindInfo> = {
  auth_required: {
    title: '🔐 需要登录',
    hint: '请在 Mac 端终端重新登录该 CLI 后再发送。'
  },
  rate_limited: {
    title: '⏳ 额度或限流',
    hint: '请求被限流或额度已用完，稍等片刻后重试。'
  },
  context_exhausted: {
    title: '📚 上下文已满',
    hint: '建议压缩上下文或新开一个会话。'
  },
  max_turns: {
    title: '🔁 达到最大轮数',
    hint: '本轮已达单轮最大往返次数，可以继续发送消息。'
  },
  cancelled: {
    title: '⏹ 已取消',
    hint: '本轮已被取消，可以重新发送。'
  },
  api_error: {
    title: '❌ 接口错误',
    hint: '接口调用失败，稍后可重试。'
  }
};

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
  onOpenThread?: (threadID: string) => void;
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
      className={`${messageLayoutClass(message)} space-y-2 min-w-0 overflow-hidden mb-4 ${messageCardClass(message)}`}
    >
      <div className="flex items-center justify-between gap-2 text-mc-caption">
        <span className="font-semibold text-[var(--foreground)]">{messageKindLabel(message)}</span>
        <span className="text-[var(--foreground-muted)]">
          #{message.turnIndex} · {formatRelativeTime(message.createdAt)}
        </span>
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

  React.useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [onClose]);

  return createPortal(
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        ref={sheetRef}
        className="thread-action-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="message-actions-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
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
      </section>
    </div>,
    document.body
  );
};

// 行级 memo：流式输出只会让变化的消息重新渲染，其余行（含 Markdown 解析结果）保持原样。
const TimelineEntry = React.memo<TimelineEntryProps>((props) => {
  const {
    threadID,
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
    isRunning = false,
    onContinue,
    onOpenFile,
    decisionAnswers,
    onSendStewardMessage,
    onOpenThread
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
    const workHeaderLabel = turnWorkHeaderLabel(item.entries, workRecordRunning, workRecordNowMs, item.turnStartedAt, item.turnCompletedAt);
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
            <span>#{item.turnIndex}</span>
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
                                  {rowLabel}
                                </span>
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
                              <MarkdownView text={entry.text || ''} highlight={highlightFor(entry.id)} math={!isThinkingRow} />
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
        />
      );
    }
  }
  if (message.kind === 'steward_result' && message.role === 'assistant') {
    const result = parseStewardResult(message.text);
    if (result) {
      return <StewardResultMessage key={message.id} message={message} onOpenThread={onOpenThread} />;
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
  // 「更多」只收次要操作，行内因此只留复制、分支、引用、编辑这类高频按钮（移动端也不会挤爆一行）
  const moreItems: MessageMoreMenuItem[] = [];
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
        <div className="flex flex-col items-end mb-4 min-w-0" {...longPress.handlers}>
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
          <div className="flex items-center justify-end gap-2 mt-1 px-1 min-w-0">
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
            <span className="text-mc-footnote text-[var(--foreground-muted)] text-right">
              {formatRelativeTime(message.createdAt)}
            </span>
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
        className="mb-5 w-full bg-transparent border-0 p-0 text-[16px] leading-[23px] text-[var(--foreground)] min-w-0"
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

        <div className="flex items-center justify-end gap-2 min-w-0 mt-2">
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

          {canQuote && (
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
      className={`${messageLayoutClass(message)} space-y-2 min-w-0 overflow-hidden mb-4 ${messageCardClass(message)}`}
    >
      <div className="flex items-center justify-between gap-2 text-mc-caption">
        <span className="font-semibold text-[var(--foreground)]">
          {errorInfo ? errorInfo.title : messageKindLabel(message)}
        </span>
        <span className="text-[var(--foreground-muted)]">
          #{message.turnIndex} · {formatRelativeTime(message.createdAt)}
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
  previous.isRunning === next.isRunning &&
  previous.onContinue === next.onContinue &&
  previous.onOpenFile === next.onOpenFile
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
  /** 总管决策卡选择/忽略时发送一条普通用户消息。 */
  onSendStewardMessage?: (text: string) => void | Promise<unknown>;
  /** 总管结果卡的「打开任务」跳转。 */
  onOpenThread?: (threadID: string) => void;
}

interface VirtualTimelineProps extends ThreadTimelineProps {
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
}

const VirtualTimeline: React.FC<VirtualTimelineProps> = ({
  threadID,
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
  onSendStewardMessage,
  onOpenThread
}) => {
  const [minuteTick, setMinuteTick] = React.useState(0);
  React.useEffect(() => {
    const timer = window.setInterval(() => setMinuteTick((tick) => tick + 1), 60_000);
    return () => window.clearInterval(timer);
  }, []);

  const autoCollapse = useAutoCollapseReasoning();
  const { toggleWorkRecord } = useWorkRecordHandlers(threadID, autoCollapse, onToggleWorkRecord);
  // 时间线最后一条可见消息：错误卡片的自动重试只认它
  const lastMessageID = React.useMemo(() => lastVisibleMessageID(items), [items]);

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
          swarmTruncatedReports={swarmTruncatedReports}
          isRunning={isRunning}
          onOpenFile={onOpenFile}
          onContinue={onContinue}
          decisionAnswers={decisionAnswers}
          onSendStewardMessage={onSendStewardMessage}
          onOpenThread={onOpenThread}
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
  onOpenFile,
  onSendStewardMessage,
  onOpenThread
}) => {
  const autoCollapse = useAutoCollapseReasoning();
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
  // 时间线最后一条可见消息（已跳过隐藏消息与并进胶囊的汇报行）
  const lastMessageID = React.useMemo(() => lastVisibleMessageID(visibleItems), [visibleItems]);
  // B7 后台任务：命令只算「回合已结束、命令仍在跑」，bee 只算运行中；都为 0 时聚合行不渲染
  const backgroundCommands = React.useMemo(
    () => collectBackgroundCommands(visibleItems, Boolean(isRunning)),
    [visibleItems, isRunning]
  );
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
        backgroundCommands={backgroundCommands}
        runningBees={runningBees}
          decisionAnswers={decisionAnswers}
          onSendStewardMessage={onSendStewardMessage}
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
          swarmTruncatedReports={swarmTruncatedReports}
          isRunning={isRunning}
          onOpenFile={onOpenFile}
          onContinue={onContinue}
           decisionAnswers={decisionAnswers}
           onSendStewardMessage={onSendStewardMessage}
           onOpenThread={onOpenThread}
        />
      </div>
    );
  };

  return (
    <>
      {visibleItems.map((item) => renderEntry(item))}
      <BackgroundTasksRow
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
