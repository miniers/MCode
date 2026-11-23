import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import type { ComposerQuotaScope, RemoteQuotaBudget } from '../types/quota';
import {
  FALLBACK_SORT_MINUTES,
  fallbackWindowLabel,
  isAuxiliaryWindow,
  periodMinutes,
  periodShortLabel
} from '../utils/quotaPace';
import { ProviderIcon } from './ProviderIcon';
import { ComposerQuotaSheet, formatQuotaPercent, quotaWindowContent, usageColor, usagePercentColor } from './ComposerQuotaSheet';

export interface ComposerQuotaBarProps {
  scope: ComposerQuotaScope;
  isRunning?: boolean;
  hasMessages?: boolean;
  /** 线程摘要里的主代理显式 agy 账号 ID */
  account?: string | null;
  /** 线程摘要里的子代理显式 agy 账号 ID */
  swarmBeesAccount?: string | null;
  /** 切换账号失败时的错误提示回调 */
  onAccountError?: (message: string | null) => void;
  /** 收到容器 relative、贴下边缘悬浮时不占文档流（默认仍留在文档流里） */
  floating?: boolean;
}

const QUOTA_STRIP_BASE_CLASS = 'flex items-center overflow-x-auto no-scrollbar whitespace-nowrap px-1 text-mc-caption text-[var(--foreground-muted)]';
/** 文档流内：保持原有 mt-1 间距 */
export const QUOTA_STRIP_CLASS = `mt-1 ${QUOTA_STRIP_BASE_CLASS}`;
/** 悬浮：脱离文档流，以 top-full 贴在 relative 父容器的下边缘 */
export const QUOTA_STRIP_FLOATING_CLASS = `composer-quota-floating absolute left-0 right-0 top-full pt-0.5 z-10 ${QUOTA_STRIP_BASE_CLASS}`;

export function quotaStripClass(floating: boolean): string {
  return floating ? QUOTA_STRIP_FLOATING_CLASS : QUOTA_STRIP_CLASS;
}

/**
 * 额度条上的一个读数：短标签 + 读数。
 * 读数要么是已用百分比，要么是一段文字（纯文本窗口 display、余额「剩余 x unit」、「不限量」）。
 */
export interface QuotaReadout {
  label: string;
  /** 已用百分比；纯文本 / 余额 / 不限量窗口没有百分比，为 null */
  usedPercent: number | null;
  /** usedPercent 为 null 时额度条要显示的文字 */
  text?: string;
}

function isRenderableBudget(budget: RemoteQuotaBudget): boolean {
  if (budget.state === 'used' || budget.state === 'stale') return budget.windows.length > 0;
  // unknown 也要可见：额度来源不可确认时额度条应显式显示「不可用」，而不是当作没有额度。
  if (budget.state === 'unknown') return true;
  if (budget.state === 'credits') return budget.amount !== null || budget.windows.length > 0;
  // attention 和 unknown 一样要显式可见：后端标记凭据耗尽或账号非 active 时，额度条应显示「不可用」而不是整条消失。
  if (budget.state === 'attention') return true;
  return false;
}

export { fallbackWindowLabel, isAuxiliaryWindow, periodShortLabel };

/** 同周期窗口排序用：0 = 全模型/账户窗口，1 = 按模型限定的窗口。 */
function windowScopeRank(title: string): number {
  const lower = title.toLowerCase();
  if (lower.includes('all models')) return 0;
  const idx = title.indexOf(' · ');
  if (idx >= 0 && title.slice(idx + 3).trim() !== '') return 1;
  return 0;
}

/**
 * 把一条 budget 折算成额度条上的若干读数：先剔除附加窗口，再按周期从短到长排序
 *（同周期优先 all models / 不含「 · 」的窗口），最后按标签去重。
 * 纯文本、余额、不限量窗口不参与百分比排序（排到最后），但必须保留：它们不能被当成
 *「没有额度」而消失。全部被过滤时兜底取前两条窗口，标签为「额度」「额度2」。
 */
export function stripReadouts(budget: RemoteQuotaBudget): QuotaReadout[] {
  const candidates: Array<QuotaReadout & { minutes: number; scope: number; order: number }> = [];
  budget.windows.forEach((quotaWindow, order) => {
    if (isAuxiliaryWindow(quotaWindow)) return;
    const content = quotaWindowContent(quotaWindow);
    const periodLabel = periodShortLabel(quotaWindow.period);
    const minutes = periodMinutes(quotaWindow.period);
    if (content.kind === 'percent') {
      if (!periodLabel || minutes === null) return;
      candidates.push({
        label: periodLabel,
        usedPercent: content.usedPercent,
        minutes,
        scope: windowScopeRank(quotaWindow.title),
        order
      });
      return;
    }
    candidates.push({
      label: periodLabel ?? fallbackWindowLabel(quotaWindow),
      usedPercent: null,
      text: content.text,
      minutes: minutes ?? FALLBACK_SORT_MINUTES,
      scope: windowScopeRank(quotaWindow.title),
      order
    });
  });

  candidates.sort((a, b) => {
    if (a.minutes !== b.minutes) return a.minutes - b.minutes;
    if (a.scope !== b.scope) return a.scope - b.scope;
    return a.order - b.order;
  });

  const seen = new Set<string>();
  const readouts: QuotaReadout[] = [];
  for (const candidate of candidates) {
    const readout: QuotaReadout =
      candidate.usedPercent === null
        ? { label: candidate.label, usedPercent: null, text: candidate.text ?? '' }
        : { label: candidate.label, usedPercent: candidate.usedPercent };
    // 去重键带上读数类型：文字窗口和百分比窗口同标签时两条都要留。
    const key = `${readout.usedPercent === null ? `t:${readout.text}` : 'p'}:${readout.label}`;
    if (seen.has(key)) continue;
    seen.add(key);
    readouts.push(readout);
  }
  if (readouts.length > 0) return readouts;

  return budget.windows.slice(0, 2).map((quotaWindow, index) => {
    const content = quotaWindowContent(quotaWindow);
    const label = index === 0 ? '额度' : `额度${index + 1}`;
    return content.kind === 'percent'
      ? { label, usedPercent: content.usedPercent }
      : { label, usedPercent: null, text: content.text };
  });
}

export interface ProviderQuotaGroup {
  /** 分组键为 `${role}:${provider}`：主代理与子代理各占一组，也用它标识打开的是哪个弹层 */
  key: string;
  role: 'lead' | 'bees';
  provider: string;
  /** 该组展示的账号；空表示宿主没细分账号。同组其他账号在额度弹层里列出 */
  account?: string;
  providerName: string;
  /** 额度条实际展示的那一个 budget */
  budget: RemoteQuotaBudget;
}

/** 切换线程/项目时用的空额度列表：没有该来源的数据就不展示旧来源的数据。 */
const EMPTY_BUDGETS: RemoteQuotaBudget[] = [];

function budgetRole(budget: RemoteQuotaBudget): 'lead' | 'bees' {
  return budget.role === 'bees' ? 'bees' : 'lead';
}

/**
 * 按 `${role}:${provider}` 分组（保持首次出现顺序），每组展示 active 优先、其次有窗口的那一个 budget。
 * lead 组排在 bees 组之前；同一角色内在用的账号排到前面，其余保持原有顺序。
 */
export function groupByProvider(renderable: RemoteQuotaBudget[]): ProviderQuotaGroup[] {
  const order: string[] = [];
  const grouped = new Map<string, RemoteQuotaBudget[]>();
  for (const budget of renderable) {
    const role = budgetRole(budget);
    const key = `${role}:${budget.provider}`;
    const existing = grouped.get(key);
    if (existing) {
      existing.push(budget);
      continue;
    }
    grouped.set(key, [budget]);
    order.push(key);
  }
  const groups = order.map((key) => {
    const providerBudgets = grouped.get(key) ?? [];
    const selected = providerBudgets.find((budget) => budget.active === true)
      ?? providerBudgets.find((budget) => budget.state !== 'unknown')
      ?? providerBudgets[0];
    return {
      key,
      role: budgetRole(selected),
      provider: selected.provider,
      account: selected.account ?? undefined,
      providerName: selected.providerName,
      budget: selected
    };
  });
  // lead 组在前、bees 组在后；同一角色内在用的账号排到前面，其余保持原有顺序
  const rank = (group: ProviderQuotaGroup): number =>
    (group.role === 'bees' ? 2 : 0) + (group.budget.active === true ? 0 : 1);
  return [...groups].sort((a, b) => rank(a) - rank(b));
}

export const ComposerQuotaBar: React.FC<ComposerQuotaBarProps> = ({
  scope,
  isRunning = false,
  hasMessages = false,
  account = null,
  swarmBeesAccount = null,
  onAccountError,
  floating = false
}) => {
  const { connectionState, capabilities, getComposerQuota, updateThread } = useRemoteStatic();
  const threadID = scope.threadID;
  const projectID = scope.projectID;
  // capabilities 取失败会被置为 null，此时按宽松口径视为支持 composer.quota，避免额度条被永久隐藏。
  const quotaEnabled = connectionState === 'connected'
    && (capabilities ? capabilities.methods.includes('composer.quota') : true)
    && Boolean(threadID || projectID);
  // 额度结果按来源（线程/项目）隔离：切换后旧结果即使晚到也不能覆盖或短暂露出。
  const scopeKey = `${threadID ?? ''}|${projectID ?? ''}`;
  const [quotaData, setQuotaData] = useState<{ key: string; budgets: RemoteQuotaBudget[] }>({ key: '', budgets: [] });
  const budgets = quotaData.key === scopeKey ? quotaData.budgets : EMPTY_BUDGETS;
  const [sheetGroupKey, setSheetGroupKey] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const requestIDRef = useRef(0);
  const wasRunningRef = useRef(isRunning);
  const retryTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    return () => {
      if (retryTimerRef.current) {
        clearTimeout(retryTimerRef.current);
        retryTimerRef.current = null;
      }
    };
  }, []);

  useEffect(() => {
    setSheetGroupKey(null);
  }, [threadID, projectID]);

  const loadQuota = useCallback(() => {
    if (retryTimerRef.current) {
      clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    const requestID = ++requestIDRef.current;
    if (!quotaEnabled) return;
    getComposerQuota({ threadID, projectID })
      .then((result) => {
        if (requestIDRef.current === requestID) {
          setQuotaData({ key: scopeKey, budgets: result.budgets });
          if (result.budgets.some((b) => b.state === 'loading')) {
            retryTimerRef.current = setTimeout(loadQuota, 2000);
          }
        }
      })
      .catch(() => {
        // 读取失败保留上次数据，额度条不展示错误
      })
      .finally(() => {
        if (requestIDRef.current === requestID) setRefreshing(false);
      });
  }, [getComposerQuota, projectID, quotaEnabled, scopeKey, threadID]);

  // 弹层内的刷新：走同一份 loadQuota，额外记录请求中状态用于 spinner
  const handleRefresh = useCallback(() => {
    setRefreshing(true);
    loadQuota();
  }, [loadQuota]);

  const closeSheet = useCallback(() => setSheetGroupKey(null), []);

  useEffect(() => {
    loadQuota();
  }, [loadQuota]);

  useEffect(() => {
    if (wasRunningRef.current && !isRunning) loadQuota();
    wasRunningRef.current = isRunning;
  }, [isRunning, loadQuota]);

  useEffect(() => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') loadQuota();
    };
    document.addEventListener('visibilitychange', handleVisibilityChange);
    return () => document.removeEventListener('visibilitychange', handleVisibilityChange);
  }, [loadQuota]);

  const groups = useMemo(() => groupByProvider(budgets.filter(isRenderableBudget)), [budgets]);
  const sheetGroup = sheetGroupKey ? groups.find((group) => group.key === sheetGroupKey) ?? null : null;
  const sheetProvider = sheetGroup?.provider ?? null;
  const sheetRole = sheetGroup?.role ?? null;
  // 弹层里按 `${role}:${provider}` 展示该组全部账号的额度
  const sheetBudgets = useMemo(
    () =>
      sheetGroupKey
        ? budgets.filter((budget) => `${budgetRole(budget)}:${budget.provider}` === sheetGroupKey)
        : [],
    [budgets, sheetGroupKey]
  );
  // 只有后端明确标记 active 的 budget 才能标「在用」：Magpie 多账号时默认展示的第一张卡不能当在用。
  const activeAccount = useMemo(
    () => sheetBudgets.find((budget) => budget.active === true)?.account ?? null,
    [sheetBudgets]
  );
  const selectedAccountID = sheetRole === 'bees' ? (swarmBeesAccount ?? '') : sheetRole === 'lead' ? (account ?? '') : '';

  const handleSelectAccount = useCallback(
    async (targetAccountID: string) => {
      const targetThreadID = scope.threadID;
      const targetRole = sheetRole;
      setSheetGroupKey(null);
      if (!targetThreadID || !targetRole) return;
      onAccountError?.(null);
      try {
        const changes =
          targetRole === 'bees'
            ? { swarmBeesAccount: targetAccountID }
            : { account: targetAccountID };
        await updateThread(targetThreadID, changes);
        loadQuota();
      } catch (err) {
        const detail = err instanceof Error ? err.message : String(err);
        onAccountError?.(detail ? `切换账号失败: ${detail}` : '切换账号失败');
      }
    },
    [loadQuota, onAccountError, scope.threadID, sheetRole, updateThread]
  );

  if (groups.length === 0 && !sheetGroup) return null;

  return (
    <>
      {groups.length > 0 && (
        <div className={quotaStripClass(floating)}>
          {groups.map((group, groupIndex) => {
            const unknown = group.budget.state === 'unknown' || group.budget.state === 'attention';
            const readouts = unknown || group.budget.state === 'credits' ? [] : stripReadouts(group.budget);
            const accountSuffix = group.budget.account ? ` · ${group.budget.account}` : '';
            return (
              <React.Fragment key={group.key}>
                {groupIndex > 0 && (
                  <span aria-hidden="true" className="mx-1 shrink-0 text-mc-caption text-[var(--foreground-muted)]">｜</span>
                )}
                <button
                  type="button"
                  onClick={() => setSheetGroupKey(group.key)}
                  title={`${group.providerName} 额度${accountSuffix}${unknown ? ' · 不可用' : ''}`}
                  aria-label={`查看 ${group.providerName} 额度详情${accountSuffix}`}
                  className={`flex shrink-0 items-center gap-1.5 rounded-mc-xs px-1 py-0.5 transition ${
                    group.budget.state === 'stale' ? 'opacity-50' : ''
                  } ${sheetGroupKey === group.key ? 'bg-[var(--border)]' : ''}`}
                >
                  <ProviderIcon provider={group.provider} colored className="w-3 h-3 shrink-0 text-[var(--foreground-muted)]" title={group.providerName} />
                  {group.budget.state === 'credits' ? (
                    <span className="tabular-nums text-mc-caption leading-none font-medium">{group.budget.amount}</span>
                  ) : unknown ? (
                    <span className="text-mc-caption leading-none opacity-80">不可用</span>
                  ) : (
                    readouts.map((readout) => {
                      if (readout.usedPercent === null) {
                        // 纯文本 / 余额 / 不限量：只显示文字，不画进度条、不显示百分比
                        const text = readout.text ?? '';
                        return (
                          <span
                            key={`${readout.label}:${text}`}
                            role="group"
                            aria-label={`${group.providerName} ${readout.label} ${text}`}
                            className="flex items-center gap-1"
                          >
                            <span className="text-mc-caption leading-none opacity-80">{readout.label}</span>
                            <span className="text-mc-caption leading-none font-medium">{text}</span>
                          </span>
                        );
                      }
                      const color = usageColor(readout.usedPercent);
                      const percentText = formatQuotaPercent(readout.usedPercent);
                      return (
                        <span
                          key={readout.label}
                          role="group"
                          aria-label={`${group.providerName} ${readout.label} 已用 ${percentText}%`}
                          className="flex items-center gap-1"
                        >
                          <span className="text-mc-caption leading-none opacity-80">{readout.label}</span>
                          <span aria-hidden="true" className="block h-[3px] w-[18px] overflow-hidden rounded-full bg-[var(--border)]">
                            <span
                              className="block h-full rounded-full"
                              style={{ width: `${readout.usedPercent}%`, backgroundColor: color }}
                            />
                          </span>
                          <span className="tabular-nums text-mc-caption leading-none font-medium" style={{ color: usagePercentColor(readout.usedPercent) }}>
                            {percentText}%
                          </span>
                        </span>
                      );
                    })
                  )}
                </button>
              </React.Fragment>
            );
          })}
        </div>
      )}

      <ComposerQuotaSheet
        isOpen={Boolean(sheetGroup)}
        onClose={closeSheet}
        provider={sheetProvider}
        role={sheetRole}
        budgets={sheetBudgets}
        activeAccount={activeAccount}
        hasThread={Boolean(threadID)}
        selectedAccountID={selectedAccountID}
        isRunning={isRunning}
        hasMessages={hasMessages}
        onSelectAccount={handleSelectAccount}
        refreshing={refreshing}
        onRefresh={handleRefresh}
      />
    </>
  );
};
