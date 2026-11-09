import { useCallback, useMemo } from 'react';
import type { RemoteStewardDecisionsListParams, RemoteStewardDecisionsListResult, RemoteStewardOpenResult } from '../types/generated';
import { RpcError } from '../services/rpc';
import type { UseRemoteCallResult } from './useRemoteCall';
import { useRemoteCall } from './useRemoteCall';

export const REMOTE_STEWARD_OPEN_METHOD = 'steward.open';
export const REMOTE_STEWARD_ANSWER_METHOD = 'steward.decisions.answer';
export const REMOTE_STEWARD_DECISIONS_LIST_METHOD = 'steward.decisions.list';

export async function loadOpenDecisionCount(call: UseRemoteCallResult['call']): Promise<number | null> {
  const params: RemoteStewardDecisionsListParams = { status: 'open', limit: 200 };
  try {
    const result = await call<RemoteStewardDecisionsListResult>(REMOTE_STEWARD_DECISIONS_LIST_METHOD, params);
    return typeof result.total === 'number' ? result.total : result.decisions.filter((decision) => decision.status === 'open').length;
  } catch (error) {
    if (error instanceof RpcError && error.code === -32601) return null;
    throw error;
  }
}

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
