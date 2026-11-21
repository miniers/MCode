// 触控安全确认弹窗：防误触二次确认与拒绝审查反馈填写

import React, { useEffect, useRef, useState } from 'react';

interface ConfirmDialogProps {
  isOpen: boolean;
  title: string;
  message: string;
  confirmLabel?: string;
  // notice 是纯提示形态（对应桌面端 ConfirmTone 的 ConfirmNotice）：只有「知道了」，点击、Enter、Esc 都关闭
  confirmVariant?: 'primary' | 'danger' | 'success' | 'notice';
  cancelLabel?: string;
  isLoading?: boolean;
  // 可选的逐条明细（如待清理的条目），放在正文之后的限高滚动区里（对应桌面端 ConfirmDialog.Detail）
  details?: string[];
  withFeedbackInput?: boolean;
  feedbackPlaceholder?: string;
  initialFeedback?: string;
  onConfirm: (feedback?: string) => void;
  onCancel: () => void;
}

export const ConfirmDialog: React.FC<ConfirmDialogProps> = ({
  isOpen,
  title,
  message,
  confirmLabel,
  confirmVariant = 'primary',
  cancelLabel = '取消',
  isLoading = false,
  details,
  withFeedbackInput = false,
  feedbackPlaceholder = '请输入审查修改意见或拒绝理由（可选）...',
  initialFeedback = '',
  onConfirm,
  onCancel
}) => {
  const isNotice = confirmVariant === 'notice';
  const resolvedConfirmLabel = confirmLabel ?? (isNotice ? '知道了' : '确认');
  const [feedback, setFeedback] = useState('');
  const dialogRef = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    if (!isOpen) return;
    const dialog = dialogRef.current;
    const parentDialog = document.activeElement?.closest<HTMLDialogElement>('dialog');
    dialog?.showModal();
    return () => {
      dialog?.close();
      if (parentDialog?.open && !parentDialog.contains(document.activeElement)) {
        parentDialog.querySelector<HTMLElement>('button:not([disabled])')?.focus();
      }
    };
  }, [isOpen]);

  // 重置输入
  useEffect(() => {
    if (isOpen) {
      setFeedback(initialFeedback);
    }
  }, [isOpen]);

  if (!isOpen) return null;

  const getConfirmButtonClasses = () => {
    switch (confirmVariant) {
      case 'danger':
        return 'bg-rose-600 hover:bg-rose-700 active:bg-rose-800 text-white focus-visible:ring-rose-500';
      case 'success':
        return 'bg-emerald-600 hover:bg-emerald-700 active:bg-emerald-800 text-white focus-visible:ring-emerald-500';
      default:
        return 'bg-[var(--primary)] hover:opacity-90 active:opacity-80 text-white focus-visible:ring-[var(--primary)]';
    }
  };

  // 提示态没有取消按钮，Esc 与点遮罩都等同于按「知道了」
  const requestDismiss = () => {
    if (isLoading) return;
    if (isNotice) onConfirm(feedback);
    else onCancel();
  };

  return (
    <dialog
      ref={dialogRef}
      role="dialog"
      aria-modal="true"
      aria-label={title}
      className="fixed inset-0 m-0 h-full w-full max-h-none max-w-none border-0 z-50 flex items-center justify-center p-4 bg-black/60 backdrop-blur-sm animate-fadeIn"
      onCancel={(event) => {
        event.preventDefault();
        requestDismiss();
      }}
      onClick={(e) => {
        if (e.target === e.currentTarget) requestDismiss();
      }}
    >
      <div className="w-full max-w-md flex flex-col max-h-[calc(var(--app-height)-2rem)] bg-[var(--card)] rounded-2xl shadow-2xl border border-[var(--border)] overflow-hidden transform transition-all">
        <div className="min-h-0 flex-1 overflow-y-auto p-5 sm:p-6 space-y-4">
          <h3 className="text-lg font-semibold text-[var(--foreground)]">
            {title}
          </h3>

          <p className="text-sm text-[var(--foreground-muted)] leading-relaxed">
            {message}
          </p>

          {details && details.length > 0 && (
            <ul className="max-h-[40vh] overflow-y-auto overscroll-contain space-y-1.5 rounded-xl border border-[var(--border)] bg-[var(--screen)] p-2.5">
              {details.map((line, index) => (
                <li
                  key={`${index}-${line}`}
                  className="text-sm leading-relaxed text-[var(--foreground)] break-words [overflow-wrap:anywhere]"
                >
                  {line}
                </li>
              ))}
            </ul>
          )}

          {withFeedbackInput && (
            <div className="space-y-1.5 pt-1">
              <label className="block text-xs font-medium text-[var(--foreground)]">
                修改建议反馈:
              </label>
              <textarea
                aria-label="修改建议反馈"
                value={feedback}
                onChange={(e) => setFeedback(e.target.value)}
                placeholder={feedbackPlaceholder}
                rows={3}
                disabled={isLoading}
                className="w-full rounded-lg border border-[var(--input-border)] bg-[var(--screen)] p-3 text-sm text-[var(--foreground)] focus:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)] disabled:opacity-50"
              />
            </div>
          )}
        </div>

        <div className="shrink-0 flex items-center justify-end gap-3 px-5 py-3.5 bg-[var(--card-alt)] border-t border-[var(--border)]">
          {!isNotice && (
            <button
              type="button"
              disabled={isLoading}
              onClick={onCancel}
              className="min-h-[44px] px-4 py-2 text-sm font-medium text-[var(--foreground)] bg-[var(--card)] hover:bg-[var(--mc-overlay-hover)] active:bg-[var(--mc-overlay-pressed)] rounded-xl border border-[var(--border)] transition focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)] disabled:opacity-50"
            >
              {cancelLabel}
            </button>
          )}

          <button
            type="button"
            disabled={isLoading}
            onClick={() => onConfirm(feedback)}
            className={`min-h-[44px] px-5 py-2 text-sm font-medium rounded-xl shadow transition focus-visible:outline-none focus-visible:ring-2 disabled:opacity-50 inline-flex items-center gap-2 ${getConfirmButtonClasses()}`}
          >
            {isLoading && (
              <span className="w-4 h-4 border-2 border-white/30 border-t-white rounded-full animate-spin" />
            )}
            <span>{resolvedConfirmLabel}</span>
          </button>
        </div>
      </div>
    </dialog>
  );
};
