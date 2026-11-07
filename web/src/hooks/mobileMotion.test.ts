// 移动端手感相关纯函数的表驱动测试（W-2/W-4/W-8/W-10）。
// 和 ModelPicker.test.tsx 一样不依赖测试框架：接入运行器后本文件即可直接执行，
// 断言失败直接抛错；也不引用 node 内置类型，tsc 能单独通过。
import { resolveNavDirection, shouldSkipNavTransition } from './mobileNavTransition';
import { shouldDismissSheet } from './useSheetDrag';
import { shouldCommitEdgeBack } from './useEdgeSwipeBack';
import { dampOffset } from './threadSwipe';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

function assertClose(label: string, actual: number, expected: number): void {
  if (Math.abs(actual - expected) > 1e-6) {
    throw new Error(`${label}: 期望 ${expected}，实际 ${actual}`);
  }
}

type DirectionCase = {
  label: string;
  prev: { kind: string };
  next: { kind: string };
  replace?: boolean;
  expected: 'push' | 'pop' | 'replace';
};

const directionCases: DirectionCase[] = [
  { label: '列表 → 会话', prev: { kind: 'home' }, next: { kind: 'thread' }, expected: 'push' },
  { label: '列表 → 看板', prev: { kind: 'home' }, next: { kind: 'board' }, expected: 'push' },
  { label: '看板 → 任务', prev: { kind: 'board' }, next: { kind: 'task' }, expected: 'push' },
  { label: '列表 → 设置', prev: { kind: 'home' }, next: { kind: 'settings' }, expected: 'push' },
  { label: '任务 → 看板', prev: { kind: 'task' }, next: { kind: 'board' }, expected: 'pop' },
  { label: '目标 → 会话', prev: { kind: 'goal' }, next: { kind: 'thread' }, expected: 'pop' },
  { label: '会话 → 列表', prev: { kind: 'thread' }, next: { kind: 'home' }, expected: 'pop' },
  { label: '会话 → 设置（同层）', prev: { kind: 'thread' }, next: { kind: 'settings' }, expected: 'replace' },
  { label: '设置切 tab（同层）', prev: { kind: 'settings' }, next: { kind: 'settings' }, expected: 'replace' },
  { label: '首页切视图（replace）', prev: { kind: 'home' }, next: { kind: 'board' }, replace: true, expected: 'replace' },
  { label: '返回首页（replace）', prev: { kind: 'board' }, next: { kind: 'home' }, replace: true, expected: 'replace' }
];

for (const testCase of directionCases) {
  assertEqual(
    `resolveNavDirection ${testCase.label}`,
    resolveNavDirection(testCase.prev, testCase.next, testCase.replace),
    testCase.expected
  );
}

type SkipTransitionCase = {
  label: string;
  prev: { kind: string };
  next: { kind: string };
  expected: boolean;
};

// 设置是盖在会话列表上的覆盖层（抽屉/弹窗），开关它以及内部切 tab 都跳过过场：
// 宽屏设置弹窗没有 view-transition-name，整页 root 交叉淡入会闪一下，底层会话列表也会横移。
const skipTransitionCases: SkipTransitionCase[] = [
  { label: '设置切 tab', prev: { kind: 'settings' }, next: { kind: 'settings' }, expected: true },
  { label: '列表 → 设置', prev: { kind: 'home' }, next: { kind: 'settings' }, expected: true },
  { label: '设置 → 列表', prev: { kind: 'settings' }, next: { kind: 'home' }, expected: true },
  { label: '会话 → 设置', prev: { kind: 'thread' }, next: { kind: 'settings' }, expected: true },
  { label: '列表 → 看板', prev: { kind: 'home' }, next: { kind: 'board' }, expected: false }
];

for (const testCase of skipTransitionCases) {
  assertEqual(
    `shouldSkipNavTransition ${testCase.label}`,
    shouldSkipNavTransition(testCase.prev, testCase.next),
    testCase.expected
  );
}

type SheetCase = {
  label: string;
  dy: number;
  velocity: number;
  height: number;
  expected: boolean;
};

const sheetCases: SheetCase[] = [
  // 阈值 = max(64, min(120, height / 4))
  { label: '高度 400 恰好在阈值上', dy: 100, velocity: 0, height: 400, expected: false },
  { label: '高度 400 超过阈值', dy: 101, velocity: 0, height: 400, expected: true },
  { label: '高度 2000 阈值封顶 120', dy: 120, velocity: 0, height: 2000, expected: false },
  { label: '高度 2000 超过 120', dy: 121, velocity: 0, height: 2000, expected: true },
  { label: '高度 100 阈值保底 64', dy: 64, velocity: 0, height: 100, expected: false },
  { label: '高度 100 超过 64', dy: 65, velocity: 0, height: 100, expected: true },
  { label: '快速下滑但位移很小', dy: 8, velocity: 0.51, height: 400, expected: true },
  { label: '速度恰好 0.5 不算甩', dy: 8, velocity: 0.5, height: 400, expected: false },
  { label: '向上拖不关闭', dy: -40, velocity: 2, height: 400, expected: false },
  { label: '没动的点击不关闭', dy: 0, velocity: 0, height: 400, expected: false }
];

for (const testCase of sheetCases) {
  assertEqual(
    `shouldDismissSheet ${testCase.label}`,
    shouldDismissSheet(testCase.dy, testCase.velocity, testCase.height),
    testCase.expected
  );
}

type EdgeCase = {
  label: string;
  dx: number;
  velocity: number;
  width: number;
  expected: boolean;
};

const edgeCases: EdgeCase[] = [
  { label: '宽度 360 恰好 30%', dx: 108, velocity: 0, width: 360, expected: false },
  { label: '宽度 360 超过 30%', dx: 109, velocity: 0, width: 360, expected: true },
  { label: '宽度 768 不到 30%', dx: 230, velocity: 0, width: 768, expected: false },
  { label: '宽度 768 超过 30%', dx: 231, velocity: 0, width: 768, expected: true },
  { label: '快速轻扫', dx: 20, velocity: 0.31, width: 360, expected: true },
  { label: '速度恰好 0.3 不算甩', dx: 20, velocity: 0.3, width: 360, expected: false },
  { label: '原地不动', dx: 0, velocity: 1, width: 360, expected: false },
  { label: '左滑不触发返回', dx: -60, velocity: 1, width: 360, expected: false }
];

for (const testCase of edgeCases) {
  assertEqual(
    `shouldCommitEdgeBack ${testCase.label}`,
    shouldCommitEdgeBack(testCase.dx, testCase.velocity, testCase.width),
    testCase.expected
  );
}

type DampCase = { label: string; raw: number; expected: number };

const dampCases: DampCase[] = [
  { label: '右滑回 0', raw: 24, expected: 0 },
  { label: '起点', raw: 0, expected: 0 },
  { label: '线性区中段', raw: -50, expected: -50 },
  { label: '线性区末端', raw: -96, expected: -96 },
  { label: '超出 4px 乘 0.3', raw: -100, expected: -97.2 },
  { label: '超出 24px 乘 0.3', raw: -120, expected: -103.2 },
  { label: '大幅超拖也有阻尼', raw: -300, expected: -157.2 }
];

for (const testCase of dampCases) {
  assertClose(`dampOffset ${testCase.label}`, dampOffset(testCase.raw), testCase.expected);
}
