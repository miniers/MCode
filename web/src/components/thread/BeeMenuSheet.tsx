// 子代理页「…」菜单：返回主会话、停止子代理、沿用改动续做、审阅待合并改动、复制会话链接。
// 外层结构与 thread/ThreadMenuSheet.tsx 保持一致（sheet-layer + thread-action-sheet）。

import React, { useCallback, useEffect, useState } from 'react';
import { ChevronRightIcon, XMarkIcon } from '../Icons';
import { useRemoteSwarmExtApi } from '../../context/remoteSwarmExtApi';
import { useThreadSwarm } from '../../context/remoteSwarm';

export interface BeeMenuSheetProps {
  isOpen: boolean;
  beeName?: string;
  threadID: string;
  /** 与原先底部「停止」一致：canStop 且已连接时才可用 */
  canStop: boolean;
  stopSubmitting?: boolean;
  hasPendingMerge: boolean;
  hasLeadThread: boolean;
  onClose: () => void;
  onBackToLead: () => void;
  onStop: () => void;
  onReviewMerge: () => void;
}

export const BeeMenuSheet: React.FC<BeeMenuSheetProps> = ({
  isOpen,
  beeName,
  threadID,
  canStop,
  stopSubmitting = false,
  hasPendingMerge,
  hasLeadThread,
  onClose,
  onBackToLead,
  onStop,
  onReviewMerge
}) => {
  const [copyNotice, setCopyNotice] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null);
  const [retryError, setRetryError] = useState<string | null>(null);
  const [retrySubmitting, setRetrySubmitting] = useState(false);

  // 「沿用改动续做」要用子代理的失败状态与合入状态，这些字段不在 props 里；
  // 只在菜单打开时按需拉一次编排状态，关闭后传 null 停止跟踪。
  const swarm = useThreadSwarm(isOpen ? threadID : null);
  const swarmApi = useRemoteSwarmExtApi();
  const bee = swarm.state?.bee;
  const leadThreadID = swarm.state?.leadThreadID;
  const canRetryBee = Boolean(
    swarmApi.supportsRetryBee &&
      bee &&
      (bee.status === 'failed' || bee.status === 'stopped') &&
      bee.mergeState !== 'merged'
  );

  useEffect(() => {
    if (!isOpen) {
      setCopyNotice(null);
      setRetryError(null);
      setRetrySubmitting(false);
    }
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isOpen, onClose]);

  // 复制当前子代理会话的分享链接（应用内 hash 路由）
  const handleCopyLink = useCallback(async () => {
    const link = `${window.location.origin}${window.location.pathname}#/t/${encodeURIComponent(threadID)}`;
    try {
      if (!navigator.clipboard?.writeText) throw new Error('当前浏览器环境不支持剪贴板写入');
      await navigator.clipboard.writeText(link);
      setCopyNotice({ kind: 'ok', text: '会话链接已复制' });
    } catch (err) {
      setCopyNotice({ kind: 'error', text: err instanceof Error ? err.message : '复制会话链接失败' });
    }
  }, [threadID]);

  // 沿用失败/已停止子代理的原 worktree 与半成品改动重新派发；成功后直接关菜单，
  // 列表由宿主推送的 threads.swarm.changed 刷新，失败时把原因留在菜单里。
  const handleRetryBee = useCallback(async () => {
    setRetrySubmitting(true);
    setRetryError(null);
    try {
      await swarmApi.retryBee({ threadID: leadThreadID || threadID, beeThreadID: threadID });
      onClose();
    } catch (err) {
      setRetryError(err instanceof Error ? err.message : '沿用改动续做失败');
    } finally {
      setRetrySubmitting(false);
    }
  }, [swarmApi, leadThreadID, threadID, onClose]);

  if (!isOpen) return null;

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="thread-action-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="bee-actions-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="eyebrow">子代理</p>
            <h2 id="bee-actions-title" className="sheet-title truncate">
              {beeName || '子代理会话'}
            </h2>
          </div>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            aria-label="关闭菜单"
            title="关闭"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="sheet-actions">
          {hasLeadThread && (
            <button type="button" className="sheet-action" onClick={onBackToLead}>
              <ChevronRightIcon className="h-4 w-4 rotate-180" />
              <span>返回主会话</span>
            </button>
          )}
          <button
            type="button"
            className="sheet-action sheet-action--danger"
            onClick={onStop}
            disabled={!canStop || stopSubmitting}
          >
            <span className="text-sm">■</span>
            <span>{stopSubmitting ? '停止中…' : '停止子代理'}</span>
          </button>
          {canRetryBee && (
            <button
              type="button"
              className="sheet-action"
              onClick={() => void handleRetryBee()}
              disabled={retrySubmitting}
            >
              <span className="text-sm">↻</span>
              <span>{retrySubmitting ? '重新派发中…' : '沿用改动续做'}</span>
            </button>
          )}
          {hasPendingMerge && (
            <button type="button" className="sheet-action" onClick={onReviewMerge}>
              <span className="text-sm">⇄</span>
              <span>审阅待合并改动</span>
            </button>
          )}
          <button type="button" className="sheet-action" onClick={() => void handleCopyLink()}>
            <span className="text-sm">🔗</span>
            <span>复制会话链接</span>
          </button>
        </div>

        {retryError && (
          <p role="alert" className="mt-1.5 text-mc-caption text-[var(--danger-foreground)]">
            {retryError}
          </p>
        )}

        {copyNotice && (
          <p
            role={copyNotice.kind === 'ok' ? 'status' : 'alert'}
            className={`mt-1.5 text-mc-caption ${
              copyNotice.kind === 'ok'
                ? 'text-emerald-600 dark:text-emerald-400'
                : 'text-[var(--danger-foreground)]'
            }`}
          >
            {copyNotice.text}
          </p>
        )}
      </section>
    </div>
  );
};
