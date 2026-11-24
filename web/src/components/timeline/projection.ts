import type { RemoteThreadHistoryPage, RemoteThreadMessage } from '../../types/contract';
import type { RemoteSwarmBee, RemoteSwarmState } from '../../types/swarm';
import { parseSwarmReport, splitSwarmFence, type SwarmEntryParsed, type SwarmReportSection } from '../../utils/markdown';
import type { SwarmDispatchReportIndex } from '../SwarmCapsules';
import { dispatchEntryKey, matchCandidate, reportSectionKey } from '../SwarmCapsules';
import type { TurnFinishedStats } from './TurnFinishedBar';
import { collectTurnEditedFiles, type TurnEditedFile } from './TurnFilesCard';
import type { BackgroundTaskCommand } from './BackgroundTasksRow';
import { isFileEditToolName } from './ToolEntryDiff';
import { commandSnippet } from './BackgroundTasksRow';
import { isToolCallEntry } from './toolSegments';
import { reasoningHeaderTitle } from './stewardTimeline';
import { parseSwarmReportArchiveFields, type SwarmReportFullItem } from './SwarmReportFullText';
import {
  todoProgressFromMessage,
  type TodoItem,
  type TodoSourceMessage
} from '../../utils/todoProgress';

export const COLLAPSE_THRESHOLD = 4000;
export const COLLAPSE_PREVIEW_LENGTH = 4000;

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
export function timelineItemKey(item: TimelineItem): string {
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
export function timelineItemContainsMessage(item: TimelineItem, messageID: string): boolean {
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
export function buildSwarmReportIndex(items: TimelineItem[]): SwarmDispatchReportIndex {
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
export function truncatedReportKey(report: SwarmReportSection): string {
  return `${report.name.trim()}\u0000${report.task.trim()}`;
}

/**
 * 被截断的汇报才在正文里带 bee ID：按消息把它抽出来建索引，
 * 并进发起胶囊的汇报不再占自己的消息行，只能靠名字与任务找回全文入口。
 */
export function buildTruncatedReportIndex(items: TimelineItem[]): Record<string, SwarmReportFullItem> {
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
export function truncatedReportsOfDispatch(
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
export function isMergedSwarmReportRow(item: TimelineItem, index?: SwarmDispatchReportIndex): boolean {
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

export const STEWARD_SILENT_MARKER = '[[静默]]';

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

export function isAssistantMessage(message: ThreadMessage): boolean {
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

export interface TurnChecklistData {
  items: TodoItem[];
  done: number;
  total: number;
}

function isChecklistTurnBoundary(message: TodoSourceMessage): boolean {
  if (message.kind === 'host_event') return false;
  return message.kind === 'user' || (!message.kind && message.role === 'user');
}

/**
 * 提取本回合（用户消息到下一条用户消息之间）最后一条可解析为待办清单的工具消息；
 * 复用 todoProgress.ts 的解析口径，没有清单时返回 null。
 */
export function turnChecklist(messages: readonly TodoSourceMessage[]): TurnChecklistData | null {
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index];
    if (!message) continue;
    if (isChecklistTurnBoundary(message)) break;
    const progress = todoProgressFromMessage(message);
    if (progress) {
      return {
        items: progress.items,
        done: progress.done,
        total: progress.total
      };
    }
  }
  return null;
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
 * background 任务可查看日志并停止。
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
        toolInput: entry.toolInput,
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
      toolInput: entry.toolInput,
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
export function isBlockedToolEntry(entry: ThreadMessage): boolean {
  if (entry.toolStatus === 'blocked') return true;
  const text = entry.text || '';
  const firstLine = text.split('\n').find((line) => line.trim().length > 0)?.trim() ?? '';
  return firstLine.startsWith('PreToolUse:') && firstLine.includes(' hook error:');
}

// 与桌面端 isToolFailed 一致：blocked 不算失败
export function isFailedToolEntry(entry: ThreadMessage): boolean {
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

export function messageKindLabel(message: ThreadMessage): string {
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

