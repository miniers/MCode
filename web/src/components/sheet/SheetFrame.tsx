// 底部抽屉的共用外壳：统一遮罩点击、Esc 关闭、dialog 语义与两种卡片样式，各 Sheet 只负责卡片内容。
// 迁移期间仍保留原有 class（thread-action-sheet / settings-sheet），视觉与现有抽屉完全一致。

import React, { useEffect } from 'react';

export const SHEET_MENU_CARD_CLASS = 'thread-action-sheet';
export const SHEET_PANEL_CARD_CLASS =
  'settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden';

export interface SheetFrameProps {
  /** 点遮罩时调用；closeOnEscape 为 true 时按 Esc 也调用 */
  onRequestClose: () => void;
  /** 卡片 section 的 aria-labelledby */
  labelledBy?: string;
  /** 默认 'panel'；menu 用 SHEET_MENU_CARD_CLASS，panel 用 SHEET_PANEL_CARD_CLASS */
  size?: 'menu' | 'panel';
  /** 追加到卡片 class 末尾 */
  cardClassName?: string;
  /** 默认 false，避免和现有组件自带的 Esc 处理重复 */
  closeOnEscape?: boolean;
  children: React.ReactNode;
}

export function SheetFrame({
  onRequestClose,
  labelledBy,
  size = 'panel',
  cardClassName,
  closeOnEscape = false,
  children,
}: SheetFrameProps) {
  useEffect(() => {
    if (!closeOnEscape) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onRequestClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [closeOnEscape, onRequestClose]);

  const cardClass = [size === 'menu' ? SHEET_MENU_CARD_CLASS : SHEET_PANEL_CARD_CLASS, cardClassName]
    .filter(Boolean)
    .join(' ');

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onRequestClose}>
      <section
        className={cardClass}
        role="dialog"
        aria-modal="true"
        aria-labelledby={labelledBy}
        onMouseDown={(event) => event.stopPropagation()}
      >
        {children}
      </section>
    </div>
  );
}
