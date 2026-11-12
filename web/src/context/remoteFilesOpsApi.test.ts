// 无框架测试：web/ 没有测试运行器，顶层断言失败即抛错。
import { createRemoteFilesOpsApi, filesOpsSupport } from './remoteFilesOpsApi';

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

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望包含 “${needle}”，实际为：${haystack}`);
  }
}

const methods = ['files.create', 'files.rename', 'files.trash', 'files.duplicate'];
const calls: Array<{ method: string; params: unknown }> = [];
const api = createRemoteFilesOpsApi(async <T>(method: string, params?: unknown) => {
  calls.push({ method, params });
  return {} as T;
}, methods);
const threadID = 'thread';
await api.create({ threadID, path: 'dir/new', dir: true });
await api.rename({ threadID, path: 'dir/a', newPath: 'dir/b' });
await api.trash({ threadID, path: 'dir/b' });
await api.duplicate({ threadID, path: 'dir/a' });
assertEqualJSON(calls, [
  { method: 'files.create', params: { threadID, path: 'dir/new', dir: true } },
  { method: 'files.rename', params: { threadID, path: 'dir/a', newPath: 'dir/b' } },
  { method: 'files.trash', params: { threadID, path: 'dir/b' } },
  { method: 'files.duplicate', params: { threadID, path: 'dir/a' } }
], '各方法发送的参数');

assertEqualJSON(
  filesOpsSupport(['files.rename']),
  { create: false, rename: true, trash: false, duplicate: false },
  '文件操作能力'
);
assertEqual(Object.values(filesOpsSupport(undefined)).some(Boolean), false, '未声明能力时是否有可用操作');

const unsupported = createRemoteFilesOpsApi(async <T>() => { throw new Error('unexpected'); return undefined as T; }, []);
let unsupportedError = '';
try {
  await unsupported.trash({ threadID: 'thread', path: 'a' });
} catch (error) {
  unsupportedError = error instanceof Error ? error.message : String(error);
}
assertIncludes(unsupportedError, '未声明 files.trash 能力');
