// web/ 没有测试运行器，顶层直接断言，失败时 throw。
import { visibleFilterChips } from './homeFilterChips';

function assertChips(actual: string[], expected: string[], label: string): void {
  if (actual.join(',') !== expected.join(',')) {
    throw new Error(`${label} 期望 ${expected.join(',')}，实际 ${actual.join(',')}`);
  }
}

const empty = { attention: 0, running: 0, failed: 0 };
assertChips(visibleFilterChips('all', empty), [], '全部且没有状态');
assertChips(visibleFilterChips('all', { ...empty, running: 1 }), ['all', 'running'], '只有运行中');
assertChips(visibleFilterChips('archived', empty), ['all', 'archived'], '已归档且没有状态');
assertChips(visibleFilterChips('failed', empty), ['all', 'failed'], '失败数量为零但已选中');
