// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 集成校验：外层 SheetFrame 只产生一层 sheet-layer，栈内两页共用这一层遮罩；
// 同时回归 MenuSheetHeader 的返回/关闭互斥与五条详情路由的往返。
import { renderToStaticMarkup } from 'react-dom/server';
import { SheetFrame } from '../../sheet/SheetFrame';
import { MenuStackView } from './MenuStackView';
import { MenuSheetHeader } from './MenuSheetHeader';
import {
  pageFromRoute,
  routeForPage,
  type ThreadMenuPageSpec,
  type ThreadSubview
} from './menuPages';

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

// 最小组合：SheetFrame 提供唯一遮罩，MenuStackView 两页（main → agents），栈顶为 agents。
const stackedHtml = renderToStaticMarkup(
  <SheetFrame size="menu" labelledBy="menu-agents-title" onRequestClose={() => {}}>
    <MenuStackView
      size="menu"
      activeId="agents"
      pages={[
        {
          id: 'main',
          node: (
            <MenuSheetHeader
              eyebrow="当前工作区"
              title="会话菜单"
              labelledBy="menu-main-title"
              leading="back"
              onLeading={() => {}}
            />
          )
        },
        {
          id: 'agents',
          node: (
            <>
              <MenuSheetHeader
                eyebrow="子代理"
                title="子代理进度与汇报"
                labelledBy="menu-agents-title"
                leading="close"
                onLeading={() => {}}
              />
              <p>运行中的子代理会在这里汇报进度。</p>
            </>
          )
        }
      ]}
    />
  </SheetFrame>
);

assertEqual(countOccurrences(stackedHtml, 'sheet-layer'), 1, '栈内遮罩层数');
assertIncludes(stackedHtml, '子代理进度与汇报');
assertIncludes(stackedHtml, '运行中的子代理会在这里汇报进度。');
assertIncludes(stackedHtml, 'aria-hidden="true"');

// 返回/关闭互斥：leading 决定只渲染其中一个按钮。
const backHeaderHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="子代理"
    title="子代理进度与汇报"
    labelledBy="back-menu-title"
    leading="back"
    onLeading={() => {}}
  />
);
// 返回/关闭按钮同时带 aria-label 与 title，纯文本会重复计数，故按 aria-label 计数。
assertEqual(countOccurrences(backHeaderHtml, 'aria-label="返回上一页"'), 1, '返回按钮数量');
assertEqual(countOccurrences(backHeaderHtml, 'aria-label="关闭菜单"'), 0, '返回页的关闭按钮数量');

const closeHeaderHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="当前工作区"
    title="会话菜单"
    labelledBy="close-menu-title"
    leading="close"
    onLeading={() => {}}
  />
);
assertEqual(countOccurrences(closeHeaderHtml, 'aria-label="关闭菜单"'), 1, '关闭按钮数量');
assertEqual(countOccurrences(closeHeaderHtml, 'aria-label="返回上一页"'), 0, '关闭页的返回按钮数量');

// 路由往返：page → route → page → route，五条详情路由都不能丢信息。
function parseRoute(route: string): { subview: ThreadSubview; filePath?: string } {
  const segments = route.split('/').filter(Boolean);
  return {
    subview: segments[0] as ThreadSubview,
    filePath: segments[1] ? decodeURIComponent(segments[1]) : undefined
  };
}

const routedPages: ThreadMenuPageSpec[] = [
  { kind: 'agents' },
  { kind: 'review' },
  { kind: 'git' },
  { kind: 'scripts' },
  { kind: 'files', filePath: 'web/src/App.tsx' }
];

for (const page of routedPages) {
  const route = routeForPage(page);
  if (!route) throw new Error(`期望 ${page.kind} 有路由，实际为 null`);
  const parsed = parseRoute(route);
  const roundTrip = pageFromRoute(parsed.subview, parsed.filePath);
  assertEqual(roundTrip.kind, page.kind, `${page.kind} 路由往返后的 kind`);
  assertEqual(roundTrip.filePath ?? null, page.filePath ?? null, `${page.kind} 路由往返后的路径`);
  assertEqual(routeForPage(roundTrip), route, `${page.kind} 二次路由`);
}
