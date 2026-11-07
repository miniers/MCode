// 会话内查找：字级命中计算（纯函数，不碰 DOM）。
//
// 计数口径（两处必须一致，见下方说明）：
// 1. findMatches 在消息原文（message.text）上按 UTF-16 偏移扫描，occurrence 是「这条消息内第几处」，从 0 开始，
//    start/end 是原文偏移；消息按传入顺序（时间线顺序）遍历，命中跳过隐藏消息（复用 isHiddenTimelineMessage）。
// 2. Markdown 渲染时语法标记（`**`、`[text](url)`、表格线等）会被渲染器移除，所以 MarkdownView 的 rehype 插件
//    按「渲染后文本节点的文档顺序」累计序号，调用 highlightRangesFrom 逐段推进计数器。
//    常规内容两套序号完全一致；只有当查询本身跨越了被移除的语法标记（例如搜 `**粗**` 的星号）时才可能错位，
//    这种情况下以渲染后的顺序为准——active 只落在渲染序号相同的那个命中上，超出范围时不标当前命中。
//
// 匹配规则与桌面端 internal/ui/chat_find.go 的 foldFindRune 一致：默认不区分大小写，大小写折叠只用于比较，
// 返回的偏移永远是可以直接 slice 原文的位置。

import type { ThreadMessage } from '../components/ThreadTimeline';
import { isHiddenTimelineMessage } from '../components/ThreadTimeline';

/** 一处命中的区间，start/end 是原文的 UTF-16 偏移（end 不含）。 */
export interface FindRange {
  start: number;
  end: number;
}

/** 带消息内序号的命中区间：occurrence 从 0 开始。 */
export interface OccurrenceRange extends FindRange {
  occurrence: number;
}

/** 一条消息里的一处命中：occurrence 是该消息内的第几处，start/end 是消息原文偏移。 */
export interface FindMatch {
  messageID: string;
  occurrence: number;
  start: number;
  end: number;
}

/** 当前命中在 DOM 里的选择器：MarkdownView 插件给当前命中加 data-find-active 与 find-mark-active。 */
export const FIND_ACTIVE_SELECTOR = 'mark.find-mark-active, [data-find-active]';

/**
 * 在一段文本里扫描 query 的每一处命中，返回原文偏移；从左到右、互不重叠。
 * query 只有空白或文本为空时返回空数组。
 */
function scanRanges(text: string, query: string, caseSensitive: boolean): FindRange[] {
  const needleSource = query.trim();
  if (text === '' || needleSource === '') return [];

  let haystack = text;
  let needle = needleSource;
  if (!caseSensitive) {
    const lowerText = text.toLowerCase();
    const lowerNeedle = needleSource.toLowerCase();
    // 折叠后长度变了（例如 'İ'）就无法复用偏移，退回区分大小写的扫描，保证偏移始终落在原文上
    if (lowerText.length === text.length && lowerNeedle.length === needleSource.length) {
      haystack = lowerText;
      needle = lowerNeedle;
    }
  }

  const ranges: FindRange[] = [];
  let from = 0;
  while (from <= haystack.length - needle.length) {
    const found = haystack.indexOf(needle, from);
    if (found < 0) break;
    ranges.push({ start: found, end: found + needle.length });
    from = found + needle.length;
  }
  return ranges;
}

/** 一段（原文）文本里的全部命中区间；纯文本渲染方用它给命中包 mark。 */
export function highlightRanges(text: string, query: string, caseSensitive = false): FindRange[] {
  return scanRanges(text, query, caseSensitive);
}

/**
 * 渲染顺序计数：把一段文本节点接在已经计过 occurrenceOffset 处命中的文本后面继续计数，
 * 返回本段的命中（带消息内的全局序号）与下一段的起始序号。
 * MarkdownView 的 rehype 插件按文档顺序逐段调用它，所以序号与渲染后的文本顺序一致。
 */
export function highlightRangesFrom(
  text: string,
  query: string,
  caseSensitive: boolean,
  occurrenceOffset: number
): { ranges: OccurrenceRange[]; nextOccurrence: number } {
  const base = scanRanges(text, query, caseSensitive);
  return {
    ranges: base.map((range, index) => ({
      start: range.start,
      end: range.end,
      occurrence: occurrenceOffset + index
    })),
    nextOccurrence: occurrenceOffset + base.length
  };
}

/**
 * 时间线顺序的全部命中，逐处展开（一条消息里 3 处就是 3 条，occurrence 依次 0/1/2）。
 * 隐藏消息（总管唤醒、静默标记）不参与；思考块与工具输出如果在时间线上可见（展开的回合工作记录）就参与，
 * 因为它们同样会被 MarkdownView 渲染并高亮。
 */
export function findMatches(
  messages: ThreadMessage[],
  query: string,
  caseSensitive = false
): FindMatch[] {
  const needle = query.trim();
  if (needle === '') return [];

  const matches: FindMatch[] = [];
  for (const message of messages) {
    if (!message || !message.id) continue;
    if (isHiddenTimelineMessage(message)) continue;
    const text = message.text ?? '';
    if (text === '') continue;
    const ranges = scanRanges(text, needle, caseSensitive);
    for (let occurrence = 0; occurrence < ranges.length; occurrence += 1) {
      const range = ranges[occurrence];
      matches.push({
        messageID: message.id,
        occurrence,
        start: range.start,
        end: range.end
      });
    }
  }
  return matches;
}
