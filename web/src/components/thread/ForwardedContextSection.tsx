import React, { useEffect, useMemo, useState } from 'react';
import type { RemoteForwardedContext } from '../../types/contract';
import { parseForwardedTurns, splitForwardedTail, type ForwardedTurn } from '../../utils/forwardedContext';
import { ChevronDownIcon, ChevronRightIcon } from '../Icons';
import { MarkdownView } from '../MarkdownView';

interface ForwardedContextSectionProps {
  threadID: string;
  forwardedContext: RemoteForwardedContext | null;
  forkedFromThreadID: string | null;
  forkedFromTitle: string;
  isContinuation: boolean;
  showHistory: boolean;
  onOpenOrigin?: (threadID: string) => void;
}

const ForwardedTurnItem: React.FC<{ turn: ForwardedTurn }> = React.memo(({ turn }) => {
  if (turn.role === 'user') {
    return (
      <div className="flex justify-end">
        <div className="max-w-[85%] rounded-[20px] px-3.5 py-2.5 bg-[var(--user-bubble)] text-[16px] leading-[1.45] text-[var(--foreground)] whitespace-pre-wrap break-words">
          {turn.text}
        </div>
      </div>
    );
  }
  if (turn.role === 'omitted') {
    return (
      <div className="flex items-center gap-2 py-0.5 text-mc-caption text-[var(--foreground-muted)] select-none">
        <span className="h-px flex-1 bg-[var(--border)]" />
        <span>…</span>
        <span className="h-px flex-1 bg-[var(--border)]" />
      </div>
    );
  }
  return (
    <div className="min-w-0 text-[var(--foreground)]">
      <MarkdownView text={turn.text} />
    </div>
  );
});

/**
 * 渲染转发会话带入的历史上下文与来源分隔线，交互与桌面端 renderForwardedContextRow 对齐：
 * 默认展开最后一轮对话（最后一条 user 及之后），更早轮次折叠为「更早 N 条」按钮。
 */
export const ForwardedContextSection: React.FC<ForwardedContextSectionProps> = ({
  threadID,
  forwardedContext,
  forkedFromThreadID,
  forkedFromTitle,
  isContinuation,
  showHistory,
  onOpenOrigin
}) => {
  const [expanded, setExpanded] = useState(false);

  useEffect(() => {
    setExpanded(false);
  }, [threadID]);

  const parsed = useMemo(() => {
    if (!showHistory || !forwardedContext || !forwardedContext.content.trim()) {
      return null;
    }
    const turns = parseForwardedTurns(forwardedContext.content);
    if (turns.length === 0) return null;
    return splitForwardedTail(turns);
  }, [forwardedContext, showHistory]);

  const resolvedTitle = forkedFromTitle.trim() || forwardedContext?.sourceTitle?.trim() || '';
  const hasOriginLine = Boolean(forkedFromThreadID || parsed);

  if (!parsed && !hasOriginLine) {
    return null;
  }

  return (
    <div className="space-y-3">
      {parsed && (
        <div className="opacity-70 space-y-3" data-testid="forwarded-context-history">
          {parsed.earlierCount > 0 && (
            <div>
              <button
                type="button"
                aria-expanded={expanded}
                onClick={() => setExpanded((prev) => !prev)}
                className="inline-flex items-center gap-1 rounded-md px-2 py-1 text-mc-caption text-[var(--foreground-muted)] transition hover:bg-[var(--user-bubble)] active:opacity-75"
              >
                {expanded ? (
                  <ChevronDownIcon className="w-3 h-3 shrink-0" />
                ) : (
                  <ChevronRightIcon className="w-3 h-3 shrink-0" />
                )}
                <span>更早 {parsed.earlierCount} 条</span>
              </button>
            </div>
          )}

          {(parsed.earlierCount === 0 || expanded) && parsed.earlier.length > 0 && (
            <div className="space-y-3">
              {parsed.earlier.map((turn, index) => (
                <ForwardedTurnItem key={`earlier-${index}`} turn={turn} />
              ))}
            </div>
          )}

          {parsed.tail.map((turn, index) => (
            <ForwardedTurnItem key={`tail-${index}`} turn={turn} />
          ))}
        </div>
      )}

      {hasOriginLine && (
        <div className="flex items-center gap-2 pb-1 text-mc-caption text-[var(--foreground-muted)]">
          <span className="h-px flex-1 bg-[var(--border)]" />
          {forkedFromThreadID && resolvedTitle && onOpenOrigin ? (
            <button
              type="button"
              onClick={() => onOpenOrigin(forkedFromThreadID)}
              title="打开来源会话"
              className="max-w-[70%] shrink-0 truncate rounded-md px-1.5 py-0.5 text-[var(--primary)] transition hover:bg-[var(--user-bubble)] active:opacity-75"
            >
              {isContinuation ? `继续自「${resolvedTitle}」` : `分叉自「${resolvedTitle}」`}
            </button>
          ) : resolvedTitle ? (
            <span className="max-w-[70%] shrink-0 truncate px-1.5 py-0.5">
              {isContinuation ? `继续自「${resolvedTitle}」` : `分叉自「${resolvedTitle}」`}
            </span>
          ) : (
            <span className="shrink-0 px-1.5 py-0.5">
              {isContinuation ? '继续自其它会话' : '分叉自其它会话'}
            </span>
          )}
          <span className="h-px flex-1 bg-[var(--border)]" />
        </div>
      )}
    </div>
  );
};
