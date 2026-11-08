// web/ 目前没有测试运行器（package.json 里没有 vitest/jest，scripts/check.sh 只跑 tsc --noEmit），
// 所以这里不引入任何测试框架与 node 内置模块：断言失败直接抛 Error，接入运行器后本文件即可直接执行。
import { approvalShortcut, pickActiveRequestId } from './RemoteRequestPanel';

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

const keyEvent = {
  key: 'Enter', defaultPrevented: false, repeat: false, metaKey: false, ctrlKey: false,
  altKey: false, shiftKey: false, isComposing: false, keyCode: 13
};
const shortcutCases: Array<{
  event: typeof keyEvent;
  tag?: string;
  editable?: boolean;
  dialog?: boolean;
  expected: 'allow' | 'reject' | null;
}> = [
  { event: keyEvent, expected: 'allow' },
  { event: { ...keyEvent, key: 'Escape' }, expected: 'reject' },
  ...(['metaKey', 'ctrlKey', 'altKey', 'shiftKey'] as const).map((key) => ({
    event: { ...keyEvent, [key]: true }, expected: null
  })),
  { event: { ...keyEvent, defaultPrevented: true }, expected: null },
  { event: { ...keyEvent, repeat: true }, expected: null },
  { event: { ...keyEvent, isComposing: true }, expected: null },
  { event: { ...keyEvent, keyCode: 229 }, expected: null },
  { event: keyEvent, tag: 'TEXTAREA', expected: null },
  { event: keyEvent, editable: true, expected: null },
  { event: keyEvent, dialog: true, expected: null }
];
for (const testCase of shortcutCases) {
  const actual = approvalShortcut(testCase.event, testCase.tag ?? 'DIV', testCase.editable ?? false, testCase.dialog ?? false);
  if (actual !== testCase.expected) throw new Error(`快捷键结果错误：期望 ${String(testCase.expected)}，实际 ${String(actual)}`);
}
