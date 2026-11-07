// 同 LazyFallback.test.tsx：web/ 没有测试运行器，顶层执行纯函数断言，失败时 throw，靠 tsc 校验。
// 不渲染 hook：只覆盖栈的纯函数与 sheetSurfaceFor 的四种组合。
import { createBasePage, popMenuPage, pushMenuPage, resetMenuPage } from './useMenuPageStack';
import { sheetSurfaceFor } from './useIsWideSheet';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertTrue(value: boolean, what: string): void {
  assertEqual(value, true, what);
}

// createBasePage：种类透传、id 为 page-N 且每次生成都不同。
const base = createBasePage({ kind: 'main' });
assertEqual(base.kind, 'main', '栈底种类');
assertTrue(/^page-\d+$/.test(base.id), '栈底 id 格式');
const anotherBase = createBasePage({ kind: 'main' });
assertTrue(base.id !== anotherBase.id, '栈底 id 唯一');

// push：追加并保持原栈不变。
const stack1 = pushMenuPage([base], { kind: 'agents' });
const stack2 = pushMenuPage(stack1, { kind: 'review' });
assertEqual(stack1.length, 2, 'push 一次后的深度');
assertEqual(stack2.length, 3, 'push 两次后的深度');
assertEqual(stack2[0], base, 'push 保留栈底');
assertEqual(stack2[2].kind, 'review', 'push 的栈顶种类');
assertTrue(stack2[2].id !== stack2[1].id, 'push 生成新 id');

// pop：连续两次回到 base；栈底 pop 返回原数组。
const back1 = popMenuPage(stack2);
const back2 = popMenuPage(back1);
assertEqual(back1.length, 2, 'pop 一次后的深度');
assertEqual(back2.length, 1, 'pop 两次后的深度');
assertEqual(back2[0], base, 'pop 回到栈底');
const singleStack = [base];
assertTrue(popMenuPage(singleStack) === singleStack, '栈底 pop 返回原数组');
assertEqual(popMenuPage(singleStack).length, 1, '栈底 pop 不减少深度');

// reset：只剩单个栈底页，且 id 重新生成。
const resetStack = resetMenuPage({ kind: 'review' });
assertEqual(resetStack.length, 1, 'reset 后的深度');
assertEqual(resetStack[0].kind, 'review', 'reset 后的栈底种类');
assertTrue(resetStack[0].id !== stack2[2].id, 'reset 后 id 重新生成');

// sheetSurfaceFor：宽屏看栈顶种类，不看栈深度。
assertEqual(sheetSurfaceFor(true, 'main'), 'popover', 'wide + compact');
assertEqual(sheetSurfaceFor(true, 'save-task'), 'popover', 'wide + 存看板任务');
assertEqual(sheetSurfaceFor(true, 'review'), 'side-panel', 'wide + 栈底为 review');
assertEqual(sheetSurfaceFor(true, 'files'), 'side-panel', 'wide + 文件页');
assertEqual(sheetSurfaceFor(false, 'main'), 'drawer', '窄屏 + compact');
assertEqual(sheetSurfaceFor(false, 'review'), 'drawer', '窄屏 + 详情页');
