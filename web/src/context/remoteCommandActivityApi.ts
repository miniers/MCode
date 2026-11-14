import { useCallback, useMemo } from 'react';
import type { RemoteThreadsCommandActivityResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export function useRemoteCommandActivityApi() {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod('threads.commandActivity');
  const activity = useCallback((threadID: string) => call<RemoteThreadsCommandActivityResult>('threads.commandActivity', { threadID }), [call]);
  return useMemo(() => ({ supported, activity }), [supported, activity]);
}
