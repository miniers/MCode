import { shouldUseMath } from './lazyMath';

function assertEqual(label: string, actual: boolean, expected: boolean): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

assertEqual('禁用公式时保留 shell 变量', shouldUseMath('$SANDBOX/home $SANDBOX/work', false), false);
assertEqual('启用公式时识别行内公式', shouldUseMath('$x^2$', true), true);
assertEqual('空文本不启用公式', shouldUseMath('', true), false);
