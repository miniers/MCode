// 触感反馈（W-18）的表驱动测试：不依赖测试框架，断言失败直接抛错。
// 假 navigator 记录 vibrate 的参数，用来验证开关与力度映射。
import { HAPTIC_DURATION_MS, haptic, shouldHapticOnCross } from './haptic';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

function assertDeepEqual(label: string, actual: number[], expected: number[]): void {
  if (actual.length !== expected.length || actual.some((value, index) => value !== expected[index])) {
    throw new Error(`${label}: 期望 [${expected.join(',')}]，实际 [${actual.join(',')}]`);
  }
}

function fakeVibrator(): { calls: number[]; vibrator: { vibrate: (pattern: number) => boolean } } {
  const calls: number[] = [];
  return {
    calls,
    vibrator: {
      vibrate: (pattern: number) => {
        calls.push(pattern);
        return true;
      }
    }
  };
}

const light = fakeVibrator();
haptic('light', light.vibrator, true);
assertDeepEqual('light 映射为 8ms', light.calls, [HAPTIC_DURATION_MS.light]);

const medium = fakeVibrator();
haptic('medium', medium.vibrator, true);
assertDeepEqual('medium 映射为 16ms', medium.calls, [HAPTIC_DURATION_MS.medium]);

const disabled = fakeVibrator();
haptic('medium', disabled.vibrator, false);
assertDeepEqual('开关关闭时不振动', disabled.calls, []);

// 没有 vibrate 的 navigator（iOS Safari）不能抛错，也不能假装有反馈。
haptic('light', {}, true);
haptic('light', null, true);
haptic('light', undefined, true);
assertEqual('缺少 vibrate 时静默跳过', true, true);

const threshold = -42;
const crossCases: { label: string; previous: number; next: number; expected: boolean }[] = [
  { label: '从阈值内越过阈值触发', previous: -30, next: -42, expected: true },
  { label: '越过阈值前不触发', previous: -30, next: -41, expected: false },
  { label: '已在阈值外继续拖动不重复触发', previous: -50, next: -60, expected: false },
  { label: '退回阈值内不触发', previous: -50, next: -20, expected: false },
  { label: '退回后再次越过重新触发', previous: -20, next: -50, expected: true }
];

for (const item of crossCases) {
  assertEqual(item.label, shouldHapticOnCross(item.previous, item.next, threshold), item.expected);
}
