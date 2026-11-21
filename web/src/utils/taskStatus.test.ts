import {
  ATTENTION_KIND_GROUPS,
  GOAL_STATUS_LABELS,
  KANBAN_COLUMN_LABELS,
  TASK_STATUS_LABELS,
  attentionKindLabel,
  automationRunStatusInfo,
  goalStatusClass,
  goalStatusLabel,
  isStewardOrigin,
  kanbanColumnLabel,
  stewardTaskStatusInfo,
  taskStatusLabel,
  taskStatusToneClass
} from './taskStatus';

function assertEqual(actual: unknown, expected: unknown, message?: string): void {
  if (JSON.stringify(actual) !== JSON.stringify(expected)) {
    throw new Error(
      `${message ? `${message}: ` : ''}期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`
    );
  }
}

// 1. 看板 5 列
assertEqual(KANBAN_COLUMN_LABELS, {
  todo: '待办',
  in_progress: '进行中',
  needs_you: '待你处理',
  review: '待审阅',
  done: '已完成'
});
assertEqual(kanbanColumnLabel('todo'), '待办');
assertEqual(kanbanColumnLabel('in_progress'), '进行中');
assertEqual(kanbanColumnLabel('needs_you'), '待你处理');
assertEqual(kanbanColumnLabel('review'), '待审阅');
assertEqual(kanbanColumnLabel('done'), '已完成');
assertEqual(kanbanColumnLabel('unknown'), '未知状态');

// 2. 任务 9 态与语义色
assertEqual(TASK_STATUS_LABELS, {
  todo: '待办',
  in_progress: '进行中',
  needs_approval: '待审批',
  waiting: '等待中',
  review: '待审阅',
  done: '已完成',
  rejected: '已驳回',
  failed: '失败',
  cancelled: '已取消'
});
assertEqual(taskStatusLabel('rejected'), '已驳回');
assertEqual(taskStatusLabel('review'), '待审阅');
assertEqual(taskStatusLabel('done'), '已完成');
assertEqual(taskStatusLabel('needs_approval'), '待审批');
assertEqual(taskStatusLabel('unknown'), '未知状态');

assertEqual(taskStatusToneClass({ status: 'failed' }), 'thread-status--danger');
assertEqual(taskStatusToneClass({ status: 'needs_approval' }), 'thread-status--warning');
assertEqual(taskStatusToneClass({ status: 'rejected' }), 'thread-status--warning');
assertEqual(taskStatusToneClass({ status: 'waiting', waitKind: 'human' }), 'thread-status--warning');
assertEqual(taskStatusToneClass({ status: 'todo', blocked: true }), 'thread-status--warning');
assertEqual(taskStatusToneClass({ status: 'todo', displayColumn: 'needs_you' }), 'thread-status--warning');
assertEqual(taskStatusToneClass({ status: 'review' }), 'thread-status--primary');
assertEqual(taskStatusToneClass({ status: 'in_progress' }), 'thread-status--running');
assertEqual(taskStatusToneClass({ status: 'done' }).includes('var(--mc-success)'), true);
assertEqual(taskStatusToneClass({ status: 'todo' }), '');

assertEqual(stewardTaskStatusInfo({ status: 'waiting', waitKind: 'human' }), {
  label: '待你处理',
  color: 'thread-status--warning'
});
assertEqual(stewardTaskStatusInfo({ status: 'needs_approval' }), {
  label: '待审批',
  color: 'thread-status--warning'
});
assertEqual(stewardTaskStatusInfo({ status: 'waiting', waitKind: 'machine' }), {
  label: '等待中',
  color: ''
});

// 3. 目标 7 态与语义色
assertEqual(GOAL_STATUS_LABELS, {
  planning: '规划中',
  plan_failed: '规划失败',
  plan_review: '方案待审',
  running: '进行中',
  paused: '已暂停',
  completed: '已完成',
  cancelled: '已取消'
});
assertEqual(goalStatusLabel('running'), '进行中');
assertEqual(goalStatusLabel('paused'), '已暂停');
assertEqual(goalStatusLabel('completed'), '已完成');
assertEqual(goalStatusClass('plan_failed'), 'thread-status--danger');
assertEqual(goalStatusClass('plan_review'), 'thread-status--warning');
assertEqual(goalStatusClass('planning'), 'thread-status--running');
assertEqual(goalStatusClass('running'), 'thread-status--running');
assertEqual(goalStatusClass('completed').includes('var(--mc-success)'), true);
assertEqual(goalStatusClass('paused'), 'text-[var(--foreground-muted)]');
assertEqual(goalStatusClass('cancelled'), 'text-[var(--foreground-muted)]');

// 4. 自动化 5 运行态
assertEqual(automationRunStatusInfo('running'), {
  label: '运行中',
  className: 'text-[var(--status-running)]'
});
assertEqual(automationRunStatusInfo('succeeded'), {
  label: '成功',
  className: 'text-[var(--mc-success)]'
});
assertEqual(automationRunStatusInfo('failed'), {
  label: '失败',
  className: 'text-[var(--mc-danger)]'
});
assertEqual(automationRunStatusInfo('stopped'), {
  label: '已停止',
  className: 'text-[var(--foreground-muted)]'
});
assertEqual(automationRunStatusInfo('skipped'), {
  label: '已跳过',
  className: 'text-[var(--foreground-muted)]'
});

// 5. 收件箱 13 种事件类型
assertEqual(ATTENTION_KIND_GROUPS.length, 13);
assertEqual(attentionKindLabel('thread_stalled'), '会话卡住');
assertEqual(attentionKindLabel('thread_failed'), '会话出错');
assertEqual(attentionKindLabel('thread_quota'), '额度用尽');
assertEqual(attentionKindLabel('thread_approval'), '等待审批');
assertEqual(attentionKindLabel('kanban_failed'), '看板失败');
assertEqual(attentionKindLabel('kanban_needs_approval'), '看板待审批');
assertEqual(attentionKindLabel('kanban_review'), '看板待验收');
assertEqual(attentionKindLabel('kanban_plan_failed'), '规划失败');
assertEqual(attentionKindLabel('automation_failed'), '自动化失败');
assertEqual(attentionKindLabel('automation_finished'), '自动化完成');
assertEqual(attentionKindLabel('bee_failed'), '子代理失败');
assertEqual(attentionKindLabel('merge_review_pending'), '合并待审阅');
assertEqual(attentionKindLabel('thread_no_progress'), '任务无进展');
assertEqual(attentionKindLabel('unknown_kind'), '其他');

// 6. 总管来源判定
assertEqual(isStewardOrigin('owner'), true);
assertEqual(isStewardOrigin('steward'), true);
assertEqual(isStewardOrigin('user'), false);
assertEqual(isStewardOrigin(undefined), false);
