// 提问导航轨：贴在时间线右缘的迷你地图，每条用户提问一根刻度，宽屏悬停出预览卡、点击跳转，
// 触屏点轨道弹出可滚动的提问列表。刻度布局、当前项高亮与「+N」折叠照桌面端
// internal/ui/timeline_minimap.go（minimapMinEntries = 4 少于 4 条不显示；刻度高 3、宽 8…20，
// 间距宽松 8 / 最紧 4，总高预算 470，超预算只留最近一段并给最早一段一个「+N」格）。
// 当前所在提问由 ThreadView 的滚动容器与消息 DOM 位置判断（rAF 节流），不额外保存滚动状态。

import React from 'react';
import { createPortal } from 'react-dom';
import type { ThreadMessage } from '../ThreadTimeline';
import { isHiddenTimelineMessage } from '../ThreadTimeline';
import { formatRelativeTime } from '../../utils/date';

/** 少于这个条数不显示导航轨（与桌面端 minimapMinEntries 一致） */
export const MINIMAP_MIN_ENTRIES = 4;

const TICK_HEIGHT = 3;
const SPACING_ROOMY = 8;
const SPACING_TIGHT = 4;
const RAIL_BUDGET = 470;
const TICK_MIN_WIDTH = 6;
const TICK_MAX_WIDTH = 16;
/** 桌面端按正文长度算刻度权重，这里沿用同一公式（len/420，下限 0.15） */
const TICK_WEIGHT_LENGTH = 420;
const PREVIEW_BODY_RUNES = 160;

export interface QuestionMinimapEntry {
  messageID: string;
  /** 该提问在 messages 里的下标，用来按滚动位置判断当前提问 */
  index: number;
  /** 序号（1 基），用于无障碍标签与列表 */
  ordinal: number;
  /** 折叠空白后的正文（截断到 PREVIEW_BODY_RUNES） */
  text: string;
  /** 刻度宽度权重 0.15…1 */
  weight: number;
  createdAt: string;
}

export interface QuestionMinimapData {
  entries: QuestionMinimapEntry[];
  /** messageID → messages 下标 */
  order: Map<string, number>;
}

function foldWhitespace(text: string): string {
  return text.replace(/\s+/g, ' ').trim();
}

function clampRunes(text: string, limit: number): string {
  const runes = [...text];
  if (runes.length <= limit) return text;
  return `${runes.slice(0, limit).join('')}…`;
}

/** 从已加载的消息里挑出用户提问（顺序即时间线顺序），并算好刻度宽度。 */
export function buildQuestionMinimapData(messages: readonly ThreadMessage[]): QuestionMinimapData {
  const entries: QuestionMinimapEntry[] = [];
  const order = new Map<string, number>();
  messages.forEach((message, index) => {
    if (message.role !== 'user') return;
    if (isHiddenTimelineMessage(message)) return;
    const folded = foldWhitespace(message.text ?? '');
    const text = clampRunes(folded, PREVIEW_BODY_RUNES);
    if (text === '') return;
    order.set(message.id, index);
    entries.push({
      messageID: message.id,
      index,
      ordinal: entries.length + 1,
      text,
      weight: Math.max(0.15, Math.min(1, folded.length / TICK_WEIGHT_LENGTH)),
      createdAt: message.createdAt ?? ''
    });
  });
  return { entries, order };
}

interface MinimapRow {
  entry: QuestionMinimapEntry;
  /** 该行折叠了多少条更早的提问（0 表示普通刻度，>0 表示「+N」格） */
  plus: number;
}

interface MinimapPlan {
  rows: MinimapRow[];
  spacing: number;
}

/** 按可用高度规划行：放得下就按宽松间距排，放不下压紧间距，仍放不下就把最早一段折叠成「+N」。 */
function planMinimapRows(entries: readonly QuestionMinimapEntry[], available: number): MinimapPlan {
  if (entries.length === 0) return { rows: [], spacing: SPACING_ROOMY };
  const height = Math.max(60, available);
  const spacing = entries.length * (TICK_HEIGHT + SPACING_ROOMY) <= height ? SPACING_ROOMY : SPACING_TIGHT;
  const capacity = Math.max(1, Math.floor(height / (TICK_HEIGHT + spacing)));
  if (entries.length <= capacity) {
    return { rows: entries.map((entry) => ({ entry, plus: 0 })), spacing };
  }
  const hidden = Math.max(1, Math.min(capacity - 1, entries.length - 1));
  return {
    rows: [{ entry: entries[0], plus: hidden }, ...entries.slice(hidden).map((entry) => ({ entry, plus: 0 }))],
    spacing
  };
}

function useHoverCapable(): boolean {
  const [canHover, setCanHover] = React.useState<boolean>(() =>
    typeof window === 'undefined' || typeof window.matchMedia !== 'function'
      ? true
      : window.matchMedia('(hover: hover) and (pointer: fine)').matches
  );
  React.useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia('(hover: hover) and (pointer: fine)');
    const update = () => setCanHover(query.matches);
    update();
    query.addEventListener?.('change', update);
    return () => query.removeEventListener?.('change', update);
  }, []);
  return canHover;
}

function previewTitle(text: string): string {
  const words = text.split(' ').filter(Boolean);
  return clampRunes(words.slice(0, 8).join(' ') || text, 48);
}

export interface QuestionMinimapProps {
  data: QuestionMinimapData;
  /** 时间线的滚动容器：用来按视口位置判断当前提问 */
  containerRef: { readonly current: HTMLElement | null };
  onJump: (messageID: string) => void;
}

export const QuestionMinimap: React.FC<QuestionMinimapProps> = ({ data, containerRef, onJump }) => {
  const entries = data.entries;
  const canHover = useHoverCapable();
  const frameRef = React.useRef<HTMLDivElement | null>(null);
  const [available, setAvailable] = React.useState(RAIL_BUDGET);
  const [hovered, setHovered] = React.useState<number | null>(null);
  const [activeID, setActiveID] = React.useState<string | null>(null);
  const [listOpen, setListOpen] = React.useState(false);

  // 轨道高度跟随时间线可用高度（窗口变化、输入区伸缩都会改它）
  React.useEffect(() => {
    const frame = frameRef.current;
    if (!frame) return;
    const measure = () => {
      const height = frame.clientHeight;
      if (height > 0) setAvailable(Math.min(height, RAIL_BUDGET));
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(frame);
    return () => observer.disconnect();
  }, []);

  // 当前提问：取视口上方 35% 处的锚点消息，落在它之前（含自身）的最后一条提问
  React.useEffect(() => {
    const container = containerRef.current;
    if (!container || entries.length === 0) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      const elements = container.querySelectorAll<HTMLElement>('[data-message-id]');
      if (elements.length === 0) return;
      const box = container.getBoundingClientRect();
      const line = box.top + Math.min(box.height * 0.35, 240);
      let anchor = '';
      let best = Number.POSITIVE_INFINITY;
      elements.forEach((element) => {
        const rect = element.getBoundingClientRect();
        const distance =
          rect.top <= line && rect.bottom >= line
            ? 0
            : Math.min(Math.abs(rect.top - line), Math.abs(rect.bottom - line));
        if (distance < best) {
          best = distance;
          anchor = element.dataset.messageId ?? '';
        }
      });
      const anchorIndex = data.order.get(anchor);
      if (anchorIndex === undefined) return;
      let current: QuestionMinimapEntry | null = null;
      for (const entry of entries) {
        if (entry.index <= anchorIndex) current = entry;
        else break;
      }
      if (!current) return;
      const next = current.messageID;
      setActiveID((previous) => (previous === next ? previous : next));
    };
    const schedule = () => {
      if (frame === 0) frame = window.requestAnimationFrame(measure);
    };
    schedule();
    container.addEventListener('scroll', schedule, { passive: true });
    const observer = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(schedule);
    observer?.observe(container);
    return () => {
      container.removeEventListener('scroll', schedule);
      observer?.disconnect();
      if (frame !== 0) window.cancelAnimationFrame(frame);
    };
  }, [containerRef, data, entries]);

  // 当前提问消失（切会话/消息被删）时清掉高亮
  React.useEffect(() => {
    if (activeID && !data.order.has(activeID)) setActiveID(null);
  }, [activeID, data]);

  const plan = React.useMemo(() => planMinimapRows(entries, available), [entries, available]);
  const rowHeight = TICK_HEIGHT + plan.spacing;
  const railHeight = plan.rows.length * rowHeight;
  const plusRow = plan.rows.find((row) => row.plus > 0);
  const activeEntryIndex = activeID === null ? -1 : entries.findIndex((entry) => entry.messageID === activeID);
  const activeInPlusRow = plusRow !== undefined && activeEntryIndex >= 0 && activeEntryIndex < plusRow.plus;

  if (entries.length < MINIMAP_MIN_ENTRIES) return null;

  const jump = (messageID: string) => {
    setActiveID(messageID);
    onJump(messageID);
  };

  const rows = plan.rows.map((row, rowIndex) => {
    const isPlus = row.plus > 0;
    const isActive = isPlus ? activeInPlusRow : row.entry.messageID === activeID;
    const isHovered = hovered === rowIndex;
    const width = isPlus
      ? TICK_MAX_WIDTH
      : Math.round(TICK_MIN_WIDTH + (TICK_MAX_WIDTH - TICK_MIN_WIDTH) * row.entry.weight);
    const color = isActive ? 'var(--primary)' : isHovered ? 'var(--foreground)' : 'var(--foreground-muted)';
    const opacity = isActive ? 1 : isHovered ? 0.9 : 0.34;
    const label = isPlus
      ? `跳到更早的 ${row.plus} 条提问中的第 1 条`
      : `跳到第 ${row.entry.ordinal} 条提问`;
    const tick = (
      <span
        aria-hidden="true"
        className="ml-auto block rounded-full transition-[width,opacity] duration-150"
        style={{ height: TICK_HEIGHT, width, backgroundColor: color, opacity }}
      />
    );

    return (
      <div
        key={isPlus ? `plus-${row.plus}` : row.entry.messageID}
        className="absolute right-0 flex items-center justify-end"
        style={{ top: rowIndex * rowHeight, height: rowHeight, width: 20 }}
      >
        {canHover ? (
          <button
            type="button"
            aria-label={label}
            title={isPlus ? `更早的 ${row.plus} 条提问` : row.entry.text}
            onMouseEnter={() => setHovered(rowIndex)}
            onMouseLeave={() => setHovered((previous) => (previous === rowIndex ? null : previous))}
            onFocus={() => setHovered(rowIndex)}
            onBlur={() => setHovered((previous) => (previous === rowIndex ? null : previous))}
            onClick={() => jump(row.entry.messageID)}
            className="relative flex h-full w-5 items-center justify-end rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-primary"
          >
            {isPlus ? (
              <span className="ml-auto text-mc-caption font-medium leading-none" style={{ color, opacity: Math.max(opacity, 0.6) }}>
                +{row.plus}
              </span>
            ) : (
              tick
            )}
            {isHovered && !isPlus && (
              <div className="pointer-events-none absolute right-full top-1/2 z-30 mr-2 w-[260px] -translate-y-1/2 rounded-xl border border-border bg-card px-3 py-2 text-left shadow-xl">
                <div className="flex items-baseline gap-2">
                  <span className="shrink-0 text-mc-caption text-muted">#{row.entry.ordinal}</span>
                  <span className="min-w-0 truncate text-caption font-medium text-foreground">
                    {previewTitle(row.entry.text)}
                  </span>
                </div>
                <div className="mt-1 line-clamp-4 text-mc-caption leading-[1.45] text-muted">{row.entry.text}</div>
                {row.entry.createdAt && (
                  <div className="mt-1 text-mc-caption text-muted">{formatRelativeTime(row.entry.createdAt)}</div>
                )}
              </div>
            )}
          </button>
        ) : (
          <span className="flex h-full w-5 items-center justify-end">{isPlus ? null : tick}</span>
        )}
      </div>
    );
  });

  return (
    <>
      <div
        ref={frameRef}
        className="pointer-events-none absolute bottom-14 top-2 z-20 flex w-6 items-center justify-end pr-1"
        style={{
          // 与「跳到最新」按钮同一套定位：宽屏贴在居中正文列（880px）右外侧，窄屏回退为贴右边 8px
          right: 'max(0.5rem, calc((100% - 880px) / 2 - 2rem))'
        }}
      >
        <div
          role="navigation"
          aria-label="提问导航"
          data-question-minimap="true"
          className="pointer-events-auto relative w-5"
          style={{ height: railHeight }}
        >
          {rows}
          {!canHover && (
            <button
              type="button"
              aria-label={`查看 ${entries.length} 条提问`}
              onClick={() => setListOpen(true)}
              className="absolute inset-0 z-10 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-primary"
            />
          )}
        </div>
      </div>

      {listOpen &&
        typeof document !== 'undefined' &&
        createPortal(
          <div
            className="sheet-layer"
            onClick={(event) => {
              if (event.target === event.currentTarget) setListOpen(false);
            }}
          >
            <div className="settings-sheet" role="dialog" aria-modal="true" aria-label="提问列表">
              <div className="sheet-handle" />
              <div className="mb-2 flex items-center justify-between gap-2">
                <div className="text-footnote font-semibold text-foreground">提问列表</div>
                <button
                  type="button"
                  onClick={() => setListOpen(false)}
                  className="rounded-full px-2 py-1 text-caption text-muted hover:bg-user"
                >
                  关闭
                </button>
              </div>
              <ul className="max-h-[60vh] space-y-1 overflow-y-auto">
                {entries.map((entry) => {
                  const isActive = entry.messageID === activeID;
                  return (
                    <li key={entry.messageID}>
                      <button
                        type="button"
                        onClick={() => {
                          jump(entry.messageID);
                          setListOpen(false);
                        }}
                        aria-current={isActive ? 'true' : undefined}
                        className={`flex w-full items-start gap-2 rounded-xl px-2 py-2 text-left transition ${
                          isActive ? 'bg-user' : 'hover:bg-user'
                        }`}
                      >
                        <span
                          className={`mt-[2px] w-5 shrink-0 text-mc-caption ${
                            isActive ? 'font-semibold text-primary' : 'text-muted'
                          }`}
                        >
                          {entry.ordinal}
                        </span>
                        <span className="min-w-0 flex-1">
                          <span
                            className={`block truncate text-caption ${
                              isActive ? 'font-medium text-primary' : 'text-foreground'
                            }`}
                          >
                            {previewTitle(entry.text)}
                          </span>
                          <span className="mt-0.5 block truncate text-mc-caption text-muted">
                            {entry.createdAt ? formatRelativeTime(entry.createdAt) : ''}
                          </span>
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </div>
          </div>,
          document.body
        )}
    </>
  );
};

export default QuestionMinimap;
