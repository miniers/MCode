// 计划全文阅读弹层：审批卡只展示摘要，完整计划在这里读，底部保留与卡片上完全相同的操作。
// 外层结构沿用项目内 settings-sheet 抽屉（sheet-layer + sheet-handle + 标题栏 + 滚动主体）。

import React from 'react';
import { createPortal } from 'react-dom';
import { XMarkIcon } from './Icons';
import { MarkdownView } from './MarkdownView';

export interface PlanReaderSheetProps {
  /** 计划标题，为空时显示兜底文案 */
  title: string;
  /** 计划正文（Markdown 原文） */
  text: string;
  /** 标题上方的小标签，默认「计划确认」；时间线里的计划传「实施计划」 */
  eyebrow?: string;
  /** 底部操作区：与审批卡上是同一批按钮，回调也相同 */
  actions?: React.ReactNode;
  onClose: () => void;
}

export const PlanReaderSheet: React.FC<PlanReaderSheetProps> = ({
  title,
  text,
  eyebrow = '计划确认',
  actions,
  onClose
}) => {
  const closeRef = React.useRef<HTMLButtonElement | null>(null);

  React.useEffect(() => {
    closeRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      onClose();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [onClose]);

  return createPortal(
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="plan-reader-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="mb-3 flex w-full min-w-0 shrink-0 items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">{eyebrow}</p>
            <h2
              id="plan-reader-title"
              className="mt-0.5 break-words [overflow-wrap:anywhere] text-headline font-bold text-[var(--foreground)]"
            >
              {title || '完整计划'}
            </h2>
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={onClose}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="min-h-0 w-full min-w-0 max-w-full flex-1 overflow-y-auto overflow-x-hidden pr-0.5">
          <div className="select-text">
            <MarkdownView text={text} />
          </div>
        </div>

        {actions ? (
          <div className="mt-3 w-full min-w-0 shrink-0 border-t border-[var(--border)] pt-3">
            {actions}
          </div>
        ) : null}
      </section>
    </div>,
    document.body
  );
};
