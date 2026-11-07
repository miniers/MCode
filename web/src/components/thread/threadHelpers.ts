// 会话页纯工具函数：未决动作判定、队列分组 ID、关联任务列名、子代理耗时与状态展示。

import type { RemoteFollowUp } from '../../types/contract';
import type { RemoteThreadAction } from '../../context/RemoteContext';
import type { RemoteSwarmBee } from '../../types/swarm';

export function isUnresolvedAction(action?: RemoteThreadAction | null): boolean {
  return Boolean(
    action &&
      (action.state === 'submitting' || action.state === 'pending' || action.state === 'unknown')
  );
}

export function queueGroupID(followUp: RemoteFollowUp): string {
  return followUp.bundleID ?? followUp.id;
}

export function linkedTaskColumnLabel(value: string): string {
  if (value === 'needs_you') return '待你处理';
  if (value === 'review') return '待审';
  if (value === 'in_progress') return '进行中';
  if (value === 'todo') return '待办';
  if (value === 'done') return '完成';
  return '未知状态';
}

export function formatHelperDuration(seconds: number): string {
  if (seconds < 60) return `${seconds}秒`;
  const mins = Math.floor(seconds / 60);
  const remainingSecs = seconds % 60;
  if (mins < 60) return `${mins}分${remainingSecs}秒`;
  const hours = Math.floor(mins / 60);
  const remainingMins = mins % 60;
  return `${hours}小时${remainingMins}分`;
}

export function getHelperDurationText(bee?: RemoteSwarmBee, nowMs = Date.now()): string {
  if (!bee?.startedAt) return '';
  const startMs = new Date(bee.startedAt).getTime();
  if (Number.isNaN(startMs)) return '';

  if (bee.status === 'running') {
    const elapsedSecs = Math.max(0, Math.floor((nowMs - startMs) / 1000));
    return `已耗时 ${formatHelperDuration(elapsedSecs)}`;
  }

  if (bee.finishedAt) {
    const finishMs = new Date(bee.finishedAt).getTime();
    if (!Number.isNaN(finishMs)) {
      const durationSecs = Math.max(0, Math.floor((finishMs - startMs) / 1000));
      return `耗时 ${formatHelperDuration(durationSecs)}`;
    }
  }

  return '';
}

export function getHelperStatusDisplay(status?: string): { label: string; className: string } {
  switch (status) {
    case 'running':
      return { label: '运行中', className: 'text-sky-600 dark:text-sky-400 font-semibold' };
    case 'completed':
      return { label: '已完成', className: 'text-slate-600 dark:text-zinc-300 font-medium' };
    case 'failed':
      return { label: '失败', className: 'text-rose-600 dark:text-rose-400 font-semibold' };
    case 'stopped':
      return { label: '已停止', className: 'text-[var(--foreground-muted)] font-normal' };
    default:
      return { label: status || '未知状态', className: 'text-[var(--foreground-muted)] font-normal' };
  }
}
