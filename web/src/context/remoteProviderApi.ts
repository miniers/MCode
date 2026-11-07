import { useCallback } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type { RemoteCapabilityDeclaration, RemoteSwarmComposerInfo, RemoteProvidersListResult } from '../types/contract';
import type { ComposerQuotaScope, RemoteComposerQuotaResult } from '../types/quota';
import type { ComposerSelectionMemoryParams } from '../utils/composerOptions';
import type { RemoteProviderListWithCatalog } from './remoteTypes';

interface Ref<T> {
  current: T;
}

interface UseRemoteProviderApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
  setProviders: Dispatch<SetStateAction<RemoteProviderListWithCatalog>>;
  setSwarm: Dispatch<SetStateAction<RemoteSwarmComposerInfo | null>>;
  setProvidersLoading: Dispatch<SetStateAction<boolean>>;
  setProvidersError: Dispatch<SetStateAction<string | null>>;
}

/**
 * Provider 目录、额度与推送订阅相关的 RPC 调用；目录结果写回调用方传入的 state setter。
 */
export function useRemoteProviderApi({
  clientRef,
  capabilities,
  setProviders,
  setSwarm,
  setProvidersLoading,
  setProvidersError
}: UseRemoteProviderApiOptions) {
  const listProviders = useCallback(async (projectID?: string): Promise<RemoteProvidersListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取 Provider 目录');
    if (capabilities && !capabilities.methods.includes('providers.list')) {
      throw new Error('当前宿主未声明 providers.list 能力');
    }
    setProvidersLoading(true);
    try {
      const result = await client.listProviders(projectID);
      if (clientRef.current !== client) throw new Error('连接已更换，请重新读取 Provider 目录');
      const list = (Array.isArray(result.providers) ? [...result.providers] : []) as RemoteProviderListWithCatalog;
      if (result.catalog && Array.isArray(result.catalog)) {
        list.catalog = result.catalog;
      }
      setProviders(list);
      // 旧宿主不返回 swarm 字段，按“无可用组合”处理
      setSwarm(result.swarm ?? null);
      setProvidersError(null);
      return result;
    } catch (err) {
      if (clientRef.current === client) {
        setProvidersError(err instanceof Error ? err.message : '加载 Provider 目录失败');
      }
      throw err;
    } finally {
      if (clientRef.current === client) setProvidersLoading(false);
    }
  }, [capabilities, clientRef, setSwarm, setProviders, setProvidersError, setProvidersLoading]);

  const getComposerQuota = useCallback(async (scope: ComposerQuotaScope): Promise<RemoteComposerQuotaResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取额度');
    if (capabilities && !capabilities.methods.includes('composer.quota')) {
      throw new Error('当前宿主未声明 composer.quota 能力');
    }
    return client.getComposerQuota(scope);
  }, [capabilities, clientRef]);

  /** 把用户在输入区主动改的模型类选择写进宿主记忆；返回的 draft 是最新默认值。不响 loading 状态，失败由调用方静默处理。 */
  const rememberProviderSelection = useCallback(
    async (params: ComposerSelectionMemoryParams): Promise<RemoteProvidersListResult> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法记录模型选择');
      if (capabilities && !capabilities.methods.includes('providers.rememberSelection')) {
        throw new Error('当前宿主未声明 providers.rememberSelection 能力');
      }
      return client.rememberProviderSelection(params);
    },
    [capabilities, clientRef]
  );

  const getPushPublicKey = useCallback(async (): Promise<{ publicKey: string }> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取推送公钥');
    if (capabilities && !capabilities.methods.includes('push.publicKey')) {
      throw new Error('当前宿主未声明 push.publicKey 能力');
    }
    return client.getPushPublicKey();
  }, [capabilities, clientRef]);

  const subscribePush = useCallback(
    async (params: { endpoint: string; p256dh: string; auth: string }): Promise<{ subscribed: boolean }> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法订阅推送');
      if (capabilities && !capabilities.methods.includes('push.subscribe')) {
        throw new Error('当前宿主未声明 push.subscribe 能力');
      }
      return client.subscribePush(params);
    },
    [capabilities, clientRef]
  );

  const unsubscribePush = useCallback(
    async (endpoint: string): Promise<{ unsubscribed: boolean }> => {
      const client = clientRef.current;
      if (!client) throw new Error('未连接到 Mac 端，无法取消推送订阅');
      if (capabilities && !capabilities.methods.includes('push.unsubscribe')) {
        throw new Error('当前宿主未声明 push.unsubscribe 能力');
      }
      return client.unsubscribePush(endpoint);
    },
    [capabilities, clientRef]
  );

  return {
    listProviders,
    rememberProviderSelection,
    getComposerQuota,
    getPushPublicKey,
    subscribePush,
    unsubscribePush
  };
}
