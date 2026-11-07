// 会话人设：列表来自 providers.personas，写入走 threads.setPersona。
// 宿主没声明这两个方法（旧版本）时 supported 为假，界面上整块人设入口都不出现。

import { useCallback, useEffect, useRef, useState } from 'react';
import type {
  RemotePersona,
  RemoteProvidersPersonasParams,
  RemoteProvidersPersonasResult,
  RemoteThreadsSetPersonaParams,
  RemoteThreadSummary
} from '../types/generated';
import { useRemoteCall } from './useRemoteCall';
import { useRemoteStatic } from './RemoteContext';

/** 「不使用人设」的取值，与桌面端 personaNoneValue 一致；空串表示清除会话人设。 */
export const PERSONA_NONE_VALUE = 'none';

export interface UseThreadPersonasResult {
  /** provider/model 下可选的人设；不支持或没连接时是空数组。 */
  personas: RemotePersona[];
  loading: boolean;
  error: Error | null;
  /** 改会话人设；persona 传空串表示回到「默认（不使用人设）」。失败时抛错，由调用方提示。 */
  setPersona: (persona: string) => Promise<void>;
  /** 宿主是否同时支持读列表与写人设。 */
  supported: boolean;
}

/** 与桌面端一致：空 id 与 none 不是可选人设，列出来只会误导。 */
const selectablePersonas = (value: unknown): RemotePersona[] => {
  if (!Array.isArray(value)) return [];
  return value.filter((item): item is RemotePersona => {
    if (!item || typeof item !== 'object') return false;
    const id = (item as RemotePersona).id;
    return typeof id === 'string' && id !== '' && id !== PERSONA_NONE_VALUE;
  });
};

const toError = (err: unknown): Error => (err instanceof Error ? err : new Error(String(err)));

/**
 * 取某个会话 provider/model 的人设列表，并给出改人设的入口。
 * 只依赖 useRemoteCall：新方法不需要改动 RemoteContext，旧宿主则由 supported 兜住。
 */
export function useThreadPersonas(
  threadID: string | null | undefined,
  provider: string | null | undefined,
  model: string | null | undefined
): UseThreadPersonasResult {
  const { call, supportsMethod } = useRemoteCall();
  const { connectionState } = useRemoteStatic();
  const supported = supportsMethod('providers.personas') && supportsMethod('threads.setPersona');
  const isConnected = connectionState === 'connected';

  const [personas, setPersonas] = useState<RemotePersona[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  // 会话或 provider/model 切换后，旧请求的结果不能回踩。
  const requestIdRef = useRef(0);

  useEffect(() => {
    if (!supported || !isConnected || !provider) {
      requestIdRef.current += 1;
      setPersonas([]);
      setLoading(false);
      setError(null);
      return;
    }
    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    // 存成 const，隔一层的 async 闭包不用依赖参数上的类型收窄。
    const providerValue = provider;
    const modelValue = model ?? '';
    let cancelled = false;
    setLoading(true);
    setError(null);
    void (async () => {
      try {
        const params: RemoteProvidersPersonasParams = { provider: providerValue, model: modelValue };
        const result = await call<RemoteProvidersPersonasResult>('providers.personas', params);
        if (cancelled || requestId !== requestIdRef.current) return;
        setPersonas(selectablePersonas(result?.personas));
      } catch (err) {
        if (cancelled || requestId !== requestIdRef.current) return;
        setPersonas([]);
        setError(toError(err));
      } finally {
        if (!cancelled && requestId === requestIdRef.current) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
      if (requestIdRef.current === requestId) requestIdRef.current += 1;
    };
  }, [call, isConnected, model, provider, supported]);

  const setPersona = useCallback(
    async (persona: string): Promise<void> => {
      if (!threadID) throw new Error('当前会话不支持切换人设');
      if (!supported) throw new Error('宿主不支持切换人设');
      // 把列表用的 provider/model 一并带上：弹窗里的模型选择要等下一次发送才写回会话，
      // 宿主按这组参数校验，才和用户看到的列表是同一个目录。
      const params: RemoteThreadsSetPersonaParams = {
        threadID,
        persona,
        provider: provider ?? undefined,
        model: model ?? undefined
      };
      // 成功后宿主会推 threads.changed，会话摘要里的 persona 随广播更新，这里不用回写状态。
      await call<RemoteThreadSummary>('threads.setPersona', params);
    },
    [call, model, provider, supported, threadID]
  );

  return { personas, loading, error, setPersona, supported };
}
