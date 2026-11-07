// 远程设置（settings.get / settings.set / settings.changed）的调用封装，
// 对应宿主 internal/remote/settings_ext_methods.go 与 internal/app/remote_settings.go。
// 值类型与白名单都由宿主返回：客户端不硬编码键集合，只按 writableKeys 渲染表单。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。

import { useCallback, useEffect, useState } from 'react';
import type {
  RemoteSettingsChangedParams,
  RemoteSettingsSnapshot,
  RemoteSettingsValue,
  RemoteSettingsValues
} from '../types/generated';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type {
  RemoteSettingsChangedParams,
  RemoteSettingsSnapshot,
  RemoteSettingsValue,
  RemoteSettingsValues
};

export const REMOTE_SETTINGS_GET_METHOD = 'settings.get';
export const REMOTE_SETTINGS_SET_METHOD = 'settings.set';
export const REMOTE_SETTINGS_CHANGED_METHOD = 'settings.changed';

/** 设置读写集合：快照 + 单键写入 + 远端变更订阅。 */
export interface RemoteSettingsApi {
  /** 宿主是否提供 settings.get；为假时设置面板不渲染远端表单。 */
  supportsSettings: boolean;
  /** 最近一次读到的快照；未加载成功时为 null。 */
  snapshot: RemoteSettingsSnapshot | null;
  loading: boolean;
  loadError: string | null;
  /** 正在提交的键名，用于禁用对应控件。 */
  savingKey: string | null;
  /** 重新拉取快照。 */
  reload: () => void;
  /** 提交单个键；成功返回 null，失败返回服务端错误文案，调用方据此回滚。 */
  setValue: (key: string, value: RemoteSettingsValue) => Promise<string | null>;
}

const errorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error && error.message ? error.message : fallback;

/**
 * 读取并写入远端设置。
 * settings.changed 只带变化的键，因此就地合并到快照，桌面端改动后表单自动刷新。
 */
export function useRemoteSettingsApi(): RemoteSettingsApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsSettings = supportsMethod(REMOTE_SETTINGS_GET_METHOD);
  const [snapshot, setSnapshot] = useState<RemoteSettingsSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [savingKey, setSavingKey] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  useEffect(() => {
    if (!supportsSettings) {
      setSnapshot(null);
      setLoading(false);
      setLoadError(null);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setLoadError(null);
    call<RemoteSettingsSnapshot>(REMOTE_SETTINGS_GET_METHOD, {})
      .then((result) => {
        if (cancelled) return;
        setSnapshot(result ?? null);
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        setLoadError(errorMessage(error, '读取设置失败，请稍后重试'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [call, reloadToken, supportsSettings]);

  useRemoteNotification(
    REMOTE_SETTINGS_CHANGED_METHOD,
    (params) => {
      const values = (params as Partial<RemoteSettingsChangedParams> | null)?.values;
      if (!values) return;
      // 快照还没读到时不拼接，避免出现半份 values；下一次 get 会带全量。
      setSnapshot((current) =>
        current ? { ...current, values: { ...current.values, ...values } } : current
      );
    },
    supportsSettings
  );

  const reload = useCallback(() => setReloadToken((token) => token + 1), []);

  const setValue = useCallback(
    async (key: string, value: RemoteSettingsValue): Promise<string | null> => {
      if (!supportsSettings) return '当前 Mac 不支持远程修改设置';
      setSavingKey(key);
      try {
        // 只提交改动的这一个键：服务端按白名单原子写入，任一键非法整体拒绝。
        const result = await call<RemoteSettingsSnapshot>(REMOTE_SETTINGS_SET_METHOD, {
          values: { [key]: value }
        });
        if (result?.values) setSnapshot(result);
        return null;
      } catch (error) {
        return errorMessage(error, '保存设置失败，请稍后重试');
      } finally {
        setSavingKey(null);
      }
    },
    [call, supportsSettings]
  );

  return {
    supportsSettings,
    snapshot,
    loading,
    loadError,
    savingKey,
    reload,
    setValue
  };
}
