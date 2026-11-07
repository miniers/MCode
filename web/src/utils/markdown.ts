// 轻量 Markdown 解析器（零依赖、纯函数、不产生 HTML 字符串）
// 把一段 Markdown 文本解析成结构化 token，交给 React 直接渲染。
// 支持：ATX 标题（# 到 ####，更深的层级归并到 4）、无序列表（- / *）、
// 有序列表（1. / 1)）、围栏代码块（``` 或 ~~~，带语言标记）、引用块（>）、
// 水平分割线、GFM 表格、嵌套列表，以及行内代码、粗体、斜体、删除线与链接。

import { isThreadMentionHref } from './threadMention';

/** 行内 token：纯文本 */
export interface MarkdownInlineText {
  type: 'text';
  content: string;
}

/** 行内 token：单反引号包裹的行内代码 */
export interface MarkdownInlineCode {
  type: 'code';
  content: string;
}

/** 行内 token：双星号包裹的粗体，inlines 是内部递归解析的结果 */
export interface MarkdownInlineStrong {
  type: 'strong';
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 行内 token：单星号/单下划线包裹的斜体 */
export interface MarkdownInlineEmphasis {
  type: 'emphasis';
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 行内 token：双波浪线包裹的删除线 */
export interface MarkdownInlineStrike {
  type: 'strike';
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 行内 token：方括号加圆括号的链接 */
export interface MarkdownInlineLink {
  type: 'link';
  content: string;
  href: string;
  inlines: MarkdownInlineToken[];
}

export type MarkdownInlineToken =
  | MarkdownInlineText
  | MarkdownInlineCode
  | MarkdownInlineStrong
  | MarkdownInlineEmphasis
  | MarkdownInlineStrike
  | MarkdownInlineLink;

/** 块级 token：普通段落（保留原始换行，渲染时用 whitespace-pre-wrap） */
export interface MarkdownParagraphBlock {
  type: 'paragraph';
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 块级 token：ATX 标题，depth 已归并到 1-4 */
export interface MarkdownHeadingBlock {
  type: 'heading';
  depth: 1 | 2 | 3 | 4;
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 列表项，自身带行内 token，因此列表项内部可以再嵌套行内结构 */
export interface MarkdownListItem {
  content: string;
  inlines: MarkdownInlineToken[];
  /** 缩进的子块（嵌套列表等），没有则为空数组 */
  children: MarkdownBlock[];
}

/** 块级 token：有序或无序列表 */
export interface MarkdownListBlock {
  type: 'list';
  ordered: boolean;
  start: number;
  items: MarkdownListItem[];
}

/** 块级 token：围栏代码块 */
export interface MarkdownCodeBlock {
  type: 'code';
  language: string;
  content: string;
}

/** 块级 token：引用块，内部是递归解析出的块级 token */
export interface MarkdownQuoteBlock {
  type: 'quote';
  blocks: MarkdownBlock[];
}

/** 表格单元格 */
export interface MarkdownTableCell {
  content: string;
  inlines: MarkdownInlineToken[];
}

/** 块级 token：GFM 表格，align 与表头列一一对应 */
export interface MarkdownTableBlock {
  type: 'table';
  align: Array<'left' | 'center' | 'right' | null>;
  header: MarkdownTableCell[];
  rows: MarkdownTableCell[][];
}

/** 块级 token：水平分割线 */
export interface MarkdownDividerBlock {
  type: 'divider';
}

export type MarkdownBlock =
  | MarkdownParagraphBlock
  | MarkdownHeadingBlock
  | MarkdownListBlock
  | MarkdownCodeBlock
  | MarkdownQuoteBlock
  | MarkdownTableBlock
  | MarkdownDividerBlock;

const FENCE_PATTERN = /^ {0,3}(`{3,}|~{3,})[ \t]*([\w+#.-]*)[ \t]*$/;
const HEADING_PATTERN = /^ {0,3}(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$/;
const DIVIDER_PATTERN = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const QUOTE_PATTERN = /^ {0,3}>[ \t]?(.*)$/;
const UNORDERED_ITEM_PATTERN = /^ {0,3}[-*][ \t]+(.*)$/;
const ORDERED_ITEM_PATTERN = /^ {0,3}(\d{1,9})[.)][ \t]+(.*)$/;

interface ListItemMatch {
  ordered: boolean;
  start: number;
  content: string;
}

/** 判断一行是否是列表项起始行，并返回序号与内容 */
function matchListItem(line: string): ListItemMatch | null {
  const ordered = ORDERED_ITEM_PATTERN.exec(line);
  if (ordered) {
    return { ordered: true, start: Number.parseInt(ordered[1], 10) || 1, content: ordered[2] };
  }
  const unordered = UNORDERED_ITEM_PATTERN.exec(line);
  if (unordered) {
    return { ordered: false, start: 1, content: unordered[1] };
  }
  return null;
}

const TABLE_DELIMITER_PATTERN = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;

/** 按未转义的 | 切分表格行，去掉首尾竖线 */
function splitTableRow(line: string): string[] {
  let text = line.trim();
  if (text.startsWith('|')) text = text.slice(1);
  if (text.endsWith('|') && !text.endsWith('\\|')) text = text.slice(0, -1);
  const cells: string[] = [];
  let buffer = '';
  for (let i = 0; i < text.length; i++) {
    if (text[i] === '\\' && text[i + 1] === '|') {
      buffer += '|';
      i++;
    } else if (text[i] === '|') {
      cells.push(buffer.trim());
      buffer = '';
    } else {
      buffer += text[i];
    }
  }
  cells.push(buffer.trim());
  return cells;
}

/** header 行后紧跟分隔行且列数一致时，才认定为表格 */
function isTableStart(header: string, delimiter: string | undefined): boolean {
  if (delimiter === undefined || !header.includes('|') || !delimiter.includes('-')) return false;
  if (!TABLE_DELIMITER_PATTERN.test(delimiter)) return false;
  return splitTableRow(header).length === splitTableRow(delimiter).length;
}

function leadingSpaces(line: string): number {
  return line.length - line.replace(/^ +/, '').length;
}

/** 一行是否会打断正在累积的段落/列表项 */
function isBlockStart(line: string): boolean {
  return (
    FENCE_PATTERN.test(line) ||
    HEADING_PATTERN.test(line) ||
    DIVIDER_PATTERN.test(line) ||
    QUOTE_PATTERN.test(line) ||
    matchListItem(line) !== null
  );
}

/** 统计 text 从 index 开始连续出现的同一字符个数 */
function countRun(text: string, index: number, char: string): number {
  let length = 0;
  while (index + length < text.length && text[index + length] === char) length++;
  return length;
}

/** 从 index 开始查找长度不小于 minRun 的同一字符连续段，返回起始下标，找不到返回 -1 */
function findRun(text: string, index: number, char: string, minRun: number): number {
  let cursor = index;
  while (cursor < text.length) {
    if (text[cursor] === char && countRun(text, cursor, char) >= minRun) return cursor;
    cursor++;
  }
  return -1;
}

/** 解析 [label](href)，href 允许含一层嵌套括号；返回结束下标（不含） */
function matchLink(text: string, index: number): { label: string; href: string; end: number } | null {
  const labelEnd = text.indexOf(']', index + 1);
  if (labelEnd < 0 || text[labelEnd + 1] !== '(') return null;

  let depth = 1;
  let cursor = labelEnd + 2;
  while (cursor < text.length) {
    const char = text[cursor];
    if (char === '(') depth++;
    else if (char === ')') {
      depth--;
      if (depth === 0) break;
    }
    cursor++;
  }
  if (depth !== 0) return null;

  const label = text.slice(index + 1, labelEnd);
  const href = text.slice(labelEnd + 2, cursor).trim();
  if (!label || !href) return null;

  return { label, href, end: cursor + 1 };
}

/** 只放行常规协议，避免 javascript:/data: 之类的链接进入锚点 */
function isSafeHref(href: string): boolean {
  // 会话引用（@提及）用 mcode:// 自定义协议，必须放行，否则会被当成不安全链接丢掉。
  if (isThreadMentionHref(href)) return true;
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) {
    return /^(https?|mailto|tel):/i.test(href);
  }
  // 相对路径与锚点
  return !href.startsWith('//');
}

/**
 * 解析行内 Markdown，返回按顺序排列的行内 token。
 * 文本中没有行内标记时返回单个 text token。
 */
export function parseInline(text: string): MarkdownInlineToken[] {
  const tokens: MarkdownInlineToken[] = [];
  let buffer = '';
  let index = 0;

  const flush = () => {
    if (!buffer) return;
    tokens.push({ type: 'text', content: buffer });
    buffer = '';
  };

  while (index < text.length) {
    const char = text[index];

    // 反斜杠转义
    if (char === '\\' && index + 1 < text.length) {
      buffer += text[index + 1];
      index += 2;
      continue;
    }

    // 行内代码
    if (char === '`') {
      const fenceLength = countRun(text, index, '`');
      const close = findRun(text, index + fenceLength, '`', fenceLength);
      if (close >= 0) {
        flush();
        tokens.push({ type: 'code', content: text.slice(index + fenceLength, close).trim() });
        index = close + fenceLength;
        continue;
      }
    }

    // 粗体（双星号/双下划线）与斜体（单星号/单下划线）
    if (char === '*' || char === '_') {
      const isUnderscore = char === '_';
      const previous = index > 0 ? text[index - 1] : '';
      const underscoreOk = !isUnderscore || (!/[\w]/.test(previous) && text[index + 1] !== ' ');

      if (underscoreOk) {
        const runLength = countRun(text, index, char);
        if (runLength >= 2) {
          // 三星号按粗体处理，避免 ***文本*** 被拆成错位的标记
          const delimiter = runLength >= 3 ? 3 : 2;
          const close = findRun(text, index + delimiter, char, delimiter);
          if (close >= 0) {
            const inner = text.slice(index + delimiter, close);
            if (inner.trim()) {
              flush();
              tokens.push({ type: 'strong', content: inner, inlines: parseInline(inner) });
              index = close + delimiter;
              continue;
            }
          }
        }

        const close = findRun(text, index + 1, char, 1);
        if (close >= 0) {
          const inner = text.slice(index + 1, close);
          const innerOk =
            inner.trim().length > 0 &&
            !/^\s/.test(inner) &&
            !/\s$/.test(inner) &&
            // 下划线不能贴着单词边界，避免 snake_case 被误判成斜体
            (!isUnderscore || !/\w/.test(text[close + 1] || ''));
          if (innerOk) {
            flush();
            tokens.push({ type: 'emphasis', content: inner, inlines: parseInline(inner) });
            index = close + 1;
            continue;
          }
        }
      }
    }

    // 删除线
    if (char === '~' && text[index + 1] === '~') {
      const close = findRun(text, index + 2, '~', 2);
      if (close >= 0) {
        const inner = text.slice(index + 2, close);
        if (inner.trim()) {
          flush();
          tokens.push({ type: 'strike', content: inner, inlines: parseInline(inner) });
          index = close + 2;
          continue;
        }
      }
    }

    // 链接
    if (char === '[') {
      const link = matchLink(text, index);
      if (link && isSafeHref(link.href)) {
        flush();
        tokens.push({ type: 'link', content: link.label, href: link.href, inlines: parseInline(link.label) });
        index = link.end;
        continue;
      }
    }

    buffer += char;
    index++;
  }

  flush();
  return tokens;
}

/** 把累积的段落行收成一个段落块 */
function pushParagraph(blocks: MarkdownBlock[], lines: string[]): void {
  if (lines.length === 0) return;
  const content = lines.join('\n').replace(/^\n+|\s+$/, '');
  if (!content) return;
  blocks.push({ type: 'paragraph', content, inlines: parseInline(content) });
}

/**
 * 解析 Markdown 文本，返回块级 token 数组。
 * 不抛出异常：无法识别的行一律按普通段落处理。
 */
export function parseMarkdown(text: string): MarkdownBlock[] {
  const lines = String(text ?? '').split(/\r?\n/);
  const blocks: MarkdownBlock[] = [];
  let paragraph: string[] = [];

  const flushParagraph = () => {
    pushParagraph(blocks, paragraph);
    paragraph = [];
  };

  let index = 0;
  while (index < lines.length) {
    const line = lines[index];

    // 空行：结束当前段落
    if (!line.trim()) {
      flushParagraph();
      index++;
      continue;
    }

    // 围栏代码块
    const fence = FENCE_PATTERN.exec(line);
    if (fence) {
      flushParagraph();
      const marker = fence[1][0];
      const markerLength = fence[1].length;
      const body: string[] = [];
      index++;
      while (index < lines.length) {
        const closing = FENCE_PATTERN.exec(lines[index]);
        if (closing && closing[1][0] === marker && closing[1].length >= markerLength) {
          index++;
          break;
        }
        body.push(lines[index]);
        index++;
      }
      blocks.push({
        type: 'code',
        language: fence[2] || '',
        content: body.join('\n').replace(/\s+$/, '')
      });
      continue;
    }

    // ATX 标题
    const heading = HEADING_PATTERN.exec(line);
    if (heading) {
      flushParagraph();
      const depth = Math.min(heading[1].length, 4) as 1 | 2 | 3 | 4;
      const content = heading[2].trim();
      blocks.push({ type: 'heading', depth, content, inlines: parseInline(content) });
      index++;
      continue;
    }

    // 水平分割线（需在列表之前判断，"- - -" 也属于分割线）
    if (DIVIDER_PATTERN.test(line)) {
      flushParagraph();
      blocks.push({ type: 'divider' });
      index++;
      continue;
    }

    // 引用块：连续 > 行去掉前缀后递归解析
    const quote = QUOTE_PATTERN.exec(line);
    if (quote) {
      flushParagraph();
      const inner: string[] = [];
      while (index < lines.length) {
        const quoted = QUOTE_PATTERN.exec(lines[index]);
        if (!quoted) break;
        inner.push(quoted[1]);
        index++;
      }
      blocks.push({ type: 'quote', blocks: parseMarkdown(inner.join('\n')) });
      continue;
    }

    // 列表：连续同类列表项收成一个列表块
    const firstItem = matchListItem(line);
    if (firstItem) {
      flushParagraph();
      const items: MarkdownListItem[] = [];
      const ordered = firstItem.ordered;
      const start = firstItem.start;

      while (index < lines.length) {
        // 缩进 2 格以上的列表行属于上一项的子列表，不是同级项
        const item = leadingSpaces(lines[index]) >= 2 ? null : matchListItem(lines[index]);
        if (!item || item.ordered !== ordered) break;
        let content = item.content;
        index++;

        // 缩进行并入当前列表项：先是续行文字，遇到块起始行后全部作为子块递归解析
        const childLines: string[] = [];
        while (index < lines.length) {
          const next = lines[index];
          if (!next.trim()) {
            // 空行后仍是缩进内容时属于当前项
            let lookahead = index;
            while (lookahead < lines.length && !lines[lookahead].trim()) lookahead++;
            if (lookahead < lines.length && leadingSpaces(lines[lookahead]) >= 2 && childLines.length > 0) {
              childLines.push(...lines.slice(index, lookahead));
              index = lookahead;
              continue;
            }
            break;
          }
          if (leadingSpaces(next) >= 2) {
            if (childLines.length === 0 && !isBlockStart(next)) {
              content += '\n' + next.trim();
            } else {
              childLines.push(next);
            }
            index++;
            continue;
          }
          break;
        }

        const indent = childLines.reduce(
          (min, line) => (line.trim() ? Math.min(min, leadingSpaces(line)) : min),
          Infinity
        );
        const children = childLines.length
          ? parseMarkdown(childLines.map((line) => line.slice(Math.min(indent, leadingSpaces(line)))).join('\n'))
          : [];
        items.push({ content, inlines: parseInline(content), children });

        // 列表项之间的空行：只有下一非空行还是同类列表项时才继续
        if (index < lines.length && !lines[index].trim()) {
          let lookahead = index;
          while (lookahead < lines.length && !lines[lookahead].trim()) lookahead++;
          const nextItem =
            lookahead < lines.length && leadingSpaces(lines[lookahead]) < 2 ? matchListItem(lines[lookahead]) : null;
          if (nextItem && nextItem.ordered === ordered) {
            index = lookahead;
            continue;
          }
          break;
        }
      }

      blocks.push({ type: 'list', ordered, start, items });
      continue;
    }

    // GFM 表格：表头行 + 分隔行 + 若干数据行
    if (isTableStart(line, lines[index + 1])) {
      flushParagraph();
      const align = splitTableRow(lines[index + 1]).map((cell) => {
        const left = cell.startsWith(':');
        const right = cell.endsWith(':');
        return left && right ? 'center' : right ? 'right' : left ? 'left' : null;
      });
      const toCells = (row: string): MarkdownTableCell[] => {
        const cells = splitTableRow(row);
        // 补齐或截断到表头列数
        return align.map((_, col) => ({ content: cells[col] ?? '', inlines: parseInline(cells[col] ?? '') }));
      };
      const header = toCells(line);
      index += 2;
      const rows: MarkdownTableCell[][] = [];
      while (index < lines.length && lines[index].trim() && lines[index].includes('|')) {
        rows.push(toCells(lines[index]));
        index++;
      }
      blocks.push({ type: 'table', align, header, rows });
      continue;
    }

    // 普通文本行，累积到段落
    paragraph.push(line);
    index++;
  }

  flushParagraph();
  return blocks;
}

const PARSE_CACHE_LIMIT = 400;
const parseCache = new Map<string, MarkdownBlock[]>();

/**
 * 带缓存的解析：时间线重渲染时同一段文本不再重复解析。
 * 返回值被多处共享，调用方只读不改；缓存按插入顺序淘汰最旧项。
 */
export function parseMarkdownCached(text: string): MarkdownBlock[] {
  const hit = parseCache.get(text);
  if (hit) return hit;
  const blocks = parseMarkdown(text);
  if (parseCache.size >= PARSE_CACHE_LIMIT) {
    const oldest = parseCache.keys().next().value;
    if (oldest !== undefined) parseCache.delete(oldest);
  }
  parseCache.set(text, blocks);
  return blocks;
}

function inlinesToPlainText(inlines: MarkdownInlineToken[]): string {
  return inlines.map((token) => ('inlines' in token ? inlinesToPlainText(token.inlines) : token.content)).join('');
}

function blocksToPlainText(blocks: MarkdownBlock[]): string {
  const parts: string[] = [];
  for (const block of blocks) {
    if (block.type === 'paragraph' || block.type === 'heading') {
      parts.push(inlinesToPlainText(block.inlines));
    } else if (block.type === 'list') {
      parts.push(
        block.items
          .map((item, i) => {
            const head = `${block.ordered ? `${block.start + i}.` : '-'} ${inlinesToPlainText(item.inlines)}`;
            const nested = blocksToPlainText(item.children).replace(/^/gm, '  ');
            return item.children.length ? `${head}\n${nested}` : head;
          })
          .join('\n')
      );
    } else if (block.type === 'table') {
      const toLine = (cells: MarkdownTableCell[]) => cells.map((cell) => inlinesToPlainText(cell.inlines)).join('\t');
      parts.push([toLine(block.header), ...block.rows.map(toLine)].join('\n'));
    } else if (block.type === 'code') {
      parts.push(block.content);
    } else if (block.type === 'quote') {
      parts.push(blocksToPlainText(block.blocks));
    }
    // divider 没有文字，丢弃
  }
  return parts.filter((part) => part.length > 0).join('\n\n');
}

/** 把 Markdown 转为纯文本：去掉标记，保留段落、列表和代码块的文字。 */
export function toPlainText(text: string): string {
  return blocksToPlainText(parseMarkdownCached(text));
}

function findLastCodeBlock(blocks: MarkdownBlock[]): MarkdownCodeBlock | null {
  for (let i = blocks.length - 1; i >= 0; i--) {
    const block = blocks[i];
    if (block.type === 'code') {
      // swarm（旧 hydra）块是协调指令，不是用户要复制的代码
      const lang = block.language.toLowerCase();
      if (lang !== 'swarm' && lang !== 'hydra') return block;
    } else if (block.type === 'quote') {
      const nested = findLastCodeBlock(block.blocks);
      if (nested) return nested;
    }
  }
  return null;
}

/** 取最后一个代码块的内容（排除 language 为 swarm/hydra 的块）；没有则返回 null。 */
export function lastCodeBlock(text: string): string | null {
  return findLastCodeBlock(parseMarkdownCached(text))?.content ?? null;
}

/** 从派单块解析出的单条子代理派单（只保留界面展示需要的字段） */
export interface SwarmEntryParsed {
  name?: string;
  task: string;
  /** 执行档位标识（read / edit / deep 等），缺省表示主会话没写 */
  tier?: string;
  /** 只读档子代理：改动不会落回项目 */
  readOnly?: boolean;
}

/** 从派单 JSON 项里取展示字段：兼容 name/head、task/title/prompt 等写法 */
function toSwarmEntry(item: unknown): SwarmEntryParsed {
  const itemObj = typeof item === 'object' && item !== null ? (item as Record<string, unknown>) : {};
  const pick = (key: string): string | undefined => {
    const value = itemObj[key];
    return typeof value === 'string' && value.trim() ? value.trim() : undefined;
  };
  const tier = pick('tier');
  const readOnlyRaw = itemObj.readOnly ?? itemObj.read_only;
  return {
    name: pick('name') ?? pick('head'),
    task: pick('task') ?? pick('title') ?? pick('prompt') ?? '子任务',
    tier,
    readOnly: typeof readOnlyRaw === 'boolean' ? readOnlyRaw : tier?.toLowerCase() === 'read'
  };
}

/**
 * 解析编排派单块的内容，返回每条派单。
 * 流式中 JSON 尚未闭合时尽量收集已完整闭合的对象，解析不出任何条目返回 null。
 */
export function parseSwarmEntries(content: string): SwarmEntryParsed[] | null {
  const trimmed = content.trim();
  if (!trimmed) return null;

  try {
    const parsed = JSON.parse(trimmed);
    if (Array.isArray(parsed)) {
      return parsed.map(toSwarmEntry);
    }
    if (typeof parsed === 'object' && parsed !== null) {
      const parsedObj = parsed as Record<string, unknown>;
      if (Array.isArray(parsedObj.delegations)) {
        return parsedObj.delegations.map(toSwarmEntry);
      }
      return [toSwarmEntry(parsedObj)];
    }
  } catch {
    // 流式状态下可能 JSON 尚未闭合，尝试正则匹配已闭合的对象
    const matches = trimmed.match(/\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\}/g);
    if (matches && matches.length > 0) {
      const partial: SwarmEntryParsed[] = [];
      for (const raw of matches) {
        try {
          const item = JSON.parse(raw);
          if (item && typeof item === 'object') partial.push(toSwarmEntry(item));
        } catch {
          // 忽略单个残缺块
        }
      }
      if (partial.length > 0) return partial;
    }
  }

  return null;
}

/** 行首最多 3 个空格后跟 ``` 加派单类型语言标记（swarm / swarm-sent / swarm-leaving，过渡期也认 hydra 前缀）的开围栏；
 * swarm-answer（旧 hydra-answer）是主会话回答子代理提问的块，不是派单，留给 MarkdownView 处理 */
const SWARM_FENCE_OPEN = /^ {0,3}`{3,}[ \t]*(?:swarm|hydra)(?:-(?:sent|leaving))?[ \t]*$/i;
/** 只由三个以上反引号构成的闭合围栏 */
const SWARM_FENCE_CLOSE = /^ {0,3}`{3,}[ \t]*$/;
/** 流式中统计围栏里已出现的 task 键个数 */
const SWARM_TASK_KEY = /"task"[ \t]*:/g;

export interface SwarmFenceSplit {
  /** 去掉派单块（以及流式中未闭合的尾部）之后的正文 */
  body: string;
  /** 已闭合派单块里的条目 */
  entries: SwarmEntryParsed[];
  /** 有未闭合的派单块：正在流式输出中 */
  pending: boolean;
  /** 未闭合派单块里已出现的 task 键个数 */
  pendingCount: number;
}

/**
 * 把消息正文和末尾的编排派单块分开。
 * 派单块总在回复末尾，且常常超出时间线的 4000 字折叠上限，因此先剥离再决定折叠哪部分。
 */
export function splitSwarmFence(text: string): SwarmFenceSplit {
  const lines = String(text ?? '').split(/\r?\n/);
  const kept: string[] = [];
  const entries: SwarmEntryParsed[] = [];
  let pending = false;
  let pendingCount = 0;
  let index = 0;

  while (index < lines.length) {
    if (!SWARM_FENCE_OPEN.test(lines[index])) {
      kept.push(lines[index]);
      index++;
      continue;
    }

    let close = -1;
    for (let cursor = index + 1; cursor < lines.length; cursor++) {
      if (SWARM_FENCE_CLOSE.test(lines[cursor])) {
        close = cursor;
        break;
      }
    }

    if (close < 0) {
      // 流式中：派单块还没闭合，从开围栏起的整段尾部都不属于正文
      pending = true;
      const tail = lines.slice(index).join('\n');
      pendingCount += (tail.match(SWARM_TASK_KEY) ?? []).length;
      break;
    }

    const parsed = parseSwarmEntries(lines.slice(index + 1, close).join('\n'));
    if (parsed) entries.push(...parsed);
    index = close + 1;
  }

  return { body: kept.join('\n'), entries, pending, pendingCount };
}

/** 子代理汇报里的一段：标题形如 `## 名字：任务 (已完成)` */
export interface SwarmReportSection {
  name: string;
  task: string;
  /** 标题括号里的状态文字，如 已完成 / 失败 / 已停止 / 运行中 */
  status: string;
  /** 该段标题之后到下一个标题之前的正文 */
  body: string;
}

const SWARM_REPORT_PREFIXES = ['[编排] 子代理汇报：', '[Swarm]', '[Hydra]'];
const SWARM_REPORT_HEADING = /^ {0,3}##[ \t]+(.*)$/;
const SWARM_REPORT_STATUS = /[（(]([^()（）]*)[)）][ \t]*$/;

/** 把 `## 名字：任务 (状态)` 拆成名字、任务和状态 */
function parseReportHeading(raw: string): { name: string; task: string; status: string } {
  const text = raw.trim();
  const statusMatch = SWARM_REPORT_STATUS.exec(text);
  const status = statusMatch ? statusMatch[1].trim() : '';
  const title = (statusMatch ? text.slice(0, statusMatch.index) : text).trim();
  const separator = title.search(/[：:]/);
  if (separator < 0) return { name: title, task: '', status };
  return {
    name: title.slice(0, separator).trim(),
    task: title.slice(separator + 1).trim(),
    status
  };
}

/**
 * 解析子代理汇报消息：正文以 `[编排] 子代理汇报：`（或旧的 `[Swarm]`、`[Hydra]`）开头时按 `## ` 标题切段，
 * 其他消息返回 null，交给普通消息渲染。
 */
export function parseSwarmReport(text: string): SwarmReportSection[] | null {
  const source = String(text ?? '').trimStart();
  if (!SWARM_REPORT_PREFIXES.some((prefix) => source.startsWith(prefix))) return null;

  const sections: SwarmReportSection[] = [];
  let current: { name: string; task: string; status: string; lines: string[] } | null = null;

  const flush = () => {
    if (!current) return;
    sections.push({
      name: current.name,
      task: current.task,
      status: current.status,
      body: current.lines.join('\n').trim()
    });
    current = null;
  };

  for (const line of source.split(/\r?\n/)) {
    const heading = SWARM_REPORT_HEADING.exec(line);
    if (heading) {
      flush();
      current = { ...parseReportHeading(heading[1]), lines: [] };
      continue;
    }
    if (current) current.lines.push(line);
  }
  flush();

  return sections.length > 0 ? sections : null;
}
