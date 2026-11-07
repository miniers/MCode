// 撤销此轮改动：先预览这一轮会还原哪些文件、哪些文件因为之后又被改过而保留，确认后才落盘。
// 对应桌面端 internal/ui/revert_dialog.go（openRevertPreview / confirmRevert）。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteRevertApi } from '../../context/remoteRevertApi';
import type {
  RemoteThreadsRevertApplyResult,
  RemoteThreadsRevertPreviewResult
} from '../../types/generated';
import { ExclamationTriangleIcon, XMarkIcon } from '../Icons';

export interface RevertTurnSheetProps {
  isOpen: boolean;
  threadID: string;
  turnID: string;
  /** 会话运行中：宿主会拒绝撤销，按钮直接禁用并说明原因（与桌面同逻辑） */
  isRunning?: boolean;
  onClose: () => void;
  /** 撤销成功后回调，由调用方刷新时间线与提示 */
  onReverted?: (result: RemoteThreadsRevertApplyResult) => void;
}

// 预览里最多直接列出的文件数量，其余折叠成计数
const MAX_LISTED_FILES = 12;

/** 已保留文件的说明文案，与桌面 revert_dialog.go 一致 */
const KEPT_REASON = '已保留（轮次结束后被修改过）';

export const RevertTurnSheet: React.FC<RevertTurnSheetProps> = ({
  isOpen,
  threadID,
  turnID,
  isRunning = false,
  onClose,
  onReverted
}) => {
  const { supported, preview: previewRevert, apply: applyRevert } = useRemoteRevertApi();

  const [preview, setPreview] = useState<RemoteThreadsRevertPreviewResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 成功后的结果：sheet 留在这里显示还原/保留的数量，点「完成」才关闭
  const [applied, setApplied] = useState<RemoteThreadsRevertApplyResult | null>(null);

  const requestRef = useRef(0);
  const mountedRef = useRef(true);

  const loadPreview = useCallback(async () => {
    if (!supported) {
      setError('当前宿主不支持撤销回合改动');
      return;
    }
    const requestID = ++requestRef.current;
    setLoading(true);
    setError(null);
    try {
      const result = await previewRevert(threadID, turnID);
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setPreview(result);
    } catch (err) {
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setPreview(null);
      setError(err instanceof Error ? err.message : '加载撤销预览失败');
    } finally {
      if (mountedRef.current && requestID === requestRef.current) setLoading(false);
    }
  }, [previewRevert, supported, threadID, turnID]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
    };
  }, []);

  // 打开时拉预览；关闭时清空状态并作废在途请求
  useEffect(() => {
    if (isOpen) {
      void loadPreview();
      return;
    }
    requestRef.current += 1;
    setPreview(null);
    setLoading(false);
    setExecuting(false);
    setError(null);
    setApplied(null);
  }, [isOpen, loadPreview]);

  const requestClose = useCallback(() => {
    if (executing) return;
    onClose();
  }, [executing, onClose]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      requestClose();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [isOpen, requestClose]);

  const handleApply = useCallback(async () => {
    if (executing || !preview || isRunning || preview.restorableCount === 0) return;
    setExecuting(true);
    setError(null);
    try {
      const result = await applyRevert(threadID, turnID);
      if (!mountedRef.current) return;
      if (!result.ok) {
        setError('Mac 端没有还原任何文件，请稍后重试');
        return;
      }
      setApplied(result);
      onReverted?.(result);
    } catch (err) {
      if (!mountedRef.current) return;
      setError(err instanceof Error ? err.message : '撤销此轮改动失败');
    } finally {
      if (mountedRef.current) setExecuting(false);
    }
  }, [applyRevert, executing, isRunning, onReverted, preview, threadID, turnID]);

  if (!isOpen) return null;

  const restorableFiles = preview ? preview.files.filter((file) => file.restorable) : [];
  const keptFiles = preview ? preview.files.filter((file) => !file.restorable) : [];
  const busy = loading || executing;
  const nothingToRevert = Boolean(preview && preview.restorableCount === 0);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={requestClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full max-w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="revert-sheet-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full mb-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">文件改动</p>
            <h2 id="revert-sheet-title" className="mt-0.5 text-headline font-bold text-[var(--foreground)]">
              撤销此轮改动
            </h2>
            <p className="mt-1 text-xs text-[var(--foreground-muted)]">
              把这一轮改过的文件还原到回合开始前的状态
            </p>
          </div>
          <button
            type="button"
            onClick={requestClose}
            disabled={executing}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px] disabled:opacity-40"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          {loading && (
            <div className="empty-state">
              <span className="loading-spinner mb-1" />
              <h2>正在读取改动范围</h2>
              <p className="text-xs text-[var(--foreground-muted)]">正在对比这一轮的文件改动…</p>
            </div>
          )}

          {!loading && applied && (
            <div className="inline-alert" role="status">
              <span className="break-words select-text">
                已还原 {applied.restorableCount} 个文件
                {applied.keptCount > 0 ? `，保留 ${applied.keptCount} 个文件` : ''}
              </span>
            </div>
          )}

          {!loading && !applied && preview && (
            <>
              <p className="text-xs font-semibold text-[var(--foreground)]">
                将还原 {preview.restorableCount} 个文件，保留 {preview.keptCount} 个文件（共 {preview.total} 个）
              </p>

              {restorableFiles.length > 0 && (
                <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-1.5">
                  <p className="text-xs font-semibold text-[var(--foreground)]">将还原</p>
                  <ul className="space-y-1">
                    {restorableFiles.slice(0, MAX_LISTED_FILES).map((file) => (
                      <li key={file.path} className="flex items-start justify-between gap-2 min-w-0">
                        <span className="font-mono text-mc-caption text-[var(--foreground)] break-all min-w-0">
                          {file.path}
                        </span>
                        <span className="shrink-0 font-mono text-mc-caption text-[var(--foreground-muted)]">
                          +{file.additions} −{file.deletions}
                        </span>
                      </li>
                    ))}
                    {restorableFiles.length > MAX_LISTED_FILES && (
                      <li className="text-mc-caption text-[var(--foreground-muted)]">
                        还有 {restorableFiles.length - MAX_LISTED_FILES} 个文件…
                      </li>
                    )}
                  </ul>
                </div>
              )}

              {keptFiles.length > 0 && (
                <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-1.5">
                  <p className="text-xs font-semibold text-[var(--foreground)]">保留不动 · {keptFiles.length} 个</p>
                  <ul className="space-y-1">
                    {keptFiles.slice(0, MAX_LISTED_FILES).map((file) => (
                      <li key={file.path} className="min-w-0">
                        <span className="font-mono text-mc-caption text-[var(--foreground-muted)] break-all">
                          {file.path}
                        </span>
                        <span className="block text-mc-caption text-[var(--foreground-muted)]">{KEPT_REASON}</span>
                      </li>
                    ))}
                    {keptFiles.length > MAX_LISTED_FILES && (
                      <li className="text-mc-caption text-[var(--foreground-muted)]">
                        还有 {keptFiles.length - MAX_LISTED_FILES} 个文件…
                      </li>
                    )}
                  </ul>
                </div>
              )}

              {nothingToRevert && (
                <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
                  这一轮的文件改动都已被后续改动覆盖，没有可以还原的内容。
                </p>
              )}

              {isRunning && (
                <div className="inline-alert" role="status">
                  <div className="flex items-start gap-2 min-w-0 flex-1">
                    <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--warning-foreground)]" />
                    <span className="break-words select-text">
                      会话正在运行，等这一轮结束后才能撤销改动。
                    </span>
                  </div>
                </div>
              )}
            </>
          )}

          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
              <button
                type="button"
                onClick={() => void loadPreview()}
                disabled={busy}
                className="inline-alert__action min-h-[44px] px-2 flex items-center"
              >
                重试
              </button>
            </div>
          )}
        </div>

        <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 min-w-0 max-w-full w-full flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={requestClose}
            disabled={executing}
            className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] disabled:opacity-50 transition"
          >
            {applied ? '完成' : '取消'}
          </button>
          {!applied && (
            <button
              type="button"
              onClick={() => void handleApply()}
              disabled={busy || isRunning || nothingToRevert || !preview}
              className="primary-button min-h-[44px] min-w-[88px] disabled:opacity-50"
            >
              {executing ? '撤销中…' : '撤销此轮改动'}
            </button>
          )}
        </div>
      </section>
    </div>
  );
};
