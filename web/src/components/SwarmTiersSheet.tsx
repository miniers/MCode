// 全局子代理档位抽屉：列出/新增/编辑/删除档位，改动先落在本地草稿，保存时整表提交给 Mac 端

import React, { useCallback, useEffect, useRef, useState } from 'react';
import type { RemoteSwarmTier } from '../types/generated';
import { useRemoteStatic } from '../context/RemoteContext';
import { modelDisplayName } from '../utils/modelLabel';
import { makeActionID } from '../context/remoteGuards';
import { ConfirmDialog } from './ConfirmDialog';
import {
  ArrowPathIcon,
  ExclamationTriangleIcon,
  LockIcon,
  PlusIcon,
  XMarkIcon
} from './Icons';

export interface SwarmTiersSheetProps {
  isOpen: boolean;
  onClose: () => void;
}

interface TierEditorState {
  isNew: boolean;
  index: number;
  draft: RemoteSwarmTier;
}

// 与宿主 config.Validate 的档位 ID 规则一致（config.go tierIDRegex）
const TIER_ID_PATTERN = /^[a-z0-9][a-z0-9_-]{0,31}$/;
// config.MaxSuggestedSwarmTiers：只是建议上限，超过后桌面端会提示，但不阻止保存
const MAX_SUGGESTED_TIERS = 5;
const EFFORT_OPTIONS = ['', 'low', 'medium', 'high', 'xhigh', 'max'] as const;

const INPUT_CLASS =
  'pairing-input w-full min-w-0 text-sm';
const TEXTAREA_CLASS =
  'pairing-input min-h-24 resize-y w-full min-w-0 text-sm';

const trimmed = (value?: string) => (value ?? '').trim();

// 服务端返回的可选字段可能被 omitempty 省略，补齐成可编辑的字符串
const normalizeTier = (tier: RemoteSwarmTier): RemoteSwarmTier => ({
  id: trimmed(tier.id).toLowerCase(),
  name: trimmed(tier.name),
  description: trimmed(tier.description),
  readOnly: tier.readOnly,
  // 写入白名单本表单不编辑，但必须原样回传，否则保存一次就会丢
  writePaths: tier.writePaths && tier.writePaths.length > 0 ? [...tier.writePaths] : undefined,
  provider: trimmed(tier.provider),
  model: trimmed(tier.model),
  effort: trimmed(tier.effort),
  systemPrompt: trimmed(tier.systemPrompt),
  fastMode: tier.fastMode ?? false
});

const emptyDraft = (): RemoteSwarmTier => ({
  id: '',
  name: '',
  description: '',
  readOnly: false,
  provider: '',
  model: '',
  effort: '',
  systemPrompt: '',
  fastMode: false
});

// 本地校验沿用宿主规则，避免整表保存后才看到英文报错
function validateTiers(list: RemoteSwarmTier[]): string | null {
  const seen = new Set<string>();
  for (const tier of list) {
    if (!TIER_ID_PATTERN.test(tier.id)) {
      return `档位 ID「${tier.id || '空'}」需为 1-32 位小写字母、数字、下划线或短横线，并以字母或数字开头`;
    }
    if (seen.has(tier.id)) {
      return `档位 ID「${tier.id}」重复，请换一个`;
    }
    seen.add(tier.id);
  }
  if (list.length > 0 && !list.some((tier) => !tier.readOnly)) {
    return '至少保留一个非只读档位';
  }
  return null;
}

export const SwarmTiersSheet: React.FC<SwarmTiersSheetProps> = ({ isOpen, onClose }) => {
  const {
    capabilities,
    connectionState,
    connectionMessage,
    getSwarmTiers,
    setSwarmTiers,
    providers
  } = useRemoteStatic();

  const [tiers, setTiers] = useState<RemoteSwarmTier[]>([]);
  const [baseline, setBaseline] = useState('[]');
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [savedNotice, setSavedNotice] = useState(false);
  const [editing, setEditing] = useState<TierEditorState | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [confirmClose, setConfirmClose] = useState(false);

  const reqIdRef = useRef(0);
  const mountedRef = useRef(true);

  const hasGetMethod = capabilities?.methods.includes('swarm.tiers.get') ?? false;
  const hasSetMethod = capabilities?.methods.includes('swarm.tiers.set') ?? false;
  const isConnected = connectionState === 'connected';
  const dirty = JSON.stringify(tiers) !== baseline;
  const hasUnsaved = dirty || editing !== null;

  const fetchTiers = useCallback(async () => {
    if (!hasGetMethod) {
      setLoadError('当前宿主未声明子代理档位能力 (swarm.tiers.get)');
      return;
    }
    if (!isConnected) {
      setLoadError(`未连接到 Mac 宿主（当前连接：${connectionMessage || connectionState}）`);
      return;
    }

    const reqId = ++reqIdRef.current;
    setLoading(true);
    setLoadError(null);

    try {
      const result = await getSwarmTiers();
      if (!mountedRef.current || reqId !== reqIdRef.current) return;
      const next = result.tiers.map(normalizeTier);
      setTiers(next);
      setBaseline(JSON.stringify(next));
      setEditing(null);
      setFormError(null);
      setSaveError(null);
      setSavedNotice(false);
    } catch (error) {
      if (!mountedRef.current || reqId !== reqIdRef.current) return;
      setLoadError(error instanceof Error ? error.message : '读取子代理档位失败');
    } finally {
      if (mountedRef.current && reqId === reqIdRef.current) {
        setLoading(false);
      }
    }
  }, [connectionMessage, connectionState, getSwarmTiers, hasGetMethod, isConnected]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
    };
  }, []);

  // 打开时拉取；关闭时重置草稿并作废在途请求
  useEffect(() => {
    if (isOpen) {
      void fetchTiers();
    } else {
      reqIdRef.current += 1;
      setTiers([]);
      setBaseline('[]');
      setLoading(false);
      setLoadError(null);
      setSaving(false);
      setSaveError(null);
      setSavedNotice(false);
      setEditing(null);
      setFormError(null);
      setConfirmClose(false);
    }
  }, [fetchTiers, isOpen]);

  const requestClose = useCallback(() => {
    if (saving) return;
    if (hasUnsaved) {
      setConfirmClose(true);
      return;
    }
    onClose();
  }, [hasUnsaved, onClose, saving]);

  // Esc 先退编辑表单，再走带未保存确认的关闭
  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || confirmClose) return;
      if (editing) {
        setEditing(null);
        setFormError(null);
        return;
      }
      requestClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [confirmClose, editing, isOpen, requestClose]);

  const openEditor = (index: number) => {
    const tier = tiers[index];
    if (!tier) return;
    setEditing({ isNew: false, index, draft: normalizeTier(tier) });
    setFormError(null);
    setSaveError(null);
    setSavedNotice(false);
  };

  const openCreator = () => {
    setEditing({ isNew: true, index: -1, draft: emptyDraft() });
    setFormError(null);
    setSaveError(null);
    setSavedNotice(false);
  };

  const updateDraft = (patch: Partial<RemoteSwarmTier>) => {
    setEditing((prev) => (prev ? { ...prev, draft: { ...prev.draft, ...patch } } : prev));
  };

  // 表单“确定”只写入本地草稿列表，落盘发生在底部“保存”
  const commitDraft = () => {
    if (!editing) return;
    const normalized = normalizeTier(editing.draft);
    const next = editing.isNew
      ? [...tiers, normalized]
      : tiers.map((tier, index) => (index === editing.index ? normalized : tier));
    const error = validateTiers(next);
    if (error) {
      setFormError(error);
      return;
    }
    setTiers(next);
    setEditing(null);
    setFormError(null);
    setSaveError(null);
    setSavedNotice(false);
  };

  const handleDelete = (index: number) => {
    const next = tiers.filter((_tier, position) => position !== index);
    const error = validateTiers(next);
    if (error) {
      setSaveError(error);
      return;
    }
    setTiers(next);
    setSaveError(null);
    setSavedNotice(false);
    setEditing(null);
  };

  const handleSave = async () => {
    if (saving || !hasSetMethod) return;
    const error = validateTiers(tiers);
    if (error) {
      setSaveError(error);
      return;
    }

    setSaving(true);
    setSaveError(null);
    setSavedNotice(false);

    try {
      const result = await setSwarmTiers({
        tiers: tiers.map(normalizeTier),
        actionID: makeActionID()
      });
      if (!mountedRef.current) return;
      // 以宿主归一化后的结果为准（小写 ID、空名称回填等）
      const next = result.tiers.map(normalizeTier);
      setTiers(next);
      setBaseline(JSON.stringify(next));
      setEditing(null);
      setSavedNotice(true);
    } catch (err) {
      if (!mountedRef.current) return;
      setSaveError(err instanceof Error ? err.message : '保存子代理档位失败');
    } finally {
      if (mountedRef.current) setSaving(false);
    }
  };

  if (!isOpen) return null;

  const renderTierMeta = (tier: RemoteSwarmTier) => {
    const hasModel = Boolean(tier.provider || tier.model);
    return (
      <>
        <div className="flex flex-wrap items-center gap-1.5 text-mc-caption text-[var(--foreground-muted)]">
          {hasModel ? (
            <span className="px-1.5 py-0.5 rounded-md bg-[var(--border)]/40 font-mono break-all">
              {[tier.provider, modelDisplayName(providers, tier.provider, tier.model)].filter(Boolean).join(' / ')}
            </span>
          ) : (
            <span className="italic">跟随会话子代理模型</span>
          )}
          {tier.effort && (
            <span className="px-1.5 py-0.5 rounded-md bg-[var(--border)]/40">思考 {tier.effort}</span>
          )}
          {tier.fastMode && (
            <span className="px-1.5 py-0.5 rounded-md bg-[var(--border)]/40">Fast</span>
          )}
          {tier.systemPrompt && (
            <span className="px-1.5 py-0.5 rounded-md bg-[var(--border)]/40">含系统提示词</span>
          )}
        </div>
        {tier.description && (
          <p className="text-xs text-[var(--foreground-muted)] leading-relaxed break-words">
            {tier.description}
          </p>
        )}
      </>
    );
  };

  const renderEditor = () => {
    if (!editing) return null;
    const { draft, isNew } = editing;
    return (
      <div className="space-y-3">
        <div className="flex items-center justify-between gap-3">
          <h3 className="text-sm font-bold text-[var(--foreground)]">
            {isNew ? '添加子代理档位' : '编辑子代理档位'}
          </h3>
          <button
            type="button"
            className="text-xs font-medium text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition"
            onClick={() => {
              setEditing(null);
              setFormError(null);
            }}
          >
            取消编辑
          </button>
        </div>

        <label className="block space-y-1">
          <span className="text-xs text-[var(--foreground-muted)]">档位 ID</span>
          <input
            type="text"
            value={draft.id}
            onChange={(event) => updateDraft({ id: event.target.value })}
            disabled={!isNew}
            placeholder="英文小写标识，如 read / edit / deep"
            className={`${INPUT_CLASS} disabled:opacity-60`}
          />
          <span className="block text-mc-caption text-[var(--foreground-muted)]">
            {isNew ? '1-32 位小写字母、数字、下划线或短横线，保存后不可更改' : '已存在的档位 ID 不可修改'}
          </span>
        </label>

        <label className="block space-y-1">
          <span className="text-xs text-[var(--foreground-muted)]">档位名称</span>
          <input
            type="text"
            value={draft.name}
            onChange={(event) => updateDraft({ name: event.target.value })}
            placeholder="如：只读 / 执行 / 深度"
            className={INPUT_CLASS}
          />
        </label>

        <label className="block space-y-1">
          <span className="text-xs text-[var(--foreground-muted)]">用途说明</span>
          <textarea
            value={draft.description}
            onChange={(event) => updateDraft({ description: event.target.value })}
            placeholder="向主代理说明该档位的适用场景"
            className={TEXTAREA_CLASS}
          />
        </label>

        <div className="flex items-center justify-between gap-3 py-1">
          <div className="min-w-0">
            <p className="text-xs font-semibold text-[var(--foreground)]">只读模式</p>
            <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
              只读档子代理在只读模式和独立 worktree 中运行，任何改动都会被丢弃
            </p>
          </div>
          <button
            type="button"
            role="switch"
            aria-checked={draft.readOnly}
            aria-label="切换只读模式"
            onClick={() => updateDraft({ readOnly: !draft.readOnly })}
            className={`relative h-5 w-9 shrink-0 rounded-full transition ${draft.readOnly ? 'bg-mc-accent' : 'bg-[var(--border)]'}`}
          >
            <span
              className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${draft.readOnly ? 'translate-x-4' : ''}`}
            />
          </button>
        </div>

        <div className="space-y-2">
          <p className="text-xs font-semibold text-[var(--foreground)]">模型配置</p>
          <p className="text-mc-caption text-[var(--foreground-muted)]">
            留空表示跟随会话当前生效的子代理模型
          </p>
          <div className="grid grid-cols-2 gap-2">
            <label className="block space-y-1 min-w-0">
              <span className="text-xs text-[var(--foreground-muted)]">Provider</span>
              <input
                type="text"
                value={draft.provider ?? ''}
                onChange={(event) => updateDraft({ provider: event.target.value })}
                placeholder="留空跟随会话"
                className={`${INPUT_CLASS} font-mono text-xs`}
              />
            </label>
            <label className="block space-y-1 min-w-0">
              <span className="text-xs text-[var(--foreground-muted)]">Model</span>
              <input
                type="text"
                value={draft.model ?? ''}
                onChange={(event) => updateDraft({ model: event.target.value })}
                placeholder="留空跟随会话"
                className={`${INPUT_CLASS} font-mono text-xs`}
              />
            </label>
          </div>

          <label className="block space-y-1">
            <span className="text-xs text-[var(--foreground-muted)]">思考档位</span>
            <select
              value={draft.effort ?? ''}
              onChange={(event) => updateDraft({ effort: event.target.value })}
              className="sheet-select"
            >
              {EFFORT_OPTIONS.map((option) => (
                <option key={option || 'default'} value={option}>
                  {option || '跟随会话'}
                </option>
              ))}
            </select>
          </label>

          <div className="flex items-center justify-between gap-3 py-1">
            <div className="min-w-0">
              <p className="text-xs font-semibold text-[var(--foreground)]">Fast 模式</p>
              <p className="text-mc-caption text-[var(--foreground-muted)]">更快的输出速度，可能消耗更多额度</p>
            </div>
            <button
              type="button"
              role="switch"
              aria-checked={draft.fastMode ?? false}
              aria-label="切换 Fast 模式"
              onClick={() => updateDraft({ fastMode: !(draft.fastMode ?? false) })}
              className={`relative h-5 w-9 shrink-0 rounded-full transition ${draft.fastMode ? 'bg-mc-accent' : 'bg-[var(--border)]'}`}
            >
              <span
                className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${draft.fastMode ? 'translate-x-4' : ''}`}
              />
            </button>
          </div>
        </div>

        <label className="block space-y-1">
          <span className="text-xs text-[var(--foreground-muted)]">系统提示词（可选）</span>
          <textarea
            value={draft.systemPrompt ?? ''}
            onChange={(event) => updateDraft({ systemPrompt: event.target.value })}
            placeholder="为该档位注入额外的系统指令（可选）"
            className={TEXTAREA_CLASS}
          />
        </label>

        {formError && (
          <p className="text-xs text-[var(--danger-foreground)] break-words" role="alert">{formError}</p>
        )}

        <div className="flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={() => {
              setEditing(null);
              setFormError(null);
            }}
            className="min-h-[40px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] transition"
          >
            取消
          </button>
          <button
            type="button"
            onClick={commitDraft}
            className="primary-button min-h-[40px] px-4 text-xs"
          >
            {isNew ? '加入草稿' : '更新草稿'}
          </button>
        </div>
      </div>
    );
  };

  return (
    <>
      <div className="sheet-layer" role="presentation" onMouseDown={requestClose}>
        <section
          className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden"
          role="dialog"
          aria-modal="true"
          aria-labelledby="swarm-tiers-title"
          onMouseDown={(event) => event.stopPropagation()}
        >
          <div className="sheet-handle shrink-0" aria-hidden="true" />

          {/* 标题栏 */}
          <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full mb-3">
            <div className="min-w-0 flex-1">
              <p className="eyebrow truncate">编排</p>
              <h2 id="swarm-tiers-title" className="mt-0.5 text-headline font-bold text-[var(--foreground)]">
                子代理档位
              </h2>
            </div>
            <div className="flex items-center gap-1.5 shrink-0">
              <button
                type="button"
                onClick={() => void fetchTiers()}
                disabled={loading || saving || dirty}
                aria-label="重新读取档位"
                title={dirty ? '有未保存改动，先保存或关闭放弃后再重新读取' : '重新读取档位'}
                className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
              >
                <ArrowPathIcon className={`h-4 w-4 ${loading ? 'animate-spin text-[var(--primary)]' : ''}`} />
              </button>
              <button
                type="button"
                onClick={requestClose}
                aria-label="关闭"
                title="关闭"
                className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
              >
                <XMarkIcon className="h-4 w-4" />
              </button>
            </div>
          </div>

          {/* 主体滚动区 */}
          <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
            <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
              主代理按任务难度为每个子代理选档位，每档对应一个模型；只读档的子代理无法改动项目文件。
            </p>

            {!hasSetMethod && (
              <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--screen)] text-xs text-[var(--foreground-muted)]">
                当前 Mac 未声明 swarm.tiers.set 能力，只能查看档位。
              </div>
            )}

            {loadError && (
              <div className="inline-alert inline-alert--danger" role="alert">
                <div className="flex items-start gap-2 min-w-0 flex-1">
                  <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                  <span className="break-words select-text">{loadError}</span>
                </div>
                <button
                  type="button"
                  onClick={() => void fetchTiers()}
                  disabled={loading}
                  className="inline-alert__action min-h-[44px] px-2 flex items-center"
                >
                  重试
                </button>
              </div>
            )}

            {loading && !loadError && (
              <div className="empty-state">
                <span className="loading-spinner mb-1" />
                <h2>正在读取档位</h2>
                <p className="text-xs text-[var(--foreground-muted)]">正在与 Mac 宿主通信获取档位配置…</p>
              </div>
            )}

            {!loading && !loadError && editing && (
              <div className="p-3.5 rounded-2xl border border-[var(--border)] bg-[var(--card)]">
                {renderEditor()}
              </div>
            )}

            {!loading && !loadError && !editing && (
              <>
                {tiers.length === 0 ? (
                  <div className="empty-state">
                    <h2>暂无档位配置</h2>
                    <p className="text-xs text-[var(--foreground-muted)]">点击下方“添加档位”创建</p>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {tiers.map((tier, index) => (
                      <article
                        key={`${tier.id}-${index}`}
                        className="p-3.5 rounded-2xl border border-[var(--border)] bg-[var(--card)] space-y-2"
                      >
                        <div className="flex items-start justify-between gap-2">
                          <div className="min-w-0 flex-1">
                            <div className="flex items-center gap-1.5 min-w-0">
                              <span className="text-sm font-semibold text-[var(--foreground)] truncate">
                                {tier.name || tier.id}
                              </span>
                              {tier.readOnly && (
                                <span className="inline-flex items-center gap-0.5 shrink-0 px-1.5 py-0.5 rounded text-mc-caption bg-[var(--border)]/60 text-[var(--foreground-muted)]">
                                  <LockIcon className="w-3 h-3" />
                                  只读
                                </span>
                              )}
                            </div>
                            <p className="mt-0.5 font-mono text-mc-caption text-[var(--foreground-muted)] break-all select-text">
                              {tier.id}
                            </p>
                          </div>
                          {hasSetMethod && (
                            <div className="flex items-center gap-1.5 shrink-0">
                              <button
                                type="button"
                                onClick={() => openEditor(index)}
                                className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] transition"
                              >
                                编辑
                              </button>
                              <button
                                type="button"
                                onClick={() => handleDelete(index)}
                                className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-rose-200 dark:border-rose-900/40 bg-rose-50 dark:bg-rose-950/20 hover:bg-rose-100 text-rose-600 dark:text-rose-400 transition"
                              >
                                删除
                              </button>
                            </div>
                          )}
                        </div>
                        {renderTierMeta(tier)}
                      </article>
                    ))}
                  </div>
                )}

                {tiers.length > MAX_SUGGESTED_TIERS && (
                  <div className="flex items-start gap-2 p-3 rounded-xl border border-amber-300/60 dark:border-amber-900/40 bg-amber-50 dark:bg-amber-950/20 text-mc-caption text-amber-700 dark:text-amber-400">
                    <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5" />
                    <span>档位超过 {MAX_SUGGESTED_TIERS} 个时，主代理更容易选错档位</span>
                  </div>
                )}

                {saveError && (
                  <p className="text-xs text-[var(--danger-foreground)] break-words" role="alert">{saveError}</p>
                )}
                {savedNotice && (
                  <p className="text-xs text-emerald-600 dark:text-emerald-400" role="status">已保存</p>
                )}
                {dirty && (
                  <p className="text-mc-caption text-[var(--foreground-muted)]">有未保存的改动</p>
                )}
              </>
            )}
          </div>

          {/* 底部操作栏 */}
          <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 min-w-0 max-w-full w-full flex items-center justify-between gap-2">
            <div className="min-w-0 flex-1 text-mc-caption text-[var(--foreground-muted)] truncate">
              {tiers.length > 0 ? `${tiers.length} 个档位` : '在 Mac 端持久化保存'}
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {hasSetMethod && !editing && (
                <button
                  type="button"
                  onClick={openCreator}
                  disabled={loading || Boolean(loadError)}
                  className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] inline-flex items-center gap-1 disabled:opacity-50 transition"
                >
                  <PlusIcon className="w-3.5 h-3.5" />
                  添加档位
                </button>
              )}
              <button
                type="button"
                onClick={requestClose}
                className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] transition"
              >
                关闭
              </button>
              {hasSetMethod && (
                <button
                  type="button"
                  onClick={() => void handleSave()}
                  disabled={saving || !dirty || Boolean(loadError) || Boolean(editing)}
                  className="primary-button min-h-[44px] min-w-[72px] disabled:opacity-50"
                >
                  {saving ? '保存中…' : '保存'}
                </button>
              )}
            </div>
          </div>
        </section>
      </div>

      <ConfirmDialog
        isOpen={confirmClose}
        title="放弃未保存的改动？"
        message="档位改动还没有保存到 Mac，关闭后本地草稿会丢失。"
        confirmLabel="放弃改动"
        confirmVariant="danger"
        cancelLabel="继续编辑"
        onConfirm={() => {
          setConfirmClose(false);
          setTiers([]);
          setBaseline('[]');
          setEditing(null);
          onClose();
        }}
        onCancel={() => setConfirmClose(false)}
      />
    </>
  );
};
