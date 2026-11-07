// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 覆盖页面层的 data-relation 分布、aria-hidden 互斥与文本共存；不断言 SSR 下不稳定的 inline 高度。
import { renderToStaticMarkup } from 'react-dom/server';
import { MenuStackView, clampMenuHeight, computePanelStackHeight, measureNaturalHeight } from './MenuStackView';

function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

// panel 固定高度：先取「92% 视口」与「视口 - 20」的较小值，再扣掉父卡片 padding/border。
assertEqual(computePanelStackHeight(1000, 40), 880, 'panel 高度（视口 92% 上限）');
assertEqual(computePanelStackHeight(500, 40), 420, 'panel 高度（视口 - 20 上限）');
assertEqual(computePanelStackHeight(300, 400), 0, 'panel 高度（chrome 超过可用高度）');

// menu 翻页的过渡目标高度：内容矮时用内容高度，内容超高时收在 panel 上限。
assertEqual(clampMenuHeight(300, 1000, 40), 300, 'menu 高度（内容矮于上限）');
assertEqual(clampMenuHeight(2000, 1000, 40), 880, 'menu 高度（内容超过 92% 视口上限）');
assertEqual(clampMenuHeight(2000, 500, 40), 420, 'menu 高度（内容超过视口 - 20 上限）');
assertEqual(clampMenuHeight(120, 300, 400), 0, 'menu 高度（chrome 超过可用高度）');

// measureNaturalHeight：临时把 inline 高度交给内容量自然高度，量完必须把原值写回。
const lockedContainer = {
  style: { height: '300px' },
  getBoundingClientRect: () => ({ height: lockedContainer.style.height === 'auto' ? 120 : 300 })
};
assertEqual(measureNaturalHeight(lockedContainer), 120, 'measureNaturalHeight 内容自然高度');
assertEqual(lockedContainer.style.height, '300px', 'measureNaturalHeight 还原 inline 高度');

// 内容高度量不出来（0）时返回 0，调用方据此回退到当前高度。
const emptyContainer = {
  style: { height: '180px' },
  getBoundingClientRect: () => ({ height: emptyContainer.style.height === 'auto' ? 0 : 180 })
};
assertEqual(measureNaturalHeight(emptyContainer), 0, 'measureNaturalHeight 内容为空时的高度');
assertEqual(emptyContainer.style.height, '180px', 'measureNaturalHeight 为空时也还原 inline 高度');

// 两页、栈顶为第二页：第一页 before，第二页 active。
const twoPageHtml = renderToStaticMarkup(
  <MenuStackView
    size="panel"
    activeId="page-b"
    pages={[
      { id: 'page-a', node: <p>第一页内容</p> },
      { id: 'page-b', node: <p>第二页内容</p> }
    ]}
  />
);

assertIncludes(twoPageHtml, 'menu-stack');
assertIncludes(twoPageHtml, 'data-size="panel"');
assertIncludes(twoPageHtml, '第一页内容');
assertIncludes(twoPageHtml, '第二页内容');
assertEqual(countOccurrences(twoPageHtml, 'data-relation="active"'), 1, 'active 页数量');
assertEqual(countOccurrences(twoPageHtml, 'data-relation="before"'), 1, 'before 页数量');
assertEqual(countOccurrences(twoPageHtml, 'data-relation="after"'), 0, 'after 页数量');
assertEqual(countOccurrences(twoPageHtml, 'aria-hidden="true"'), 1, '隐藏页数量');
assertEqual(countOccurrences(twoPageHtml, 'aria-hidden="false"'), 1, '可见页数量');

// 三页、活动页居中：before/active/after 各一，隐藏页为两个。
const threePageHtml = renderToStaticMarkup(
  <MenuStackView
    size="menu"
    activeId="page-b"
    pages={[
      { id: 'page-a', node: <p>主页</p> },
      { id: 'page-b', node: <p>详情页</p> },
      { id: 'page-c', node: <p>确认页</p> }
    ]}
  />
);

assertIncludes(threePageHtml, 'data-size="menu"');
assertEqual(countOccurrences(threePageHtml, 'data-relation="active"'), 1, '三页 active 数量');
assertEqual(countOccurrences(threePageHtml, 'data-relation="before"'), 1, '三页 before 数量');
assertEqual(countOccurrences(threePageHtml, 'data-relation="after"'), 1, '三页 after 数量');
assertEqual(countOccurrences(threePageHtml, 'aria-hidden="true"'), 2, '三页隐藏页数量');
assertEqual(countOccurrences(threePageHtml, 'aria-hidden="false"'), 1, '三页可见页数量');

// 节点顺序与 pages 一致。
const orderHtml = renderToStaticMarkup(
  <MenuStackView
    size="menu"
    activeId="page-a"
    pages={[
      { id: 'page-a', node: <p>最先渲染</p> },
      { id: 'page-b', node: <p>随后渲染</p> }
    ]}
  />
);
assertEqual(orderHtml.indexOf('最先渲染') < orderHtml.indexOf('随后渲染'), true, '页面节点顺序');
