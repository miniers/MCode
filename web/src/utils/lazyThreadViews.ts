// 会话页按需加载：ThreadView/ThreadDraftView 会带进时间线、输入框、markdown 与 highlight.js，
// 首屏只加载会话列表，所以这里集中提供动态 import，React.lazy 与空闲预取共用同一份入口。
// 只有一个 prefetch 入口（prefetchStarted 去重），保证模块最多请求一次。

/** 会话详情模块。 */
export const loadThreadView = () => import('../components/ThreadView');

/** 新建会话草稿模块。 */
export const loadThreadDraftView = () => import('../components/ThreadDraftView');

let prefetchStarted = false;

/** 空闲时预取会话页：优先 requestIdleCallback，不支持时退化成 1.5s 定时器。 */
export function prefetchThreadViews(): void {
  if (prefetchStarted) return;
  if (typeof window === 'undefined') return;
  prefetchStarted = true;
  const run = () => {
    // 预取失败无需上报：真正打开会话时 React.lazy 会再试一次，由 LazyBoundary 兜底提示。
    void loadThreadView().catch(() => undefined);
    void loadThreadDraftView().catch(() => undefined);
  };
  if (typeof window.requestIdleCallback === 'function') {
    window.requestIdleCallback(run, { timeout: 3000 });
    return;
  }
  window.setTimeout(run, 1500);
}
