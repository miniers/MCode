import type { TaskItem, TaskStatus } from '../../types/contract';

const labels: Record<TaskStatus, string> = {
  todo: '待处理', in_progress: '进行中', needs_approval: '等你决定', review: '待审阅',
  rejected: '已驳回', done: '已完成', failed: '失败', cancelled: '已取消', waiting: '待处理'
};

export function StewardTaskCard({ task, compact = false, onOpenThread }: {
  task: Pick<TaskItem, 'title' | 'status' | 'threadID'> & Partial<Pick<TaskItem, 'blocked' | 'displayColumn' | 'lastActivityText' | 'lastFailureReason' | 'resultSummary'>>;
  compact?: boolean;
  onOpenThread?: (threadID: string) => void;
}) {
  const status = task.status;
  const color = status === 'failed' || status === 'rejected' ? 'thread-status--danger'
    : status === 'needs_approval' || task.blocked || task.displayColumn === 'needs_you' ? 'thread-status--warning'
    : status === 'review' ? 'thread-status--primary'
    : status === 'in_progress' ? 'thread-status--running' : '';
  const signal = status === 'in_progress' ? task.lastActivityText
    : status === 'failed' ? task.lastFailureReason
    : status === 'done' ? task.resultSummary : null;
  const threadID = task.threadID?.trim();
  return (
    <button
      type="button"
      disabled={!threadID || !onOpenThread}
      onClick={() => threadID && onOpenThread?.(threadID)}
      className={`animate-fadeIn min-w-0 max-w-full text-left transition-colors motion-reduce:animate-none motion-reduce:transition-none hover:bg-[var(--card-alt,var(--card))] disabled:cursor-default ${compact ? 'inline-flex items-center gap-1.5 rounded-full border border-[var(--border)] px-2.5 py-1 text-xs' : 'block w-full rounded-xl border border-[var(--border)] bg-[var(--card)] p-3 text-sm'}`}
    >
      <span className="flex min-w-0 items-center gap-2">
        <span className={`thread-status ${color} transition-colors motion-reduce:transition-none`} aria-hidden="true">●</span>
        <span className="min-w-0 flex-1 truncate">{task.title}</span>
        <span className={`thread-status ${color} shrink-0 transition-colors motion-reduce:transition-none`}>{labels[status]}</span>
      </span>
      {!compact && signal?.trim() && <span className="mt-1 block truncate text-xs text-[var(--foreground-muted)]">{signal.trim().split(/\r?\n/)[0]}</span>}
    </button>
  );
}
