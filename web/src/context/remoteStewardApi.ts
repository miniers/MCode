import { useCallback, useMemo } from 'react';
import type { RemoteStewardOpenResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export const REMOTE_STEWARD_OPEN_METHOD = 'steward.open';

export function stewardActionErrorText(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

export function useRemoteStewardApi() {
  const { call, supportsMethod } = useRemoteCall();
  const supportsOpen = supportsMethod(REMOTE_STEWARD_OPEN_METHOD);
  const openSteward = useCallback(async (projectID: string): Promise<string> => {
    const result = await call<RemoteStewardOpenResult>(REMOTE_STEWARD_OPEN_METHOD, { projectID });
    return result?.threadID ?? '';
  }, [call]);
  return useMemo(() => ({ supportsOpen, openSteward }), [supportsOpen, openSteward]);
}
