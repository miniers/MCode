// 移动端批量整理抽屉：按最后活跃时间与置顶/已休眠范围预览命中会话，再批量归档、标为已休眠或标为已读。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import type {
  RemoteThreadsBulkArchiveParams,
  RemoteThreadsBulkArchiveResult,
  RemoteThreadsBulkMarkReadParams,
  RemoteThreadsBulkMarkReadResult,
  RemoteThreadsBulkPreviewParams,
  RemoteThreadsBulkPreviewResult,
  RemoteThreadsBulkMarkDormantParams,
  RemoteThreadsBulkMarkDormantResult,
  RemoteThreadsBulkUndoParams,
  RemoteThreadsBulkUndoResult
} from '../types/generated';
import {
  ArchiveBoxIcon,
  CheckCircleIcon,
  EyeIcon,
  ExclamationTriangleIcon,
  XMarkIcon
} from './Icons';

export type BulkThreadsAction = 'archive' | 'dormant' | 'markRead';

/** 批量整理依赖的宿主接口，与 McodeRpcClient 上的 threads.bulk.* 一一对应。 */
export interface BulkThreadsApi {
  previewBulkThreads: (params: RemoteThreadsBulkPreviewParams) => Promise<RemoteThreadsBulkPreviewResult>;
  archiveBulkThreads: (params: RemoteThreadsBulkArchiveParams) => Promise<RemoteThreadsBulkArchiveResult>;
  markDormantBulkThreads: (params: RemoteThreadsBulkMarkDormantParams) => Promise<RemoteThreadsBulkMarkDormantResult>;
  markBulkThreadsRead: (params: RemoteThreadsBulkMarkReadParams) => Promise<RemoteThreadsBulkMarkReadResult>;
  undoBulkThreads: (params: RemoteThreadsBulkUndoParams) => Promise<RemoteThreadsBulkUndoResult>;
}

/**
 * 批量整理依赖 RemoteContext 暴露的 threads.bulk.* 接口；
 * 宿主声明 threads.bulk.preview 能力时才视为可用。
 */
export function useBulkThreadsApi(): BulkThreadsApi | null {
  const {
    capabilities,
    previewBulkThreads,
    archiveBulkThreads,
    markDormantBulkThreads,
    markBulkThreadsRead,
    undoBulkThreads
  } = useRemoteStatic();
  return useMemo(() => {
    if (!capabilities?.methods.includes('threads.bulk.preview')) return null;
    return {
      previewBulkThreads,
      archiveBulkThreads,
      markDormantBulkThreads,
      markBulkThreadsRead,
      undoBulkThreads
    };
  }, [
    capabilities,
    previewBulkThreads,
    archiveBulkThreads,
    markDormantBulkThreads,
    markBulkThreadsRead,
    undoBulkThreads
  ]);
}

export interface BulkThreadsSheetProps {
  projectID: string;
  onClose: () => void;
  onDone: (message: string, undoToken?: string, action?: BulkThreadsAction) => void;
}

const RANGE_OPTIONS: Array<{ days: number; label: string }> = [
  { days: 1, label: '早于 1 天前' },
  { days: 3, label: '早于 3 天前' },
  { days: 7, label: '早于 7 天前' },
  { days: 30, label: '早于 30 天前' }
];

const ACTION_OPTIONS: Array<{ value: BulkThreadsAction; label: string; hint: string }> = [
  { value: 'archive', label: '归档', hint: '从列表隐藏，可在「已归档」里找回' },
  { value: 'dormant', label: '标记为休眠', hint: '收进已休眠分组，仍保留在列表' },
  { value: 'markRead', label: '标记已读', hint: '只清除未读标记，不改动会话' }
];

const ACTION_ICONS: Record<BulkThreadsAction, React.FC<{ className?: string }>> = {
  archive: ArchiveBoxIcon,
  dormant: CheckCircleIcon,
  markRead: EyeIcon
};

const ACTION_LABELS: Record<BulkThreadsAction, string> = {
  archive: '归档',
  dormant: '标记为休眠',
  markRead: '标为已读'
};

const MS_PER_DAY = 24 * 60 * 60 * 1000;
const PREVIEW_DEBOUNCE_MS = 260;

export const BulkThreadsSheet: React.FC<BulkThreadsSheetProps> = ({
  projectID,
  onClose,
  onDone
}) => {
  const api = useBulkThreadsApi();

  const [action, setAction] = useState<BulkThreadsAction>('archive');
  const [rangeDays, setRangeDays] = useState(7);
  const [includePinned, setIncludePinned] = useState(false);
  const [includeDormant, setIncludeDormant] = useState(false);

  const [preview, setPreview] = useState<RemoteThreadsBulkPreviewResult | null>(null);
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState<string | null>(null);

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const previewRequestRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      previewRequestRef.current += 1;
    };
  }, []);

  // 任一筛选变化后防抖取一次预览；代际号丢弃过期响应，避免慢请求覆盖新结果。
  useEffect(() => {
    if (!api) {
      setPreview(null);
      setPreviewLoading(false);
      setPreviewError('当前连接未提供批量整理接口（threads.bulk.*）');
      return;
    }

    const requestId = ++previewRequestRef.current;
    setPreviewLoading(true);
    setPreviewError(null);

    const timer = setTimeout(() => {
      const before = new Date(Date.now() - rangeDays * MS_PER_DAY).toISOString();
      void api
        .previewBulkThreads({ projectID, before, includePinned, includeDormant })
        .then((result) => {
          if (!mountedRef.current || requestId !== previewRequestRef.current) return;
          setPreview(result);
        })
        .catch((error: unknown) => {
          if (!mountedRef.current || requestId !== previewRequestRef.current) return;
          setPreview(null);
          setPreviewError(error instanceof Error ? error.message : '统计可整理会话失败');
        })
        .finally(() => {
          if (!mountedRef.current || requestId !== previewRequestRef.current) return;
          setPreviewLoading(false);
        });
    }, PREVIEW_DEBOUNCE_MS);

    return () => clearTimeout(timer);
  }, [api, includePinned, includeDormant, projectID, rangeDays]);

  const affectedCount = preview?.threadIDs.length ?? 0;
  const skippedRunning = preview?.skippedRunning ?? 0;
  const canSubmit = Boolean(api) && affectedCount > 0 && !previewLoading && !submitting;

  const previewText = (() => {
    if (previewError) return previewError;
    if (previewLoading) return '正在统计可整理的会话…';
    if (!preview) return '选择筛选条件后统计可整理的会话';
    if (affectedCount === 0) return '没有符合条件的会话';
    const base = `将处理 ${affectedCount} 个会话`;
    return skippedRunning > 0 ? `${base}，跳过 ${skippedRunning} 个运行中的会话` : base;
  })();

  const handleConfirm = useCallback(async () => {
    if (!api || !preview || preview.threadIDs.length === 0 || submitting) return;
    const threadIDs = preview.threadIDs;
    setSubmitting(true);
    setSubmitError(null);

    try {
      if (action === 'archive') {
        const result = await api.archiveBulkThreads({ projectID, threadIDs });
        if (!mountedRef.current) return;
        onDone(`已归档 ${result.threadIDs.length} 个会话`, result.undoToken, 'archive');
        return;
      }
      if (action === 'dormant') {
        const result = await api.markDormantBulkThreads({ projectID, threadIDs });
        if (!mountedRef.current) return;
        onDone(`已休眠 ${result.threadIDs.length} 个会话`, result.undoToken, 'dormant');
        return;
      }
      const result = await api.markBulkThreadsRead({ projectID, threadIDs });
      if (!mountedRef.current) return;
      // 标记已读不提供撤销，与桌面端 internal/ui/sidebar_bulk.go 一致。
      onDone(`已将 ${result.threadIDs.length} 个会话标为已读`, undefined, 'markRead');
    } catch (error) {
      if (!mountedRef.current) return;
      setSubmitting(false);
      setSubmitError(error instanceof Error ? error.message : '批量整理失败');
    }
  }, [action, api, onDone, preview, projectID, submitting]);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="bulk-threads-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="mb-3 flex w-full min-w-0 shrink-0 items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">批量整理</p>
            <h2
              id="bulk-threads-title"
              className="mt-0.5 break-words [overflow-wrap:anywhere] text-headline font-bold text-[var(--foreground)]"
            >
              整理会话
            </h2>
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="min-h-0 w-full min-w-0 max-w-full flex-1 space-y-3 overflow-y-auto overflow-x-hidden pr-0.5">
          {submitError && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex min-w-0 flex-1 items-start gap-2">
                <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--danger-foreground)]" />
                <span className="select-text break-words">{submitError}</span>
              </div>
            </div>
          )}

          {/* 动作单选 */}
          <div className="space-y-1.5">
            <p className="settings-label">操作</p>
            <div className="sheet-actions" role="radiogroup" aria-label="批量操作">
              {ACTION_OPTIONS.map((option) => {
                const Icon = ACTION_ICONS[option.value];
                const selected = action === option.value;
                return (
                  <button
                    key={option.value}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    onClick={() => setAction(option.value)}
                    className="sheet-action"
                  >
                    <Icon className="h-4 w-4 shrink-0 text-[var(--primary)]" />
                    <span className="min-w-0 flex-1">
                      <span className="block text-xs font-semibold text-[var(--foreground)]">
                        {option.label}
                      </span>
                      <span className="block text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                        {option.hint}
                      </span>
                    </span>
                    <span
                      aria-hidden="true"
                      className={`ml-auto grid h-4 w-4 shrink-0 place-items-center rounded-full border ${
                        selected
                          ? 'border-[var(--primary)] bg-[var(--primary)]'
                          : 'border-[var(--border)]'
                      }`}
                    >
                      {selected && <span className="h-1.5 w-1.5 rounded-full bg-white" />}
                    </span>
                  </button>
                );
              })}
            </div>
          </div>

          {/* 时间范围 */}
          <div className="space-y-1.5">
            <p className="settings-label">最后活跃时间</p>
            <div
              className="flex gap-1 rounded-mc-lg border border-[var(--border)] bg-[var(--screen)] p-0.5"
              role="group"
              aria-label="最后活跃时间范围"
            >
              {RANGE_OPTIONS.map((option) => {
                const selected = rangeDays === option.days;
                return (
                  <button
                    key={option.days}
                    type="button"
                    aria-pressed={selected}
                    onClick={() => setRangeDays(option.days)}
                    className={`min-h-[36px] flex-1 rounded-mc-md px-1 text-mc-caption font-semibold transition ${
                      selected
                        ? 'bg-[var(--card)] text-[var(--primary)] shadow-sm'
                        : 'text-[var(--foreground-muted)]'
                    }`}
                  >
                    {option.label}
                  </button>
                );
              })}
            </div>
          </div>

          {/* 范围开关 */}
          <div className="space-y-1.5">
            <div className="flex items-center justify-between gap-3 py-1">
              <div className="min-w-0">
                <p className="text-xs font-semibold text-[var(--foreground)]">包含置顶</p>
                <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                  置顶会话默认不动，开启后一并整理
                </p>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={includePinned}
                aria-label="切换包含置顶"
                onClick={() => setIncludePinned((value) => !value)}
                className={`relative h-5 w-9 shrink-0 rounded-full transition ${
                  includePinned ? 'bg-mc-accent' : 'bg-[var(--border)]'
                }`}
              >
                <span
                  className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${
                    includePinned ? 'translate-x-4' : ''
                  }`}
                />
              </button>
            </div>

            <div className="flex items-center justify-between gap-3 py-1">
              <div className="min-w-0">
                <p className="text-xs font-semibold text-[var(--foreground)]">包含已休眠</p>
                <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                  已休眠会话默认跳过，开启后一并整理
                </p>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={includeDormant}
                aria-label="切换包含已休眠"
                onClick={() => setIncludeDormant((value) => !value)}
                className={`relative h-5 w-9 shrink-0 rounded-full transition ${
                  includeDormant ? 'bg-mc-accent' : 'bg-[var(--border)]'
                }`}
              >
                <span
                  className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${
                    includeDormant ? 'translate-x-4' : ''
                  }`}
                />
              </button>
            </div>
          </div>

          {/* 预览结果 */}
          <div
            role="status"
            aria-live="polite"
            className="flex items-center gap-2 rounded-lg bg-[var(--border)]/40 px-3 py-2 text-xs text-[var(--foreground-muted)]"
          >
            {previewLoading && <span className="loading-spinner loading-spinner--small shrink-0" />}
            <span className="min-w-0 select-text break-words">{previewText}</span>
          </div>

          <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
            运行中的会话不会被整理；操作完成后可在提示条里撤销。
          </p>
        </div>

        <div className="mt-3 flex w-full min-w-0 max-w-full shrink-0 items-center justify-end gap-2 border-t border-[var(--border)] pb-0 pt-3">
          <button
            type="button"
            onClick={onClose}
            className="secondary-button min-h-[44px] min-w-[72px]"
          >
            取消
          </button>
          <button
            type="button"
            onClick={() => void handleConfirm()}
            disabled={!canSubmit}
            className="primary-button min-h-[44px] min-w-[96px] disabled:opacity-60"
          >
            {submitting && <span className="loading-spinner loading-spinner--small shrink-0" />}
            {submitting ? '处理中…' : ACTION_LABELS[action]}
          </button>
        </div>
      </section>
    </div>
  );
};
