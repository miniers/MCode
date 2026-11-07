// 移动端工作区 Git 抽屉：变更（状态 / 分支 / 分文件提交）、贮藏、PR/MR 三个标签页。
// 每个标签与写入按钮都先按 supportsMethod 判断，宿主未声明能力时隐藏并退回只读视图。

import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import type { RemoteGitBranchesResult, RemoteGitStatusResult } from '../types/contract';
import { useRemote, useRemoteStatic } from '../context/RemoteContext';
import { useRemoteCall } from '../context/useRemoteCall';
import {
  GIT_EXT_METHODS,
  fetchGitRemote,
  pullGitBranch,
  pushGitBranch
} from '../context/remoteGitApi';
import {
  ArrowDownIcon,
  ArrowPathIcon,
  ArrowUpIcon,
  CheckCircleIcon,
  ExclamationTriangleIcon
} from './Icons';
import { GitBranchPanel } from './GitBranchPanel';
import { GitCommitPanel } from './GitCommitPanel';
import { GitStashPanel } from './GitStashPanel';
import { GitPRPanel } from './GitPRPanel';
import { SheetFrame } from './sheet/SheetFrame';
import { MenuSheetHeader } from './thread/menu/MenuSheetHeader';
import { MenuPageChromeContext, useMenuPageChrome } from './thread/menu/menuPageChrome';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';

/** 本抽屉自己发起的远程同步占用键，与子面板的占用键区分开。 */
const SYNC_PUSH_KEY = 'sync-push';
const SYNC_PULL_KEY = 'sync-pull';
const SYNC_FETCH_KEY = 'sync-fetch';

type GitRemoteSyncAction = 'push' | 'pull' | 'fetch';

const GIT_SYNC_LABELS: Record<GitRemoteSyncAction, string> = {
  push: '推送',
  pull: '拉取',
  fetch: '抓取'
};

export interface GitStatusSheetProps {
  isOpen: boolean;
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
}

/** 内容体：供菜单页面栈与兼容外壳复用；不含遮罩、卡片与标题栏。 */
export interface GitStatusSheetBodyProps {
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
}

type GitSheetTab = 'changes' | 'stash' | 'pr';

function BranchIcon({ className = 'w-4 h-4' }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="2" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M6 3v12m0 0a3 3 0 1 0 3 3V9a3 3 0 0 1 3-3h3m0 0a3 3 0 1 0 0-6 3 3 0 0 0 0 6Zm0 0v6a3 3 0 1 0 3 3h-3"
      />
    </svg>
  );
}

interface GitStatusSectionProps {
  result: RemoteGitStatusResult | null;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
}

/** 工作区状态区：分支、上游、领先落后与改动文件列表。 */
const GitStatusSection: React.FC<GitStatusSectionProps> = ({
  result,
  loading,
  error,
  onRetry
}) => (
  <div className="space-y-3">
    <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)] px-0.5">
      <span className="font-semibold text-[var(--foreground)]">工作区状态</span>
    </div>

    {/* 状态错误提示及单独重试 */}
    {error && (
      <div className="inline-alert inline-alert--danger" role="alert">
        <div className="flex items-start gap-2 min-w-0 flex-1">
          <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
          <span className="break-words select-text">{error}</span>
        </div>
        <button
          type="button"
          onClick={onRetry}
          disabled={loading}
          className="inline-alert__action min-h-[44px] px-2 flex items-center"
        >
          重试
        </button>
      </div>
    )}

    {/* 刷新现有数据时的加载提示条 */}
    {loading && result && (
      <div className="flex items-center gap-2 py-1.5 px-3 rounded-lg bg-[var(--border)]/40 text-xs text-[var(--foreground-muted)]">
        <span className="loading-spinner loading-spinner--small shrink-0" />
        <span>正在刷新工作区状态...</span>
      </div>
    )}

    {/* 初次拉取中的空态加载器 */}
    {loading && !result && (
      <div className="empty-state">
        <span className="loading-spinner mb-1" />
        <h2>正在读取 Git 状态</h2>
        <p className="text-xs text-[var(--foreground-muted)]">
          正在与 Mac 宿主通信获取工作区状态...
        </p>
      </div>
    )}

    {/* 无数据且非加载中的空状态 */}
    {!loading && !result && !error && (
      <div className="empty-state">
        <div className="empty-state__icon">
          <BranchIcon className="w-6 h-6" />
        </div>
        <h2>暂无 Git 状态数据</h2>
        <button type="button" onClick={onRetry} className="primary-button min-h-[44px]">
          读取状态
        </button>
      </div>
    )}

    {/* 状态卡主体展示 */}
    {result && (
      <>
        {!result.isRepository ? (
          <div className="p-4 rounded-xl border border-[var(--border)] bg-[var(--card)] text-center space-y-2">
            <div className="mx-auto w-10 h-10 rounded-full flex items-center justify-center bg-[var(--border)]/50 text-[var(--foreground-muted)]">
              <ExclamationTriangleIcon className="w-5 h-5 text-amber-500" />
            </div>
            <h3 className="text-sm font-bold text-[var(--foreground)]">当前工作区不是 Git 仓库</h3>
            <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
              当前会话关联的工作目录未包含 Git 元数据目录，未初始化版本控制。
            </p>
          </div>
        ) : (
          <div className="space-y-3">
            {/* 基础概览卡 */}
            <div className="p-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-3">
              {/* 当前分支与上游分支 */}
              <div className="flex flex-col gap-2">
                <div className="flex items-center justify-between gap-2 min-w-0">
                  <span className="text-xs text-[var(--foreground-muted)] shrink-0">当前分支</span>
                  <span className="select-text font-mono text-xs font-bold text-[var(--foreground)] truncate">
                    {result.branch || '游离 HEAD (detached HEAD)'}
                  </span>
                </div>

                <div className="flex items-center justify-between gap-2 min-w-0">
                  <span className="text-xs text-[var(--foreground-muted)] shrink-0">上游追踪</span>
                  <span className="select-text font-mono text-xs text-[var(--foreground-muted)] truncate">
                    {result.upstream || '未设置上游'}
                  </span>
                </div>

                {result.defaultBranch && (
                  <div className="flex items-center justify-between gap-2 min-w-0">
                    <span className="text-xs text-[var(--foreground-muted)] shrink-0">默认分支</span>
                    <span className="select-text font-mono text-xs text-[var(--foreground-muted)] truncate">
                      {result.defaultBranch}
                    </span>
                  </div>
                )}
              </div>

              {/* 提交领先/落后与工作区干净状态 */}
              <div className="border-t border-[var(--border)] pt-2.5 flex items-center justify-between gap-2 flex-wrap">
                <div className="flex items-center gap-3 text-xs">
                  <span
                    className={`flex items-center gap-1 font-semibold ${
                      result.ahead > 0
                        ? 'text-sky-600 dark:text-sky-400'
                        : 'text-[var(--foreground-muted)]'
                    }`}
                    title="领先上游提交数"
                  >
                    <ArrowUpIcon className="w-3.5 h-3.5" />
                    <span>领先 {result.ahead}</span>
                  </span>

                  <span
                    className={`flex items-center gap-1 font-semibold ${
                      result.behind > 0
                        ? 'text-amber-600 dark:text-amber-400'
                        : 'text-[var(--foreground-muted)]'
                    }`}
                    title="落后上游提交数"
                  >
                    <ArrowDownIcon className="w-3.5 h-3.5" />
                    <span>落后 {result.behind}</span>
                  </span>
                </div>

                <div className="flex items-center gap-1.5 text-xs font-semibold">
                  {result.isClean ? (
                    <span className="flex items-center gap-1 text-emerald-600 dark:text-emerald-400">
                      <CheckCircleIcon className="w-4 h-4" />
                      <span>工作区干净</span>
                    </span>
                  ) : (
                    <span className="flex items-center gap-1 text-amber-600 dark:text-amber-400">
                      <ExclamationTriangleIcon className="w-4 h-4" />
                      <span>存在未提交改动</span>
                    </span>
                  )}
                </div>
              </div>
            </div>

            {/* 截断提示 */}
            {result.truncated && (
              <div className="inline-alert inline-alert--warning" role="alert">
                <div className="flex items-start gap-2 min-w-0">
                  <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--warning-foreground)]" />
                  <span className="text-xs">
                    改动文件列表已截断：当前仅展示部分文件摘要，不作为全部改动数。
                  </span>
                </div>
              </div>
            )}

            {/* 已修改文件列表 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)] px-0.5">
                <span className="font-semibold text-[var(--foreground)]">
                  已修改文件 ({result.modifiedFiles.length})
                </span>
              </div>
              {result.modifiedFiles.length === 0 ? (
                <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                  无修改文件
                </div>
              ) : (
                <div className="space-y-1">
                  {result.modifiedFiles.map((file, idx) => (
                    <div
                      key={`${file}-${idx}`}
                      className="p-2.5 rounded-lg border border-[var(--border)] bg-[var(--card)] select-text font-mono text-xs break-all whitespace-pre-wrap leading-relaxed text-[var(--foreground)]"
                    >
                      {file}
                    </div>
                  ))}
                </div>
              )}
            </div>

            {/* 未跟踪文件列表 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)] px-0.5">
                <span className="font-semibold text-[var(--foreground)]">
                  未跟踪文件 ({result.untrackedFiles.length})
                </span>
              </div>
              {result.untrackedFiles.length === 0 ? (
                <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                  无未跟踪文件
                </div>
              ) : (
                <div className="space-y-1">
                  {result.untrackedFiles.map((file, idx) => (
                    <div
                      key={`${file}-${idx}`}
                      className="p-2.5 rounded-lg border border-[var(--border)] bg-[var(--card)] select-text font-mono text-xs break-all whitespace-pre-wrap leading-relaxed text-[var(--foreground)]"
                    >
                      {file}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </>
    )}
  </div>
);

interface GitBranchesSectionProps {
  result: RemoteGitBranchesResult | null;
  loading: boolean;
  error: string | null;
  /** 状态区拿到的当前分支，分支接口未返回 current 时作为兜底。 */
  currentBranchFallback?: string;
  onRetry: () => void;
  /** 分支写操作入口：宿主支持切换/新建/合并时传入。 */
  onManageBranches?: () => void;
}

/** 分支区：本地分支与远程分支的列表，宿主支持时提供切换/新建/合并入口。 */
const GitBranchesSection: React.FC<GitBranchesSectionProps> = ({
  result,
  loading,
  error,
  currentBranchFallback,
  onRetry,
  onManageBranches
}) => (
  <div className="space-y-3">
    <div className="flex items-center justify-between gap-2 text-xs text-[var(--foreground-muted)] px-0.5">
      <span className="font-semibold text-[var(--foreground)]">分支列表</span>
      {onManageBranches && (
        <button
          type="button"
          onClick={onManageBranches}
          className="secondary-button min-h-[44px] px-2.5 text-xs shrink-0"
        >
          管理分支
        </button>
      )}
    </div>

    {/* 分支错误提示及单独重试 */}
    {error && (
      <div className="inline-alert inline-alert--danger" role="alert">
        <div className="flex items-start gap-2 min-w-0 flex-1">
          <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
          <span className="break-words select-text">{error}</span>
        </div>
        <button
          type="button"
          onClick={onRetry}
          disabled={loading}
          className="inline-alert__action min-h-[44px] px-2 flex items-center"
        >
          重试
        </button>
      </div>
    )}

    {/* 刷新现有数据时的加载提示条 */}
    {loading && result && (
      <div className="flex items-center gap-2 py-1.5 px-3 rounded-lg bg-[var(--border)]/40 text-xs text-[var(--foreground-muted)]">
        <span className="loading-spinner loading-spinner--small shrink-0" />
        <span>正在刷新分支列表...</span>
      </div>
    )}

    {/* 初次拉取中的空态加载器 */}
    {loading && !result && (
      <div className="empty-state">
        <span className="loading-spinner mb-1" />
        <h2>正在读取分支列表</h2>
        <p className="text-xs text-[var(--foreground-muted)]">正在获取本地分支与远程分支...</p>
      </div>
    )}

    {/* 无数据且非加载中的空状态 */}
    {!loading && !result && !error && (
      <div className="empty-state">
        <div className="empty-state__icon">
          <BranchIcon className="w-6 h-6" />
        </div>
        <h2>暂无分支数据</h2>
        <button type="button" onClick={onRetry} className="primary-button min-h-[44px]">
          读取分支
        </button>
      </div>
    )}

    {/* 分支列表主体展示 */}
    {result && (
      <>
        {!result.isRepository ? (
          <div className="p-4 rounded-xl border border-[var(--border)] bg-[var(--card)] text-center space-y-2">
            <div className="mx-auto w-10 h-10 rounded-full flex items-center justify-center bg-[var(--border)]/50 text-[var(--foreground-muted)]">
              <ExclamationTriangleIcon className="w-5 h-5 text-amber-500" />
            </div>
            <h3 className="text-sm font-bold text-[var(--foreground)]">当前工作区不是 Git 仓库</h3>
            <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
              当前会话关联的工作目录不是 Git 仓库，无法列出分支。
            </p>
          </div>
        ) : (
          <div className="space-y-3">
            {/* 分支截断提示 */}
            {result.truncated && (
              <div className="inline-alert inline-alert--warning" role="alert">
                <div className="flex items-start gap-2 min-w-0">
                  <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--warning-foreground)]" />
                  <span className="text-xs">
                    分支列表已截断：分支数量较多，仅展示部分返回结果。
                  </span>
                </div>
              </div>
            )}

            {/* 本地分支 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)] px-0.5">
                <span className="font-semibold text-[var(--foreground)]">
                  本地分支 ({result.local.length})
                </span>
              </div>
              {result.local.length === 0 ? (
                <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                  暂无本地分支
                </div>
              ) : (
                <div className="space-y-1">
                  {result.local.map((branchName) => {
                    const currentBranch = result.current || currentBranchFallback;
                    const isCurrent = branchName === currentBranch;

                    return (
                      <div
                        key={branchName}
                        className={`p-2.5 rounded-lg border flex items-center justify-between gap-2 ${
                          isCurrent
                            ? 'border-[var(--primary)] bg-[var(--card-alt,var(--card))]'
                            : 'border-[var(--border)] bg-[var(--card)]'
                        }`}
                      >
                        <div className="flex items-center gap-2 min-w-0 flex-1">
                          <BranchIcon
                            className={`w-3.5 h-3.5 shrink-0 ${
                              isCurrent ? 'text-[var(--primary)]' : 'text-[var(--foreground-muted)]'
                            }`}
                          />
                          <span
                            className={`select-text font-mono text-xs break-all leading-snug ${
                              isCurrent
                                ? 'font-bold text-[var(--foreground)]'
                                : 'text-[var(--foreground)]'
                            }`}
                          >
                            {branchName}
                          </span>
                        </div>
                        {isCurrent && (
                          <span className="px-2 py-0.5 rounded text-mc-caption font-semibold bg-[var(--primary)] text-white shrink-0">
                            当前
                          </span>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>

            {/* 远程分支 */}
            <div className="space-y-1.5">
              <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)] px-0.5">
                <span className="font-semibold text-[var(--foreground)]">
                  远程分支 ({result.remote.length})
                </span>
              </div>
              {result.remote.length === 0 ? (
                <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                  暂无远程分支
                </div>
              ) : (
                <div className="space-y-1">
                  {result.remote.map((remoteBranch) => (
                    <div
                      key={remoteBranch}
                      className="p-2.5 rounded-lg border border-[var(--border)] bg-[var(--card)] flex items-center gap-2"
                    >
                      <BranchIcon className="w-3.5 h-3.5 shrink-0 text-[var(--foreground-muted)]" />
                      <span className="select-text font-mono text-xs break-all leading-snug text-[var(--foreground-muted)]">
                        {remoteBranch}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>

            {/* 写操作说明：宿主支持分支操作时只提示仍有边界的能力 */}
            <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--screen)] text-xs text-[var(--foreground-muted)] space-y-1">
              <p className="font-semibold text-[var(--foreground)]">查看与提交</p>
              <p className="leading-relaxed">
                {onManageBranches
                  ? '移动端支持切换、新建与合并分支，以及分文件提交与贮藏；推送、拉取与抓取在状态区操作。'
                  : '移动端支持查看分支、分文件提交与贮藏。分支切换、新建、推送等操作需在 Mac 桌面端完成。'}
              </p>
            </div>
          </div>
        )}
      </>
    )}
  </div>
);

export const GitStatusSheetBody = React.forwardRef<ThreadMenuPageHandle, GitStatusSheetBodyProps>(
  function GitStatusSheetBody({ onClose, threadID }, ref) {
  const {
    capabilities,
    connectionState,
    connectionMessage,
    getThreadGitStatus,
    getThreadGitBranches
  } = useRemoteStatic();
  const { threads, currentThread } = useRemote();
  const { call, supportsMethod } = useRemoteCall();
  const { setTrailing } = useMenuPageChrome();

  useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

  const [activeTab, setActiveTab] = useState<GitSheetTab>('changes');
  /** 分支子页：宿主支持切换/新建/合并时从分支区进入。 */
  const [branchPanelOpen, setBranchPanelOpen] = useState(false);
  /** 远程同步（推送/拉取/抓取）与子面板共用的写操作占用键。 */
  const [writeBusyKey, setWriteBusyKey] = useState<string | null>(null);
  const [syncNotice, setSyncNotice] = useState<string | null>(null);
  const [syncError, setSyncError] = useState<string | null>(null);

  const [statusResult, setStatusResult] = useState<RemoteGitStatusResult | null>(null);
  const [statusLoading, setStatusLoading] = useState(false);
  const [statusError, setStatusError] = useState<string | null>(null);

  const [branchesResult, setBranchesResult] = useState<RemoteGitBranchesResult | null>(null);
  const [branchesLoading, setBranchesLoading] = useState(false);
  const [branchesError, setBranchesError] = useState<string | null>(null);

  const [refreshToken, setRefreshToken] = useState(0);

  const statusReqIdRef = useRef(0);
  const branchesReqIdRef = useRef(0);
  const mountedRef = useRef(true);

  const hasGitFeature = Boolean(capabilities?.features?.includes('git.readonly'));
  const hasStatusMethod = Boolean(capabilities?.methods?.includes('git.status'));
  const hasBranchesMethod = Boolean(capabilities?.methods?.includes('git.branches'));
  const isConnected = connectionState === 'connected';

  // 扩展方法的可用性：不支持就隐藏对应标签与按钮，整体退回只读视图
  const canCommit = supportsMethod('git.changedFiles') && supportsMethod('git.commitSelected');
  const canStash = supportsMethod('git.stash.list');
  const canPR = supportsMethod('git.pr.list');
  const canPush = supportsMethod(GIT_EXT_METHODS.push);
  const canPull = supportsMethod(GIT_EXT_METHODS.pull);
  const canFetch = supportsMethod(GIT_EXT_METHODS.fetch);
  const canCheckout = supportsMethod(GIT_EXT_METHODS.checkout);
  const canCreateBranch = supportsMethod(GIT_EXT_METHODS.createBranch);
  const canMerge = supportsMethod(GIT_EXT_METHODS.merge);
  const canGenerateMessage = supportsMethod(GIT_EXT_METHODS.generateCommitMessage);
  const canCreatePR = supportsMethod(GIT_EXT_METHODS.prCreateUrl);
  const canManageBranches = canCheckout || canCreateBranch || canMerge;
  const showSyncActions = canPush || canPull || canFetch;
  const showTabBar = canStash || canPR;

  // 会话运行中：拉取、切换、新建与合并都会改写工作区，按桌面逻辑禁用
  const threadRunning = useMemo(() => {
    const summary =
      currentThread && currentThread.id === threadID
        ? currentThread
        : threads.find((item) => item.id === threadID);
    return summary?.isRunning ?? false;
  }, [currentThread, threadID, threads]);

  const effectiveTab: GitSheetTab =
    (activeTab === 'stash' && !canStash) || (activeTab === 'pr' && !canPR) ? 'changes' : activeTab;

  // 读取 Git 状态，使用代际防止慢响应串会话
  const fetchStatus = useCallback(async () => {
    if (!hasGitFeature || !hasStatusMethod) {
      setStatusError('当前宿主未声明 Git 状态能力 (git.readonly / git.status)');
      return;
    }

    if (!isConnected) {
      setStatusError(`未连接到 Mac 宿主（当前连接：${connectionMessage || connectionState}）`);
      return;
    }

    const reqId = ++statusReqIdRef.current;
    setStatusLoading(true);
    setStatusError(null);

    try {
      const result = await getThreadGitStatus(threadID);
      if (!mountedRef.current || reqId !== statusReqIdRef.current) return;
      setStatusResult(result);
    } catch (err) {
      if (!mountedRef.current || reqId !== statusReqIdRef.current) return;
      setStatusError(err instanceof Error ? err.message : '获取 Git 状态失败');
    } finally {
      if (mountedRef.current && reqId === statusReqIdRef.current) {
        setStatusLoading(false);
      }
    }
  }, [
    connectionMessage,
    connectionState,
    getThreadGitStatus,
    hasGitFeature,
    hasStatusMethod,
    isConnected,
    threadID
  ]);

  // 读取 Git 分支，使用独立代际允许单项重试
  const fetchBranches = useCallback(async () => {
    if (!hasGitFeature || !hasBranchesMethod) {
      setBranchesError('当前宿主未声明 Git 分支能力 (git.readonly / git.branches)');
      return;
    }

    if (!isConnected) {
      setBranchesError(`未连接到 Mac 宿主（当前连接：${connectionMessage || connectionState}）`);
      return;
    }

    const reqId = ++branchesReqIdRef.current;
    setBranchesLoading(true);
    setBranchesError(null);

    try {
      const result = await getThreadGitBranches(threadID);
      if (!mountedRef.current || reqId !== branchesReqIdRef.current) return;
      setBranchesResult(result);
    } catch (err) {
      if (!mountedRef.current || reqId !== branchesReqIdRef.current) return;
      setBranchesError(err instanceof Error ? err.message : '获取 Git 分支失败');
    } finally {
      if (mountedRef.current && reqId === branchesReqIdRef.current) {
        setBranchesLoading(false);
      }
    }
  }, [
    connectionMessage,
    connectionState,
    getThreadGitBranches,
    hasBranchesMethod,
    hasGitFeature,
    isConnected,
    threadID
  ]);

  // 刷新按钮：重读状态与分支，并让贮藏/PR 面板重新拉取
  const handleRefreshAll = useCallback(() => {
    void fetchStatus();
    void fetchBranches();
    setRefreshToken((prev) => prev + 1);
  }, [fetchBranches, fetchStatus]);

  // 远程同步：成功后刷新状态与分支，失败直接展示宿主返回的原文
  const handleRemoteSync = useCallback(
    async (action: GitRemoteSyncAction) => {
      if (writeBusyKey !== null) return;
      const busyKey =
        action === 'push' ? SYNC_PUSH_KEY : action === 'pull' ? SYNC_PULL_KEY : SYNC_FETCH_KEY;
      setWriteBusyKey(busyKey);
      setSyncError(null);
      setSyncNotice(null);
      try {
        if (action === 'push') {
          await pushGitBranch(call, threadID);
        } else if (action === 'pull') {
          await pullGitBranch(call, threadID);
        } else {
          await fetchGitRemote(call, threadID);
        }
        if (!mountedRef.current) return;
        setSyncNotice(
          action === 'push'
            ? '已推送到远程'
            : action === 'pull'
              ? '已拉取远程提交（快进）'
              : '已抓取远程更新'
        );
        handleRefreshAll();
      } catch (err) {
        if (!mountedRef.current) return;
        setSyncError(
          `${GIT_SYNC_LABELS[action]}失败：${err instanceof Error ? err.message : '未知错误'}`
        );
      } finally {
        if (mountedRef.current) setWriteBusyKey(null);
      }
    },
    [call, handleRefreshAll, threadID, writeBusyKey]
  );

  // 打开分支子页时重新拉取分支，避免展示陈旧列表
  const openBranchPanel = useCallback(() => {
    setBranchPanelOpen(true);
    setSyncError(null);
    setSyncNotice(null);
    void fetchBranches();
  }, [fetchBranches]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      statusReqIdRef.current += 1;
      branchesReqIdRef.current += 1;
    };
  }, []);

  // 挂载即拉取数据；卸载由 mountedRef 与请求代际作废在途响应
  useEffect(() => {
    void fetchStatus();
    void fetchBranches();
  }, [fetchBranches, fetchStatus]);

  // 标题栏刷新按钮由内容体上报，loading 变化时重建以驱动图标旋转
  useEffect(() => {
    setTrailing?.(
      <button
        type="button"
        onClick={handleRefreshAll}
        disabled={statusLoading || branchesLoading}
        aria-label="刷新 Git 数据"
        title="刷新 Git 数据"
        className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
      >
        <ArrowPathIcon
          className={`h-4 w-4 ${
            statusLoading || branchesLoading ? 'animate-spin text-[var(--primary)]' : ''
          }`}
        />
      </button>
    );
    return () => setTrailing?.(null);
  }, [branchesLoading, handleRefreshAll, setTrailing, statusLoading]);

  // 断连时作废旧请求并清除加载态，避免并发风暴
  useEffect(() => {
    if (!isConnected) {
      statusReqIdRef.current += 1;
      branchesReqIdRef.current += 1;
      setStatusLoading(false);
      setBranchesLoading(false);
    }
  }, [isConnected]);

  const changesCount =
    statusResult && statusResult.isRepository
      ? statusResult.modifiedFiles.length + statusResult.untrackedFiles.length
      : 0;

  // 远程同步按钮的可用条件：推送需要当前分支，拉取需要能快进
  const repoStatus = statusResult && statusResult.isRepository ? statusResult : null;
  const hasUpstream = Boolean(repoStatus?.upstream);
  const fastForwardReady =
    hasUpstream && (repoStatus?.ahead ?? 0) === 0 && (repoStatus?.behind ?? 0) > 0;
  const syncBusy = writeBusyKey !== null;

  return (
    <div className="flex flex-col flex-1 min-h-0 min-w-0">
        {/* 分区切换选项卡：宿主未支持贮藏与 PR 时不显示，整体退回只读视图 */}
        {showTabBar && (
          <div className="shrink-0 mb-3" role="tablist" aria-label="Git 面板分区">
            <div className="grid grid-cols-3 gap-1.5 p-1 rounded-xl border border-[var(--border)] bg-[var(--screen)]">
              <button
                type="button"
                role="tab"
                aria-selected={effectiveTab === 'changes'}
                onClick={() => setActiveTab('changes')}
                className={`min-h-[44px] px-2 py-1.5 rounded-lg text-xs font-semibold flex items-center justify-center gap-1.5 transition ${
                  effectiveTab === 'changes'
                    ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                    : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                }`}
              >
                <span>变更</span>
                {changesCount > 0 && (
                  <span className="px-1.5 py-0.5 rounded-full text-mc-caption bg-amber-500/15 text-amber-600 dark:text-amber-400 font-medium">
                    {changesCount}
                  </span>
                )}
              </button>
              {canStash && (
                <button
                  type="button"
                  role="tab"
                  aria-selected={effectiveTab === 'stash'}
                  onClick={() => setActiveTab('stash')}
                  className={`min-h-[44px] px-2 py-1.5 rounded-lg text-xs font-semibold flex items-center justify-center gap-1.5 transition ${
                    effectiveTab === 'stash'
                      ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                      : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                  }`}
                >
                  贮藏
                </button>
              )}
              {canPR && (
                <button
                  type="button"
                  role="tab"
                  aria-selected={effectiveTab === 'pr'}
                  onClick={() => setActiveTab('pr')}
                  className={`min-h-[44px] px-2 py-1.5 rounded-lg text-xs font-semibold flex items-center justify-center gap-1.5 transition ${
                    effectiveTab === 'pr'
                      ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                      : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                  }`}
                >
                  PR/MR
                </button>
              )}
            </div>
          </div>
        )}

        {/* 主体滚动展示区 */}
        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          {effectiveTab === 'changes' &&
            (branchPanelOpen && canManageBranches ? (
              <GitBranchPanel
                threadID={threadID}
                call={call}
                branches={branchesResult}
                branchesLoading={branchesLoading}
                branchesError={branchesError}
                onReloadBranches={() => void fetchBranches()}
                currentBranch={statusResult?.branch}
                hasUncommittedChanges={statusResult ? !statusResult.isClean : false}
                threadRunning={threadRunning}
                canCheckout={canCheckout}
                canCreateBranch={canCreateBranch}
                canMerge={canMerge}
                busyKey={writeBusyKey}
                onBusyChange={setWriteBusyKey}
                onChanged={handleRefreshAll}
                onBack={() => setBranchPanelOpen(false)}
              />
            ) : (
              <>
                <GitStatusSection
                  result={statusResult}
                  loading={statusLoading}
                  error={statusError}
                  onRetry={() => void fetchStatus()}
                />

                {/* 远程同步：推送 / 拉取（快进）/ 抓取 */}
                {showSyncActions && repoStatus && (
                  <div className="p-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-2.5">
                    <div className="flex items-center justify-between gap-2 min-w-0">
                      <span className="text-xs font-semibold text-[var(--foreground)] shrink-0">
                        远程同步
                      </span>
                      <span className="min-w-0 text-mc-caption text-[var(--foreground-muted)] truncate">
                        <span className="select-text font-mono">
                          {repoStatus.branch || '游离 HEAD'}
                        </span>
                        {hasUpstream
                          ? ` · 领先 ${repoStatus.ahead} / 落后 ${repoStatus.behind}`
                          : ' · 未设置上游'}
                      </span>
                    </div>

                    <div className="flex items-center gap-1.5">
                      {canFetch && (
                        <button
                          type="button"
                          onClick={() => void handleRemoteSync('fetch')}
                          disabled={syncBusy}
                          className="secondary-button flex-1 min-h-[44px] px-2 text-xs"
                        >
                          {writeBusyKey === SYNC_FETCH_KEY ? (
                            <span className="loading-spinner loading-spinner--small shrink-0" />
                          ) : (
                            <ArrowPathIcon className="w-3.5 h-3.5 shrink-0" />
                          )}
                          <span>{writeBusyKey === SYNC_FETCH_KEY ? '抓取中' : '抓取'}</span>
                        </button>
                      )}
                      {canPull && (
                        <button
                          type="button"
                          onClick={() => void handleRemoteSync('pull')}
                          disabled={syncBusy || threadRunning || !fastForwardReady}
                          className="secondary-button flex-1 min-h-[44px] px-2 text-xs"
                        >
                          {writeBusyKey === SYNC_PULL_KEY ? (
                            <span className="loading-spinner loading-spinner--small shrink-0" />
                          ) : (
                            <ArrowDownIcon className="w-3.5 h-3.5 shrink-0" />
                          )}
                          <span>{writeBusyKey === SYNC_PULL_KEY ? '拉取中' : '拉取'}</span>
                        </button>
                      )}
                      {canPush && (
                        <button
                          type="button"
                          onClick={() => void handleRemoteSync('push')}
                          disabled={syncBusy || !repoStatus.branch}
                          className="secondary-button flex-1 min-h-[44px] px-2 text-xs"
                        >
                          {writeBusyKey === SYNC_PUSH_KEY ? (
                            <span className="loading-spinner loading-spinner--small shrink-0" />
                          ) : (
                            <ArrowUpIcon className="w-3.5 h-3.5 shrink-0" />
                          )}
                          <span>{writeBusyKey === SYNC_PUSH_KEY ? '推送中' : '推送'}</span>
                        </button>
                      )}
                    </div>

                    {canPull && !hasUpstream && (
                      <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                        当前分支还没有上游分支：先推送一次建立跟踪，之后才能快进拉取。
                      </p>
                    )}
                    {canPull && hasUpstream && !fastForwardReady && (
                      <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                        {repoStatus.ahead > 0
                          ? '本地有未推送的提交，需要先推送才能快进拉取。'
                          : '没有需要拉取的远程提交。'}
                      </p>
                    )}
                    {canPull && threadRunning && (
                      <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                        会话运行中，拉取会改写工作区，请先停止当前运行。
                      </p>
                    )}

                    {syncNotice && (
                      <div
                        className="flex items-start gap-2 px-3 py-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 text-xs text-emerald-600 dark:text-emerald-400"
                        role="status"
                      >
                        <CheckCircleIcon className="w-4 h-4 shrink-0 mt-0.5" />
                        <span className="break-words select-text leading-relaxed">
                          {syncNotice}
                        </span>
                      </div>
                    )}

                    {syncError && (
                      <div className="inline-alert inline-alert--danger" role="alert">
                        <div className="flex items-start gap-2 min-w-0">
                          <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                          <span className="break-words select-text">{syncError}</span>
                        </div>
                      </div>
                    )}
                  </div>
                )}

                {canCommit && statusResult?.isRepository && (
                  <GitCommitPanel
                    threadID={threadID}
                    call={call}
                    onCommitted={handleRefreshAll}
                    refreshToken={refreshToken}
                    canPush={canPush}
                    canGenerateMessage={canGenerateMessage}
                    busyKey={writeBusyKey}
                    onBusyChange={setWriteBusyKey}
                  />
                )}

                <GitBranchesSection
                  result={branchesResult}
                  loading={branchesLoading}
                  error={branchesError}
                  currentBranchFallback={statusResult?.branch}
                  onRetry={() => void fetchBranches()}
                  onManageBranches={canManageBranches ? openBranchPanel : undefined}
                />
              </>
            ))}

          {effectiveTab === 'stash' && canStash && (
            <GitStashPanel
              threadID={threadID}
              call={call}
              onWorkspaceChanged={handleRefreshAll}
              refreshToken={refreshToken}
            />
          )}

          {effectiveTab === 'pr' && canPR && (
            <GitPRPanel
              threadID={threadID}
              call={call}
              refreshToken={refreshToken}
              status={statusResult}
              onWorkspaceChanged={handleRefreshAll}
              canCreatePR={canCreatePR}
              canPush={canPush}
              busyKey={writeBusyKey}
              onBusyChange={setWriteBusyKey}
            />
          )}
        </div>

        {/* 底部操作与关闭栏 */}
        <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 pb-0 min-w-0 max-w-full w-full flex items-center justify-between gap-2">
          <div className="text-xs text-[var(--foreground-muted)] truncate min-w-0 flex-1">
            {statusResult?.isRepository ? (
              <span className="truncate">
                {statusResult.branch ? `分支: ${statusResult.branch}` : '游离 HEAD'}
              </span>
            ) : (
              <span>{showTabBar || canCommit || showSyncActions ? 'Git 面板' : '只读面板'}</span>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <button
              type="button"
              onClick={handleRefreshAll}
              disabled={statusLoading || branchesLoading}
              className="secondary-button min-h-[44px]"
            >
              <ArrowPathIcon
                className={`h-4 w-4 ${statusLoading || branchesLoading ? 'animate-spin' : ''}`}
              />
              <span>刷新</span>
            </button>
            <button
              type="button"
              onClick={onClose}
              className="primary-button min-h-[44px] min-w-[72px]"
            >
              关闭
            </button>
          </div>
        </div>
    </div>
  );
  }
);

export const GitStatusSheet: React.FC<GitStatusSheetProps> = ({
  isOpen,
  onClose,
  threadID,
  threadTitle
}) => {
  const bodyRef = useRef<ThreadMenuPageHandle | null>(null);
  const [trailing, setTrailing] = useState<React.ReactNode>(null);

  const handleRequestClose = useCallback(() => {
    if (bodyRef.current?.requestClose() === false) return;
    onClose();
  }, [onClose]);

  if (!isOpen) return null;

  return (
    <SheetFrame onRequestClose={handleRequestClose} labelledBy="git-sheet-title" size="panel">
      <MenuPageChromeContext.Provider
        value={{
          leading: 'close',
          onLeading: handleRequestClose,
          onClose: handleRequestClose,
          setTrailing
        }}
      >
        <MenuSheetHeader
          eyebrow={threadTitle ? `Git · ${threadTitle}` : '工作区 Git'}
          title="Git 工作区"
          labelledBy="git-sheet-title"
          leading="close"
          onLeading={handleRequestClose}
          onClose={handleRequestClose}
          trailing={trailing}
        />
        <GitStatusSheetBody
          ref={bodyRef}
          onClose={onClose}
          threadID={threadID}
          threadTitle={threadTitle}
        />
      </MenuPageChromeContext.Provider>
    </SheetFrame>
  );
};
