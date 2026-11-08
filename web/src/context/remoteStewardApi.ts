import { useCallback, useMemo } from 'react';
import type { RemoteStewardOpenResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export const REMOTE_STEWARD_OPEN_METHOD = 'steward.open';
export const REMOTE_STEWARD_ANSWER_METHOD = 'steward.decisions.answer';

export function stewardActionErrorText(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

export function useRemoteStewardApi() {
  const { call, supportsMethod } = useRemoteCall();
  const supportsOpen = supportsMethod(REMOTE_STEWARD_OPEN_METHOD);
  const supportsAnswer = supportsMethod(REMOTE_STEWARD_ANSWER_METHOD);
  const answerDecision = useCallback(async (id: string, answer: string): Promise<void> => {
    await call(REMOTE_STEWARD_ANSWER_METHOD, { id, answer });
  }, [call]);
  const openSteward = useCallback(async (projectID: string): Promise<string> => {
    const result = await call<RemoteStewardOpenResult>(REMOTE_STEWARD_OPEN_METHOD, { projectID });
    return result?.threadID ?? '';
  }, [call]);
  return useMemo(() => ({ supportsOpen, openSteward, supportsAnswer, answerDecision }), [supportsOpen, openSteward, supportsAnswer, answerDecision]);
}
