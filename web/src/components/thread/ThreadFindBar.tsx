// 会话内查找条：头部下方展开，输入关键词并给出「第 i / N 处」计数与上/下一个命中。
// 计数是字级命中（一处命中一次），不再是「第几条消息」；定位由调用方通过 activeFindMatch 交给时间线滚动。

import React, { useEffect, useRef } from 'react';
import { ArrowDownIcon, ArrowUpIcon, SearchIcon, XMarkIcon } from '../Icons';

export interface ThreadFindBarProps {
  query: string;
  onQueryChange: (value: string) => void;
  /** 当前命中序号（从 1 开始）；没有命中时为 0 */
  current: number;
  /** 命中总数：按字级命中计数，一条消息里出现 3 次就算 3 处 */
  total: number;
  onPrevious: () => void;
  onNext: () => void;
  onClose: () => void;
  /** 区分大小写开关的当前值；不传则不显示这个开关，行为与旧版完全一致 */
  caseSensitive?: boolean;
  onCaseSensitiveChange?: (value: boolean) => void;
}

export const ThreadFindBar: React.FC<ThreadFindBarProps> = ({
  query,
  onQueryChange,
  current,
  total,
  onPrevious,
  onNext,
  onClose,
  caseSensitive = false,
  onCaseSensitiveChange
}) => {
  const inputRef = useRef<HTMLInputElement | null>(null);

  // 展开即聚焦并选中已有内容，方便直接改写关键词
  useEffect(() => {
    inputRef.current?.focus();
    inputRef.current?.select();
  }, []);

  // 输入框失焦时 Esc 也能关掉查找条
  useEffect(() => {
    const handler = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      onClose();
    };
    window.addEventListener('keydown', handler);
    return () => window.removeEventListener('keydown', handler);
  }, [onClose]);

  const counter =
    query.trim() === '' ? '' : total === 0 ? '无匹配' : `第 ${current} / ${total} 处`;

  return (
    <div
      className="shrink-0 border-b border-[var(--border)] bg-[var(--screen)] pb-2"
      style={{
        paddingLeft: 'max(0.75rem, calc(0.75rem + var(--sal)))',
        paddingRight: 'max(0.75rem, calc(0.75rem + var(--sar)))'
      }}
    >
      <div className="mx-auto flex w-full max-w-[880px] items-center gap-1.5">
        <div className="flex min-w-0 flex-1 items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card)] px-2.5">
          <SearchIcon className="h-3.5 w-3.5 shrink-0 text-[var(--foreground-muted)]" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === 'Enter') {
                event.preventDefault();
                if (event.shiftKey) {
                  onPrevious();
                } else {
                  onNext();
                }
                return;
              }
              if (event.key === 'Escape') {
                event.preventDefault();
                onClose();
              }
            }}
            placeholder="在会话内查找"
            aria-label="在会话内查找"
            className="min-h-[32px] min-w-0 flex-1 bg-transparent text-xs text-[var(--foreground)] outline-none placeholder:text-[var(--foreground-muted)]"
          />
          {onCaseSensitiveChange && (
            <button
              type="button"
              onClick={() => onCaseSensitiveChange(!caseSensitive)}
              aria-label="区分大小写"
              aria-pressed={caseSensitive}
              title="区分大小写"
              className={`inline-flex h-6 shrink-0 items-center justify-center rounded-md px-1.5 text-mc-caption font-medium transition active:scale-95 ${
                caseSensitive
                  ? 'bg-[var(--user-bubble)] text-[var(--foreground)]'
                  : 'text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)]'
              }`}
            >
              Aa
            </button>
          )}
          {counter !== '' && (
            <span className="shrink-0 text-mc-caption tabular-nums text-[var(--foreground-muted)]">{counter}</span>
          )}
        </div>
        <button
          type="button"
          onClick={onPrevious}
          disabled={total === 0}
          aria-label="上一个匹配"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] transition active:scale-95 disabled:opacity-40"
        >
          <ArrowUpIcon className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={onNext}
          disabled={total === 0}
          aria-label="下一个匹配"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] transition active:scale-95 disabled:opacity-40"
        >
          <ArrowDownIcon className="h-3.5 w-3.5" />
        </button>
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭查找"
          className="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-lg text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] transition active:scale-95"
        >
          <XMarkIcon className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
};
