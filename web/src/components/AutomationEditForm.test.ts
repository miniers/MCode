// 无框架测试：web/ 没有测试运行器，顶层断言失败即抛错。
import {
  createDebouncedPreview,
  shouldShowAutomationPreview
} from './AutomationEditForm';
import type { RemoteAutomationExtPreviewResult } from '../context/remoteAutomationApi';

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

function deferred<T>(): { promise: Promise<T>; resolve: (value: T) => void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}

function wait(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

const stale = deferred<string>();
const results: Array<string | null> = [];
const requests: string[] = [];
const runner = createDebouncedPreview<string, string>(
  (value) => {
    requests.push(value);
    return value === 'old' ? stale.promise : Promise.resolve(value);
  },
  (result) => results.push(result),
  20
);

runner.update('superseded');
await wait(10);
runner.update('old');
await wait(25);
assertEqualJSON(requests, ['old'], '去抖后发出的请求');
runner.update('new');
await wait(25);
stale.resolve('stale result');
await Promise.resolve();
assertEqual(results[results.length - 1], 'new', '最新一次预览结果');
assertEqual(results.includes('stale result'), false, '结果中是否包含过期响应');
runner.cancel();

const preview: RemoteAutomationExtPreviewResult = { times: ['2030-01-01T00:00:00Z'], exhausted: false };
assertEqual(shouldShowAutomationPreview(false, preview), false, '方法不可用时是否显示预览');
assertEqual(shouldShowAutomationPreview(true, preview), true, '有运行时间时是否显示预览');
assertEqual(shouldShowAutomationPreview(true, { times: [], exhausted: true }), true, '已排尽时是否显示预览');
assertEqual(shouldShowAutomationPreview(true, { times: [], exhausted: false }), false, '无运行时间且未排尽时是否显示预览');
