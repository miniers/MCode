import { useCallback, useEffect } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import { McodeRpcClient } from '../services/rpc';
import {
  clearSessionCredentials,
  extractPairingTokenFromURL
} from '../services/storage';
import type { DeviceIdentity, SessionCredentials } from '../services/storage';
import { clearAllDrafts } from '../services/draftStorage';
import type {
  Board,
  ConnectionState,
  RemoteRequestsChangedParams,
  RemoteThreadsQueueChangedParams,
  ThreadsChangedParams,
  ThreadsEventParams
} from '../types/contract';
import type { RemoteSwarmState } from '../types/swarm';

interface Ref<T> {
  current: T;
}

interface UseRemoteConnectionOptions {
  clientRef: Ref<McodeRpcClient | null>;
  credentialsRef: Ref<SessionCredentials | null>;
  rememberDeviceRef: Ref<boolean>;
  connectionStateRef: Ref<ConnectionState>;
  forgetInProgressRef: Ref<boolean>;
  forgetRequestRef: Ref<number>;
  isSyncingRef: Ref<boolean>;
  activeSyncPromiseRef: Ref<Promise<void> | null>;
  syncGenerationRef: Ref<number>;
  stopActiveSubscription: () => Promise<void>;
  markClientDisconnected: (client: McodeRpcClient) => void;
  handleThreadEvent: (client: McodeRpcClient, event: ThreadsEventParams) => void;
  handleThreadsChanged: (change: ThreadsChangedParams) => void;
  handleRequestsChanged: (change: RemoteRequestsChangedParams) => void;
  handleQueueChanged: (change: RemoteThreadsQueueChangedParams) => void;
  handleSwarmChanged?: (state: RemoteSwarmState) => void;
  resetForNewClient: (pairToken: boolean) => void;
  resetAfterAuthFailure: () => void;
  resetAfterClearAuth: () => void;
  setRememberDeviceState: Dispatch<SetStateAction<boolean>>;
  setDeviceIdentity: Dispatch<SetStateAction<DeviceIdentity | null>>;
  setCredentialNotice: Dispatch<SetStateAction<string | null>>;
  setForgetInProgress: Dispatch<SetStateAction<boolean>>;
  setBoard: Dispatch<SetStateAction<Board | null>>;
  setConnectionState: Dispatch<SetStateAction<ConnectionState>>;
  setConnectionMessage: Dispatch<SetStateAction<string>>;
  setAuthError: Dispatch<SetStateAction<string | null>>;
  setPairingError: Dispatch<SetStateAction<string | null>>;
  setPairingInProgress: Dispatch<SetStateAction<boolean>>;
  setIsSyncing: Dispatch<SetStateAction<boolean>>;
}

export function useRemoteConnection({
  clientRef,
  credentialsRef,
  rememberDeviceRef,
  connectionStateRef,
  forgetInProgressRef,
  forgetRequestRef,
  isSyncingRef,
  activeSyncPromiseRef,
  syncGenerationRef,
  stopActiveSubscription,
  markClientDisconnected,
  handleThreadEvent,
  handleThreadsChanged,
  handleRequestsChanged,
  handleQueueChanged,
  handleSwarmChanged,
  resetForNewClient,
  resetAfterAuthFailure,
  resetAfterClearAuth,
  setRememberDeviceState,
  setDeviceIdentity,
  setCredentialNotice,
  setForgetInProgress,
  setBoard,
  setConnectionState,
  setConnectionMessage,
  setAuthError,
  setPairingError,
  setPairingInProgress,
  setIsSyncing
}: UseRemoteConnectionOptions) {
  const setRememberDevice = useCallback((remember: boolean) => {
    rememberDeviceRef.current = remember;
    setRememberDeviceState(remember);
    clientRef.current?.setRememberDevice(remember);
  }, [clientRef, rememberDeviceRef]);

  const createClient = useCallback((pairToken: string | null) => {
    forgetRequestRef.current++;
    forgetInProgressRef.current = false;
    setForgetInProgress(false);
    void stopActiveSubscription();
    const previousClient = clientRef.current;
    clientRef.current = null;
    previousClient?.dispose();
    activeSyncPromiseRef.current = null;
    syncGenerationRef.current++;
    isSyncingRef.current = true;
    setIsSyncing(true);

    resetForNewClient(Boolean(pairToken));

    if (pairToken) {
      credentialsRef.current = null;
      setDeviceIdentity(null);
      setCredentialNotice(clearSessionCredentials());
    }

    const client = new McodeRpcClient(
      {
        onConnectionChange: (state, message) => {
          if (clientRef.current !== client) return;
          if (state !== 'connected') {
            markClientDisconnected(client);
          }
          if (state !== 'connected' && state !== connectionStateRef.current) {
            syncGenerationRef.current++;
            activeSyncPromiseRef.current = null;
            isSyncingRef.current = true;
            setIsSyncing(true);
          }
          setConnectionState(state);
          connectionStateRef.current = state;
          if (message) setConnectionMessage(message);
          if (state === 'connected') {
            setPairingInProgress(false);
            setPairingError(null);
          } else if (state === 'unpaired') {
            isSyncingRef.current = false;
            setIsSyncing(false);
          }
        },
        onBoardUpdated: (newBoard) => {
          if (clientRef.current !== client) return;
          setBoard(newBoard);
          // 重新认证的初始快照确认连接；恢复中的推送不能代替本次探测响应。
          if (
            connectionStateRef.current !== 'connected' &&
            document.visibilityState === 'visible' && navigator.onLine !== false
          ) {
            isSyncingRef.current = false;
            setIsSyncing(false);
          }
        },
        onAuthFailed: (errorMessage) => {
          if (clientRef.current !== client) return;
          void stopActiveSubscription();
          resetAfterAuthFailure();
          setAuthError(errorMessage);
        },
        onCredentialsChanged: (credentials, notice) => {
          if (clientRef.current !== client) return;
          credentialsRef.current = credentials;
          setDeviceIdentity(credentials?.deviceIdentity ?? null);
          if (credentials) {
            rememberDeviceRef.current = credentials.rememberDevice;
            setRememberDeviceState(credentials.rememberDevice);
          }
          setCredentialNotice(notice);
        },
        onThreadEvent: (event) => {
          const activeClient = clientRef.current;
          if (activeClient) handleThreadEvent(activeClient, event);
        },
        onThreadsChanged: (change) => {
          if (clientRef.current === client) handleThreadsChanged(change);
        },
        onRequestsChanged: (change) => {
          if (clientRef.current === client) handleRequestsChanged(change);
        },
        onQueueChanged: (change) => {
          if (clientRef.current === client) handleQueueChanged(change);
        },
        onSwarmChanged: (state) => {
          if (clientRef.current === client) handleSwarmChanged?.(state);
        }
      },
      pairToken,
      credentialsRef.current,
      rememberDeviceRef.current
    );

    clientRef.current = client;
    client.start();
  }, [
    activeSyncPromiseRef,
    clientRef,
    connectionStateRef,
    credentialsRef,
    forgetInProgressRef,
    forgetRequestRef,
    handleThreadEvent,
    handleQueueChanged,
    handleRequestsChanged,
    handleThreadsChanged,
    isSyncingRef,
    markClientDisconnected,
    rememberDeviceRef,
    resetAfterAuthFailure,
    resetForNewClient,
    stopActiveSubscription,
    syncGenerationRef,
  ]);

  useEffect(() => {
    const initialPairing = extractPairingTokenFromURL();
    if (initialPairing.token) {
      rememberDeviceRef.current = true;
      setRememberDeviceState(true);
    }
    createClient(initialPairing.token);
    if (initialPairing.error) setPairingError(initialPairing.error);

    const handleHashChange = () => {
      const pairing = extractPairingTokenFromURL();
      if (pairing.token) {
        if (forgetInProgressRef.current) return;
        rememberDeviceRef.current = true;
        setRememberDeviceState(true);
        createClient(pairing.token);
      } else if (pairing.error) {
        setPairingError(pairing.error);
      }
    };
    window.addEventListener('hashchange', handleHashChange);

    return () => {
      window.removeEventListener('hashchange', handleHashChange);
      const currentClient = clientRef.current;
      clientRef.current = null;
      currentClient?.dispose();
    };
  }, [clientRef, createClient, forgetInProgressRef, rememberDeviceRef]);

  const pairWithToken = useCallback(
    (pairToken: string) => {
      if (forgetInProgressRef.current) return;
      createClient(pairToken);
    },
    [createClient, forgetInProgressRef]
  );

  const reconnect = useCallback(() => {
    if (
      forgetInProgressRef.current ||
      connectionStateRef.current === 'unpaired' ||
      connectionStateRef.current === 'auth_failed'
    ) return;
    const canStart = Boolean(clientRef.current?.canReconnect);
    if (!canStart && !credentialsRef.current) return;
    setAuthError(null);
    syncGenerationRef.current++;
    activeSyncPromiseRef.current = null;
    isSyncingRef.current = true;
    setIsSyncing(true);
    if (canStart) {
      clientRef.current?.start();
      return;
    }
    // iOS 在 pagehide 后可能已 dispose 客户端，凭据仍在时重建客户端兜底。
    if (credentialsRef.current) {
      createClient(null);
    }
  }, [
    activeSyncPromiseRef,
    clientRef,
    connectionStateRef,
    createClient,
    credentialsRef,
    forgetInProgressRef,
    isSyncingRef,
    syncGenerationRef
  ]);

  const performSync = useCallback(
    async (isManual: boolean): Promise<void> => {
      if (
        connectionStateRef.current === 'unpaired' ||
        connectionStateRef.current === 'auth_failed'
      ) {
        if (isManual) {
          throw new Error(
            connectionStateRef.current === 'auth_failed'
              ? '配对凭据已失效，请在 Mac 端重新扫码配对'
              : '未检测到配对凭据，请在 Mac 端扫描二维码配对'
          );
        }
        return;
      }

      isSyncingRef.current = true;
      setIsSyncing(true);
      // 先确认连接可用，避免 iOS 冻结遗留的在途 promise 挡住页面恢复后的探测。
      const client = clientRef.current;
      if (!client || !client.isReady) {
        reconnect();
        if (isManual) {
          const msg = '未与 Mac 建立有效连接，正在尝试重新连接...';
          setConnectionMessage(msg);
          throw new Error(msg);
        }
        return;
      }

      if (activeSyncPromiseRef.current) {
        return activeSyncPromiseRef.current;
      }

      const generation = syncGenerationRef.current;
      const syncPromise = client.refreshBoard(10000).then(
        () => {
          if (
            clientRef.current === client && generation === syncGenerationRef.current &&
            document.visibilityState === 'visible' && navigator.onLine !== false
          ) {
            isSyncingRef.current = false;
            setIsSyncing(false);
          }
        },
        (err: unknown) => {
          if (clientRef.current !== client || generation !== syncGenerationRef.current) return;
          reconnect();
          if (isManual) {
            const errorMsg =
              '刷新看板失败：' + (err instanceof Error ? err.message : '连接异常') + '，正在重新连接...';
            setConnectionMessage(errorMsg);
            throw err;
          }
        }
      ).finally(() => {
        if (activeSyncPromiseRef.current === syncPromise) {
          activeSyncPromiseRef.current = null;
        }
      });

      activeSyncPromiseRef.current = syncPromise;
      return syncPromise;
    },
    [
      activeSyncPromiseRef,
      clientRef,
      connectionStateRef,
      isSyncingRef,
      reconnect,
      syncGenerationRef
    ]
  );

  const markPendingUpdate = useCallback(() => {
    if (
      connectionStateRef.current === 'unpaired' ||
      connectionStateRef.current === 'auth_failed'
    ) {
      return;
    }
    syncGenerationRef.current++;
    activeSyncPromiseRef.current = null;
    isSyncingRef.current = true;
    setIsSyncing(true);
  }, [
    activeSyncPromiseRef,
    connectionStateRef,
    isSyncingRef,
    syncGenerationRef
  ]);

  useEffect(() => {
    const PROBE_DEBOUNCE_MS = 400;
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;

    const scheduleProbe = () => {
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        void performSync(false).catch(() => {});
      }, PROBE_DEBOUNCE_MS);
    };

    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') {
        scheduleProbe();
      } else {
        if (debounceTimer) {
          clearTimeout(debounceTimer);
          debounceTimer = null;
        }
        markPendingUpdate();
      }
    };

    const handlePageShow = () => {
      if (document.visibilityState === 'visible') {
        scheduleProbe();
      } else {
        markPendingUpdate();
      }
    };

    const handlePageHide = () => {
      if (debounceTimer) {
        clearTimeout(debounceTimer);
        debounceTimer = null;
      }
      markPendingUpdate();
    };

    const handleOnline = () => {
      if (document.visibilityState === 'visible') {
        scheduleProbe();
      } else {
        markPendingUpdate();
      }
    };

    const handleOffline = () => {
      if (debounceTimer) {
        clearTimeout(debounceTimer);
        debounceTimer = null;
      }
      markPendingUpdate();
    };

    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('pageshow', handlePageShow);
    window.addEventListener('pagehide', handlePageHide);
    window.addEventListener('online', handleOnline);
    window.addEventListener('offline', handleOffline);

    return () => {
      if (debounceTimer) clearTimeout(debounceTimer);
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('pageshow', handlePageShow);
      window.removeEventListener('pagehide', handlePageHide);
      window.removeEventListener('online', handleOnline);
      window.removeEventListener('offline', handleOffline);
    };
  }, [markPendingUpdate, performSync]);

  const clearAuth = useCallback(() => {
    credentialsRef.current = null;
    setDeviceIdentity(null);
    const storageNotice = clearSessionCredentials();
    // 草稿可能含敏感文本，忘记设备/退出时一并清除（仅用户主动退出；令牌失效不走这里）。
    clearAllDrafts();
    setCredentialNotice(storageNotice);
    forgetRequestRef.current++;
    forgetInProgressRef.current = false;
    setForgetInProgress(false);
    resetAfterClearAuth();
    void stopActiveSubscription();
    const currentClient = clientRef.current;
    clientRef.current = null;
    currentClient?.dispose();
    return storageNotice;
  }, [
    clientRef,
    credentialsRef,
    forgetInProgressRef,
    forgetRequestRef,
    resetAfterClearAuth,
    stopActiveSubscription
  ]);

  const forgetDevice = useCallback(async (): Promise<void> => {
    if (forgetInProgressRef.current) return;
    const client = clientRef.current;
    const identity = credentialsRef.current?.deviceIdentity;
    const exitLocally = (reason: string): void => {
      const storageNotice = clearAuth();
      setCredentialNotice([
        storageNotice ? '已退出当前页面。' : '已清除此浏览器的配对凭据与本地草稿。',
        reason,
        storageNotice
      ].filter(Boolean).join(' '));
    };

    if (!client?.isReady || navigator.onLine === false) {
      exitLocally('当前未连接 Mac，尚未确认 Mac 撤销；请到 Mac 设置 → 远程控制撤销此设备。');
      return;
    }
    if (!identity) {
      setCredentialNotice('缺少当前设备身份，尚未撤销 Mac 上的配对；请重新连接获取身份，或到 Mac 设置 → 远程控制撤销此设备。');
      return;
    }

    const requestID = ++forgetRequestRef.current;
    forgetInProgressRef.current = true;
    setForgetInProgress(true);
    setCredentialNotice(null);
    try {
      await client.revokeDevice(identity.deviceID);
      if (clientRef.current !== client || forgetRequestRef.current !== requestID) return;
      const storageNotice = clearAuth();
      setCredentialNotice(['Mac 上的配对已撤销，已忘记此设备。', storageNotice].filter(Boolean).join(' '));
    } catch (err) {
      if (clientRef.current !== client || forgetRequestRef.current !== requestID) return;
      if (!client.isReady || !navigator.onLine) {
        exitLocally('连接在撤销确认前中断，尚未确认 Mac 撤销；请到 Mac 设置 → 远程控制核对并撤销此设备。');
      } else {
        setCredentialNotice(`未能确认 Mac 撤销：${err instanceof Error ? err.message : '请求失败'}。当前连接与凭据已保留，可重试，或到 Mac 设置 → 远程控制撤销此设备。`);
      }
    } finally {
      if (forgetRequestRef.current === requestID) {
        forgetInProgressRef.current = false;
        setForgetInProgress(false);
      }
    }
  }, [
    clearAuth,
    clientRef,
    credentialsRef,
    forgetInProgressRef,
    forgetRequestRef,
  ]);

  const refreshBoard = useCallback(async () => {
    await performSync(true);
  }, [performSync]);

  return {
    clearAuth,
    createClient,
    forgetDevice,
    pairWithToken,
    reconnect,
    refreshBoard,
    setRememberDevice
  };
}
