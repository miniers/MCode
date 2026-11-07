// web/ 目前没有测试运行器（package.json 里没有 vitest/jest，scripts/check.sh 只跑 tsc --noEmit），
// 所以这里不引入任何测试框架与 node 内置模块：断言失败直接抛 Error，接入运行器后本文件即可直接执行。
import { pickActiveRequestId } from './RemoteRequestPanel';

interface PickCase {
  name: string;
  ids: string[];
  currentID: string | null;
  expected: string | null;
}

const cases: PickCase[] = [
  { name: '空列表返回 null', ids: [], currentID: null, expected: null },
  { name: '空列表即便记着旧 id 也返回 null', ids: [], currentID: 'a', expected: null },
  { name: '当前项还在列表里就保持它', ids: ['a', 'b', 'c'], currentID: 'b', expected: 'b' },
  { name: '没有当前项时默认第一条', ids: ['a', 'b', 'c'], currentID: null, expected: 'a' },
  { name: '当前项已被处理掉时回到第一条', ids: ['b', 'c'], currentID: 'a', expected: 'b' },
  { name: '只剩一条时返回这一条', ids: ['x'], currentID: 'gone', expected: 'x' }
];

for (const testCase of cases) {
  const requests = testCase.ids.map((requestID) => ({ requestID }));
  const actual = pickActiveRequestId(requests, testCase.currentID);
  if (actual !== testCase.expected) {
    throw new Error(`期望「${testCase.name}」得到 ${String(testCase.expected)}，实际为 ${String(actual)}`);
  }
}
