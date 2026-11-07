// 移动端 PR/MR 面板：列出仓库的 PR/MR，展开查看详情，可在浏览器打开创建页面

import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  fetchGitPRCreateURL,
  fetchGitPRDetail,
  fetchGitPRList,
  pushGitBranch,
  type GitExtCaller,
  type RemoteGitPRDetail,
  type RemoteGitPRSummary
} from '../context/remoteGitApi';
import type { RemoteGitStatusResult } from '../types/contract';
import { ConfirmDialog } from './ConfirmDialog';
import {
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ExclamationTriangleIcon,
  EyeIcon
} from './Icons';

export interface GitPRPanelProps {
  threadID: string;
  call: GitExtCaller;
  /** 外层刷新令牌，变化时重新读取列表。 */
  refreshToken?: number;
  /** 当前工作区状态：没有 upstream 或领先远程时先推送再取创建链接。 */
  status?: RemoteGitStatusResult | null;
  /** 推送成功后通知外层刷新工作区与分支列表。 */
  onWorkspaceChanged?: () => void;
  /** 宿主支持 git.pr.createUrl：显示「创建 PR/MR」。 */
  canCreatePR?: boolean;
  /** 宿主支持 git.push：未推送的分支可以先推送。 */
  canPush?: boolean;
  /** 外层其它 Git 写操作占用键，非空时锁定本面板。 */
  busyKey?: string | null;
  /** 通知外层占用/释放 Git 写操作。 */
  onBusyChange?: (key: string | null) => void;
}

const PR_CREATE_BUSY_KEY = 'pr-create';
const PR_PUSH_BUSY_KEY = 'pr-push';
/** 本面板自己发起的占用键：这些键不算外部占用。 */
const OWN_BUSY_KEYS = new Set([PR_CREATE_BUSY_KEY, PR_PUSH_BUSY_KEY]);

function ExternalLinkIcon({ className = 'w-4 h-4' }: { className?: string }) {
  return (
    <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="2" stroke="currentColor">
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M13.5 5.25h5.25v5.25M18.75 5.25 11.25 12.75M18 13.5v4.5A1.5 1.5 0 0 1 16.5 19.5h-10.5A1.5 1.5 0 0 1 4.5 18V7.5A1.5 1.5 0 0 1 6 6h4.5"
      />
    </svg>
  );
}

/** PR/MR 状态的中文说明与配色。 */
function gitExtPRState(state: string, isDraft: boolean): { label: string; className: string } {
  if (isDraft) {
    return {
      label: '草稿',
      className: 'bg-[var(--border)] text-[var(--foreground-muted)]'
    };
  }
  switch (state.trim().toLowerCase()) {
    case 'open':
    case 'opened':
      return {
        label: '打开',
        className: 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400'
      };
    case 'merged':
      return {
        label: '已合并',
        className: 'bg-violet-500/15 text-violet-600 dark:text-violet-400'
      };
    case 'closed':
      return {
        label: '已关闭',
        className: 'bg-red-500/15 text-red-600 dark:text-red-400'
      };
    default:
      return {
        label: state.trim() || '未知',
        className: 'bg-[var(--border)] text-[var(--foreground-muted)]'
      };
  }
}

/** CI 检查状态的中文说明。 */
function gitExtCheckLabel(state: string): string {
  switch (state.trim().toLowerCase()) {
    case 'success':
      return '通过';
    case 'failure':
    case 'failed':
      return '失败';
    case 'pending':
      return '进行中';
    case 'none':
    case '':
      return '无';
    default:
      return state;
  }
}

/** 把 RFC3339 时间显示成短格式，解析不到时退回原文。 */
function gitExtFormatTime(value?: string): string {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  });
}

export const GitPRPanel: React.FC<GitPRPanelProps> = ({
  threadID,
  call,
  refreshToken = 0,
  status = null,
  onWorkspaceChanged,
  canCreatePR = false,
  canPush = false,
  busyKey: externalBusyKey = null,
  onBusyChange
}) => {
  const [items, setItems] = useState<RemoteGitPRSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<number | null>(null);
  const [details, setDetails] = useState<Record<number, RemoteGitPRDetail>>({});
  const [detailLoading, setDetailLoading] = useState<number | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const [localBusy, setLocalBusy] = useState<string | null>(null);
  const [pendingCreate, setPendingCreate] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createNotice, setCreateNotice] = useState<string | null>(null);
  /** 弹窗被浏览器拦截时退化为可点击链接。 */
  const [fallbackURL, setFallbackURL] = useState<string | null>(null);

  // 外层其它 Git 写操作进行中时锁定本面板；自己发起的操作不算外部占用
  const externalBusy = externalBusyKey !== null && !OWN_BUSY_KEYS.has(externalBusyKey);
  const busy = localBusy !== null || externalBusy;
  const creating = localBusy === PR_CREATE_BUSY_KEY || localBusy === PR_PUSH_BUSY_KEY;
  // 没有 upstream 或领先远程时，创建页拿不到可比较的分支，需要先推送
  const needsPush = status !== null && (!status.upstream || status.ahead > 0);

  const setBusy = useCallback(
    (key: string | null) => {
      setLocalBusy(key);
      onBusyChange?.(key);
    },
    [onBusyChange]
  );

  const reqIdRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
    };
  }, []);

  // 读取 PR/MR 列表
  useEffect(() => {
    const reqId = ++reqIdRef.current;
    setLoading(true);
    setLoadError(null);

    void fetchGitPRList(call, threadID)
      .then((result) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setItems(result.items);
        // 列表刷新后详情可能已变，清掉缓存并收起展开项
        setDetails({});
        setExpanded(null);
        setDetailError(null);
      })
      .catch((err: unknown) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setItems([]);
        setDetails({});
        setExpanded(null);
        setLoadError(err instanceof Error ? err.message : '读取 PR/MR 列表失败');
      })
      .finally(() => {
        if (mountedRef.current && reqId === reqIdRef.current) {
          setLoading(false);
        }
      });
  }, [call, refreshToken, reloadToken, threadID]);

  const loadDetail = useCallback(
    async (number: number) => {
      setDetailError(null);
      setDetailLoading(number);
      try {
        const detail = await fetchGitPRDetail(call, threadID, number);
        if (!mountedRef.current) return;
        setDetails((prev) => ({ ...prev, [number]: detail }));
      } catch (err) {
        if (!mountedRef.current) return;
        setDetailError(err instanceof Error ? err.message : '读取 PR/MR 详情失败');
      } finally {
        if (mountedRef.current) {
          setDetailLoading(null);
        }
      }
    },
    [call, threadID]
  );

  const toggleExpand = useCallback(
    (item: RemoteGitPRSummary) => {
      setDetailError(null);
      setExpanded((prev) => (prev === item.number ? null : item.number));
      if (!details[item.number] && detailLoading !== item.number) {
        void loadDetail(item.number);
      }
    },
    [detailLoading, details, loadDetail]
  );

  const openInBrowser = useCallback((url?: string) => {
    if (!url) return;
    window.open(url, '_blank', 'noopener,noreferrer');
  }, []);

  // 取创建链接并在新标签页打开；先推送未推送的分支，与桌面端「推送并建 PR/MR」一致
  const doCreate = useCallback(async () => {
    setPendingCreate(false);
    if (!canCreatePR || busy) return;
    if (needsPush && !canPush) {
      setCreateError('当前分支还没有推送到远程，且宿主不支持推送，请先在 Mac 端推送后再创建。');
      return;
    }
    const willPush = needsPush && canPush;
    setCreateError(null);
    setCreateNotice(null);
    setFallbackURL(null);
    setBusy(willPush ? PR_PUSH_BUSY_KEY : PR_CREATE_BUSY_KEY);
    try {
      if (willPush) {
        await pushGitBranch(call, threadID);
        if (!mountedRef.current) return;
        onWorkspaceChanged?.();
      }
      const result = await fetchGitPRCreateURL(call, threadID);
      if (!mountedRef.current) return;
      // 带 noopener 打开时部分浏览器返回 null，这里先拿句柄再断开 opener
      const win = window.open(result.url, '_blank');
      if (win) {
        try {
          win.opener = null;
        } catch {
          // 跨域时可能不允许赋值，忽略即可
        }
        setCreateNotice(willPush ? '已推送，已在新标签页打开创建页面' : '已在新标签页打开创建页面');
      } else {
        // 手机浏览器拦截弹窗：退化为可点击链接
        setFallbackURL(result.url);
        setCreateNotice('浏览器拦截了新标签页，点击下方链接打开创建页面。');
      }
      // 新建 PR 后列表会变化，顺手刷新一次
      setReloadToken((prev) => prev + 1);
    } catch (err) {
      if (!mountedRef.current) return;
      const reason = err instanceof Error ? err.message : '未知错误';
      setCreateError(willPush ? `已推送，但获取创建链接失败：${reason}` : `获取创建链接失败：${reason}`);
    } finally {
      if (mountedRef.current) {
        setBusy(null);
      }
    }
  }, [busy, call, canCreatePR, canPush, needsPush, onWorkspaceChanged, setBusy, threadID]);

  // 需要先推送时按桌面逻辑二次确认
  const requestCreate = useCallback(() => {
    if (!canCreatePR || busy) return;
    setCreateError(null);
    setCreateNotice(null);
    if (needsPush && canPush) {
      setPendingCreate(true);
      return;
    }
    void doCreate();
  }, [busy, canCreatePR, canPush, doCreate, needsPush]);

  return (
    <div className="space-y-3">
      {canCreatePR && (
        <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-2">
          <div className="flex items-center justify-between gap-2 min-w-0">
            <span className="text-xs font-semibold text-[var(--foreground)] truncate">
              新建 PR/MR
            </span>
            {needsPush && canPush && (
              <span className="px-1.5 py-0.5 rounded text-mc-caption font-semibold bg-amber-500/15 text-amber-600 dark:text-amber-400 shrink-0">
                需先推送
              </span>
            )}
          </div>
          <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
            在浏览器中打开创建页面，标题与正文会一并带上。
          </p>
          <button
            type="button"
            onClick={requestCreate}
            disabled={busy || loading}
            className="primary-button min-h-[44px] w-full"
          >
            {creating ? (
              <>
                <span className="loading-spinner loading-spinner--small shrink-0" />
                <span>{localBusy === PR_PUSH_BUSY_KEY ? '推送并创建中...' : '创建中...'}</span>
              </>
            ) : (
              <>
                <ExternalLinkIcon className="w-4 h-4" />
                <span>{needsPush && canPush ? '推送并建 PR/MR' : '创建 PR/MR'}</span>
              </>
            )}
          </button>

          {createNotice && (
            <div
              className="flex items-start gap-2 px-3 py-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 text-xs text-emerald-600 dark:text-emerald-400"
              role="status"
            >
              <CheckCircleIcon className="w-4 h-4 shrink-0 mt-0.5" />
              <span className="break-words select-text leading-relaxed">{createNotice}</span>
            </div>
          )}

          {fallbackURL && (
            <a
              href={fallbackURL}
              target="_blank"
              rel="noopener noreferrer"
              className="secondary-button min-h-[44px] w-full text-xs"
            >
              <ExternalLinkIcon className="w-3.5 h-3.5 shrink-0" />
              <span className="truncate select-text">{fallbackURL}</span>
            </a>
          )}

          {createError && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{createError}</span>
              </div>
            </div>
          )}
        </div>
      )}

      <div className="flex items-center justify-between gap-2 px-0.5">
        <span className="text-xs text-[var(--foreground-muted)]">
          {loading ? '正在读取...' : `共 ${items.length} 条`}
        </span>
        <button
          type="button"
          onClick={() => setReloadToken((prev) => prev + 1)}
          disabled={loading}
          className="secondary-button min-h-[44px] px-2.5 text-xs"
        >
          刷新列表
        </button>
      </div>

      {loadError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{loadError}</span>
          </div>
          <button
            type="button"
            onClick={() => setReloadToken((prev) => prev + 1)}
            disabled={loading}
            className="inline-alert__action min-h-[44px] px-2 flex items-center"
          >
            重试
          </button>
        </div>
      )}

      {loading && items.length === 0 && (
        <div className="empty-state">
          <span className="loading-spinner mb-1" />
          <h2>正在读取 PR/MR</h2>
          <p className="text-xs text-[var(--foreground-muted)]">
            正在通过 gh / glab 读取仓库的 Pull Request 与 Merge Request...
          </p>
        </div>
      )}

      {!loading && !loadError && items.length === 0 && (
        <div className="empty-state">
          <h2>没有开放的 PR/MR</h2>
          <p className="text-xs text-[var(--foreground-muted)]">
            当前仓库没有可展示的 Pull Request 或 Merge Request。
          </p>
        </div>
      )}

      {items.length > 0 && (
        <ul className="space-y-1.5">
          {items.map((item) => {
            const isExpanded = expanded === item.number;
            const detail = details[item.number];
            const state = gitExtPRState(item.state, item.isDraft);
            return (
              <li
                key={item.number}
                className="rounded-lg border border-[var(--border)] bg-[var(--card)] overflow-hidden"
              >
                <button
                  type="button"
                  onClick={() => toggleExpand(item)}
                  aria-expanded={isExpanded}
                  className="w-full text-left p-2.5 min-h-[44px] flex items-start gap-2"
                >
                  <span className="mt-0.5 shrink-0 text-[var(--foreground-muted)]">
                    {isExpanded ? (
                      <ChevronDownIcon className="w-4 h-4" />
                    ) : (
                      <ChevronRightIcon className="w-4 h-4" />
                    )}
                  </span>
                  <span className="min-w-0 flex-1 space-y-1">
                    <span className="block text-xs font-semibold text-[var(--foreground)] break-words leading-snug">
                      #{item.number} · {item.title || '（无标题）'}
                    </span>
                    <span className="flex flex-wrap items-center gap-1.5 text-mc-caption text-[var(--foreground-muted)]">
                      <span className={`px-1.5 py-0.5 rounded font-semibold ${state.className}`}>
                        {state.label}
                      </span>
                      {item.author && (
                        <span className="select-text truncate max-w-[10rem]">@{item.author}</span>
                      )}
                      <span className="font-mono break-all">
                        {item.headBranch}
                        {item.baseBranch ? ` → ${item.baseBranch}` : ''}
                      </span>
                    </span>
                  </span>
                </button>

                {isExpanded && (
                  <div className="border-t border-[var(--border)] p-2.5 space-y-2">
                    {detailLoading === item.number && !detail && (
                      <div className="flex items-center gap-2 py-1 text-xs text-[var(--foreground-muted)]">
                        <span className="loading-spinner loading-spinner--small shrink-0" />
                        <span>正在读取详情...</span>
                      </div>
                    )}

                    {detailError && detailLoading !== item.number && (
                      <div className="inline-alert inline-alert--danger" role="alert">
                        <div className="flex items-start gap-2 min-w-0 flex-1">
                          <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                          <span className="break-words select-text">{detailError}</span>
                        </div>
                        <button
                          type="button"
                          onClick={() => void loadDetail(item.number)}
                          className="inline-alert__action min-h-[44px] px-2 flex items-center"
                        >
                          重试
                        </button>
                      </div>
                    )}

                    {detail && (
                      <>
                        <div className="flex items-center gap-3 flex-wrap text-mc-caption text-[var(--foreground-muted)]">
                          <span className="text-emerald-600 dark:text-emerald-400">
                            +{detail.additions}
                          </span>
                          <span className="text-red-600 dark:text-red-400">
                            -{detail.deletions}
                          </span>
                          <span>{detail.changedFiles} 个文件</span>
                          {detail.updatedAt && (
                            <span>更新于 {gitExtFormatTime(detail.updatedAt)}</span>
                          )}
                          {detail.reviewDecision && (
                            <span>评审: {detail.reviewDecision}</span>
                          )}
                          {detail.checksState && (
                            <span>检查: {gitExtCheckLabel(detail.checksState)}</span>
                          )}
                        </div>

                        {detail.body && (
                          <p className="select-text text-xs leading-relaxed text-[var(--foreground)] whitespace-pre-wrap break-words">
                            {detail.body}
                          </p>
                        )}

                        {detail.checks.length > 0 && (
                          <div className="space-y-1">
                            <span className="block text-mc-caption font-semibold text-[var(--foreground-muted)]">
                              CI 检查 ({detail.checks.length})
                            </span>
                            <ul className="space-y-0.5">
                              {detail.checks.map((check, idx) => (
                                <li
                                  key={`${check.name}-${idx}`}
                                  className="flex items-center justify-between gap-2 text-mc-caption text-[var(--foreground-muted)]"
                                >
                                  <span className="select-text font-mono truncate min-w-0">
                                    {check.name}
                                  </span>
                                  <span className="shrink-0">{gitExtCheckLabel(check.state)}</span>
                                </li>
                              ))}
                            </ul>
                          </div>
                        )}

                        {detail.comments.length > 0 && (
                          <div className="space-y-1">
                            <span className="block text-mc-caption font-semibold text-[var(--foreground-muted)]">
                              评论 ({detail.comments.length})
                            </span>
                            <ul className="space-y-1">
                              {detail.comments.map((comment, idx) => (
                                <li
                                  key={`${comment.author}-${idx}`}
                                  className="p-2 rounded-lg bg-[var(--screen)] space-y-0.5"
                                >
                                  <span className="block text-mc-caption font-semibold text-[var(--foreground)] truncate">
                                    @{comment.author}
                                    {comment.createdAt
                                      ? ` · ${gitExtFormatTime(comment.createdAt)}`
                                      : ''}
                                  </span>
                                  <span className="select-text block text-mc-caption leading-relaxed text-[var(--foreground)] break-words whitespace-pre-wrap">
                                    {comment.body}
                                  </span>
                                </li>
                              ))}
                            </ul>
                          </div>
                        )}
                      </>
                    )}

                    {item.url ? (
                      <button
                        type="button"
                        onClick={() => openInBrowser(item.url)}
                        className="secondary-button min-h-[44px] w-full text-xs"
                      >
                        <ExternalLinkIcon className="w-3.5 h-3.5" />
                        <span>在浏览器打开</span>
                      </button>
                    ) : (
                      <div className="flex items-center gap-1.5 text-mc-caption text-[var(--foreground-muted)]">
                        <EyeIcon className="w-3.5 h-3.5 shrink-0" />
                        <span>该记录没有可打开的链接</span>
                      </div>
                    )}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--screen)] text-xs text-[var(--foreground-muted)] space-y-1">
        <p className="font-semibold text-[var(--foreground)]">查看与创建</p>
        <p className="leading-relaxed">
          移动端可以查看 PR/MR 详情，并按当前分支在浏览器里创建新的 PR/MR；评审与合并仍需在 Mac 桌面端或网页端完成。
        </p>
      </div>

      <ConfirmDialog
        isOpen={pendingCreate}
        title="推送并建 PR/MR"
        message={`当前分支${status?.branch ? ` ${status.branch}` : ''}还没有推送到远程，需要先推送才能创建 PR/MR。确认推送？`}
        confirmLabel="推送并创建"
        isLoading={creating}
        onConfirm={() => void doCreate()}
        onCancel={() => setPendingCreate(false)}
      />
    </div>
  );
};
