// 会话更多操作面板的「编排」子页：全局开关、会话级编排模式、主代理只规划覆盖、子代理档位覆盖。
// 会话级编排模式的状态留在 ThreadMenuSheet（主页面那一行要显示当前模式），这里只负责渲染与回写。

import { useCallback, useEffect, useState } from 'react';
import type { RemoteThreadSummary } from '../../../types/contract';
import type { RemoteSwarmState } from '../../../types/swarm';
import type { RemoteSwarmTier } from '../../../types/generated';
import { useRemoteStatic } from '../../../context/RemoteContext';
import { makeActionID } from '../../../context/remoteGuards';
import type { ThreadSwarmMode } from '../useThreadMetaActions';
import { useRemoteMethodSupport } from '../useThreadMetaActions';
import { overrideCount, parseBeeTiers, serializeBeeTiers } from '../../../utils/beeTiers';
import type { TierOverrideDraft } from '../../../utils/beeTiers';

// 会话级工作模式，文案与顺序照抄桌面端 internal/ui/swarm_mode_menu.go 的 swarmModeOptions
const SWARM_MODE_OPTIONS: Array<{ mode: ThreadSwarmMode; label: string; detail: string }> = [
  { mode: 'inherited', label: '跟随全局设置', detail: '按「设置 › 编排」里的全局开关决定' },
  {
    mode: 'collaborative',
    label: '协同模式',
    detail: '主代理规划并委派大任务；小改动可亲自执行'
  },
  { mode: 'leadPlansOnly', label: '主代理只规划不动手', detail: '主代理只规划并委派，零代码改动' },
  {
    mode: 'alwaysBees',
    label: '消息直达子代理',
    detail: '绕过主代理模型；每条消息直接派发给子代理执行'
  }
];

const TIER_INPUT_CLASS =
  'w-full min-h-[32px] px-2 rounded-lg border border-[var(--input-border)] bg-[var(--screen)] text-mc-caption font-mono text-[var(--foreground)] outline-none focus:border-[var(--primary)] disabled:opacity-50';

export interface ThreadMenuSwarmPanelProps {
  threadID: string;
  latestSummary: RemoteThreadSummary;
  swarmState: RemoteSwarmState | null;
  swarmAvailable: boolean;
  swarmEnabled: boolean;
  isTogglingSwarm: boolean;
  swarmInlineError: string | null;
  handleToggleSwarm: (nextEnabled: boolean) => Promise<void>;
  /** 宿主是否支持会话级编排模式（threads.swarm.setMode） */
  swarmModeSupported: boolean;
  /** 当前生效的会话级编排模式，由 ThreadMenuSheet 持有以保证主页面同步 */
  swarmMode: ThreadSwarmMode;
  isSettingSwarmMode: boolean;
  swarmModeError: string | null;
  onSelectSwarmMode: (mode: ThreadSwarmMode) => void;
}

export function ThreadMenuSwarmPanel({
  threadID,
  latestSummary,
  swarmState,
  swarmAvailable,
  swarmEnabled,
  isTogglingSwarm,
  swarmInlineError,
  handleToggleSwarm,
  swarmModeSupported,
  swarmMode,
  isSettingSwarmMode,
  swarmModeError,
  onSelectSwarmMode
}: ThreadMenuSwarmPanelProps) {
  const { getSwarmTiers, setThreadSwarmPlansOnly: setPlansOnlyRpc, setThreadSwarmBeeTiers: setBeeTiersRpc } =
    useRemoteStatic();
  const supportsMethod = useRemoteMethodSupport();

  // 主代理只规划：plansOnlyOverridden 为 true 时可以清除覆盖、回到全局默认
  const plansOnlySupported = supportsMethod('threads.swarm.setPlansOnly');
  const livePlansOnly = swarmState?.plansOnly ?? false;
  const livePlansOnlyOverridden = swarmState?.plansOnlyOverridden ?? false;
  const [plansOnlyOverride, setPlansOnlyOverride] = useState<{
    plansOnly: boolean;
    overridden: boolean;
  } | null>(null);
  const plansOnly = plansOnlyOverride?.plansOnly ?? livePlansOnly;
  const plansOnlyOverridden = plansOnlyOverride?.overridden ?? livePlansOnlyOverridden;
  const [isSettingPlansOnly, setIsSettingPlansOnly] = useState(false);
  const [plansOnlyError, setPlansOnlyError] = useState<string | null>(null);

  useEffect(() => {
    setPlansOnlyOverride(null);
  }, [livePlansOnly, livePlansOnlyOverridden, threadID]);

  const handleSetPlansOnly = useCallback(
    async (next: boolean | null) => {
      if (isSettingPlansOnly) return;
      setIsSettingPlansOnly(true);
      setPlansOnlyError(null);
      try {
        const updated = await setPlansOnlyRpc({ threadID, plansOnly: next, actionID: makeActionID() });
        setPlansOnlyOverride({
          plansOnly: updated.plansOnly ?? false,
          overridden: updated.plansOnlyOverridden ?? next !== null
        });
      } catch (err) {
        setPlansOnlyError(err instanceof Error ? err.message : '切换主代理只规划失败');
      } finally {
        setIsSettingPlansOnly(false);
      }
    },
    [isSettingPlansOnly, setPlansOnlyRpc, threadID]
  );

  // 子代理档位覆盖：子页打开即读取全局档位表，草稿按档位 ID 索引，保存时整表序列化下发
  const beeTiersSupported = supportsMethod('threads.swarm.setBeeTiers');
  const [tiers, setTiers] = useState<RemoteSwarmTier[] | null>(null);
  const [tierDraft, setTierDraft] = useState<TierOverrideDraft>(() =>
    parseBeeTiers(latestSummary.swarmBeeTiers)
  );
  const [tierBaseline, setTierBaseline] = useState<string>(() =>
    serializeBeeTiers(parseBeeTiers(latestSummary.swarmBeeTiers))
  );
  const [isLoadingTiers, setIsLoadingTiers] = useState(false);
  const [isSavingTiers, setIsSavingTiers] = useState(false);
  const [tierError, setTierError] = useState<string | null>(null);
  const [tierNotice, setTierNotice] = useState<string | null>(null);

  const loadTiers = useCallback(async () => {
    setIsLoadingTiers(true);
    setTierError(null);
    try {
      const result = await getSwarmTiers();
      setTiers(result.tiers);
    } catch (err) {
      setTiers([]);
      setTierError(err instanceof Error ? err.message : '读取子代理档位失败');
    } finally {
      setIsLoadingTiers(false);
    }
  }, [getSwarmTiers]);

  // 档位覆盖不再折叠，进入子页就把档位表拉下来
  useEffect(() => {
    if (tiers === null) void loadTiers();
  }, [loadTiers, tiers]);

  // 宿主推送的会话摘要变了就以它为准
  useEffect(() => {
    const parsed = parseBeeTiers(latestSummary.swarmBeeTiers);
    setTierDraft(parsed);
    setTierBaseline(serializeBeeTiers(parsed));
  }, [latestSummary.swarmBeeTiers, threadID]);

  const updateTierDraft = useCallback((tierIDKey: string, field: 'provider' | 'model', value: string) => {
    setTierDraft((previous) => {
      const nextEntry: Record<string, unknown> = { ...(previous[tierIDKey] ?? {}), [field]: value };
      // 清空输入等于取消该字段的覆盖
      if (!value.trim()) delete nextEntry[field];
      const next: TierOverrideDraft = { ...previous };
      if (Object.keys(nextEntry).length > 0) next[tierIDKey] = nextEntry;
      else delete next[tierIDKey];
      return next;
    });
    setTierNotice(null);
  }, []);

  const resetTierDraft = useCallback((tierIDKey: string) => {
    setTierDraft((previous) => {
      if (!previous[tierIDKey]) return previous;
      const next = { ...previous };
      delete next[tierIDKey];
      return next;
    });
    setTierNotice(null);
  }, []);

  const handleSaveTiers = useCallback(async () => {
    if (isSavingTiers) return;
    setIsSavingTiers(true);
    setTierError(null);
    setTierNotice(null);
    try {
      const payload = serializeBeeTiers(tierDraft);
      await setBeeTiersRpc({ threadID, beeTiers: payload, actionID: makeActionID() });
      setTierBaseline(payload);
      setTierNotice(payload ? '已保存档位覆盖' : '已恢复默认档位');
    } catch (err) {
      setTierError(err instanceof Error ? err.message : '保存档位覆盖失败');
    } finally {
      setIsSavingTiers(false);
    }
  }, [isSavingTiers, setBeeTiersRpc, threadID, tierDraft]);

  const tierDirty = serializeBeeTiers(tierDraft) !== tierBaseline;

  return (
    <div className="mt-4">
      <div className="flex items-center justify-between">
        <div className="min-w-0 pr-3">
          <div className="text-xs font-semibold text-[var(--foreground)]">启用编排</div>
          <div className="text-mc-caption text-[var(--foreground-muted)]">
            {!swarmAvailable ? '宿主环境不可用' : swarmEnabled ? '已启用 · 可派发子代理' : '未启用 · 单会话'}
          </div>
        </div>
        <button
          type="button"
          role="switch"
          aria-checked={swarmEnabled}
          disabled={!swarmAvailable || isTogglingSwarm}
          onClick={() => void handleToggleSwarm(!swarmEnabled)}
          className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none disabled:opacity-40 ${
            swarmEnabled ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'
          }`}
        >
          <span
            aria-hidden="true"
            className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out ${
              swarmEnabled ? 'translate-x-5' : 'translate-x-0'
            }`}
          />
        </button>
      </div>
      {swarmInlineError && (
        <p className="mt-1.5 text-mc-caption text-[var(--danger-foreground)]">{swarmInlineError}</p>
      )}

      {/* 会话级编排：工作模式、只规划覆盖与子代理档位覆盖（子代理会话本身不承载这些设置） */}
      {swarmAvailable && (swarmModeSupported || plansOnlySupported || beeTiersSupported) && (
        <>
          {swarmModeSupported && (
            <div className="mt-3 pt-3 border-t border-[var(--border)]">
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-xs font-semibold text-[var(--foreground)]">编排模式</span>
                <span className="text-mc-caption text-[var(--foreground-muted)]">
                  {isSettingSwarmMode ? '保存中…' : '本会话生效'}
                </span>
              </div>
              <div role="radiogroup" aria-label="编排模式" className="space-y-1">
                {SWARM_MODE_OPTIONS.map((option) => {
                  const active = option.mode === swarmMode;
                  return (
                    <button
                      key={option.mode}
                      type="button"
                      role="radio"
                      aria-checked={active}
                      disabled={isSettingSwarmMode}
                      onClick={() => onSelectSwarmMode(option.mode)}
                      className={`w-full min-h-[44px] px-2.5 py-2 rounded-xl border text-left flex items-start gap-2 transition disabled:opacity-50 ${
                        active
                          ? 'border-[var(--primary)] bg-[var(--user-bubble)]'
                          : 'border-[var(--border)] bg-[var(--card)]'
                      }`}
                    >
                      <span
                        aria-hidden="true"
                        className={`mt-0.5 shrink-0 text-mc-caption ${
                          active ? 'text-[var(--primary)]' : 'text-transparent'
                        }`}
                      >
                        ✓
                      </span>
                      <span className="min-w-0">
                        <span className="block text-xs font-semibold text-[var(--foreground)]">
                          {option.label}
                        </span>
                        <span className="block text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                          {option.detail}
                        </span>
                      </span>
                    </button>
                  );
                })}
              </div>
              {swarmModeError && (
                <p className="mt-1.5 text-mc-caption text-[var(--danger-foreground)]" role="alert">
                  {swarmModeError}
                </p>
              )}
            </div>
          )}

          {/* 新宿主用「主代理只规划不动手」这个编排模式承担同一功能，两者同时存在会让开关互相打架 */}
          {plansOnlySupported && !swarmModeSupported && (
            <div className="mt-3 pt-3 border-t border-[var(--border)]">
              <div className="flex items-center justify-between">
                <div className="min-w-0 pr-3">
                  <div className="text-xs font-semibold text-[var(--foreground)]">主代理只规划</div>
                  <div className="text-mc-caption text-[var(--foreground-muted)]">
                    {plansOnlyOverridden ? '已覆盖全局设置' : '跟随全局设置'}
                  </div>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={plansOnly}
                  aria-label="切换主代理只规划"
                  disabled={isSettingPlansOnly}
                  onClick={() => void handleSetPlansOnly(!plansOnly)}
                  className={`relative inline-flex h-6 w-11 shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out disabled:opacity-40 ${
                    plansOnly ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'
                  }`}
                >
                  <span
                    aria-hidden="true"
                    className={`pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out ${
                      plansOnly ? 'translate-x-5' : 'translate-x-0'
                    }`}
                  />
                </button>
              </div>
              {plansOnlyOverridden && (
                <button
                  type="button"
                  disabled={isSettingPlansOnly}
                  onClick={() => void handleSetPlansOnly(null)}
                  className="mt-1 text-mc-caption font-medium text-[var(--primary)] disabled:opacity-50"
                >
                  恢复跟随全局
                </button>
              )}
              {plansOnlyError && (
                <p className="mt-1.5 text-mc-caption text-[var(--danger-foreground)]" role="alert">
                  {plansOnlyError}
                </p>
              )}
            </div>
          )}

          {beeTiersSupported && (
            <div className="mt-3 pt-3 border-t border-[var(--border)]">
              <div className="flex items-center justify-between mb-1.5">
                <span className="text-xs font-semibold text-[var(--foreground)]">子代理档位覆盖</span>
                <span className="text-mc-caption text-[var(--foreground-muted)]">
                  {overrideCount(tierDraft) > 0 ? `${overrideCount(tierDraft)} 个档位已覆盖` : '默认'}
                </span>
              </div>
              <div className="space-y-2">
                {isLoadingTiers && (
                  <p className="text-mc-caption text-[var(--foreground-muted)]">正在读取档位…</p>
                )}
                {!isLoadingTiers && tiers && tiers.length === 0 && (
                  <p className="text-mc-caption text-[var(--foreground-muted)]">宿主没有可用的子代理档位</p>
                )}
                {tiers && tiers.length > 0 && (
                  <div className="space-y-2">
                    {tiers.map((tier) => {
                      const override = tierDraft[tier.id];
                      const tierLabel = tier.name || tier.id;
                      const providerValue =
                        override && typeof override.provider === 'string' ? override.provider : '';
                      const modelValue =
                        override && typeof override.model === 'string' ? override.model : '';
                      return (
                        <div
                          key={tier.id}
                          className="p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-1.5"
                        >
                          <div className="flex items-center justify-between gap-2">
                            <span className="min-w-0 truncate text-mc-caption font-semibold text-[var(--foreground)]">
                              {tierLabel}
                            </span>
                            {override ? (
                              <button
                                type="button"
                                disabled={isSavingTiers}
                                onClick={() => resetTierDraft(tier.id)}
                                className="shrink-0 text-mc-caption font-medium text-[var(--primary)] disabled:opacity-50"
                              >
                                恢复默认
                              </button>
                            ) : (
                              <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">默认</span>
                            )}
                          </div>
                          <div className="grid grid-cols-2 gap-1.5">
                            <input
                              type="text"
                              value={providerValue}
                              onChange={(event) => updateTierDraft(tier.id, 'provider', event.target.value)}
                              placeholder={tier.provider || 'Provider'}
                              aria-label={`${tierLabel} 的 provider 覆盖`}
                              disabled={isSavingTiers}
                              className={TIER_INPUT_CLASS}
                            />
                            <input
                              type="text"
                              value={modelValue}
                              onChange={(event) => updateTierDraft(tier.id, 'model', event.target.value)}
                              placeholder={tier.model || 'Model'}
                              aria-label={`${tierLabel} 的 model 覆盖`}
                              disabled={isSavingTiers}
                              className={TIER_INPUT_CLASS}
                            />
                          </div>
                        </div>
                      );
                    })}
                  </div>
                )}

                <div className="flex items-center justify-between gap-2">
                  <span className="min-w-0 text-mc-caption text-[var(--foreground-muted)]">
                    {tierNotice || (tierDirty ? '有未保存的改动' : '留空的档位跟随全局档位配置')}
                  </span>
                  <button
                    type="button"
                    onClick={() => void handleSaveTiers()}
                    disabled={isSavingTiers || !tierDirty}
                    className="min-h-[36px] px-3 rounded-xl text-xs font-semibold bg-[var(--primary)] text-white disabled:opacity-40 transition"
                  >
                    {isSavingTiers ? '保存中…' : '保存'}
                  </button>
                </div>

                {tierError && (
                  <p className="text-mc-caption text-[var(--danger-foreground)]" role="alert">
                    {tierError}
                  </p>
                )}
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}
