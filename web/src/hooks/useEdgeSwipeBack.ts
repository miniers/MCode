// 边缘右滑返回（W-8）：只在左边缘起手、横向位移占优时接管，跟手位移写在页面容器上。
import { useEffect, useRef } from 'react';

/** 起手点必须落在左边缘 24px 内。 */
export const EDGE_SWIPE_START_PX = 24;
/** 超过宽度的 30% 就提交返回。 */
export const EDGE_SWIPE_COMMIT_RATIO = 0.3;
/** 或者速度超过 0.3px/ms（快速甩出）。 */
export const EDGE_SWIPE_COMMIT_VELOCITY = 0.3;
const EDGE_SWIPE_SLIDE_MS = 200;
const EDGE_SWIPE_SPRING_MS = 250;

export function shouldCommitEdgeBack(dx: number, velocity: number, width: number): boolean {
  if (dx <= 0) return false;
  if (velocity > EDGE_SWIPE_COMMIT_VELOCITY) return true;
  return dx > width * EDGE_SWIPE_COMMIT_RATIO;
}

/** 只在 PWA standalone 或窄屏（<768px）启用，避免桌面宽屏误触。 */
export function isEdgeSwipeAvailable(): boolean {
  if (typeof window === 'undefined') return false;
  const standalone =
    typeof window.matchMedia === 'function' && window.matchMedia('(display-mode: standalone)').matches;
  return standalone || window.innerWidth < 768;
}

export interface EdgeSwipeBackOptions {
  enabled: boolean;
  onCommit: () => void;
}

/**
 * 位移作用在 documentElement 上：返回首页后设置页是从 Header 里渲染的 fixed 全屏层，
 * 挂在某个路由容器上会漏掉它。提交时先滑出再 onCommit，回调里的 goBack 需要跳过 View Transition。
 */
export function useEdgeSwipeBack({ enabled, onCommit }: EdgeSwipeBackOptions): void {
  const commitRef = useRef(onCommit);
  useEffect(() => {
    commitRef.current = onCommit;
  }, [onCommit]);

  useEffect(() => {
    if (!enabled || !isEdgeSwipeAvailable()) return;
    const root = document.documentElement;
    let pointerID: number | null = null;
    let startX = 0;
    let startY = 0;
    let dx = 0;
    let claimed = false;
    let lastX = 0;
    let lastTime = 0;
    let velocity = 0;
    let timer = 0;
    let frame = 0;
    let done = false;
    let committing = false;

    const clearStyles = () => {
      root.style.transition = '';
      root.style.transform = '';
    };

    const finish = (commit: boolean) => {
      claimed = false;
      pointerID = null;
      const width = window.innerWidth;
      window.clearTimeout(timer);
      window.cancelAnimationFrame(frame);
      if (commit) {
        done = true;
        committing = true;
        root.style.transition = `transform ${EDGE_SWIPE_SLIDE_MS}ms cubic-bezier(.22,1,.36,1)`;
        root.style.transform = `translateX(${width}px)`;
        timer = window.setTimeout(() => {
          commitRef.current();
          // 等 React 把新路由画出来再撤位移（否则旧页面会闪回），但不要拖成一段白屏。
          // 记得复位，否则下次进内页就再也滑不动了。
          frame = window.requestAnimationFrame(() => {
            frame = window.requestAnimationFrame(() => {
              committing = false;
              done = false;
              clearStyles();
            });
          });
        }, EDGE_SWIPE_SLIDE_MS);
      } else {
        root.style.transition = `transform ${EDGE_SWIPE_SPRING_MS}ms cubic-bezier(.22,1,.36,1)`;
        root.style.transform = 'translateX(0)';
        timer = window.setTimeout(clearStyles, EDGE_SWIPE_SPRING_MS);
      }
    };

    const onPointerDown = (event: PointerEvent) => {
      if (done || !event.isPrimary) return;
      if (event.pointerType === 'mouse') return;
      if (event.clientX >= EDGE_SWIPE_START_PX) return;
      const target = event.target;
      if (target instanceof Element) {
        // 边缘的输入控件、以及盖在页面上的弹层不接管。
        if (target.closest('input, textarea, select, [contenteditable="true"]')) return;
        if (target.closest('.sheet-layer, [aria-modal="true"]')) return;
      }
      pointerID = event.pointerId;
      startX = event.clientX;
      startY = event.clientY;
      lastX = event.clientX;
      lastTime = performance.now();
      dx = 0;
      velocity = 0;
      claimed = false;
    };

    const onPointerMove = (event: PointerEvent) => {
      if (pointerID === null || event.pointerId !== pointerID) return;
      const mx = event.clientX - startX;
      const my = event.clientY - startY;
      if (!claimed) {
        // 纵向手势交还给页面滚动。
        if (Math.abs(my) > 12 && Math.abs(my) > Math.abs(mx)) {
          pointerID = null;
          return;
        }
        if (Math.abs(mx) < 8) return;
        if (mx <= 0) {
          pointerID = null;
          return;
        }
        if (Math.abs(mx) <= Math.abs(my)) return;
        claimed = true;
        root.style.transition = 'none';
      }
      dx = Math.max(0, mx);
      const now = performance.now();
      const elapsed = now - lastTime;
      if (elapsed > 0) {
        velocity = (event.clientX - lastX) / elapsed;
        lastX = event.clientX;
        lastTime = now;
      }
      root.style.transform = `translateX(${dx}px)`;
    };

    const onPointerUp = (event: PointerEvent) => {
      if (pointerID === null || event.pointerId !== pointerID) return;
      if (!claimed) {
        pointerID = null;
        return;
      }
      const paused = performance.now() - lastTime > 100;
      finish(shouldCommitEdgeBack(dx, paused ? 0 : velocity, window.innerWidth));
    };

    const onPointerCancel = () => {
      if (!claimed) {
        pointerID = null;
        return;
      }
      finish(false);
    };

    // 路由变化（过渡动画或浏览器返回手势）时立刻撤掉位移。
    const onRouteChange = () => {
      if (!committing) clearStyles();
    };

    window.addEventListener('pointerdown', onPointerDown);
    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerCancel);
    window.addEventListener('popstate', onRouteChange);
    window.addEventListener('hashchange', onRouteChange);
    return () => {
      window.clearTimeout(timer);
      window.cancelAnimationFrame(frame);
      window.removeEventListener('pointerdown', onPointerDown);
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerCancel);
      window.removeEventListener('popstate', onRouteChange);
      window.removeEventListener('hashchange', onRouteChange);
      clearStyles();
    };
  }, [enabled]);
}
