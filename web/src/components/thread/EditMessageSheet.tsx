// 编辑用户消息后重发：预填原文，确认后由调用方分支/新建会话再发送改后的文本。
// 发送失败时输入框里的文本保持不动（对应桌面 internal/ui/timeline_menu.go:321 submitInlineEdit）。

import React, { useCallback, useEffect, useState } from 'react';
import { ExclamationTriangleIcon, XMarkIcon } from '../Icons';

export interface EditMessageSheetProps {
  isOpen: boolean;
  /** 预填的原始消息正文 */
  initialText: string;
  /** 提交改后的文本；抛错时由本组件展示错误并保留输入 */
  onSubmit: (text: string) => Promise<void>;
  onClose: () => void;
}

export const EditMessageSheet: React.FC<EditMessageSheetProps> = ({
  isOpen,
  initialText,
  onSubmit,
  onClose
}) => {
  const [text, setText] = useState(initialText);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // 每次打开都回到原文，并清掉上一次的错误
  useEffect(() => {
    if (!isOpen) return;
    setText(initialText);
    setSubmitting(false);
    setError(null);
  }, [initialText, isOpen]);

  const requestClose = useCallback(() => {
    if (submitting) return;
    onClose();
  }, [onClose, submitting]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      requestClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isOpen, requestClose]);

  const handleSubmit = useCallback(async () => {
    if (submitting) return;
    const trimmed = text.trim();
    if (!trimmed) {
      setError('消息内容不能为空');
      return;
    }
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit(trimmed);
    } catch (err) {
      setError(err instanceof Error ? err.message : '编辑后重发失败');
    } finally {
      setSubmitting(false);
    }
  }, [onSubmit, submitting, text]);

  if (!isOpen) return null;

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={requestClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="edit-message-sheet-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full mb-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">消息</p>
            <h2 id="edit-message-sheet-title" className="mt-0.5 text-headline font-bold text-[var(--foreground)]">
              编辑后重发
            </h2>
            <p className="mt-1 text-xs text-[var(--foreground-muted)] leading-relaxed">
              改后的文本会从这条消息之前最近的助手答复处分支出去发送；之前没有答复时新建会话。
            </p>
          </div>
          <button
            type="button"
            onClick={requestClose}
            disabled={submitting}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px] disabled:opacity-40"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          <label className="block space-y-1">
            <span className="text-xs font-semibold text-[var(--foreground)]">消息内容</span>
            <textarea
              value={text}
              onChange={(event) => setText(event.target.value)}
              rows={8}
              disabled={submitting}
              aria-label="编辑消息内容"
              className="pairing-input min-h-32 resize-y w-full min-w-0 text-sm disabled:opacity-60"
            />
          </label>

          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
            </div>
          )}
        </div>

        <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 min-w-0 max-w-full w-full flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={requestClose}
            disabled={submitting}
            className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] disabled:opacity-50 transition"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => void handleSubmit()}
            disabled={submitting || text.trim().length === 0}
            className="primary-button min-h-[44px] min-w-[88px] disabled:opacity-50"
          >
            {submitting ? '发送中…' : '保存并发送'}
          </button>
        </div>
      </section>
    </div>
  );
};
