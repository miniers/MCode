// 同 LazyFallback.test.tsx：web/ 没有测试运行器，顶层执行纯函数断言，失败时 throw，靠 tsc 校验。
// 覆盖五条可路由页的往返、不可路由页、files 路径编码与 compact/panel 分类。
import {
  MENU_PAGE_TITLES,
  isCompactPageKind,
  isRoutablePage,
  pageFromRoute,
  pageSizeFor,
  routeForPage
} from './menuPages';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertTrue(value: boolean, what: string): void {
  assertEqual(value, true, what);
}

function assertFalse(value: boolean, what: string): void {
  assertEqual(value, false, what);
}

const ROUTABLE_ROUTES = [
  ['agents', '/agents'],
  ['review', '/review'],
  ['git', '/git'],
  ['scripts', '/scripts'],
  ['files', '/files']
] as const;

for (const [subview, route] of ROUTABLE_ROUTES) {
  const page = pageFromRoute(subview);
  assertEqual(page.kind, subview, `${subview} 页面种类`);
  assertEqual(routeForPage(page), route, `${subview} 路由往返`);
  assertTrue(isRoutablePage(subview), `${subview} 可路由`);
}

// 无 subview 时栈底是菜单主页，不写 hash。
const basePage = pageFromRoute(undefined);
assertEqual(basePage.kind, 'main', '无 subview 时的栈底');
assertEqual(routeForPage(basePage), null, 'main 的路由');
assertFalse(isRoutablePage('main'), 'main 可路由');

// files：空路径回到 /files，带路径时整体 URL 编码。
assertEqual(routeForPage(pageFromRoute('files')), '/files', 'files 空路径路由');
const filePath = 'src/菜单 a.tsx';
assertEqual(
  routeForPage({ kind: 'files', filePath }),
  `/files/${encodeURIComponent(filePath)}`,
  'files 路径编码'
);
assertEqual(
  routeForPage(pageFromRoute('files', filePath)),
  `/files/${encodeURIComponent(filePath)}`,
  'files 路由往返'
);

// 不可路由页：routeForPage 必须为 null，isRoutablePage 必须为 false。
const NON_ROUTABLE_KINDS = ['main', 'swarm', 'workspace', 'export', 'mcp', 'worktree', 'save-task', 'forward'] as const;
for (const kind of NON_ROUTABLE_KINDS) {
  assertEqual(routeForPage({ kind }), null, `${kind} 的路由`);
  assertFalse(isRoutablePage(kind), `${kind} 可路由`);
}

// compact 页用 menu（内容高度），详情页用 panel（固定高度）。
const COMPACT_KINDS = ['main', 'swarm', 'workspace', 'export', 'save-task', 'forward'] as const;
const DETAIL_KINDS = ['agents', 'review', 'git', 'scripts', 'files', 'mcp', 'worktree'] as const;
for (const kind of COMPACT_KINDS) {
  assertTrue(isCompactPageKind(kind), `${kind} 是 compact 页`);
  assertEqual(pageSizeFor(kind), 'menu', `${kind} 的尺寸`);
}
for (const kind of DETAIL_KINDS) {
  assertFalse(isCompactPageKind(kind), `${kind} 是 compact 页`);
  assertEqual(pageSizeFor(kind), 'panel', `${kind} 的尺寸`);
}

// 每个种类都有标题，且标题非空。
for (const kind of [...COMPACT_KINDS, ...DETAIL_KINDS]) {
  assertTrue(MENU_PAGE_TITLES[kind].length > 0, `${kind} 的标题`);
}
assertEqual(MENU_PAGE_TITLES.agents, '子代理进度与汇报', '子代理页标题');
assertEqual(MENU_PAGE_TITLES.review, '代码变更与审阅', '审阅页标题');
