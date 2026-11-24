import { SheetFrame } from './sheet/SheetFrame';
// 移动端输入框额度详情底部弹层组件（点击额度条中某个 provider 组打开）

import React, { useEffect, useState } from 'react';
import type { RemoteProvider } from '../types/generated';
import type { RemoteQuotaBudget, RemoteQuotaWindow } from '../types/quota';
import { providerDisplayName } from '../utils/modelLabel';
import {
  formatCompactReset,
  isAuxiliaryWindow,
  quotaBarLayout,
  quotaElapsedRatio,
  quotaPaceKnown,
  quotaWindowCompactStatus,
  quotaWindowHasNoReading,
  quotaWindowHoverDetail,
  resolveWindowPeriod,
  type QuotaBarLayout,
  type QuotaStatusTone,
  type QuotaWindowDetail
} from '../utils/quotaPace';
import { ProviderIcon } from './ProviderIcon';
import { ArrowPathIcon, ChevronRightIcon, XMarkIcon } from './Icons';

export interface AgyAccountChoice {
  accountID: string;
  account: string;
  disabled: boolean;
  anyWindowFull?: boolean;
}

export interface ComposerQuotaSheetProps {
  isOpen: boolean;
  onClose: () => void;
  /** 当前查看的 provider；为空时弹层关闭 */
  provider: string | null;
  /** 可选 provider 列表，用于在无 providerName 时映射显示名称 */
  providers?: RemoteProvider[];
  /** 当前查看的角色：主代理或子代理 */
  role?: 'lead' | 'bees' | null;
  /** 该 provider 的全部 budget（含 loading / attention 等不可渲染状态） */
  budgets: RemoteQuotaBudget[];
  /** 额度条当前选中的账号，命中时标记为「在用」 */
  activeAccount?: string | null;
  /** 是否处于已有会话（草稿页无 threadID 时为 false，保持只读） */
  hasThread?: boolean;
  /** 当前线程显式选中的 agy 账号 ID（lead 取 account，bees 取 swarmBeesAccount；空表示自动） */
  selectedAccountID?: string;
  /** 主会话是否运行中（lead 锁定判定用） */
  isRunning?: boolean;
  /** 主会话是否已有消息（lead 锁定判定用） */
  hasMessages?: boolean;
  /** 选中某个账号（空串表示自动） */
  onSelectAccount?: (accountID: string) => void;
  /** 刷新请求进行中 */
  refreshing?: boolean;
  onRefresh: () => void;
  /** 测试注入时钟（默认 Date.now()） */
  now?: number;
}

/** 已用百分比对应的配色，额度条与弹层共用。 */
export function usageColor(usedPercent: number): string {
  if (usedPercent < 50) return 'var(--quota-low)';
  if (usedPercent < 80) return 'var(--quota-mid)';
  if (usedPercent < 95) return 'var(--quota-high)';
  return 'var(--quota-critical)';
}

/** 百分比文字配色：用量不高时用弱化文字色，其余跟进度条同色。 */
export function usagePercentColor(usedPercent: number): string {
  if (usedPercent < 50) return 'var(--foreground-muted)';
  return usageColor(usedPercent);
}

function pad2(value: number): string {
  return value < 10 ? `0${value}` : String(value);
}

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, value));
}

/**
 * 百分比文案：整数不带小数，非整数保留一位（usedPercent 现在可能是小数）。
 * 桌面端托盘按整数四舍五入，这里多留一位小数，避免 0.5% 的变化被抹平。
 */
export function formatQuotaPercent(value: number): string {
  const clamped = clampPercent(value);
  const fixed = clamped.toFixed(1);
  return fixed.endsWith('.0') ? fixed.slice(0, -2) : fixed;
}

/** 与桌面端 formatTrayQuotaBalance 一致：四舍五入到两位小数，输出「剩余 12.3 credits」。 */
export function formatQuotaBalance(balance: number, unit?: string | null): string {
  if (!Number.isFinite(balance)) return '';
  const value = String(Math.round(balance * 100) / 100);
  const cleanUnit = (unit ?? '').trim();
  return cleanUnit ? `剩余 ${value} ${cleanUnit}` : `剩余 ${value}`;
}

/** 与桌面端 formatQuotaReadAge 一致：读取时间转成「刚刚 / 3 分钟前 / 2 小时前 / 1月2日 15:04」。 */
export function formatQuotaReadAge(readAt?: string | null, now: number = Date.now()): string {
  const raw = (readAt ?? '').trim();
  if (!raw) return '';
  const readMs = new Date(raw).getTime();
  if (!Number.isFinite(readMs)) return '';
  if (readMs >= now) return '刚刚';
  const age = now - readMs;
  if (age < 60 * 1000) return '刚刚';
  if (age < 60 * 60 * 1000) return `${Math.floor(age / (60 * 1000))} 分钟前`;
  if (age < 24 * 60 * 60 * 1000) return `${Math.floor(age / (60 * 60 * 1000))} 小时前`;
  const at = new Date(readMs);
  return `${at.getMonth() + 1}月${at.getDate()}日 ${pad2(at.getHours())}:${pad2(at.getMinutes())}`;
}

/** 一个额度窗口该显示什么：要么百分比，要么一段文字（不含进度条）。 */
export type QuotaWindowContent =
  | { kind: 'percent'; usedPercent: number }
  | { kind: 'text' | 'balance' | 'unlimited'; text: string };

/**
 * 照桌面端托盘规则（internal/ui/tray_quota_panel.go 的 trayQuotaWindowDrawsBar 与 buildTrayQuotaModel）：
 * text 为 true 只显示 display 文字；余额窗口（用量为 0）显示「剩余 x unit」；unlimited 显示「不限量」。
 * 这三类都不看 usedPercent、不画进度条，因此不能被当成「没有额度」丢掉。
 */
export function quotaWindowContent(quotaWindow: RemoteQuotaWindow): QuotaWindowContent {
  if (quotaWindow.text === true) {
    const display = (quotaWindow.display ?? '').trim();
    return { kind: 'text', text: display || (quotaWindow.title ?? '').trim() || '额度' };
  }
  const usedPercent = clampPercent(quotaWindow.usedPercent);
  if (typeof quotaWindow.balance === 'number' && usedPercent === 0) {
    return { kind: 'balance', text: formatQuotaBalance(quotaWindow.balance, quotaWindow.unit) };
  }
  if (quotaWindow.unlimited === true) return { kind: 'unlimited', text: '不限量' };
  return { kind: 'percent', usedPercent };
}

/** 账号组里第一个非空套餐名（照桌面 quotaGroupPlan）。 */
export function quotaGroupPlan(budgets: RemoteQuotaBudget[]): string {
  for (const budget of budgets) {
    for (const quotaWindow of budget.windows) {
      const plan = (quotaWindow.plan ?? '').trim();
      if (plan) return plan;
    }
  }
  return '';
}

/** 账号组里第一个已知的读取时间（照桌面 quotaGroupReadAt），全部未知时为 null。 */
export function quotaGroupReadAt(budgets: RemoteQuotaBudget[]): string | null {
  for (const budget of budgets) {
    for (const quotaWindow of budget.windows) {
      const readAt = (quotaWindow.readAt ?? '').trim();
      if (readAt) return readAt;
    }
  }
  return null;
}

/** 账号组是否整组已停用：每个窗口都带 disabled 才算（照桌面 groupQuotaWindowsByAccount 的 acc.disabled）。 */
export function quotaGroupDisabled(budgets: RemoteQuotaBudget[]): boolean {
  return (
    budgets.length > 0 &&
    budgets.every(
      (budget) => budget.windows.length > 0 && budget.windows.every((quotaWindow) => quotaWindow.disabled === true)
    )
  );
}

function is5hWindow(quotaWindow: RemoteQuotaWindow): boolean {
  const period = (quotaWindow.period ?? '').trim().toLowerCase();
  if (period === '5h') return true;
  const id = (quotaWindow.id ?? '').trim().toLowerCase();
  if (!id) return false;
  return id === '5h' || /(?:^|[^0-9])5h(?:$|[^a-z0-9])/.test(id);
}

interface AgyAccountEntry {
  accountID: string;
  account: string;
  allWindowsDisabled: boolean;
  fiveHourFull: boolean;
  anyWindowFull: boolean;
}

function collectAgyAccounts(budgets: RemoteQuotaBudget[]): AgyAccountEntry[] {
  const order: string[] = [];
  const grouped = new Map<string, RemoteQuotaBudget[]>();
  for (const budget of budgets) {
    if (budget.provider !== 'agy') continue;
    const accountID = (budget.accountID ?? '').trim();
    if (!accountID) continue;
    const existing = grouped.get(accountID);
    if (existing) {
      existing.push(budget);
      continue;
    }
    grouped.set(accountID, [budget]);
    order.push(accountID);
  }
  return order.map((accountID) => {
    const accountBudgets = grouped.get(accountID) ?? [];
    const windows = accountBudgets.flatMap((budget) => budget.windows);
    let account = '';
    for (const budget of accountBudgets) {
      const name = (budget.account ?? '').trim();
      if (name) {
        account = name;
        break;
      }
    }
    if (!account) {
      for (const quotaWindow of windows) {
        const name = (quotaWindow.account ?? '').trim();
        if (name) {
          account = name;
          break;
        }
      }
    }
    const allWindowsDisabled = !windows.some((quotaWindow) => quotaWindow.disabled !== true);
    const fiveHourFull = windows.some((quotaWindow) => is5hWindow(quotaWindow) && quotaWindow.usedPercent >= 100);
    const anyWindowFull = windows.some((quotaWindow) => {
      const content = quotaWindowContent(quotaWindow);
      return content.kind === 'percent' && content.usedPercent >= 100;
    });
    return {
      accountID,
      account: account || accountID,
      allWindowsDisabled,
      fiveHourFull,
      anyWindowFull
    };
  });
}

/** 提取 agy 可选账号列表（按 accountID 去重保序）：窗口全停用或任一 5h 窗口已满则禁用。 */
export function agyAccountChoices(budgets: RemoteQuotaBudget[]): AgyAccountChoice[] {
  return collectAgyAccounts(budgets).map((entry) => ({
    accountID: entry.accountID,
    account: entry.account,
    disabled: entry.allWindowsDisabled || entry.fiveHourFull,
    anyWindowFull: entry.anyWindowFull
  }));
}

/** 仅在已有会话且存在至少 2 个窗口未全部停用的不同 agy 账号时允许切换账号。 */
export function agyAccountSelectable(budgets: RemoteQuotaBudget[], hasThread: boolean): boolean {
  if (!hasThread) return false;
  return collectAgyAccounts(budgets).filter((entry) => !entry.allWindowsDisabled).length >= 2;
}

/**
 * 判断主代理 agy 账号选择器是否锁定：
 * 运行中锁定；无消息解锁；有消息时默认锁定，
 * 例外是线程显式指定的账号不在 choices 里、已禁用（全停用/5h 满额）或任一窗口满额时解锁允许换号。
 */
export function agyLeadAccountLocked(opts: {
  isRunning: boolean;
  hasMessages: boolean;
  currentAccountID: string;
  choices: readonly Pick<AgyAccountChoice, 'accountID' | 'disabled' | 'anyWindowFull'>[];
}): boolean {
  if (opts.isRunning) return true;
  if (!opts.hasMessages) return false;
  const currentID = opts.currentAccountID.trim();
  if (!currentID) return true;
  const matched = opts.choices.find((choice) => choice.accountID === currentID);
  if (!matched || matched.disabled || matched.anyWindowFull) return false;
  return true;
}

interface SheetWindowModel {
  shortLabel: string;
  sortMinutes: number;
  usedPercent: number;
  noBar: boolean;
  noBarText: string;
  resetsAtMs: number | null;
  compactText: string;
  statusTone: QuotaStatusTone;
  statusPriority: number;
  detail: QuotaWindowDetail;
  paceLayout: QuotaBarLayout | null;
}

interface SheetNoReadingItem {
  account: string;
  label: string;
  value: string;
}

interface SheetAccountDetail {
  compact: string;
  tone: QuotaStatusTone;
  detailLeft: string;
  detailRight: string;
  detailFull: string;
  detailTone: QuotaStatusTone;
}

interface SheetAccountModel {
  key: string;
  accountID: string;
  displayName: string;
  plan: string;
  isActive: boolean;
  allDisabled: boolean;
  windows: SheetWindowModel[];
  detail: SheetAccountDetail;
}

interface SheetColumn {
  shortLabel: string;
  sortMinutes: number;
}

interface AllocatedColumnCell {
  win: SheetWindowModel | null;
  inlineTag: boolean;
}

function stripProviderPrefix(name: string, provider: string, providerName: string): string {
  const trimmed = name.trim();
  const idx = trimmed.indexOf(' · ');
  if (idx > 0) {
    const prefix = trimmed.slice(0, idx).trim().toLowerCase();
    if (
      prefix &&
      (prefix === provider.toLowerCase() || prefix === providerName.toLowerCase())
    ) {
      const rest = trimmed.slice(idx + 3).trim();
      if (rest) return rest;
    }
  }
  return trimmed;
}

function toneRank(tone: QuotaStatusTone): number {
  if (tone === 'danger') return 2;
  if (tone === 'warn') return 1;
  return 0;
}

function summarizeAccountCompactAndDetail(
  windows: readonly SheetWindowModel[],
  noReadings: readonly SheetNoReadingItem[],
  now: number
): SheetAccountDetail {
  let bestPriority = -1;
  let bestUsed = -1;
  let bestResetMs: number | null = null;
  let compact = '';
  let tone: QuotaStatusTone = 'normal';

  for (const win of windows) {
    if (!win.compactText) continue;
    const winFutureReset = win.resetsAtMs !== null && win.resetsAtMs > now;
    const bestFutureReset = bestResetMs !== null && bestResetMs > now;
    let better = false;
    if (win.statusPriority > bestPriority) {
      better = true;
    } else if (win.statusPriority === bestPriority) {
      if (win.statusPriority >= 3 && win.usedPercent !== bestUsed) {
        better = win.usedPercent > bestUsed;
      } else if (winFutureReset && (!bestFutureReset || (bestResetMs !== null && win.resetsAtMs! < bestResetMs))) {
        better = true;
      } else if (!bestFutureReset && bestResetMs === null && win.resetsAtMs !== null) {
        better = true;
      }
    }
    if (better) {
      bestPriority = win.statusPriority;
      bestUsed = win.usedPercent;
      bestResetMs = win.resetsAtMs;
      compact = win.compactText;
      tone = win.statusTone;
    }
  }

  const parts: string[] = [];
  const rightParts: string[] = [];
  let detailLeft = '';
  let detailTone: QuotaStatusTone = 'normal';
  for (let i = 0; i < windows.length; i += 1) {
    const win = windows[i];
    const part = win.detail.detail;
    if (!part) continue;
    parts.push(part);
    if (detailLeft === '') {
      detailLeft = part;
    } else {
      rightParts.push(part);
    }
    if (toneRank(win.detail.tone) > toneRank(detailTone)) {
      detailTone = win.detail.tone;
    }
  }

  for (const nr of noReadings) {
    const note = nr.value ? (nr.label ? `${nr.label}: ${nr.value}` : nr.value) : nr.label;
    if (!note) continue;
    parts.push(note);
    if (detailLeft === '') {
      detailLeft = note;
    } else {
      rightParts.push(note);
    }
  }

  let detailRight = '';
  if (rightParts.length > 0) {
    detailRight = rightParts.join(' · ');
  } else if (windows.length === 1) {
    detailLeft = windows[0].detail.left;
    detailRight = windows[0].detail.right;
  }

  return {
    compact,
    tone,
    detailLeft,
    detailRight,
    detailFull: parts.join(' · '),
    detailTone
  };
}

/** 提取表头列（最多 2 列，按出现频次降序、周期短优先排序，最终按周期从短到长排列）。 */
function deriveQuotaColumns(accounts: readonly SheetAccountModel[]): SheetColumn[] {
  const byLabel = new Map<string, { shortLabel: string; sortMinutes: number; count: number }>();
  for (const acc of accounts) {
    const seen = new Set<string>();
    for (const win of acc.windows) {
      if (win.noBar || !win.shortLabel) continue;
      const existing = byLabel.get(win.shortLabel);
      if (!existing) {
        byLabel.set(win.shortLabel, {
          shortLabel: win.shortLabel,
          sortMinutes: win.sortMinutes,
          count: 1
        });
        seen.add(win.shortLabel);
      } else {
        if (win.sortMinutes < existing.sortMinutes) {
          existing.sortMinutes = win.sortMinutes;
        }
        if (!seen.has(win.shortLabel)) {
          seen.add(win.shortLabel);
          existing.count += 1;
        }
      }
    }
  }
  if (byLabel.size === 0) return [];
  const stats = Array.from(byLabel.values()).sort((a, b) => {
    if (a.count !== b.count) return b.count - a.count;
    return a.sortMinutes - b.sortMinutes;
  });
  return stats
    .slice(0, 2)
    .sort((a, b) => a.sortMinutes - b.sortMinutes)
    .map(({ shortLabel, sortMinutes }) => ({ shortLabel, sortMinutes }));
}

function allocateQuotaColumns(
  windows: readonly SheetWindowModel[],
  cols: readonly SheetColumn[]
): { cells: AllocatedColumnCell[]; extra: SheetWindowModel[] } {
  const cells: AllocatedColumnCell[] = cols.map(() => ({ win: null, inlineTag: false }));
  const used = new Array<boolean>(windows.length).fill(false);

  for (let ci = 0; ci < cols.length; ci += 1) {
    const col = cols[ci];
    for (let wi = 0; wi < windows.length; wi += 1) {
      if (used[wi] || windows[wi].noBar) continue;
      if (windows[wi].shortLabel === col.shortLabel) {
        used[wi] = true;
        cells[ci] = { win: windows[wi], inlineTag: false };
        break;
      }
    }
  }

  const extra: SheetWindowModel[] = [];
  for (let wi = 0; wi < windows.length; wi += 1) {
    if (used[wi]) continue;
    if (windows[wi].noBar) {
      extra.push(windows[wi]);
      continue;
    }
    let placed = false;
    for (let ci = 0; ci < cells.length; ci += 1) {
      if (cells[ci].win === null) {
        cells[ci] = { win: windows[wi], inlineTag: true };
        placed = true;
        break;
      }
    }
    if (!placed) extra.push(windows[wi]);
  }
  return { cells, extra };
}

function statusToneClass(tone: QuotaStatusTone): string {
  if (tone === 'danger') return 'text-[var(--mc-danger)] font-medium';
  if (tone === 'warn') return 'text-[var(--mc-warning)] font-medium';
  return 'text-[var(--mc-text-tertiary)]';
}

function detailRightToneClass(tone: QuotaStatusTone): string {
  if (tone === 'danger') return 'text-[var(--mc-danger)] font-medium';
  if (tone === 'warn') return 'text-[var(--mc-warning)] font-medium';
  return 'text-[var(--mc-text-secondary)]';
}

function renderQuotaBar(usedPercent: number, paceLayout: QuotaBarLayout | null) {
  const color = usageColor(usedPercent);
  const fillWidth = paceLayout ? paceLayout.normalPercent + paceLayout.overPercent : clampPercent(usedPercent);
  return (
    <span
      aria-hidden="true"
      className="relative h-1 flex-1 min-w-[20px] overflow-hidden rounded-full bg-[var(--mc-overlay-selected)]"
    >
      <span
        className="absolute left-0 top-0 bottom-0 rounded-full transition-[width,background-color] duration-mc-base motion-reduce:transition-none"
        style={{ width: `${fillWidth}%`, backgroundColor: color }}
      />
      {paceLayout !== null && (
        <span
          data-testid="quota-pace-marker"
          title={`时间已过 ${Math.round(paceLayout.markerPercent)}%`}
          className="absolute top-0 bottom-0 w-[1.5px] -translate-x-1/2 bg-[var(--mc-text-primary)] opacity-75"
          style={{
            left: `calc(min(max(${paceLayout.markerPercent}%, 0.75px), 100% - 0.75px))`
          }}
        />
      )}
    </span>
  );
}

function renderInlineDetailBox(
  expanded: boolean,
  left: string,
  right: string,
  full: string,
  tone: QuotaStatusTone
) {
  const hasPair = left !== '' && right !== '';
  const bodyText = full || left || right;
  if (!bodyText) return null;
  return (
    <div
      aria-hidden={!expanded}
      className={`grid transition-[grid-template-rows,opacity] duration-mc-snap ease-out motion-reduce:transition-none ${
        expanded ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0 pointer-events-none'
      }`}
    >
      <div className="overflow-hidden">
        <div className="mx-1 mb-1 mt-0.5 flex flex-wrap items-center justify-between gap-x-3 gap-y-0.5 rounded-mc-md border border-[var(--mc-hairline)] bg-[var(--mc-surface-raised)] px-2.5 py-1.5 text-mc-caption tabular-nums">
          {hasPair ? (
            <>
              <span className="min-w-0 text-[var(--mc-text-secondary)]">{left}</span>
              <span className={`shrink-0 ${detailRightToneClass(tone)}`}>{right}</span>
            </>
          ) : (
            <span className={detailRightToneClass(tone)}>{bodyText}</span>
          )}
        </div>
      </div>
    </div>
  );
}

export interface ComposerQuotaSheetContentProps extends Omit<ComposerQuotaSheetProps, 'isOpen'> {
  expandedRowKey: string | null;
  onToggleRow: (rowKey: string) => void;
  noReadingsOpen: boolean;
  onToggleNoReadings: () => void;
}

export const ComposerQuotaSheetContent: React.FC<ComposerQuotaSheetContentProps> = ({
  onClose,
  provider,
  providers,
  role = null,
  budgets,
  activeAccount = null,
  hasThread = false,
  selectedAccountID = '',
  isRunning = false,
  hasMessages = false,
  onSelectAccount,
  refreshing = false,
  onRefresh,
  now = Date.now(),
  expandedRowKey,
  onToggleRow,
  noReadingsOpen,
  onToggleNoReadings
}) => {
  if (!provider) return null;

  const providerBudgets = budgets.filter(
    (budget) => budget.provider === provider && (!role || budget.role === role)
  );
  const providerName = providerBudgets[0]?.providerName || providerDisplayName(providers, provider);
  const credits = providerBudgets.find((budget) => budget.amount !== null) ?? null;
  const anyWindows = providerBudgets.some((budget) => budget.windows.length > 0);
  const loading = providerBudgets.some((budget) => budget.state === 'loading');
  const unavailable = providerBudgets.some((budget) => budget.state === 'attention' || budget.state === 'unknown');

  const canSelectAccount = provider === 'agy' && agyAccountSelectable(providerBudgets, hasThread);
  const choices = canSelectAccount ? agyAccountChoices(providerBudgets) : [];
  const choiceMap = new Map(choices.map((choice) => [choice.accountID, choice]));
  const currentAccountID = selectedAccountID.trim();
  const locked = canSelectAccount
    ? role === 'bees'
      ? false
      : agyLeadAccountLocked({ isRunning, hasMessages, currentAccountID, choices })
    : false;

  const handleSelectAccount = (accountID: string) => {
    if (locked) return;
    onClose();
    onSelectAccount?.(accountID);
  };

  // 按账号分组（有 accountID 时以它为键避免同名合并），保持接口返回顺序
  const groupOrder: string[] = [];
  const accountGroupMap = new Map<string, RemoteQuotaBudget[]>();
  for (const budget of providerBudgets) {
    const groupKey = (budget.accountID ?? '').trim() || (budget.account ?? '').trim();
    const existing = accountGroupMap.get(groupKey);
    if (existing) {
      existing.push(budget);
      continue;
    }
    accountGroupMap.set(groupKey, [budget]);
    groupOrder.push(groupKey);
  }

  const groupNoReadings: SheetNoReadingItem[] = [];
  const accounts: SheetAccountModel[] = [];

  for (let idx = 0; idx < groupOrder.length; idx += 1) {
    const groupKey = groupOrder[idx];
    const groupBudgets = accountGroupMap.get(groupKey) ?? [];
    const rawWindows = groupBudgets.flatMap((b) => b.windows);
    const groupAccountID = (groupBudgets.find((b) => (b.accountID ?? '').trim())?.accountID ?? '').trim();
    const rawAccountName =
      (groupBudgets.find((b) => (b.account ?? '').trim())?.account ?? '').trim() ||
      (rawWindows.find((w) => (w.account ?? '').trim())?.account ?? '').trim() ||
      groupAccountID;
    const displayName = stripProviderPrefix(rawAccountName, provider, providerName) || (groupOrder.length > 1 ? '默认账号' : '');
    const isActive =
      groupBudgets.some((b) => b.active === true) ||
      (Boolean(activeAccount) && (rawAccountName === activeAccount || displayName === activeAccount));
    const plan = quotaGroupPlan(groupBudgets);
    const allDisabled = rawWindows.length === 0 || !rawWindows.some((w) => w.disabled !== true);

    const accNoReadings: SheetNoReadingItem[] = [];
    const windows: SheetWindowModel[] = [];
    for (const quotaWindow of rawWindows) {
      if (isAuxiliaryWindow(quotaWindow)) continue;
      if (quotaWindowHasNoReading(quotaWindow)) {
        const nr: SheetNoReadingItem = {
          account: displayName,
          label: (quotaWindow.label ?? '').trim() || (quotaWindow.title ?? '').trim(),
          value: (quotaWindow.display ?? '').trim() || 'Unknown'
        };
        accNoReadings.push(nr);
        groupNoReadings.push(nr);
        continue;
      }
      const content = quotaWindowContent(quotaWindow);
      const noBar = content.kind !== 'percent';
      const usedPercent = content.kind === 'percent' ? content.usedPercent : 0;
      const { shortLabel, sortMinutes, minutes } = resolveWindowPeriod(quotaWindow);
      const compact = quotaWindowCompactStatus(quotaWindow, shortLabel, sortMinutes, now, minutes);
      const detail = quotaWindowHoverDetail(quotaWindow, shortLabel, sortMinutes, now, minutes);
      const paceElapsed = !noBar ? quotaElapsedRatio(quotaWindow.resetsAt, minutes, now) : null;
      const paceLayout =
        paceElapsed === null
          ? null
          : quotaBarLayout(usedPercent, paceElapsed, quotaPaceKnown(usedPercent, paceElapsed));
      const resetsAtMs = quotaWindow.resetsAt ? new Date(quotaWindow.resetsAt).getTime() : NaN;
      windows.push({
        shortLabel,
        sortMinutes,
        usedPercent,
        noBar,
        noBarText: content.kind === 'percent' ? '' : content.text,
        resetsAtMs: Number.isFinite(resetsAtMs) ? resetsAtMs : null,
        compactText: compact.text,
        statusTone: compact.tone,
        statusPriority: compact.priority,
        detail,
        paceLayout
      });
    }

    windows.sort((a, b) => a.sortMinutes - b.sortMinutes);
    accounts.push({
      key: groupKey || `__acc_${idx}`,
      accountID: groupAccountID,
      displayName,
      plan,
      isActive,
      allDisabled,
      windows,
      detail: summarizeAccountCompactAndDetail(windows, accNoReadings, now)
    });
  }

  const pickManualAccountID = (): string => {
    const activeChoice = choices.find((choice) => {
      if (choice.disabled) return false;
      return accounts.some((acc) => acc.accountID === choice.accountID && acc.isActive);
    });
    if (activeChoice) return activeChoice.accountID;
    return choices.find((choice) => !choice.disabled)?.accountID ?? '';
  };

  const multiAccount = accounts.length > 1;
  const tableMode = canSelectAccount || multiAccount;
  const singleAccount = !tableMode && accounts.length === 1 ? accounts[0] : null;
  const headerPlan = singleAccount ? singleAccount.plan || quotaGroupPlan(providerBudgets) : '';

  const cols = tableMode ? deriveQuotaColumns(accounts) : [];
  const hasAnyStatus = tableMode && accounts.some((acc) => acc.detail.compact !== '');
  const hasWarnOrDanger = tableMode && accounts.some((acc) => acc.detail.tone !== 'normal');
  const statusColHead = hasWarnOrDanger ? '状态' : '重置';
  const showStatusCol = hasAnyStatus || cols.length > 0;

  const tableGridClass =
    cols.length >= 2
      ? showStatusCol
        ? 'grid-cols-[minmax(0,1fr)_68px_68px_58px] gap-1.5 sm:grid-cols-[minmax(120px,1fr)_92px_92px_72px] sm:gap-2.5'
        : 'grid-cols-[minmax(0,1fr)_76px_76px] gap-1.5 sm:grid-cols-[minmax(120px,1fr)_92px_92px] sm:gap-2.5'
      : cols.length === 1
        ? showStatusCol
          ? 'grid-cols-[minmax(0,1fr)_92px_62px] gap-1.5 sm:grid-cols-[minmax(120px,1fr)_116px_72px] sm:gap-2.5'
          : 'grid-cols-[minmax(0,1fr)_104px] gap-1.5 sm:grid-cols-[minmax(120px,1fr)_128px] sm:gap-2.5'
        : 'grid-cols-[minmax(0,1fr)_auto] gap-2';

  const singleRowHasStatus = singleAccount ? singleAccount.windows.some((w) => w.compactText !== '') : false;
  const singleRowGridClass = singleRowHasStatus
    ? 'grid-cols-[44px_minmax(0,1fr)_38px_64px] gap-2 sm:grid-cols-[48px_minmax(0,1fr)_42px_72px] sm:gap-2.5'
    : 'grid-cols-[44px_minmax(0,1fr)_38px] gap-2 sm:grid-cols-[48px_minmax(0,1fr)_42px] sm:gap-2.5';

  const manualPickAvailable = Boolean(pickManualAccountID());

  return (
    <>
      <div className="sheet-handle shrink-0" aria-hidden="true" />

      {/* 顶栏：图标 + 供应商名 + 单账号套餐 + [分段路由] + 图标刷新 + 关闭 */}
      <div className="mb-3 flex w-full min-w-0 shrink-0 items-center gap-2">
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <ProviderIcon provider={provider} colored className="h-4 w-4 shrink-0" title={providerName} />
          <h2 className="min-w-0 truncate text-mc-title font-semibold text-[var(--mc-text-primary)]">
            {providerName}
          </h2>
          {headerPlan && (
            <span className="shrink-0 whitespace-nowrap text-mc-footnote text-[var(--mc-text-tertiary)]">
              · {headerPlan}
            </span>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {canSelectAccount && (
            <div
              role="group"
              aria-label="账号路由模式"
              className={`inline-flex h-mc-control-sm shrink-0 items-center gap-[1px] rounded-mc-pill bg-[var(--mc-overlay-hover)] p-[2px]${
                locked ? ' opacity-55' : ''
              }`}
            >
              <button
                type="button"
                disabled={locked}
                aria-pressed={currentAccountID === ''}
                onClick={() => {
                  if (locked || currentAccountID === '') return;
                  handleSelectAccount('');
                }}
                className={`inline-flex h-mc-control-xs items-center justify-center whitespace-nowrap rounded-mc-pill px-2 text-mc-caption font-medium transition-[background-color,color,box-shadow] duration-mc-snap motion-reduce:transition-none ${
                  currentAccountID === ''
                    ? 'bg-[var(--mc-surface-raised)] text-[var(--mc-text-primary)] ring-1 ring-[var(--mc-hairline)]'
                    : 'text-[var(--mc-text-tertiary)] enabled:hover:text-[var(--mc-text-secondary)]'
                }`}
              >
                自动选号
              </button>
              <button
                type="button"
                disabled={locked || (currentAccountID === '' && !manualPickAvailable)}
                aria-pressed={currentAccountID !== ''}
                onClick={() => {
                  if (locked || currentAccountID !== '') return;
                  const targetID = pickManualAccountID();
                  if (targetID) handleSelectAccount(targetID);
                }}
                className={`inline-flex h-mc-control-xs items-center justify-center whitespace-nowrap rounded-mc-pill px-2 text-mc-caption font-medium transition-[background-color,color,box-shadow] duration-mc-snap motion-reduce:transition-none ${
                  currentAccountID !== ''
                    ? 'bg-[var(--mc-surface-raised)] text-[var(--mc-text-primary)] ring-1 ring-[var(--mc-hairline)]'
                    : 'text-[var(--mc-text-tertiary)] enabled:hover:text-[var(--mc-text-secondary)]'
                }`}
              >
                指定
              </button>
            </div>
          )}
          <button
            type="button"
            onClick={onRefresh}
            disabled={refreshing}
            aria-label="刷新额度"
            title="刷新额度"
            className="inline-flex h-mc-control-sm w-mc-control-sm shrink-0 items-center justify-center rounded-mc-sm text-[var(--mc-text-secondary)] transition-colors duration-mc-fast motion-reduce:transition-none enabled:hover:bg-[var(--mc-overlay-hover)] enabled:hover:text-[var(--mc-text-primary)] disabled:opacity-50"
          >
            <ArrowPathIcon
              className={`h-mc-icon-sm w-mc-icon-sm ${
                refreshing ? 'animate-spin motion-reduce:animate-none text-[var(--mc-accent)]' : ''
              }`}
            />
          </button>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>
      </div>

      <div className="no-scrollbar w-full min-w-0 max-w-full flex-1 min-h-0 overflow-y-auto overflow-x-hidden">
        <div className="space-y-3">
          {/* 预付费余额 */}
          {credits && (
            <div className="flex items-center justify-between gap-2 px-1 text-mc-footnote">
              <span className="shrink-0 text-[var(--mc-text-secondary)]">余额</span>
              <span className="min-w-0 truncate tabular-nums font-medium text-[var(--mc-text-primary)]">
                {credits.amount}
              </span>
            </div>
          )}

          {anyWindows ? (
            <>
              {unavailable && (
                <p className="rounded-mc-md bg-[color-mix(in_srgb,var(--mc-warning)_10%,transparent)] px-3 py-1.5 text-mc-footnote text-[var(--mc-warning)]">
                  部分账号额度不可用
                </p>
              )}

              {/* 单账号模式：省略账号行，每个窗口一行 */}
              {singleAccount && (
                <div className="flex flex-col gap-0.5">
                  {singleAccount.windows.map((win, winIdx) => {
                    const rowKey = `single-${winIdx}-${win.shortLabel}`;
                    const isExpanded = expandedRowKey === rowKey;
                    const resetFallback = win.noBar && !win.compactText && win.resetsAtMs !== null
                      ? formatCompactReset(new Date(win.resetsAtMs).toISOString(), now)
                      : win.compactText;
                    return (
                      <div key={rowKey} className="flex flex-col">
                        <button
                          type="button"
                          aria-expanded={isExpanded}
                          title={win.detail.detail || undefined}
                          onClick={() => onToggleRow(rowKey)}
                          className={`grid w-full min-h-[32px] items-center rounded-mc-sm px-2 text-left transition-colors duration-mc-fast motion-reduce:transition-none hover:bg-[var(--mc-overlay-hover)] ${singleRowGridClass}`}
                        >
                          <span className="truncate text-mc-footnote text-[var(--mc-text-secondary)]">
                            {win.shortLabel}
                          </span>
                          {win.noBar ? (
                            <span className="col-span-2 truncate text-right text-mc-footnote font-medium text-[var(--mc-text-primary)]">
                              {win.noBarText}
                            </span>
                          ) : (
                            <>
                              {renderQuotaBar(win.usedPercent, win.paceLayout)}
                              <span
                                className="text-right text-mc-footnote font-semibold tabular-nums"
                                style={{ color: usagePercentColor(win.usedPercent) }}
                              >
                                {formatQuotaPercent(win.usedPercent)}%
                              </span>
                            </>
                          )}
                          {singleRowHasStatus && (
                            <span
                              className={`truncate text-right text-mc-caption tabular-nums ${statusToneClass(
                                win.statusTone
                              )}`}
                            >
                              {resetFallback}
                            </span>
                          )}
                        </button>
                        {renderInlineDetailBox(
                          isExpanded,
                          win.detail.left,
                          win.detail.right,
                          win.detail.detail,
                          win.detail.tone
                        )}
                      </div>
                    );
                  })}
                </div>
              )}

              {/* 多账号 / 可选账号模式：表头 + 每账号紧凑单行 */}
              {tableMode && (
                <div
                  role={canSelectAccount ? 'radiogroup' : undefined}
                  aria-label={canSelectAccount ? (role === 'bees' ? '子代理账号' : '账号') : undefined}
                  className="flex flex-col gap-0.5"
                >
                  {cols.length > 0 && (
                    <div className="flex h-5 items-center px-2 text-mc-caption font-medium text-[var(--mc-text-tertiary)] select-none">
                      {canSelectAccount && <span className="w-6 shrink-0" aria-hidden="true" />}
                      <div className={`grid min-w-0 flex-1 items-center ${tableGridClass}`}>
                        <span className="truncate">账号</span>
                        {cols.map((col) => (
                          <span key={col.shortLabel} className="truncate">
                            {col.shortLabel}
                          </span>
                        ))}
                        {showStatusCol && <span className="text-right">{statusColHead}</span>}
                      </div>
                    </div>
                  )}

                  {accounts.map((acc) => {
                    const isRadio = canSelectAccount && acc.accountID !== '';
                    const isSelected = isRadio && currentAccountID === acc.accountID;
                    const choice = isRadio ? choiceMap.get(acc.accountID) : undefined;
                    const optionDisabled =
                      isRadio && (locked || (choice ? choice.disabled : acc.allDisabled));
                    const rowDimmed = isRadio ? optionDisabled : acc.allDisabled;
                    const isExpanded = expandedRowKey === acc.key;
                    const { cells, extra } = allocateQuotaColumns(acc.windows, cols);

                    const dotColorClass = acc.isActive
                      ? 'bg-[var(--mc-accent)]'
                      : acc.detail.tone === 'danger'
                        ? 'bg-[var(--mc-danger)]'
                        : 'bg-[var(--mc-overlay-pressed)]';

                    return (
                      <div key={acc.key} className="flex flex-col">
                        <div
                          className={`flex min-h-[34px] items-center rounded-mc-sm border px-2 transition-colors duration-mc-fast motion-reduce:transition-none ${
                            isSelected
                              ? 'border-[color-mix(in_srgb,var(--mc-accent)_32%,transparent)] bg-[color-mix(in_srgb,var(--mc-accent-subtle)_75%,transparent)]'
                              : 'border-transparent hover:bg-[var(--mc-overlay-hover)]'
                          }${rowDimmed ? ' opacity-55' : ''}`}
                        >
                          {isRadio && (
                            <button
                              type="button"
                              role="radio"
                              aria-checked={isSelected}
                              disabled={optionDisabled}
                              aria-label={`选择账号 ${acc.displayName}`}
                              onClick={() => handleSelectAccount(acc.accountID)}
                              className="flex h-7 w-6 shrink-0 items-center justify-start bg-transparent disabled:cursor-not-allowed"
                            >
                              <span
                                aria-hidden="true"
                                className={`relative inline-flex h-3.5 w-3.5 items-center justify-center rounded-full border-[1.5px] transition-colors duration-mc-fast motion-reduce:transition-none ${
                                  optionDisabled && !isSelected
                                    ? 'border-[var(--mc-hairline)] bg-[var(--mc-overlay-hover)]'
                                    : isSelected
                                      ? 'border-[var(--mc-accent)] bg-[var(--mc-accent)]'
                                      : 'border-[var(--mc-border-strong)]'
                                }`}
                              >
                                {isSelected && (
                                  <span className="h-1.5 w-1.5 rounded-full bg-[var(--mc-on-accent)]" />
                                )}
                              </span>
                            </button>
                          )}

                          <button
                            type="button"
                            aria-expanded={isExpanded}
                            title={acc.detail.detailFull || undefined}
                            onClick={() => onToggleRow(acc.key)}
                            className={`grid min-w-0 flex-1 items-center py-1 text-left ${tableGridClass}`}
                          >
                            <span className="flex min-w-0 items-center gap-1.5">
                              {!isRadio && (
                                <span
                                  aria-hidden="true"
                                  className={`h-1.5 w-1.5 shrink-0 rounded-full ${dotColorClass}`}
                                />
                              )}
                              <span className="min-w-0 truncate text-mc-footnote text-[var(--mc-text-primary)]">
                                {acc.displayName}
                              </span>
                              {isRadio && currentAccountID === '' && acc.isActive && (
                                <span className="inline-flex h-4 shrink-0 items-center gap-1 whitespace-nowrap rounded-mc-pill bg-[var(--mc-accent-subtle)] px-1.5 text-[10px] font-medium text-[var(--mc-accent)]">
                                  <span aria-hidden="true" className="h-1 w-1 rounded-full bg-current" />
                                  在用
                                </span>
                              )}
                              {!isRadio && multiAccount && acc.plan && (
                                <span className="shrink-0 whitespace-nowrap text-mc-caption text-[var(--mc-text-tertiary)]">
                                  {acc.plan}
                                </span>
                              )}
                            </span>

                            {cells.map((cell, ci) => {
                              const cellWin = cell.win;
                              if (!cellWin) {
                                return (
                                  <span
                                    key={`empty-${ci}`}
                                    className="text-center text-mc-caption text-[var(--mc-text-tertiary)]"
                                  >
                                    —
                                  </span>
                                );
                              }
                              return (
                                <span key={`win-${ci}-${cellWin.shortLabel}`} className="flex min-w-0 items-center gap-1">
                                  {cell.inlineTag && (
                                    <span className="shrink-0 text-mc-caption text-[var(--mc-text-tertiary)]">
                                      {cellWin.shortLabel}
                                    </span>
                                  )}
                                  {renderQuotaBar(cellWin.usedPercent, cellWin.paceLayout)}
                                  <span
                                    className="w-[34px] shrink-0 text-right text-mc-footnote font-semibold tabular-nums"
                                    style={{ color: usagePercentColor(cellWin.usedPercent) }}
                                  >
                                    {formatQuotaPercent(cellWin.usedPercent)}%
                                  </span>
                                </span>
                              );
                            })}

                            {showStatusCol && (
                              <span
                                className={`truncate text-right text-mc-caption tabular-nums ${statusToneClass(
                                  acc.detail.tone
                                )}`}
                              >
                                {acc.detail.compact}
                              </span>
                            )}
                          </button>
                        </div>

                        {extra.map((ex, exIdx) => (
                          <div
                            key={`${acc.key}-ex-${exIdx}`}
                            className="flex min-h-[24px] items-center gap-2 pl-7 pr-2 text-mc-caption"
                          >
                            {ex.shortLabel && (
                              <span className="shrink-0 text-[var(--mc-text-tertiary)]">{ex.shortLabel}</span>
                            )}
                            {ex.noBar ? (
                              <span className="ml-auto truncate font-medium text-[var(--mc-text-secondary)]">
                                {ex.noBarText}
                              </span>
                            ) : (
                              <>
                                {renderQuotaBar(ex.usedPercent, ex.paceLayout)}
                                <span
                                  className="w-[34px] shrink-0 text-right font-semibold tabular-nums"
                                  style={{ color: usagePercentColor(ex.usedPercent) }}
                                >
                                  {formatQuotaPercent(ex.usedPercent)}%
                                </span>
                              </>
                            )}
                          </div>
                        ))}

                        {renderInlineDetailBox(
                          isExpanded,
                          acc.detail.detailLeft,
                          acc.detail.detailRight,
                          acc.detail.detailFull,
                          acc.detail.detailTone
                        )}
                      </div>
                    );
                  })}
                </div>
              )}

              {/* 无有效读数的次要项（如 Image generation: Unknown） */}
              {groupNoReadings.length > 0 && (
                <div className="flex flex-col gap-1 px-1 pt-0.5">
                  <button
                    type="button"
                    aria-expanded={noReadingsOpen}
                    onClick={onToggleNoReadings}
                    className="inline-flex items-center gap-1 self-start rounded-mc-xs px-1.5 py-0.5 text-mc-caption text-[var(--mc-text-tertiary)] transition-colors duration-mc-fast motion-reduce:transition-none hover:bg-[var(--mc-overlay-hover)] hover:text-[var(--mc-text-secondary)]"
                  >
                    <ChevronRightIcon
                      className={`h-3 w-3 shrink-0 transition-transform duration-mc-snap motion-reduce:transition-none ${
                        noReadingsOpen ? 'rotate-90' : ''
                      }`}
                    />
                    <span>{groupNoReadings.length} 项无读数</span>
                  </button>
                  <div
                    aria-hidden={!noReadingsOpen}
                    className={`grid transition-[grid-template-rows,opacity] duration-mc-snap ease-out motion-reduce:transition-none ${
                      noReadingsOpen
                        ? 'grid-rows-[1fr] opacity-100'
                        : 'grid-rows-[0fr] opacity-0 pointer-events-none'
                    }`}
                  >
                    <div className="overflow-hidden">
                      <div className="flex flex-col gap-0.5 rounded-mc-sm bg-[var(--mc-overlay-hover)] px-2 py-1">
                        {groupNoReadings.map((item, idx) => {
                          const leftText =
                            multiAccount && item.account
                              ? item.label
                                ? `${item.account} · ${item.label}`
                                : item.account
                              : item.label;
                          return (
                            <div
                              key={`${item.account}-${item.label}-${idx}`}
                              className="flex min-h-[20px] items-center justify-between gap-2 text-mc-caption text-[var(--mc-text-tertiary)]"
                            >
                              <span className="min-w-0 truncate">{leftText}</span>
                              <span className="shrink-0">{item.value}</span>
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  </div>
                </div>
              )}
            </>
          ) : loading ? (
            <div className="flex items-center gap-1.5 text-mc-footnote text-[var(--mc-text-secondary)]">
              <span className="h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-[var(--mc-text-secondary)] border-t-transparent motion-reduce:animate-none" />
              <span>正在加载额度…</span>
            </div>
          ) : unavailable ? (
            <p className="text-mc-footnote text-[var(--mc-warning)]">额度不可用</p>
          ) : (
            <p className="text-mc-footnote text-[var(--mc-text-tertiary)]">暂无额度数据</p>
          )}
        </div>
      </div>
    </>
  );
};

export const ComposerQuotaSheet: React.FC<ComposerQuotaSheetProps> = ({
  isOpen,
  onClose,
  provider,
  providers,
  role = null,
  budgets,
  activeAccount = null,
  hasThread = false,
  selectedAccountID = '',
  isRunning = false,
  hasMessages = false,
  onSelectAccount,
  refreshing = false,
  onRefresh,
  now
}) => {
  const [expandedRowKey, setExpandedRowKey] = useState<string | null>(null);
  const [noReadingsOpen, setNoReadingsOpen] = useState(false);

  useEffect(() => {
    setExpandedRowKey(null);
    setNoReadingsOpen(false);
  }, [isOpen, provider]);

  if (!isOpen || !provider) return null;

  const providerBudgets = budgets.filter(
    (budget) => budget.provider === provider && (!role || budget.role === role)
  );
  const providerName = providerBudgets[0]?.providerName || providerDisplayName(providers, provider);

  const handleToggleRow = (rowKey: string) => {
    setExpandedRowKey((prev) => (prev === rowKey ? null : rowKey));
  };

  return (
    <SheetFrame
      onRequestClose={onClose}
      size="bare"
      cardClassName="settings-sheet flex flex-col max-h-[calc(var(--app-height,100dvh)*0.92)] outline-none w-full max-w-full min-w-0 overflow-x-hidden"
      ariaLabel={`${providerName} 额度`}
    >
      <ComposerQuotaSheetContent
        onClose={onClose}
        provider={provider}
        providers={providers}
        role={role}
        budgets={budgets}
        activeAccount={activeAccount}
        hasThread={hasThread}
        selectedAccountID={selectedAccountID}
        isRunning={isRunning}
        hasMessages={hasMessages}
        onSelectAccount={onSelectAccount}
        refreshing={refreshing}
        onRefresh={onRefresh}
        now={now}
        expandedRowKey={expandedRowKey}
        onToggleRow={handleToggleRow}
        noReadingsOpen={noReadingsOpen}
        onToggleNoReadings={() => setNoReadingsOpen((prev) => !prev)}
      />
    </SheetFrame>
  );
};

export default ComposerQuotaSheet;
