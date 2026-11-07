// 子代理停止：确认弹窗、停止动作幂等核对，以及运行中耗时计时。

import { useCallback, useEffect, useState } from 'react';
import { RpcError } from '../../services/rpc';
import { makeActionID } from '../../context/remoteGuards';
import type { UseThreadSwarmResult } from '../../context/remoteSwarm';
import type { RemoteContextType } from '../../context/remoteTypes';

interface UseHelperStopParams {
  swarmState: UseThreadSwarmResult['state'];
  threadID: string;
  isConnected: boolean;
  refetchSwarm: UseThreadSwarmResult['refetch'];
  stopSwarmBee: UseThreadSwarmResult['stopBee'];
  checkThreadAction: RemoteContextType['checkThreadAction'];
}

export function useHelperStop({
  swarmState,
  threadID,
  isConnected,
  refetchSwarm,
  stopSwarmBee,
  checkThreadAction
}: UseHelperStopParams) {
  const [helperNowMs, setHelperNowMs] = useState(() => Date.now());
  useEffect(() => {
    if (swarmState?.bee?.status !== 'running') return;
    const interval = setInterval(() => setHelperNowMs(Date.now()), 1000);
    return () => clearInterval(interval);
  }, [swarmState?.bee?.status]);

  const [helperStopConfirmOpen, setHelperStopConfirmOpen] = useState(false);
  const [helperStopAction, setHelperStopAction] = useState<{
    actionID: string;
    leadThreadID: string;
    beeThreadID: string;
    state: 'submitting' | 'pending' | 'accepted' | 'failed' | 'unknown';
    error?: string;
  } | null>(null);
  const [isCheckingHelperStop, setIsCheckingHelperStop] = useState(false);

  const handleConfirmHelperStop = useCallback(async () => {
    const leadThreadID = swarmState?.leadThreadID;
    const beeThreadID = swarmState?.bee?.threadID || threadID;
    if (!leadThreadID || !beeThreadID || !isConnected) return;

    const actionID = makeActionID();
    setHelperStopAction({
      actionID,
      leadThreadID,
      beeThreadID,
      state: 'submitting'
    });
    setHelperStopConfirmOpen(false);

    try {
      await stopSwarmBee(beeThreadID, leadThreadID, actionID);
      setHelperStopAction({
        actionID,
        leadThreadID,
        beeThreadID,
        state: 'accepted'
      });
      void refetchSwarm();
    } catch (err) {
      if (err instanceof RpcError) {
        setHelperStopAction({
          actionID,
          leadThreadID,
          beeThreadID,
          state: 'failed',
          error: err.message
        });
      } else {
        setHelperStopAction({
          actionID,
          leadThreadID,
          beeThreadID,
          state: 'unknown',
          error: `${err instanceof Error ? err.message : '网络异常'}；停止结果待确认，请核对状态`
        });
      }
    }
  }, [swarmState?.bee?.threadID, swarmState?.leadThreadID, isConnected, refetchSwarm, stopSwarmBee, threadID]);

  const handleCheckHelperStop = useCallback(async () => {
    if (!helperStopAction || isCheckingHelperStop || !isConnected) return;
    setIsCheckingHelperStop(true);
    try {
      const res = await checkThreadAction(helperStopAction.actionID);
      setHelperStopAction((prev) => prev ? {
        ...prev,
        state: res.state,
        error: res.error
      } : null);
      if (res.state === 'accepted') {
        void refetchSwarm();
      }
    } catch (err) {
      setHelperStopAction((prev) => prev ? {
        ...prev,
        state: 'unknown',
        error: `${err instanceof Error ? err.message : '核对状态失败'}；请稍后重试`
      } : null);
    } finally {
      setIsCheckingHelperStop(false);
    }
  }, [checkThreadAction, helperStopAction, isCheckingHelperStop, isConnected, refetchSwarm]);

  return {
    helperNowMs,
    helperStopConfirmOpen,
    setHelperStopConfirmOpen,
    helperStopAction,
    setHelperStopAction,
    isCheckingHelperStop,
    handleConfirmHelperStop,
    handleCheckHelperStop
  };
}
