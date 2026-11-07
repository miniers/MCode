import { buildTimelineItems, mergeThreadMessages, turnWorkHeaderLabel, WORK_ENTRY_ROW_CLASS, type ThreadMessage } from './ThreadTimeline';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`);
}
const message = (id: string, role: string, at: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({
  id, role, text: '', createdAt: at, turnIndex: 1, turnID: 'turn', ...extra
});
const start = '2026-11-06T08:00:00.000Z';
const end = '2026-11-06T08:00:10.000Z';
const pageOnly = message('tool', 'tool', '2026-11-06T08:00:09.000Z', { kind: 'tool', toolStatus: 'completed', toolCompletedAt: '2026-11-06T08:00:40.000Z', turnStartedAt: start, turnCompletedAt: end, turnStatus: 'completed' });
const projected = buildTimelineItems([pageOnly]).find((item) => item.type === 'turnFinished');
if (!projected || projected.type !== 'turnFinished') throw new Error('Expected finished-turn projection');
assertEqual(projected.stats.durationMs, 10_000, 'paged message yields complete authoritative duration');
assertEqual(projected.stats.endedAt, end, 'projection uses authoritative end');
const userOnly = buildTimelineItems([message('user', 'user', start, { turnStartedAt: start, turnCompletedAt: end, turnStatus: 'completed' })]);
assertEqual(userOnly.some((item) => item.type === 'turnFinished'), true, 'user-only turn is projected');
assertEqual(turnWorkHeaderLabel([pageOnly], false, Date.now(), start, end), '用时 10s', 'work record uses authoritative interval');
const merged = mergeThreadMessages([{ ...pageOnly, turnStatus: 'completed' }], [{ ...pageOnly, turnStatus: 'running', turnStartedAt: start, turnCompletedAt: undefined }]);
assertEqual(merged[0].turnStatus, 'completed', 'late running projection preserves terminal status');
const unordered = buildTimelineItems([message('late', 'tool', '2026-11-06T08:00:20.000Z', { kind: 'tool', toolCompletedAt: '2026-11-06T08:01:00.000Z', turnCompletedAt: end, turnStatus: 'completed' }), message('early', 'user', start, { turnStartedAt: start })]);
const unorderedFinished = unordered.find((item) => item.type === 'turnFinished');
if (!unorderedFinished || unorderedFinished.type !== 'turnFinished') throw new Error('Expected unordered finished-turn projection');
assertEqual(unorderedFinished.stats.durationMs, 10_000, 'late tool completion does not extend turn duration');
// 工作记录条目是平铺行，不能回退成内层卡片
for (const forbidden of ['border', 'rounded', 'bg-']) {
  assertEqual(WORK_ENTRY_ROW_CLASS.includes(forbidden), false, `work entry row stays flat (no ${forbidden})`);
}
