// 移动端会话级与按轮 Diff 审阅抽屉组件

import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import type { RemoteReviewTurn, TaskItem, ThreadsGetDiffResult } from '../types/contract';
import { useRemoteStatic } from '../context/RemoteContext';
import { DiffViewer } from './DiffViewer';
import { ConfirmDialog } from './ConfirmDialog';
import {
  deleteReviewComment,
  formatCommentsFeedback,
  getReviewComments,
  saveReviewComment,
  type DiffComment,
  type ParsedDiffLine,
  type ParsedFileDiff
} from '../utils/diff';
import { setTaskRejectFeedbackPrefill } from './TaskDetailModal';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';
import { useRemoteReviewApi, reviewContentHash, reviewHunkHash, type ReviewScope } from '../context/remoteReviewApi';
import type { RemoteThreadsReviewGetResult } from '../types/generated';

export interface ThreadReviewSheetProps {
  isOpen: boolean;
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
  isHelper?: boolean;
  linkedTask?: TaskItem | null;
  onAppendToDraft?: (feedback: string) => void;
}

/** 内容体 props：去掉 isOpen，入栈时挂载、出栈即卸载。 */
export type ThreadReviewSheetBodyProps = Omit<ThreadReviewSheetProps, 'isOpen'>;

/** 整会话 diff 的缓存可用时长；工作区可能已变，超过后命中先渲染缓存再静默刷新 */
const WHOLE_DIFF_CACHE_TTL_MS = 30 * 1000;

/** 缓存键：整会话用固定后缀 all，轮次用 turnID，同一会话内互不冲突 */
function diffCacheKey(threadID: string, turnID: string | null): string {
  return `${threadID}:${turnID || 'all'}`;
}

function getTurnStatusLabel(status: RemoteReviewTurn['status']): { label: string; className: string } {
  switch (status) {
    case 'running':
      return { label: '运行中', className: 'text-sky-600 dark:text-sky-400 font-semibold' };
    case 'completed':
      return { label: '已完成', className: 'text-slate-600 dark:text-zinc-300' };
    case 'failed':
      return { label: '失败', className: 'text-rose-600 dark:text-rose-400 font-semibold' };
    case 'interrupted':
      return { label: '已中断', className: 'text-amber-600 dark:text-amber-400' };
    default:
      return { label: status || '未知', className: 'text-slate-400 dark:text-zinc-500' };
  }
}

export const ThreadReviewSheetBody = forwardRef<ThreadMenuPageHandle, ThreadReviewSheetBodyProps>(
  function ThreadReviewSheetBody(
    {
      onClose,
      threadID,
      isHelper = false,
      linkedTask,
      onAppendToDraft
    },
    ref
  ) {
    const { capabilities, connectionState, getThreadDiff } = useRemoteStatic();
    const reviewApi = useRemoteReviewApi();
    const isConnected = connectionState === 'connected';
    // 宿主是否同时声明了审阅能力；作为拉取 effect 的依赖，能力上线/下线才重新拉取
    const reviewSupported = Boolean(
      capabilities?.methods?.includes('threads.review.get') ||
        (capabilities?.features?.includes('threads.review') && capabilities?.methods?.includes('threads.getDiff'))
    );

    const [loading, setLoading] = useState(false);
    const [error, setError] = useState<string | null>(null);
    const [diffResult, setDiffResult] = useState<ThreadsGetDiffResult | null>(null);
    const [reviewSnapshot, setReviewSnapshot] = useState<RemoteThreadsReviewGetResult | null>(null);
    const [reviewError, setReviewError] = useState<string | null>(null);
    const [revertingHunk, setRevertingHunk] = useState(false);
    const [turns, setTurns] = useState<RemoteReviewTurn[]>([]);
    const [selectedTurnID, setSelectedTurnID] = useState<string | null>(null);

    // 评论状态与列表
    const [reviewComments, setReviewComments] = useState<DiffComment[]>([]);
    const commentsMap = useMemo(() => {
      const map: Record<string, DiffComment> = {};
      for (const c of reviewComments) {
        map[`${c.filePath}:${c.lineId}`] = c;
      }
      return map;
    }, [reviewComments]);

    // 二次确认弹窗状态
    const [confirmDialog, setConfirmDialog] = useState<{
      isOpen: boolean;
      title: string;
      message: string;
      confirmLabel: string;
      confirmVariant: 'primary' | 'danger' | 'success';
      action: () => void;
    }>({
      isOpen: false,
      title: '',
      message: '',
      confirmLabel: '确认',
      confirmVariant: 'primary',
      action: () => {}
    });

    const requestIdRef = useRef(0);
    const reviewRequestIdRef = useRef(0);
    const mountedRef = useRef(true);
    // 当前选中轮次的镜像：effect 与稳定回调里读取，避免把它们绑到 selectedTurnID 上
    const selectedTurnIDRef = useRef<string | null>(null);

    // 数据层缓存：
    // - 键为 `${threadID}:${turnID || 'all'}`（diffCacheKey），轮次结果与整会话结果分开存；
    // - 某一轮的 diff 产出后即固定，命中直接用，不发请求（不设 TTL）；
    // - 整会话结果会随工作区变化，命中后最多沿用 WHOLE_DIFF_CACHE_TTL_MS，超过则先渲染缓存再静默刷新；
    //   抽屉重新打开或断线重连时一律“先渲染缓存 + 后台刷新”，避免整会话总是整段重拉、也不闪 loading；
    // - threadID 变化（切换会话）立即清空，避免跨会话串数据；
    // - 截断（truncated）后的“重新获取”走 force 绕过缓存强制重拉。
    const resultCacheRef = useRef(new Map<string, { result: ThreadsGetDiffResult; at: number }>());
    // 同一缓存键的请求进行中时复用同一个 Promise，避免重复发 RPC
    const inFlightRef = useRef(new Map<string, Promise<ThreadsGetDiffResult>>());
    // 最近一次整会话结果里的轮次列表镜像：用来判断某轮是否还在运行（运行中 diff 会变，不写入缓存）
    const turnsRef = useRef<RemoteReviewTurn[]>([]);

    const fetchReviewSnapshot = useCallback(async (targetTurnID: string | null) => {
      const requestID = ++reviewRequestIdRef.current;
      if (!capabilities?.methods.includes('threads.review.get')) {
        setReviewSnapshot(null);
        setReviewError(null);
        return;
      }
      if (!isConnected) {
        setReviewSnapshot(null);
        setReviewError(null);
        return;
      }
      try {
        const scope: ReviewScope = targetTurnID ? 'turn' : 'session';
        const result = await reviewApi.get({ threadID, scope, ...(targetTurnID ? { turnID: targetTurnID } : {}) });
        if (requestID !== reviewRequestIdRef.current) return;
        if (result.unavailable) {
          setReviewSnapshot(result);
          setReviewError(result.unavailable);
          return;
        }
        setReviewSnapshot(result);
        setReviewError(null);
      } catch (err) {
        if (requestID !== reviewRequestIdRef.current) return;
        setReviewSnapshot(null);
        setReviewError(err instanceof Error ? err.message : '获取审阅快照失败');
      }
    }, [capabilities, isConnected, reviewApi, threadID]);

    useEffect(() => {
      mountedRef.current = true;
      return () => {
        mountedRef.current = false;
      };
    }, []);

    // 同步刷新本地评论状态
    const syncComments = useCallback((targetTurnID: string | null) => {
      const loaded = getReviewComments(threadID, targetTurnID);
      setReviewComments(loaded);
    }, [threadID]);

    // 把拉取到（或缓存命中）的结果写入视图状态；轮次列表只由整会话结果更新
    const applyResult = useCallback((result: ThreadsGetDiffResult, isWholeSession: boolean) => {
      setDiffResult(result);
      setError(result.failureReason ?? null);
      if (isWholeSession && Array.isArray(result.turns)) {
        setTurns(result.turns);
        turnsRef.current = result.turns;
      }
    }, []);

    // 同键请求去重：进行中直接复用 Promise，结束后清空占位
    const requestDiff = useCallback(
      (key: string, targetTurnID: string | null) => {
        const pending = inFlightRef.current.get(key);
        if (pending) return pending;
        // 同键只会有一个在途请求（上面的复用保证），所以 finally 里可以无条件删除
        const request = getThreadDiff(threadID, targetTurnID || undefined).finally(() => {
          inFlightRef.current.delete(key);
        });
        inFlightRef.current.set(key, request);
        return request;
      },
      [getThreadDiff, threadID]
    );

    // 拉取 Diff 结果：先查缓存，未命中再发请求，请求代际防回踩
    // options.force 绕过缓存强制重拉（截断后的“重新获取”）；
    // options.revalidate 命中缓存后仍发起一次后台静默刷新（断线重连、重开抽屉）。
    const fetchDiff = useCallback(
      async (
        targetTurnID: string | null,
        options: { force?: boolean; revalidate?: boolean } = {}
      ) => {
        // 宿主能力检查：未同时声明 threads.review 与 threads.getDiff 时不发请求
        if (!reviewSupported) {
          setError('当前宿主未声明审阅能力 (threads.review / threads.getDiff)');
          return;
        }

        if (!isConnected) {
          setError('未连接到 Mac，无法获取代码变更');
          return;
        }

        const isWholeSession = !targetTurnID;
        // 运行中的轮次 diff 还会继续增长，不写入缓存，避免切回来看到冻结的旧结果
        const isRunningTurn =
          !isWholeSession &&
          turnsRef.current.some((t) => t.turnID === targetTurnID && t.status === 'running');
        const key = diffCacheKey(threadID, targetTurnID);
        const cached = options.force ? undefined : resultCacheRef.current.get(key);
        // 轮次 diff 不会变化，视为永远新鲜；整会话结果超过 TTL 就需要后台刷新
        const cacheFresh = cached
          ? !isWholeSession || Date.now() - cached.at < WHOLE_DIFF_CACHE_TTL_MS
          : false;

        if (cached) {
          // 命中缓存：先渲染，不进入 loading（重开抽屉不闪空白）
          applyResult(cached.result, isWholeSession);
          if (cacheFresh && !options.revalidate) return;
        }

        const requestId = ++requestIdRef.current;
        // 有缓存可显示时不打断当前内容，改为静默后台刷新
        const silent = Boolean(cached);
        if (!silent) {
          setLoading(true);
          setError(null);
        }

        try {
          const result = await requestDiff(key, targetTurnID);
          // 失败结果与运行中轮次的结果不入缓存，避免把瞬时状态固化成缓存命中
          if (!result.failureReason && !isRunningTurn) {
            resultCacheRef.current.set(key, { result, at: Date.now() });
          }
          if (!mountedRef.current || requestId !== requestIdRef.current) return;
          applyResult(result, isWholeSession);
        } catch (err) {
          if (!mountedRef.current || requestId !== requestIdRef.current) return;
          setError(err instanceof Error ? err.message : '获取代码变更失败');
        } finally {
          if (mountedRef.current && requestId === requestIdRef.current) {
            setLoading(false);
          }
        }
      },
      [applyResult, isConnected, requestDiff, reviewSupported, threadID]
    );

    // 缓存失效：切换会话（threadID 变化）时清空，避免跨会话串数据
    useEffect(() => {
      resultCacheRef.current.clear();
      inFlightRef.current.clear();
    }, [threadID]);

    // 内容体只在入栈时挂载：挂载与更换会话时重置选中轮次与评论
    // 这里不依赖 isConnected / capabilities / fetchDiff：断线重连既不清空已选轮次，也不清空缓存
    useEffect(() => {
      setSelectedTurnID(null);
      selectedTurnIDRef.current = null;
      syncComments(null);
    }, [syncComments]);

    // 用 ref 转存 fetchDiff，让下面的拉取 effect 只依赖真正需要响应的状态（开关、会话、连接、能力），
    // 不会因为 fetchDiff 函数身份变化（父级重渲染）而多发请求。
    const fetchDiffRef = useRef(fetchDiff);
    useEffect(() => {
      fetchDiffRef.current = fetchDiff;
    }, [fetchDiff]);

    // 拉取当前视图（整会话或已选轮次）：挂载、切换会话、连接状态或宿主能力变化时调用。
    // 命中缓存时先渲染再后台刷新，因此重连后选中的轮次不丢，也不会整段重拉整会话。
    useEffect(() => {
      void fetchDiffRef.current(selectedTurnIDRef.current, { revalidate: true });
    }, [isConnected, reviewSupported, threadID]);

    useEffect(() => {
      void fetchReviewSnapshot(selectedTurnIDRef.current);
      return () => { reviewRequestIdRef.current++; };
    }, [capabilities, fetchReviewSnapshot, isConnected, selectedTurnID, threadID]);

    // 无关闭守卫：评论草稿直接丢弃，确认弹窗自己管，允许出栈
    useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

    // 切换轮次：命中缓存直接渲染，不再发 RPC
    const handleSelectTurn = (turnID: string | null) => {
      if (loading && selectedTurnID === turnID) return;
      setSelectedTurnID(turnID);
      selectedTurnIDRef.current = turnID;
      syncComments(turnID);
      void fetchDiff(turnID);
    };

    // 重试与截断后的“重新获取”：绕过缓存强制重拉
    const handleRetryDiff = useCallback(() => {
      void fetchDiff(selectedTurnIDRef.current, { force: true });
      void fetchReviewSnapshot(selectedTurnIDRef.current);
    }, [fetchDiff, fetchReviewSnapshot]);

    const handleRevertHunk = useCallback((file: ParsedFileDiff, startLine: number, lineCount: number) => {
      const snapshot = reviewSnapshot;
      const metadata = snapshot?.files.find((item) => item.path === file.displayPath);
      if (!reviewApi.revertHunkSupported || !snapshot || !metadata || snapshot.running || snapshot.unavailable) return;
      setConfirmDialog({
        isOpen: true,
        title: '撤销此块？',
        message: '',
        confirmLabel: '撤销',
        confirmVariant: 'danger',
        action: () => {
          setConfirmDialog((prev) => ({ ...prev, isOpen: false }));
          setRevertingHunk(true);
          void (async () => {
            try {
              const scope = snapshot.scope;
              const fileSnapshot = await reviewApi.get({
                threadID, scope, ...(scope === 'turn' ? { turnID: snapshot.turnID } : {}), path: metadata.path
              });
              const content = fileSnapshot.file;
              if (fileSnapshot.unavailable || fileSnapshot.running || !content || content.binary || content.tooLarge || content.missing) {
                throw new Error(fileSnapshot.unavailable || '审阅快照不可用');
              }
              const [hunkHash, expectedFileHash] = await Promise.all([
                reviewHunkHash(content.newText, startLine, lineCount),
                reviewContentHash(content.newText)
              ]);
              await reviewApi.revertHunk({
                threadID, scope, ...(scope === 'turn' ? { turnID: fileSnapshot.turnID } : {}),
                path: metadata.path, baseRevision: fileSnapshot.baseRevision, headRevision: fileSnapshot.headRevision,
                startLine, lineCount, hunkHash, expectedFileHash
              });
              resultCacheRef.current.delete(diffCacheKey(threadID, selectedTurnIDRef.current));
              inFlightRef.current.delete(diffCacheKey(threadID, selectedTurnIDRef.current));
              await fetchReviewSnapshot(selectedTurnIDRef.current);
              await fetchDiff(selectedTurnIDRef.current, { force: true });
            } catch (err) {
              const message = err instanceof Error ? err.message : '撤销改动块失败';
              await fetchReviewSnapshot(selectedTurnIDRef.current);
              await fetchDiff(selectedTurnIDRef.current, { force: true });
              setReviewError(message);
            } finally {
              setRevertingHunk(false);
            }
          })();
        }
      });
    }, [fetchDiff, fetchReviewSnapshot, reviewApi, reviewSnapshot, threadID]);

    // 添加评论
    const handleAddComment = useCallback(
      (line: ParsedDiffLine, file: ParsedFileDiff, content: string) => {
        const newComment: DiffComment = {
          id: `${file.displayPath}:${line.id}`,
          filePath: file.displayPath,
          lineId: line.id,
          oldLineNumber: line.oldLineNumber,
          newLineNumber: line.newLineNumber,
          lineType: line.type === 'del' ? 'del' : line.type === 'add' ? 'add' : 'context',
          lineText: line.text,
          content,
          createdAt: Date.now()
        };
        const updated = saveReviewComment(threadID, selectedTurnID, newComment);
        setReviewComments(updated);
      },
      [selectedTurnID, threadID]
    );

    // 删除评论
    const handleDeleteComment = useCallback(
      (commentId: string) => {
        const updated = deleteReviewComment(threadID, selectedTurnID, commentId);
        setReviewComments(updated);
      },
      [selectedTurnID, threadID]
    );

    // 格式化汇总反馈
    const formattedFeedback = useMemo(() => {
      return formatCommentsFeedback(reviewComments);
    }, [reviewComments]);

    // 处理追加到会话草稿
    const handleConfirmAppendToDraft = () => {
      if (isHelper || reviewComments.length === 0 || !formattedFeedback) return;
      setConfirmDialog({
        isOpen: true,
        title: '追加审阅反馈到草稿',
        message: `确定将 ${reviewComments.length} 条行内评论追加到当前会话输入框草稿中吗？\n（追加后可在输入框中核对与修改，由你确认后手动发送）`,
        confirmLabel: '追加到草稿',
        confirmVariant: 'primary',
        action: () => {
          onAppendToDraft?.(formattedFeedback);
          setConfirmDialog((prev) => ({ ...prev, isOpen: false }));
          onClose();
        }
      });
    };

    // 处理用于任务退回
    const handleConfirmUseForTaskReturn = () => {
      if (!linkedTask || reviewComments.length === 0 || !formattedFeedback) return;
      setConfirmDialog({
        isOpen: true,
        title: '用于任务退回反馈',
        message: `确定将 ${reviewComments.length} 条审阅评论预填用于退回任务「${linkedTask.title}」吗？\n提交将跳转至任务详情进行确认。`,
        confirmLabel: '用于退回',
        confirmVariant: 'danger',
        action: () => {
          setTaskRejectFeedbackPrefill(linkedTask.id, formattedFeedback, true);
          setConfirmDialog((prev) => ({ ...prev, isOpen: false }));
          onClose();
          window.location.hash = `#/task/${encodeURIComponent(linkedTask.id)}`;
        }
      });
    };

    return (
      <div className="flex flex-col flex-1 min-h-0 min-w-0">
        {/* 轮次选择栏 */}
        <div className="shrink-0 mb-3 space-y-1.5">
          <div className="flex items-center justify-between text-mc-caption text-[var(--foreground-muted)]">
            <span>选择审阅范围</span>
            {selectedTurnID ? (
              <button
                type="button"
                onClick={() => handleSelectTurn(null)}
                className="text-[var(--primary)] hover:underline"
              >
                查看整会话
              </button>
            ) : (
              <span>当前：整会话全量 Diff</span>
            )}
          </div>

          <div className="flex items-center gap-1.5 overflow-x-auto pb-1 no-scrollbar">
            <button
              type="button"
              onClick={() => handleSelectTurn(null)}
              className={`min-h-[32px] px-3 py-1 rounded-full text-xs font-medium border shrink-0 transition ${
                selectedTurnID === null
                  ? 'border-[var(--primary)] bg-[var(--primary)] text-white shadow-sm'
                  : 'border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))]'
              }`}
            >
              整会话
            </button>

            {turns.map((turn) => {
              const statusInfo = getTurnStatusLabel(turn.status);
              const isSelected = selectedTurnID === turn.turnID;

              return (
                <button
                  key={turn.turnID}
                  type="button"
                  onClick={() => handleSelectTurn(turn.turnID)}
                  className={`min-h-[32px] px-3 py-1 rounded-full text-xs font-medium border shrink-0 flex items-center gap-1.5 transition ${
                    isSelected
                      ? 'border-[var(--primary)] bg-[var(--primary)] text-white shadow-sm'
                      : 'border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))]'
                  }`}
                >
                  <span>第 {turn.index} 轮</span>
                  <span
                    className={`text-mc-caption ${
                      isSelected ? 'text-white/80' : statusInfo.className
                    }`}
                  >
                    ({statusInfo.label})
                  </span>
                  {turn.hasChanges && (
                    <span
                      className={`w-1.5 h-1.5 rounded-full shrink-0 ${
                        isSelected ? 'bg-white' : 'bg-emerald-500'
                      }`}
                      title="有变更"
                    />
                  )}
                </button>
              );
            })}
          </div>
        </div>

        {/* 审阅 Diff 主体滚动展示区 */}
        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5">
          <DiffViewer
            diffText={diffResult?.diff}
            truncated={diffResult?.truncated}
            metadataTruncated={diffResult?.metadataTruncated}
            filesChanged={diffResult?.filesChanged}
            failureReason={error || reviewError || diffResult?.failureReason}
            onRetry={handleRetryDiff}
            comments={commentsMap}
            onAddComment={handleAddComment}
            onDeleteComment={handleDeleteComment}
            onRevertHunk={reviewApi.revertHunkSupported && !revertingHunk && reviewSnapshot && !reviewSnapshot.running ? handleRevertHunk : undefined}
            canRevertHunk={(file) => Boolean(
              reviewSnapshot?.files.some((item) => item.path === file.displayPath && !item.binary && !item.tooLarge && !item.missing && !item.origPath && item.status !== 'A' && item.status !== 'D' && item.status !== 'R')
            )}
          />
        </div>

        {/* 底部评论汇总与反馈操作栏 */}
        <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 pb-0 min-w-0 max-w-full w-full overflow-x-hidden space-y-2">
          <div className="flex items-center justify-between text-xs text-[var(--foreground-muted)]">
            <span>
              {reviewComments.length > 0
                ? `当前已编写 ${reviewComments.length} 条审阅评论`
                : '点击代码行末「＋」可添加行内意见'}
            </span>
            {reviewComments.length > 0 && isHelper && (
              <span className="text-mc-caption text-[var(--warning-foreground)]">
                辅助会话为只读态，无法发送
              </span>
            )}
          </div>

          <div className="flex flex-wrap items-center justify-end gap-2">
            <button
              type="button"
              onClick={onClose}
              className="secondary-button"
            >
              关闭
            </button>

            {linkedTask && (
              <button
                type="button"
                disabled={reviewComments.length === 0}
                onClick={handleConfirmUseForTaskReturn}
                className="secondary-button text-[var(--danger-foreground)] disabled:opacity-40"
                title={
                  reviewComments.length === 0
                    ? '请先添加至少一条代码行审阅评论'
                    : '预填到任务退回界面'
                }
              >
                用于任务退回
                {reviewComments.length > 0 ? ` (${reviewComments.length})` : ''}
              </button>
            )}

            {!isHelper && (
              <button
                type="button"
                disabled={reviewComments.length === 0}
                onClick={handleConfirmAppendToDraft}
                className="primary-button disabled:opacity-40"
                title={
                  reviewComments.length === 0
                    ? '请先添加至少一条代码行审阅评论'
                    : '将评论整理为反馈追加到输入框'
                }
              >
                追加到会话草稿
                {reviewComments.length > 0 ? ` (${reviewComments.length})` : ''}
              </button>
            )}
          </div>
        </div>

        <ConfirmDialog
          isOpen={confirmDialog.isOpen}
          title={confirmDialog.title}
          message={confirmDialog.message}
          confirmLabel={confirmDialog.confirmLabel}
          confirmVariant={confirmDialog.confirmVariant}
          onConfirm={confirmDialog.action}
          onCancel={() => setConfirmDialog((prev) => ({ ...prev, isOpen: false }))}
        />
      </div>
    );
  }
);

