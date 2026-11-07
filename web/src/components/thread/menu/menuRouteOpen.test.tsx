// 同 LazyFallback.test.tsx：web/ 没有测试运行器，顶层执行纯函数断言，失败时 throw，靠 tsc 校验。
// 覆盖：无 subview 不打开菜单、agents 不受能力限制、能力关闭时对应页不打开、files 路径透传。
import { menuBaseForRoute } from './menuRouteOpen';
import type { MenuRouteCaps } from './menuRouteOpen';

const NO_CAPS: MenuRouteCaps = {
  reviewSupported: false,
  gitSupported: false,
  scriptsSupported: false,
  filesSupported: false
};

const ALL_CAPS: MenuRouteCaps = {
  reviewSupported: true,
  gitSupported: true,
  scriptsSupported: true,
  filesSupported: true
};

function assertKind(actual: { kind: string } | null, expected: string | null, what: string): void {
  if (expected === null) {
    if (actual !== null) throw new Error(`期望${what}为 null，实际为 ${actual.kind}`);
    return;
  }
  if (actual === null) throw new Error(`期望${what}为 ${expected}，实际为 null`);
  if (actual.kind !== expected) throw new Error(`期望${what}为 ${expected}，实际为 ${actual.kind}`);
}

// 无 subview：不打开菜单。
assertKind(menuBaseForRoute(undefined, undefined, ALL_CAPS), null, '无 subview 的栈底');
assertKind(menuBaseForRoute(null, '/src/a.ts', ALL_CAPS), null, 'subview 为 null 的栈底');

// agents：四个能力全关也仍以子代理页为栈底。
const agentsPage = menuBaseForRoute('agents', undefined, NO_CAPS);
assertKind(agentsPage, 'agents', '能力全关时的子代理栈底');

// 能力开关：关闭时不打开，开启时打开对应页。
assertKind(menuBaseForRoute('review', undefined, NO_CAPS), null, '审阅能力关闭时的栈底');
assertKind(menuBaseForRoute('review', undefined, ALL_CAPS), 'review', '审阅能力开启时的栈底');
assertKind(menuBaseForRoute('git', undefined, { ...ALL_CAPS, gitSupported: false }), null, 'Git 能力关闭时的栈底');
assertKind(menuBaseForRoute('git', undefined, ALL_CAPS), 'git', 'Git 能力开启时的栈底');
assertKind(menuBaseForRoute('scripts', undefined, { ...ALL_CAPS, scriptsSupported: false }), null, '脚本能力关闭时的栈底');
assertKind(menuBaseForRoute('scripts', undefined, ALL_CAPS), 'scripts', '脚本能力开启时的栈底');
assertKind(menuBaseForRoute('files', undefined, { ...ALL_CAPS, filesSupported: false }), null, '文件能力关闭时的栈底');

// files：带路径时原样透传给页面（路由编码由 routeForPage 负责）。
const filesPage = menuBaseForRoute('files', 'src/菜单 a.tsx', ALL_CAPS);
assertKind(filesPage, 'files', '文件页栈底');
if (!filesPage || filesPage.filePath !== 'src/菜单 a.tsx') {
  throw new Error(`期望文件页 path 透传，实际为 ${filesPage?.filePath ?? 'undefined'}`);
}
