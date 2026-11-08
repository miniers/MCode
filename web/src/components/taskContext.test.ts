import { taskContextDetail } from './taskContext';

function expect(actual: string, expected: string): void {
  if (actual !== expected) throw new Error(`期望 ${expected}，实际 ${actual}`);
}

expect(taskContextDetail({ status: 'waiting', waitKind: 'human' }), '等待你的决定');
expect(taskContextDetail({ status: 'waiting', waitKind: 'machine' }), '等待条件满足');
expect(taskContextDetail({ status: 'waiting', waitKind: 'children' }), '等待子任务');
expect(taskContextDetail({ status: 'waiting', waitKind: 'human', wakeAt: 'invalid' }), '等待你的决定');
expect(taskContextDetail({ status: 'done', resultSummary: '已完成' }), '已完成');
expect(taskContextDetail({ status: 'done' }), '');

const now = new Date();
const today = new Date(now.getFullYear(), now.getMonth(), now.getDate(), 9, 5);
expect(taskContextDetail({ status: 'waiting', waitKind: 'machine', wakeAt: today.toISOString() }), '等待条件满足 · 09:05');
const tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 10, 6);
expect(taskContextDetail({ status: 'waiting', waitKind: 'children', wakeAt: tomorrow.toISOString() }),
  `等待子任务 · ${tomorrow.getMonth() + 1}月${tomorrow.getDate()}日 10:06`);
