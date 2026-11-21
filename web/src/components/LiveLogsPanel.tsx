// 实时终端/命令日志订阅流面板：可嵌入 ThreadMenuSheet 栈内页或独立打开（宽屏 .menu-side-panel / 窄屏 SheetFrame size="panel"），
// 支持流筛选胶囊、流变化分隔标题、复制/清空、智能贴底滚动与断线降级提示。

import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import '../styles/skeleton.css';
import { useRemoteMeta } from '../context/RemoteContext';
import { useLiveLogStream } from '../hooks/useLiveLogStream';
import type { RemoteLogChunk, RemoteLogStreamInfo } from '../types/generated';
import {
  ArrowDownIcon,
  ArrowPathIcon,
  CheckCircleIcon,
  CommandLineIcon
} from './Icons';
import { SheetFrame } from './sheet/SheetFrame';
import { MenuSheetHeader } from './thread/menu/MenuSheetHeader';
import { useMenuPageChrome } from './thread/menu/menuPageChrome';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';
import { useIsWideSheet } from './thread/menu/useIsWideSheet';

export const LOG_SCROLL_BOTTOM_THRESHOLD_PX = 24;

export function isScrolledNearBottom(
  scrollHeight: number,
  scrollTop: number,
  clientHeight: number
): boolean {
  return scrollHeight - scrollTop - clientHeight <= LOG_SCROLL_BOTTOM_THRESHOLD_PX;
}

export function logSourceLabel(source: RemoteLogChunk['source'] | RemoteLogStreamInfo['source']): string {
  return source === 'script' ? '脚本' : '命令';
}

export function logStatusMeta(status: RemoteLogStreamInfo['status']): {
  label: string;
  dotClass: string;
  textClass: string;
} {
  switch (status) {
    case 'running':
      return {
        label: '运行中',
        dotClass: 'bg-[var(--status-running)]',
        textClass: 'text-[var(--status-running)]'
      };
    case 'error':
      return {
        label: '异常',
        dotClass: 'bg-[var(--mc-danger)]',
        textClass: 'text-[var(--mc-danger)]'
      };
    case 'done':
    default:
      return {
        label: '已完成',
        dotClass: 'bg-[var(--mc-success)]',
        textClass: 'text-[var(--mc-success)]'
      };
  }
}

export interface LiveLogsPanelProps {
  threadID: string | null;
  embedded?: boolean;
  isOpen?: boolean;
  onClose?: () => void;
}

function LogsSkeleton() {
  return (
    <div className="mc-skeleton gap-mc-2 p-mc-3" role="status" aria-live="polite" aria-busy="true">
      <span className="mc-skeleton-label">正在连接实时日志…</span>
      <div className="mc-skeleton-block mc-skeleton-line" style={{ width: '72%' }} />
      <div className="mc-skeleton-block mc-skeleton-line mc-skeleton-line--sub" style={{ width: '88%' }} />
      <div className="mc-skeleton-block mc-skeleton-line mc-skeleton-line--sub" style={{ width: '64%' }} />
    </div>
  );
}

export const LiveLogsPanel = React.forwardRef<ThreadMenuPageHandle, LiveLogsPanelProps>(
  function LiveLogsPanel({ threadID, embedded = true, isOpen = true, onClose }, ref) {
    const { connectionState } = useRemoteMeta();
    const chrome = useMenuPageChrome();
    const wide = useIsWideSheet();

    useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

    const active = embedded ? isOpen : isOpen !== false;
    const {
      streams,
      chunks,
      truncated,
      loading,
      error,
      supported,
      clearLocal,
      resubscribe
    } = useLiveLogStream(threadID, active);

    const [activeStreamID, setActiveStreamID] = useState<string | null>(null);
    const [copied, setCopied] = useState(false);
    const [showScrollBottom, setShowScrollBottom] = useState(false);

    const scrollRef = useRef<HTMLDivElement | null>(null);
    const autoScrollRef = useRef(true);
    const copyTimerRef = useRef<number | null>(null);

    useEffect(
      () => () => {
        if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
      },
      []
    );

    useEffect(() => {
      setActiveStreamID(null);
      setCopied(false);
      autoScrollRef.current = true;
      setShowScrollBottom(false);
    }, [threadID]);

    // 如果当前筛选流已不在列表里，回退到「全部」。
    useEffect(() => {
      if (activeStreamID && !streams.some((stream) => stream.streamID === activeStreamID)) {
        setActiveStreamID(null);
      }
    }, [activeStreamID, streams]);

    const streamMap = useMemo(
      () => new Map(streams.map((stream) => [stream.streamID, stream])),
      [streams]
    );

    const filteredChunks = useMemo(() => {
      if (!activeStreamID) return chunks;
      return chunks.filter((chunk) => chunk.streamID === activeStreamID);
    }, [activeStreamID, chunks]);

    const refreshButton = useMemo(
      () => (
        <button
          type="button"
          className="icon-button"
          onClick={resubscribe}
          disabled={!supported || !threadID || loading || connectionState !== 'connected'}
          aria-label="重新订阅实时日志"
          title="刷新"
        >
          <ArrowPathIcon className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
        </button>
      ),
      [connectionState, loading, resubscribe, supported, threadID]
    );

    const setChromeTrailing = chrome.setTrailing;
    const hasOuterMenuHeader = embedded && (chrome.leading === 'back' || chrome.leading === 'close');
    useEffect(() => {
      if (!hasOuterMenuHeader || !setChromeTrailing) return;
      setChromeTrailing(refreshButton);
      return () => setChromeTrailing(null);
    }, [hasOuterMenuHeader, refreshButton, setChromeTrailing]);

    // 自动滚底：有新日志块到达且未手动暂停时贴底。
    useEffect(() => {
      if (!autoScrollRef.current) return;
      const element = scrollRef.current;
      if (!element) return;
      element.scrollTop = element.scrollHeight;
    }, [filteredChunks, activeStreamID]);

    const handleScroll = useCallback(() => {
      const element = scrollRef.current;
      if (!element) return;
      const nearBottom = isScrolledNearBottom(
        element.scrollHeight,
        element.scrollTop,
        element.clientHeight
      );
      autoScrollRef.current = nearBottom;
      setShowScrollBottom(!nearBottom);
    }, []);

    const scrollToBottom = useCallback(() => {
      autoScrollRef.current = true;
      setShowScrollBottom(false);
      const element = scrollRef.current;
      if (element) {
        element.scrollTop = element.scrollHeight;
      }
    }, []);

    const handleCopyLogs = useCallback(async () => {
      if (filteredChunks.length === 0 || !navigator.clipboard?.writeText) return;
      const payload = filteredChunks.map((chunk) => chunk.text).join('');
      try {
        await navigator.clipboard.writeText(payload);
        setCopied(true);
        if (copyTimerRef.current !== null) window.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = window.setTimeout(() => {
          copyTimerRef.current = null;
          setCopied(false);
        }, 1500);
      } catch {
        // 剪贴板写入失败时保持原状
      }
    }, [filteredChunks]);

    const handleClear = useCallback(() => {
      clearLocal();
      autoScrollRef.current = true;
      setShowScrollBottom(false);
    }, [clearLocal]);

    if (!active) return null;

    const renderedEntries: React.ReactNode[] = [];
    let previousStreamID: string | null = null;
    for (let index = 0; index < filteredChunks.length; index += 1) {
      const chunk = filteredChunks[index];
      if (chunk.streamID !== previousStreamID) {
        previousStreamID = chunk.streamID;
        const streamInfo = streamMap.get(chunk.streamID);
        const statusMeta = streamInfo ? logStatusMeta(streamInfo.status) : null;
        renderedEntries.push(
          <div
            key={`sep-${chunk.streamID}-${chunk.seq}-${index}`}
            className={`flex items-center justify-between gap-2 border-b border-[var(--mc-hairline)] pb-1 text-mc-caption text-[var(--mc-text-secondary)] ${
              index > 0 ? 'mt-3 pt-1' : ''
            }`}
          >
            <span className="flex min-w-0 items-center gap-1.5">
              <span className="shrink-0 rounded-mc-xs bg-[var(--mc-surface-card)] px-1.5 py-0.5 text-[var(--mc-text-secondary)] border border-[var(--mc-border)]">
                {logSourceLabel(streamInfo?.source ?? chunk.source)}
              </span>
              <span className="truncate font-medium text-[var(--mc-text-primary)]">
                {streamInfo?.title || chunk.streamID}
              </span>
            </span>
            {statusMeta && (
              <span className={`inline-flex shrink-0 items-center gap-1 ${statusMeta.textClass}`}>
                <span className={`h-1.5 w-1.5 rounded-full ${statusMeta.dotClass}`} aria-hidden="true" />
                <span>{statusMeta.label}</span>
              </span>
            )}
          </div>
        );
      }
      renderedEntries.push(
        <span key={`chunk-${chunk.streamID}-${chunk.seq}-${index}`}>{chunk.text}</span>
      );
    }

    const body = (
      <div className="relative flex min-h-0 flex-1 flex-col gap-mc-3">
        {!hasOuterMenuHeader && !embedded && (
          <MenuSheetHeader
            eyebrow="终端输出"
            title="实时日志"
            labelledBy="live-logs-title"
            leading="close"
            onLeading={() => onClose?.()}
            trailing={refreshButton}
            showHandle={!wide}
            closeLabel="关闭实时日志"
          />
        )}

        {/* 顶部紧凑控制条：流筛选胶囊 + 复制 / 清空 */}
        <div className="flex shrink-0 flex-wrap items-center justify-between gap-2">
          <div className="filter-row !mt-0 min-w-0 flex-1 py-0.5" role="toolbar" aria-label="日志流筛选">
            <button
              type="button"
              className={`filter-chip touch-hit-44 ${activeStreamID === null ? 'filter-chip--active' : ''}`}
              aria-pressed={activeStreamID === null}
              onClick={() => setActiveStreamID(null)}
            >
              <span>全部</span>
            </button>
            {streams.map((stream) => {
              const selected = activeStreamID === stream.streamID;
              const statusMeta = logStatusMeta(stream.status);
              return (
                <button
                  key={stream.streamID}
                  type="button"
                  className={`filter-chip touch-hit-44 max-w-[180px] ${selected ? 'filter-chip--active' : ''}`}
                  aria-pressed={selected}
                  onClick={() => setActiveStreamID(stream.streamID)}
                >
                  <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${statusMeta.dotClass}`} aria-hidden="true" />
                  <span className="truncate">{stream.title || stream.streamID}</span>
                </button>
              );
            })}
          </div>

          <div className="flex shrink-0 items-center gap-1.5">
            <button
              type="button"
              onClick={() => void handleCopyLogs()}
              disabled={filteredChunks.length === 0}
              aria-label={copied ? '已复制日志' : '复制当前日志'}
              className="secondary-button min-h-[32px] touch-hit-44 px-2.5 text-mc-caption disabled:opacity-40"
            >
              {copied ? (
                <>
                  <CheckCircleIcon className="h-3.5 w-3.5 text-[var(--mc-success)]" aria-hidden="true" />
                  <span>已复制</span>
                </>
              ) : (
                <span>复制</span>
              )}
            </button>
            <button
              type="button"
              onClick={handleClear}
              disabled={chunks.length === 0}
              aria-label="清空日志视图"
              className="secondary-button min-h-[32px] touch-hit-44 px-2.5 text-mc-caption disabled:opacity-40"
            >
              清空
            </button>
          </div>
        </div>

        {error && (
          <div className="inline-alert inline-alert--danger !my-0 shrink-0" role="alert">
            <span className="min-w-0 select-text break-words">{error}</span>
            <button type="button" className="inline-alert__action touch-hit-44" onClick={resubscribe}>
              重试
            </button>
          </div>
        )}

        {truncated && (
          <div
            role="status"
            className="shrink-0 rounded-mc-md border border-[var(--mc-border)] bg-[var(--mc-surface-card)] px-3 py-1 text-mc-caption text-[var(--mc-text-secondary)]"
          >
            早期日志已截断，仅保留最近输出
          </div>
        )}

        {!supported ? (
          <div className="empty-state">
            <div className="empty-state__icon">
              <CommandLineIcon className="h-6 w-6" aria-hidden="true" />
            </div>
            <h2>当前 Mac 暂不支持实时日志</h2>
          </div>
        ) : loading && chunks.length === 0 ? (
          <div className="flex-1 min-h-0 rounded-mc-lg border border-[var(--mc-border)] bg-[var(--mc-surface-canvas)]">
            <LogsSkeleton />
          </div>
        ) : filteredChunks.length === 0 ? (
          <div className="empty-state rounded-mc-lg border border-[var(--mc-border)] bg-[var(--mc-surface-canvas)]">
            <div className="empty-state__icon">
              <CommandLineIcon className="h-6 w-6" aria-hidden="true" />
            </div>
            <h2>暂无实时日志</h2>
          </div>
        ) : (
          <div className="relative flex min-h-0 flex-1 flex-col">
            <div
              ref={scrollRef}
              onScroll={handleScroll}
              role="log"
              aria-live="polite"
              aria-label="实时命令输出"
              className="flex-1 min-h-0 overflow-y-auto overscroll-contain rounded-mc-lg border border-[var(--mc-border)] bg-[var(--mc-surface-canvas)] p-3 font-mono text-mc-code select-text whitespace-pre-wrap break-all text-[var(--mc-text-primary)]"
            >
              {renderedEntries}
            </div>
            {showScrollBottom && (
              <div className="pointer-events-none absolute inset-x-0 bottom-3 flex justify-center">
                <button
                  type="button"
                  onClick={scrollToBottom}
                  aria-label="回到底部"
                  className="pointer-events-auto inline-flex min-h-[36px] touch-hit-44 items-center gap-1.5 rounded-mc-pill border border-[var(--mc-border)] bg-[var(--mc-surface-popover)] px-3 text-mc-caption font-medium text-[var(--mc-text-primary)] shadow-mc-elev-float transition active:scale-95"
                >
                  <ArrowDownIcon className="h-3.5 w-3.5" aria-hidden="true" />
                  <span>回到底部</span>
                </button>
              </div>
            )}
          </div>
        )}
      </div>
    );

    if (embedded) {
      return body;
    }

    if (wide) {
      return (
        <div className="menu-side-layer" role="presentation" onMouseDown={() => onClose?.()}>
          <div
            className="menu-side-panel"
            role="dialog"
            aria-modal="true"
            aria-labelledby="live-logs-title"
            tabIndex={-1}
            onMouseDown={(event) => event.stopPropagation()}
          >
            {body}
          </div>
        </div>
      );
    }

    return (
      <SheetFrame
        onRequestClose={() => onClose?.()}
        size="panel"
        labelledBy="live-logs-title"
      >
        {body}
      </SheetFrame>
    );
  }
);
