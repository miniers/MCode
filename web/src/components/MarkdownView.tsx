import React from 'react';
import { createPortal } from 'react-dom';
import ReactMarkdown, { defaultUrlTransform, type Components } from 'react-markdown';
import { CheckCircleIcon, LayoutIcon, XMarkIcon } from './Icons';
import { MarkdownImage } from './MarkdownImage';
import remarkGfm from 'remark-gfm';
import remarkBreaks from 'remark-breaks';
import type { Element, Root, RootContent } from 'hast';
import type { PluggableList } from 'unified';
import {
  getLoadedMathPlugins,
  loadMathPlugins,
  shouldUseMath,
  type MathPlugins
} from '../utils/lazyMath';
import {
  getHighlightLanguagesVersion,
  highlightCode,
  subscribeHighlightLanguages
} from '../utils/highlight';
import { parseSwarmEntries, type SwarmEntryParsed } from '../utils/markdown';
import { useOverflowFade } from '../hooks/useOverflowFade';
import '../styles/overflow-fade.css';
import { highlightRangesFrom, type OccurrenceRange } from '../utils/findHighlight';
import {
  isThreadMentionHref,
  navigateToThreadMention,
  stripReferencedThreadBlock,
  threadMentionIDFromHref
} from '../utils/threadMention';

// 派单块解析放在 utils/markdown.ts（时间线也要用它拆分正文），这里保持原有导出入口。
export { parseSwarmEntries };
export type { SwarmEntryParsed };

export const HEADING_SIZE: Record<1 | 2 | 3 | 4, string> = {
  1: 'text-mc-title',
  2: 'text-mc-prose',
  3: 'text-mc-body',
  4: 'text-mc-body'
};

/**
 * mcode:// 会话引用不是 http(s) 链接，默认 urlTransform 会把它清成空串；其余 URL 仍走默认清洗。
 * file:// 本地图片同理：默认清洗会把它清空，导致 MarkdownImage 拿不到路径。
 */
function mentionSafeUrlTransform(url: string): string {
  return isThreadMentionHref(url) || /^file:/i.test(url) ? url : defaultUrlTransform(url);
}

export const CodeBlock = React.memo(function CodeBlock({ content, language, children }: { content: string; language: string; children?: React.ReactNode }) {
  // 冷门语言的语法包是首次遇到才下载的：注册完成会推进版本号，这里据此重新高亮
  const languageVersion = React.useSyncExternalStore(subscribeHighlightLanguages, getHighlightLanguagesVersion, getHighlightLanguagesVersion);
  const html = React.useMemo(() => highlightCode(content, language), [content, language, languageVersion]);
  // 横向溢出时给滚动容器写 --fade-left/--fade-right，窄屏由 overflow-fade.css 做边缘遮罩。
  const scrollerRef = React.useRef<HTMLPreElement>(null);
  useOverflowFade(scrollerRef);
  const [copied, setCopied] = React.useState(false);
  const copyTimerRef = React.useRef<number | null>(null);
  const copyAttemptRef = React.useRef(0);

  React.useEffect(() => () => {
    copyAttemptRef.current += 1;
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
  }, []);

  const handleCopy = async () => {
    const attempt = ++copyAttemptRef.current;
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    setCopied(false);
    if (!navigator.clipboard?.writeText) return;
    try {
      await navigator.clipboard.writeText(content);
      if (copyAttemptRef.current !== attempt) return;
      setCopied(true);
      copyTimerRef.current = window.setTimeout(() => {
        copyTimerRef.current = null;
        setCopied(false);
      }, 1500);
    } catch {
      // 复制失败时保持复制图标。
    }
  };

  return (
    <div className="group relative min-w-0 max-w-full rounded-xl">
      <pre
        ref={scrollerRef}
        className="overflow-fade p-3 pr-14 rounded-xl bg-[var(--user-bubble)] border border-[var(--border)] text-[var(--foreground)] text-mc-footnote leading-relaxed font-mono overflow-x-auto whitespace-pre select-text"
      >
        {children ?? (html !== null ? (
          // highlight.js escapes the source text, so the HTML only contains its own span tags.
          <code className="hljs-code" dangerouslySetInnerHTML={{ __html: html }} />
        ) : (
          content
        ))}
      </pre>
      <button
        type="button"
        onClick={() => void handleCopy()}
        aria-label={copied ? '已复制' : '复制代码'}
        className="absolute right-1.5 top-1.5 inline-flex h-8 w-8 items-center justify-center rounded-lg border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)] opacity-60 transition-opacity duration-mc-fast motion-reduce:transition-none [@media(hover:hover)_and_(pointer:fine)]:opacity-0 [@media(hover:hover)_and_(pointer:fine)]:group-hover:opacity-100 focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
      >
        {copied ? <CheckCircleIcon className="h-4 w-4" /> : (
          <svg className="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
            <rect x="8" y="8" width="11" height="11" rx="2" />
            <path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" />
          </svg>
        )}
      </button>
    </div>
  );
});

export function SwarmBlock({ content }: { content: string; language: string }) {
  const entries = parseSwarmEntries(content);
  if (entries && entries.length > 0) {
    return (
      <div className="my-2 min-w-0 max-w-full overflow-hidden rounded-2xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] p-3 space-y-2 select-text">
        <div className="flex items-center gap-1.5 text-xs font-semibold text-[var(--primary)]">
          <span className="w-2 h-2 rounded-full bg-[var(--primary)] animate-pulse shrink-0" />
          <span>派出 {entries.length} 个子代理</span>
        </div>
        <div className="space-y-1.5 pt-0.5">
          {entries.map((entry, idx) => (
            <div
              key={`bee-${idx}`}
              className="flex items-start gap-2 text-xs text-[var(--foreground)] bg-[var(--card)] rounded-xl p-2 border border-[var(--border)]"
            >
              {entry.name && (
                <span className="shrink-0 font-medium text-[var(--primary)] bg-[var(--user-bubble)] px-1.5 py-0.5 rounded text-mc-caption">
                  {entry.name}
                </span>
              )}
              <span className="min-w-0 flex-1 truncate font-medium">
                {entry.task}
              </span>
            </div>
          ))}
        </div>
      </div>
    );
  }
  return (
    <div className="my-2 flex items-center gap-2 px-3 py-2 rounded-xl bg-[var(--user-bubble)] text-xs text-[var(--foreground-muted)] italic select-none">
      <span className="w-3.5 h-3.5 border-2 border-[var(--foreground-muted)] border-t-transparent rounded-full animate-spin shrink-0" />
      <span>正在派出子代理…</span>
    </div>
  );
}

type TableElementProps = React.ClassAttributes<HTMLTableElement> & React.TableHTMLAttributes<HTMLTableElement>;

const TABLE_CLASS = 'w-full border-collapse text-mc-prose leading-[20px] text-[var(--foreground)] select-text';

/**
 * 清洗 GFM 表格的单个单元格：单元格里不能有换行，折成空格；竖线是列分隔符，必须转义成 `\|`。
 * 逐字对应桌面端 tableCellMarkdown（internal/ui/table_popup.go:195）。
 */
export function tableCellMarkdown(source: string): string {
  const flattened = source
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .replace(/\n/g, ' ')
    .trim();
  return flattened.replace(/\|/g, '\\|');
}

/**
 * 把单元格重建为 GFM 表格文本：第一行是表头，第二行是分隔行，每行按最宽行补齐列数（GFM 要求每行列数一致）。
 * 对齐信息不参与复制，分隔行统一用无对齐的 `---`；rows 为空时返回空串。
 * 逐字对应桌面端 tableCellsMarkdown（internal/ui/table_popup.go:204）。
 */
export function tableCellsMarkdown(rows: ReadonlyArray<ReadonlyArray<string>>): string {
  let cols = 0;
  for (const row of rows) {
    if (row.length > cols) cols = row.length;
  }
  if (cols === 0) return '';

  const separator = `|${' --- |'.repeat(cols)}`;
  const lines: string[] = [];
  rows.forEach((row, index) => {
    const cells: string[] = [];
    for (let column = 0; column < cols; column += 1) {
      cells.push(` ${column < row.length ? tableCellMarkdown(row[column]) : ''} `);
    }
    lines.push(`|${cells.join('|')}|`);
    if (index === 0) lines.push(separator);
  });
  return lines.map((line) => `${line}\n`).join('');
}

/**
 * 从已渲染的表格 DOM 读出各行单元格的文本：thead 在前，tfoot 在最后，tbody 按顺序居中。
 * 单元格文本取 textContent，等价于桌面端从解析树取出的单元格原文（tableCellSource）。
 */
export function tableCellsFromElement(table: HTMLTableElement): string[][] {
  const rows: string[][] = [];
  const collect = (section: HTMLTableSectionElement | null) => {
    if (!section) return;
    for (const row of Array.from(section.rows)) {
      rows.push(Array.from(row.cells).map((cell) => cell.textContent ?? ''));
    }
  };
  collect(table.tHead);
  for (const body of Array.from(table.tBodies)) collect(body);
  collect(table.tFoot);
  return rows;
}

// 表格放大：滚动容器右上角一个小按钮，点开全屏弹层看同一张表（对应桌面端 table_popup.go）。
function TableBlock({ tableProps, children }: { tableProps: TableElementProps; children?: React.ReactNode }) {
  const [expanded, setExpanded] = React.useState(false);
  const [copyStatus, setCopyStatus] = React.useState<'idle' | 'copied' | 'error'>('idle');
  const closeRef = React.useRef<HTMLButtonElement | null>(null);
  const popupTableRef = React.useRef<HTMLTableElement | null>(null);
  const copyTimerRef = React.useRef<number | null>(null);

  const flashCopyStatus = React.useCallback((status: 'copied' | 'error') => {
    setCopyStatus(status);
    if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    copyTimerRef.current = window.setTimeout(() => {
      copyTimerRef.current = null;
      setCopyStatus('idle');
    }, status === 'copied' ? 1500 : 2500);
  }, []);

  React.useEffect(
    () => () => {
      if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
    },
    []
  );

  // 复制按钮：把弹层里这张表重建为 GFM 表格写进剪贴板，单元格里的竖线已转义
  // （对应桌面端 table_popup.go:255 的 renderTableExpandButton 标题栏复制按钮）。
  const handleCopyMarkdown = React.useCallback(async () => {
    const table = popupTableRef.current;
    if (!table) return;
    const markdown = tableCellsMarkdown(tableCellsFromElement(table));
    if (markdown === '') return;
    if (!navigator.clipboard?.writeText) {
      flashCopyStatus('error');
      return;
    }
    try {
      await navigator.clipboard.writeText(markdown);
      flashCopyStatus('copied');
    } catch {
      flashCopyStatus('error');
    }
  }, [flashCopyStatus]);

  React.useEffect(() => {
    if (!expanded) return;
    closeRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      setExpanded(false);
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [expanded]);

  return (
    <div className="relative min-w-0 max-w-full">
      <div className="min-w-0 max-w-full overflow-x-auto rounded-xl border border-[var(--border)] pr-14">
        <table className={TABLE_CLASS} {...tableProps}>
          {children}
        </table>
      </div>
      <button
        type="button"
        onClick={() => setExpanded(true)}
        aria-label="放大查看表格"
        title="放大查看表格"
        className="absolute right-1.5 top-1.5 inline-flex h-7 items-center gap-1 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2 text-mc-caption font-medium text-[var(--foreground-muted)] shadow-sm transition active:scale-95"
      >
        <LayoutIcon className="h-3.5 w-3.5" />
        <span>放大</span>
      </button>
      {expanded &&
        createPortal(
          <div
            role="dialog"
            aria-modal="true"
            aria-label="表格全屏查看"
            className="fixed inset-0 z-[80] flex flex-col bg-[var(--screen)]"
            style={{
              paddingTop: 'env(safe-area-inset-top, 0px)',
              paddingBottom: 'env(safe-area-inset-bottom, 0px)'
            }}
          >
            <header className="flex shrink-0 items-center justify-between gap-3 border-b border-[var(--border)] px-3 py-2">
              <span className="truncate text-mc-prose font-semibold text-[var(--foreground)]">表格</span>
              <div className="flex shrink-0 items-center gap-2">
                <button
                  type="button"
                  onClick={() => void handleCopyMarkdown()}
                  aria-label="复制为 Markdown"
                  title="复制为 Markdown"
                  className="inline-flex h-9 items-center rounded-full border border-[var(--border)] bg-[var(--card)] px-3 text-mc-footnote font-medium text-[var(--foreground)] transition active:scale-95"
                >
                  {copyStatus === 'copied' ? '已复制' : copyStatus === 'error' ? '复制失败' : '复制为 Markdown'}
                </button>
                <button
                  ref={closeRef}
                  type="button"
                  onClick={() => setExpanded(false)}
                  aria-label="关闭表格"
                  className="flex h-9 w-9 shrink-0 items-center justify-center rounded-full border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] transition active:scale-95"
                >
                  <XMarkIcon className="h-4 w-4" />
                </button>
              </div>
            </header>
            <div className="min-h-0 flex-1 overflow-auto overscroll-contain px-3 py-3 [&_thead]:sticky [&_thead]:top-0 [&_thead]:z-10">
              <div className="w-max min-w-full rounded-xl border border-[var(--border)]">
                <table ref={popupTableRef} className={TABLE_CLASS}>
                  {children}
                </table>
              </div>
            </div>
          </div>,
          document.body
        )}
    </div>
  );
}

const components: Components = {
  h1: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[1]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  h2: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[2]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  h3: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[3]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  h4: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[4]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  h5: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[4]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  h6: ({ node, ...props }) => (
    <p
      className={`${HEADING_SIZE[4]} font-semibold text-[var(--foreground)] leading-relaxed select-text min-w-0 break-words [overflow-wrap:anywhere]`}
      {...props}
    />
  ),
  p: ({ node, ...props }) => (
    <p
      className="text-[16px] leading-[23px] text-[var(--foreground)] font-sans select-text min-w-0 break-words [overflow-wrap:anywhere]"
      {...props}
    />
  ),
  ul: ({ node, ...props }) => (
    <ul className="list-disc pl-5 space-y-1 min-w-0 max-w-full" {...props} />
  ),
  ol: ({ node, start, ...props }) => (
    <ol start={start} className="list-decimal pl-5 space-y-1 min-w-0 max-w-full" {...props} />
  ),
  li: ({ node, ...props }) => (
    <li
      className="text-[16px] leading-[23px] text-[var(--foreground)] select-text min-w-0 break-words [overflow-wrap:anywhere]"
      {...props}
    />
  ),
  blockquote: ({ node, ...props }) => (
    <blockquote
      className="border-l-2 border-[var(--border)] pl-3 space-y-1.5 text-[var(--foreground-muted)] min-w-0 max-w-full"
      {...props}
    />
  ),
  hr: ({ node, ...props }) => (
    <hr className="border-[var(--border)]" {...props} />
  ),
  table: ({ node, children, ...props }) => (
    <TableBlock tableProps={props}>{children}</TableBlock>
  ),
  thead: ({ node, ...props }) => (
    <thead className="bg-[var(--user-bubble)]" {...props} />
  ),
  th: ({ node, style, ...props }) => (
    <th
      style={style}
      className="px-3 py-1.5 font-semibold border-b border-[var(--border)] whitespace-nowrap"
      {...props}
    />
  ),
  tr: ({ node, ...props }) => (
    <tr className="border-t border-[var(--border)] first:border-t-0" {...props} />
  ),
  td: ({ node, style, ...props }) => (
    <td style={style} className="px-3 py-1.5 align-top" {...props} />
  ),
  strong: ({ node, ...props }) => (
    <strong className="font-semibold text-[var(--foreground)]" {...props} />
  ),
  em: ({ node, ...props }) => (
    <em className="italic text-[var(--foreground)]" {...props} />
  ),
  del: ({ node, ...props }) => (
    <del className="line-through text-[var(--foreground-muted)]" {...props} />
  ),
  a: ({ node, href, ...props }) => {
    // 会话引用链接显示成胶囊，点击跳到被引用的会话（与桌面端时间线的显示一致）。
    const mentionThreadID = threadMentionIDFromHref(href);
    if (mentionThreadID !== '') {
      return (
        <button
          type="button"
          title="打开被引用的会话"
          onClick={() => navigateToThreadMention(mentionThreadID)}
          className="mx-0.5 inline max-w-full truncate align-baseline rounded-md bg-[color-mix(in_srgb,var(--primary)_16%,transparent)] px-1.5 py-0.5 font-medium text-[var(--primary)] transition hover:bg-[color-mix(in_srgb,var(--primary)_28%,transparent)] focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
        >
          {props.children}
        </button>
      );
    }
    return (
      <a
        target="_blank"
        rel="noreferrer noopener"
        className="text-[var(--primary)] underline underline-offset-2 break-words [overflow-wrap:anywhere] rounded focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
        {...props}
        href={href}
      />
    );
  },
  code: ({ node, ...props }) => (
    <code
      className="px-1 py-0.5 rounded bg-[var(--user-bubble)] font-mono text-mc-footnote text-[var(--foreground)] break-words [overflow-wrap:anywhere]"
      {...props}
    />
  ),
  pre: ({ node, children }) => {
    const firstChild = node?.children?.[0];
    const codeNode = firstChild && firstChild.type === 'element' ? firstChild : undefined;

    let language = '';
    if (codeNode?.properties?.className) {
      const className = codeNode.properties.className;
      if (Array.isArray(className)) {
        const entry = className.find((cls) => typeof cls === 'string' && cls.startsWith('language-'));
        if (entry && typeof entry === 'string') {
          language = entry.slice('language-'.length);
        }
      }
    }

    let text = '';
    if (codeNode?.children) {
      // 查找高亮会在代码块里插入 mark，这里按递归取文本，避免命中处的字符被丢掉
      text = codeNode.children.map(hastText).join('');
    }
    if (text.endsWith('\n')) {
      text = text.slice(0, -1);
    }

    const langLower = language.toLowerCase().trim();
    if (langLower.startsWith('swarm') || langLower.startsWith('hydra')) {
      return <SwarmBlock content={text} language={language} />;
    }
    // 代码块命中：CodeBlock 走 highlight.js 的 innerHTML，mark 塞不进去，这里退回纯文本渲染让高亮看得见；
    // 没有命中时仍走原来的语法着色，行为不变。
    if (codeNode && hasMarkDescendant(codeNode)) {
      return <CodeBlock content={text} language={language}>{children}</CodeBlock>;
    }
    return <CodeBlock content={text} language={language} />;
  },
  // 图片交给 MarkdownImage：远程图直接显示，工作区内本地图经 files.read 读成 data URL，其余回退成链接
  img: ({ src, alt }) => <MarkdownImage src={src} alt={alt} />
};

// remark-gfm 的邮箱自动链接在运行时构造带后行断言的正则，iOS 16.4 之前的 Safari 会抛错并让整页空白；
// 不支持时退回不带 GFM 的解析（表格、删除线按纯文本显示）。
const supportsLookbehind = (() => {
  try {
    new RegExp('(?<=a)b');
    return true;
  } catch {
    return false;
  }
})();
const REMARK_PLUGINS_LEAD: PluggableList = supportsLookbehind ? [remarkGfm] : [];
const REMARK_PLUGINS_TAIL: PluggableList = [remarkBreaks];
// 不含公式时用这组模块级常量：引用稳定，Markdown 不会因为插件数组变化重新解析。
const REMARK_PLUGINS: PluggableList = [...REMARK_PLUGINS_LEAD, ...REMARK_PLUGINS_TAIL];
const REHYPE_PLUGINS: PluggableList = [];

// 数学公式（remark-math 只认成对的 $…$ 与 $$…$$，代码块里的美元符号不会被当作公式；
// rehype-katex 把 math 节点转成 KaTeX HTML，样式随插件一起按需加载）体积最大，因此懒加载：
// 插件按需加载，加载完成后按同一份插件对象缓存数组，保证引用稳定。
let cachedRemarkMath: MathPlugins | null = null;
let cachedRemarkPlugins: PluggableList = REMARK_PLUGINS;
let cachedRehypeMath: MathPlugins | null = null;
let cachedRehypePlugins: PluggableList = REHYPE_PLUGINS;

function remarkPluginsFor(math: MathPlugins | null): PluggableList {
  if (!math) return REMARK_PLUGINS;
  if (cachedRemarkMath !== math) {
    cachedRemarkMath = math;
    cachedRemarkPlugins = [...REMARK_PLUGINS_LEAD, ...math.remark, ...REMARK_PLUGINS_TAIL];
  }
  return cachedRemarkPlugins;
}

function rehypePluginsFor(math: MathPlugins | null): PluggableList {
  if (!math) return REHYPE_PLUGINS;
  if (cachedRehypeMath !== math) {
    cachedRehypeMath = math;
    cachedRehypePlugins = [...math.rehype];
  }
  return cachedRehypePlugins;
}

/** 查找高亮参数：query 与 ThreadTimeline 的 findQuery 同源，activeOccurrence 是「本条消息内第几处」（从 0 开始）。 */
export interface MarkdownHighlight {
  query: string;
  caseSensitive?: boolean;
  activeOccurrence?: number | null;
}

// 查找高亮的底色：普通命中用柔和黄底，当前命中用醒目黄底并带 data-find-active 供滚动定位；
// 类名写字面量，Tailwind 才能扫到（index.css 里没有 mark 的全局样式）。
const FIND_MARK_CLASS = [
  'find-mark',
  'rounded-mc-xs',
  'bg-amber-200/70',
  'text-inherit',
  'dark:bg-amber-300/30'
];
const FIND_MARK_ACTIVE_CLASS = [
  'find-mark',
  'find-mark-active',
  'rounded-mc-xs',
  'bg-amber-400',
  'text-black',
  'dark:bg-amber-300',
  'dark:text-black'
];

// 这些元素里的文本不可能也不该被高亮（公式由 katex 重新渲染，脚本类元素更不碰）。
const HIGHLIGHT_SKIP_TAGS = new Set(['script', 'style', 'textarea', 'noscript']);

function isMathElement(node: Element): boolean {
  const className = node.properties?.className;
  if (!Array.isArray(className)) return false;
  return className.some((entry) => typeof entry === 'string' && entry.startsWith('math'));
}

// 把一段文本按命中切成 text / mark 节点；properties 里的 dataFindActive 会被 hast 工具渲染成 data-find-active 属性。
function buildMarkedNodes(value: string, ranges: OccurrenceRange[], activeOccurrence: number | null): RootContent[] {
  const nodes: RootContent[] = [];
  let cursor = 0;
  for (const range of ranges) {
    if (range.start > cursor) {
      nodes.push({ type: 'text', value: value.slice(cursor, range.start) });
    }
    const active = activeOccurrence !== null && range.occurrence === activeOccurrence;
    const mark: Element = {
      type: 'element',
      tagName: 'mark',
      properties: {
        className: active ? FIND_MARK_ACTIVE_CLASS : FIND_MARK_CLASS,
        ...(active ? { dataFindActive: 'true' } : {})
      },
      children: [{ type: 'text', value: value.slice(range.start, range.end) }]
    };
    nodes.push(mark);
    cursor = range.end;
  }
  if (cursor < value.length) {
    nodes.push({ type: 'text', value: value.slice(cursor) });
  }
  return nodes;
}

// 把 hast 子树里的文本拼回来（跳过内部的 mark 包装），代码块的语言/正文提取靠它
function hastText(node: RootContent): string {
  if (node.type === 'text') return node.value;
  if (node.type === 'element') return node.children.map(hastText).join('');
  return '';
}

function hasMarkDescendant(node: RootContent): boolean {
  if (node.type !== 'element') return false;
  if (node.tagName === 'mark') return true;
  return node.children.some(hasMarkDescendant);
}

/**
 * 查找高亮的 rehype 插件：按「渲染后文本节点的文档顺序」累计 occurrence，与 findHighlight 的
 * highlightRangesFrom 共用同一套计数。代码块与行内代码同样包 mark（文本未变），链接文字照常包，
 * href/src 等属性一律不动。
 */
function rehypeFindHighlight(query: string, caseSensitive: boolean, activeOccurrence: number | null) {
  return (tree: Root): void => {
    let occurrence = 0;
    const visit = (parent: { children: RootContent[] }): void => {
      const next: RootContent[] = [];
      for (const child of parent.children) {
        if (child.type === 'text') {
          const result = highlightRangesFrom(child.value, query, caseSensitive, occurrence);
          occurrence = result.nextOccurrence;
          if (result.ranges.length === 0) {
            next.push(child);
          } else {
            next.push(...buildMarkedNodes(child.value, result.ranges, activeOccurrence));
          }
          continue;
        }
        if (child.type === 'element' && !HIGHLIGHT_SKIP_TAGS.has(child.tagName) && !isMathElement(child)) {
          visit(child);
        }
        next.push(child);
      }
      parent.children = next;
    };
    visit(tree);
  };
}

export interface MarkdownViewProps {
  text: string;
  /** 会话内查找高亮；不传（或不给 query）时行为与旧版完全一致 */
  highlight?: MarkdownHighlight;
  math?: boolean;
}

type RehypePlugins = React.ComponentProps<typeof ReactMarkdown>['rehypePlugins'];

export const MarkdownView = React.memo(function MarkdownView({ text, highlight, math: mathEnabled = true }: MarkdownViewProps) {
  const query = (highlight?.query ?? '').trim();
  const caseSensitive = Boolean(highlight?.caseSensitive);
  const activeOccurrence = highlight?.activeOccurrence ?? null;

  // 发送时会把被引用会话的背景资料块拼在正文前面：时间线里只显示正文和引用胶囊，
  // 因此按 START/END 标记整段剥离（桌面端不做剥离，Web 端按设计不铺开资料块）。
  const displayText = React.useMemo(() => stripReferencedThreadBlock(text), [text]);

  // 公式插件懒加载：文本里可能有公式时才启用 KaTeX，未加载就先用无公式插件渲染一遍，
  // 加载完成后由 state 触发重渲染，带上 remark-math / rehype-katex 再解析一次。
  const mayHaveMath = React.useMemo(() => shouldUseMath(displayText, mathEnabled), [displayText, mathEnabled]);
  const [math, setMath] = React.useState<MathPlugins | null>(() => getLoadedMathPlugins());

  React.useEffect(() => {
    if (!mayHaveMath || math) return;
    let cancelled = false;
    loadMathPlugins()
      .then((plugins) => {
        if (!cancelled) setMath(plugins);
      })
      .catch(() => {
        // 公式插件加载失败时保持无公式渲染，正文照常显示
      });
    return () => {
      cancelled = true;
    };
  }, [mayHaveMath, math]);

  // 不含公式的文本即使插件已加载也不启用，保持插件数组引用稳定
  const activeMath = mayHaveMath ? math : null;
  const remarkPlugins = remarkPluginsFor(activeMath);
  // 只有查找生效时才换插件数组，其余时候沿用模块级常量，Markdown 解析结果不受影响。
  const rehypePlugins = React.useMemo<RehypePlugins>(() => {
    const base = rehypePluginsFor(activeMath);
    return query === ''
      ? base
      : [() => rehypeFindHighlight(query, caseSensitive, activeOccurrence), ...base];
  }, [query, caseSensitive, activeOccurrence, activeMath]);

  if (!displayText.trim()) {
    return <p className="text-[var(--foreground-muted)]">（无内容）</p>;
  }

  return (
    <div className="min-w-0 max-w-full space-y-2">
      <ReactMarkdown
        remarkPlugins={remarkPlugins}
        rehypePlugins={rehypePlugins}
        components={components}
        urlTransform={mentionSafeUrlTransform}
      >
        {displayText}
      </ReactMarkdown>
    </div>
  );
});

export default MarkdownView;
