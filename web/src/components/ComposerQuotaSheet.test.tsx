// 同 ComposerQuotaBar.test.tsx：web/ 没有测试运行器，顶层直接断言，失败时 throw。
// 额度配色按用量分档：<50 绿、<80 黄、<95 橙、其余红；百分比文字在低用量时用弱化色。
import { usageColor, usagePercentColor } from './ComposerQuotaSheet';

function assertEqual(actual: string, expected: string, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label} 期望 “${expected}”，实际为 “${actual}”`);
  }
}

assertEqual(usageColor(0), 'var(--quota-low)', 'usageColor(0)');
assertEqual(usageColor(49.9), 'var(--quota-low)', 'usageColor(49.9)');
assertEqual(usageColor(50), 'var(--quota-mid)', 'usageColor(50)');
assertEqual(usageColor(80), 'var(--quota-high)', 'usageColor(80)');
assertEqual(usageColor(95), 'var(--quota-critical)', 'usageColor(95)');
assertEqual(usageColor(100), 'var(--quota-critical)', 'usageColor(100)');

assertEqual(usagePercentColor(30), 'var(--foreground-muted)', 'usagePercentColor(30)');
assertEqual(usagePercentColor(74), 'var(--quota-mid)', 'usagePercentColor(74)');
