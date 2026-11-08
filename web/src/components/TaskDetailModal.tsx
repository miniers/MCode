// 任务与目标详情 sheet：所有写入都由宿主能力与 revision 约束。

import React, { useEffect, useRef, useState } from 'react';
import type { Goal, GoalStatus, TaskItem, TaskStatus } from '../types/contract';
import { useRemoteStatic } from '../context/RemoteContext';
import { DiffViewer } from './DiffViewer';
import { ConfirmDialog } from './ConfirmDialog';
import {
  CheckCircleIcon,
  ChevronRightIcon,
  CodeBracketIcon,
  DocumentTextIcon,
  ExclamationTriangleIcon,
  EyeIcon,
  LockIcon,
  MessageIcon,
  PlayIcon,
  SparklesIcon,
  XMarkIcon
} from './Icons';
import { MarkdownView } from './MarkdownView';
import { formatRelativeTime } from '../utils/date';

interface TaskDetailModalProps {
  task: TaskItem;
  projectName?: string;
  onClose: () => void;
  onOpenThread?: (threadID: string) => void;
  onOpenReviewThread?: (threadID: string) => void;
  onOpenGoal?: (goalID: string) => void;
}

interface GoalDetailModalProps {
  goal: Goal;
  projectName?: string;
  tasks: TaskItem[];
  onClose: () => void;
  onOpenTask: (task: TaskItem) => void;
  onOpenThread?: (threadID: string) => void;
  onOpenReviewThread?: (threadID: string) => void;
}

const GOAL_STATUS_LABELS: Record<GoalStatus, string> = {
  planning: '规划中',
  plan_failed: '规划失败',
  plan_review: '方案待审',
  running: '运行中',
  paused: '暂停',
  completed: '完成',
  cancelled: '已取消'
};

const TASK_STATUS_LABELS: Record<TaskStatus, string> = {
  todo: '待办',
  in_progress: '进行中',
  needs_approval: '等你',
  waiting: '等待中',
  review: '待审',
  done: '完成',
  rejected: '失败',
  failed: '失败',
  cancelled: '已取消'
};

function taskStatusLabel(status: string): string {
  return TASK_STATUS_LABELS[status as TaskStatus] ?? '未知状态';
}

function goalStatusLabel(status: string): string {
  return GOAL_STATUS_LABELS[status as GoalStatus] ?? '未知状态';
}

function actionErrorMessage(error: unknown): string {
  const message = error instanceof Error ? error.message : '操作失败';
  if (message.includes('状态待确认')) return `请求超时，结果待确认。${message}`;
  if (message.includes('版本冲突')) return `两端状态已变化，已刷新看板。${message}`;
  return message;
}

// 供审阅面板预填退回反馈的运行态缓存
const taskRejectFeedbackPrefills = new Map<string, { feedback: string; autoOpen?: boolean }>();

export function setTaskRejectFeedbackPrefill(
  taskID: string,
  feedback: string,
  autoOpen = false
): void {
  taskRejectFeedbackPrefills.set(taskID, { feedback, autoOpen });
}

export function getTaskRejectFeedbackPrefill(taskID: string): string | null {
  return taskRejectFeedbackPrefills.get(taskID)?.feedback ?? null;
}

export function clearTaskRejectFeedbackPrefill(taskID: string): void {
  taskRejectFeedbackPrefills.delete(taskID);
}

function SheetFrame({
  title,
  eyebrow,
  onClose,
  children,
  footer
}: {
  title: string;
  eyebrow: string;
  onClose: () => void;
  children: React.ReactNode;
  footer?: React.ReactNode;
}) {
  const sheetRef = useRef<HTMLElement>(null);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    sheetRef.current?.focus();
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  return (
    <div
      className="sheet-layer"
      role="presentation"
      onMouseDown={onClose}
    >
      <section
        ref={sheetRef}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        className="settings-sheet flex flex-col outline-none w-full max-w-full min-w-0 overflow-x-hidden"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />
        <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">{eyebrow}</p>
            <h2 className="mt-1 break-words [overflow-wrap:anywhere] text-headline font-bold text-foreground">{title}</h2>
          </div>
          <button type="button" onClick={onClose} aria-label="关闭" title="关闭" className="icon-button shrink-0">
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>
        <div className="mt-4 min-h-0 flex-1 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full">{children}</div>
        {footer && <div className="mt-4 shrink-0 border-t border-border pt-3.5 pb-0 min-w-0 max-w-full w-full overflow-x-hidden">{footer}</div>}
      </section>
    </div>
  );
}

export const TaskDetailModal: React.FC<TaskDetailModalProps> = ({
  task,
  projectName,
  onClose,
  onOpenThread,
  onOpenReviewThread,
  onOpenGoal
}) => {
  const {
    connectionState,
    activeActionTaskID,
    actionLoadingText,
    isSyncing,
    moveTask,
    startTask,
    reviewTask,
    refreshBoard,
    retryGoalTask,
    skipGoalTask
  } = useRemoteStatic();
  const [showDiff, setShowDiff] = useState(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [rejectDraft, setRejectDraft] = useState(() => getTaskRejectFeedbackPrefill(task.id) || '');

  useEffect(() => {
    const prefill = taskRejectFeedbackPrefills.get(task.id);
    if (prefill?.feedback) {
      setRejectDraft(prefill.feedback);
      if (prefill.autoOpen && task.canApprove) {
        taskRejectFeedbackPrefills.set(task.id, { feedback: prefill.feedback, autoOpen: false });
        setConfirmState({
          isOpen: true,
          title: '退回并留下反馈',
          message: '退回会把任务交还给执行流程。若两端状态冲突，反馈会保留，刷新后可重新提交。',
          confirmLabel: '退回任务',
          confirmVariant: 'danger',
          withFeedbackInput: true,
          action: async (feedback) => {
            setRejectDraft(feedback ?? '');
            const succeeded = await runConfirmed(() => reviewTask(task.id, 'reject', feedback, task.revision));
            if (succeeded) {
              setRejectDraft('');
              clearTaskRejectFeedbackPrefill(task.id);
            }
          }
        });
      }
    }
  }, [task.id, task.canApprove]);
  const [confirmState, setConfirmState] = useState<{
    isOpen: boolean;
    title: string;
    message: string;
    confirmLabel: string;
    confirmVariant: 'primary' | 'danger' | 'success';
    withFeedbackInput?: boolean;
    action: (feedback?: string) => Promise<void>;
  }>({
    isOpen: false,
    title: '',
    message: '',
    confirmLabel: '',
    confirmVariant: 'primary',
    action: async () => {}
  });

  const isDisconnected = connectionState !== 'connected';
  const isBusy = activeActionTaskID === task.id;
  const isActionDisabled = isBusy || isDisconnected || isSyncing;

  const refreshAfterConflict = async () => {
    await refreshBoard().catch(() => undefined);
  };

  // 失败后关闭确认框，让错误在 sheet 内可见；下次打开用刷新后的 revision 重新确认，不自动重放。
  const runConfirmed = async (operation: () => Promise<void>): Promise<boolean> => {
    setErrorMessage(null);
    try {
      await operation();
      setConfirmState((previous) => ({ ...previous, isOpen: false }));
      return true;
    } catch (error) {
      setConfirmState((previous) => ({ ...previous, isOpen: false }));
      setErrorMessage(actionErrorMessage(error));
      await refreshAfterConflict();
      return false;
    }
  };

  const handleStart = () => {
    setConfirmState({
      isOpen: true,
      title: '启动任务确认',
      message: `确定启动「${task.title}」吗？启动只会请求 Mac 执行，不会在手机端编辑计划。`,
      confirmLabel: '启动',
      confirmVariant: 'primary',
      action: async () => {
        await runConfirmed(() => startTask(task.id, task.revision));
      }
    });
  };

  const handleRequestReview = async () => {
    setErrorMessage(null);
    try {
      await reviewTask(task.id, 'requestReview', undefined, task.revision);
    } catch (error) {
      setErrorMessage(actionErrorMessage(error));
      await refreshAfterConflict();
    }
  };

  const handleApprove = () => {
    setConfirmState({
      isOpen: true,
      title: '验收通过确认',
      message: `确定验收通过「${task.title}」吗？验收只改变任务状态，不合并或推送代码。`,
      confirmLabel: '验收通过',
      confirmVariant: 'success',
      action: async () => {
        await runConfirmed(() => reviewTask(task.id, 'accept', undefined, task.revision));
      }
    });
  };

  const handleReject = () => {
    setConfirmState({
      isOpen: true,
      title: '退回并留下反馈',
      message: '退回会把任务交还给执行流程。若两端状态冲突，反馈会保留，刷新后可重新提交。',
      confirmLabel: '退回任务',
      confirmVariant: 'danger',
      withFeedbackInput: true,
      action: async (feedback) => {
        setRejectDraft(feedback ?? '');
        const succeeded = await runConfirmed(() => reviewTask(task.id, 'reject', feedback, task.revision));
        if (succeeded) {
          setRejectDraft('');
          clearTaskRejectFeedbackPrefill(task.id);
        }
      }
    });
  };

  const handleMove = async (status: TaskStatus) => {
    setErrorMessage(null);
    try {
      await moveTask(task.id, status, task.revision);
    } catch (error) {
      setErrorMessage(actionErrorMessage(error));
      await refreshAfterConflict();
    }
  };

  const handleGoalTaskAction = async (action: 'retry' | 'skip') => {
    setErrorMessage(null);
    try {
      if (action === 'retry') await retryGoalTask(task.id, task.revision);
      else await skipGoalTask(task.id, task.revision);
    } catch (error) {
      setErrorMessage(actionErrorMessage(error));
      await refreshAfterConflict();
    }
  };

  const availableMoveTargets: TaskStatus[] = task.status === 'review' || task.status === 'rejected' || task.status === 'waiting' || task.status === 'failed' || task.status === 'cancelled'
    ? ['todo']
    : (['todo', 'review', 'done'] as TaskStatus[]).filter((status) => status !== task.status);

  return (
    <>
      <SheetFrame title={task.title} eyebrow="任务详情" onClose={onClose} footer={(
        <div className="flex flex-wrap items-center justify-end gap-2 min-w-0 max-w-full">
          {task.canStart && (
            <button type="button" disabled={isActionDisabled || task.blocked} onClick={handleStart} className="primary-button" data-host-actionable="true">
              <PlayIcon className="h-4 w-4" />启动
            </button>
          )}
          {task.canReview && (
            <button type="button" disabled={isActionDisabled} onClick={() => void handleRequestReview()} className="secondary-button text-warning" data-host-actionable="true">
              <EyeIcon className="h-4 w-4" />请求复审
            </button>
          )}
          {task.canApprove && (
            <>
              <button type="button" disabled={isActionDisabled} onClick={handleReject} className="secondary-button text-danger" data-host-actionable="true"><XMarkIcon className="h-4 w-4" />退回</button>
              <button type="button" disabled={isActionDisabled} onClick={handleApprove} className="primary-button" data-host-actionable="true"><CheckCircleIcon className="h-4 w-4" />验收</button>
            </>
          )}
          {task.status === 'rejected' && task.goalID && (
            <>
              <button type="button" disabled={isActionDisabled} onClick={() => void handleGoalTaskAction('retry')} className="primary-button" data-host-actionable="true"><PlayIcon className="h-4 w-4" />重试</button>
              <button type="button" disabled={isActionDisabled} onClick={() => void handleGoalTaskAction('skip')} className="secondary-button" data-host-actionable="true">跳过</button>
            </>
          )}
          {isBusy && <span className="text-xs text-status-running">{actionLoadingText || '处理中…'}</span>}
        </div>
      )}>
        {(isDisconnected || isSyncing) && (
          <div className="floating-status" role="status">
            <div className={`floating-status__pill${isDisconnected ? ' floating-status__pill--warning' : ''}`}>
              {isDisconnected ? <><ExclamationTriangleIcon className="h-4 w-4 shrink-0" /><span>当前连接已断开，写入操作已禁用。</span></> : <><span className="loading-spinner loading-spinner--small shrink-0" /><span>正在同步最新状态，请稍候再操作。</span></>}
            </div>
          </div>
        )}
        <div className="space-y-5 min-w-0 max-w-full w-full overflow-x-hidden">
          {errorMessage && <div className="inline-alert inline-alert--danger min-w-0 break-words" role="alert"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" /><span className="min-w-0 break-words [overflow-wrap:anywhere]">{errorMessage}</span></div>}

          <div className="flex flex-wrap items-center gap-2 text-xs text-muted min-w-0">
            <span className="thread-status">{taskStatusLabel(task.status)}</span>
            {projectName && <span className="truncate max-w-[200px]">{projectName}</span>}
            <span>更新于 {formatRelativeTime(task.updatedAt)}</span>
            {task.blocked && <span className="inline-flex items-center gap-1 text-warning"><LockIcon className="h-3.5 w-3.5 shrink-0" />等待 {Math.max(task.dependsOn.length, 1)} 项依赖</span>}
          </div>

          <section className="space-y-2 min-w-0" aria-labelledby="task-summary-title">
            <div className="flex items-center gap-1.5 text-xs font-semibold text-muted"><DocumentTextIcon className="h-4 w-4 shrink-0" /><h3 id="task-summary-title">摘要与说明</h3></div>
            <div className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-border bg-card-alt p-3.5 text-sm leading-relaxed text-foreground min-w-0">{task.notes || '暂无额外说明。'}</div>
          </section>

          {(task.acceptanceCriteria || (task.rejectCount ?? 0) > 0) && (
            <section className="space-y-2 min-w-0" aria-labelledby="task-acceptance-title">
              <div className="flex items-center gap-1.5 text-xs font-semibold text-muted"><CheckCircleIcon className="h-4 w-4 shrink-0" /><h3 id="task-acceptance-title">验收标准</h3></div>
              <div className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-border bg-card-alt p-3.5 text-sm leading-relaxed text-foreground min-w-0">
                {task.acceptanceCriteria || '未填写验收标准。'}
              </div>
              {(task.rejectCount ?? 0) > 0 && (
                <p className="text-xs text-danger">已打回 {task.rejectCount} 次</p>
              )}
            </section>
          )}

          {(task.reviewSummary || task.reviewFeedback || task.lastFailureReason) && (
            <section className="space-y-2 min-w-0" aria-labelledby="task-feedback-title">
              <div className="flex items-center gap-1.5 text-xs font-semibold text-muted"><SparklesIcon className="h-4 w-4 shrink-0" /><h3 id="task-feedback-title">执行与反馈</h3></div>
              {task.reviewSummary && <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-primary/20 bg-primary/5 p-3 text-sm text-foreground min-w-0">{task.reviewSummary}</p>}
              {task.reviewFeedback && <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-warning/20 bg-warning/5 p-3 text-sm text-foreground min-w-0">{task.reviewFeedback}</p>}
              {task.lastFailureReason && <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-danger/20 bg-danger/5 p-3 text-sm text-danger min-w-0">失败原因：{task.lastFailureReason}</p>}
            </section>
          )}

          {(task.threadID || task.reviewThreadID || task.goalID) && (
            <section className="flex flex-wrap items-center gap-2 border-y border-border py-3 min-w-0">
              {task.threadID && onOpenThread && <button type="button" className="secondary-button" onClick={() => onOpenThread(task.threadID!)}><CodeBracketIcon className="h-4 w-4" />打开执行会话</button>}
              {task.reviewThreadID && onOpenReviewThread && <button type="button" className="secondary-button" onClick={() => onOpenReviewThread(task.reviewThreadID!)}><EyeIcon className="h-4 w-4" />打开审查会话</button>}
              {task.goalID && onOpenGoal && <button type="button" className="secondary-button" onClick={() => onOpenGoal(task.goalID!)}><SparklesIcon className="h-4 w-4" />打开目标</button>}
            </section>
          )}

          {task.dependsOn.length > 0 && <section className="space-y-2 min-w-0"><h3 className="text-xs font-semibold text-muted">依赖阻塞</h3><p className="text-sm text-muted">此任务等待 {task.dependsOn.length} 项任务完成；手机端不编辑依赖关系。</p></section>}

          <section className="space-y-2 min-w-0 max-w-full w-full overflow-hidden">
            <button type="button" className="secondary-button w-full justify-between" onClick={() => setShowDiff((value) => !value)}>
              <span className="inline-flex items-center gap-2"><CodeBracketIcon className="h-4 w-4 text-primary" />Diff</span>
              <span>{showDiff ? '收起' : '查看'}</span>
            </button>
            {showDiff && <DiffViewer taskID={task.id} defaultCollapsedWhenOver={200} />}
          </section>

          {task.canMove && <section className="space-y-2 border-t border-border pt-4 min-w-0"><h3 className="text-xs font-semibold text-muted">移动到</h3><div className="flex flex-wrap gap-2">{availableMoveTargets.map((status) => <button key={status} type="button" disabled={isActionDisabled} onClick={() => void handleMove(status)} className="secondary-button" data-host-actionable="true"><ChevronRightIcon className="h-3.5 w-3.5" />{taskStatusLabel(status)}</button>)}</div></section>}
        </div>
      </SheetFrame>
      <ConfirmDialog
        isOpen={confirmState.isOpen}
        title={confirmState.title}
        message={confirmState.message}
        confirmLabel={confirmState.confirmLabel}
        confirmVariant={confirmState.confirmVariant}
        isLoading={isBusy}
        withFeedbackInput={confirmState.withFeedbackInput}
        initialFeedback={rejectDraft}
        onConfirm={(feedback) => void confirmState.action(feedback)}
        onCancel={() => setConfirmState((previous) => ({ ...previous, isOpen: false }))}
      />
    </>
  );
};

export const GoalDetailModal: React.FC<GoalDetailModalProps> = ({
  goal,
  projectName,
  tasks,
  onClose,
  onOpenTask,
  onOpenThread,
  onOpenReviewThread
}) => {
  const {
    connectionState,
    activeActionTaskID,
    actionLoadingText,
    isSyncing,
    startGoal,
    pauseGoal,
    cancelGoal,
    replanGoal,
    refreshBoard
  } = useRemoteStatic();
  const [previewAcknowledged, setPreviewAcknowledged] = useState(false);
  const [feedback, setFeedback] = useState('');
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [cancelOpen, setCancelOpen] = useState(false);
  const isDisconnected = connectionState !== 'connected';
  const isBusy = activeActionTaskID === goal.id;
  const disabled = isDisconnected || isSyncing || isBusy;
  const goalTasks = tasks.filter((task) => task.goalID === goal.id);
  const stalled =
    goal.status === 'running' &&
    goal.progress.running === 0 &&
    goal.progress.ready === 0 &&
    goal.progress.done < goal.progress.total;
  const hasOutputs =
    ['running', 'paused', 'completed'].includes(goal.status) &&
    goalTasks.some((task) => Boolean(task.reviewSummary?.trim()));

  const runGoalAction = async (action: () => Promise<void>): Promise<boolean> => {
    setErrorMessage(null);
    try {
      await action();
      return true;
    } catch (error) {
      setErrorMessage(actionErrorMessage(error));
      await refreshBoard().catch(() => undefined);
      return false;
    }
  };

  const handleStart = () => {
    if (goal.status === 'plan_review' && !previewAcknowledged) {
      setErrorMessage('请先阅读只读计划预览与依赖，再启动目标。');
      return;
    }
    void runGoalAction(() => startGoal(goal.id, goal.revision));
  };

  const handleReplan = () => {
    if (!feedback.trim()) {
      setErrorMessage('请先填写重规划反馈。');
      return;
    }
    void runGoalAction(() => replanGoal(goal.id, feedback.trim(), goal.revision)).then((succeeded) => {
      if (succeeded) setFeedback('');
    });
  };

  const handleCancel = async () => {
    await runGoalAction(() => cancelGoal(goal.id, goal.revision));
    setCancelOpen(false);
  };

  return (
    <>
      <SheetFrame title={goal.title} eyebrow="目标详情" onClose={onClose} footer={(
        <div className="flex flex-wrap items-center justify-end gap-2 min-w-0 max-w-full">
          {goal.planThreadID && onOpenThread && (
            <button
              type="button"
              className="secondary-button"
              onClick={() => onOpenThread(goal.planThreadID!)}
              title="可以在这里询问进度、追加任务"
            >
              <MessageIcon className="h-4 w-4" />
              {goal.status === 'planning' || goal.status === 'plan_failed' ? '规划对话' : 'Goal 对话'}
            </button>
          )}
          {goal.status === 'plan_review' || goal.status === 'paused' ? <button type="button" disabled={disabled || (goal.status === 'plan_review' && !previewAcknowledged)} onClick={handleStart} className="primary-button" data-host-actionable="true"><PlayIcon className="h-4 w-4" />{goal.status === 'paused' ? '继续' : '启动'}</button> : null}
          {goal.status === 'running' && <button type="button" disabled={disabled} onClick={() => void runGoalAction(() => pauseGoal(goal.id, goal.revision))} className="secondary-button" data-host-actionable="true"><EyeIcon className="h-4 w-4" />暂停</button>}
          {(goal.status === 'plan_failed' || goal.status === 'plan_review') && <button type="button" disabled={disabled || !feedback.trim()} onClick={handleReplan} className="secondary-button" data-host-actionable="true"><SparklesIcon className="h-4 w-4" />重新规划</button>}
          {!['completed', 'cancelled'].includes(goal.status) && <button type="button" disabled={disabled} onClick={() => setCancelOpen(true)} className="secondary-button text-danger" data-host-actionable="true"><XMarkIcon className="h-4 w-4" />取消</button>}
          {isBusy && <span className="text-xs text-status-running">{actionLoadingText || '处理中…'}</span>}
        </div>
      )}>
        <div className="space-y-5 min-w-0 max-w-full w-full overflow-x-hidden">
          {errorMessage && <div className="inline-alert inline-alert--danger min-w-0 break-words" role="alert"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" /><span className="min-w-0 break-words [overflow-wrap:anywhere]">{errorMessage}</span></div>}
          {isDisconnected && <div className="inline-alert inline-alert--warning min-w-0 break-words" role="status"><ExclamationTriangleIcon className="h-4 w-4 shrink-0" /><span>当前连接已断开，写入操作已禁用。</span></div>}
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted min-w-0"><span className="thread-status">{goalStatusLabel(goal.status)}</span>{projectName && <span className="truncate max-w-[200px]">{projectName}</span>}<span>{goal.progress.done}/{goal.progress.total} 完成</span>{goal.progress.failed > 0 && <span className="text-danger">失败 {goal.progress.failed}</span>}{goal.progress.blocked > 0 && <span className="text-warning">依赖阻塞 {goal.progress.blocked}</span>}{goal.progress.waitingOnYou > 0 && <span>等你 {goal.progress.waitingOnYou}</span>}</div>

          <section className="space-y-2 min-w-0">
            <h3 className="text-xs font-semibold text-muted">目标策略</h3>
            <div className="rounded-xl border border-primary/20 bg-primary/5 p-3 text-sm leading-relaxed text-foreground break-words [overflow-wrap:anywhere] min-w-0">
              {goal.policy.maxConcurrency === 1 ? '串行执行' : `最多同时执行 ${goal.policy.maxConcurrency} 项`} · {goal.policy.previewPlan ? '先预览计划' : '直接执行'} · {goal.policy.autoAccept ? '自动验收：完成后自动标记完成' : '需人工验收：完成后进入待审'} · 最多 {goal.policy.maxAttempts} 次尝试
            </div>
          </section>

          <section className="space-y-2 min-w-0">
            <h3 className="text-xs font-semibold text-muted">目标描述</h3>
            <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] rounded-xl border border-border bg-card-alt p-3.5 text-sm leading-relaxed text-foreground min-w-0">{goal.prompt}</p>
            {stalled && (
              <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm text-warning min-w-0">
                卡住了：有任务失败或在等你处理，没有可运行的任务
              </p>
            )}
            {goal.statusReason && <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm text-muted min-w-0">{goal.statusReason}</p>}
            {goal.planSummary && <p className="whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm text-foreground min-w-0">{goal.planSummary}</p>}
          </section>

          {goal.status === 'plan_review' && (
            <section className="space-y-3 rounded-xl border border-primary/25 bg-primary/5 p-3.5 min-w-0" aria-labelledby="goal-preview-title">
              <div><h3 id="goal-preview-title" className="text-sm font-semibold text-foreground">只读计划预览</h3><p className="mt-1 text-xs text-muted">查看任务与依赖后才能启动；计划内容不在手机端逐项编辑。</p></div>
              <div className="space-y-2 min-w-0">
                {goalTasks.length === 0 && <p className="text-xs text-muted">当前快照尚未返回计划任务。</p>}
                {goalTasks.map((task) => <button key={task.id} type="button" className="flex w-full items-center justify-between gap-3 border-b border-border-subtle py-2 text-left last:border-0 min-w-0" onClick={() => onOpenTask(task)}><span className="min-w-0 truncate text-sm text-foreground">{task.title}</span><span className="shrink-0 text-micro text-muted">{task.dependsOn.length ? `依赖 ${task.dependsOn.length} 项` : '无依赖'}</span></button>)}
              </div>
              <label className="flex items-start gap-2 text-xs text-foreground min-w-0"><input type="checkbox" checked={previewAcknowledged} onChange={(event) => setPreviewAcknowledged(event.target.checked)} className="shrink-0 mt-0.5" /><span>我已阅读当前计划与依赖。</span></label>
            </section>
          )}

          {(goal.status === 'plan_failed' || goal.status === 'plan_review') && <section className="space-y-2 min-w-0"><label htmlFor="goal-replan-feedback" className="text-xs font-semibold text-muted">规划反馈</label><textarea id="goal-replan-feedback" value={feedback} onChange={(event) => setFeedback(event.target.value)} className="pairing-input min-h-24 resize-y w-full min-w-0" placeholder="告诉 Mac 需要如何调整规划" disabled={disabled} /></section>}

          {hasOutputs && (
            <section className="space-y-2 min-w-0">
              <h3 className="text-xs font-semibold text-muted">产出</h3>
              <div className="space-y-3 min-w-0">
                {goalTasks.map((task) => {
                  if (!task.reviewSummary?.trim()) return null;
                  return (
                    <div
                      key={task.id}
                      className="rounded-xl border border-border bg-card-alt p-3.5 space-y-2.5 min-w-0"
                    >
                      <div className="flex items-center justify-between gap-3 min-w-0">
                        <button
                          type="button"
                          className="min-w-0 truncate text-sm font-semibold text-foreground text-left hover:underline"
                          onClick={() => onOpenTask(task)}
                        >
                          {task.title}
                        </button>
                        <span className="shrink-0 text-xs text-muted">
                          {taskStatusLabel(task.status)}
                        </span>
                      </div>
                      <div className="text-sm text-foreground break-words [overflow-wrap:anywhere] min-w-0">
                        <MarkdownView text={task.reviewSummary} />
                      </div>
                      {task.threadID && (
                        <div className="flex flex-wrap items-center gap-2 pt-1">
                          {onOpenThread && (
                            <button
                              type="button"
                              className="secondary-button text-xs py-1 px-2.5 min-h-0 inline-flex items-center gap-1"
                              onClick={() => onOpenThread(task.threadID!)}
                            >
                              <CodeBracketIcon className="h-3.5 w-3.5" />
                              <span>打开会话</span>
                            </button>
                          )}
                          {onOpenReviewThread && (
                            <button
                              type="button"
                              className="secondary-button text-xs py-1 px-2.5 min-h-0 inline-flex items-center gap-1"
                              onClick={() => onOpenReviewThread(task.threadID!)}
                            >
                              <EyeIcon className="h-3.5 w-3.5" />
                              <span>查看改动</span>
                            </button>
                          )}
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </section>
          )}

          <section className="space-y-2 min-w-0">
            <h3 className="text-xs font-semibold text-muted">目标任务</h3>
            {goalTasks.length === 0 ? <p className="text-sm text-muted">暂无任务快照。</p> : goalTasks.map((task) => <button key={task.id} type="button" className="flex w-full items-center justify-between gap-3 border-b border-border-subtle py-3 text-left last:border-0 min-w-0" onClick={() => onOpenTask(task)}><span className="min-w-0 truncate text-sm text-foreground">{task.title}</span><span className="shrink-0 text-xs text-muted">{taskStatusLabel(task.status)}{task.lastFailureReason ? ' · 失败' : ''}</span></button>)}
          </section>
        </div>
      </SheetFrame>
      <ConfirmDialog
        isOpen={cancelOpen}
        title="取消目标？"
        message="取消会中断正在运行的任务，并使目标进入终态。此操作需要 Mac 确认，不会自动重放。"
        confirmLabel="确认取消"
        confirmVariant="danger"
        isLoading={isBusy}
        onConfirm={() => void handleCancel()}
        onCancel={() => setCancelOpen(false)}
      />
    </>
  );
};
