// 宽屏判定：断点与设置页一致（900px）。
// 外壳形态由「是否宽屏 + 栈顶页面种类」共同决定，而不是栈深度：
// 深链接直达审阅时审阅是栈底，宽屏下仍应是侧栏，不能塞进 popover。

import { useEffect, useState } from 'react';
import type { ThreadMenuPageKind } from './menuPages';
import { isCompactPageKind } from './menuPages';

export const SHEET_WIDE_QUERY = '(min-width: 900px)';

export type SheetSurface = 'popover' | 'side-panel' | 'drawer';

export function sheetSurfaceFor(wide: boolean, topKind: ThreadMenuPageKind): SheetSurface {
  if (!wide) return 'drawer';
  return isCompactPageKind(topKind) ? 'popover' : 'side-panel';
}

function readWide(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return window.matchMedia(SHEET_WIDE_QUERY).matches;
}

export function useIsWideSheet(): boolean {
  const [wide, setWide] = useState(readWide);

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia(SHEET_WIDE_QUERY);
    const handleChange = (event: MediaQueryListEvent) => setWide(event.matches);
    setWide(query.matches);
    query.addEventListener('change', handleChange);
    return () => query.removeEventListener('change', handleChange);
  }, []);

  return wide;
}
