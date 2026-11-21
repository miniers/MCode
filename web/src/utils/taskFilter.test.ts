// web/ 没有测试运行器，顶层直接断言，失败时 throw。
import type { RemoteAutomationExtAutomation } from '../context/remoteAutomationApi';
import type { Goal, TaskItem } from '../types/contract';
import {
  filterAutomations,
  filterKanbanGoals,
  filterKanbanTasks
} from './taskFilter';

function assertIDs(actual: { id: string }[], expected: string[], label: string): void {
  const got = actual.map((item) => item.id).join(',');
  const want = expected.join(',');
  if (got !== want) {
    throw new Error(`${label} 期望 [${want}]，实际 [${got}]`);
  }
}

function makeTask(overrides: Partial<TaskItem> & { id: string }): TaskItem {
  return {
    projectID: 'proj-1',
    threadID: null,
    reviewThreadID: null,
    goalID: null,
    dependsOn: [],
    title: '默认任务',
    notes: '',
    status: 'todo',
    priority: 'medium',
    updatedAt: '2026-11-21T00:00:00Z',
    blocked: false,
    reviewSummary: null,
    reviewFeedback: null,
    reviewStatus: null,
    canStart: true,
    canMove: true,
    canReview: false,
    canApprove: false,
    attentionFlags: [],
    displayColumn: 'todo',
    lastActivityText: null,
    attemptCount: 0,
    lastFailureReason: null,
    revision: 1,
    ...overrides
  };
}

function makeGoal(overrides: Partial<Goal> & { id: string }): Goal {
  return {
    projectID: 'proj-1',
    title: '默认目标',
    prompt: '实现目标',
    status: 'running',
    policy: {
      maxConcurrency: 2,
      previewPlan: true,
      autoAccept: false,
      maxAttempts: 3
    },
    progress: {
      total: 4,
      done: 1,
      running: 1,
      waitingOnYou: 0,
      failed: 0,
      blocked: 0,
      ready: 2,
      fraction: 0.25,
      needsAttention: false
    },
    planThreadID: null,
    planSummary: null,
    statusReason: null,
    resultSummary: null,
    revision: 1,
    updatedAt: '2026-11-21T00:00:00Z',
    ...overrides
  };
}

function makeAutomation(
  overrides: Partial<RemoteAutomationExtAutomation> & { id: string }
): RemoteAutomationExtAutomation {
  return {
    name: '默认自动化',
    prompt: '定时巡检',
    schedule: { type: 'interval', seconds: 3600 },
    scheduleText: '每小时',
    enabled: true,
    unreadCount: 0,
    consecutiveFailures: 0,
    maxConsecutiveFailures: 3,
    maxIterations: 5,
    revision: 1,
    ...overrides
  };
}

const tasks: TaskItem[] = [
  makeTask({ id: 't-todo', title: '整理接口文档', notes: '补充 WebSocket 契约', status: 'todo', displayColumn: 'todo' }),
  makeTask({ id: 't-approval', title: '发布审批', status: 'needs_approval', displayColumn: 'needs_you' }),
  makeTask({ id: 't-failed', title: '构建镜像', status: 'failed', displayColumn: 'in_progress' }),
  makeTask({ id: 't-rejected', title: '修复样式', status: 'rejected', displayColumn: 'todo' }),
  makeTask({ id: 't-human', title: '确认部署参数', status: 'waiting', waitKind: 'human', displayColumn: 'in_progress' }),
  makeTask({ id: 't-review', title: '验收登录页', status: 'review', displayColumn: 'review', origin: 'steward' }),
  makeTask({ id: 't-owner', title: '总管巡检', status: 'in_progress', displayColumn: 'in_progress', origin: 'owner' })
];

assertIDs(filterKanbanTasks(tasks, '', 'all'), ['t-todo', 't-approval', 't-failed', 't-rejected', 't-human', 't-review', 't-owner'], '看板任务 all');
assertIDs(filterKanbanTasks(tasks, '', 'needs_you'), ['t-approval', 't-failed', 't-rejected', 't-human'], '看板任务 needs_you');
assertIDs(filterKanbanTasks(tasks, '', 'review'), ['t-review'], '看板任务 review');
assertIDs(filterKanbanTasks(tasks, '', 'steward'), ['t-review', 't-owner'], '看板任务 steward');
assertIDs(filterKanbanTasks(tasks, '  websocket ', 'all'), ['t-todo'], '看板任务按 notes 大小写无关搜索');
assertIDs(filterKanbanTasks(tasks, '待审阅', 'all'), ['t-review'], '看板任务按中文状态待审阅搜索');
assertIDs(filterKanbanTasks(tasks, '已驳回', 'all'), ['t-rejected'], '看板任务按中文状态已驳回搜索');
assertIDs(filterKanbanTasks(tasks, '总管', 'all'), ['t-review', 't-owner'], '看板任务按总管标识搜索');
assertIDs(filterKanbanTasks(tasks, '总管', 'review'), ['t-review'], '看板任务组合筛选 review + 总管');

const goals: Goal[] = [
  makeGoal({ id: 'g-review', title: '重构路由', prompt: '迁移到 hash router', status: 'plan_review', owner: 'steward' }),
  makeGoal({ id: 'g-failed', title: '生成单测', prompt: '覆盖过滤逻辑', status: 'plan_failed' }),
  makeGoal({
    id: 'g-waiting',
    title: '发布新版本',
    status: 'running',
    progress: {
      total: 2,
      done: 0,
      running: 0,
      waitingOnYou: 1,
      failed: 0,
      blocked: 0,
      ready: 1,
      fraction: 0,
      needsAttention: true
    }
  }),
  makeGoal({
    id: 'g-prog-fail',
    title: '清理日志',
    status: 'running',
    progress: {
      total: 2,
      done: 0,
      running: 0,
      waitingOnYou: 0,
      failed: 1,
      blocked: 0,
      ready: 1,
      fraction: 0,
      needsAttention: true
    }
  }),
  makeGoal({ id: 'g-run', title: '日常同步', status: 'running' })
];

assertIDs(filterKanbanGoals(goals, '', 'all'), ['g-review', 'g-failed', 'g-waiting', 'g-prog-fail', 'g-run'], '目标 all');
assertIDs(filterKanbanGoals(goals, '', 'needs_you'), ['g-review', 'g-failed', 'g-waiting', 'g-prog-fail'], '目标 needs_you');
assertIDs(filterKanbanGoals(goals, '', 'review'), ['g-review'], '目标 review');
assertIDs(filterKanbanGoals(goals, '', 'steward'), ['g-review'], '目标 steward');
assertIDs(filterKanbanGoals(goals, '方案待审', 'all'), ['g-review'], '目标按中文状态方案待审搜索');
assertIDs(filterKanbanGoals(goals, '规划失败', 'all'), ['g-failed'], '目标按中文状态规划失败搜索');
assertIDs(filterKanbanGoals(goals, 'ROUTER', 'all'), ['g-review'], '目标按 prompt 大小写无关搜索');

const automations: RemoteAutomationExtAutomation[] = [
  makeAutomation({ id: 'a-ok', name: '每日构建', prompt: '执行 build', enabled: true, lastRunStatus: 'succeeded' }),
  makeAutomation({ id: 'a-fail', name: '回归测试', prompt: '跑集成测试', enabled: true, lastRunStatus: 'failed' }),
  makeAutomation({ id: 'a-status-fail', name: '同步索引', enabled: true, status: 'failed' }),
  makeAutomation({ id: 'a-paused', name: '周报汇总', prompt: '汇总本周 PR', enabled: false, lastRunStatus: 'succeeded' }),
  makeAutomation({ id: 'a-status-paused', name: '夜间清理', enabled: true, status: 'paused' })
];

assertIDs(filterAutomations(automations, '', 'all'), ['a-ok', 'a-fail', 'a-status-fail', 'a-paused', 'a-status-paused'], '自动化 all');
assertIDs(filterAutomations(automations, '', 'failed'), ['a-fail', 'a-status-fail'], '自动化 failed');
assertIDs(filterAutomations(automations, '', 'paused'), ['a-paused', 'a-status-paused'], '自动化 paused');
assertIDs(filterAutomations(automations, '构建', 'all'), ['a-ok'], '自动化按名称搜索');
assertIDs(filterAutomations(automations, '失败', 'all'), ['a-fail'], '自动化按运行状态中文名失败搜索');
assertIDs(filterAutomations(automations, '已暂停', 'all'), ['a-paused'], '自动化按停用状态中文名已暂停搜索');
assertIDs(filterAutomations(automations, 'pr', 'paused'), ['a-paused'], '自动化组合筛选 paused + prompt');
