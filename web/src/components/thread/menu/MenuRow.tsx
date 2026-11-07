// 会话更多操作面板的通用行与分组：主页面和各子页面共用同一套外观，
// 保证分组标题、行高、右侧取值/角标/箭头在视觉上完全一致

import React from 'react';
import { ChevronRightIcon } from '../../Icons';

// 分组标题：字号与间距沿用 sheet 里的 eyebrow
export function MenuGroupTitle({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-mc-caption font-semibold uppercase tracking-wide text-[var(--foreground-muted)] px-1">
      {children}
    </p>
  );
}

export interface MenuRowProps {
  /** 行首线框图标，可选；传入后自动套一层固定尺寸的容器 */
  icon?: React.ReactNode;
  label: string;
  /** 行尾的当前取值，例如「跟随全局」「Worktree」 */
  value?: string;
  /** 大于 0 时显示主色圆角角标，例如未结束的子代理数 */
  badge?: number;
  /** 是否显示行尾箭头，表示会进入下一级页面 */
  chevron?: boolean;
  /** 危险操作用红色文字 */
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}

export function MenuRow({ icon, label, value, badge, chevron, danger, disabled, onClick }: MenuRowProps) {
  return (
    <button
      type="button"
      className={`sheet-action${danger ? ' sheet-action--danger' : ''}`}
      disabled={disabled}
      onClick={onClick}
    >
      {icon && (
        <span
          className={`h-4 w-4 shrink-0${danger ? '' : ' text-[var(--foreground-muted)]'}`}
          aria-hidden="true"
        >
          {icon}
        </span>
      )}
      <span className="flex-1 truncate">{label}</span>
      {value && (
        <span className="text-mc-caption text-[var(--foreground-muted)] truncate max-w-[45%]">{value}</span>
      )}
      {badge !== undefined && badge > 0 && (
        <span className="px-1.5 py-0.5 rounded-full bg-[var(--primary)] text-white text-mc-caption font-semibold">
          {badge}
        </span>
      )}
      {chevron && <ChevronRightIcon className="h-3.5 w-3.5 text-[var(--foreground-muted)]" />}
    </button>
  );
}

export interface MenuGroupProps {
  /** 省略时不渲染分组标题 */
  title?: string;
  children: React.ReactNode;
  /** 分组下方的提示或错误，例如导出结果、worktree 提示 */
  footer?: React.ReactNode;
}

export function MenuGroup({ title, children, footer }: MenuGroupProps) {
  return (
    <div className="mt-4 pt-2">
      {title && <MenuGroupTitle>{title}</MenuGroupTitle>}
      <div className="sheet-actions !mt-1">{children}</div>
      {footer}
    </div>
  );
}
