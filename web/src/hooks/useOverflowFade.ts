// 横向滚动容器的边缘渐隐（W-15）：溢出方向写 --fade-left / --fade-right，
// 具体遮罩交给 styles/overflow-fade.css，只在窄屏生效。
import React from 'react';

/** 渐隐宽度，与 overflow-fade.css 里的遮罩尺寸一致。 */
export const OVERFLOW_FADE_SIZE = 24;
/** 亚像素滚动容差：滚动位置带小数时，贴近边缘仍算「到头」。 */
const EDGE_TOLERANCE = 1;

export interface OverflowFade {
  left: number;
  right: number;
}

/** 纯计算：给出左右两侧应遮住的像素宽度（0 或 OVERFLOW_FADE_SIZE）。 */
export function overflowFade(scrollLeft: number, scrollWidth: number, clientWidth: number): OverflowFade {
  const maxScroll = scrollWidth - clientWidth;
  if (maxScroll <= EDGE_TOLERANCE) {
    return { left: 0, right: 0 };
  }
  return {
    left: scrollLeft > EDGE_TOLERANCE ? OVERFLOW_FADE_SIZE : 0,
    right: scrollLeft < maxScroll - EDGE_TOLERANCE ? OVERFLOW_FADE_SIZE : 0
  };
}

/** 把当前滚动位置换算成 CSS 变量写回元素；滚动与尺寸变化时都重算。 */
export function useOverflowFade(ref: React.RefObject<HTMLElement | null>): void {
  React.useEffect(() => {
    const element = ref.current;
    if (!element) return;

    const update = (): void => {
      const { left, right } = overflowFade(element.scrollLeft, element.scrollWidth, element.clientWidth);
      element.style.setProperty('--fade-left', `${left}px`);
      element.style.setProperty('--fade-right', `${right}px`);
    };

    update();
    element.addEventListener('scroll', update, { passive: true });
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => {
      element.removeEventListener('scroll', update);
      observer.disconnect();
    };
  }, [ref]);
}
