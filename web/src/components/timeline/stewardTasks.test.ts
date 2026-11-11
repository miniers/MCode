import type { TaskItem } from '../../types/contract';
import type { TimelineItem, ThreadMessage } from '../ThreadTimeline';
import { stewardTasksByTurnEnd } from './stewardTasks';

function assertEqual(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`);
}

const message = (id: string, role: 'user' | 'assistant'): TimelineItem => ({
  type: 'message', message: { id, role } as ThreadMessage
});
const task = (id: string, fields: Partial<TaskItem>): TaskItem => ({ id, origin: 'owner', ...fields } as TaskItem);
const items: TimelineItem[] = [message('before', 'assistant'), message('u1', 'user'), message('a1', 'assistant'), message('u2', 'user'), message('a2', 'assistant')];
const created = task('created', { sourceMessageID: 'u1', amendMessageIDs: ['u2', 'u2'] });
const amended = task('amended', { sourceMessageID: 'u2', amendMessageIDs: ['u1'] });
const grouped = stewardTasksByTurnEnd(items, [
  created, amended,
  task('other', { origin: 'agent', sourceMessageID: 'u1', amendMessageIDs: ['u2'] }),
  task('child', { parentID: 'created', sourceMessageID: 'u1', amendMessageIDs: ['u2'] }),
  task('missing', { sourceMessageID: 'unknown', amendMessageIDs: ['unknown'] })
]);
assertEqual([...grouped.keys()], [2, 4]);
assertEqual(grouped.get(2), { created: [created], amended: [amended] });
assertEqual(grouped.get(4), { created: [amended], amended: [created] });
assertEqual(stewardTasksByTurnEnd([message('u1', 'user')], [created]).get(0)?.created[0], created);
assertEqual(stewardTasksByTurnEnd([message('a1', 'assistant')], [created]).size, 0);
