// Web 端统一状态字典与来源标识：看板列、任务态、目标态、总管任务态、自动化运行态与收件箱事件类型的单点真相。

import type { GoalStatus, KanbanDisplayColumn, TaskItem, TaskStatus } from '../types/contract';

export const KANBAN_COLUMN_LABELS: Record<KanbanDisplayColumn, string> = {
  todo: '待办',
  in_progress: '进行中',
  needs_you: '待你处理',
  review: '待审阅',
  done: '已完成'
};

export function kanbanColumnLabel(col: string): string {
  return KANBAN_COLUMN_LABELS[col as KanbanDisplayColumn] ?? '未知状态';
}

export const TASK_STATUS_LABELS: Record<TaskStatus, string> = {
  todo: '待办',
  in_progress: '进行中',
  needs_approval: '待审批',
  waiting: '等待中',
  review: '待审阅',
  done: '已完成',
  rejected: '已驳回',
  failed: '失败',
  cancelled: '已取消'
};

export function taskStatusLabel(status: string): string {
  return TASK_STATUS_LABELS[status as TaskStatus] ?? '未知状态';
}

export type TaskStatusToneInput = Pick<TaskItem, 'status'> &
  Partial<Pick<TaskItem, 'waitKind' | 'blocked' | 'displayColumn'>>;

export function taskStatusToneClass(task: TaskStatusToneInput): string {
  if (task.status === 'failed') return 'thread-status--danger';
  const waitingOnHuman = task.status === 'waiting' && task.waitKind === 'human';
  if (
    task.status === 'needs_approval' ||
    task.status === 'rejected' ||
    waitingOnHuman ||
    task.blocked ||
    task.displayColumn === 'needs_you'
  ) {
    return 'thread-status--warning';
  }
  if (task.status === 'review' || task.displayColumn === 'review') return 'thread-status--primary';
  if (task.status === 'in_progress' || task.displayColumn === 'in_progress') return 'thread-status--running';
  if (task.status === 'done') return '!text-[var(--mc-success)]';
  return '';
}

export function stewardTaskStatusInfo(task: TaskStatusToneInput): { label: string; color: string } {
  if (task.status === 'waiting' && task.waitKind === 'human') {
    return { label: '待你处理', color: 'thread-status--warning' };
  }
  if (task.status === 'needs_approval') {
    return { label: TASK_STATUS_LABELS.needs_approval, color: 'thread-status--warning' };
  }
  return {
    label: taskStatusLabel(task.status),
    color: taskStatusToneClass(task)
  };
}

export const GOAL_STATUS_LABELS: Record<GoalStatus, string> = {
  planning: '规划中',
  plan_failed: '规划失败',
  plan_review: '方案待审',
  running: '进行中',
  paused: '已暂停',
  completed: '已完成',
  cancelled: '已取消'
};

export function goalStatusLabel(status: string): string {
  return GOAL_STATUS_LABELS[status as GoalStatus] ?? '未知状态';
}

export function goalStatusClass(status: string): string {
  switch (status) {
    case 'plan_failed':
      return 'thread-status--danger';
    case 'plan_review':
      return 'thread-status--warning';
    case 'planning':
    case 'running':
      return 'thread-status--running';
    case 'completed':
      return '!text-[var(--mc-success)]';
    case 'paused':
    case 'cancelled':
    default:
      return 'text-[var(--foreground-muted)]';
  }
}

export function automationRunStatusInfo(status: string): { label: string; className: string } {
  switch (status) {
    case 'running':
      return { label: '运行中', className: 'text-[var(--status-running)]' };
    case 'succeeded':
    case 'success':
    case 'completed':
      return { label: '成功', className: 'text-[var(--mc-success)]' };
    case 'failed':
    case 'error':
      return { label: '失败', className: 'text-[var(--mc-danger)]' };
    case 'stopped':
    case 'canceled':
    case 'cancelled':
      return { label: '已停止', className: 'text-[var(--foreground-muted)]' };
    case 'skipped':
      return { label: '已跳过', className: 'text-[var(--foreground-muted)]' };
    case '':
      return { label: '未知', className: 'text-[var(--foreground-muted)]' };
    default:
      return { label: status, className: 'text-[var(--foreground-muted)]' };
  }
}

export const ATTENTION_KIND_GROUPS: readonly { kind: string; label: string }[] = [
  { kind: 'thread_stalled', label: '会话卡住' },
  { kind: 'thread_failed', label: '会话出错' },
  { kind: 'thread_quota', label: '额度用尽' },
  { kind: 'thread_approval', label: '等待审批' },
  { kind: 'kanban_failed', label: '看板失败' },
  { kind: 'kanban_needs_approval', label: '看板待审批' },
  { kind: 'kanban_review', label: '看板待验收' },
  { kind: 'kanban_plan_failed', label: '规划失败' },
  { kind: 'automation_failed', label: '自动化失败' },
  { kind: 'automation_finished', label: '自动化完成' },
  { kind: 'bee_failed', label: '子代理失败' },
  { kind: 'merge_review_pending', label: '合并待审阅' },
  { kind: 'thread_no_progress', label: '任务无进展' }
];

const ATTENTION_KIND_MAP = new Map<string, string>(
  ATTENTION_KIND_GROUPS.map((group) => [group.kind, group.label])
);

export function attentionKindLabel(kind: string): string {
  return ATTENTION_KIND_MAP.get(kind) ?? '其他';
}

export function isStewardOrigin(origin?: string): boolean {
  return origin === 'owner' || origin === 'steward';
}
