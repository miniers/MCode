import type { RemoteAutomationExtAutomation } from '../context/remoteAutomationApi';
import type { Goal, TaskItem } from '../types/contract';
import {
  automationRunStatusInfo,
  goalStatusLabel,
  isStewardOrigin,
  kanbanColumnLabel,
  taskStatusLabel
} from './taskStatus';

export type KanbanStatusFilter = 'all' | 'needs_you' | 'review' | 'steward';

export type AutomationStatusFilter = 'all' | 'failed' | 'paused';

function normalizeQuery(query: string): string {
  return query.trim().toLowerCase();
}

function matchesFields(needle: string, fields: string[]): boolean {
  if (!needle) return true;
  return fields.some((field) => field.toLowerCase().includes(needle));
}

export function filterKanbanTasks(
  tasks: TaskItem[],
  query: string,
  statusFilter: KanbanStatusFilter
): TaskItem[] {
  const needle = normalizeQuery(query);
  return tasks.filter((task) => {
    if (statusFilter === 'needs_you') {
      const needsYou =
        task.displayColumn === 'needs_you' ||
        task.status === 'needs_approval' ||
        task.status === 'failed' ||
        task.status === 'rejected' ||
        (task.status === 'waiting' && task.waitKind === 'human');
      if (!needsYou) return false;
    } else if (statusFilter === 'review') {
      if (task.displayColumn !== 'review' && task.status !== 'review') return false;
    } else if (statusFilter === 'steward') {
      if (!isStewardOrigin(task.origin)) return false;
    }

    return matchesFields(needle, [
      task.title,
      task.notes ?? '',
      taskStatusLabel(task.status),
      kanbanColumnLabel(task.displayColumn),
      isStewardOrigin(task.origin) ? '总管' : ''
    ]);
  });
}

export function filterKanbanGoals(
  goals: Goal[],
  query: string,
  statusFilter: KanbanStatusFilter
): Goal[] {
  const needle = normalizeQuery(query);
  return goals.filter((goal) => {
    if (statusFilter === 'needs_you') {
      const needsYou =
        goal.status === 'plan_review' ||
        goal.status === 'plan_failed' ||
        (goal.progress?.waitingOnYou ?? 0) > 0 ||
        (goal.progress?.failed ?? 0) > 0;
      if (!needsYou) return false;
    } else if (statusFilter === 'review') {
      if (goal.status !== 'plan_review') return false;
    } else if (statusFilter === 'steward') {
      if (goal.owner !== 'steward') return false;
    }

    return matchesFields(needle, [
      goal.title,
      goal.prompt ?? '',
      goalStatusLabel(goal.status)
    ]);
  });
}

export function filterAutomations(
  items: RemoteAutomationExtAutomation[],
  query: string,
  statusFilter: AutomationStatusFilter
): RemoteAutomationExtAutomation[] {
  const needle = normalizeQuery(query);
  return items.filter((item) => {
    if (statusFilter === 'failed') {
      if (item.lastRunStatus !== 'failed' && item.status !== 'failed') return false;
    } else if (statusFilter === 'paused') {
      if (item.enabled && item.status !== 'paused') return false;
    }

    return matchesFields(needle, [
      item.name,
      item.prompt ?? '',
      item.lastRunStatus ? automationRunStatusInfo(item.lastRunStatus).label : '',
      item.enabled ? '已启用' : '已暂停'
    ]);
  });
}
