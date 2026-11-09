import { useCallback, useEffect, useRef, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import { loadOpenDecisionCount, REMOTE_STEWARD_DECISIONS_LIST_METHOD } from '../context/remoteStewardApi';
import { useRemoteCall, useRemoteNotification } from '../context/useRemoteCall';

export function useOpenDecisionCount(): number | null {
  const { connectionState } = useRemote();
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod(REMOTE_STEWARD_DECISIONS_LIST_METHOD);
  const [count, setCount] = useState<number | null>(null);
  const sequence = useRef(0);
  const unavailable = useRef(false);

  const refresh = useCallback(async () => {
    if (connectionState !== 'connected' || !supported || unavailable.current) return;
    const current = ++sequence.current;
    try {
      const next = await loadOpenDecisionCount(call);
      if (current !== sequence.current) return;
      unavailable.current = next === null;
      setCount(next);
    } catch {
      // 暂时读取失败时保留上次的计数，等待下一次通知重试。
    }
  }, [call, connectionState, supported]);

  useEffect(() => {
    unavailable.current = false;
    setCount(null);
    void refresh();
    return () => { sequence.current += 1; };
  }, [refresh]);

  useRemoteNotification('threads.changed', () => void refresh(), supported);
  useRemoteNotification('kanban.taskUpdated', () => void refresh(), supported);
  return count;
}
