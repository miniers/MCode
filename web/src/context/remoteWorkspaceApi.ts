import { useCallback } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type {
  RemoteCapabilityDeclaration,
  RemoteFilesListResult,
  RemoteFilesReadResult,
  RemoteFilesSearchResult,
  RemoteGitBranchesResult,
  RemoteGitStatusResult,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult
} from '../types/contract';

interface Ref<T> {
  current: T;
}

interface UseRemoteWorkspaceApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

/**
 * 工作区只读接口：Git 状态/分支、目录浏览、文件读取与搜索、消息图片读取。
 */
export function useRemoteWorkspaceApi({ clientRef, capabilities }: UseRemoteWorkspaceApiOptions) {
  const getThreadGitStatus = useCallback(async (threadID: string): Promise<RemoteGitStatusResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取 Git 状态');
    if (capabilities && (
      !capabilities.methods.includes('git.status') ||
      !capabilities.features?.includes('git.readonly')
    )) {
      throw new Error('当前宿主未声明 Git 只读能力');
    }
    const result = await client.getThreadGitStatus(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认 Git 状态');
    return result;
  }, [capabilities, clientRef]);

  const getThreadGitBranches = useCallback(async (threadID: string): Promise<RemoteGitBranchesResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取 Git 分支');
    if (capabilities && (
      !capabilities.methods.includes('git.branches') ||
      !capabilities.features?.includes('git.readonly')
    )) {
      throw new Error('当前宿主未声明 Git 只读能力');
    }
    const result = await client.getThreadGitBranches(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认 Git 分支');
    return result;
  }, [capabilities, clientRef]);

  const listThreadFiles = useCallback(async (threadID: string, subpath?: string): Promise<RemoteFilesListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取工作区目录');
    if (capabilities && (
      !capabilities.methods.includes('files.list') ||
      !capabilities.features?.includes('files.read')
    )) {
      throw new Error('当前宿主未声明工作区文件浏览能力');
    }
    const result = await client.listThreadFiles(threadID, subpath);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取工作区目录');
    return result;
  }, [capabilities, clientRef]);

  const readThreadFile = useCallback(async (
    threadID: string,
    path: string,
    offset?: number,
    length?: number
  ): Promise<RemoteFilesReadResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取文件内容');
    if (capabilities && (
      !capabilities.methods.includes('files.read') ||
      !capabilities.features?.includes('files.read')
    )) {
      throw new Error('当前宿主未声明工作区文件浏览能力');
    }
    const result = await client.readThreadFile(threadID, path, offset, length);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取文件内容');
    return result;
  }, [capabilities, clientRef]);

  const searchThreadFiles = useCallback(async (threadID: string, query: string): Promise<RemoteFilesSearchResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法搜索工作区文件');
    if (capabilities && (
      !capabilities.methods.includes('files.search') ||
      !capabilities.features?.includes('files.read')
    )) {
      throw new Error('当前宿主未声明工作区文件浏览能力');
    }
    const result = await client.searchThreadFiles(threadID, query);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新搜索工作区文件');
    return result;
  }, [capabilities, clientRef]);

  const readThreadMedia = useCallback(async (
    params: RemoteThreadsMediaReadParams
  ): Promise<RemoteThreadsMediaReadResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取图片');
    if (capabilities && (
      !capabilities.methods.includes('threads.media.read') ||
      !capabilities.features?.includes('threads.media')
    )) {
      throw new Error('当前宿主未声明消息图片读取能力');
    }
    const result = await client.readThreadMedia(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取图片');
    return result;
  }, [capabilities, clientRef]);

  return {
    getThreadGitStatus,
    getThreadGitBranches,
    listThreadFiles,
    readThreadFile,
    searchThreadFiles,
    readThreadMedia
  };
}
