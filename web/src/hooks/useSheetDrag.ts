// 底部 sheet 拖把手关闭（W-4）：只在把手上起手，内容区滚动永远不触发关闭。
import { useEffect, useRef, type RefObject } from 'react';

/** 松手时向下的速度超过它就算“甩”，直接关闭（px/ms）。 */
export const SHEET_DISMISS_VELOCITY = 0.5;

/** 关闭阈值：位移超过 max(64, min(120, 高度/4)) 就关闭。 */
export function sheetDismissThreshold(height: number): number {
  return Math.max(64, Math.min(120, height / 4));
}

export function shouldDismissSheet(dy: number, velocity: number, height: number): boolean {
  if (dy <= 0) return false;
  if (velocity > SHEET_DISMISS_VELOCITY) return true;
  return dy > sheetDismissThreshold(height);
}

/**
 * 把手的 pointerdown 才接管；拖动期间去掉 transition 让 sheet 跟手，松手后 200ms 滑出再 onClose，
 * 否则 200ms 回弹。enabled=false 时不挂监听（如宽屏形态、sheet 未渲染）。
 */
export function useSheetDrag(
  sheetRef: RefObject<HTMLElement | null>,
  handleRef: RefObject<HTMLElement | null>,
  onClose: () => void,
  enabled = true
): void {
  const onCloseRef = useRef(onClose);
  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (!enabled) return;
    const handle = handleRef.current;
    const sheet = sheetRef.current;
    if (!handle || !sheet) return;

    let pointerID: number | null = null;
    let startY = 0;
    let dy = 0;
    let lastY = 0;
    let lastTime = 0;
    let velocity = 0;
    let settleTimer = 0;
    let closing = false;

    const clearStyles = () => {
      sheet.style.transition = '';
      sheet.style.transform = '';
      sheet.style.willChange = '';
    };

    const releaseCapture = () => {
      if (pointerID === null) return;
      const id = pointerID;
      pointerID = null;
      try {
        handle.releasePointerCapture(id);
      } catch {
        // 指针已经被浏览器释放，忽略。
      }
    };

    const settle = (dismiss: boolean) => {
      closing = dismiss;
      window.clearTimeout(settleTimer);
      sheet.style.transition = 'transform 200ms cubic-bezier(.2,.8,.2,1)';
      if (dismiss) {
        sheet.style.transform = `translateY(${Math.max(sheet.offsetHeight, dy) + 80}px)`;
        settleTimer = window.setTimeout(() => {
          clearStyles();
          onCloseRef.current();
        }, 200);
      } else {
        sheet.style.transform = 'translateY(0)';
        settleTimer = window.setTimeout(clearStyles, 200);
      }
    };

    const onPointerDown = (event: PointerEvent) => {
      if (closing || !event.isPrimary) return;
      if (event.pointerType === 'mouse' && event.button !== 0) return;
      // 把手区域里的按钮/输入框不算拖动（设置页的顶部栏本身当把手用）。
      const target = event.target;
      if (target instanceof Element && target.closest('button, a, input, textarea, select, [contenteditable="true"], .settings-tabs, [role="tablist"]')) {
        return;
      }
      pointerID = event.pointerId;
      startY = event.clientY;
      lastY = event.clientY;
      dy = 0;
      velocity = 0;
      lastTime = performance.now();
      try {
        handle.setPointerCapture(event.pointerId);
      } catch {
        // 不支持指针捕获时退回 window 监听，行为一致。
      }
      sheet.style.transition = 'none';
      sheet.style.willChange = 'transform';
    };

    const onPointerMove = (event: PointerEvent) => {
      if (pointerID === null || event.pointerId !== pointerID) return;
      // 只能往下拖：向上的位移夹到 0。
      dy = Math.max(0, event.clientY - startY);
      const now = performance.now();
      const elapsed = now - lastTime;
      if (elapsed > 0) {
        velocity = (event.clientY - lastY) / elapsed;
        lastY = event.clientY;
        lastTime = now;
      }
      sheet.style.transform = `translateY(${dy}px)`;
    };

    const onPointerUp = (event: PointerEvent) => {
      if (pointerID === null || event.pointerId !== pointerID) return;
      releaseCapture();
      // 手指停住再松开不算快速滑动。
      const paused = performance.now() - lastTime > 100;
      settle(shouldDismissSheet(dy, paused ? 0 : velocity, sheet.offsetHeight));
    };

    const onPointerCancel = () => {
      if (pointerID === null) return;
      releaseCapture();
      settle(false);
    };

    handle.addEventListener('pointerdown', onPointerDown);
    window.addEventListener('pointermove', onPointerMove);
    window.addEventListener('pointerup', onPointerUp);
    window.addEventListener('pointercancel', onPointerCancel);
    return () => {
      window.clearTimeout(settleTimer);
      handle.removeEventListener('pointerdown', onPointerDown);
      window.removeEventListener('pointermove', onPointerMove);
      window.removeEventListener('pointerup', onPointerUp);
      window.removeEventListener('pointercancel', onPointerCancel);
      releaseCapture();
      clearStyles();
    };
  }, [enabled, handleRef, sheetRef]);
}
