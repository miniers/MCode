import { useCallback, useMemo } from 'react';
import type { RemoteThreadsBackgroundTaskLogResult, RemoteThreadsStopBackgroundTaskResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export function useRemoteBackgroundTaskApi() {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod('threads.backgroundTaskLog') && supportsMethod('threads.stopBackgroundTask');
  const log = useCallback((threadID: string, messageID: string) => {
    if (!supportsMethod('threads.backgroundTaskLog')) return Promise.reject(new Error('不支持后台任务日志'));
    return call<RemoteThreadsBackgroundTaskLogResult>('threads.backgroundTaskLog', { threadID, messageID });
  }, [call, supportsMethod]);
  const stop = useCallback((threadID: string, messageID: string) => {
    if (!supportsMethod('threads.stopBackgroundTask')) return Promise.reject(new Error('不支持停止后台任务'));
    return call<RemoteThreadsStopBackgroundTaskResult>('threads.stopBackgroundTask', { threadID, messageID });
  }, [call, supportsMethod]);
  return useMemo(() => ({ supported, log, stop }), [supported, log, stop]);
}
