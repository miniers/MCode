// @ts-expect-error Vitest is provided by the test runner but is not a web runtime dependency.
import { describe, expect, it } from 'vitest';
import { createRemoteFilesOpsApi, filesOpsSupport } from './remoteFilesOpsApi';

describe('remote files operations API', () => {
  it('sends the typed parameters for each method', async () => {
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
    expect(calls).toEqual([
      { method: 'files.create', params: { threadID, path: 'dir/new', dir: true } },
      { method: 'files.rename', params: { threadID, path: 'dir/a', newPath: 'dir/b' } },
      { method: 'files.trash', params: { threadID, path: 'dir/b' } },
      { method: 'files.duplicate', params: { threadID, path: 'dir/a' } }
    ]);
  });

  it('reports capabilities and refuses calls for missing methods', async () => {
    expect(filesOpsSupport(['files.rename'])).toEqual({ create: false, rename: true, trash: false, duplicate: false });
    expect(Object.values(filesOpsSupport(undefined)).some(Boolean)).toBe(false);
    const unsupported = createRemoteFilesOpsApi(async <T>() => { throw new Error('unexpected'); return undefined as T; }, []);
    await expect(unsupported.trash({ threadID: 'thread', path: 'a' })).rejects.toThrow('未声明 files.trash 能力');
  });
});
