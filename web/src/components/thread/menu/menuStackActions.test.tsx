// 同 LazyFallback.test.tsx：web/ 没有测试运行器，纯函数在顶层直接断言，失败时 throw，靠 tsc 校验。
import { routeAfterPop, shouldIgnoreMenuEscape } from './menuStackActions';
import type { ThreadMenuPage, ThreadMenuPageKind } from './menuPages';

function page(id: string, kind: ThreadMenuPageKind, filePath?: string): ThreadMenuPage {
  return filePath === undefined ? { id, kind } : { id, kind, filePath };
}

function assertEqual(actual: string | null, expected: string | null, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertBool(actual: boolean, expected: boolean, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

// 出栈路由：只有新栈顶是可路由详情页才写 hash，回到主页 / 单页栈都清回会话根。
assertEqual(
  routeAfterPop([page('p1', 'main'), page('p2', 'review')]),
  null,
  'main + review 出栈后的路由'
);
assertEqual(
  routeAfterPop([page('p1', 'main'), page('p2', 'agents'), page('p3', 'review')]),
  '/agents',
  'main + agents + review 出栈后的路由'
);
assertEqual(routeAfterPop([page('p1', 'review')]), null, 'review 单页出栈后的路由');
assertEqual(
  routeAfterPop([page('p1', 'main'), page('p2', 'files')]),
  null,
  'main + files 出栈后的路由'
);

// Esc 判定：普通 Esc 才交给栈；已被处理、嵌套弹窗、非 Esc 都忽略。
assertBool(
  shouldIgnoreMenuEscape({ key: 'Escape', defaultPrevented: false }, 1),
  false,
  '普通 Esc 的忽略结果'
);
assertBool(
  shouldIgnoreMenuEscape({ key: 'Escape', defaultPrevented: true }, 1),
  true,
  '已 preventDefault 的 Esc 的忽略结果'
);
assertBool(
  shouldIgnoreMenuEscape({ key: 'Escape', defaultPrevented: false }, 2),
  true,
  '两层 modal 时 Esc 的忽略结果'
);
assertBool(
  shouldIgnoreMenuEscape({ key: 'Enter', defaultPrevented: false }, 1),
  true,
  '非 Esc 按键的忽略结果'
);
