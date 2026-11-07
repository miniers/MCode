import { useCallback } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration } from '../types/contract';
import type {
  RemoteThreadsBulkArchiveParams,
  RemoteThreadsBulkArchiveResult,
  RemoteThreadsBulkMarkReadParams,
  RemoteThreadsBulkMarkReadResult,
  RemoteThreadsBulkPreviewParams,
  RemoteThreadsBulkPreviewResult,
  RemoteThreadsBulkMarkDormantParams,
  RemoteThreadsBulkMarkDormantResult,
  RemoteThreadsBulkUndoParams,
  RemoteThreadsBulkUndoResult
} from '../types/generated';

interface Ref<T> {
  current: T;
}

interface UseRemoteBulkApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

function assertBulkCapability(capabilities: RemoteCapabilityDeclaration | null, method: string) {
  if (capabilities && !capabilities.methods.includes(method)) {
    throw new Error(`当前宿主未声明 ${method} 能力`);
  }
}

/**
 * 批量整理接口：按时间范围预览命中会话，再批量归档、标记已休眠、标记已读，并支持撤销。
 */
export function useRemoteBulkApi({ clientRef, capabilities }: UseRemoteBulkApiOptions) {
  const previewBulkThreads = useCallback(async (
    params: RemoteThreadsBulkPreviewParams
  ): Promise<RemoteThreadsBulkPreviewResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法统计可整理会话');
    assertBulkCapability(capabilities, 'threads.bulk.preview');
    const result = await client.previewBulkThreads(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新统计可整理会话');
    return result;
  }, [capabilities, clientRef]);

  const archiveBulkThreads = useCallback(async (
    params: RemoteThreadsBulkArchiveParams
  ): Promise<RemoteThreadsBulkArchiveResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法批量归档');
    assertBulkCapability(capabilities, 'threads.bulk.archive');
    const result = await client.archiveBulkThreads(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请到 Mac 端确认归档结果');
    return result;
  }, [capabilities, clientRef]);

  const markDormantBulkThreads = useCallback(async (
    params: RemoteThreadsBulkMarkDormantParams
  ): Promise<RemoteThreadsBulkMarkDormantResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法批量标记已休眠');
    assertBulkCapability(capabilities, 'threads.bulk.markDormant');
    const result = await client.markDormantBulkThreads(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请到 Mac 端确认休眠结果');
    return result;
  }, [capabilities, clientRef]);

  const markBulkThreadsRead = useCallback(async (
    params: RemoteThreadsBulkMarkReadParams
  ): Promise<RemoteThreadsBulkMarkReadResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法批量标记已读');
    assertBulkCapability(capabilities, 'threads.bulk.markRead');
    const result = await client.markBulkThreadsRead(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请到 Mac 端确认已读结果');
    return result;
  }, [capabilities, clientRef]);

  const undoBulkThreads = useCallback(async (
    params: RemoteThreadsBulkUndoParams
  ): Promise<RemoteThreadsBulkUndoResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法撤销批量整理');
    assertBulkCapability(capabilities, 'threads.bulk.undo');
    const result = await client.undoBulkThreads(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请到 Mac 端确认撤销结果');
    return result;
  }, [capabilities, clientRef]);

  return {
    previewBulkThreads,
    archiveBulkThreads,
    markDormantBulkThreads,
    markBulkThreadsRead,
    undoBulkThreads
  };
}
