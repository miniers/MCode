import { useEffect, useState } from 'react';

import { useRemoteSettingsApi } from '../context/remoteSettingsApi';
import type { RemoteSettingsQuickReply } from '../types/generated';

/**
 * 快捷回复条：照桌面端 internal/ui/composer_quick_replies.go 的规则，
 * 在输入框上方显示一排快捷回复 chip，点击只把正文填进草稿，不发送。
 *
 * 数据只读来自设置快照 remote/settings.get 的 quickReplies；宿主不支持 settings.get 时不显示。
 * 「更多」里放超出可见数量的其余回复（手机上一屏最多 3 个）。
 */

/** 标签最大长度（桌面端 quickReplyLabel 上限 40 字，超出截断加「…」）。 */
export const QUICK_REPLY_LABEL_MAX = 40;

/** 一屏最多显示几个 chip，其余收进「更多」。 */
export const QUICK_REPLY_VISIBLE_LIMIT = 3;

export interface QuickReplyItem {
  label: string;
  message: string;
}

export interface QuickReplyVisibilityInput {
  /** 当前草稿正文 */
  draft: string;
  /** 是否正在跑回复 */
  isRunning: boolean;
  /** 待发送附件数量 */
  attachmentCount: number;
  /** 会话里是否已有消息；桌面端草稿页（没有消息）不显示快捷回复 */
  hasMessages: boolean;
}

/**
 * 与桌面端 quickRepliesVisible 同义（internal/ui/composer_quick_replies.go:91）：
 * 会话已有消息、不在运行、草稿为空、没有附件时才显示。
 *
 * 桌面端原条件是 `len(s.messages) == 0` 时隐藏，也就是草稿页（新会话）不显示；
 * Web 端拿不到消息列表，由调用方传 hasMessages（不传按已有消息处理），接好线后才与桌面端一致。
 */
export function quickRepliesVisible(input: QuickReplyVisibilityInput): boolean {
  if (!input.hasMessages) return false;
  if (input.draft.trim() !== '') return false;
  if (input.isRunning) return false;
  if (input.attachmentCount > 0) return false;
  return true;
}

/** 没有 label 时用正文第一行做标签（桌面端 firstLineLabel 同义）。 */
export function firstLineLabel(message: string): string {
  const line = message.split(/\r?\n/).find((item) => item.trim() !== '');
  return line ? line.trim() : '';
}

/** 标签截断到 QUICK_REPLY_LABEL_MAX 个字（按字符个数，不按 UTF-16 长度）。 */
export function truncateQuickReplyLabel(label: string): string {
  const chars = Array.from(label);
  if (chars.length <= QUICK_REPLY_LABEL_MAX) return label;
  return `${chars.slice(0, QUICK_REPLY_LABEL_MAX).join('')}…`;
}

/**
 * 把设置快照里的 quickReplies 折算成 chip 列表：
 * 去掉正文为空的项；label 为空时取正文第一行；两者都空时兜底「快捷回复」；标签统一截断。
 * 前 visibleLimit 个进入 primary，其余进入 overflow（「更多」）。
 */
export function buildQuickReplyItems(
  replies: RemoteSettingsQuickReply[] | null | undefined,
  visibleLimit: number = QUICK_REPLY_VISIBLE_LIMIT
): { primary: QuickReplyItem[]; overflow: QuickReplyItem[] } {
  const items: QuickReplyItem[] = [];
  for (const reply of replies ?? []) {
    const message = (reply?.message ?? '').trim();
    if (message === '') continue;
    const rawLabel = ((reply?.label ?? '').trim() || firstLineLabel(message)).trim();
    items.push({
      label: truncateQuickReplyLabel(rawLabel || '快捷回复'),
      // 保留正文原文（只去首尾空白），因为要原样填进草稿。
      message: reply?.message ?? ''
    });
  }
  const limit = Math.max(0, visibleLimit);
  return { primary: items.slice(0, limit), overflow: items.slice(limit) };
}

const CHIP_CLASS =
  'h-8 max-w-[240px] shrink-0 truncate rounded-full border border-[var(--border)] bg-[var(--card)] px-3 text-xs text-[var(--foreground-muted)] transition hover:text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] active:scale-95';

interface QuickReplyBarProps {
  draft: string;
  isRunning: boolean;
  attachmentCount: number;
  hasMessages: boolean;
  onDraftChange: (value: string) => void;
  onFocusComposer: () => void;
}

export function QuickReplyBar({
  draft,
  isRunning,
  attachmentCount,
  hasMessages,
  onDraftChange,
  onFocusComposer
}: QuickReplyBarProps) {
  const api = useRemoteSettingsApi();
  const [moreOpen, setMoreOpen] = useState(false);
  const { primary, overflow } = buildQuickReplyItems(api.snapshot?.quickReplies);
  const visible =
    api.supportsSettings && primary.length > 0 && quickRepliesVisible({ draft, isRunning, attachmentCount, hasMessages });

  // 条子隐藏时收掉「更多」浮层，避免下次显示时残留。
  useEffect(() => {
    if (!visible) setMoreOpen(false);
  }, [visible]);

  if (!visible) return null;

  const apply = (message: string) => {
    onDraftChange(message);
    setMoreOpen(false);
    onFocusComposer();
  };

  return (
    <div className="relative mb-2">
      <div className="flex items-center gap-2 overflow-x-auto pb-0.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden">
        {primary.map((item) => (
          <button
            key={`${item.label}:${item.message}`}
            type="button"
            title={item.label}
            onClick={() => apply(item.message)}
            className={CHIP_CLASS}
          >
            {item.label}
          </button>
        ))}
        {overflow.length > 0 && (
          <button
            type="button"
            aria-expanded={moreOpen}
            onClick={() => setMoreOpen((prev) => !prev)}
            className={CHIP_CLASS}
          >
            更多 {overflow.length}
          </button>
        )}
      </div>
      {moreOpen && overflow.length > 0 && (
        <>
          {/* 点击外部关闭 */}
          <button
            type="button"
            aria-label="关闭快捷回复列表"
            className="fixed inset-0 z-30 cursor-default bg-transparent"
            onClick={() => setMoreOpen(false)}
          />
          <div
            className="absolute bottom-full left-0 z-40 mb-1 w-full max-w-[320px] overflow-hidden rounded-lg border border-[var(--border)] bg-[var(--card)] p-1 shadow-lg"
          >
            {overflow.map((item) => (
              <button
                key={`${item.label}:${item.message}`}
                type="button"
                title={item.label}
                onClick={() => apply(item.message)}
                className="block w-full truncate rounded-md px-2.5 py-1.5 text-left text-xs text-[var(--foreground-muted)] hover:bg-[var(--card-alt,var(--card))] hover:text-[var(--foreground)]"
              >
                {item.label}
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}

export default QuickReplyBar;
