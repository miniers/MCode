// 路由 → 菜单栈底页：深链接、刷新与时间线等外部入口据此决定菜单打开时停在哪一页。
// subview 为空、或对应能力未声明时返回 null，表示这条路由不该打开菜单。
// agents 不受能力开关限制：子代理状态随会话一起拉取，宿主没有相关 feature 也能查看。

import { pageFromRoute } from './menuPages';
import type { ThreadMenuPageSpec, ThreadSubview } from './menuPages';

export interface MenuRouteCaps {
  reviewSupported: boolean;
  gitSupported: boolean;
  scriptsSupported: boolean;
  filesSupported: boolean;
}

export function menuBaseForRoute(
  subview: ThreadSubview | null | undefined,
  filePath: string | undefined,
  caps: MenuRouteCaps
): ThreadMenuPageSpec | null {
  if (!subview) return null;
  switch (subview) {
    case 'review':
      if (!caps.reviewSupported) return null;
      break;
    case 'git':
      if (!caps.gitSupported) return null;
      break;
    case 'scripts':
      if (!caps.scriptsSupported) return null;
      break;
    case 'files':
      if (!caps.filesSupported) return null;
      break;
    default:
      break;
  }
  return pageFromRoute(subview, filePath);
}
