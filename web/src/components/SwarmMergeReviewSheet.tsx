// 子代理改动合并审查抽屉：查看待合入 diff，批准合并或放弃改动

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { RemoteSwarmMergeDiffResult } from '../types/generated';
import type { RemoteSwarmBee } from '../types/swarm';
import { ConfirmDialog } from './ConfirmDialog';
import { DiffViewer } from './DiffViewer';
import { ArrowPathIcon, XMarkIcon } from './Icons';

const REVIEW_LAYER_LABELS: Record<NonNullable<RemoteSwarmBee['reviewLayer']>, string> = {
  l0: 'L0 检查',
  l1: 'L1 规则',
  l2: 'L2 模型',
  l3: 'L3 会话',
  human: '人工'
};

export interface SwarmMergeReviewSheetProps {
  /** 为空表示抽屉关闭 */
  bee: RemoteSwarmBee | null;
  onClose: () => void;
  getMergeDiff: (beeThreadID: string) => Promise<RemoteSwarmMergeDiffResult>;
  onApproveMerge: (beeThreadID: string) => Promise<void>;
  onDiscardMerge: (beeThreadID: string) => Promise<void>;
  /** 打开该 bee 的审查会话线程；未提供或无 reviewThreadId 时不渲染跳转按钮 */
  onOpenReviewThread?: (threadID: string) => void;
  /** 审查决定层；缺省时回退到 bee.reviewLayer */
  reviewLayer?: RemoteSwarmBee['reviewLayer'];
  /** 审查疑点；最多展示 10 条 */
  reviewConcerns?: string[];
  /** 审查消耗的 token 数 */
  reviewTokens?: number;
  /** token 数是估算值 */
  reviewTokensEstimated?: boolean;
}

export const SwarmMergeReviewSheet: React.FC<SwarmMergeReviewSheetProps> = ({
  bee,
  onClose,
  getMergeDiff,
  onApproveMerge,
  onDiscardMerge,
  onOpenReviewThread,
  reviewLayer,
  reviewConcerns,
  reviewTokens,
  reviewTokensEstimated
}) => {
  const beeThreadID = bee?.threadID ?? null;

  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [diffResult, setDiffResult] = useState<RemoteSwarmMergeDiffResult | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [pendingAction, setPendingAction] = useState<'approve' | 'discard' | null>(null);
  const [confirmDiscard, setConfirmDiscard] = useState(false);

  const requestIdRef = useRef(0);
  const mountedRef = useRef(true);
  // 按子代理会话缓存 diff：同一 bee 重新打开抽屉时先渲染缓存（不闪 loading）再静默刷新，不重复等首包；
  // 子代理可能继续提交，所以缓存不作为唯一依据，每次打开都后台刷新一次；
  // 截断（truncated）后的“重新获取”走 force 绕过缓存强制重拉。
  const resultCacheRef = useRef(new Map<string, RemoteSwarmMergeDiffResult>());
  // 同一子代理的请求进行中时复用同一个 Promise
  const inFlightRef = useRef(new Map<string, Promise<RemoteSwarmMergeDiffResult>>());

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // 同键请求去重：进行中直接复用 Promise，结束后清空占位
  const requestDiff = useCallback(
    (targetThreadID: string) => {
      const pending = inFlightRef.current.get(targetThreadID);
      if (pending) return pending;
      // 同键只会有一个在途请求（上面的复用保证），所以 finally 里可以无条件删除
      const request = getMergeDiff(targetThreadID).finally(() => {
        inFlightRef.current.delete(targetThreadID);
      });
      inFlightRef.current.set(targetThreadID, request);
      return request;
    },
    [getMergeDiff]
  );

  // 拉取待合并 diff：先查缓存，未命中再发请求
  // options.force 绕过缓存强制重拉（截断后的“重新获取”与错误重试）。
  const fetchDiff = useCallback(
    async (options: { force?: boolean } = {}) => {
      if (!beeThreadID) return;
      const cached = options.force ? undefined : resultCacheRef.current.get(beeThreadID);
      if (cached) {
        // 命中缓存：先渲染，不进入 loading（重开抽屉不闪空白），随后仍做一次后台刷新
        setDiffResult(cached);
      }

      const requestId = ++requestIdRef.current;
      // 有缓存可显示时改为静默后台刷新，不打断当前内容
      const silent = Boolean(cached);
      if (!silent) {
        setLoading(true);
        setLoadError(null);
      }
      try {
        const result = await requestDiff(beeThreadID);
        resultCacheRef.current.set(beeThreadID, result);
        if (!mountedRef.current || requestId !== requestIdRef.current) return;
        setDiffResult(result);
        setLoadError(null);
      } catch (err) {
        if (!mountedRef.current || requestId !== requestIdRef.current) return;
        // 后台刷新失败时保留已展示的缓存内容，只报错
        if (!silent) setDiffResult(null);
        setLoadError(err instanceof Error ? err.message : '获取待合并改动失败');
      } finally {
        if (mountedRef.current && requestId === requestIdRef.current) {
          setLoading(false);
        }
      }
    },
    [beeThreadID, requestDiff]
  );

  // 打开/切换子代理时重置并拉取 diff
  useEffect(() => {
    if (!beeThreadID) {
      requestIdRef.current++;
      setDiffResult(null);
      setLoadError(null);
      setActionError(null);
      setConfirmDiscard(false);
      setPendingAction(null);
      return;
    }
    setActionError(null);
    setConfirmDiscard(false);
    void fetchDiff();
  }, [fetchDiff, beeThreadID]);

  // 重试与截断后的“重新获取”：绕过缓存强制重拉（回调稳定，React.memo 才能生效）
  const handleRetryDiff = useCallback(() => {
    void fetchDiff({ force: true });
  }, [fetchDiff]);

  const handleApprove = async () => {
    if (!beeThreadID || pendingAction) return;
    setPendingAction('approve');
    setActionError(null);
    try {
      await onApproveMerge(beeThreadID);
      if (!mountedRef.current) return;
      setPendingAction(null);
      onClose();
    } catch (err) {
      if (!mountedRef.current) return;
      setPendingAction(null);
      setActionError(err instanceof Error ? err.message : '批准合并失败，请稍后重试');
    }
  };

  const handleDiscard = async () => {
    if (!beeThreadID || pendingAction) return;
    setConfirmDiscard(false);
    setPendingAction('discard');
    setActionError(null);
    try {
      await onDiscardMerge(beeThreadID);
      if (!mountedRef.current) return;
      setPendingAction(null);
      onClose();
    } catch (err) {
      if (!mountedRef.current) return;
      setPendingAction(null);
      setActionError(err instanceof Error ? err.message : '放弃改动失败，请稍后重试');
    }
  };

  // 传给 DiffViewer 的数组 props 用 useMemo 稳定引用，避免父组件重渲染时打断其 memo
  const changedFiles = useMemo(
    () => diffResult?.filesChanged ?? bee?.changedFiles ?? [],
    [diffResult, bee?.changedFiles]
  );
  const conflictFiles = useMemo(() => bee?.conflictFiles ?? [], [bee?.conflictFiles]);

  if (!bee) return null;

  const isBusy = pendingAction !== null;
  const isRejected = bee.mergeState === 'rejected';
  const reviewReason = (bee.reviewReason ?? '').trim();
  // 取成 const 才能在按钮回调里保持非空收窄
  const reviewThreadID = (bee.reviewThreadId ?? '').trim();
  const layer = reviewLayer ?? bee.reviewLayer;
  const layerLabel = layer ? REVIEW_LAYER_LABELS[layer] ?? '' : '';
  const concerns = (reviewConcerns ?? bee.reviewConcerns ?? [])
    .map((item) => item.trim())
    .filter((item) => item.length > 0)
    .slice(0, 10);
  const tokens = reviewTokens ?? bee.reviewTokens;
  const tokensEstimated = reviewTokensEstimated ?? bee.reviewTokensEstimated;
  const approveLabel = bee.mergeState === 'failed' ? '重新尝试合并' : '批准合并';

  return (
    <>
      <div className="sheet-layer" role="presentation" onMouseDown={isBusy ? undefined : onClose}>
        <section
          className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-x-hidden"
          role="dialog"
          aria-modal="true"
          aria-label="审查子代理改动"
          onMouseDown={(event) => event.stopPropagation()}
        >
          <div className="sheet-handle shrink-0" aria-hidden="true" />

          {/* 标题栏 */}
          <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full mb-3">
            <div className="min-w-0 flex-1">
              <p className="eyebrow truncate">合并审查</p>
              <h2 className="mt-0.5 break-words [overflow-wrap:anywhere] text-headline font-bold text-[var(--foreground)]">
                {bee.name || `子代理 #${bee.index}`} 的改动
              </h2>
              <p className="mt-1 text-mc-caption text-[var(--foreground-muted)]">
                {`${bee.linesAdded ?? 0} 行新增 · ${bee.linesDeleted ?? 0} 行删除`}
                {bee.tier ? ` · ${bee.tier} 档` : ''}
                {bee.readOnly ? ' · 只读档（改动不落回项目）' : ''}
                {layerLabel ? ` · ${layerLabel}` : ''}
              </p>
            </div>
            <button
              type="button"
              onClick={onClose}
              disabled={isBusy}
              className="icon-button shrink-0 disabled:opacity-40"
              aria-label="关闭"
              title="关闭"
            >
              <XMarkIcon className="h-4 w-4" />
            </button>
          </div>

          {/* 审查结论：未通过时不能批准或放弃，工作副本留给主代理返工 */}
          {isRejected && (
            <div
              role="alert"
              className="mb-3 shrink-0 min-w-0 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 py-2 space-y-1"
            >
              <p className="text-mc-caption font-semibold text-[var(--danger-foreground)]">审查未通过</p>
              {reviewReason && (
                <p className="text-mc-caption leading-relaxed text-[var(--danger-foreground)] break-words [overflow-wrap:anywhere]">
                  {reviewReason}
                </p>
              )}
              <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                改动未合入，补丁已保留，可让主代理派子代理按审查意见返工
              </p>
            </div>
          )}
          {!isRejected && (bee.mergeState === 'pending' || bee.mergeState === 'merged') && reviewReason && (
            <div
              role="status"
              className="mb-3 shrink-0 min-w-0 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 py-2 space-y-1"
            >
              <p className="text-mc-caption font-semibold text-[var(--foreground)]">审查模型意见：</p>
              <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)] break-words [overflow-wrap:anywhere]">
                {reviewReason}
              </p>
            </div>
          )}
          {(concerns.length > 0 || typeof tokens === 'number') && (
            <div
              role="status"
              className="mb-3 shrink-0 min-w-0 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 py-2 space-y-1"
            >
              {concerns.length > 0 && (
                <>
                  <p className="text-mc-caption font-semibold text-[var(--foreground)]">审查疑点：</p>
                  <ul className="space-y-0.5">
                    {concerns.map((concern, index) => (
                      <li
                        key={`${index}-${concern}`}
                        className="font-mono text-mc-caption leading-relaxed text-[var(--foreground-muted)] break-words [overflow-wrap:anywhere]"
                      >
                        {concern}
                      </li>
                    ))}
                  </ul>
                </>
              )}
              {typeof tokens === 'number' && (
                <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                  {tokensEstimated ? `约 ${tokens} tokens` : `${tokens} tokens`}
                </p>
              )}
            </div>
          )}
          {reviewThreadID && onOpenReviewThread && (
            <div className="mb-3 shrink-0 min-w-0">
              <button
                type="button"
                onClick={() => {
                  onOpenReviewThread(reviewThreadID);
                  onClose();
                }}
                disabled={isBusy}
                className="secondary-button disabled:opacity-40"
              >
                查看审查会话
              </button>
            </div>
          )}

          {/* 冲突与上次合并失败原因：主工作区已经回滚，直接把问题摆出来 */}
          {conflictFiles.length > 0 && (
            <div
              role="status"
              className="mb-3 shrink-0 min-w-0 rounded-xl border border-[var(--warning-border,var(--border))] bg-[var(--card-alt,var(--card))] px-3 py-2 space-y-1"
            >
              <p className="text-mc-caption font-semibold text-[var(--warning-foreground,var(--foreground))]">
                这些文件存在冲突：
              </p>
              <div className="flex flex-wrap gap-1.5">
                {conflictFiles.slice(0, 8).map((file) => (
                  <span
                    key={file}
                    className="max-w-[240px] truncate rounded-full border border-[var(--border)] bg-[var(--card)] px-2 py-0.5 font-mono text-mc-caption"
                    title={file}
                  >
                    {file}
                  </span>
                ))}
                {conflictFiles.length > 8 && (
                  <span className="px-2 py-0.5 text-mc-caption text-[var(--foreground-muted)]">
                    {`等 ${conflictFiles.length} 个`}
                  </span>
                )}
              </div>
              <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                主工作区未改动，已交回子代理处理，处理完后可再次批准。
              </p>
            </div>
          )}
          {bee.mergeError && (
            <div
              role="alert"
              className="mb-3 shrink-0 min-w-0 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-3 py-2"
            >
              <p className="text-mc-caption font-semibold text-[var(--danger-foreground)]">上次合并失败</p>
              <p className="mt-0.5 text-mc-caption leading-relaxed text-[var(--danger-foreground)] break-words [overflow-wrap:anywhere]">
                {bee.mergeError}
              </p>
            </div>
          )}

          {/* 改动文件清单 */}
          {changedFiles.length > 0 && (
            <div className="shrink-0 mb-3 min-w-0">
              <p className="mb-1 text-mc-caption text-[var(--foreground-muted)]">
                {`${changedFiles.length} 个文件待合入`}
              </p>
              <div className="flex flex-wrap gap-1.5">
                {changedFiles.slice(0, 8).map((file) => (
                  <span
                    key={file}
                    className="max-w-[240px] truncate rounded-full border border-[var(--border)] bg-[var(--card)] px-2 py-0.5 font-mono text-mc-caption"
                    title={file}
                  >
                    {file}
                  </span>
                ))}
                {changedFiles.length > 8 && (
                  <span className="px-2 py-0.5 text-mc-caption text-[var(--foreground-muted)]">
                    {`等 ${changedFiles.length} 个`}
                  </span>
                )}
              </div>
            </div>
          )}

          {/* diff 主体 */}
          <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5">
            {loading ? (
              <div className="flex flex-col items-center justify-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] p-8">
                <span className="w-6 h-6 border-2 border-[var(--primary)]/30 border-t-[var(--primary)] rounded-full animate-spin" />
                <p className="text-xs text-[var(--foreground-muted)]">正在加载待合并改动…</p>
              </div>
            ) : loadError && !diffResult ? (
              <div className="space-y-3 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] p-4">
                <p className="text-xs text-[var(--danger-foreground)] break-words">{loadError}</p>
                <button
                  type="button"
                  onClick={handleRetryDiff}
                  className="secondary-button"
                >
                  <ArrowPathIcon className="h-3.5 w-3.5" />重试
                </button>
              </div>
            ) : (
              <>
                {diffResult && loadError && (
                  <p role="alert" className="mb-2 text-mc-caption text-[var(--danger-foreground)] break-words">
                    刷新失败，以下为上次获取的结果：{loadError}
                  </p>
                )}
                {diffResult?.truncated && (
                  <p className="mb-2 text-mc-caption text-[var(--warning-foreground,var(--foreground-muted))]">
                    改动过大，已截断
                  </p>
                )}
                <DiffViewer
                  diffText={diffResult?.diff}
                  truncated={diffResult?.truncated}
                  filesChanged={changedFiles}
                  readOnly
                  onRetry={handleRetryDiff}
                />
              </>
            )}
          </div>

          {/* 底部操作栏 */}
          <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 min-w-0 max-w-full w-full overflow-x-hidden space-y-2">
            {actionError && (
              <p role="alert" className="text-xs text-[var(--danger-foreground)] break-words">
                {actionError}
              </p>
            )}
            <div className="flex flex-wrap items-center justify-end gap-2">
              <button
                type="button"
                onClick={onClose}
                disabled={isBusy}
                className="secondary-button disabled:opacity-40"
              >
                关闭
              </button>
              {!isRejected && (
                <>
                  <button
                    type="button"
                    onClick={() => setConfirmDiscard(true)}
                    disabled={isBusy || loading}
                    className="secondary-button text-[var(--danger-foreground)] disabled:opacity-40"
                  >
                    {pendingAction === 'discard' ? '正在放弃…' : '放弃改动'}
                  </button>
                  <button
                    type="button"
                    onClick={() => void handleApprove()}
                    disabled={isBusy || loading}
                    className="primary-button disabled:opacity-40"
                  >
                    {pendingAction === 'approve' ? '正在合并…' : approveLabel}
                  </button>
                </>
              )}
            </div>
          </div>
        </section>
      </div>

      <ConfirmDialog
        isOpen={confirmDiscard}
        title="放弃子代理改动"
        message={`确定要放弃“${bee.name || '当前子代理'}”的全部改动吗？该操作不可撤销。`}
        confirmLabel="放弃改动"
        confirmVariant="danger"
        isLoading={pendingAction === 'discard'}
        onConfirm={() => void handleDiscard()}
        onCancel={() => {
          if (pendingAction !== 'discard') setConfirmDiscard(false);
        }}
      />
    </>
  );
};
