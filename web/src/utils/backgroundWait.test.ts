// 本仓库 web/ 没有测试运行器：直接用断言函数，失败即 throw，
// 与 threadRoute.test.ts 保持一致（不引入测试框架与 node 内置模块）。
import type { RemoteThreadSummary } from '../types/contract';
import {
  BACKGROUND_WAIT_PLACEHOLDER,
  backgroundWaitCount,
  backgroundWaitStatusText,
  composerPlaceholderFor
} from './backgroundWait';

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) {
    throw new Error(`${label}: expected ${right}, got ${left}`);
  }
}

// 只关心本模块读到的字段：是否在忙、后台任务数，其余字段与判定无关。
function stubSummary(fields: Partial<RemoteThreadSummary>): RemoteThreadSummary {
  return { isRunning: false, ...fields } as RemoteThreadSummary;
}

// 会话为空、忙、空闲带后台任务、缺省字段四种取值。
assertEqual(backgroundWaitCount(null), 0, '摘要为 null');
assertEqual(backgroundWaitCount(undefined), 0, '摘要为 undefined');
assertEqual(
  backgroundWaitCount(stubSummary({ isRunning: true, backgroundTasks: 2 })),
  0,
  '主线程运行中不重复提示'
);
assertEqual(
  backgroundWaitCount(stubSummary({ swarmBusy: true, backgroundTasks: 2 })),
  0,
  '子代理运行中不重复提示'
);
assertEqual(backgroundWaitCount(stubSummary({ backgroundTasks: 2 })), 2, '空闲时取后台任务数');
assertEqual(backgroundWaitCount(stubSummary({})), 0, '缺省字段视为无后台任务');

assertEqual(composerPlaceholderFor(0), '继续输入消息…', '无等待时用默认占位');
assertEqual(composerPlaceholderFor(1), BACKGROUND_WAIT_PLACEHOLDER, '有等待时用等待占位');
assertEqual(backgroundWaitStatusText(3), '等待后台 · 3 项', '状态文字带数量');
