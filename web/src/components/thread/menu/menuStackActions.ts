// 菜单页面栈的纯函数：出栈后应同步的路由，以及 Esc 是否该交给栈处理。

import type { ThreadMenuPage } from './menuPages';
import { routeForPage } from './menuPages';

/** 出栈后新栈顶的路由；栈深 <= 1 时回到会话根路由（null）。 */
export function routeAfterPop(stack: ThreadMenuPage[]): string | null {
  if (stack.length <= 1) return null;
  const nextTop = stack[stack.length - 2];
  return nextTop ? routeForPage(nextTop) : null;
}

/** 嵌套弹窗（>1 层 aria-modal dialog）或已被处理的 Escape 不再触发菜单出栈。 */
export function shouldIgnoreMenuEscape(
  event: { key: string; defaultPrevented: boolean },
  openModalCount: number
): boolean {
  if (event.key !== 'Escape') return true;
  if (event.defaultPrevented) return true;
  return openModalCount > 1;
}
