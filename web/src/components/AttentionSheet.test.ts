import { resolveAttentionTargetHash } from './AttentionSheet';

function assertEqual(actual: unknown, expected: unknown): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(`期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`);
  }
}

assertEqual(
  resolveAttentionTargetHash({ subjectType: 'kanban_task', subjectID: 'task 1', threadID: 't-1' }),
  '#/task/task%201'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'kanban_goal', subjectID: 'goal/1', threadID: '' }),
  '#/goal/goal%2F1'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'automation', subjectID: 'auto 1', threadID: '' }),
  '#/automations/auto%201'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'automation_run', subjectID: 'run-1', threadID: 'thread 1' }),
  '#/t/thread%201'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'automation_run', subjectID: 'run-1', threadID: '' }),
  '#/automations'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'thread', subjectID: 'thread-2', threadID: 'thread 2' }),
  '#/t/thread%202'
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'kanban_task', subjectID: '', threadID: '' }),
  null
);
assertEqual(
  resolveAttentionTargetHash({ subjectType: 'unknown', subjectID: 'x', threadID: '' }),
  null
);
