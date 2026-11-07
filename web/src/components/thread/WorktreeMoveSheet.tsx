// 移到 worktree 抽屉：先预览可移到 worktree 的条件与建议分支名，确认后调用 threads.worktree.execute

import React, { useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react';
import type { RuntimeWorktreeExecuteResult, RuntimeWorktreePreviewResult } from '../../types/generated';
import { useRemoteStatic } from '../../context/RemoteContext';
import { SheetFrame } from '../sheet/SheetFrame';
import { MenuSheetHeader } from './menu/MenuSheetHeader';
import type { ThreadMenuPageHandle } from './menu/menuPages';
import { ExclamationTriangleIcon } from '../Icons';

export interface WorktreeMoveSheetProps {
  isOpen: boolean;
  threadID: string;
  threadTitle: string;
  onClose: () => void;
  /** 移到 worktree 成功后回调，由调用方负责关闭菜单与提示 */
  onExecuted?: (result: RuntimeWorktreeExecuteResult, transferFiles: boolean) => void;
}

// 预览里最多直接列出的文件名数量，其余折叠成计数
const MAX_LISTED_FILES = 8;

const INPUT_CLASS = 'pairing-input w-full min-w-0 text-sm font-mono';

/** 内容体：不含遮罩与标题栏，挂载即拉预览；执行中拦截关闭。 */
export const WorktreeMoveSheetBody = React.forwardRef<
  ThreadMenuPageHandle,
  Omit<WorktreeMoveSheetProps, 'isOpen'>
>(function WorktreeMoveSheetBody({ threadID, threadTitle, onClose, onExecuted }, ref) {
  const {
    connectionState,
    connectionMessage,
    previewWorktreeMove,
    executeWorktreeMove,
    limitProviderText
  } = useRemoteStatic();

  const [preview, setPreview] = useState<RuntimeWorktreePreviewResult | null>(null);
  const [branch, setBranch] = useState('');
  const [transferFiles, setTransferFiles] = useState(true);
  const [loading, setLoading] = useState(false);
  const [executing, setExecuting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [limitedText, setLimitedText] = useState<string | null>(null);
  const [limitingText, setLimitingText] = useState(false);
  const [limitError, setLimitError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  const requestRef = useRef(0);
  const mountedRef = useRef(true);

  const isConnected = connectionState === 'connected';

  const loadPreview = useCallback(async () => {
    if (!isConnected) {
      setError(`未连接到 Mac 宿主（当前连接：${connectionMessage || connectionState}）`);
      return;
    }
    const requestID = ++requestRef.current;
    setLoading(true);
    setError(null);
    try {
      const result = await previewWorktreeMove({ threadID });
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setPreview(result);
      setBranch(result.suggestedBranch || '');
      // 没有未提交改动时默认不转移文件，避免凭空新建空 worktree 分支
      setTransferFiles(result.files.length > 0 || result.untracked.length > 0);
    } catch (err) {
      if (!mountedRef.current || requestID !== requestRef.current) return;
      setPreview(null);
      setError(err instanceof Error ? err.message : '读取 worktree 预览失败');
    } finally {
      if (mountedRef.current && requestID === requestRef.current) setLoading(false);
    }
  }, [connectionMessage, connectionState, isConnected, previewWorktreeMove, threadID]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestRef.current += 1;
    };
  }, []);

  // 每次挂载（即每次入栈）拉一次预览；卸载时由 mountedRef 清理作废在途请求
  useEffect(() => {
    void loadPreview();
  }, [loadPreview]);

  const handleLimitText = useCallback(async () => {
    if (limitingText) return;
    setLimitingText(true);
    setLimitError(null);
    setCopied(false);
    try {
      const result = await limitProviderText(threadID);
      if (!mountedRef.current) return;
      setLimitedText(result.text || '');
    } catch (err) {
      if (!mountedRef.current) return;
      setLimitError(err instanceof Error ? err.message : '生成精简交接提示词失败');
    } finally {
      if (mountedRef.current) setLimitingText(false);
    }
  }, [limitProviderText, limitingText, threadID]);

  const handleCopyLimitedText = useCallback(async () => {
    if (!limitedText) return;
    try {
      await navigator.clipboard.writeText(limitedText);
      if (mountedRef.current) setCopied(true);
    } catch {
      if (mountedRef.current) setLimitError('浏览器不允许写剪贴板，请手动选择文本复制');
    }
  }, [limitedText]);

  const requestClose = useCallback(() => (executing ? false : true), [executing]);

  useImperativeHandle(ref, () => ({ requestClose }), [requestClose]);

  const handleClose = useCallback(() => {
    if (!requestClose()) return;
    onClose();
  }, [onClose, requestClose]);

  const handleExecute = useCallback(async () => {
    if (executing || !preview) return;
    setExecuting(true);
    setError(null);
    try {
      const result = await executeWorktreeMove({
        threadID,
        branch: branch.trim() || undefined,
        transferFiles
      });
      if (!mountedRef.current) return;
      onExecuted?.(result, transferFiles);
    } catch (err) {
      if (!mountedRef.current) return;
      setError(err instanceof Error ? err.message : '移到 worktree 失败');
    } finally {
      if (mountedRef.current) setExecuting(false);
    }
  }, [branch, executing, executeWorktreeMove, onExecuted, preview, threadID, transferFiles]);

  const eligible = Boolean(preview?.eligible);
  const pendingFiles = preview ? [...preview.files, ...preview.untracked] : [];
  const busy = loading || executing;

  return (
    <div className="flex flex-col flex-1 min-h-0 min-w-0">
      <p className="mb-3 shrink-0 text-xs text-[var(--foreground-muted)] truncate">{threadTitle || '未命名会话'}</p>

      <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          {loading && (
            <div className="empty-state">
              <span className="loading-spinner mb-1" />
              <h2>正在检查工作区</h2>
              <p className="text-xs text-[var(--foreground-muted)]">正在读取未提交改动与建议分支名…</p>
            </div>
          )}

          {!loading && preview && !eligible && (
            <>
              <div className="inline-alert inline-alert--danger" role="alert">
                <div className="flex items-start gap-2 min-w-0 flex-1">
                  <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                  <span className="break-words select-text">
                    {preview.reason || '当前会话暂时无法移到 worktree'}
                  </span>
                </div>
              </div>
              <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
                移到 worktree 要求会话未归档、没有运行中的轮次且队列为空；处理完再打开本页重新预览。
              </p>
            </>
          )}

          {!loading && eligible && preview && (
            <>
              <label className="block space-y-1">
                <span className="text-xs font-semibold text-[var(--foreground)]">分支名</span>
                <input
                  type="text"
                  value={branch}
                  onChange={(event) => setBranch(event.target.value)}
                  placeholder={preview.suggestedBranch || 'mcode/分支名'}
                  disabled={executing}
                  className={`${INPUT_CLASS} disabled:opacity-60`}
                />
                <span className="block text-mc-caption text-[var(--foreground-muted)] break-all">
                  将创建于 {preview.worktreePath || '项目 worktrees 目录'}
                </span>
              </label>

              <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-1.5">
                <p className="text-xs font-semibold text-[var(--foreground)]">
                  待转移改动 · {preview.files.length} 个已跟踪 / {preview.untracked.length} 个未跟踪
                </p>
                {pendingFiles.length === 0 ? (
                  <p className="text-mc-caption text-[var(--foreground-muted)]">工作区干净，仅新建 Worktree 分支。</p>
                ) : (
                  <ul className="space-y-0.5">
                    {pendingFiles.slice(0, MAX_LISTED_FILES).map((file) => (
                      <li key={file} className="font-mono text-mc-caption text-[var(--foreground-muted)] break-all">
                        {file}
                      </li>
                    ))}
                    {pendingFiles.length > MAX_LISTED_FILES && (
                      <li className="text-mc-caption text-[var(--foreground-muted)]">
                        还有 {pendingFiles.length - MAX_LISTED_FILES} 个文件…
                      </li>
                    )}
                  </ul>
                )}
              </div>

              <div className="flex items-center justify-between gap-3 py-1">
                <div className="min-w-0">
                  <p className="text-xs font-semibold text-[var(--foreground)]">转移未提交改动</p>
                  <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
                    关闭时只新建 Worktree，当前工作区的改动留在原处
                  </p>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={transferFiles}
                  aria-label="切换转移未提交改动"
                  disabled={executing || pendingFiles.length === 0}
                  onClick={() => setTransferFiles((previous) => !previous)}
                  className={`relative h-6 w-11 shrink-0 rounded-full transition disabled:opacity-40 ${
                    transferFiles ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'
                  }`}
                >
                  <span
                    className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform ${
                      transferFiles ? 'translate-x-5' : ''
                    }`}
                  />
                </button>
              </div>
            </>
          )}

          {/* 交接提示词：迁不进去时给一段可以直接贴到新会话里的精简说明 */}
          {!loading && (
            <div className="pt-2 border-t border-[var(--border)] space-y-2">
              <div className="flex items-center justify-between gap-2">
                <span className="text-xs font-semibold text-[var(--foreground)]">精简交接提示词</span>
                <button
                  type="button"
                  onClick={() => void handleLimitText()}
                  disabled={busy || limitingText}
                  className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] disabled:opacity-50 transition"
                >
                  {limitingText ? '生成中…' : limitedText ? '重新生成' : '生成'}
                </button>
              </div>
              {limitError && (
                <p className="text-mc-caption text-[var(--danger-foreground)] break-words" role="alert">
                  {limitError}
                </p>
              )}
              {limitedText && (
                <>
                  <textarea
                    value={limitedText}
                    readOnly
                    rows={5}
                    className="pairing-input min-h-24 resize-y w-full min-w-0 text-xs"
                  />
                  <button
                    type="button"
                    onClick={() => void handleCopyLimitedText()}
                    className="min-h-[36px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] transition"
                  >
                    {copied ? '已复制' : '复制提示词'}
                  </button>
                </>
              )}
            </div>
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
          onClick={handleClose}
          disabled={executing}
          className="min-h-[44px] px-3 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] disabled:opacity-50 transition"
        >
          取消
        </button>
        <button
          type="button"
          onClick={() => void handleExecute()}
          disabled={!eligible || busy}
          className="primary-button min-h-[44px] min-w-[88px] disabled:opacity-50"
        >
          {executing ? '移动中…' : '移到 worktree'}
        </button>
      </div>
    </div>
  );
});

/** 兼容外壳：保留原 props，内部用 SheetFrame + MenuSheetHeader 包住内容体。 */
export const WorktreeMoveSheet: React.FC<WorktreeMoveSheetProps> = ({
  isOpen,
  threadID,
  threadTitle,
  onClose,
  onExecuted
}) => {
  const bodyRef = useRef<ThreadMenuPageHandle>(null);

  const requestClose = useCallback(() => {
    if (bodyRef.current?.requestClose() === false) return;
    onClose();
  }, [onClose]);

  if (!isOpen) return null;

  return (
    <SheetFrame
      size="panel"
      labelledBy="worktree-sheet-title"
      onRequestClose={requestClose}
      closeOnEscape
    >
      <MenuSheetHeader
        eyebrow="工作区"
        title="移到 worktree"
        labelledBy="worktree-sheet-title"
        leading="close"
        onLeading={requestClose}
      />
      <WorktreeMoveSheetBody
        ref={bodyRef}
        threadID={threadID}
        threadTitle={threadTitle}
        onClose={onClose}
        onExecuted={onExecuted}
      />
    </SheetFrame>
  );
};
