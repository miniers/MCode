// 桌面端更新的远程调用，参数与结果类型由协议生成。
import { useCallback, useMemo } from 'react';
import { RpcError } from '../services/rpc';
import type { AppUpdateInstallResult, AppUpdateStatus } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export const REMOTE_APP_UPDATE_GET_METHOD = 'app.update.get';
export const REMOTE_APP_UPDATE_CHECK_METHOD = 'app.update.check';
export const REMOTE_APP_UPDATE_INSTALL_METHOD = 'app.update.install';

export interface RemoteAppUpdateApi {
  get: () => Promise<AppUpdateStatus>;
  check: () => Promise<AppUpdateStatus>;
  install: () => Promise<AppUpdateInstallResult>;
}

export function isAppUpdateMethodNotFound(error: unknown): boolean {
  return error instanceof RpcError && error.code === -32601;
}

export function useRemoteAppUpdateApi(): RemoteAppUpdateApi {
  const { call } = useRemoteCall();
  const get = useCallback(() => call<AppUpdateStatus>(REMOTE_APP_UPDATE_GET_METHOD, {}), [call]);
  const check = useCallback(() => call<AppUpdateStatus>(REMOTE_APP_UPDATE_CHECK_METHOD, {}), [call]);
  const install = useCallback(() => call<AppUpdateInstallResult>(REMOTE_APP_UPDATE_INSTALL_METHOD, {}), [call]);
  return useMemo(() => ({ get, check, install }), [get, check, install]);
}
