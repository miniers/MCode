import { parseRoute } from './route';

function assertEqual(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) throw new Error(`Expected ${JSON.stringify(expected)}, got ${JSON.stringify(actual)}`);
}

assertEqual(parseRoute('#/'), { kind: 'home' });
assertEqual(parseRoute('#/board'), { kind: 'board' });
assertEqual(parseRoute('#/automations'), { kind: 'automations' });
assertEqual(parseRoute('#/t/thread%201/review'), { kind: 'thread', threadID: 'thread 1', subview: 'review' });
assertEqual(parseRoute('#/task/task%201'), { kind: 'task', taskID: 'task 1' });
