// 额度节奏换算：与桌面 internal/ui/usage_pace.go 的 quotaElapsedRatioAt / quotaBarSegments 等同口径。
// 这里只做纯计算，不依赖组件，供额度条与额度详情弹层共用。

import type { RemoteQuotaWindow } from '../types/quota';

/** 开始区分「超速段」的最低门槛：已用不足 5% 或已过去不足窗口时长的 3% 时不做判断。 */
const PACE_MIN_USED_PERCENT = 5;
const PACE_MIN_ELAPSED_RATIO = 0.03;
const MINUTE_MS = 60_000;
const HOUR_MS = 60 * MINUTE_MS;
const DAY_MS = 24 * HOUR_MS;
const WEEK_MINUTES = 7 * 24 * 60;
const WEEKDAYS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'] as const;

export type QuotaStatusTone = 'normal' | 'warn' | 'danger';
export type QuotaPaceState = 'unknown' | 'normal' | 'early';

export interface QuotaPaceResult {
  state: QuotaPaceState;
  runOutAtMs: number | null;
}

export interface QuotaCompactStatus {
  text: string;
  tone: QuotaStatusTone;
  priority: number;
}

export interface QuotaWindowDetail {
  left: string;
  right: string;
  tone: QuotaStatusTone;
  detail: string;
}

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, value));
}

function pad2(value: number): string {
  return value < 10 ? `0${value}` : String(value);
}

function parseTimestampMs(iso: string | null | undefined): number | null {
  if (!iso) return null;
  const ms = new Date(iso).getTime();
  return Number.isFinite(ms) ? ms : null;
}

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
  const resetsAtMs = parseTimestampMs(resetsAt);
  if (resetsAtMs === null) return null;
  const windowMs = minutes * MINUTE_MS;
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
  const used = clampPercent(usedPercent);
  const ratio = Math.max(0, Math.min(1, Number.isFinite(elapsedRatio) ? elapsedRatio : 0));
  const markerPercent = ratio * 100;
  const over = paceKnown && used > markerPercent;
  return {
    normalPercent: over ? markerPercent : used,
    overPercent: over ? used - markerPercent : 0,
    markerPercent
  };
}

/** 与桌面 formatCompactDuration 一致：把时长格式化成 4h59m、1h12m、45m、1m、2h。 */
export function formatCompactDuration(diffMs: number): string {
  if (!Number.isFinite(diffMs) || diffMs <= 0) return '';
  if (diffMs < MINUTE_MS) return '1m';
  const totalMinutes = Math.floor(diffMs / MINUTE_MS);
  const hours = Math.floor(totalMinutes / 60);
  const mins = totalMinutes % 60;
  if (hours === 0) return `${mins}m`;
  if (mins === 0) return `${hours}h`;
  return `${hours}h${mins}m`;
}

/** 与桌面 quotaResetDayLabel 一致：同一天返回 15:04，明天返回「明天」，7 天内返回「周X」，更远返回「M/D」。 */
export function quotaResetDayLabel(target: string | number | null | undefined, now: number = Date.now()): string {
  const targetMs = typeof target === 'number' ? target : parseTimestampMs(target);
  if (targetMs === null || !Number.isFinite(targetMs)) return '';
  const localNow = new Date(now);
  const rAt = new Date(targetMs);
  const nowDay = new Date(localNow.getFullYear(), localNow.getMonth(), localNow.getDate());
  const resetDay = new Date(rAt.getFullYear(), rAt.getMonth(), rAt.getDate());
  const days = Math.round((resetDay.getTime() - nowDay.getTime()) / DAY_MS);
  if (days === 0) return `${pad2(rAt.getHours())}:${pad2(rAt.getMinutes())}`;
  if (days === 1) return '明天';
  if (days >= 2 && days < 7) return WEEKDAYS[rAt.getDay()];
  return `${rAt.getMonth() + 1}/${rAt.getDate()}`;
}

/** 与桌面 formatCompactReset 一致：24h 内「4h59m 重置」，7 天内「周五重置」，更远「12/18 重置」。 */
export function formatCompactReset(resetsAt: string | null | undefined, now: number = Date.now()): string {
  const targetMs = parseTimestampMs(resetsAt);
  if (targetMs === null) return '';
  if (targetMs <= now) return '即将重置';
  const diff = targetMs - now;
  if (diff < DAY_MS) {
    const dur = formatCompactDuration(diff);
    return dur ? `${dur} 重置` : '即将重置';
  }
  const day = quotaResetDayLabel(targetMs, now);
  if (!day) return '';
  return day.includes('/') ? `${day} 重置` : `${day}重置`;
}

/** 与桌面 formatLimitReset 一致：生成完整中文重置时间文案，供悬停/内联详情使用。 */
export function formatLimitReset(resetsAt: string | null | undefined, now: number = Date.now()): string {
  const targetMs = parseTimestampMs(resetsAt);
  if (targetMs === null) return '';
  if (targetMs <= now) return '即将重置';
  const rAt = new Date(targetMs);
  const diff = targetMs - now;
  if (diff < DAY_MS) {
    const totalSeconds = Math.floor(diff / 1000);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    if (hours > 0) return `${hours} 小时 ${minutes} 分钟后重置`;
    return `${Math.max(1, minutes)} 分钟后重置`;
  }
  if (diff < 7 * DAY_MS) {
    return `${WEEKDAYS[rAt.getDay()]} ${pad2(rAt.getHours())}:${pad2(rAt.getMinutes())} 重置`;
  }
  return `${rAt.getMonth() + 1}月${rAt.getDate()}日 重置`;
}

/** 与桌面 quotaPace 一致：按已用比例与已过去时长线性外推是否会在重置前用尽。 */
export function quotaPace(
  quotaWindow: Pick<RemoteQuotaWindow, 'usedPercent' | 'resetsAt' | 'period'>,
  now: number = Date.now(),
  overrideMinutes?: number | null
): QuotaPaceResult {
  const used = clampPercent(quotaWindow.usedPercent);
  if (used < PACE_MIN_USED_PERCENT) return { state: 'unknown', runOutAtMs: null };
  const minutes = overrideMinutes !== undefined ? overrideMinutes : periodMinutes(quotaWindow.period);
  const ratio = quotaElapsedRatio(quotaWindow.resetsAt, minutes, now);
  if (ratio === null || ratio < PACE_MIN_ELAPSED_RATIO) {
    return { state: 'unknown', runOutAtMs: null };
  }
  if (used >= 100) return { state: 'early', runOutAtMs: now };
  if (minutes === null || minutes <= 0) return { state: 'unknown', runOutAtMs: null };
  const resetsAtMs = parseTimestampMs(quotaWindow.resetsAt);
  if (resetsAtMs === null) return { state: 'unknown', runOutAtMs: null };

  const elapsedMs = Math.round(ratio * minutes * MINUTE_MS);
  const remaining = 100 - used;
  const runOutAtMs = now + Math.round((remaining / used) * elapsedMs);
  if (runOutAtMs < resetsAtMs) {
    return { state: 'early', runOutAtMs };
  }
  return { state: 'normal', runOutAtMs: null };
}

function formatPaceRunOut(runOutAtMs: number | null, now: number): string {
  if (runOutAtMs === null || !Number.isFinite(runOutAtMs)) return '';
  const diff = runOutAtMs - now;
  if (diff <= 0) return '';
  if (diff < MINUTE_MS) return '1 分钟内';
  if (diff < HOUR_MS) return `约 ${Math.floor(diff / MINUTE_MS)} 分钟后`;
  if (diff < DAY_MS) {
    const hours = Math.floor(diff / HOUR_MS);
    const minutes = Math.floor(diff / MINUTE_MS) % 60;
    if (minutes > 0) return `约 ${hours} 时 ${minutes} 分后`;
    return `约 ${hours} 小时后`;
  }
  return `约 ${Math.floor(diff / DAY_MS)} 天后`;
}

function paceRunOutText(runOutAtMs: number | null, state: QuotaPaceState, now: number): string {
  if (state !== 'early') return '';
  const rel = formatPaceRunOut(runOutAtMs, now);
  return rel ? `预计${rel}用尽` : '预计将提前用尽';
}

function quotaPaceRunOutText(
  runOutAtMs: number | null,
  resetsAt: string | null | undefined,
  state: QuotaPaceState,
  isWeek: boolean,
  now: number
): string {
  if (state !== 'early') return '';
  if (isWeek) {
    const runOutDay = quotaResetDayLabel(runOutAtMs, now);
    const resetDay = quotaResetDayLabel(resetsAt, now);
    if (runOutDay && resetDay) {
      return `${runOutDay}用尽 · ${resetDay}重置`;
    }
  }
  return paceRunOutText(runOutAtMs, state, now);
}

const UNKNOWN_QUOTA_DISPLAYS = new Set([
  'unknown',
  'n/a',
  'na',
  '--',
  '-',
  '未知',
  '无读数',
  'none',
  'null'
]);

/** 与桌面 isUnknownQuotaDisplay 一致：判断展示文案是否代表无有效读数。 */
export function isUnknownQuotaDisplay(display: string | null | undefined): boolean {
  return UNKNOWN_QUOTA_DISPLAYS.has((display ?? '').trim().toLowerCase());
}

/** 与桌面 quotaWindowHasNoReading 一致：判断额度窗口是否属于无有效读数的次要项。 */
export function quotaWindowHasNoReading(quotaWindow: RemoteQuotaWindow): boolean {
  if (quotaWindow.text === true) {
    const d = (quotaWindow.display ?? '').trim();
    return d === '' || isUnknownQuotaDisplay(d);
  }
  if (quotaWindow.unlimited === true || typeof quotaWindow.balance === 'number') {
    return false;
  }
  const d = (quotaWindow.display ?? '').trim();
  return isUnknownQuotaDisplay(d) && quotaWindow.usedPercent <= 0;
}

function isNonPercentWindow(w: RemoteQuotaWindow): boolean {
  return (
    w.text === true ||
    w.unlimited === true ||
    (typeof w.balance === 'number' && clampPercent(w.usedPercent) === 0)
  );
}

function formatQuotaBalanceText(balance: number, unit?: string | null): string {
  if (!Number.isFinite(balance)) return '';
  const value = String(Math.round(balance * 100) / 100);
  const cleanUnit = (unit ?? '').trim();
  return cleanUnit ? `剩余 ${value} ${cleanUnit}` : `剩余 ${value}`;
}

/** 与桌面 quotaWindowCompactStatus 一致：生成单窗口右列紧凑状态词、语义色调与优先级。 */
export function quotaWindowCompactStatus(
  quotaWindow: RemoteQuotaWindow,
  shortLabel: string,
  sortMinutes: number,
  now: number = Date.now(),
  overrideMinutes?: number | null
): QuotaCompactStatus {
  if (isNonPercentWindow(quotaWindow)) {
    const resetText = formatCompactReset(quotaWindow.resetsAt, now);
    if (resetText) return { text: resetText, tone: 'normal', priority: 1 };
    return { text: '', tone: 'normal', priority: 0 };
  }

  const clamped = clampPercent(quotaWindow.usedPercent);
  const pct = Math.round(clamped);
  const label = shortLabel.trim();
  const isWeek = sortMinutes === WEEK_MINUTES;
  const { state, runOutAtMs } = quotaPace(quotaWindow, now, overrideMinutes);

  if (pct >= 100) {
    const resetText = formatCompactReset(quotaWindow.resetsAt, now);
    return { text: resetText || '已用尽', tone: 'danger', priority: 4 };
  }

  if (clamped >= 80) {
    const tone: QuotaStatusTone = clamped >= 95 ? 'danger' : 'warn';
    return { text: label ? `${label} ${pct}%` : `${pct}%`, tone, priority: 3 };
  }

  if (state === 'early') {
    if (runOutAtMs !== null && runOutAtMs > now) {
      const diff = runOutAtMs - now;
      if (diff >= DAY_MS) {
        const day = quotaResetDayLabel(runOutAtMs, now);
        if (day) {
          return {
            text: day.includes('/') ? `${day} 用尽` : `${day}用尽`,
            tone: 'warn',
            priority: 2
          };
        }
      }
      const dur = formatCompactDuration(diff);
      if (dur) return { text: `${dur} 用尽`, tone: 'warn', priority: 2 };
    }
    if (isWeek) {
      const day = quotaResetDayLabel(quotaWindow.resetsAt, now);
      if (day) return { text: `${day}前用尽`, tone: 'warn', priority: 2 };
    }
    return { text: '将用尽', tone: 'warn', priority: 2 };
  }

  const resetText = formatCompactReset(quotaWindow.resetsAt, now);
  if (resetText) return { text: resetText, tone: 'normal', priority: 1 };
  return { text: '', tone: 'normal', priority: 0 };
}

/** 与桌面 quotaWindowHoverDetail 一致：生成单窗口完整详情的左右段文本与语义色调。 */
export function quotaWindowHoverDetail(
  quotaWindow: RemoteQuotaWindow,
  shortLabel: string,
  sortMinutes: number,
  now: number = Date.now(),
  overrideMinutes?: number | null
): QuotaWindowDetail {
  const label = shortLabel.trim();
  const resetFull = formatLimitReset(quotaWindow.resetsAt, now);

  if (isNonPercentWindow(quotaWindow)) {
    let val = '';
    if (quotaWindow.text === true) {
      val = (quotaWindow.display ?? '').trim();
    } else if (typeof quotaWindow.balance === 'number') {
      val = formatQuotaBalanceText(quotaWindow.balance, quotaWindow.unit);
    } else if (quotaWindow.unlimited === true) {
      val = '不限量';
    }
    const left = label && val ? `${label} · ${val}` : val || label;
    const detail = left && resetFull ? `${left} · ${resetFull}` : left || resetFull;
    return { left, right: resetFull, tone: 'normal', detail };
  }

  const clamped = clampPercent(quotaWindow.usedPercent);
  const pct = Math.round(clamped);
  const isWeek = sortMinutes === WEEK_MINUTES;
  const { state, runOutAtMs } = quotaPace(quotaWindow, now, overrideMinutes);

  const pctPart = pct >= 100 ? '已用尽' : `${pct}%`;
  const left = label ? `${label} ${pctPart}` : pctPart;

  let tone: QuotaStatusTone = 'normal';
  let right = resetFull;
  if (pct >= 100) {
    tone = 'danger';
    right = resetFull;
  } else if (clamped >= 95) {
    tone = 'danger';
    right = quotaPaceRunOutText(runOutAtMs, quotaWindow.resetsAt, state, isWeek, now) || resetFull;
  } else if (clamped >= 80) {
    tone = 'warn';
    right = quotaPaceRunOutText(runOutAtMs, quotaWindow.resetsAt, state, isWeek, now) || resetFull;
  } else if (state === 'early') {
    tone = 'warn';
    const pace = quotaPaceRunOutText(runOutAtMs, quotaWindow.resetsAt, state, isWeek, now);
    if (!isWeek && resetFull && pace) {
      right = `${pace} · ${resetFull}`;
    } else {
      right = pace || resetFull;
    }
  }

  const detail = left && right ? `${left} · ${right}` : left || right;
  return { left, right, tone, detail };
}

/** 周期短标签：5h → 5h、7d → 周、1d → 日、30d → 月，其余 Nd/Nh 原样返回，认不出为 null。 */
export function periodShortLabel(period: string | null | undefined): string | null {
  const text = (period ?? '').trim().toLowerCase();
  if (!text) return null;
  switch (text) {
    case '5h':
      return '5h';
    case '7d':
      return '周';
    case '1d':
      return '日';
    case '30d':
      return '月';
    default:
      break;
  }
  return /^\d+[dh]$/.test(text) ? text : null;
}

/** 附加用量窗口（MCP tools、按需附加用量、预付费余额占位）不是订阅额度，不进主额度列表。 */
export function isAuxiliaryWindow(quotaWindow: RemoteQuotaWindow): boolean {
  const id = (quotaWindow.id ?? '').trim().toLowerCase();
  if (id) {
    return id === 'ai-credits' || id.includes('extra-usage') || id.startsWith('tools-');
  }
  const title = (quotaWindow.title ?? '').toLowerCase();
  return title.includes('mcp tools') || title.includes('extra usage');
}

/** 认不出周期时的窗口短标签，与桌面 trayQuotaFallbackLabel 同口径（credits → 额度，超过 8 个字截断）。 */
export function fallbackWindowLabel(quotaWindow: RemoteQuotaWindow): string {
  const raw = (quotaWindow.label ?? '').trim() || (quotaWindow.title ?? '').trim();
  if (!raw) return '额度';
  if (raw.toLowerCase() === 'credits') return '额度';
  const runes = Array.from(raw);
  return runes.length > 8 ? `${runes.slice(0, 8).join('')}…` : raw;
}

export const FALLBACK_SORT_MINUTES = Number.MAX_SAFE_INTEGER;

/** 解析窗口短标签、排序分钟数与可用于推算节奏的周期分钟数。 */
export function resolveWindowPeriod(quotaWindow: RemoteQuotaWindow): {
  shortLabel: string;
  sortMinutes: number;
  minutes: number | null;
} {
  const minutes = periodMinutes(quotaWindow.period);
  const label = periodShortLabel(quotaWindow.period);
  if (minutes !== null && label) {
    return { shortLabel: label, sortMinutes: minutes, minutes };
  }
  const hint = `${quotaWindow.id ?? ''} ${quotaWindow.label ?? ''} ${quotaWindow.title ?? ''}`.toLowerCase();
  if (/(?:^|[^0-9a-z])5h(?:$|[^0-9a-z])/.test(hint)) {
    return { shortLabel: '5h', sortMinutes: 5 * 60, minutes: 5 * 60 };
  }
  if (/(?:^|[^0-9a-z])7d(?:$|[^0-9a-z])/.test(hint)) {
    return { shortLabel: '周', sortMinutes: WEEK_MINUTES, minutes: WEEK_MINUTES };
  }
  return {
    shortLabel: fallbackWindowLabel(quotaWindow),
    sortMinutes: FALLBACK_SORT_MINUTES,
    minutes: null
  };
}
