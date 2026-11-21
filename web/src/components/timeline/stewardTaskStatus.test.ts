import { stewardTaskStatus } from './StewardTaskCard';

function assertEqual(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`);
}

assertEqual(stewardTaskStatus({ status: 'waiting', waitKind: 'human' }), { label: '待你处理', color: 'thread-status--warning' });
assertEqual(stewardTaskStatus({ status: 'needs_approval' }), { label: '待审批', color: 'thread-status--warning' });
assertEqual(stewardTaskStatus({ status: 'todo' }).label, '待办');
assertEqual(stewardTaskStatus({ status: 'waiting', waitKind: 'machine' }).label, '等待中');
assertEqual(stewardTaskStatus({ status: 'failed' }).color, 'thread-status--danger');
assertEqual(stewardTaskStatus({ status: 'done' }).label, '已完成');
