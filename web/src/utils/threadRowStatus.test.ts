// 同 quotaPace.test.ts：web/ 没有测试运行器，顶层直接断言，失败时 throw。
// 这里只校验行尾状态位的优先级，以及「回合结束但仍有后台任务」的等待标记。
import type { RemoteThreadSummary } from '../types/contract';
import {
  threadBackgroundWaitCount,
  threadRowStatus,
  threadRowStatusAriaLabel
} from './threadRowStatus';

const NOW = Date.parse('2026-01-01T00:00:00Z');

// 只填必需字段，其余用例按需覆盖。
function thread(overrides: Partial<RemoteThreadSummary> = {}): RemoteThreadSummary {
  return {
    id: 'thread-1',
    projectID: 'project-1',
    title: '会话',
    provider: '',
    model: null,
    effort: null,
    isRunning: false,
    isArchived: false,
    isPinned: false,
    isDormant: false,
    hasUnread: false,
    attention: '',
    lastActivityText: null,
    section: '',
    orderKey: '',
    updatedAt: new Date(NOW - 5 * 60_000).toISOString(),
    lastStatus: null,
    hasPendingRequest: false,
    taskID: null,
    goalID: null,
    runtimeMode: 'supervised',
    interactionMode: 'build',
    fastMode: false,
    worktreeRequested: false,
    swarmEnabled: false,
    swarmPairID: null,
    swarmBeesProvider: null,
    swarmBeesModel: null,
    swarmBeesEffort: null,
    ...overrides
  };
}

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  if (actual !== expected) throw new Error(`${label} 期望 ${String(expected)}，实际 ${String(actual)}`);
}

// (a) 回合结束、还有 2 个后台任务：行尾返回 waiting，数量为 2。
const waitingThread = thread({ backgroundTasks: 2 });
assertEqual(threadBackgroundWaitCount(waitingThread), 2, 'threadBackgroundWaitCount(不忙，2 个后台任务)');
const waiting = threadRowStatus(waitingThread, NOW);
assertEqual(waiting.kind, 'waiting', 'threadRowStatus(不忙，2 个后台任务).kind');
if (waiting.kind !== 'waiting') throw new Error('threadRowStatus(不忙，2 个后台任务) 应返回 waiting');
assertEqual(waiting.count, 2, 'threadRowStatus(不忙，2 个后台任务).count');

// (b) 仍在运行：不显示等待标记，后台任务数视为 0。
const runningThread = thread({ isRunning: true, backgroundTasks: 2 });
assertEqual(threadBackgroundWaitCount(runningThread), 0, 'threadBackgroundWaitCount(运行中)');
if (threadRowStatus(runningThread, NOW).kind === 'waiting') throw new Error('运行中不应返回 waiting');

// (c) 待审批优先：即使还有后台任务也返回 pending。
const pendingThread = thread({ hasPendingRequest: true, backgroundTasks: 2 });
assertEqual(threadRowStatus(pendingThread, NOW).kind, 'pending', 'threadRowStatus(待审批，2 个后台任务).kind');

// (d) waiting 优先于未读和置顶。
const unreadPinnedThread = thread({ hasUnread: true, isPinned: true, backgroundTasks: 2 });
assertEqual(
  threadRowStatus(unreadPinnedThread, NOW).kind,
  'waiting',
  'threadRowStatus(未读 + 置顶 + 2 个后台任务).kind'
);

// (e) 没有 backgroundTasks 字段时行为不变：未读仍是未读，未读也没有则回到最近活动时间。
const unreadThread = thread({ hasUnread: true });
assertEqual(threadBackgroundWaitCount(unreadThread), 0, 'threadBackgroundWaitCount(缺省)');
assertEqual(threadRowStatus(unreadThread, NOW).kind, 'unread', 'threadRowStatus(缺省 + 未读).kind');
assertEqual(threadRowStatus(thread(), NOW).kind, 'activity', 'threadRowStatus(缺省) 回退到最近活动');

// (f) 读屏文字带上等待信息，同时保留未读。
const ariaLabel = threadRowStatusAriaLabel(thread({ backgroundTasks: 2, hasUnread: true }), NOW);
if (!ariaLabel.includes('等待 2 个后台任务')) {
  throw new Error(`threadRowStatusAriaLabel 应包含「等待 2 个后台任务」，实际 ${ariaLabel}`);
}
if (!ariaLabel.includes('未读')) {
  throw new Error(`threadRowStatusAriaLabel 应保留「未读」，实际 ${ariaLabel}`);
}
