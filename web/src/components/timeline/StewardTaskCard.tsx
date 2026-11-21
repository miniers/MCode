import type { TaskItem } from '../../types/contract';
import { stewardTaskStatusInfo } from '../../utils/taskStatus';

type StewardTaskStatusTask = Pick<TaskItem, 'status'> & Partial<Pick<TaskItem, 'waitKind' | 'blocked' | 'displayColumn'>>;

export function stewardTaskStatus(task: StewardTaskStatusTask): { label: string; color: string } {
  return stewardTaskStatusInfo(task);
}

export function StewardTaskCard({ task, compact = false, onOpenThread }: {
  task: Pick<TaskItem, 'title' | 'status' | 'threadID'> & Partial<Pick<TaskItem, 'waitKind' | 'blocked' | 'displayColumn' | 'lastActivityText' | 'lastFailureReason' | 'resultSummary'>>;
  compact?: boolean;
  onOpenThread?: (threadID: string) => void;
}) {
  const status = task.status;
  const { label, color } = stewardTaskStatus(task);
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
        <span className={`thread-status ${color} shrink-0 transition-colors motion-reduce:transition-none`}>{label}</span>
      </span>
      {!compact && signal?.trim() && <span className="mt-1 block truncate text-xs text-[var(--foreground-muted)]">{signal.trim().split(/\r?\n/)[0]}</span>}
    </button>
  );
}
