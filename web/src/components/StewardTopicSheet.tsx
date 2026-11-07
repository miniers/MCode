// 话题操作面板与「交给它」面板：手机上点话题行的「⋯」或总管固定行的菜单打开。
// 按钮出现条件由 StewardTopicRow 的 stewardTopicActions 给出（对齐桌面端 internal/ui/steward_topic_card.go:232），
// 待验收任务的「通过 / 打回」只在 running / paused 阶段出现（internal/ui/steward_topic_card.go:160）。

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { createPortal } from 'react-dom';
import { ConfirmDialog } from './ConfirmDialog';
import { XMarkIcon } from './Icons';
import { stewardTopicActions, stewardTopicPendingCount } from './StewardTopicRow';
import type { StewardTopicActionCaps, StewardTopicActionID } from './StewardTopicRow';
import { useRemoteCall } from '../context/useRemoteCall';
import { stewardActionErrorText as describeError } from '../context/remoteStewardApi';
import type { Board, RemoteStewardTopic, Task } from '../types/generated';

/** 面板里的一条待验收任务，只需要定位与修订号。 */
export interface StewardReviewTask {
  id: string;
  title: string;
  revision: number;
}

/** 从看板里挑出属于该话题目标、状态为验收中的任务（桌面端 st.reviewTasks 的同一口径）。 */
export function pickStewardReviewTasks(board: Board | null, projectID: string, goalID: string): StewardReviewTask[] {
  if (!board || !goalID) return [];
  return board.tasks
    .filter(
      (task: Task) =>
        task.goalID === goalID &&
        (!projectID || task.projectID === projectID) &&
        (task.status === 'review' || task.displayColumn === 'review')
    )
    .map((task) => ({ id: task.id, title: task.title || '未命名任务', revision: task.revision }));
}

/** 拉取话题下的待验收任务：宿主没有 kanban.getBoard 时返回空表，面板不显示该区块。 */
export function useStewardReviewTasks(
  projectID: string,
  goalID: string,
  enabled: boolean
): { tasks: StewardReviewTask[]; loading: boolean; error: string | null } {
  const { call, supportsMethod } = useRemoteCall();
  const supported = enabled && Boolean(goalID) && supportsMethod('kanban.getBoard');
  const [board, setBoard] = useState<Board | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!supported) {
      setBoard(null);
      setError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    void call<Board>('kanban.getBoard', {})
      .then((result) => {
        if (!cancelled) setBoard(result ?? null);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(describeError(err, '读取待验收任务失败'));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [call, supported, projectID, goalID]);

  const tasks = useMemo(
    () => (supported ? pickStewardReviewTasks(board, projectID, goalID) : []),
    [board, goalID, projectID, supported]
  );
  return { tasks, loading, error };
}

export interface StewardTopicSheetProps {
  topic: RemoteStewardTopic;
  /** 阶段文案，由上层用既有的 stewardTopicPhaseLabel 传进来。 */
  phaseLabel: string;
  projectName?: string;
  caps: StewardTopicActionCaps;
  /** 宿主是否提供任务验收与看板读取。 */
  canReviewTask: boolean;
  onClose: () => void;
  /** 执行一个话题动作；抛错时面板显示错误并留在原地。 */
  onRunAction: (id: StewardTopicActionID) => Promise<void>;
  /** 验收一条任务：reject 必须带非空 feedback。 */
  onReviewTask: (
    review: StewardReviewTask,
    decision: 'accept' | 'reject',
    feedback?: string
  ) => Promise<void>;
}

export const StewardTopicSheet: React.FC<StewardTopicSheetProps> = ({
  topic,
  phaseLabel,
  projectName,
  caps,
  canReviewTask,
  onClose,
  onRunAction,
  onReviewTask
}) => {
  const closeRef = React.useRef<HTMLButtonElement | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [rejectTask, setRejectTask] = useState<StewardReviewTask | null>(null);

  const actions = useMemo(() => stewardTopicActions(topic, caps), [caps, topic]);
  const pending = stewardTopicPendingCount(topic);
  const showReviewTasks = canReviewTask && (topic.phase === 'running' || topic.phase === 'paused');
  const { tasks, loading: tasksLoading, error: tasksError } = useStewardReviewTasks(
    topic.projectID,
    topic.goalID,
    showReviewTasks
  );

  useEffect(() => {
    closeRef.current?.focus();
  }, []);

  const runAction = useCallback(
    async (id: StewardTopicActionID) => {
      if (busy) return;
      setBusy(id);
      setError(null);
      try {
        await onRunAction(id);
        onClose();
      } catch (err) {
        setError(describeError(err, '操作失败，请稍后重试'));
      } finally {
        setBusy(null);
      }
    },
    [busy, onClose, onRunAction]
  );

  const runReview = useCallback(
    async (task: StewardReviewTask, decision: 'accept' | 'reject', feedback?: string) => {
      if (busy) return;
      setBusy(task.id);
      setError(null);
      try {
        await onReviewTask(task, decision, feedback);
        setRejectTask(null);
      } catch (err) {
        setError(describeError(err, decision === 'accept' ? '通过任务失败' : '打回任务失败'));
      } finally {
        setBusy(null);
      }
    },
    [busy, onReviewTask]
  );

  const title = topic.title || '未命名话题';

  return (
    <>
      <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
        <section
          className="thread-action-sheet"
          role="dialog"
          aria-modal="true"
          aria-labelledby="steward-topic-sheet-title"
          onMouseDown={(event) => event.stopPropagation()}
        >
          <div className="sheet-handle" aria-hidden="true" />
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <p className="eyebrow">话题</p>
              <h2 id="steward-topic-sheet-title" className="sheet-title truncate">
                {title}
              </h2>
              <p className="mt-1 text-micro text-muted">
                {[projectName, phaseLabel].filter(Boolean).join(' · ')}
              </p>
            </div>
            <button
              ref={closeRef}
              type="button"
              className="icon-button"
              onClick={onClose}
              aria-label="关闭话题操作"
              title="关闭"
            >
              <XMarkIcon className="h-4 w-4" />
            </button>
          </div>

          <p className="mt-2 text-micro text-muted">
            任务 {topic.tasksDone}/{topic.tasksTotal}
            {pending > 0 ? ` · ${pending} 项待处理` : ''}
          </p>

          {actions.length > 0 && (
            <div className="sheet-actions">
              {actions.map((action) => (
                <button
                  key={action.id}
                  type="button"
                  className={`sheet-action${action.danger ? ' sheet-action--danger' : ''}`}
                  disabled={busy !== null}
                  onClick={() => {
                    if (action.confirmText) {
                      setConfirmDelete(true);
                      return;
                    }
                    void runAction(action.id);
                  }}
                >
                  {busy === action.id ? (
                    <span className="loading-spinner loading-spinner--small" />
                  ) : null}
                  <span>{action.label}</span>
                </button>
              ))}
            </div>
          )}

          {showReviewTasks && (
            <div className="mt-4">
              <p className="text-micro font-semibold text-[var(--foreground)]">待验收任务</p>
              {tasksLoading && (
                <p className="mt-1 flex items-center gap-2 text-micro text-muted">
                  <span className="loading-spinner loading-spinner--small" />
                  正在读取…
                </p>
              )}
              {!tasksLoading && tasksError && <p className="mt-1 text-micro text-danger">{tasksError}</p>}
              {!tasksLoading && !tasksError && tasks.length === 0 && (
                <p className="mt-1 text-micro text-muted">暂无待验收任务</p>
              )}
              {tasks.map((task) => (
                <div key={task.id} className="flex min-h-[44px] items-center gap-2">
                  <span className="min-w-0 flex-1 truncate text-footnote">{task.title}</span>
                  <button
                    type="button"
                    className="min-h-[36px] shrink-0 rounded-lg px-2 text-micro font-medium text-[var(--foreground)] hover:bg-row-hover disabled:opacity-50"
                    disabled={busy !== null}
                    onClick={() => void runReview(task, 'accept')}
                  >
                    {busy === task.id ? '处理中…' : '通过'}
                  </button>
                  <button
                    type="button"
                    className="min-h-[36px] shrink-0 rounded-lg px-2 text-micro font-medium text-danger hover:bg-row-hover disabled:opacity-50"
                    disabled={busy !== null}
                    onClick={() => setRejectTask(task)}
                  >
                    打回
                  </button>
                </div>
              ))}
            </div>
          )}

          {error && <p className="mt-3 text-micro text-danger">{error}</p>}
        </section>
      </div>

      <ConfirmDialog
        isOpen={confirmDelete}
        title="删除话题"
        message={`确定删除「${title}」吗？目标与在跑的任务会一起消失，且无法撤销。`}
        confirmLabel="删除"
        confirmVariant="danger"
        isLoading={busy === 'delete'}
        onConfirm={() => {
          setConfirmDelete(false);
          void runAction('delete');
        }}
        onCancel={() => setConfirmDelete(false)}
      />

      <ConfirmDialog
        isOpen={rejectTask !== null}
        title="打回任务"
        message={`打回「${rejectTask?.title ?? ''}」后要把修改意见写清楚，总管会按它返工。`}
        confirmLabel="打回"
        confirmVariant="danger"
        withFeedbackInput
        feedbackPlaceholder="请写清要改什么（必填）"
        isLoading={busy !== null && busy === rejectTask?.id}
        onConfirm={(feedback) => {
          if (!rejectTask) return;
          const trimmed = (feedback ?? '').trim();
          if (trimmed === '') {
            setError('打回必须填写原因');
            return;
          }
          void runReview(rejectTask, 'reject', trimmed);
        }}
        onCancel={() => setRejectTask(null)}
      />
    </>
  );
};

export interface StewardStartTopicSheetProps {
  /** 归属项目名，只用于标题下的一行说明。 */
  projectName?: string;
  busy: boolean;
  error: string | null;
  onClose: () => void;
  onSubmit: (request: string) => void;
}

/** 「交给它」面板：写一句需求就开话题，对齐桌面端 internal/ui/steward_topic_card.go:464 的入口。 */
export const StewardStartTopicSheet: React.FC<StewardStartTopicSheetProps> = ({
  projectName,
  busy,
  error,
  onClose,
  onSubmit
}) => {
  const [request, setRequest] = useState('');
  const [localError, setLocalError] = useState<string | null>(null);
  const inputRef = React.useRef<HTMLTextAreaElement | null>(null);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  return createPortal(
    <div className="sheet-layer" role="presentation" onMouseDown={busy ? undefined : onClose}>
      <section
        className="thread-action-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="steward-start-sheet-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="eyebrow">交给它</p>
            <h2 id="steward-start-sheet-title" className="sheet-title">
              交给总管
            </h2>
            {projectName && <p className="mt-1 text-micro text-muted">{projectName}</p>}
          </div>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            disabled={busy}
            aria-label="关闭交给总管"
            title="关闭"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <label className="settings-label mt-3 block" htmlFor="steward-start-request">
          想让它做什么
        </label>
        <textarea
          id="steward-start-request"
          ref={inputRef}
          rows={4}
          value={request}
          disabled={busy}
          onChange={(event) => {
            setRequest(event.target.value);
            setLocalError(null);
          }}
          placeholder="一句话说清目标，例如：把登录页改成新样式"
          className="sheet-select min-h-[96px] w-full resize-y"
        />

        {(localError || error) && <p className="mt-2 text-micro text-danger">{localError || error}</p>}

        <button
          type="button"
          className="primary-button primary-button--wide mt-3"
          disabled={busy}
          onClick={() => {
            const trimmed = request.trim();
            if (trimmed === '') {
              setLocalError('先写一句想让它做什么');
              return;
            }
            onSubmit(trimmed);
          }}
        >
          {busy ? '正在交办…' : '交给它'}
        </button>
      </section>
    </div>,
    document.body
  );
};
