// 额度节奏换算：与桌面 internal/ui/usage_pace.go 的 quotaElapsedRatioAt / quotaBarSegments 同口径。
// 这里只做纯计算，不依赖组件，供额度条与额度详情弹层共用。

/** 开始区分「超速段」的最低门槛：已用不足 5% 或已过去不足窗口时长的 3% 时不做判断。 */
const PACE_MIN_USED_PERCENT = 5;
const PACE_MIN_ELAPSED_RATIO = 0.03;

/** 把周期标记（5h、7d、1d、30d…）换算成分钟；认不出时返回 null。 */
export function periodMinutes(period: string | null | undefined): number | null {
  const text = (period ?? '').trim().toLowerCase();
  if (!text) return null;
  const match = /^(\d+)\s*([dh])$/.exec(text);
  if (!match) return null;
  const value = Number(match[1]);
  if (!Number.isFinite(value) || value <= 0) return null;
  return value * (match[2] === 'd' ? 24 * 60 : 60);
}

/**
 * 按重置时刻与窗口分钟数计算时间已过比例（0 到 1）。
 * 重置时刻无效、时长未知、窗口还没开始（重置时间晚于一个完整周期）
 * 或已到重置时刻（数据尚未刷新）时返回 null。
 */
export function quotaElapsedRatio(
  resetsAt: string | null | undefined,
  minutes: number | null,
  now: number = Date.now()
): number | null {
  if (!resetsAt || minutes === null || !Number.isFinite(minutes) || minutes <= 0) return null;
  const resetsAtMs = new Date(resetsAt).getTime();
  if (!Number.isFinite(resetsAtMs)) return null;
  const windowMs = minutes * 60_000;
  const elapsed = now - (resetsAtMs - windowMs);
  if (elapsed <= 0 || resetsAtMs <= now) return null;
  return Math.max(0, Math.min(1, elapsed / windowMs));
}

/** 是否满足区分超速段的门槛：已用 >= 5% 且时间已过 >= 3%。 */
export function quotaPaceKnown(usedPercent: number, elapsedRatio: number): boolean {
  return usedPercent >= PACE_MIN_USED_PERCENT && elapsedRatio >= PACE_MIN_ELAPSED_RATIO;
}

/** 进度条的分段布局，取值都是 0..100 的百分比。 */
export interface QuotaBarLayout {
  /** 正常填充段的宽度，从 0 开始 */
  normalPercent: number;
  /** 超速段的宽度，紧接正常填充段之后 */
  overPercent: number;
  /** 重置进度竖线左偏移（时间已过比例） */
  markerPercent: number;
}

/**
 * 把已用比例与时间已过比例折算成进度条的分段布局，对应桌面 quotaBarSegments：
 * 满足门槛且已用超过时间进度时，0..elapsed 为正常填充、elapsed..used 为超速段；
 * 否则 0..used 全部是正常填充；竖线永远落在 elapsed 处。
 */
export function quotaBarLayout(usedPercent: number, elapsedRatio: number, paceKnown: boolean): QuotaBarLayout {
  const used = Math.max(0, Math.min(100, Number.isFinite(usedPercent) ? usedPercent : 0));
  const ratio = Math.max(0, Math.min(1, Number.isFinite(elapsedRatio) ? elapsedRatio : 0));
  const markerPercent = ratio * 100;
  const over = paceKnown && used > markerPercent;
  return {
    normalPercent: over ? markerPercent : used,
    overPercent: over ? used - markerPercent : 0,
    markerPercent
  };
}
