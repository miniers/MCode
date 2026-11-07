// 页面切换方向与过场（W-2）：判断是前进、返回还是替换，用 View Transition 包住路由更新。
// 方向判断写成纯函数，便于表驱动测试；运行时只依赖浏览器原生 View Transition API。

export type NavDirection = 'push' | 'pop' | 'replace';

/** 路由只关心“页面层级”：层级变深算前进，变浅算返回，同层（含切 tab）算替换。 */
export interface NavRouteLike {
  kind: string;
  subview?: string;
  /** thread 路由的会话标识；用来判断前后是否仍是同一个会话。 */
  threadID?: string;
}

export function routeDepth(route: NavRouteLike): number {
  switch (route.kind) {
    case 'home':
      return 0;
    case 'board':
    case 'settings':
    case 'draft':
    case 'thread':
      return 1;
    case 'task':
    case 'goal':
      return 2;
    default:
      return 0;
  }
}

/**
 * replace（首页切看板等同层切换）只淡入；设置切 tab 由 shouldSkipNavTransition 直接跳过过场；
 * 其他情况按路由深度：变深 push、变浅 pop，同层视作 replace。
 */
export function resolveNavDirection(
  prevRoute: NavRouteLike,
  nextRoute: NavRouteLike,
  replace?: boolean
): NavDirection {
  if (replace) return 'replace';
  const delta = routeDepth(nextRoute) - routeDepth(prevRoute);
  if (delta > 0) return 'push';
  if (delta < 0) return 'pop';
  return 'replace';
}

/**
 * 以下切换直接提交路由、不走过场：
 * - 设置是盖在会话列表上的底部抽屉/弹窗，开关它只动覆盖层，整页快照的 push/pop 会让底层
 *   会话列表横移一下；设置内部切 tab 也属于原地换内容，宽屏弹窗没有 view-transition-name，
 *   浏览器对整个 root 默认交叉淡入，看起来像闪烁。
 * - 从会话页返回会话列表直接换页，列表不再播放返回过场。
 * - 更多操作菜单打开 agents/review/git/scripts/files 子页时只改同一会话的 subview，
 *   菜单自己已有页面栈滑动动画，再叠一次整页 replace 淡入会闪，也和不改 hash 的子页不一致。
 */
export function shouldSkipNavTransition(
  prevRoute: NavRouteLike,
  nextRoute: NavRouteLike
): boolean {
  if (prevRoute.kind === 'settings' || nextRoute.kind === 'settings') return true;
  if (prevRoute.kind === 'thread' && nextRoute.kind === 'home') return true;
  return (
    prevRoute.kind === 'thread' &&
    nextRoute.kind === 'thread' &&
    prevRoute.threadID === nextRoute.threadID &&
    (prevRoute.subview ?? '') !== (nextRoute.subview ?? '')
  );
}

export function supportsMobilePageTransition(): boolean {
  if (typeof document === 'undefined' || typeof window === 'undefined') return false;
  if (typeof document.startViewTransition !== 'function') return false;
  if (typeof window.matchMedia !== 'function') return true;
  return !window.matchMedia('(prefers-reduced-motion: reduce)').matches;
}

/**
 * 删掉 data-mobile-direction 后，index.css 里被它抑制的 .settings-sheet slide-up 会从头重播，
 * 卡片回到 opacity 0，看起来像设置面板透明闪了一下。清标记前把已挂载的 sheet 入场动画冻成 none。
 */
function freezeSheetEnterAnimation(): void {
  if (typeof document === 'undefined' || typeof document.querySelectorAll !== 'function') return;
  const sheets = document.querySelectorAll('.settings-sheet');
  sheets.forEach((node) => {
    const element = node as Element & { style?: { animation?: string } };
    if (element.style) element.style.animation = 'none';
  });
}

/**
 * 先写 data-mobile-direction（CSS 据此决定入场方向），再在 View Transition 回调里同步提交路由，
 * 过场结束或跳过时清掉标记。不支持或用户要求减少动效时直接执行更新。
 */
export function startMobilePageTransition(
  direction: NavDirection,
  update: () => void
): ViewTransition | null {
  if (!supportsMobilePageTransition()) {
    update();
    return null;
  }
  const root = document.documentElement;
  let applied = false;
  const apply = () => {
    if (applied) return;
    applied = true;
    update();
  };
  root.dataset.mobileDirection = direction;
  try {
    const transition = document.startViewTransition(apply);
    void transition.finished
      .catch(() => undefined)
      .finally(() => {
        freezeSheetEnterAnimation();
        delete root.dataset.mobileDirection;
      });
    return transition;
  } catch {
    // 极端情况下 API 抛错：退化为直接更新，别把页面卡在旧路由上。
    apply();
    freezeSheetEnterAnimation();
    delete root.dataset.mobileDirection;
    return null;
  }
}
