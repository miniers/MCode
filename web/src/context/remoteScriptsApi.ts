import { useCallback } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration } from '../types/contract';
import type { ScriptsSaveParams, ScriptsSaveScript } from '../types/generated';

export type RemoteScriptsSaveScript = ScriptsSaveScript;

export interface RemotePackageScript {
  name: string;
  command: string;
  isFavorite: boolean;
}

export interface RemoteProjectScript {
  id: string;
  name: string;
  symbol: string | null;
  /** 脚本命令；旧宿主不回该字段。 */
  command?: string;
  /** 新建工作树时是否自动运行；旧宿主不回该字段。 */
  runOnWorktreeCreate?: boolean;
}

export interface RemoteScriptsListResult {
  directory: string;
  runner: string;
  packageScripts: RemotePackageScript[];
  projectScripts: RemoteProjectScript[];
  /** 项目脚本版本号，写回时作为 expectedRevision 传回；旧宿主不回该字段。 */
  revision?: string;
}

/** scripts.save 的版本冲突错误（宿主回 -32009），调用方据此提示刷新后重试。 */
export class ScriptsSaveConflictError extends Error {
  readonly code = -32009;
  constructor(message = '脚本已在 Mac 端被修改，请刷新后重试') {
    super(message);
    this.name = 'ScriptsSaveConflictError';
  }
}

export interface RemoteScriptsRunTarget {
  kind: 'package' | 'project';
  name?: string;
  scriptID?: string;
}

export interface RemoteScriptsRunParams extends RemoteScriptsRunTarget {
  threadID: string;
}

export interface RemoteScriptsRunResult {
  ok: true;
  runID: string;
}

export interface RemoteScriptsRunStatusParams {
  threadID: string;
  runID: string;
}

/** 脚本运行状态：running 时无 exitCode、output 为空；rejected 为并发上限超时被拒 */
export interface RemoteScriptsRunStatus {
  runID: string;
  label: string;
  status: 'running' | 'finished' | 'rejected';
  exitCode?: number;
  output: string;
}

export interface RemoteScriptsFavoriteParams {
  threadID: string;
  name: string;
  isFavorite: boolean;
}

export interface RemoteScriptsFavoriteResult {
  favorites: string[];
}

interface Ref<T> {
  current: T;
}

interface UseRemoteScriptsApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

function assertScriptsCapability(capabilities: RemoteCapabilityDeclaration | null, method: string) {
  if (capabilities && (
    !capabilities.methods.includes(method) ||
    !capabilities.features?.includes('scripts.run')
  )) {
    throw new Error('当前宿主未声明脚本能力');
  }
}

const isRevisionConflict = (err: unknown): boolean =>
  typeof err === 'object' && err !== null && (err as { code?: unknown }).code === -32009;

/**
 * 脚本接口：列出 package.json scripts 与项目脚本、在 Mac 端执行并轮询运行结果、切换 package 脚本收藏。
 */
export function useRemoteScriptsApi({ clientRef, capabilities }: UseRemoteScriptsApiOptions) {
  const listThreadScripts = useCallback(async (threadID: string): Promise<RemoteScriptsListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取脚本列表');
    assertScriptsCapability(capabilities, 'scripts.list');
    const result = await client.listThreadScripts(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取脚本列表');
    return result;
  }, [capabilities, clientRef]);

  const runThreadScript = useCallback(async (
    threadID: string,
    target: RemoteScriptsRunTarget
  ): Promise<RemoteScriptsRunResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法运行脚本');
    assertScriptsCapability(capabilities, 'scripts.run');
    const result = await client.runThreadScript({ threadID, ...target });
    if (clientRef.current !== client) throw new Error('连接已更换，脚本可能已在 Mac 端启动，请到 Mac 端确认');
    return result;
  }, [capabilities, clientRef]);

  const getThreadScriptRunStatus = useCallback(async (
    threadID: string,
    runID: string
  ): Promise<RemoteScriptsRunStatus> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取脚本运行结果');
    assertScriptsCapability(capabilities, 'scripts.runStatus');
    const result = await client.getScriptRunStatus({ threadID, runID });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取脚本运行结果');
    return result;
  }, [capabilities, clientRef]);

  const setThreadScriptFavorite = useCallback(async (
    threadID: string,
    name: string,
    isFavorite: boolean
  ): Promise<RemoteScriptsFavoriteResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法更新脚本收藏');
    assertScriptsCapability(capabilities, 'scripts.setFavorite');
    const result = await client.setThreadScriptFavorite({ threadID, name, isFavorite });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新更新脚本收藏');
    return result;
  }, [capabilities, clientRef]);

  /**
   * 整表保存项目脚本：scripts 里 id 为空表示新建，symbol 可传 null 清空。
   * expectedRevision 取自 scripts.list 结果的 revision，不一致时抛 ScriptsSaveConflictError。
   */
  const saveScripts = useCallback(async (
    projectID: string,
    scripts: ScriptsSaveScript[],
    expectedRevision?: string
  ): Promise<RemoteScriptsListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法保存脚本');
    assertScriptsCapability(capabilities, 'scripts.save');
    const params: ScriptsSaveParams = {
      projectID,
      scripts,
      ...(expectedRevision ? { expectedRevision } : {})
    };
    try {
      const result = await client.call<RemoteScriptsListResult>('scripts.save', params, 30000);
      if (clientRef.current !== client) throw new Error('连接已更换，脚本可能已保存，请到 Mac 端确认');
      return result;
    } catch (err) {
      if (isRevisionConflict(err)) throw new ScriptsSaveConflictError();
      throw err instanceof Error ? err : new Error(String(err));
    }
  }, [capabilities, clientRef]);

  return {
    listThreadScripts,
    runThreadScript,
    getThreadScriptRunStatus,
    setThreadScriptFavorite,
    saveScripts
  };
}
