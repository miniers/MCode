// 会话菜单扩展 RPC：会话级编排模式/只规划/子代理档位、标题重生成与移到 worktree。
// 与 remoteSwarmApi 同构：连接更换后立即作废在途结果，宿主未声明能力时给中文报错。

import { useCallback } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration, RemoteThreadSummary } from '../types/contract';
import type { RemoteSwarmState } from '../types/swarm';
import type {
  RemoteThreadsTitleRegenerateResult,
  RuntimeWorktreeExecuteParams,
  RuntimeWorktreeExecuteResult,
  RuntimeWorktreePreviewParams,
  RuntimeWorktreePreviewResult,
  RuntimeLimitTextResult
} from '../types/generated';

interface Ref<T> {
  current: T;
}

/** 与宿主 threads.swarm.setMode 一致的工作模式取值 */
export type RemoteSwarmThreadMode = NonNullable<RemoteThreadSummary['swarmMode']>;

interface UseRemoteThreadMenuApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

function assertMethod(capabilities: RemoteCapabilityDeclaration | null, method: string): void {
  if (capabilities && !capabilities.methods.includes(method)) {
    throw new Error(`当前宿主未声明 ${method} 能力`);
  }
}

/**
 * 会话菜单（编排模式、只规划覆盖、子代理档位覆盖、标题重生成、Worktree 移到 worktree）的 RPC 集合。
 */
export function useRemoteThreadMenuApi({ clientRef, capabilities }: UseRemoteThreadMenuApiOptions) {
  const setThreadSwarmMode = useCallback(
    async (params: {
      threadID: string;
      mode: RemoteSwarmThreadMode;
      actionID: string;
    }): Promise<RemoteThreadSummary> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法设置工作模式');
      assertMethod(capabilities, 'threads.swarm.setMode');
      const result = await client.setThreadSwarmMode(params);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新确认工作模式');
      return result;
    },
    [capabilities, clientRef]
  );

  const setThreadSwarmPlansOnly = useCallback(
    async (params: {
      threadID: string;
      plansOnly: boolean | null;
      actionID: string;
    }): Promise<RemoteSwarmState> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法设置主代理只规划');
      assertMethod(capabilities, 'threads.swarm.setPlansOnly');
      const result = await client.setThreadSwarmPlansOnly(params);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新确认只规划状态');
      return result;
    },
    [capabilities, clientRef]
  );

  const setThreadSwarmBeeTiers = useCallback(
    async (params: {
      threadID: string;
      beeTiers: string;
      actionID?: string;
    }): Promise<RemoteSwarmState> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法保存子代理档位覆盖');
      assertMethod(capabilities, 'threads.swarm.setBeeTiers');
      const result = await client.setThreadSwarmBeeTiers(params);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新确认档位覆盖');
      return result;
    },
    [capabilities, clientRef]
  );

  const regenerateThreadTitle = useCallback(
    async (threadID: string): Promise<RemoteThreadsTitleRegenerateResult> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法重新生成标题');
      assertMethod(capabilities, 'threads.title.regenerate');
      const result = await client.regenerateThreadTitle(threadID);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新确认标题');
      return result;
    },
    [capabilities, clientRef]
  );

  const limitProviderText = useCallback(
    async (threadID: string): Promise<RuntimeLimitTextResult> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法生成交接提示词');
      assertMethod(capabilities, 'threads.provider.limitText');
      const result = await client.limitProviderText(threadID);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新确认交接提示词');
      return result;
    },
    [capabilities, clientRef]
  );

  const previewWorktreeMove = useCallback(
    async (params: RuntimeWorktreePreviewParams): Promise<RuntimeWorktreePreviewResult> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法预览移到 worktree 的条件');
      assertMethod(capabilities, 'threads.worktree.preview');
      const result = await client.previewWorktreeMove(params);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新预览移到 worktree 的条件');
      return result;
    },
    [capabilities, clientRef]
  );

  const executeWorktreeMove = useCallback(
    async (params: RuntimeWorktreeExecuteParams): Promise<RuntimeWorktreeExecuteResult> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法移到 worktree');
      assertMethod(capabilities, 'threads.worktree.execute');
      const result = await client.executeWorktreeMove(params);
      if (clientRef.current !== client) throw new Error('连接已更换，无法确认是否移到 worktree 成功');
      return result;
    },
    [capabilities, clientRef]
  );

  return {
    setThreadSwarmMode,
    setThreadSwarmPlansOnly,
    setThreadSwarmBeeTiers,
    regenerateThreadTitle,
    limitProviderText,
    previewWorktreeMove,
    executeWorktreeMove
  };
}
