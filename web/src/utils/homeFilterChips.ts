export type ThreadFilter = 'all' | 'attention' | 'running' | 'failed' | 'dormant' | 'archived';

type VisibleFilter = Exclude<ThreadFilter, 'dormant'>;

export function visibleFilterChips(
  filter: ThreadFilter,
  counts: { attention: number; running: number; failed: number }
): VisibleFilter[] {
  if (filter === 'all' && counts.attention + counts.running + counts.failed === 0) return [];
  const chips: VisibleFilter[] = ['all'];
  for (const key of ['attention', 'running', 'failed'] as const) {
    if (counts[key] > 0 || filter === key) chips.push(key);
  }
  if (filter === 'archived') chips.push('archived');
  return chips;
}
