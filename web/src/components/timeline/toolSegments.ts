import type { ThreadMessage } from '../ThreadTimeline';

/**
 * 回合内活动段的纯函数：与桌面端 internal/ui/timeline_rows.go 的分段规则一一对应。
 * 桌面端有两级——工具组（连续活动行）与组内节拍（思考 + 它后面的连续工具），
 * Web 端回合折叠内部只做一级：按连续活动行分段，每段一个可折叠组头。
 */

/** 段内一个节拍：段首与段尾（不含）在组内条目里的下标（对齐桌面端 timelineGroupSegment）。 */
export interface GroupSegment {
  start: number;
  end: number;
}

/** 回合折叠内部的一段：连续工具/思考条目，或一条无法成组的单独条目。 */
export interface ToolSegment {
  /** 段内第一条条目的 ID：作为开合状态的稳定 key，流式更新时不变 */
  key: string;
  /** 段内条目，保持时间线原顺序 */
  entries: ThreadMessage[];
  /** 是否渲染成可折叠的组头：不足两条或没有工具调用的活动段逐条单独显示 */
  collapsible: boolean;
}

/** 是否是思考行（与桌面端 isReasoningMessage 口径一致）。 */
export function isReasoningEntry(message: ThreadMessage): boolean {
  return message.kind === 'thinking' || (!message.kind && message.role === 'thinking');
}

/** 是否是工具调用行（与桌面端 Role == "tool" 口径一致）。 */
export function isToolCallEntry(message: ThreadMessage): boolean {
  return message.kind === 'tool' || (!message.kind && message.role === 'tool');
}

/** 是否是活动行：工具调用或思考（对齐桌面端 isActivityMessage）。 */
export function isActivityEntry(message: ThreadMessage): boolean {
  return isToolCallEntry(message) || isReasoningEntry(message);
}

/**
 * 把一组活动条目切成节拍（对齐桌面端 groupSegments）：一条思考加上它后面紧跟的所有连续
 * 工具调用为一段，直到下一条思考（或组结束）为止；组开头没有前置思考的工具单独成一段。
 */
export function groupSegments(run: ThreadMessage[]): GroupSegment[] {
  const segments: GroupSegment[] = [];
  for (let index = 0; index < run.length; ) {
    const start = index;
    if (isReasoningEntry(run[index])) index += 1;
    while (index < run.length && !isReasoningEntry(run[index]) && isToolCallEntry(run[index])) {
      index += 1;
    }
    if (index === start) {
      // 既不是思考行也不是工具行（不应出现）：单独成段，避免死循环
      index += 1;
    }
    segments.push({ start, end: index });
  }
  return segments;
}

/**
 * 把回合内的工作条目切成段（对齐桌面端 buildTimelineItemsOpts 的活动组合并规则）：
 * 连续的活动行合成一段，至少两条且含工具调用时才成组（渲染可折叠组头）；
 * 不足两条或全是思考行、以及夹在中间的系统提示条目都逐条单独展示。
 * 若整批条目只切出一段，则这一段不折叠：此时段组头与回合组头说的是同一件事，重复且要多点一次。
 */
export function buildToolSegments(entries: ThreadMessage[]): ToolSegment[] {
  const segments: ToolSegment[] = [];
  let index = 0;
  while (index < entries.length) {
    const entry = entries[index];
    if (!isActivityEntry(entry)) {
      segments.push({ key: entry.id, entries: [entry], collapsible: false });
      index += 1;
      continue;
    }
    const start = index;
    let hasTool = false;
    while (index < entries.length && isActivityEntry(entries[index])) {
      if (isToolCallEntry(entries[index])) hasTool = true;
      index += 1;
    }
    const run = entries.slice(start, index);
    if (run.length >= 2 && hasTool) {
      segments.push({ key: run[0].id, entries: run, collapsible: true });
      continue;
    }
    for (const single of run) {
      segments.push({ key: single.id, entries: [single], collapsible: false });
    }
  }
  if (segments.length === 1 && segments[0].collapsible) {
    // 回合里只有这一段：段组头与回合组头重复，直接列出条目
    segments[0] = { ...segments[0], collapsible: false };
  }
  return segments;
}

/** 段开合状态表的键：带上线程前缀，避免不同线程里同 ID 的消息互串。 */
export function toolSegmentOpenKey(threadID: string, key: string): string {
  return `${threadID}:${key}`;
}

/** 段的实际开合状态：用户显式设置过就以设置为准，否则用默认值（对齐桌面端 toolGroupOpen）。 */
export function toolSegmentOpen(
  openMap: Record<string, boolean>,
  threadID: string,
  key: string,
  defaultOpen: boolean
): boolean {
  return openMap[toolSegmentOpenKey(threadID, key)] ?? defaultOpen;
}

/** 运行中的段折叠时的只读预览：最近几条工具调用的名字（对齐桌面端 toolGroupPreviewTools）。 */
export function toolSegmentPreviewText(entries: ThreadMessage[], limit = 3): string {
  const names: string[] = [];
  for (let index = entries.length - 1; index >= 0 && names.length < limit; index -= 1) {
    const entry = entries[index];
    if (!isToolCallEntry(entry)) continue;
    names.unshift((entry.toolName || '').trim() || '工具调用');
  }
  return names.join(' · ');
}

/** 查找某条消息所在的段 key：会话内查找命中落在折叠的段里时要展开这一段。 */
export function toolSegmentKeyForMessage(entries: ThreadMessage[], messageID: string): string | null {
  if (!messageID) return null;
  for (const segment of buildToolSegments(entries)) {
    if (segment.entries.some((entry) => entry.id === messageID)) return segment.key;
  }
  return null;
}
