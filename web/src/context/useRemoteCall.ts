// 通用远程调用与通知订阅：新增 RPC 方法或服务端通知时不必再扩展 RemoteContext。

import { useEffect, useRef } from 'react';
import { useRemoteStatic } from './RemoteContext';

export interface UseRemoteCallResult {
  /** 按方法名直接调用宿主；未连接或客户端未就绪时 reject「未连接到 Mac」。 */
  call: <T>(method: string, params?: unknown, timeoutMs?: number) => Promise<T>;
  /** 宿主能力清单是否声明了该方法；清单未到位时按可用处理。 */
  supportsMethod: (method: string) => boolean;
  /** 订阅任意方法的服务端通知，返回取消订阅函数。 */
  onNotification: (method: string, handler: (params: unknown) => void) => () => void;
}

/** 通用远程调用入口：直接取上下文里的稳定引用。 */
export function useRemoteCall(): UseRemoteCallResult {
  const { callRemote, supportsMethod, onRemoteNotification } = useRemoteStatic();
  return { call: callRemote, supportsMethod, onNotification: onRemoteNotification };
}

/**
 * 订阅一条服务端通知：只随 method / enabled 重新订阅，
 * handler 用 ref 保存最新版本，避免内联函数导致反复订阅。
 */
export function useRemoteNotification(
  method: string,
  handler: (params: unknown) => void,
  enabled = true
): void {
  const { onRemoteNotification } = useRemoteStatic();
  const handlerRef = useRef(handler);

  useEffect(() => {
    handlerRef.current = handler;
  }, [handler]);

  useEffect(() => {
    if (!enabled) return;
    return onRemoteNotification(method, (params) => handlerRef.current(params));
  }, [enabled, method, onRemoteNotification]);
}
