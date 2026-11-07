import { useCallback } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import { RpcError } from '../services/rpc';
import type {
  ConnectionState,
  GetDiffResult,
  RemoteCapabilityDeclaration,
  ReviewDecision,
  TaskStatus
} from '../types/contract';
import type {
  KanbanDeleteTaskParams,
  KanbanRescueTaskParams,
  KanbanTaskMutationResult
} from '../types/generated';
import { makeActionID } from './remoteGuards';

/** 救援菜单动作：interrupt 中断并重置为待办，nudge 提醒 Agent。 */
export type KanbanRescueAction = KanbanRescueTaskParams['action'];

interface Ref<T> {
  current: T;
}
interface UseRemoteBoardActionsOptions {
  clientRef: Ref<McodeRpcClient | null>;
  connectionStateRef: Ref<ConnectionState>;
  isSyncingRef: Ref<boolean>;
  capabilities: RemoteCapabilityDeclaration | null;
  setActiveActionTaskID: Dispatch<SetStateAction<string | null>>;
  setActionLoadingText: Dispatch<SetStateAction<string | null>>;
}

export function useRemoteBoardActions({
  clientRef,
  connectionStateRef,
  isSyncingRef,
  capabilities,
  setActiveActionTaskID,
  setActionLoadingText
}: UseRemoteBoardActionsOptions) {
  const runAction = useCallback(
    async <T,>(resourceID: string, loadingText: string, operation: () => Promise<T>): Promise<T> => {
      const client = clientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，操作已禁用');
      }
      if (isSyncingRef.current) {
        throw new Error('正在同步最新看板状态，操作已禁用，请稍候重试');
      }
      setActiveActionTaskID(resourceID);
      setActionLoadingText(loadingText);
      try {
        return await operation();
      } catch (error) {
        if (error instanceof RpcError && error.code === -32009) {
          await client.refreshBoard().catch(() => undefined);
        }
        throw error;
      } finally {
        setActiveActionTaskID(null);
        setActionLoadingText(null);
      }
    },
    [clientRef, connectionStateRef, isSyncingRef, setActionLoadingText, setActiveActionTaskID]
  );

  const requireFeature = useCallback((feature: string) => {
    if (capabilities && !capabilities.features.includes(feature)) {
      throw new Error('当前 Mac 未声明此看板能力');
    }
  }, [capabilities]);

  const moveTask = useCallback(
    async (taskID: string, status: TaskStatus, expectedRevision?: number) => {
      await runAction(taskID, '正在移动任务...', () => clientRef.current!.moveTask({
        taskID,
        status,
        expectedRevision,
        actionID: makeActionID()
      }));
    },
    [clientRef, runAction]
  );

  const startTask = useCallback(
    async (taskID: string, expectedRevision?: number) => {
      await runAction(taskID, '正在向 Mac 发起启动请求...', () => clientRef.current!.startTask({
        taskID,
        expectedRevision,
        actionID: makeActionID()
      }));
    },
    [clientRef, runAction]
  );

  const reviewTask = useCallback(
    async (taskID: string, decision: ReviewDecision, feedback?: string, expectedRevision?: number) => {
      await runAction(
        taskID,
        decision === 'accept' ? '正在验收完成...' : decision === 'reject' ? '正在提交修改建议...' : '正在请求审查...',
        () => clientRef.current!.reviewTask({ taskID, decision, feedback, expectedRevision, actionID: makeActionID() })
      );
    },
    [clientRef, runAction]
  );

  const getDiff = useCallback(
    async (taskID: string): Promise<GetDiffResult> => {
      const client = clientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，无法获取代码变更');
      }
      return client.getDiff({ taskID });
    },
    [clientRef, connectionStateRef]
  );

  // threadID：把新任务关联到该会话（存为看板任务）；旧宿主不认该键，必须先有 kanban.createTaskFromThread。
  const createTask = useCallback(async (projectID: string, title: string, notes?: string, threadID?: string) => {
    requireFeature('kanban.linking');
    if (threadID && !capabilities?.features.includes('kanban.createTaskFromThread')) {
      throw new Error('当前 Mac 未声明“存为看板任务”能力');
    }
    await runAction(projectID, '正在创建任务...', () => clientRef.current!.createTask({
      projectID,
      title,
      notes: notes?.trim() || null,
      ...(threadID ? { threadID } : {}),
      actionID: makeActionID()
    }));
  }, [capabilities, clientRef, requireFeature, runAction]);

  const createGoal = useCallback(async (projectID: string, prompt: string) => {
    requireFeature('kanban.goals');
    await runAction(projectID, '正在创建目标...', () => clientRef.current!.createGoal({
      projectID,
      prompt,
      actionID: makeActionID()
    }));
  }, [clientRef, requireFeature, runAction]);

  const mutateGoal = useCallback(async (
    goalID: string,
    expectedRevision: number,
    method: 'goals.start' | 'goals.pause' | 'goals.cancel',
    loadingText: string
  ) => {
    requireFeature('kanban.goals');
    await runAction(goalID, loadingText, () => clientRef.current!.mutateGoal(method, {
      goalID,
      expectedRevision,
      actionID: makeActionID()
    }));
  }, [clientRef, requireFeature, runAction]);

  const startGoal = useCallback((goalID: string, expectedRevision: number) =>
    mutateGoal(goalID, expectedRevision, 'goals.start', '正在启动目标...'), [mutateGoal]);
  const pauseGoal = useCallback((goalID: string, expectedRevision: number) =>
    mutateGoal(goalID, expectedRevision, 'goals.pause', '正在暂停目标...'), [mutateGoal]);
  const cancelGoal = useCallback((goalID: string, expectedRevision: number) =>
    mutateGoal(goalID, expectedRevision, 'goals.cancel', '正在取消目标...'), [mutateGoal]);

  const replanGoal = useCallback(async (goalID: string, feedback: string, expectedRevision: number) => {
    requireFeature('kanban.goals');
    await runAction(goalID, '正在重新规划目标...', () => clientRef.current!.replanGoal({
      goalID,
      feedback,
      expectedRevision,
      actionID: makeActionID()
    }));
  }, [clientRef, requireFeature, runAction]);

  const mutateGoalTask = useCallback(async (
    taskID: string,
    expectedRevision: number,
    method: 'goals.retryTask' | 'goals.skipTask',
    loadingText: string
  ) => {
    requireFeature('kanban.goals');
    await runAction(taskID, loadingText, () => clientRef.current!.mutateGoalTask(method, {
      taskID,
      expectedRevision,
      actionID: makeActionID()
    }));
  }, [clientRef, requireFeature, runAction]);

  const retryGoalTask = useCallback((taskID: string, expectedRevision: number) =>
    mutateGoalTask(taskID, expectedRevision, 'goals.retryTask', '正在重试任务...'), [mutateGoalTask]);
  const skipGoalTask = useCallback((taskID: string, expectedRevision: number) =>
    mutateGoalTask(taskID, expectedRevision, 'goals.skipTask', '正在跳过任务...'), [mutateGoalTask]);

  /** 删除重试也传同一个 actionID：宿主按 actionID 做幂等，重放不会重复删除。 */
  const deleteTask = useCallback(async (
    taskID: string,
    expectedRevision: number
  ): Promise<KanbanTaskMutationResult> => {
    return runAction(taskID, '正在删除任务...', () => {
      const params: KanbanDeleteTaskParams = { taskID, expectedRevision, actionID: makeActionID() };
      return clientRef.current!.call<KanbanTaskMutationResult>('kanban.deleteTask', params);
    });
  }, [clientRef, runAction]);

  const rescueTask = useCallback(async (
    taskID: string,
    action: KanbanRescueAction,
    expectedRevision: number
  ): Promise<KanbanTaskMutationResult> => {
    const loadingText = action === 'interrupt' ? '正在中断任务...' : '正在提醒 Agent...';
    return runAction(taskID, loadingText, () => {
      const params: KanbanRescueTaskParams = { taskID, action, expectedRevision, actionID: makeActionID() };
      return clientRef.current!.call<KanbanTaskMutationResult>('kanban.rescueTask', params);
    });
  }, [clientRef, runAction]);

  return {
    moveTask,
    startTask,
    reviewTask,
    getDiff,
    createTask,
    createGoal,
    startGoal,
    pauseGoal,
    cancelGoal,
    replanGoal,
    retryGoalTask,
    skipGoalTask,
    deleteTask,
    rescueTask
  };
}
