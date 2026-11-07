import { useCallback } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration } from '../types/contract';
import type { RemoteSwarmMergeDiffResult, RemoteSwarmTier, RemoteSwarmTiersResult } from '../types/generated';
import type { RemoteSwarmState } from '../types/swarm';

interface Ref<T> {
  current: T;
}

interface UseRemoteSwarmApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
}

/**
 * 编排相关 RPC 调用：读取/开关状态、停止单个或全部子代理、清除已完成子代理。
 */
export function useRemoteSwarmApi({ clientRef, capabilities }: UseRemoteSwarmApiOptions) {
  const getThreadSwarm = useCallback(async (threadID: string): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取编排状态');
    const result = await client.getThreadSwarm(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认编排状态');
    return result;
  }, [clientRef]);

  const setThreadSwarm = useCallback(async (params: {
    threadID: string;
    enabled: boolean;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法更新编排设置');
    const result = await client.setThreadSwarm(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认编排设置');
    return result;
  }, [clientRef]);

  const stopSwarmBee = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法停止子代理');
    const result = await client.stopSwarmBee(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认停止结果');
    return result;
  }, [clientRef]);

  const stopAllSwarmBees = useCallback(async (params: {
    threadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法停止全部子代理');
    if (capabilities && !capabilities.methods.includes('threads.swarm.stopAll')) {
      throw new Error('当前宿主未声明 threads.swarm.stopAll 能力');
    }
    const result = await client.stopAllSwarmBees(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认停止结果');
    return result;
  }, [capabilities, clientRef]);

  const clearFinishedSwarmBees = useCallback(async (params: {
    threadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法清除已完成的子代理');
    if (capabilities && !capabilities.methods.includes('threads.swarm.clearFinished')) {
      throw new Error('当前宿主未声明 threads.swarm.clearFinished 能力');
    }
    const result = await client.clearFinishedSwarmBees(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认清除结果');
    return result;
  }, [capabilities, clientRef]);

  const getSwarmTiers = useCallback(async (): Promise<RemoteSwarmTiersResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取子代理档位');
    if (capabilities && !capabilities.methods.includes('swarm.tiers.get')) {
      throw new Error('当前宿主未声明 swarm.tiers.get 能力');
    }
    const result = await client.getSwarmTiers();
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取子代理档位');
    return result;
  }, [capabilities, clientRef]);

  const setSwarmTiers = useCallback(async (params: {
    tiers: RemoteSwarmTier[];
    actionID: string;
  }): Promise<RemoteSwarmTiersResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法保存子代理档位');
    if (capabilities && !capabilities.methods.includes('swarm.tiers.set')) {
      throw new Error('当前宿主未声明 swarm.tiers.set 能力');
    }
    const result = await client.setSwarmTiers(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认子代理档位');
    return result;
  }, [capabilities, clientRef]);

  /** 把子代理的提问转交主线程回答；返回最新编排状态。 */
  const forwardQuestion = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
    requestID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法转交子代理提问');
    if (capabilities && !capabilities.methods.includes('threads.swarm.forwardQuestion')) {
      throw new Error('当前宿主未声明 threads.swarm.forwardQuestion 能力');
    }
    const result = await client.forwardSwarmQuestion(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认转交结果');
    return result;
  }, [capabilities, clientRef]);

  /** 读取单个子代理待合并改动的 diff（只读，不动编排状态）。 */
  const getMergeDiff = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
  }): Promise<RemoteSwarmMergeDiffResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取待合并改动');
    if (capabilities && !capabilities.methods.includes('threads.swarm.mergeDiff')) {
      throw new Error('当前宿主未声明 threads.swarm.mergeDiff 能力');
    }
    const result = await client.getSwarmMergeDiff(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取待合并改动');
    return result;
  }, [capabilities, clientRef]);

  /** 批准把子代理改动合入主工作副本。 */
  const approveMerge = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法批准合并');
    if (capabilities && !capabilities.methods.includes('threads.swarm.approveMerge')) {
      throw new Error('当前宿主未声明 threads.swarm.approveMerge 能力');
    }
    const result = await client.approveSwarmMerge(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认合并结果');
    return result;
  }, [capabilities, clientRef]);

  /** 放弃子代理改动。 */
  const discardMerge = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法放弃子代理改动');
    if (capabilities && !capabilities.methods.includes('threads.swarm.discardMerge')) {
      throw new Error('当前宿主未声明 threads.swarm.discardMerge 能力');
    }
    const result = await client.discardSwarmMerge(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认放弃结果');
    return result;
  }, [capabilities, clientRef]);

  return {
    getThreadSwarm,
    setThreadSwarm,
    stopSwarmBee,
    stopAllSwarmBees,
    clearFinishedSwarmBees,
    getSwarmTiers,
    setSwarmTiers,
    forwardQuestion,
    getMergeDiff,
    approveMerge,
    discardMerge
  };
}
