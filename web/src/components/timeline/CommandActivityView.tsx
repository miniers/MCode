import type { CommandActivityState } from '../../utils/commandActivity';

export function CommandActivityView({ activity }: { activity?: CommandActivityState }) {
  if (!activity) return null;
  const max = Math.max(100, ...activity.history);
  const memory = activity.rss >= 1024 ** 3 ? `${(activity.rss / 1024 ** 3).toFixed(1)} GB` : `${Math.round(activity.rss / 1024 ** 2)} MB`;
  return <div className="my-1 space-y-0.5 text-mc-caption text-[var(--foreground-muted)] tabular-nums">
    <div className="flex items-center gap-2">
      <div className="flex h-4 items-end gap-px" aria-hidden="true">{activity.history.map ( (value, index) => <span key={index} className="w-[2px] bg-[var(--primary)] opacity-60 motion-safe:transition-[height]" style={{ height: `${Math.max(1, value / max * 100)}%` }} />)}</div>
      <span>{Math.round(activity.cpu)}%</span>{activity.rss >= 1024 ** 2 && <><span>·</span><span>{memory}</span></>}
    </div>
    {activity.processes.map((process, index) => <div key={index} className={`relative flex min-w-0 items-center gap-2 rounded px-1 ${process.cpu < 1 ? 'opacity-50' : ''}`}>
      <span className="absolute inset-y-0 left-0 rounded bg-[var(--primary)] opacity-10 motion-safe:transition-[width]" style={{ width: `${Math.min(process.cpu, 100)}%` }} />
      <span className="relative shrink-0 font-medium text-[var(--foreground)]">{process.name}</span><span className="relative min-w-0 flex-1 truncate">{process.object}</span><span className="relative w-12 shrink-0 text-right">{Math.round(process.cpu)}%</span>
    </div>)}
  </div>;
}
