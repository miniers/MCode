// 设备管理的远程调用集合（session.devices.list / session.devices.revoke），
// 对应宿主 internal/remote/device_ext_methods.go，参数与结果类型见 internal/remote/wire/device_ext_types.go。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。
// 只有列表与撤销两个方法都在 capabilities.methods 里时才认为宿主支持设备管理。
// 撤销成功后宿主广播 session.devices.changed，上层按版本号重新拉一次列表。
// 本模块只提供数据调用，不渲染任何 UI。

import { useCallback, useMemo, useState } from 'react';
import type {
  SessionDeviceEntry,
  SessionDevicesListResult,
  SessionDevicesRevokeResult
} from '../types/generated';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type { SessionDeviceEntry, SessionDevicesListResult, SessionDevicesRevokeResult };

/** 列出本机全部已授权设备，参数必须是空对象。 */
export const REMOTE_DEVICES_LIST_METHOD = 'session.devices.list';
/** 撤销一台设备，成功后宿主广播 session.devices.changed。 */
export const REMOTE_DEVICES_REVOKE_METHOD = 'session.devices.revoke';
/** 设备列表变化通知，载荷为 { deviceID, revoked }。 */
export const REMOTE_DEVICES_CHANGED_METHOD = 'session.devices.changed';

/** 设备管理的远程调用集合。 */
export interface RemoteDeviceApi {
  /** 宿主是否同时提供列表与撤销；为假时不显示设备管理入口。 */
  supported: boolean;
  /** 拉取设备列表，按配对时间升序；失败时 reject，文案可直接展示。 */
  list: () => Promise<SessionDeviceEntry[]>;
  /** 撤销指定设备；撤销当前设备会被宿主拒绝，错误文案可直接展示。 */
  revoke: (deviceID: string) => Promise<SessionDevicesRevokeResult>;
}

/** 取设备管理调用集合；能力与 call 不变时返回同一对象。 */
export function useRemoteDeviceApi(): RemoteDeviceApi {
  const { call, supportsMethod } = useRemoteCall();
  const supported =
    supportsMethod(REMOTE_DEVICES_LIST_METHOD) && supportsMethod(REMOTE_DEVICES_REVOKE_METHOD);

  const list = useCallback(async (): Promise<SessionDeviceEntry[]> => {
    const result = await call<SessionDevicesListResult>(REMOTE_DEVICES_LIST_METHOD, {});
    return Array.isArray(result?.devices) ? result.devices : [];
  }, [call]);

  const revoke = useCallback(
    (deviceID: string) =>
      call<SessionDevicesRevokeResult>(REMOTE_DEVICES_REVOKE_METHOD, { deviceID }),
    [call]
  );

  return useMemo<RemoteDeviceApi>(() => ({ supported, list, revoke }), [supported, list, revoke]);
}

/**
 * 订阅 session.devices.changed，返回一个只增不减的版本号：推送到达时加一，
 * 调用方把它放进依赖数组即可重新拉取设备列表。
 */
export function useDevicesChangeVersion(enabled = true): number {
  const [version, setVersion] = useState(0);
  useRemoteNotification(
    REMOTE_DEVICES_CHANGED_METHOD,
    () => setVersion((value) => value + 1),
    enabled
  );
  return version;
}
