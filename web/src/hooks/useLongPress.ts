// 长按手势（W-3）：只在触摸指针上生效，按住 500ms 成立，期间移动超过 10px 作废。
// 成立后会抑制紧接着的那次 click，避免长按同时触发气泡里的按钮或链接。
import React from 'react';

export const LONG_PRESS_MS = 500;
export const LONG_PRESS_MOVE_CANCEL_PX = 10;

export type LongPressOutcome = 'pending' | 'fire' | 'cancel';

// 纯判定：elapsedMs 是按住时长，movedPx 是相对按下点的最大位移。
export function longPressOutcome(
  elapsedMs: number,
  movedPx: number,
  pointerType: string
): LongPressOutcome {
  if (pointerType !== 'touch') return 'cancel';
  if (movedPx > LONG_PRESS_MOVE_CANCEL_PX) return 'cancel';
  if (elapsedMs < LONG_PRESS_MS) return 'pending';
  return 'fire';
}

// 这些目标自带交互或属于正文，长按不接管（正文用 data-long-press-ignore 标注）。
const LONG_PRESS_IGNORE_SELECTOR =
  'button, a, input, textarea, select, [role="menu"], [data-long-press-ignore]';

function hasTextSelection(): boolean {
  if (typeof window === 'undefined') return false;
  const selection = window.getSelection();
  return Boolean(selection && selection.toString().length > 0);
}

export interface LongPressHandlers {
  onPointerDown: (event: React.PointerEvent) => void;
  onPointerMove: (event: React.PointerEvent) => void;
  onPointerUp: (event: React.PointerEvent) => void;
  onPointerCancel: (event: React.PointerEvent) => void;
  onPointerLeave: (event: React.PointerEvent) => void;
  onClickCapture: (event: React.MouseEvent) => void;
}

export interface LongPressBinding {
  pressed: boolean;
  handlers: LongPressHandlers;
}

export function useLongPress(onLongPress: () => void, enabled = true): LongPressBinding {
  const [pressed, setPressed] = React.useState(false);
  const timerRef = React.useRef<number | null>(null);
  const startRef = React.useRef<{ x: number; y: number; pointerType: string; at: number } | null>(null);
  const positionRef = React.useRef<{ x: number; y: number } | null>(null);
  const suppressClickRef = React.useRef(false);
  const callbackRef = React.useRef(onLongPress);
  callbackRef.current = onLongPress;

  const clearTimer = React.useCallback(() => {
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  React.useEffect(() => clearTimer, [clearTimer]);

  const reset = React.useCallback(() => {
    clearTimer();
    startRef.current = null;
    positionRef.current = null;
    setPressed(false);
  }, [clearTimer]);

  const onPointerDown = React.useCallback(
    (event: React.PointerEvent) => {
      if (!enabled || event.pointerType !== 'touch') return;
      const target = event.target as Element | null;
      if (target && typeof target.closest === 'function' && target.closest(LONG_PRESS_IGNORE_SELECTOR)) {
        return;
      }
      // 已经有文字选区（例如在拖选区手柄）时不启动，避免抢走原生选择。
      if (hasTextSelection()) return;

      clearTimer();
      suppressClickRef.current = false;
      startRef.current = {
        x: event.clientX,
        y: event.clientY,
        pointerType: event.pointerType,
        at: Date.now()
      };
      positionRef.current = { x: event.clientX, y: event.clientY };
      setPressed(true);
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null;
        const start = startRef.current;
        if (!start) return;
        if (hasTextSelection()) {
          reset();
          return;
        }
        const position = positionRef.current ?? start;
        const movedPx = Math.hypot(position.x - start.x, position.y - start.y);
        if (longPressOutcome(Date.now() - start.at, movedPx, start.pointerType) !== 'fire') return;
        startRef.current = null;
        positionRef.current = null;
        suppressClickRef.current = true;
        setPressed(false);
        callbackRef.current();
      }, LONG_PRESS_MS);
    },
    [clearTimer, enabled, reset]
  );

  const onPointerMove = React.useCallback(
    (event: React.PointerEvent) => {
      const start = startRef.current;
      if (!start) return;
      positionRef.current = { x: event.clientX, y: event.clientY };
      const movedPx = Math.hypot(event.clientX - start.x, event.clientY - start.y);
      if (longPressOutcome(0, movedPx, start.pointerType) === 'cancel') reset();
    },
    [reset]
  );

  const onClickCapture = React.useCallback((event: React.MouseEvent) => {
    if (!suppressClickRef.current) return;
    suppressClickRef.current = false;
    event.preventDefault();
    event.stopPropagation();
  }, []);

  return {
    pressed,
    handlers: {
      onPointerDown,
      onPointerMove,
      onPointerUp: reset,
      onPointerCancel: reset,
      onPointerLeave: reset,
      onClickCapture
    }
  };
}
