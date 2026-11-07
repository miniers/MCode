// 同 ComposerQuotaBar.test.tsx：web/ 没有测试运行器，顶层直接断言，失败时 throw。
// 这里只校验纯计算的节奏换算：时间已过比例、门槛与进度条分段布局。
import { periodMinutes, quotaBarLayout, quotaElapsedRatio, quotaPaceKnown } from './quotaPace';

const HOUR_MS = 60 * 60 * 1000;
const NOW = Date.parse('2026-01-01T00:00:00Z');

function assertClose(actual: number, expected: number, label: string): void {
  if (Math.abs(actual - expected) > 1e-9) {
    throw new Error(`${label} 期望 ${expected}，实际 ${actual}`);
  }
}

function assertNull(actual: number | null, label: string): void {
  if (actual !== null) {
    throw new Error(`${label} 期望 null，实际 ${actual}`);
  }
}

// 周期换算：5h → 300 分钟、7d → 10080 分钟，认不出的返回 null。
if (periodMinutes('5h') !== 300) throw new Error(`periodMinutes('5h') 期望 300，实际 ${periodMinutes('5h')}`);
if (periodMinutes('7D') !== 10080) throw new Error(`periodMinutes('7D') 期望 10080，实际 ${periodMinutes('7D')}`);
assertNull(periodMinutes('week'), "periodMinutes('week')");

// (a) 5h 窗口、重置时刻在 2 小时后：已过去 3 小时，比例为 0.6。
const resetsIn2h = new Date(NOW + 2 * HOUR_MS).toISOString();
const ratio = quotaElapsedRatio(resetsIn2h, periodMinutes('5h'), NOW);
if (ratio === null) throw new Error('5h 窗口、重置在 2 小时后应能算出比例');
assertClose(ratio, 0.6, 'quotaElapsedRatio(重置在 2 小时后)');

// (b) 视为未知的四种情况：已到重置时刻、重置超出一个周期、周期认不出、重置时刻非法。
assertNull(quotaElapsedRatio(new Date(NOW - HOUR_MS).toISOString(), 300, NOW), '重置时刻已过去');
assertNull(quotaElapsedRatio(new Date(NOW + 6 * HOUR_MS).toISOString(), 300, NOW), '重置超出一个完整周期');
assertNull(quotaElapsedRatio(resetsIn2h, null, NOW), '周期为 null');
assertNull(quotaElapsedRatio('not-a-date', 300, NOW), '重置时刻非法');

// (c) 已用 80%、时间已过 50%：0..50 正常填充、50..80 超速段，竖线在 50。
const over = quotaBarLayout(80, 0.5, true);
assertClose(over.normalPercent, 50, 'quotaBarLayout(80, 0.5, true).normalPercent');
assertClose(over.overPercent, 30, 'quotaBarLayout(80, 0.5, true).overPercent');
assertClose(over.markerPercent, 50, 'quotaBarLayout(80, 0.5, true).markerPercent');

// (d) 已用 30% 未超过时间进度：全部正常填充，没有超速段。
const under = quotaBarLayout(30, 0.5, true);
assertClose(under.normalPercent, 30, 'quotaBarLayout(30, 0.5, true).normalPercent');
assertClose(under.overPercent, 0, 'quotaBarLayout(30, 0.5, true).overPercent');
assertClose(under.markerPercent, 50, 'quotaBarLayout(30, 0.5, true).markerPercent');

// (e) 不满足门槛时不分段：即使已用超过时间进度也不加超速段。
const unknownPace = quotaBarLayout(80, 0.5, false);
assertClose(unknownPace.normalPercent, 80, 'quotaBarLayout(80, 0.5, false).normalPercent');
assertClose(unknownPace.overPercent, 0, 'quotaBarLayout(80, 0.5, false).overPercent');
assertClose(unknownPace.markerPercent, 50, 'quotaBarLayout(80, 0.5, false).markerPercent');

// (f) 门槛：已用不足 5%、时间已过不足 3% 时都不做节奏判断。
if (quotaPaceKnown(4, 0.5) !== false) throw new Error('quotaPaceKnown(4, 0.5) 应为 false');
if (quotaPaceKnown(10, 0.02) !== false) throw new Error('quotaPaceKnown(10, 0.02) 应为 false');
if (quotaPaceKnown(10, 0.5) !== true) throw new Error('quotaPaceKnown(10, 0.5) 应为 true');
