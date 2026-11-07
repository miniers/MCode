// 懒加载组件的公共兜底：
// - LazyFallback：加载中的轻量居中提示（沿用现有 Tailwind 变量样式）
// - LazyErrorBoundary：块加载失败时给出提示，避免 Suspense 抛错后整页白屏
// - LazyBoundary：把上面两个和 Suspense 合成一层，调用处只写一个标签

import React from 'react';
import '../styles/skeleton.css';
import { SheetFrame } from './sheet/SheetFrame';

export interface LazyFallbackProps {
  label?: string;
}

export function LazyFallback({ label = '加载中…' }: LazyFallbackProps) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="flex items-center justify-center gap-2 px-4 py-6 text-xs text-[var(--foreground-muted)] select-none"
    >
      <span className="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-[var(--foreground-muted)] border-t-transparent" />
      <span>{label}</span>
    </div>
  );
}

/** 骨架形状：list 按会话行绘制，thread 按消息气泡绘制。 */
export type LazySkeletonVariant = 'list' | 'thread';

export interface LazySheetFallbackProps extends LazyFallbackProps {
  /** 卡片样式：menu 走 thread-action-sheet，panel 走 settings-sheet（默认）。 */
  size?: 'menu' | 'panel';
}

export interface LazyScreenFallbackProps extends LazyFallbackProps {
  /** 不传按 list，调用方不必改。 */
  variant?: LazySkeletonVariant;
}

/** 列表骨架：4 行 76px 会话行，左侧 40px 圆形头像位，右侧标题 60% / 副标题 40%。 */
function ListSkeleton() {
  return (
    <div className="mc-skeleton mc-skeleton--list" aria-hidden="true">
      {[0, 1, 2, 3].map((index) => (
        <div key={index} className="mc-skeleton-row" data-skeleton-row>
          <div className="mc-skeleton-block mc-skeleton-avatar" />
          <div className="mc-skeleton-lines">
            <div className="mc-skeleton-block mc-skeleton-line" style={{ width: '60%' }} />
            <div className="mc-skeleton-block mc-skeleton-line mc-skeleton-line--sub" style={{ width: '40%' }} />
          </div>
        </div>
      ))}
    </div>
  );
}

/** 会话骨架：3 组气泡，用户右对齐 55%、助手左对齐 80% 两行，再一个用户 40%。 */
function ThreadSkeleton() {
  return (
    <div className="mc-skeleton mc-skeleton--thread" aria-hidden="true">
      <div
        className="mc-skeleton-block mc-skeleton-bubble mc-skeleton-bubble--user"
        data-skeleton-bubble="user"
        style={{ width: '55%' }}
      />
      <div
        className="mc-skeleton-bubble mc-skeleton-bubble--assistant"
        data-skeleton-bubble="assistant"
        style={{ width: '80%' }}
      >
        <div className="mc-skeleton-block mc-skeleton-line" style={{ width: '92%' }} />
        <div className="mc-skeleton-block mc-skeleton-line" style={{ width: '64%' }} />
      </div>
      <div
        className="mc-skeleton-block mc-skeleton-bubble mc-skeleton-bubble--user"
        data-skeleton-bubble="user"
        style={{ width: '40%' }}
      />
    </div>
  );
}

/**
 * 抽屉懒加载占位：按浮层出现（sheet-layer + 卡片），不占页面流，消息列表不会因首次打开抽屉被挤动。
 * onRequestClose 传空函数：占位期间点遮罩不做任何事，等真实抽屉挂载后接管。
 */
export function LazySheetFallback({ label = '加载中…', size = 'panel' }: LazySheetFallbackProps) {
  return (
    <SheetFrame
      onRequestClose={() => {}}
      size={size}
      cardClassName={size === 'menu' ? 'min-h-[240px] flex flex-col' : 'min-h-[45dvh]'}
    >
      <div
        role="status"
        aria-live="polite"
        className="flex flex-1 items-center justify-center gap-2 px-4 py-6 text-xs text-[var(--foreground-muted)] select-none"
      >
        <span className="h-3.5 w-3.5 shrink-0 animate-spin rounded-full border-2 border-[var(--foreground-muted)] border-t-transparent" />
        <span>{label}</span>
      </div>
    </SheetFrame>
  );
}

/** 整屏懒加载占位：会话页这类全屏视图用它，避免加载瞬间出现空白或露出别的视图。 */
export function LazyScreenFallback({ label = '加载中…', variant = 'list' }: LazyScreenFallbackProps) {
  return (
    <div className="flex min-h-0 flex-1 flex-col" role="status" aria-live="polite" aria-busy="true">
      <span className="mc-skeleton-label">{label}</span>
      {variant === 'thread' ? <ThreadSkeleton /> : <ListSkeleton />}
    </div>
  );
}

interface LazyErrorBoundaryProps {
  children: React.ReactNode;
}

interface LazyErrorBoundaryState {
  failed: boolean;
}

/** 只兜住懒加载块抛出的错误；重新打开（组件重新挂载）会自动重试。 */
export class LazyErrorBoundary extends React.Component<LazyErrorBoundaryProps, LazyErrorBoundaryState> {
  state: LazyErrorBoundaryState = { failed: false };

  static getDerivedStateFromError(): LazyErrorBoundaryState {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      // 断网或版本更新后旧 chunk 已不存在时，import() 会一直失败，只能整页重新加载。
      return (
        <div
          role="alert"
          className="flex flex-col items-center justify-center gap-2 px-4 py-6 text-xs text-[var(--danger-foreground)]"
        >
          <span>加载失败</span>
          <button type="button" className="secondary-button" onClick={() => window.location.reload()}>
            重新加载
          </button>
        </div>
      );
    }
    return this.props.children;
  }
}

export interface LazyBoundaryProps {
  children: React.ReactNode;
  label?: string;
  /** inline：默认的一行提示；screen：占满父容器的整屏骨架；sheet：按浮层出现的抽屉占位。 */
  variant?: 'inline' | 'screen' | 'sheet';
  /** screen 骨架的形状，默认 list；会话页传 thread。 */
  skeleton?: LazySkeletonVariant;
  /** sheet 占位的卡片样式，默认 panel。 */
  sheetSize?: 'menu' | 'panel';
}

export function LazyBoundary({
  children,
  label,
  variant = 'inline',
  skeleton,
  sheetSize = 'panel',
}: LazyBoundaryProps) {
  const fallback =
    variant === 'screen' ? (
      <LazyScreenFallback label={label} variant={skeleton} />
    ) : variant === 'sheet' ? (
      <LazySheetFallback label={label} size={sheetSize} />
    ) : (
      <LazyFallback label={label} />
    );
  return (
    <LazyErrorBoundary>
      <React.Suspense fallback={fallback}>{children}</React.Suspense>
    </LazyErrorBoundary>
  );
}
