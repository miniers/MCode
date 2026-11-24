import {
  shouldShowTodoProgress,
  todoProgressOf,
  todoProgressText,
  type TodoSourceMessage
} from './todoProgress';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`);
  }
}

function assert(condition: unknown, label: string): void {
  if (!condition) throw new Error(label);
}

const todoMsg = (items: Array<{ content: string; status: string }>): TodoSourceMessage => ({
  role: 'tool',
  kind: 'tool',
  toolInput: JSON.stringify({ todos: items })
});

const incompleteTodos = [
  { content: '第一步', status: 'completed' },
  { content: '第二步', status: 'in_progress' },
  { content: '第三步', status: 'pending' }
];

const completedTodos = [
  { content: '第一步', status: 'completed' },
  { content: '第二步', status: 'completed' }
];

// 1. 无待办时不显示
assertEqual(shouldShowTodoProgress([], true), null, 'empty messages return null');
assertEqual(
  shouldShowTodoProgress([{ role: 'user', text: 'hello' }], false),
  null,
  'messages without todo return null'
);

// 2. 未完成且运行中显示
const runningIncomplete = shouldShowTodoProgress(
  [{ role: 'user', text: 'start' }, todoMsg(incompleteTodos)],
  true
);
assert(runningIncomplete !== null, 'incomplete while running should show');
assertEqual(runningIncomplete?.done, 1, 'running progress done count');
assertEqual(runningIncomplete?.total, 3, 'running progress total count');
assertEqual(runningIncomplete?.current, '第二步', 'running progress current text');

// 3. 全部完成时不显示（运行中与已结束都不显示）
assertEqual(
  shouldShowTodoProgress([{ role: 'user', text: 'start' }, todoMsg(completedTodos)], true),
  null,
  'all completed while running returns null'
);
assertEqual(
  shouldShowTodoProgress([{ role: 'user', text: 'start' }, todoMsg(completedTodos)], false),
  null,
  'all completed after end returns null'
);

// 4. 结束后新用户消息之后的旧清单不显示；本轮刚生成的未完成清单在结束后仍显示
assertEqual(
  shouldShowTodoProgress(
    [
      { role: 'user', text: 'turn 1' },
      todoMsg(incompleteTodos),
      { role: 'user', text: 'turn 2' },
      { role: 'assistant', text: 'ok' }
    ],
    false
  ),
  null,
  'ended session with new user message after old checklist returns null'
);

const endedCurrentTurn = shouldShowTodoProgress(
  [{ role: 'user', text: 'turn 2' }, todoMsg(incompleteTodos)],
  false
);
assert(endedCurrentTurn !== null, 'ended session with checklist after last user message shows');
assertEqual(endedCurrentTurn?.done, 1, 'ended current turn done count');

// 5. todoProgressText 格式（含当前项、全完成无当前项、空白折叠与 maxRunes 截断）
assertEqual(
  todoProgressText(
    todoProgressOf([
      { text: '已完成', status: 'completed' },
      { text: '正在   处理\n任务', status: 'in_progress' },
      { text: '待办', status: 'pending' }
    ])
  ),
  '1/3 · 正在 处理 任务',
  'formats done/total with normalized current item'
);

assertEqual(
  todoProgressText(
    todoProgressOf([
      { text: '已完成', status: 'completed' },
      { text: '1234567890', status: 'in_progress' }
    ]),
    4
  ),
  '1/2 · 1234',
  'truncates current item by maxRunes'
);

assertEqual(
  todoProgressText(
    todoProgressOf([
      { text: '已完成一', status: 'completed' },
      { text: '已完成二', status: 'completed' }
    ])
  ),
  '2/2',
  'omits separator when all completed and current is empty'
);
