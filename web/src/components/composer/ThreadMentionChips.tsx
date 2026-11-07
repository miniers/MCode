import React from 'react';
import { draftThreadMentionLabel, type DraftThreadMention } from '../../utils/threadMention';

export interface ThreadMentionChipsProps {
  /** 草稿上方已选中的会话引用（顺序即展示顺序）。 */
  mentions: ReadonlyArray<DraftThreadMention>;
  /** 点 ✕ 时移除该引用。 */
  onRemove: (threadID: string) => void;
  className?: string;
}

/**
 * 草稿上方的「@会话标题」胶囊行，对应桌面端 renderDraftThreadMentions：
 * 每颗胶囊 = 💬 + 截断后的标题 + ✕ 移除按钮。
 * 没有任何引用时不渲染任何内容。
 */
export const ThreadMentionChips = React.memo(function ThreadMentionChips({
  mentions,
  onRemove,
  className
}: ThreadMentionChipsProps) {
  if (mentions.length === 0) return null;

  return (
    <div className={`flex flex-wrap items-center gap-1.5 ${className ?? ''}`}>
      {mentions.map((mention) => {
        const title = mention.title !== '' ? mention.title : '未命名会话';
        return (
          <span
            key={mention.threadID}
            className="inline-flex h-7 max-w-[240px] shrink-0 items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--card)] py-0.5 pl-2 pr-1 text-xs text-[var(--foreground)]"
          >
            <span aria-hidden="true" className="shrink-0">
              💬
            </span>
            <span className="min-w-0 truncate">{draftThreadMentionLabel(title)}</span>
            <button
              type="button"
              title={`移除引用 ${title}`}
              aria-label={`移除引用 ${title}`}
              onClick={() => onRemove(mention.threadID)}
              className="flex h-4 w-4 shrink-0 items-center justify-center rounded-full text-mc-caption leading-none text-[var(--foreground-muted)] transition hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] active:scale-95"
            >
              ✕
            </button>
          </span>
        );
      })}
    </div>
  );
});

export default ThreadMentionChips;
