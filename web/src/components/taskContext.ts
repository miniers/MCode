import type { Task } from '../types/generated';

type ContextTask = Pick<Task, 'status' | 'resultSummary' | 'waitKind' | 'wakeAt'>;

export function taskContextDetail(task: ContextTask): string {
  if (task.status !== 'waiting') return task.resultSummary?.trim() ?? '';

  const reason = task.waitKind === 'human' ? '等待你的决定'
    : task.waitKind === 'machine' ? '等待条件满足'
    : task.waitKind === 'children' ? '等待子任务'
    : '';
  if (!task.wakeAt) return reason;

  const wake = new Date(task.wakeAt);
  if (Number.isNaN(wake.getTime())) return reason;
  const now = new Date();
  const time = `${String(wake.getHours()).padStart(2, '0')}:${String(wake.getMinutes()).padStart(2, '0')}`;
  const sameDay = wake.getFullYear() === now.getFullYear()
    && wake.getMonth() === now.getMonth() && wake.getDate() === now.getDate();
  const formatted = sameDay ? time : `${wake.getMonth() + 1}月${wake.getDate()}日 ${time}`;
  return [reason, formatted].filter(Boolean).join(' · ');
}
