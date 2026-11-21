import {
  isInsideHorizontalScrollable,
  shouldEnableEdgeSwipeBack,
  type HorizontalScrollNodeLike
} from './useEdgeSwipeBack';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

// 1. 普通移动浏览器（即使窄屏）不接管边缘右滑，交还给系统自带手势。
assertEqual(
  '普通浏览器非 standalone',
  shouldEnableEdgeSwipeBack({
    matchMedia: () => ({ matches: false }),
    navigatorStandalone: false
  }),
  false
);

// 2. standalone PWA（display-mode: standalone 或 iOS navigator.standalone）启用边缘右滑。
assertEqual(
  'PWA display-mode: standalone',
  shouldEnableEdgeSwipeBack({
    matchMedia: (query) => ({ matches: query === '(display-mode: standalone)' }),
    navigatorStandalone: false
  }),
  true
);

assertEqual(
  'iOS navigator.standalone',
  shouldEnableEdgeSwipeBack({
    matchMedia: () => ({ matches: false }),
    navigatorStandalone: true
  }),
  true
);

// 3. 横向滚动元素内部豁免：沿祖先链找到 scrollWidth > clientWidth 且 overflow-x 为 auto/scroll 的容器。
interface FakeNode extends HorizontalScrollNodeLike {
  overflowX?: string;
}

const scrollPre: FakeNode = {
  scrollWidth: 640,
  clientWidth: 320,
  overflowX: 'auto',
  parentElement: null
};
const codeChild: FakeNode = {
  scrollWidth: 640,
  clientWidth: 640,
  overflowX: 'visible',
  parentElement: scrollPre
};
assertEqual(
  '位于 overflow-x: auto 可横向滚动代码块内应豁免',
  isInsideHorizontalScrollable(codeChild, (el) => ({ overflowX: (el as FakeNode).overflowX })),
  true
);

const scrollTableWrap: FakeNode = {
  scrollWidth: 800,
  clientWidth: 360,
  overflowX: 'scroll',
  parentElement: null
};
const tdChild: FakeNode = {
  scrollWidth: 120,
  clientWidth: 120,
  overflowX: 'visible',
  parentElement: scrollTableWrap
};
assertEqual(
  '位于 overflow-x: scroll 可横向滚动表格内应豁免',
  isInsideHorizontalScrollable(tdChild, (el) => ({ overflowX: (el as FakeNode).overflowX })),
  true
);

// 未实际溢出（scrollWidth === clientWidth）或 overflow-x 为 hidden/visible 时不豁免。
const nonOverflowAuto: FakeNode = {
  scrollWidth: 320,
  clientWidth: 320,
  overflowX: 'auto',
  parentElement: null
};
const hiddenOverflow: FakeNode = {
  scrollWidth: 640,
  clientWidth: 320,
  overflowX: 'hidden',
  parentElement: nonOverflowAuto
};
assertEqual(
  '未实际溢出或 overflow-x: hidden 不应误豁免',
  isInsideHorizontalScrollable(hiddenOverflow, (el) => ({ overflowX: (el as FakeNode).overflowX })),
  false
);
assertEqual('null 目标不豁免', isInsideHorizontalScrollable(null), false);
