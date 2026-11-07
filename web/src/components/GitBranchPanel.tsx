// 移动端分支操作面板：搜索并切换本地分支、新建分支、把所选分支合并进当前分支。
// 与推送/提交共用外层传入的 busyKey，同一时间只允许一个 Git 写操作。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  checkoutGitBranch,
  createGitBranch,
  mergeGitBranch,
  type GitExtCaller
} from '../context/remoteGitApi';
import type { RemoteGitBranchesResult } from '../types/contract';
import { ConfirmDialog } from './ConfirmDialog';
import {
  CheckCircleIcon,
  ExclamationTriangleIcon,
  PlusIcon,
  SearchIcon
} from './Icons';

export interface GitBranchPanelProps {
  threadID: string;
  call: GitExtCaller;
  /** 外层已读取的分支列表，避免面板重复拉取。 */
  branches: RemoteGitBranchesResult | null;
  branchesLoading: boolean;
  branchesError: string | null;
  onReloadBranches: () => void;
  /** 分支接口没返回 current 时用状态区的分支兜底。 */
  currentBranch?: string;
  /** 工作区有未提交改动：切换分支前先提示。 */
  hasUncommittedChanges: boolean;
  /** 会话运行中：切换、新建与合并都会改写工作区，此时禁用。 */
  threadRunning: boolean;
  canCheckout: boolean;
  canCreateBranch: boolean;
  canMerge: boolean;
  /** 外层 Git 写操作占用键。 */
  busyKey: string | null;
  onBusyChange: (key: string | null) => void;
  /** 切换、新建、合并成功后刷新外层状态与分支列表。 */
  onChanged: () => void;
  onBack: () => void;
}

type BranchPanelMode = 'switch' | 'new' | 'merge';

const CHECKOUT_BUSY_KEY = 'checkout';
const CREATE_BUSY_KEY = 'create-branch';
const MERGE_BUSY_KEY = 'merge';

const MODE_LABELS: Record<BranchPanelMode, string> = {
  switch: '切换分支',
  new: '新建分支',
  merge: '合并分支'
};

function ChevronLeftIcon({ className = 'w-4 h-4' }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="2" stroke="currentColor">
      <path strokeLinecap="round" strokeLinejoin="round" d="M15.75 19.5 8.25 12l7.5-7.5" />
    </svg>
  );
}

/**
 * 分支名本地预校验，规则与服务端 gitExtBranchNameValid 一致。
 * 只是提前给出提示，最终以服务端返回的错误为准。
 */
function gitBranchNameError(name: string): string | null {
  if (name === '') return null;
  if (name.startsWith('-')) return '分支名不能以 - 开头';
  if (/[\s~^:?*\[\\]/.test(name)) return '分支名不能包含空格或 ~ ^ : ? * [ \\ 等字符';
  if (name.includes('..') || name.includes('//') || name.includes('@{')) {
    return '分支名不能包含 ..、// 或 @{';
  }
  if (name.endsWith('/') || name.endsWith('.') || name.endsWith('.lock')) {
    return '分支名不能以 /、. 或 .lock 结尾';
  }
  return null;
}

export const GitBranchPanel: React.FC<GitBranchPanelProps> = ({
  threadID,
  call,
  branches,
  branchesLoading,
  branchesError,
  onReloadBranches,
  currentBranch,
  hasUncommittedChanges,
  threadRunning,
  canCheckout,
  canCreateBranch,
  canMerge,
  busyKey,
  onBusyChange,
  onChanged,
  onBack
}) => {
  const [mode, setMode] = useState<BranchPanelMode>('switch');
  const [query, setQuery] = useState('');
  const [newName, setNewName] = useState('');
  const [mergeTarget, setMergeTarget] = useState('');
  const [pendingCheckout, setPendingCheckout] = useState<string | null>(null);
  const [pendingMerge, setPendingMerge] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const availableModes = useMemo(() => {
    const modes: BranchPanelMode[] = [];
    if (canCheckout) modes.push('switch');
    if (canCreateBranch) modes.push('new');
    if (canMerge) modes.push('merge');
    return modes;
  }, [canCheckout, canCreateBranch, canMerge]);

  // 宿主只支持部分操作时，把当前模式落到第一个可用的模式上
  const effectiveMode: BranchPanelMode = availableModes.includes(mode)
    ? mode
    : availableModes[0] ?? 'switch';

  const busy = busyKey !== null;
  const current = branches?.current || currentBranch || '';
  const localBranches = branches?.local ?? [];
  const remoteBranches = branches?.remote ?? [];
  const needle = query.trim().toLowerCase();
  const matchedLocal = localBranches.filter(
    (name) => needle === '' || name.toLowerCase().includes(needle)
  );
  const matchedRemote = remoteBranches.filter(
    (name) => needle === '' || name.toLowerCase().includes(needle)
  );
  // 合并候选：本地分支里除当前分支以外的分支，与桌面端 gitOtherBranches 一致
  const mergeCandidates = localBranches.filter(
    (name) => name !== current && (needle === '' || name.toLowerCase().includes(needle))
  );

  const trimmedName = newName.trim();
  const nameError = gitBranchNameError(trimmedName);
  const canSubmitNewBranch = trimmedName !== '' && nameError === null && !busy;

  // 统一执行分支写操作：成功后通知外层刷新，失败显示宿主返回的原文
  const runWrite = useCallback(
    async (
      key: string,
      label: string,
      action: () => Promise<unknown>,
      successText: string
    ): Promise<boolean> => {
      if (busyKey !== null) return false;
      onBusyChange(key);
      setActionError(null);
      setNotice(null);
      try {
        await action();
        if (!mountedRef.current) return true;
        setNotice(successText);
        onChanged();
        return true;
      } catch (err) {
        if (!mountedRef.current) return false;
        setActionError(`${label}失败：${err instanceof Error ? err.message : '未知错误'}`);
        return false;
      } finally {
        if (mountedRef.current) onBusyChange(null);
      }
    },
    [busyKey, onBusyChange, onChanged]
  );

  const doCheckout = useCallback(
    async (branch: string) => {
      setPendingCheckout(null);
      await runWrite(
        CHECKOUT_BUSY_KEY,
        '切换分支',
        () => checkoutGitBranch(call, threadID, branch),
        `已切换到 ${branch}`
      );
    },
    [call, runWrite, threadID]
  );

  // 点分支即切换；有未提交改动时先弹确认，避免改动被带到新分支或切换直接失败
  const requestCheckout = useCallback(
    (branch: string) => {
      if (!canCheckout || busy || threadRunning || branch === '') return;
      if (branch === current) return;
      setActionError(null);
      setNotice(null);
      if (hasUncommittedChanges) {
        setPendingCheckout(branch);
        return;
      }
      void doCheckout(branch);
    },
    [busy, canCheckout, current, doCheckout, hasUncommittedChanges, threadRunning]
  );

  const submitNewBranch = useCallback(async () => {
    if (!canSubmitNewBranch) return;
    const ok = await runWrite(
      CREATE_BUSY_KEY,
      '新建分支',
      () => createGitBranch(call, threadID, trimmedName),
      `已新建并切换到 ${trimmedName}`
    );
    // 失败时保留输入，方便改名后重试
    if (ok && mountedRef.current) setNewName('');
  }, [call, canSubmitNewBranch, runWrite, threadID, trimmedName]);

  const doMerge = useCallback(async () => {
    const branch = pendingMerge;
    if (!branch) return;
    setPendingMerge(null);
    await runWrite(
      MERGE_BUSY_KEY,
      '合并分支',
      () => mergeGitBranch(call, threadID, branch),
      `已把 ${branch} 合并进 ${current || '当前分支'}`
    );
  }, [call, current, pendingMerge, runWrite, threadID]);

  return (
    <div className="space-y-3">
      {/* 顶部：返回与当前分支 */}
      <div className="flex items-center justify-between gap-2 min-w-0">
        <button
          type="button"
          onClick={onBack}
          className="secondary-button min-h-[44px] px-2.5 text-xs shrink-0"
        >
          <ChevronLeftIcon className="w-4 h-4" />
          <span>返回</span>
        </button>
        <span className="min-w-0 flex-1 text-right text-mc-caption text-[var(--foreground-muted)] truncate">
          {current ? (
            <>
              当前分支{' '}
              <span className="select-text font-mono text-[var(--foreground)]">{current}</span>
            </>
          ) : (
            '游离 HEAD'
          )}
        </span>
      </div>

      {/* 模式切换：只显示宿主支持的操作 */}
      {availableModes.length > 1 && (
        <div className="flex gap-1.5 p-1 rounded-xl border border-[var(--border)] bg-[var(--screen)]">
          {availableModes.map((item) => (
            <button
              key={item}
              type="button"
              role="tab"
              aria-selected={effectiveMode === item}
              onClick={() => {
                setMode(item);
                setActionError(null);
                setNotice(null);
              }}
              className={`flex-1 min-h-[44px] px-2 py-1.5 rounded-lg text-xs font-semibold transition ${
                effectiveMode === item
                  ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                  : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
              }`}
            >
              {MODE_LABELS[item]}
            </button>
          ))}
        </div>
      )}

      {/* 会话运行中：切换与合并会改写工作区（桌面端同样禁用），新建分支不受限 */}
      {threadRunning && (
        <div className="inline-alert inline-alert--warning" role="alert">
          <div className="flex items-start gap-2 min-w-0">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--warning-foreground)]" />
            <span className="text-xs leading-relaxed">
              会话正在运行，切换与合并分支暂不可用，请先停止当前运行。
            </span>
          </div>
        </div>
      )}

      {notice && (
        <div
          className="flex items-start gap-2 px-3 py-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 text-xs text-emerald-600 dark:text-emerald-400"
          role="status"
        >
          <CheckCircleIcon className="w-4 h-4 shrink-0 mt-0.5" />
          <span className="break-words select-text leading-relaxed">{notice}</span>
        </div>
      )}

      {actionError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{actionError}</span>
          </div>
        </div>
      )}

      {branchesError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{branchesError}</span>
          </div>
          <button
            type="button"
            onClick={onReloadBranches}
            disabled={branchesLoading}
            className="inline-alert__action min-h-[44px] px-2 flex items-center"
          >
            重试
          </button>
        </div>
      )}

      {branchesLoading && !branches && (
        <div className="flex items-center gap-2 py-1.5 text-xs text-[var(--foreground-muted)]">
          <span className="loading-spinner loading-spinner--small shrink-0" />
          <span>正在读取分支列表...</span>
        </div>
      )}

      {/* 搜索框：切换与合并共用 */}
      {effectiveMode !== 'new' && (
        <label className="flex items-center gap-2 rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 min-h-[44px]">
          <SearchIcon className="w-4 h-4 shrink-0 text-[var(--foreground-muted)]" />
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="搜索分支..."
            aria-label="搜索分支"
            className="min-w-0 flex-1 bg-transparent text-xs text-[var(--foreground)] outline-none"
          />
        </label>
      )}

      {/* 切换分支 */}
      {effectiveMode === 'switch' && (
        <>
          <div className="space-y-1.5">
            <span className="block px-0.5 text-xs font-semibold text-[var(--foreground)]">
              本地分支 ({matchedLocal.length})
            </span>
            {matchedLocal.length === 0 ? (
              <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                {localBranches.length === 0 ? '暂无本地分支' : '没有匹配的分支'}
              </div>
            ) : (
              <ul className="space-y-1">
                {matchedLocal.map((branchName) => {
                  const isCurrent = branchName === current;
                  const disabled = isCurrent || busy || threadRunning;
                  return (
                    <li key={branchName}>
                      <button
                        type="button"
                        onClick={() => requestCheckout(branchName)}
                        disabled={disabled}
                        className={`w-full text-left p-2.5 rounded-lg border min-h-[44px] flex items-center justify-between gap-2 ${
                          isCurrent
                            ? 'border-[var(--primary)] bg-[var(--card-alt,var(--card))]'
                            : 'border-[var(--border)] bg-[var(--card)]'
                        } ${disabled && !isCurrent ? 'opacity-60' : ''}`}
                      >
                        <span className="min-w-0 flex-1 font-mono text-xs break-all leading-snug text-[var(--foreground)]">
                          {branchName}
                        </span>
                        {isCurrent ? (
                          <span className="px-2 py-0.5 rounded text-mc-caption font-semibold bg-[var(--primary)] text-white shrink-0">
                            当前
                          </span>
                        ) : busyKey === CHECKOUT_BUSY_KEY ? (
                          <span className="loading-spinner loading-spinner--small shrink-0" />
                        ) : null}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>

          <div className="space-y-1.5">
            <span className="block px-0.5 text-xs font-semibold text-[var(--foreground)]">
              远程分支 ({matchedRemote.length})
            </span>
            {matchedRemote.length === 0 ? (
              <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                {remoteBranches.length === 0 ? '暂无远程分支' : '没有匹配的分支'}
              </div>
            ) : (
              <ul className="space-y-1">
                {matchedRemote.map((remoteBranch) => (
                  <li
                    key={remoteBranch}
                    className="p-2.5 rounded-lg border border-[var(--border)] bg-[var(--card)] flex items-center gap-2"
                  >
                    <span className="select-text min-w-0 flex-1 font-mono text-xs break-all leading-snug text-[var(--foreground-muted)]">
                      {remoteBranch}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            <p className="px-0.5 text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
              远程分支只读：本地还没有对应分支时，先在 Mac 端建好跟踪分支再回来切换。
            </p>
          </div>
        </>
      )}

      {/* 新建分支 */}
      {effectiveMode === 'new' && (
        <div className="p-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-2">
          <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
            新分支会从当前分支创建并切换过去。
          </p>
          <input
            type="text"
            value={newName}
            onChange={(event) => {
              setNewName(event.target.value);
              setActionError(null);
            }}
            placeholder="新分支名..."
            aria-label="新分支名"
            disabled={busy}
            className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
          />
          {nameError && (
            <p className="text-mc-caption leading-relaxed text-[var(--danger-foreground)]">{nameError}</p>
          )}
          <button
            type="button"
            onClick={() => void submitNewBranch()}
            disabled={!canSubmitNewBranch}
            className="primary-button min-h-[44px] w-full"
          >
            {busyKey === CREATE_BUSY_KEY ? (
              <>
                <span className="loading-spinner loading-spinner--small shrink-0" />
                <span>创建中...</span>
              </>
            ) : (
              <>
                <PlusIcon className="w-4 h-4" />
                <span>创建分支</span>
              </>
            )}
          </button>
        </div>
      )}

      {/* 合并分支 */}
      {effectiveMode === 'merge' && (
        <>
          <div className="space-y-1.5">
            <span className="block px-0.5 text-xs font-semibold text-[var(--foreground)]">
              选择要合入当前分支的分支 ({mergeCandidates.length})
            </span>
            {mergeCandidates.length === 0 ? (
              <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                {localBranches.length === 0 ? '暂无本地分支' : '没有匹配的分支'}
              </div>
            ) : (
              <ul className="space-y-1">
                {mergeCandidates.map((branchName) => {
                  const selected = mergeTarget === branchName;
                  return (
                    <li key={branchName}>
                      <button
                        type="button"
                        onClick={() => setMergeTarget(branchName)}
                        disabled={busy || threadRunning}
                        className={`w-full text-left p-2.5 rounded-lg border min-h-[44px] flex items-center justify-between gap-2 ${
                          selected
                            ? 'border-[var(--primary)] bg-[var(--card-alt,var(--card))]'
                            : 'border-[var(--border)] bg-[var(--card)]'
                        }`}
                      >
                        <span className="select-text min-w-0 flex-1 font-mono text-xs break-all leading-snug text-[var(--foreground)]">
                          {branchName}
                        </span>
                        {selected && (
                          <span className="px-2 py-0.5 rounded text-mc-caption font-semibold bg-[var(--primary)] text-white shrink-0">
                            已选
                          </span>
                        )}
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>

          <button
            type="button"
            onClick={() => mergeTarget !== '' && setPendingMerge(mergeTarget)}
            disabled={mergeTarget === '' || busy || threadRunning}
            className="primary-button min-h-[44px] w-full"
          >
            {busyKey === MERGE_BUSY_KEY ? (
              <>
                <span className="loading-spinner loading-spinner--small shrink-0" />
                <span>合并中...</span>
              </>
            ) : (
              <span>合并到当前分支</span>
            )}
          </button>
          <p className="px-0.5 text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
            把所选分支合并进 {current || '当前分支'}，产生冲突时需要在 Mac 端解决。
          </p>
        </>
      )}

      <ConfirmDialog
        isOpen={pendingCheckout !== null}
        title="切换分支"
        message={
          pendingCheckout
            ? `当前有未提交的改动。切换到 ${pendingCheckout} 可能失败，或把改动一起带过去，确认继续？`
            : ''
        }
        confirmLabel="切换"
        isLoading={pendingCheckout !== null && busyKey === CHECKOUT_BUSY_KEY}
        onConfirm={() => {
          if (pendingCheckout) void doCheckout(pendingCheckout);
        }}
        onCancel={() => setPendingCheckout(null)}
      />

      <ConfirmDialog
        isOpen={pendingMerge !== null}
        title="合并分支"
        message={
          pendingMerge
            ? `确认把 ${pendingMerge} 合并进 ${current || '当前分支'}？合并会改写当前工作区。`
            : ''
        }
        confirmLabel="合并"
        isLoading={pendingMerge !== null && busyKey === MERGE_BUSY_KEY}
        onConfirm={() => void doMerge()}
        onCancel={() => setPendingMerge(null)}
      />
    </div>
  );
};
