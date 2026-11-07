// 本仓库 web/ 没有测试运行器：直接用断言函数，失败即 throw，
// 与 backgroundWait.test.ts 保持一致（不引入测试框架与 node 内置模块）。
import { resumeRetryDelay } from './resumeRefresh';

function assertEqual(actual: unknown, expected: unknown, label: string): void {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) {
    throw new Error(`${label}: expected ${right}, got ${left}`);
  }
}

assertEqual(resumeRetryDelay(1), 1000, '第 1 次重试 1s');
assertEqual(resumeRetryDelay(2), 2000, '第 2 次重试 2s');
assertEqual(resumeRetryDelay(3), 4000, '第 3 次重试 4s');
assertEqual(resumeRetryDelay(4), null, '第 4 次放弃');
assertEqual(resumeRetryDelay(0), null, '0 视为无效次数');
assertEqual(resumeRetryDelay(-1), null, '负数视为无效次数');
