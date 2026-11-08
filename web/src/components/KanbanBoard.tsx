// 看板首页：沿用会话列表的分区与行样式，展示宿主派生的五列。

import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import type { Goal, GoalStatus, KanbanDisplayColumn, TaskItem } from '../types/contract';
import type {
  KanbanDeleteTaskParams,
  KanbanRescueTaskParams,
  KanbanTaskMutationResult
} from '../types/generated';
import { useRemoteStatic } from '../context/RemoteContext';
import { makeActionID } from '../context/remoteGuards';
import { RpcError } from '../services/rpc';
import { ConfirmDialog } from './ConfirmDialog';
import { GoalOutputsSheet } from './GoalOutputsSheet';
import {
  KanbanQuickAdd,
  KanbanTaskActionSheet,
  kanbanTaskNeedsInterrupt
} from './KanbanTaskActions';
import {
  CheckCircleIcon,
  ChevronDownIcon,
  CodeBracketIcon,
  EllipsisHorizontalIcon,
  ExclamationTriangleIcon,
  EyeIcon,
  LockIcon,
  PlayIcon,
  SparklesIcon,
  XMarkIcon
} from './Icons';
import { formatRelativeTime } from '../utils/date';

const COLUMN_ORDER: KanbanDisplayColumn[] = [
  'needs_you',
  'review',
  'in_progress',
  'todo',
  'done'
];

const COLUMN_LABELS: Record<KanbanDisplayColumn, string> = {
  needs_you: '待你处理',
  review: '待审',
  in_progress: '进行中',
  todo: '待办',
  done: '完成'
};

const GOAL_STATUS_LABELS: Record<GoalStatus, string> = {
  planning: '规划中',
  plan_failed: '规划失败',
  plan_review: '方案待审',
  running: '运行中',
  paused: '暂停',
  completed: '完成',
  cancelled: '已取消'
};

interface KanbanBoardProps {
  onSelectTask?: (task: TaskItem) => void;
  onSelectGoal?: (goal: Goal) => void;
}

interface TaskRowProps {
  task: TaskItem;
  projectName?: string;
  goal?: Goal;
  onOpen: () => void;
  onOpenGoal?: () => void;
  onOpenThread?: (threadID: string) => void;
  onOpenReviewThread?: (threadID: string) => void;
  onRetry?: () => void;
  onSkip?: () => void;
  onOpenActions?: () => void;
  actionBusy?: boolean;
  actionBusyText?: string | null;
}

function SectionHeading({ title, count, onClick, expanded = true }: {
  title: string;
  count: number;
  onClick?: () => void;
  expanded?: boolean;
}) {
  const content = (
    <div className="flex items-center gap-2.5 gutter-x pt-4 pb-1 text-label font-medium text-muted">
      {onClick && <ChevronDownIcon className={`h-3.5 w-3.5 shrink-0 transition-transform ${expanded ? 'rotate-180' : ''}`} />}
      <span className="shrink-0">{title}</span>
      <span className="h-px flex-1 bg-border-subtle" aria-hidden="true" />
      {onClick && <span className="text-micro font-normal text-muted">{count}</span>}
    </div>
  );
  return onClick ? (
    <button type="button" className="w-full text-left" onClick={onClick} aria-expanded={expanded}>
      {content}
    </button>
  ) : (
    content
  );
}

function goalStatusLabel(status: string): string {
  return GOAL_STATUS_LABELS[status as GoalStatus] ?? '未知状态';
}

function taskStateLabel(task: TaskItem): string {
  if (task.status === 'waiting') return '等待中';
  if (task.status === 'cancelled') return '已取消';
  if (task.lastFailureReason || task.status === 'rejected' || task.status === 'failed') return '失败';
  if (task.blocked) return `等待 ${Math.max(task.dependsOn.length, 1)} 项`;
  return COLUMN_LABELS[task.displayColumn] ?? '未知状态';
}

function taskStateClass(task: TaskItem): string {
  if (task.lastFailureReason || task.status === 'rejected' || task.status === 'failed') return 'thread-status thread-status--danger';
  if (task.blocked || task.displayColumn === 'needs_you') return 'thread-status thread-status--warning';
  if (task.displayColumn === 'review') return 'thread-status thread-status--primary';
  if (task.displayColumn === 'in_progress') return 'thread-status thread-status--running';
  return 'thread-status';
}

/**
 * 按依赖关系用 Kahn 算法给规划任务分层：第 1 层是无依赖任务，
 * 有环时剩余任务并入最后一层。dependsOn 里指向集合外的 ID 视为不存在。
 */
export function planLayers(tasks: TaskItem[]): TaskItem[][] {
  const index = new Map<string, number>();
  tasks.forEach((task, position) => index.set(task.id, position));
  const dependents: number[][] = tasks.map(() => []);
  const indegree = tasks.map(() => 0);
  tasks.forEach((task, position) => {
    task.dependsOn.forEach((depID) => {
      const dependency = index.get(depID);
      if (dependency === undefined || dependency === position) return;
      dependents[dependency].push(position);
      indegree[position] += 1;
    });
  });

  const placed = tasks.map(() => false);
  let frontier = tasks.map((_, position) => position).filter((position) => indegree[position] === 0);
  const layers: TaskItem[][] = [];
  while (frontier.length > 0) {
    const layer: TaskItem[] = [];
    const next: number[] = [];
    frontier.forEach((position) => {
      if (placed[position]) return;
      placed[position] = true;
      layer.push(tasks[position]);
      dependents[position].forEach((dependent) => {
        indegree[dependent] -= 1;
        if (indegree[dependent] === 0 && !placed[dependent]) next.push(dependent);
      });
    });
    layers.push(layer);
    frontier = next;
  }

  const rest = tasks.filter((_, position) => !placed[position]);
  if (rest.length > 0) layers.push(rest);
  return layers;
}

const TaskRow: React.FC<TaskRowProps> = ({
  task,
  projectName,
  goal,
  onOpen,
  onOpenGoal,
  onOpenThread,
  onOpenReviewThread,
  onRetry,
  onSkip,
  onOpenActions,
  actionBusy = false,
  actionBusyText = null
}) => {
  const { activeActionTaskID, actionLoadingText } = useRemoteStatic();
  const busy = actionBusy || activeActionTaskID === task.id;
  const busyText = actionBusy ? actionBusyText : actionLoadingText;
  const secondary = [projectName, goal?.title, task.lastActivityText]
    .filter(Boolean)
    .join(' · ');

  return (
    <div className="thread-row-wrap">
      <div
        className={`relative z-10 min-h-[76px] bg-screen gutter-x py-2.5 transition-colors hover:bg-row-hover active:bg-row-hover cursor-pointer ${busy ? 'opacity-60' : ''}`}
        role="button"
        tabIndex={0}
        aria-label={`打开任务 ${task.title}`}
        onClick={onOpen}
        onKeyDown={(event) => {
          if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            onOpen();
          }
        }}
      >
        <div className="flex items-start justify-between gap-2 min-w-0">
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 items-center gap-1.5">
              {task.blocked && <LockIcon className="h-3.5 w-3.5 shrink-0 text-warning" />}
              <h3 className="text-body font-medium text-foreground line-clamp-2 text-left">{task.title}</h3>
            </div>
            {secondary && <p className="mt-1 text-label text-muted">{secondary}</p>}
            {(task.lastFailureReason || task.attentionFlags.length > 0) && (
              <p className="mt-0.5 text-label text-danger">
                {task.lastFailureReason || task.attentionFlags.join(' · ')}
              </p>
            )}
            {busy && <p className="mt-0.5 text-label text-status-running">{busyText || '处理中…'}</p>}
          </div>
          <div className="flex items-center gap-1.5 shrink-0">
            <span className={taskStateClass(task)}>{taskStateLabel(task)}</span>
            {task.goalID && onOpenGoal && (
              <a
                href={`#/goal/${encodeURIComponent(task.goalID)}`}
                className="row-action-button text-primary"
                onClick={(event) => {
                  event.stopPropagation();
                  onOpenGoal();
                }}
                aria-label={`打开目标 ${goal?.title || task.goalID}`}
                title="打开目标"
              >
                <SparklesIcon className="h-4 w-4" />
              </a>
            )}
            {task.threadID && onOpenThread && (
              <button
                type="button"
                className="row-action-button"
                onClick={(event) => {
                  event.stopPropagation();
                  onOpenThread(task.threadID!);
                }}
                aria-label="打开执行会话"
                title="打开执行会话"
              >
                <CodeBracketIcon className="h-4 w-4" />
              </button>
            )}
            {onOpenActions && (
              <button
                type="button"
                className="row-action-button"
                onClick={(event) => {
                  event.stopPropagation();
                  onOpenActions();
                }}
                aria-label={`任务 ${task.title} 操作`}
                aria-haspopup="dialog"
                title="任务操作"
                data-host-actionable="true"
              >
                <EllipsisHorizontalIcon className="h-4 w-4" />
              </button>
            )}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2 pt-1 text-micro text-muted">
          <span>更新于 {formatRelativeTime(task.updatedAt)}</span>
          {task.dependsOn.length > 0 && <span>依赖 {task.dependsOn.length} 项</span>}
          {task.reviewThreadID && onOpenReviewThread && (
            <button
              type="button"
              className="text-primary hover:underline"
              onClick={(event) => {
                event.stopPropagation();
                onOpenReviewThread(task.reviewThreadID!);
              }}
            >
              打开审查会话
            </button>
          )}
          {task.status === 'rejected' && onRetry && (
            <button type="button" className="text-primary hover:underline" onClick={(event) => { event.stopPropagation(); onRetry(); }} data-host-actionable="true">
              重试
            </button>
          )}
          {task.status === 'rejected' && onSkip && (
            <button type="button" className="text-muted hover:underline" onClick={(event) => { event.stopPropagation(); onSkip(); }} data-host-actionable="true">
              跳过
            </button>
          )}
        </div>
      </div>
      <div className="gutter-ml h-px bg-border-subtle" />
    </div>
  );
};

interface GoalRowProps {
  goal: Goal;
  menuOpen: boolean;
  onOpen: () => void;
  onOpenOutputs: () => void;
  onToggleMenu: () => void;
}

const GoalRow: React.FC<GoalRowProps> = ({ goal, menuOpen, onOpen, onOpenOutputs, onToggleMenu }) => {
  const { activeActionTaskID, actionLoadingText } = useRemoteStatic();
  const busy = activeActionTaskID === goal.id;
  const progress = goal.progress;
  const status = goalStatusLabel(goal.status);
  const stalled =
    goal.status === 'running' &&
    progress.running === 0 &&
    progress.ready === 0 &&
    progress.done < progress.total;

  return (
    <div className="relative border-b border-border-subtle bg-card-alt">
      <div className="flex min-h-[72px] items-start gap-3 gutter-x py-3">
        <button type="button" className="min-w-0 flex-1 text-left" onClick={onOpen} data-host-actionable="false">
          <div className="flex items-center gap-2">
            <SparklesIcon className="h-4 w-4 shrink-0 text-primary" />
            <span className="truncate text-sm font-semibold text-foreground">{goal.title}</span>
            <span className="shrink-0 text-mc-caption text-muted">{status}</span>
            {stalled && <span className="thread-status thread-status--warning shrink-0 text-mc-caption">卡住</span>}
          </div>
          <div className="mt-1 flex items-center gap-2 text-mc-caption text-muted">
            <div className="h-1.5 min-w-20 flex-1 overflow-hidden rounded-full bg-border-subtle" aria-label={`目标进度 ${progress.done}/${progress.total}`}>
              <div className="h-full rounded-full bg-primary" style={{ width: `${Math.max(0, Math.min(1, progress.fraction)) * 100}%` }} />
            </div>
            <span className="shrink-0">{progress.done}/{progress.total}</span>
            <span className="shrink-0">{goal.policy.autoAccept ? '自动验收' : '需人工验收'}</span>
          </div>
          {(progress.failed > 0 || progress.blocked > 0 || progress.waitingOnYou > 0) && (
            <p className="mt-1 text-mc-caption text-muted">
              {[
                progress.failed > 0 ? `失败 ${progress.failed}` : null,
                progress.blocked > 0 ? `依赖阻塞 ${progress.blocked}` : null,
                progress.waitingOnYou > 0 ? `等你 ${progress.waitingOnYou}` : null
              ].filter(Boolean).join(' · ')}
            </p>
          )}
          {goal.status === 'plan_failed' && goal.statusReason && (
            <p className="mt-1 text-mc-caption text-danger">{goal.statusReason}</p>
          )}
          {busy && <p className="mt-1 text-micro text-status-running">{actionLoadingText || '处理中…'}</p>}
        </button>
        <button
          type="button"
          className="shrink-0 rounded-lg px-2 py-1 text-mc-caption font-medium text-primary transition-colors hover:bg-row-hover"
          onClick={(event) => {
            event.stopPropagation();
            onOpenOutputs();
          }}
          aria-label={`目标 ${goal.title} 的目标产物`}
          title="目标产物"
          data-host-actionable="true"
        >
          目标产物
        </button>
        <button
          type="button"
          className="row-action-button"
          onClick={(event) => {
            event.stopPropagation();
            onToggleMenu();
          }}
          aria-label={`目标 ${goal.title} 操作`}
          aria-expanded={menuOpen}
          title="目标操作"
          data-host-actionable="true"
        >
          <EllipsisHorizontalIcon className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
};

interface PlanEdge {
  id: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
}

interface GoalPlanReviewDialogProps {
  goal: Goal;
  tasks: TaskItem[];
  busy: boolean;
  error: string | null;
  onDismissError: () => void;
  onOpenTask: (task: TaskItem) => void;
  onStart: () => void;
  onReplan: (feedback: string) => void;
  onClose: () => void;
}

/**
 * 规划审查弹窗：任务按依赖拓扑分层展示，层与层之间用 SVG 连线标出依赖方向，
 * 可点击单卡进入编辑，也可直接开始执行或打回重做。
 */
const GoalPlanReviewDialog: React.FC<GoalPlanReviewDialogProps> = ({
  goal,
  tasks,
  busy,
  error,
  onDismissError,
  onOpenTask,
  onStart,
  onReplan,
  onClose
}) => {
  const [replanOpen, setReplanOpen] = useState(false);
  const [feedback, setFeedback] = useState('');
  const layers = useMemo(() => planLayers(tasks), [tasks]);
  const numbers = useMemo(() => {
    const map = new Map<string, number>();
    tasks.forEach((task, position) => map.set(task.id, position + 1));
    return map;
  }, [tasks]);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cardRefs = useRef(new Map<string, HTMLDivElement>());
  const [edges, setEdges] = useState<PlanEdge[]>([]);

  const planning = goal.status === 'planning';
  const planAwaitingReview = goal.status === 'plan_review';
  const canStart = planAwaitingReview && tasks.length > 0;

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        onClose();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onClose]);

  // 卡片换行与高度随内容变化，依赖连线按实际位置重算
  useLayoutEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const measure = () => {
      const base = container.getBoundingClientRect();
      const next: PlanEdge[] = [];
      tasks.forEach((task) => {
        const target = cardRefs.current.get(task.id);
        if (!target) return;
        const targetBox = target.getBoundingClientRect();
        task.dependsOn.forEach((depID) => {
          const source = cardRefs.current.get(depID);
          if (!source) return;
          const sourceBox = source.getBoundingClientRect();
          next.push({
            id: `${depID}->${task.id}`,
            x1: sourceBox.left + sourceBox.width / 2 - base.left,
            y1: sourceBox.bottom - base.top,
            x2: targetBox.left + targetBox.width / 2 - base.left,
            y2: targetBox.top - base.top
          });
        });
      });
      setEdges(next);
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const observer = new ResizeObserver(measure);
    observer.observe(container);
    return () => observer.disconnect();
  }, [tasks, layers]);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        role="dialog"
        aria-modal="true"
        aria-label="审查规划"
        className="settings-sheet flex flex-col outline-none w-full max-w-full min-w-0 overflow-x-hidden"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />
        <div className="flex shrink-0 items-start justify-between gap-3 min-w-0 w-full">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">审查规划</p>
            <h2 className="mt-1 break-words [overflow-wrap:anywhere] text-headline font-bold text-foreground">{goal.title || goal.prompt}</h2>
            {(goal.planSummary || goal.statusReason) && (
              <p className="mt-1 whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-sm text-muted min-w-0">{goal.planSummary || goal.statusReason}</p>
            )}
          </div>
          <button type="button" onClick={onClose} aria-label="关闭" title="关闭" className="icon-button shrink-0">
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        {error && (
          <div className="inline-alert inline-alert--danger shrink-0" role="alert">
            <span className="min-w-0 break-words">{error}</span>
            <button type="button" className="inline-alert__action" onClick={onDismissError}>关闭</button>
          </div>
        )}

        <div className="mt-4 min-h-0 flex-1 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full">
          {planning ? (
            <div className="empty-state" role="status"><span className="loading-spinner" /><span>规划中，请稍候…</span></div>
          ) : tasks.length === 0 ? (
            <p className="py-6 text-center text-sm text-muted">规划未产出任务</p>
          ) : (
            <div ref={containerRef} className="relative space-y-4">
              <svg className="pointer-events-none absolute inset-0 z-0 h-full w-full text-muted" aria-hidden="true">
                {edges.map((edge) => (
                  <path
                    key={edge.id}
                    d={`M ${edge.x1} ${edge.y1} C ${edge.x1} ${edge.y1 + 18}, ${edge.x2} ${edge.y2 - 18}, ${edge.x2} ${edge.y2}`}
                    fill="none"
                    stroke="currentColor"
                    strokeOpacity="0.5"
                    strokeWidth="1"
                    strokeDasharray="3 3"
                  />
                ))}
              </svg>
              {layers.map((layer, layerIndex) => (
                <section key={layerIndex} className="relative z-10 space-y-2">
                  <p className="text-micro text-muted">第 {layerIndex + 1} 层 · {layer.length} 项</p>
                  <div className="space-y-2">
                    {layer.map((task) => {
                      const depLabels = task.dependsOn
                        .map((depID) => numbers.get(depID))
                        .filter((value): value is number => value !== undefined)
                        .sort((a, b) => a - b)
                        .map((value) => `#${value}`);
                      return (
                        <div
                          key={task.id}
                          ref={(node) => {
                            if (node) cardRefs.current.set(task.id, node);
                            else cardRefs.current.delete(task.id);
                          }}
                          className="rounded-xl border border-border-subtle bg-card-alt p-2.5"
                        >
                          <button
                            type="button"
                            className="flex w-full items-center gap-2 text-left"
                            onClick={() => onOpenTask(task)}
                            aria-label={`打开任务 ${task.title}`}
                          >
                            <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/15 text-mc-caption font-semibold text-primary">
                              {numbers.get(task.id)}
                            </span>
                            <span className="min-w-0 flex-1 text-sm text-foreground">{task.title}</span>
                          </button>
                          {depLabels.length > 0 && (
                            <p className="mt-1 pl-7 text-micro text-muted">依赖：{depLabels.join('、')}</p>
                          )}
                        </div>
                      );
                    })}
                  </div>
                </section>
              ))}
            </div>
          )}
        </div>

        {replanOpen && (
          <div className="mt-3 shrink-0 space-y-2">
            <textarea
              aria-label="打回反馈"
              value={feedback}
              onChange={(event) => setFeedback(event.target.value)}
              placeholder="填写打回反馈与建议..."
              rows={2}
              disabled={busy}
              className="pairing-input min-h-16 resize-y min-w-0"
            />
            <div className="flex justify-end">
              <button
                type="button"
                className="primary-button"
                disabled={busy || !planAwaitingReview || !feedback.trim()}
                onClick={() => onReplan(feedback.trim())}
                data-host-actionable="true"
              >
                提交打回
              </button>
            </div>
          </div>
        )}

        <div className="mt-4 shrink-0 border-t border-border pt-3.5 flex flex-wrap items-center gap-2 min-w-0">
          <span className="min-w-0 flex-1 text-micro text-muted">{tasks.length} 项任务 · {layers.length} 层</span>
          <button
            type="button"
            className="secondary-button"
            disabled={busy || !planAwaitingReview}
            onClick={() => setReplanOpen((open) => !open)}
            data-host-actionable="true"
          >
            打回重做
          </button>
          <button type="button" className="secondary-button" onClick={onClose}>
            稍后
          </button>
          {canStart && (
            <button type="button" className="primary-button" disabled={busy} onClick={onStart} data-host-actionable="true">
              <PlayIcon className="h-4 w-4" />开始执行
            </button>
          )}
        </div>
      </section>
    </div>
  );
};

export const KanbanBoard: React.FC<KanbanBoardProps> = ({
  onSelectTask,
  onSelectGoal
}) => {
  const {
    board,
    selectedProjectID,
    connectionState,
    connectionMessage,
    isSyncing,
    activeActionTaskID,
    setInspectedTask,
    pauseGoal,
    startGoal,
    replanGoal,
    cancelGoal,
    retryGoalTask,
    skipGoalTask,
    callRemote,
    supportsMethod,
    refreshBoard,
    capabilities,
    createTask
  } = useRemoteStatic();
  const [groupByGoal, setGroupByGoal] = useState(false);
  const [doneExpanded, setDoneExpanded] = useState(true);
  const [doneLimit, setDoneLimit] = useState(10);
  const [menuGoalID, setMenuGoalID] = useState<string | null>(null);
  const [cancelGoalTarget, setCancelGoalTarget] = useState<Goal | null>(null);
  const [goalActionError, setGoalActionError] = useState<string | null>(null);
  const [planReviewGoalID, setPlanReviewGoalID] = useState<string | null>(null);
  const [outputsGoalID, setOutputsGoalID] = useState<string | null>(null);
  const [planReviewSeenIDs, setPlanReviewSeenIDs] = useState<string[]>([]);
  const [taskMenuTaskID, setTaskMenuTaskID] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<TaskItem | null>(null);
  const [interruptTarget, setInterruptTarget] = useState<TaskItem | null>(null);
  const [taskActionBusyID, setTaskActionBusyID] = useState<string | null>(null);
  const [taskActionBusyText, setTaskActionBusyText] = useState<string | null>(null);
  const [taskActionError, setTaskActionError] = useState<string | null>(null);

  const projectMap = useMemo(() => new Map((board?.projects ?? []).map((project) => [project.id, project.name])), [board?.projects]);
  const goalMap = useMemo(() => new Map((board?.goals ?? []).map((goal) => [goal.id, goal])), [board?.goals]);
  const menuGoal = menuGoalID ? (goalMap.get(menuGoalID) ?? null) : null;
  const filteredTasks = useMemo(
    () => (board?.tasks ?? []).filter((task) => !selectedProjectID || task.projectID === selectedProjectID),
    [board?.tasks, selectedProjectID]
  );
  const filteredGoals = useMemo(
    () => (board?.goals ?? []).filter((goal) => !selectedProjectID || goal.projectID === selectedProjectID),
    [board?.goals, selectedProjectID]
  );
  const goalsWithoutTasks = useMemo(
    () => filteredGoals.filter((goal) => !filteredTasks.some((task) => task.goalID === goal.id)),
    [filteredGoals, filteredTasks]
  );
  // 分组模式下目标行随任务出现；未分组时所有目标都要能从首页进入。
  const topGoals = groupByGoal ? goalsWithoutTasks : filteredGoals;
  const tasksByColumn = useMemo(() => {
    const groups = Object.fromEntries(COLUMN_ORDER.map((column) => [column, [] as TaskItem[]])) as Record<KanbanDisplayColumn, TaskItem[]>;
    for (const task of filteredTasks) {
      const column = COLUMN_ORDER.includes(task.displayColumn) ? task.displayColumn : 'todo';
      groups[column].push(task);
    }
    return groups;
  }, [filteredTasks]);
  const isConnected = connectionState === 'connected';
  const canDeleteTask = supportsMethod('kanban.deleteTask');
  const canRescueTask = supportsMethod('kanban.rescueTask');
  const canCreateTask = supportsMethod('kanban.createTask') && (capabilities ? capabilities.features.includes('kanban.linking') : true);
  const canManageTasks = canDeleteTask || canRescueTask;
  // 快速添加必须知道落到哪个项目：已选项目优先，未筛选时只有单项目环境能确定目标。
  const quickAddProjectID = selectedProjectID ?? (board && board.projects.length === 1 ? board.projects[0].id : null);
  const menuTask = taskMenuTaskID ? ((board?.tasks ?? []).find((task) => task.id === taskMenuTaskID) ?? null) : null;

  const planReviewGoal = planReviewGoalID ? (goalMap.get(planReviewGoalID) ?? null) : null;
  const planReviewTasks = useMemo(
    () => (planReviewGoal ? filteredTasks.filter((task) => task.goalID === planReviewGoal.id) : []),
    [filteredTasks, planReviewGoal]
  );

  // plan_review 目标首次出现时自动打开一次规划审查，记录弹过的目标避免重复弹出
  useEffect(() => {
    if (planReviewGoalID) return;
    const next = filteredGoals.find((goal) => goal.status === 'plan_review' && !planReviewSeenIDs.includes(goal.id));
    if (!next) return;
    setPlanReviewSeenIDs((ids) => (ids.includes(next.id) ? ids : [...ids, next.id]));
    setPlanReviewGoalID(next.id);
  }, [filteredGoals, planReviewGoalID, planReviewSeenIDs]);

  const openPlanReview = (goal: Goal) => {
    setMenuGoalID(null);
    setGoalActionError(null);
    setPlanReviewSeenIDs((ids) => (ids.includes(goal.id) ? ids : [...ids, goal.id]));
    setPlanReviewGoalID(goal.id);
  };

  const openTask = (task: TaskItem) => {
    if (onSelectTask) onSelectTask(task);
    else setInspectedTask(task);
  };
  const openGoal = (goal: Goal) => {
    if (onSelectGoal) onSelectGoal(goal);
  };
  const outputsGoal = outputsGoalID ? (goalMap.get(outputsGoalID) ?? null) : null;

  const handleGoalAction = async (
    goal: Goal,
    action: 'start' | 'pause' | 'cancel' | 'replan',
    options?: { fromPlanReview?: boolean; feedback?: string }
  ) => {
    setMenuGoalID(null);
    setGoalActionError(null);
    if (action === 'cancel') {
      setCancelGoalTarget(goal);
      return;
    }
    // 看板菜单里的启动/重新规划先进只读计划预览页；规划审查弹窗里已预览过，直接执行。
    if (!options?.fromPlanReview) {
      if (action === 'start' && goal.status === 'plan_review') {
        openGoal(goal);
        return;
      }
      if (action === 'replan') {
        openGoal(goal);
        return;
      }
    }
    try {
      if (action === 'start') await startGoal(goal.id, goal.revision);
      if (action === 'pause') await pauseGoal(goal.id, goal.revision);
      if (action === 'replan') await replanGoal(goal.id, options?.feedback?.trim() ?? '', goal.revision);
    } catch (error) {
      setGoalActionError(error instanceof Error ? error.message : '目标操作失败');
    }
  };

  const cancelGoalAction = async () => {
    if (!cancelGoalTarget) return;
    setGoalActionError(null);
    const target = cancelGoalTarget;
    setCancelGoalTarget(null);
    try {
      await cancelGoal(target.id, target.revision);
    } catch (error) {
      setGoalActionError(error instanceof Error ? error.message : '取消目标失败');
    }
  };

  const describeTaskError = async (error: unknown, kind: 'delete' | 'nudge' | 'interrupt'): Promise<string> => {
    if (error instanceof RpcError) {
      if (error.code === -32009) {
        await refreshBoard().catch(() => undefined);
        return '看板已更新，已刷新，请重试';
      }
      if (error.code === -32000) {
        const raw = error.message || '';
        if (kind === 'delete' && raw.includes('invalid transition')) {
          return '任务仍有执行中的会话，请先打断后再删除';
        }
        if (raw.includes('follow-up')) {
          return '当前 Mac 的执行驱动不支持催促';
        }
        if (raw.includes('does not support interrupt')) {
          return '当前 Mac 的执行驱动不支持打断';
        }
        if (kind === 'nudge' && raw.includes('invalid transition')) {
          return '该任务当前没有可催促的执行会话';
        }
        if (kind === 'interrupt' && raw.includes('invalid transition')) {
          return '任务当前状态不支持打断';
        }
        return raw || '操作被拒绝';
      }
    }
    return error instanceof Error ? error.message : '任务操作失败';
  };

  const ensureTaskWritable = (): string | null => {
    if (!isConnected) return '未与 Mac 桌面端建立连接，写入操作已禁用';
    if (isSyncing) return '正在同步最新看板，请稍后重试';
    return null;
  };

  const rescueTaskAction = async (task: TaskItem, action: 'interrupt' | 'nudge'): Promise<number | null> => {
    const params: KanbanRescueTaskParams = {
      taskID: task.id,
      action,
      expectedRevision: task.revision,
      actionID: makeActionID()
    };
    const result = await callRemote<KanbanTaskMutationResult>('kanban.rescueTask', params);
    return result.revision ?? null;
  };

  const runTaskCardAction = async (
    task: TaskItem,
    loadingText: string,
    kind: 'delete' | 'nudge' | 'interrupt',
    operation: () => Promise<void>
  ): Promise<boolean> => {
    const blocked = ensureTaskWritable();
    if (blocked) {
      setTaskActionError(blocked);
      return false;
    }
    setTaskActionError(null);
    setTaskActionBusyID(task.id);
    setTaskActionBusyText(loadingText);
    try {
      await operation();
      return true;
    } catch (error) {
      setTaskActionError(await describeTaskError(error, kind));
      return false;
    } finally {
      setTaskActionBusyID(null);
      setTaskActionBusyText(null);
    }
  };

  const openTaskActions = (task: TaskItem) => {
    setTaskActionError(null);
    setTaskMenuTaskID(task.id);
  };

  const nudgeTaskAction = async (task: TaskItem) => {
    setTaskMenuTaskID(null);
    await runTaskCardAction(task, '正在提醒 Agent…', 'nudge', async () => {
      await rescueTaskAction(task, 'nudge');
    });
  };

  const requestInterruptTask = (task: TaskItem) => {
    setTaskMenuTaskID(null);
    setInterruptTarget(task);
  };

  const confirmInterruptTask = async () => {
    const target = interruptTarget;
    if (!target) return;
    // 确认弹窗可能开了一段时间，用看板里最新的任务对象拿最新修订号，减少无谓冲突。
    const latest = (board?.tasks ?? []).find((task) => task.id === target.id) ?? target;
    const ok = await runTaskCardAction(latest, '正在打断任务…', 'interrupt', async () => {
      await rescueTaskAction(latest, 'interrupt');
    });
    // 失败时也关闭弹窗，把错误交给顶部提示条，避免错误被弹窗遮住。
    if (ok) setTaskMenuTaskID(null);
    setInterruptTarget(null);
  };

  const requestDeleteTask = (task: TaskItem) => {
    setTaskMenuTaskID(null);
    setDeleteTarget(task);
  };

  const confirmDeleteTask = async () => {
    const target = deleteTarget;
    if (!target) return;
    // 确认弹窗可能开了一段时间，用看板里最新的任务对象拿最新修订号，减少无谓冲突。
    const latest = (board?.tasks ?? []).find((task) => task.id === target.id) ?? target;
    const ok = await runTaskCardAction(latest, '正在删除任务…', 'delete', async () => {
      let revision = latest.revision;
      // 运行中的任务服务端会拒绝删除：先打断，再用打断返回的最新修订号删除。
      if (kanbanTaskNeedsInterrupt(latest)) {
        const interruptedRevision = await rescueTaskAction(latest, 'interrupt');
        revision = interruptedRevision ?? revision + 1;
        setTaskActionBusyText('正在删除任务…');
      }
      const params: KanbanDeleteTaskParams = {
        taskID: latest.id,
        expectedRevision: revision,
        actionID: makeActionID()
      };
      await callRemote<KanbanTaskMutationResult>('kanban.deleteTask', params);
    });
    // 失败时也关闭弹窗，把错误交给顶部提示条，避免错误被弹窗遮住。
    if (ok) setTaskMenuTaskID(null);
    setDeleteTarget(null);
  };

  const quickAddTask = async (title: string) => {
    if (!quickAddProjectID) throw new Error('请先选择项目后再添加任务');
    try {
      await createTask(quickAddProjectID, title);
    } catch (error) {
      // 与卡片操作同口径：修订号冲突时刷新看板并给出明确提示，避免输入被静默丢弃。
      if (error instanceof RpcError && error.code === -32009) {
        await refreshBoard().catch(() => undefined);
        throw new Error('看板已更新，已刷新，请重试');
      }
      throw error;
    }
  };

  const runTaskAction = async (operation: () => Promise<void>) => {
    setGoalActionError(null);
    try {
      await operation();
    } catch (error) {
      setGoalActionError(error instanceof Error ? error.message : '任务操作失败');
    }
  };

  const renderTask = (task: TaskItem) => {
    const goalID = task.goalID;
    return (
      <TaskRow
        key={task.id}
        task={task}
        projectName={selectedProjectID ? undefined : (task.projectID ? projectMap.get(task.projectID) : undefined)}
        goal={goalID ? goalMap.get(goalID) : undefined}
        onOpen={() => openTask(task)}
        onOpenGoal={goalID && goalMap.has(goalID) ? () => openGoal(goalMap.get(goalID)!) : undefined}
        onOpenThread={task.threadID !== null ? (threadID) => { window.location.hash = `#/t/${encodeURIComponent(threadID)}`; } : undefined}
        onOpenReviewThread={task.reviewThreadID !== null ? (threadID) => { window.location.hash = `#/t/${encodeURIComponent(threadID)}`; } : undefined}
        onRetry={goalID ? () => void runTaskAction(() => retryGoalTask(task.id, task.revision)) : undefined}
        onSkip={goalID ? () => void runTaskAction(() => skipGoalTask(task.id, task.revision)) : undefined}
        onOpenActions={canManageTasks ? () => openTaskActions(task) : undefined}
        actionBusy={taskActionBusyID === task.id}
        actionBusyText={taskActionBusyText}
      />
    );
  };

  const renderColumn = (column: KanbanDisplayColumn) => {
    const allTasks = tasksByColumn[column];
    const visibleTasks = column === 'done' ? allTasks.slice(0, doneLimit) : allTasks;
    const content = !doneExpanded && column === 'done' ? null : groupByGoal ? (() => {
      const grouped = new Map<string, TaskItem[]>();
      const ungrouped: TaskItem[] = [];
      visibleTasks.forEach((task) => {
        if (!task.goalID || !goalMap.has(task.goalID)) ungrouped.push(task);
        else grouped.set(task.goalID, [...(grouped.get(task.goalID) ?? []), task]);
      });
      return (
        <>
          {Array.from(grouped.entries()).map(([goalID, tasks]) => {
            const goal = goalMap.get(goalID)!;
            return (
              <section key={goalID} className="pl-3" aria-label={`目标 ${goal.title}`}>
                <GoalRow
                  goal={goal}
                  menuOpen={menuGoalID === goal.id}
                  onOpen={() => openGoal(goal)}
                  onOpenOutputs={() => setOutputsGoalID(goal.id)}
                  onToggleMenu={() => setMenuGoalID((current) => current === goal.id ? null : goal.id)}
                />
                <div className="pl-3">{tasks.map(renderTask)}</div>
              </section>
            );
          })}
          {ungrouped.map(renderTask)}
        </>
      );
    })() : visibleTasks.map(renderTask);

    return (
      <section key={column} aria-label={COLUMN_LABELS[column]}>
        <SectionHeading
          title={COLUMN_LABELS[column]}
          count={allTasks.length}
          onClick={column === 'done' ? () => setDoneExpanded((expanded) => !expanded) : undefined}
          expanded={doneExpanded}
        />
        {content || <p className="px-0 py-3 text-xs text-muted">完成区已折叠</p>}
        {column === 'done' && doneExpanded && doneLimit < allTasks.length && (
          <button type="button" className="load-more-button" onClick={() => setDoneLimit((limit) => limit + 25)}>
            加载更多完成任务 ({Math.min(25, allTasks.length - doneLimit)})
          </button>
        )}
        {column === 'todo' && canCreateTask && quickAddProjectID && (
          <KanbanQuickAdd onSubmit={quickAddTask} />
        )}
      </section>
    );
  };

  return (
    <div className="home-list min-h-0 flex-1">
      {(isSyncing || !isConnected) && (
        <div className="floating-status" role="status">
          <div className={`floating-status__pill ${isConnected ? 'floating-status__pill--warning' : 'floating-status__pill--danger'}`}>
            <ExclamationTriangleIcon className="h-4 w-4 shrink-0" />
            {isConnected ? '正在同步最新看板，写入操作暂时禁用。' : (connectionMessage || '连接已断开，看板当前只读。')}
          </div>
        </div>
      )}
      {goalActionError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <span className="min-w-0 break-words">{goalActionError}</span>
          <button type="button" className="inline-alert__action" onClick={() => setGoalActionError(null)}>关闭</button>
        </div>
      )}
      {taskActionError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <span className="min-w-0 break-words">{taskActionError}</span>
          <button type="button" className="inline-alert__action" onClick={() => setTaskActionError(null)}>关闭</button>
        </div>
      )}
      <div className="home-toolbar home-toolbar--board">
        <p className="min-w-0 truncate text-xs text-muted">
          {filteredTasks.length} 个任务 · {filteredGoals.length} 个目标
        </p>
        <button
          type="button"
          className={`secondary-button min-h-9 shrink-0 whitespace-nowrap px-3 text-xs ${groupByGoal ? 'border-primary text-primary' : ''}`}
          onClick={() => setGroupByGoal((value) => !value)}
          aria-pressed={groupByGoal}
        >
          <SparklesIcon className="h-3.5 w-3.5" />按目标分组
        </button>
      </div>

      <div className="home-list__scroll">
        {!board ? (
          <div className="empty-state" role="status"><span className="loading-spinner" /><span>正在加载看板…</span></div>
        ) : filteredTasks.length === 0 && filteredGoals.length === 0 ? (
          <div className="empty-state">
            <div className="empty-state__icon"><CheckCircleIcon className="h-6 w-6" /></div>
            <h2>{selectedProjectID ? '此项目暂无任务' : '还没有任务'}</h2>
            <p>点右下角的 + 创建新任务或目标。</p>
            {canCreateTask && quickAddProjectID && (
              <div className="mt-3 w-full max-w-sm text-left">
                <KanbanQuickAdd onSubmit={quickAddTask} />
              </div>
            )}
          </div>
        ) : (
          <div className="thread-list" aria-live="polite">
            {topGoals.length > 0 && (
              <section aria-label="目标">
                <SectionHeading title="目标" count={topGoals.length} />
                {topGoals.map((goal) => (
                  <GoalRow
                    key={goal.id}
                    goal={goal}
                    menuOpen={menuGoalID === goal.id}
                    onOpen={() => openGoal(goal)}
                    onOpenOutputs={() => setOutputsGoalID(goal.id)}
                    onToggleMenu={() => setMenuGoalID((current) => current === goal.id ? null : goal.id)}
                  />
                ))}
              </section>
            )}
            {COLUMN_ORDER.map(renderColumn)}
          </div>
        )}
      </div>

      {menuGoal && (
        <div className="sheet-layer" role="presentation" onMouseDown={() => setMenuGoalID(null)}>
          <section
            className="thread-action-sheet"
            role="dialog"
            aria-modal="true"
            aria-labelledby="goal-actions-title"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <div className="sheet-handle" aria-hidden="true" />
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <p className="eyebrow">目标操作</p>
                <h2 id="goal-actions-title" className="sheet-title truncate">{menuGoal.title}</h2>
              </div>
              <button
                type="button"
                className="icon-button"
                onClick={() => setMenuGoalID(null)}
                aria-label="关闭目标菜单"
                title="关闭"
              >
                <XMarkIcon className="h-4 w-4" />
              </button>
            </div>
            <div className="sheet-actions">
              {(menuGoal.status === 'paused' || menuGoal.status === 'plan_review') && (
                <button
                  type="button"
                  className="sheet-action"
                  onClick={() => void handleGoalAction(menuGoal, 'start')}
                  data-host-actionable="true"
                >
                  <PlayIcon className="h-4 w-4" />
                  <span>{menuGoal.status === 'paused' ? '继续' : '启动'}</span>
                </button>
              )}
              {menuGoal.status === 'plan_review' && (
                <button
                  type="button"
                  className="sheet-action"
                  onClick={() => openPlanReview(menuGoal)}
                  data-host-actionable="true"
                >
                  <EyeIcon className="h-4 w-4" />
                  <span>审查规划</span>
                </button>
              )}
              {menuGoal.status === 'running' && (
                <button
                  type="button"
                  className="sheet-action"
                  onClick={() => void handleGoalAction(menuGoal, 'pause')}
                  data-host-actionable="true"
                >
                  <EyeIcon className="h-4 w-4" />
                  <span>暂停</span>
                </button>
              )}
              {(menuGoal.status === 'plan_failed' || menuGoal.status === 'plan_review') && (
                <button
                  type="button"
                  className="sheet-action"
                  onClick={() => void handleGoalAction(menuGoal, 'replan')}
                  data-host-actionable="true"
                >
                  <SparklesIcon className="h-4 w-4" />
                  <span>重新规划</span>
                </button>
              )}
              {!['completed', 'cancelled'].includes(menuGoal.status) && (
                <button
                  type="button"
                  className="sheet-action sheet-action--danger"
                  onClick={() => void handleGoalAction(menuGoal, 'cancel')}
                  data-host-actionable="true"
                >
                  <XMarkIcon className="h-4 w-4" />
                  <span>取消目标</span>
                </button>
              )}
            </div>
          </section>
        </div>
      )}


      {menuTask && (
        <KanbanTaskActionSheet
          task={menuTask}
          canDelete={canDeleteTask}
          canRescue={canRescueTask}
          busy={taskActionBusyID === menuTask.id}
          busyText={taskActionBusyText}
          onNudge={() => void nudgeTaskAction(menuTask)}
          onInterrupt={() => requestInterruptTask(menuTask)}
          onDelete={() => requestDeleteTask(menuTask)}
          onClose={() => setTaskMenuTaskID(null)}
        />
      )}

      {outputsGoal && (
        <GoalOutputsSheet
          goal={outputsGoal}
          tasks={board?.tasks ?? []}
          onOpenTask={(task) => {
            setOutputsGoalID(null);
            openTask(task);
          }}
          onClose={() => setOutputsGoalID(null)}
        />
      )}

      {planReviewGoal && (
        <GoalPlanReviewDialog
          goal={planReviewGoal}
          tasks={planReviewTasks}
          busy={activeActionTaskID === planReviewGoal.id || isSyncing}
          error={goalActionError}
          onDismissError={() => setGoalActionError(null)}
          onOpenTask={openTask}
          onStart={() => {
            setPlanReviewGoalID(null);
            void handleGoalAction(planReviewGoal, 'start', { fromPlanReview: true });
          }}
          onReplan={(feedback) => void handleGoalAction(planReviewGoal, 'replan', { fromPlanReview: true, feedback })}
          onClose={() => setPlanReviewGoalID(null)}
        />
      )}

      <ConfirmDialog
        isOpen={Boolean(cancelGoalTarget)}
        title="取消目标？"
        message={cancelGoalTarget ? `取消「${cancelGoalTarget.title}」会中断正在运行的任务，且目标进入终态。` : ''}
        confirmLabel="确认取消"
        confirmVariant="danger"
        isLoading={Boolean(cancelGoalTarget && activeActionTaskID === cancelGoalTarget.id)}
        onConfirm={() => void cancelGoalAction()}
        onCancel={() => setCancelGoalTarget(null)}
      />

      <ConfirmDialog
        isOpen={Boolean(deleteTarget)}
        title={deleteTarget && kanbanTaskNeedsInterrupt(deleteTarget) ? '任务运行中，需先打断' : '删除任务？'}
        message={
          deleteTarget
            ? kanbanTaskNeedsInterrupt(deleteTarget)
              ? `「${deleteTarget.title}」正在运行，直接删除会被服务端拒绝。确认后将先打断执行并退回待办，再删除该任务，删除后不可撤销。`
              : `删除「${deleteTarget.title}」后不可撤销。`
            : ''
        }
        confirmLabel={deleteTarget && kanbanTaskNeedsInterrupt(deleteTarget) ? '打断并删除' : '确认删除'}
        confirmVariant="danger"
        isLoading={Boolean(deleteTarget && taskActionBusyID === deleteTarget.id)}
        onConfirm={() => void confirmDeleteTask()}
        onCancel={() => setDeleteTarget(null)}
      />

      <ConfirmDialog
        isOpen={Boolean(interruptTarget)}
        title="打断任务？"
        message={interruptTarget ? `打断「${interruptTarget.title}」会终止正在执行的会话，并把任务退回待办。` : ''}
        confirmLabel="打断并退回待办"
        confirmVariant="danger"
        isLoading={Boolean(interruptTarget && taskActionBusyID === interruptTarget.id)}
        onConfirm={() => void confirmInterruptTask()}
        onCancel={() => setInterruptTarget(null)}
      />
    </div>
  );
};
