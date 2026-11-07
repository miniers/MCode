// 共享标题栏：左侧按 leading 显示返回/关闭，中间 eyebrow + 标题，右侧放页面自己的操作。
// 结构取自 ThreadMenuSheet 原头部；返回与关闭都由调用方决定语义（栈 pop 或关闭整个菜单）。

import React from 'react';
import { ChevronRightIcon, XMarkIcon } from '../../Icons';
import type { MenuPageLeading } from './menuPageChrome';

export interface MenuSheetHeaderProps {
  eyebrow: string;
  title: string;
  /** 标题 h2 的 id，供外层 SheetFrame 的 aria-labelledby 引用 */
  labelledBy: string;
  leading: MenuPageLeading;
  onLeading: () => void;
  /** leading === 'close' 时优先调用；缺省回退到 onLeading */
  onClose?: () => void;
  trailing?: React.ReactNode;
  /** 宽屏外壳不显示底部拖拽条 */
  showHandle?: boolean;
  /** 关闭按钮的无障碍文案；文件页可用「关闭文件浏览」 */
  closeLabel?: string;
}

export function MenuSheetHeader({
  eyebrow,
  title,
  labelledBy,
  leading,
  onLeading,
  onClose,
  trailing,
  showHandle = true,
  closeLabel = '关闭菜单'
}: MenuSheetHeaderProps) {
  const leadingAction = leading === 'close' ? onClose ?? onLeading : onLeading;

  return (
    <>
      {showHandle && <div className="sheet-handle" aria-hidden="true" />}
      <div className="menu-sheet-header flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-1">
          {leading === 'back' && (
            <button
              type="button"
              className="icon-button"
              onClick={leadingAction}
              aria-label="返回上一页"
              title="返回上一页"
            >
              <ChevronRightIcon className="h-4 w-4 rotate-180" />
            </button>
          )}
          {leading === 'close' && (
            <button
              type="button"
              className="icon-button"
              onClick={leadingAction}
              aria-label={closeLabel}
              title="关闭"
            >
              <XMarkIcon className="h-4 w-4" />
            </button>
          )}
          <div className="min-w-0">
            <p className="eyebrow truncate">{eyebrow}</p>
            <h2 id={labelledBy} className="sheet-title truncate">
              {title}
            </h2>
          </div>
        </div>
        {trailing ? <div className="flex shrink-0 items-center gap-1">{trailing}</div> : null}
      </div>
    </>
  );
}
