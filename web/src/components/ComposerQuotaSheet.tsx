// 移动端输入框额度详情底部弹层组件（点击额度条中某个 provider 组打开）

import React, { useEffect, useState } from 'react';
import type { RemoteQuotaBudget, RemoteQuotaWindow } from '../types/quota';
import { periodMinutes, quotaBarLayout, quotaElapsedRatio, quotaPaceKnown } from '../utils/quotaPace';
import { ProviderIcon } from './ProviderIcon';
import { ArrowPathIcon, XMarkIcon } from './Icons';

export interface ComposerQuotaSheetProps {
  isOpen: boolean;
  onClose: () => void;
  /** 当前查看的 provider；为空时弹层关闭 */
  provider: string | null;
  /** 该 provider 的全部 budget（含 loading / attention 等不可渲染状态） */
  budgets: RemoteQuotaBudget[];
  /** 额度条当前选中的账号，命中时组标题追加「 · 在用」 */
  activeAccount?: string | null;
  /** 刷新请求进行中 */
  refreshing?: boolean;
  onRefresh: () => void;
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

/** 移植桌面 formatQuotaResetCountdown 的文案：'2 小时 30 分后重置' / '45 分后重置' / '周一 09:00 重置'。 */
export function formatQuotaResetCountdown(resetsAt: string | null, now: number = Date.now()): string {
  if (!resetsAt) return '';
  const target = new Date(resetsAt);
  const targetMs = target.getTime();
  if (!Number.isFinite(targetMs)) return '';
  if (targetMs <= now) return '即将重置';

  const diff = targetMs - now;
  if (diff < 24 * 60 * 60 * 1000) {
    const totalSeconds = Math.floor(diff / 1000);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    if (hours > 0 && minutes > 0) return `${hours} 小时 ${minutes} 分后重置`;
    if (hours > 0) return `${hours} 小时后重置`;
    return `${Math.max(1, minutes)} 分后重置`;
  }
  if (diff < 7 * 24 * 60 * 60 * 1000) {
    const weekdays = ['周日', '周一', '周二', '周三', '周四', '周五', '周六'];
    return `${weekdays[target.getDay()]} ${pad2(target.getHours())}:${pad2(target.getMinutes())} 重置`;
  }
  return `${target.getMonth() + 1}月${target.getDate()}日 重置`;
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

/** 账号组里所有窗口的倒计时文案：全部一致时返回该文案，否则返回空串（改用行内展示）。 */
export function quotaGroupCountdown(budgets: RemoteQuotaBudget[], now: number = Date.now()): string {
  const texts: string[] = [];
  for (const budget of budgets) {
    for (const quotaWindow of budget.windows) {
      const text = formatQuotaResetCountdown(quotaWindow.resetsAt, now);
      if (text) texts.push(text);
    }
  }
  if (texts.length === 0) return '';
  return texts.every((text) => text === texts[0]) ? texts[0] : '';
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

/**
 * 账号组标题：账号 · 套餐 · 读取于 X · 在用/已停止。
 * 与桌面端 composer_usage_panel.go 的 renderComposerQuotaSection 同序：
 * 先套餐、再数据时效，最后才是「在用」或「已停止」（互斥）。
 */
export function accountGroupTitle(
  account: string,
  budgets: RemoteQuotaBudget[],
  isActive: boolean,
  now: number = Date.now()
): string {
  let title = account;
  const plan = quotaGroupPlan(budgets);
  if (plan) title += ` · ${plan}`;
  const readAge = formatQuotaReadAge(quotaGroupReadAt(budgets), now);
  if (readAge) title += ` · 读取于 ${readAge}`;
  if (isActive) title += ' · 在用';
  else if (quotaGroupDisabled(budgets)) title += ' · 已停止';
  return title;
}

export const ComposerQuotaSheet: React.FC<ComposerQuotaSheetProps> = ({
  isOpen,
  onClose,
  provider,
  budgets,
  activeAccount = null,
  refreshing = false,
  onRefresh
}) => {
  // 所有窗口共用同一个「已用 / 剩余」显示模式
  const [showRemaining, setShowRemaining] = useState(false);

  useEffect(() => {
    setShowRemaining(false);
  }, [isOpen, provider]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isOpen, onClose]);

  if (!isOpen || !provider) return null;

  const providerBudgets = budgets.filter((budget) => budget.provider === provider);
  const providerName = providerBudgets[0]?.providerName || provider;
  const credits = providerBudgets.find((budget) => budget.amount !== null) ?? null;
  const anyWindows = providerBudgets.some((budget) => budget.windows.length > 0);
  const loading = providerBudgets.some((budget) => budget.state === 'loading');
  const unavailable = providerBudgets.some((budget) => budget.state === 'attention' || budget.state === 'unknown');

  // 按账号分组，保持接口返回顺序；无账号的组不渲染标题
  const accountOrder: string[] = [];
  const accountGroups = new Map<string, RemoteQuotaBudget[]>();
  for (const budget of providerBudgets) {
    const account = (budget.account ?? '').trim();
    const existing = accountGroups.get(account);
    if (existing) {
      existing.push(budget);
      continue;
    }
    accountGroups.set(account, [budget]);
    accountOrder.push(account);
  }

  const now = Date.now();
  const plan = quotaGroupPlan(providerBudgets);
  // 没有任何账号组标题时，重置倒计时挪到供应商头部右侧显示一次
  const providerCountdown = accountOrder.some((account) => account !== '')
    ? ''
    : quotaGroupCountdown(providerBudgets, now);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-x-hidden"
        role="dialog"
        aria-modal="true"
        aria-label={`${providerName} 额度`}
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        {/* 标题栏：彩色图标 + 供应商名 + 套餐 + 刷新 */}
        <div className="flex shrink-0 items-center justify-between gap-3 min-w-0 w-full mb-3">
          <div className="flex min-w-0 flex-1 items-center gap-1.5">
            <ProviderIcon provider={provider} colored className="w-[18px] h-[18px] shrink-0" title={providerName} />
            <h2 className="min-w-0 truncate text-mc-prose font-semibold text-[var(--foreground)]">
              {providerName}
              {plan && <span className="ml-1 text-xs font-normal text-[var(--foreground-muted)]">· {plan}</span>}
            </h2>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
            {providerCountdown && (
              <span className="shrink-0 whitespace-nowrap text-xs text-[var(--foreground-muted)]">{providerCountdown}</span>
            )}
            <button
              type="button"
              onClick={onRefresh}
              disabled={refreshing}
              aria-label="刷新额度"
              title="刷新额度"
              className="inline-flex min-h-[32px] items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--card)] px-3 text-xs font-medium text-[var(--foreground)] disabled:opacity-50"
            >
              <ArrowPathIcon className={`h-3.5 w-3.5 ${refreshing ? 'animate-spin text-[var(--primary)]' : ''}`} />
              <span>刷新</span>
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

        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden no-scrollbar min-w-0 max-w-full w-full">
          <div className="space-y-4">
            {/* 预付费余额 */}
            {credits && (
              <div className="flex items-center justify-between gap-2 text-xs">
                <span className="shrink-0 text-[var(--foreground-muted)]">余额</span>
                <span className="min-w-0 truncate tabular-nums font-medium text-[var(--foreground)]">{credits.amount}</span>
              </div>
            )}

            {anyWindows ? (
              <>
                <p className="text-mc-body font-semibold text-[var(--foreground)]">额度</p>
                {/* 同一 provider 既有可展示窗口又有不可用卡片时也要提示：不能只显示可读的那部分。 */}
                {unavailable && (
                  <p className="text-xs text-[var(--warning-foreground)]">部分账号额度不可用</p>
                )}
                {accountOrder.map((account) => {
                  const groupBudgets = accountGroups.get(account) ?? [];
                  const isActive = account !== '' && account === activeAccount;
                  const groupCountdown = account === '' ? '' : quotaGroupCountdown(groupBudgets, now);
                  return (
                    <div key={account || '__no_account__'} className="space-y-2">
                      {account !== '' && (
                        <div className="flex min-w-0 items-center justify-between gap-2">
                          <p className="min-w-0 truncate text-mc-caption text-[var(--foreground-muted)]">
                            {accountGroupTitle(account, groupBudgets, isActive, now)}
                          </p>
                          {groupCountdown && (
                            <span className="shrink-0 whitespace-nowrap text-xs text-[var(--foreground-muted)]">
                              {groupCountdown}
                            </span>
                          )}
                        </div>
                      )}
                      {groupBudgets.map((budget, budgetIdx) => {
                        // 整组都已停用时置灰（单窗口带 disabled 也置灰），与托盘里「已停止」账号同口径。
                        const budgetDisabled = quotaGroupDisabled([budget]);
                        return budget.windows.map((quotaWindow, winIdx) => {
                          const content = quotaWindowContent(quotaWindow);
                          const used = content.kind === 'percent' ? content.usedPercent : 0;
                          const remaining = 100 - used;
                          const color = usageColor(used);
                          const countdown = formatQuotaResetCountdown(quotaWindow.resetsAt, now);
                          // 组标题已展示同一个倒计时时不再重复到行内
                          const inlineCountdown = countdown && countdown !== groupCountdown ? countdown : '';
                          const percentText = showRemaining
                            ? `剩余 ${formatQuotaPercent(remaining)}%`
                            : `已用 ${formatQuotaPercent(used)}%`;
                          const disabled = budgetDisabled || quotaWindow.disabled === true;
                          // 有时刻与周期可用时按「时间已过比例」画出重置进度竖线，否则不画。
                          const paceElapsed =
                            content.kind === 'percent'
                              ? quotaElapsedRatio(quotaWindow.resetsAt, periodMinutes(quotaWindow.period))
                              : null;
                          const paceLayout =
                            paceElapsed === null
                              ? null
                              : quotaBarLayout(used, paceElapsed, quotaPaceKnown(used, paceElapsed));
                          return (
                            <div
                              key={`${budgetIdx}-${winIdx}`}
                              className={`flex min-w-0 items-center gap-3${disabled ? ' opacity-50' : ''}`}
                            >
                              <span className="min-w-0 flex-1 truncate text-mc-footnote text-[var(--foreground-muted)]" title={quotaWindow.title}>
                                {quotaWindow.title}
                              </span>
                              {inlineCountdown && (
                                <span className="shrink-0 whitespace-nowrap text-mc-caption text-[var(--foreground-muted)]">
                                  {inlineCountdown}
                                </span>
                              )}
                              {content.kind === 'percent' ? (
                                <>
                                  <div aria-hidden="true" className="relative h-1 w-24 shrink-0 sm:w-[120px]">
                                    <div className="absolute inset-0 rounded-full bg-[var(--border)]" />
                                    <div
                                      className="absolute left-0 top-0 h-1 rounded-full"
                                      style={{ width: `${used}%`, backgroundColor: color }}
                                    />
                                    {paceLayout !== null && (
                                      <div
                                        data-testid="quota-pace-marker"
                                        title={`时间已过 ${Math.round(paceLayout.markerPercent)}%`}
                                        className="absolute top-1/2 h-[10px] w-[1.5px] -translate-y-1/2 bg-[var(--foreground)]"
                                        style={{
                                          left: `calc(min(max(${paceLayout.markerPercent}% - 0.75px, 0px), 100% - 1.5px))`
                                        }}
                                      />
                                    )}
                                  </div>
                                  <button
                                    type="button"
                                    onClick={() => setShowRemaining((prev) => !prev)}
                                    title="点击切换已用/剩余"
                                    aria-label={`${quotaWindow.title} ${percentText}，点击切换已用/剩余`}
                                    className="w-[52px] shrink-0 whitespace-nowrap rounded text-right text-mc-body font-medium tabular-nums hover:underline"
                                    style={{ color: usagePercentColor(used) }}
                                  >
                                    {percentText}
                                  </button>
                                </>
                              ) : (
                                <span
                                  className="min-w-0 max-w-[55%] shrink-0 truncate whitespace-nowrap text-mc-footnote font-medium text-[var(--foreground)]"
                                  title={content.text}
                                >
                                  {content.text}
                                </span>
                              )}
                            </div>
                          );
                        });
                      })}
                    </div>
                  );
                })}
              </>
            ) : loading ? (
              <div className="flex items-center gap-1.5 text-xs text-[var(--foreground-muted)]">
                <span className="h-3 w-3 shrink-0 animate-spin rounded-full border-2 border-[var(--foreground-muted)] border-t-transparent" />
                <span>正在加载额度…</span>
              </div>
            ) : unavailable ? (
              <p className="text-xs text-[var(--warning-foreground)]">额度不可用</p>
            ) : (
              <p className="text-xs text-[var(--foreground-muted)]">暂无额度数据</p>
            )}
          </div>
        </div>
      </section>
    </div>
  );
};

export default ComposerQuotaSheet;
