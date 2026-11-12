// 无框架测试：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
import { renderToStaticMarkup } from 'react-dom/server';
import { RemoteProvider } from '../context/RemoteContext';
import { DiffViewer, getRevertHunkRanges } from './DiffViewer';
import { parseFilesFromUnifiedDiff } from '../utils/diff';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertEqualJSON(actual: unknown, expected: unknown, what: string): void {
  const actualText = JSON.stringify(actual);
  const expectedText = JSON.stringify(expected);
  if (actualText !== expectedText) {
    throw new Error(`期望${what}为 ${expectedText}，实际为 ${actualText}`);
  }
}

const lf = String.fromCharCode(10);
const diffText = [
  'diff --git a/a.go b/a.go', '--- a/a.go', '+++ a/a.go', '@@ -1,5 +1,5 @@',
  ' one', '-two', '+TWO', ' three', '-four', '+FOUR', ' five'
].join(lf);

const file = parseFilesFromUnifiedDiff(diffText)[0];
assertEqualJSON(
  getRevertHunkRanges(file.lines).map(({ startLine, lineCount }) => [startLine, lineCount]),
  [[2, 1], [4, 1]],
  '分离改动的服务端行坐标'
);
const deletion = parseFilesFromUnifiedDiff([
  'diff --git a/a.go b/a.go', '--- a/a.go', '+++ a/a.go', '@@ -1 +0,0 @@', '-old'
].join(lf))[0];
assertEqualJSON(
  getRevertHunkRanges(deletion.lines).map(({ startLine, lineCount }) => [startLine, lineCount]),
  [[1, 0]],
  '文件开头删除的服务端行坐标'
);

const html = renderToStaticMarkup(
  <RemoteProvider>
    <DiffViewer diffText={diffText} />
  </RemoteProvider>
);
assertEqual(html.includes('aria-label="撤销此块"'), false, '未提供回调时是否渲染撤销按钮');
