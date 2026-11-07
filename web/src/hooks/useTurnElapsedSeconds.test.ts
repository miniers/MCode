import { resolveCurrentTurnIdentity } from './useTurnElapsedSeconds';
import type { ThreadMessage } from '../components/ThreadTimeline';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`);
}

const msg = (id: string, turnID: string, createdAt: string, extra: Partial<ThreadMessage> = {}): ThreadMessage => ({
  id, turnID, role: 'user', kind: 'user', text: '', createdAt, turnIndex: 1, ...extra
});
const old = msg('old', 'old-turn', '2026-11-06T08:00:00.000Z');
const newer = msg('new', 'new-turn', '2026-11-06T08:01:00.000Z');
assertEqual(resolveCurrentTurnIdentity('t', [old, newer], null, 'old-turn').key, 'turn:old-turn', 'pending turn takes priority');
assertEqual(resolveCurrentTurnIdentity('t', [old, { ...newer, turnStatus: 'completed' }], null, null).key === 'turn:old-turn', false, 'completed latest turn does not revive an older turn');
assertEqual(resolveCurrentTurnIdentity('t', [msg('u', 'turn', '2026-11-06T08:00:02.000Z', { turnStartedAt: '2026-11-06T08:00:00.000Z' })]).source, 2, 'host start outranks message time');
assertEqual(resolveCurrentTurnIdentity('t', [old, newer]).key, 'turn:new-turn', 'turn switch');
assertEqual(resolveCurrentTurnIdentity('t', [msg('bad', 'bad-turn', 'invalid')]).source, 0, 'invalid timestamps ignored');
assertEqual(resolveCurrentTurnIdentity('t', [msg('old-data', 'old-data-turn', 'invalid')], 'invalid').key, 'turn:old-data-turn', 'old data identity remains usable');
