import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { RemoteProvider } from '../../context/RemoteContext';
import { TimelineEntry } from '../ThreadTimeline';
import { turnChecklist, type ThreadMessage } from './projection';
import { TurnChecklistCard, TurnChecklistTrigger } from './TurnChecklist';

function assertEqual<T>(actual: T, expected: T, label: string): void {
  if (actual !== expected) {
    throw new Error(`${label}: expected ${String(expected)}, got ${String(actual)}`);
  }
}

function assert(condition: unknown, label: string): void {
  if (!condition) throw new Error(label);
}

const msg = (
  id: string,
  role: string,
  at: string,
  extra: Partial<ThreadMessage> = {}
): ThreadMessage => ({
  id,
  role,
  text: '',
  createdAt: at,
  turnIndex: 1,
  turnID: 'turn-1',
  ...extra
});

const t0 = '2026-11-24T10:00:00.000Z';
const t1 = '2026-11-24T10:00:05.000Z';
const t2 = '2026-11-24T10:00:10.000Z';
const t3 = '2026-11-24T10:00:15.000Z';
const t4 = '2026-11-24T10:00:20.000Z';

// 1. 无清单返回 null
assertEqual(turnChecklist([]), null, 'empty messages return null');
assertEqual(
  turnChecklist([
    msg('u1', 'user', t0, { kind: 'user', text: 'hello' }),
    msg('tool1', 'tool', t1, { kind: 'tool', toolName: 'Bash', toolInput: '{"command":"ls"}' }),
    msg('a1', 'assistant', t2, { kind: 'assistant', text: 'done' })
  ]),
  null,
  'messages without todo tool return null'
);

// 2. 取本回合最后一份清单并准确统计 done / total
const firstTodos = JSON.stringify({
  todos: [
    { content: '第一步', status: 'in_progress' },
    { content: '第二步', status: 'pending' }
  ]
});
const latestTodos = JSON.stringify({
  todos: [
    { content: '第一步', status: 'completed' },
    { content: '第二步', status: 'completed' },
    { content: '第三步', status: 'in_progress' },
    { content: '第四步', status: 'pending' }
  ]
});

const turnResult = turnChecklist([
  msg('u1', 'user', t0, { kind: 'user', text: 'start' }),
  msg('todo1', 'tool', t1, { kind: 'tool', toolName: 'TodoWrite', toolInput: firstTodos }),
  msg('read1', 'tool', t2, { kind: 'tool', toolName: 'Read', toolInput: '{"path":"a.ts"}' }),
  msg('todo2', 'tool', t3, { kind: 'tool', toolName: 'TodoWrite', toolInput: latestTodos }),
  msg('bash1', 'tool', t4, { kind: 'tool', toolName: 'Bash', toolInput: '{"command":"pwd"}' })
]);
if (!turnResult) throw new Error('expected checklist for turn');
assertEqual(turnResult.done, 2, 'done count matches completed items');
assertEqual(turnResult.total, 4, 'total count matches all items');
assertEqual(turnResult.items[2].text, '第三步', 'uses latest checklist items');
assertEqual(turnResult.items[2].status, 'in_progress', 'preserves in_progress status');

// 3. 兼容 plan 格式（update_plan）
const planResult = turnChecklist([
  msg('plan1', 'tool', t1, {
    kind: 'tool',
    toolName: 'update_plan',
    toolInput: JSON.stringify({
      plan: [
        { step: '检查类型', status: 'completed' },
        { step: '补充测试', status: 'completed' }
      ]
    })
  })
]);
if (!planResult) throw new Error('expected checklist for update_plan');
assertEqual(planResult.done, 2, 'plan done count');
assertEqual(planResult.total, 2, 'plan total count');

// 4. 跨回合不串：上一轮有清单，新一轮用户消息后无清单时返回 null
assertEqual(
  turnChecklist([
    msg('u1', 'user', t0, { kind: 'user', text: 'turn 1' }),
    msg('todo1', 'tool', t1, { kind: 'tool', toolName: 'TodoWrite', toolInput: latestTodos }),
    msg('u2', 'user', t2, { kind: 'user', text: 'turn 2', turnIndex: 2, turnID: 'turn-2' }),
    msg('bash2', 'tool', t3, { kind: 'tool', toolName: 'Bash', toolInput: '{"command":"ls"}', turnIndex: 2, turnID: 'turn-2' })
  ]),
  null,
  'previous turn checklist does not leak across user boundary'
);

assertEqual(
  turnChecklist([
    msg('u1', 'user', t0, { text: 'turn 1' }),
    msg('todo1', 'tool', t1, { kind: 'tool', toolName: 'TodoWrite', toolInput: latestTodos }),
    msg('u2', 'user', t2, { text: 'turn 2', turnIndex: 2, turnID: 'turn-2' })
  ]),
  null,
  'new turn with no tool messages yet returns null'
);

const secondTurnTodos = JSON.stringify({
  todos: [{ content: '新回合任务', status: 'pending' }]
});
const secondTurnResult = turnChecklist([
  msg('u1', 'user', t0, { kind: 'user', text: 'turn 1' }),
  msg('todo1', 'tool', t1, { kind: 'tool', toolName: 'TodoWrite', toolInput: latestTodos }),
  msg('u2', 'user', t2, { kind: 'user', text: 'turn 2', turnIndex: 2, turnID: 'turn-2' }),
  msg('todo2', 'tool', t3, { kind: 'tool', toolName: 'TodoWrite', toolInput: secondTurnTodos, turnIndex: 2, turnID: 'turn-2' })
]);
if (!secondTurnResult) throw new Error('expected checklist for second turn');
assertEqual(secondTurnResult.total, 1, 'second turn uses its own checklist');
assertEqual(secondTurnResult.items[0].text, '新回合任务', 'second turn item text');

// 5. UI 渲染：头部文案与图标、清单卡片图标与状态、默认开合、无清单保持原样
const incompleteTriggerHtml = renderToStaticMarkup(
  React.createElement(TurnChecklistTrigger, { checklist: turnResult, open: false, onToggle: () => {} })
);
assert(incompleteTriggerHtml.includes('2/4 步'), 'incomplete trigger renders done/total 步');
assert(!incompleteTriggerHtml.includes('--mc-success'), 'incomplete trigger uses hollow circle, not success');

const completeTriggerHtml = renderToStaticMarkup(
  React.createElement(TurnChecklistTrigger, { checklist: planResult, open: false, onToggle: () => {} })
);
assert(completeTriggerHtml.includes('2/2 步'), 'complete trigger renders done/total 步');
assert(completeTriggerHtml.includes('--mc-success'), 'complete trigger uses success color');

const runningCardHtml = renderToStaticMarkup(
  React.createElement(TurnChecklistCard, { checklist: turnResult, open: true, running: true })
);
assert(runningCardHtml.includes('is-open'), 'running card is open');
assert(runningCardHtml.includes('aria-label="进行中"'), 'in_progress while running uses RunningGlyph');
assert(runningCardHtml.includes('--mc-success'), 'completed step uses success color');
assert(runningCardHtml.includes('--mc-border'), 'pending step uses border color');
assert(!runningCardHtml.includes('line-through'), 'checklist items have no strikethrough');
assert(!runningCardHtml.includes('<progress'), 'checklist card has no progress bar');

const stoppedCardHtml = renderToStaticMarkup(
  React.createElement(TurnChecklistCard, { checklist: turnResult, open: true, running: false })
);
assert(!stoppedCardHtml.includes('aria-label="进行中"'), 'stopped in_progress does not use RunningGlyph');
assert(stoppedCardHtml.includes('--mc-accent'), 'stopped in_progress uses accent dot');

// TimelineEntry 接入：运行中默认展开，结束后默认收起，即使「用时」折叠体收起也可见
const todoEntry = msg('todo-entry', 'tool', t1, {
  kind: 'tool',
  toolName: 'TodoWrite',
  toolInput: latestTodos,
  turnStartedAt: t0,
  turnCompletedAt: t2
});
const renderWorkEntry = (turnStatus: 'running' | 'completed', entries: ThreadMessage[]) =>
  renderToStaticMarkup(
    React.createElement(
      RemoteProvider,
      null,
      React.createElement(TimelineEntry, {
        activity: {},
        threadID: 'thread',
        item: {
          type: 'workRecord',
          turnID: 'turn-1',
          turnIndex: 1,
          turnStatus,
          turnStartedAt: t0,
          turnCompletedAt: turnStatus === 'running' ? undefined : t2,
          entries
        },
        expanded: false,
        copyState: null,
        minuteTick: 0,
        onToggleExpanded: () => {},
        onToggleWorkRecord: () => {},
        segmentOpenState: {},
        onToggleToolSegment: () => {},
        autoCollapse: false,
        onCopyMessage: () => {},
        canBranch: false,
        highlighted: false,
        decisionAnswers: {}
      })
    )
  );

const runningWorkHtml = renderWorkEntry('running', [todoEntry]);
assert(runningWorkHtml.includes('data-turn-checklist-trigger="true"'), 'running workRecord renders checklist trigger');
assert(runningWorkHtml.includes('data-checklist-open="true"'), 'running workRecord checklist defaults to open');
assert(!runningWorkHtml.includes('data-expanded-work-record'), 'checklist card renders outside collapsed work body');

const completedWorkHtml = renderWorkEntry('completed', [todoEntry]);
assert(completedWorkHtml.includes('data-turn-checklist-trigger="true"'), 'completed workRecord renders checklist trigger');
assert(completedWorkHtml.includes('data-checklist-open="false"'), 'completed workRecord checklist defaults to collapsed');

const noChecklistHtml = renderWorkEntry('completed', [
  msg('bash-only', 'tool', t1, { kind: 'tool', toolName: 'Bash', toolInput: '{"command":"ls"}' })
]);
assert(!noChecklistHtml.includes('data-turn-checklist-trigger'), 'workRecord without checklist keeps original header');
