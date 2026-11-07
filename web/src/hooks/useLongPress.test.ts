// 长按判定（W-3）的表驱动测试。和 ModelPicker.test.tsx 一样不依赖测试框架：
// 断言失败直接抛错，不引用 node 内置模块，tsc 能单独通过。
import {
  LONG_PRESS_MOVE_CANCEL_PX,
  LONG_PRESS_MS,
  longPressOutcome,
  type LongPressOutcome
} from './useLongPress';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

type LongPressCase = {
  label: string;
  elapsedMs: number;
  movedPx: number;
  pointerType: string;
  expected: LongPressOutcome;
};

const cases: LongPressCase[] = [
  { label: '按住 500ms 不动即成立', elapsedMs: LONG_PRESS_MS, movedPx: 0, pointerType: 'touch', expected: 'fire' },
  { label: '按住 499ms 还不成立', elapsedMs: LONG_PRESS_MS - 1, movedPx: 0, pointerType: 'touch', expected: 'pending' },
  { label: '移动 11px 作废', elapsedMs: 800, movedPx: LONG_PRESS_MOVE_CANCEL_PX + 1, pointerType: 'touch', expected: 'cancel' },
  { label: '移动正好 10px 仍成立', elapsedMs: 600, movedPx: LONG_PRESS_MOVE_CANCEL_PX, pointerType: 'touch', expected: 'fire' },
  { label: '鼠标不算长按', elapsedMs: 900, movedPx: 0, pointerType: 'mouse', expected: 'cancel' },
  { label: '触控笔不算长按', elapsedMs: 900, movedPx: 0, pointerType: 'pen', expected: 'cancel' }
];

for (const item of cases) {
  assertEqual(item.label, longPressOutcome(item.elapsedMs, item.movedPx, item.pointerType), item.expected);
}
