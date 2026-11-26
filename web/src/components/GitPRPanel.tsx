// 移动端 PR/MR 面板：列表行与详情页切换，支持在浏览器创建或打开 PR/MR

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
import { formatRelativeTime } from '../utils/date';
import { ConfirmDialog } from './ConfirmDialog';
import {
  ArrowLeftIcon,
  ArrowPathIcon,
  CheckCircleIcon,
  ChevronRightIcon,
  CircleCheckIcon,
  CircleXIcon,
  ExclamationTriangleIcon,
  HalfRingIcon,
  PencilIcon,
  PlusIcon,
  SparklesIcon,
  UserRoundCheckIcon
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
  /** 宿主支持 git.pr.createUrl：显示「创建 PR」按钮。 */
  canCreatePR?: boolean;
  /** 宿主支持 git.push：未推送的分支可以先推送。 */
  canPush?: boolean;
  /** 外层其它 Git 写操作占用键，非空时锁定本面板。 */
  busyKey?: string | null;
  /** 通知外层占用/释放 Git 写操作。 */
  onBusyChange?: (key: string | null) => void;
}

export type PRStatusStripTone = 'success' | 'danger' | 'neutral' | 'plan';

const PR_CREATE_BUSY_KEY = 'pr-create';
const PR_PUSH_BUSY_KEY = 'pr-push';
/** 本面板自己发起的占用键：这些键不算外部占用。 */
const OWN_BUSY_KEYS = new Set([PR_CREATE_BUSY_KEY, PR_PUSH_BUSY_KEY]);

type NormalizedCheckKind = 'success' | 'failure' | 'pending' | 'none';

function normalizeCheckKind(state?: string): NormalizedCheckKind {
  switch ((state ?? '').trim().toLowerCase()) {
    case 'success':
      return 'success';
    case 'failure':
    case 'failed':
      return 'failure';
    case 'pending':
      return 'pending';
    default:
      return 'none';
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

/** 详情顶部状态条的判定（按优先级从高到低收敛为单条结论）。 */
export function prStatusStrip(detail: RemoteGitPRDetail): {
  tone: PRStatusStripTone;
  text: string;
} {
  const normalizedState = detail.state.trim().toLowerCase();
  if (normalizedState === 'merged') {
    return { tone: 'plan', text: `已合并到 ${detail.baseBranch}` };
  }
  if (normalizedState === 'closed') {
    return { tone: 'neutral', text: '已关闭' };
  }
  if (detail.isDraft) {
    return { tone: 'neutral', text: '草稿' };
  }
  if (detail.mergeable === 'CONFLICTING') {
    return { tone: 'danger', text: '有冲突' };
  }

  let failedCount = 0;
  let pendingCount = 0;
  for (const check of detail.checks) {
    const kind = normalizeCheckKind(check.state);
    if (kind === 'failure') failedCount += 1;
    else if (kind === 'pending') pendingCount += 1;
  }

  if (failedCount > 0) {
    return { tone: 'danger', text: `${failedCount} 项检查失败` };
  }
  if (pendingCount > 0) {
    return { tone: 'neutral', text: `${pendingCount} 项检查进行中` };
  }

  const decision = (detail.reviewDecision ?? '').trim().toUpperCase();
  if (decision === 'CHANGES_REQUESTED') {
    return { tone: 'danger', text: '需要修改' };
  }
  if (decision === 'REVIEW_REQUIRED') {
    return { tone: 'neutral', text: '等待评审' };
  }

  return { tone: 'success', text: '可以合并' };
}

function statusDotClass(state: string, isDraft: boolean): string {
  const normalized = state.trim().toLowerCase();
  if (normalized === 'merged') return 'bg-mc-plan';
  if (isDraft || normalized === 'closed') return 'bg-mc-text-secondary';
  if (normalized === 'open' || normalized === 'opened') return 'bg-mc-success';
  return 'bg-mc-text-secondary';
}

function renderCIIcon(checksState?: string): React.ReactNode {
  const kind = normalizeCheckKind(checksState);
  if (kind === 'success') {
    return (
      <span
        className="inline-flex items-center text-mc-success"
        title="CI 通过"
        aria-label="CI 通过"
        data-ci-state="success"
      >
        <CircleCheckIcon className="w-3.5 h-3.5" />
      </span>
    );
  }
  if (kind === 'failure') {
    return (
      <span
        className="inline-flex items-center text-mc-danger"
        title="CI 失败"
        aria-label="CI 失败"
        data-ci-state="failure"
      >
        <CircleXIcon className="w-3.5 h-3.5" />
      </span>
    );
  }
  if (kind === 'pending') {
    return (
      <span
        className="inline-flex items-center text-mc-warning"
        title="CI 进行中"
        aria-label="CI 进行中"
        data-ci-state="pending"
      >
        <HalfRingIcon className="w-3.5 h-3.5" />
      </span>
    );
  }
  return null;
}

function renderReviewIcon(reviewDecision?: string): React.ReactNode {
  const decision = (reviewDecision ?? '').trim().toUpperCase();
  if (decision === 'APPROVED') {
    return (
      <span
        className="inline-flex items-center text-mc-success"
        title="已批准"
        aria-label="已批准"
        data-review-decision="APPROVED"
      >
        <UserRoundCheckIcon className="w-3.5 h-3.5" />
      </span>
    );
  }
  if (decision === 'CHANGES_REQUESTED') {
    return (
      <span
        className="inline-flex items-center text-mc-warning"
        title="需要修改"
        aria-label="需要修改"
        data-review-decision="CHANGES_REQUESTED"
      >
        <PencilIcon className="w-3.5 h-3.5" />
      </span>
    );
  }
  return null;
}

function renderStripIcon(detail: RemoteGitPRDetail, tone: PRStatusStripTone): React.ReactNode {
  const state = detail.state.trim().toLowerCase();
  if (state === 'merged' || tone === 'success') {
    return <CircleCheckIcon className="w-4 h-4 shrink-0" />;
  }
  if (state === 'closed' || detail.isDraft) {
    return <span className="w-[7px] h-[7px] rounded-full bg-current shrink-0" />;
  }
  if (detail.mergeable === 'CONFLICTING') {
    return <ExclamationTriangleIcon className="w-4 h-4 shrink-0" />;
  }
  if (detail.checks.some((c) => normalizeCheckKind(c.state) === 'failure')) {
    return <CircleXIcon className="w-4 h-4 shrink-0" />;
  }
  if (detail.checks.some((c) => normalizeCheckKind(c.state) === 'pending')) {
    return <HalfRingIcon className="w-4 h-4 shrink-0" />;
  }
  return <PencilIcon className="w-4 h-4 shrink-0" />;
}

function stripToneStyle(tone: PRStatusStripTone): React.CSSProperties {
  switch (tone) {
    case 'success':
      return {
        backgroundColor: 'color-mix(in srgb, var(--mc-success) 12%, transparent)',
        color: 'var(--mc-success)'
      };
    case 'danger':
      return {
        backgroundColor: 'color-mix(in srgb, var(--mc-danger) 12%, transparent)',
        color: 'var(--mc-danger)'
      };
    case 'plan':
      return {
        backgroundColor: 'color-mix(in srgb, var(--mc-plan) 12%, transparent)',
        color: 'var(--mc-plan)'
      };
    case 'neutral':
    default:
      return {
        backgroundColor: 'var(--mc-overlay-hover)',
        color: 'var(--mc-text-secondary)'
      };
  }
}

export interface GitPRListRowProps {
  item: RemoteGitPRSummary;
  selected?: boolean;
  onSelect: (item: RemoteGitPRSummary) => void;
}

/** PR 列表单行：第一行状态点、标题与 CI/评审图标；第二行次要信息。 */
export const GitPRListRow: React.FC<GitPRListRowProps> = ({ item, selected = false, onSelect }) => {
  const dotClass = statusDotClass(item.state, item.isDraft);
  const ciIcon = renderCIIcon(item.checksState);
  const reviewIcon = renderReviewIcon(item.reviewDecision);
  const relTime = item.updatedAt ? formatRelativeTime(item.updatedAt) : '';
  const metaParts = [`#${item.number}`];
  if (item.author) metaParts.push(item.author);
  if (relTime) metaParts.push(relTime);

  return (
    <button
      type="button"
      onClick={() => onSelect(item)}
      aria-current={selected ? 'true' : undefined}
      className={`w-full min-h-[44px] px-3 py-2 rounded-lg text-left flex flex-col gap-1 transition-colors motion-reduce:transition-none ${
        selected
          ? 'bg-mc-overlay-selected border-l-2 border-mc-accent'
          : 'hover:bg-mc-overlay-hover active:bg-mc-overlay-pressed'
      }`}
    >
      <span className="flex items-center gap-2 min-w-0 w-full">
        <span className={`w-[7px] h-[7px] rounded-full shrink-0 ${dotClass}`} />
        <span className="flex-1 min-w-0 truncate text-mc-body font-medium text-mc-text-primary">
          {item.title || '（无标题）'}
        </span>
        {(ciIcon || reviewIcon) && (
          <span className="flex items-center gap-1.5 shrink-0">
            {ciIcon}
            {reviewIcon}
          </span>
        )}
      </span>
      <span className="pl-[15px] text-mc-caption text-mc-text-secondary truncate block">
        {metaParts.join(' · ')}
      </span>
    </button>
  );
};

export interface GitPRDetailViewProps {
  number: number;
  summary?: RemoteGitPRSummary | null;
  detail?: RemoteGitPRDetail | null;
  loading?: boolean;
  error?: string | null;
  onRetry?: () => void;
  onBack?: () => void;
  /** 宿主支持 git.pr.startReview 且 PR 未合并/关闭时显示「AI 审查」主按钮。 */
  canStartReview?: boolean;
  startingReview?: boolean;
  reviewDisabled?: boolean;
  reviewError?: string | null;
  onStartReview?: () => void;
}

/** 可复用的 PR 详情视图：标题、元信息、状态条、正文、可折叠检查项与评论。 */
export const GitPRDetailView: React.FC<GitPRDetailViewProps> = ({
  number,
  summary = null,
  detail = null,
  loading = false,
  error = null,
  onRetry,
  onBack,
  canStartReview = false,
  startingReview = false,
  reviewDisabled = false,
  reviewError = null,
  onStartReview
}) => {
  const [checksExpanded, setChecksExpanded] = useState(false);

  useEffect(() => {
    setChecksExpanded(false);
  }, [number]);

  const openInBrowser = useCallback((url?: string) => {
    if (!url) return;
    window.open(url, '_blank', 'noopener,noreferrer');
  }, []);

  const title = detail?.title || summary?.title || '（无标题）';
  const author = detail?.author || summary?.author;
  const headBranch = detail?.headBranch || summary?.headBranch;
  const baseBranch = detail?.baseBranch || summary?.baseBranch;
  const normalizedState = (detail?.state || summary?.state || '').trim().toLowerCase();
  const showStartReview =
    Boolean(canStartReview && onStartReview) &&
    normalizedState !== '' &&
    normalizedState !== 'merged' &&
    normalizedState !== 'closed';

  const startReviewButton = showStartReview ? (
    <button
      type="button"
      onClick={onStartReview}
      disabled={startingReview || reviewDisabled}
      aria-busy={startingReview || undefined}
      className={`touch-hit-44 inline-flex items-center justify-center gap-1.5 rounded-mc-sm bg-mc-accent text-mc-on-accent font-medium hover:bg-mc-accent-hover active:bg-mc-accent-pressed disabled:opacity-45 transition-colors motion-reduce:transition-none shrink-0 ${
        onBack ? 'w-full min-h-[44px] px-4 text-mc-body' : 'h-8 px-3 text-mc-footnote'
      }`}
    >
      {startingReview ? (
        <span className="loading-spinner loading-spinner--small shrink-0 motion-reduce:animate-none" />
      ) : (
        <SparklesIcon className="w-3.5 h-3.5 shrink-0" />
      )}
      <span>{startingReview ? '正在发起审查…' : 'AI 审查'}</span>
    </button>
  ) : null;

  return (
    <div key={`detail-${number}`} className="pr-view-enter space-y-3">
      <div className="flex items-center gap-2 min-w-0">
        {onBack && (
          <button
            type="button"
            onClick={onBack}
            aria-label="返回"
            className="inline-flex items-center justify-center w-11 h-11 -ml-2 rounded-lg text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary active:bg-mc-overlay-pressed shrink-0 transition-colors motion-reduce:transition-none"
          >
            <ArrowLeftIcon className="w-4 h-4" />
          </button>
        )}
        <h2 className="flex-1 min-w-0 truncate text-mc-title font-semibold text-mc-text-primary">
          {!onBack ? `#${number} ${title}` : title}
        </h2>
        {!onBack && startReviewButton}
      </div>

      {(detail || summary) && (
        <div className="flex flex-wrap items-center gap-x-1.5 gap-y-1 text-mc-footnote text-mc-text-secondary">
          {author && (
            <>
              <span className="select-text">{author}</span>
              <span>·</span>
            </>
          )}
          <span className="font-mono text-mc-code text-mc-text-primary break-all">
            {headBranch}
            {baseBranch ? ` → ${baseBranch}` : ''}
          </span>
          {detail && (
            <>
              <span>·</span>
              <span className="font-mono text-mc-code text-mc-success">
                +{detail.additions}
              </span>
              <span className="font-mono text-mc-code text-mc-danger">
                −{detail.deletions}
              </span>
            </>
          )}
        </div>
      )}

      {onBack && startReviewButton}

      {reviewError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
            <span className="break-words select-text">{reviewError}</span>
          </div>
        </div>
      )}

      {loading && !detail && (
        <div className="flex items-center gap-2 py-2 text-xs text-mc-text-secondary">
          <span className="loading-spinner loading-spinner--small shrink-0" />
          <span>正在读取详情...</span>
        </div>
      )}

      {error && !loading && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
            <span className="break-words select-text">{error}</span>
          </div>
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="inline-alert__action min-h-[44px] px-2 flex items-center"
            >
              重试
            </button>
          )}
        </div>
      )}

      {detail && (
        <div className="space-y-4">
          {(() => {
            const strip = prStatusStrip(detail);
            const activeUrl = detail.url || summary?.url;
            return (
              <div
                className="w-full rounded-lg px-3 py-2.5 flex items-center justify-between gap-3"
                style={stripToneStyle(strip.tone)}
              >
                <span className="flex items-center gap-2 min-w-0 text-mc-body font-medium">
                  {renderStripIcon(detail, strip.tone)}
                  <span className="truncate">{strip.text}</span>
                </span>
                {activeUrl && (
                  <button
                    type="button"
                    onClick={() => openInBrowser(activeUrl)}
                    className="shrink-0 min-h-[36px] touch-hit-44 px-3 rounded-md border border-mc-border bg-mc-surface-raised text-mc-text-primary text-mc-footnote font-medium hover:bg-mc-overlay-hover transition-colors motion-reduce:transition-none"
                  >
                    在浏览器打开
                  </button>
                )}
              </div>
            );
          })()}

          {detail.body && (
            <p className="select-text text-mc-prose leading-relaxed text-mc-text-primary whitespace-pre-wrap break-words">
              {detail.body}
            </p>
          )}

          {detail.checks.length > 0 && (() => {
            const passedCount = detail.checks.filter(
              (check) => normalizeCheckKind(check.state) === 'success'
            ).length;
            return (
              <div className="rounded-lg border border-mc-hairline overflow-hidden">
                <button
                  type="button"
                  onClick={() => setChecksExpanded((prev) => !prev)}
                  aria-expanded={checksExpanded}
                  className="w-full min-h-[44px] px-3 py-2 flex items-center justify-between gap-2 text-mc-body font-medium text-mc-text-primary hover:bg-mc-overlay-hover transition-colors motion-reduce:transition-none"
                >
                  <span className="flex items-center gap-2 min-w-0">
                    {renderCIIcon(detail.checksState)}
                    <span>
                      检查 {passedCount}/{detail.checks.length}
                    </span>
                  </span>
                  <span
                    className={`pr-chevron-rot text-mc-text-tertiary shrink-0${checksExpanded ? ' is-open' : ''}`}
                  >
                    <ChevronRightIcon className="w-4 h-4" />
                  </span>
                </button>
                <div
                  className={`pr-checks-collapse${checksExpanded ? ' is-open border-t border-mc-hairline' : ''}`}
                  aria-hidden={!checksExpanded}
                >
                  <div className="pr-checks-collapse__inner">
                    <ul className="divide-y divide-mc-hairline">
                      {detail.checks.map((check, idx) => {
                        const content = (
                          <>
                            <span className="flex items-center gap-2 min-w-0">
                              {renderCIIcon(check.state)}
                              <span className="select-text font-mono text-mc-footnote text-mc-text-primary truncate min-w-0">
                                {check.name}
                              </span>
                            </span>
                            <span className="shrink-0 text-mc-caption text-mc-text-tertiary">
                              {gitExtCheckLabel(check.state)}
                            </span>
                          </>
                        );
                        return (
                          <li key={`${check.name}-${idx}`}>
                            {check.url ? (
                              <a
                                href={check.url}
                                target="_blank"
                                rel="noopener noreferrer"
                                tabIndex={checksExpanded ? undefined : -1}
                                className="flex items-center justify-between gap-3 min-h-[44px] px-3 py-2 hover:bg-mc-overlay-hover transition-colors motion-reduce:transition-none"
                              >
                                {content}
                              </a>
                            ) : (
                              <div className="flex items-center justify-between gap-3 px-3 py-2">
                                {content}
                              </div>
                            )}
                          </li>
                        );
                      })}
                    </ul>
                  </div>
                </div>
              </div>
            );
          })()}

          {detail.comments.length > 0 && (
            <ul className="space-y-3">
              {detail.comments.map((comment, idx) => {
                const initial = (comment.author || '?').slice(0, 1).toUpperCase();
                const commentTime = comment.createdAt
                  ? formatRelativeTime(comment.createdAt)
                  : '';
                return (
                  <li key={`${comment.author}-${idx}`} className="flex items-start gap-2.5">
                    <span className="w-6 h-6 rounded-full bg-mc-overlay-selected text-mc-text-secondary text-mc-caption font-semibold inline-flex items-center justify-center shrink-0">
                      {initial}
                    </span>
                    <div className="flex-1 min-w-0 space-y-1">
                      <div className="flex items-baseline gap-2 min-w-0">
                        <span className="text-mc-footnote font-semibold text-mc-text-primary truncate">
                          {comment.author}
                        </span>
                        {commentTime && (
                          <span className="text-mc-caption text-mc-text-tertiary shrink-0">
                            {commentTime}
                          </span>
                        )}
                      </div>
                      <p className="select-text text-mc-footnote leading-relaxed text-mc-text-primary break-words whitespace-pre-wrap">
                        {comment.body}
                      </p>
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
};

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
  const [selectedNumber, setSelectedNumber] = useState<number | null>(null);
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
        // 列表刷新后详情可能已变，清掉缓存并回到列表页
        setDetails({});
        setSelectedNumber(null);
        setDetailError(null);
      })
      .catch((err: unknown) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setItems([]);
        setDetails({});
        setSelectedNumber(null);
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

  const openDetail = useCallback(
    (item: RemoteGitPRSummary) => {
      setDetailError(null);
      setSelectedNumber(item.number);
      if (!details[item.number] && detailLoading !== item.number) {
        void loadDetail(item.number);
      }
    },
    [detailLoading, details, loadDetail]
  );

  const closeDetail = useCallback(() => {
    setSelectedNumber(null);
    setDetailError(null);
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

  const selectedItem =
    selectedNumber !== null ? items.find((item) => item.number === selectedNumber) ?? null : null;
  const activeDetail = selectedNumber !== null ? details[selectedNumber] ?? null : null;

  return (
    <div className="space-y-3">
      {selectedNumber !== null ? (
        <GitPRDetailView
          number={selectedNumber}
          summary={selectedItem}
          detail={activeDetail}
          loading={detailLoading === selectedNumber}
          error={detailError}
          onRetry={() => void loadDetail(selectedNumber)}
          onBack={closeDetail}
        />
      ) : (
        <div key="list" className="pr-view-enter space-y-2.5">
          {/* 列表顶部栏：右侧放刷新与加号创建按钮 */}
          <div className="flex items-center justify-between gap-2 px-0.5">
            <span className="text-mc-footnote text-mc-text-secondary">
              {loading ? '正在读取...' : `共 ${items.length} 条`}
            </span>
            <div className="flex items-center gap-1 shrink-0">
              <button
                type="button"
                onClick={() => setReloadToken((prev) => prev + 1)}
                disabled={loading}
                aria-label="刷新列表"
                title="刷新列表"
                className="inline-flex items-center justify-center w-11 h-11 rounded-lg text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary active:bg-mc-overlay-pressed disabled:opacity-40 transition-colors"
              >
                <ArrowPathIcon className="w-4 h-4" />
              </button>
              {canCreatePR && (
                <button
                  type="button"
                  onClick={requestCreate}
                  disabled={busy || loading}
                  aria-label="创建 PR"
                  title="创建 PR"
                  className="inline-flex items-center justify-center w-11 h-11 rounded-lg text-mc-text-secondary hover:bg-mc-overlay-hover hover:text-mc-text-primary active:bg-mc-overlay-pressed disabled:opacity-40 transition-colors"
                >
                  {creating ? (
                    <span className="loading-spinner loading-spinner--small shrink-0" />
                  ) : (
                    <PlusIcon className="w-4 h-4" />
                  )}
                </button>
              )}
            </div>
          </div>

          {createNotice && (
            <div
              className="flex items-start gap-2 px-3 py-2 rounded-lg text-xs text-mc-success"
              style={{
                backgroundColor: 'color-mix(in srgb, var(--mc-success) 12%, transparent)',
                border: '1px solid color-mix(in srgb, var(--mc-success) 30%, transparent)'
              }}
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
              <span className="truncate select-text">{fallbackURL}</span>
            </a>
          )}

          {createError && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
                <span className="break-words select-text">{createError}</span>
              </div>
            </div>
          )}

          {loadError && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-mc-danger" />
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
            </div>
          )}

          {!loading && !loadError && items.length === 0 && (
            <div className="empty-state">
              <h2>没有开放的 PR/MR</h2>
            </div>
          )}

          {items.length > 0 && (
            <ul className="space-y-1">
              {items.map((item) => (
                <li key={item.number}>
                  <GitPRListRow item={item} onSelect={openDetail} />
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

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
