import { useCallback, useEffect, useRef, useState } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import { RpcError } from '../services/rpc';
import type {
  ConnectionState,
  RemoteCapabilityDeclaration,
  RemoteComposerOptions,
  RemoteProjectSummary,
  RemoteThreadSummary,
  RemoteThreadsSendMessageParams
} from '../types/contract';
import {
  isUnresolvedThreadAction,
  makeActionID
} from './remoteGuards';
import type { RemoteThreadAction } from './remoteTypes';

interface Ref<T> {
  current: T;
}
type RefreshThread = (threadID: string) => Promise<RemoteThreadSummary | null>;
type RefreshThreadsForProject = (
  projectID: string,
  openThreadID?: string,
  forceOpen?: boolean
) => Promise<RemoteThreadSummary | null>;

interface UseRemoteThreadActionsOptions {
  clientRef: Ref<McodeRpcClient | null>;
  connectionState: ConnectionState;
  connectionStateRef: Ref<ConnectionState>;
  isSyncingRef: Ref<boolean>;
  capabilities: RemoteCapabilityDeclaration | null;
  projectsRef: Ref<RemoteProjectSummary[]>;
  threadsRef: Ref<RemoteThreadSummary[]>;
  currentThreadRef: Ref<RemoteThreadSummary | null>;
  threadsRequestRef: Ref<number>;
  refreshThreadRef: Ref<RefreshThread>;
  refreshThreadsForProjectRef: Ref<RefreshThreadsForProject>;
}

export function useRemoteThreadActions({
  clientRef,
  connectionState,
  connectionStateRef,
  isSyncingRef,
  capabilities,
  projectsRef,
  threadsRef,
  currentThreadRef,
  threadsRequestRef,
  refreshThreadRef,
  refreshThreadsForProjectRef
}: UseRemoteThreadActionsOptions) {
  const [threadActions, setThreadActions] = useState<RemoteThreadAction[]>([]);
  const threadActionsRef = useRef<RemoteThreadAction[]>([]);

  const updateThreadActions = useCallback(
    (updater: (actions: RemoteThreadAction[]) => RemoteThreadAction[]) => {
      const next = updater(threadActionsRef.current);
      threadActionsRef.current = next;
      setThreadActions(next);
    },
    []
  );

  const replaceThreadAction = useCallback(
    (actionID: string, patch: Partial<RemoteThreadAction>): RemoteThreadAction | null => {
      let updated: RemoteThreadAction | null = null;
      updateThreadActions((actions) => actions.map((action) => {
        if (action.actionID !== actionID) return action;
        const isActionDormant = action.state === 'accepted' || (action.state === 'failed' && action.safeToRecoverDraft !== undefined);
        if (isActionDormant && patch.state && patch.state !== action.state) {
          updated = action;
          return action;
        }
        const nextAction = { ...action, ...patch };
        updated = nextAction;
        return nextAction;
      }));
      return updated;
    },
    [updateThreadActions]
  );

  const reset = useCallback(() => {
    threadActionsRef.current = [];
    setThreadActions([]);
  }, []);

  const requireThreadWriteClient = useCallback(
    (method: 'threads.create' | 'threads.sendMessage'): McodeRpcClient => {
      const client = clientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，操作已禁用');
      }
      if (isSyncingRef.current) {
        throw new Error('正在同步最新看板状态，操作已禁用，请稍候重试');
      }
      if (!capabilities || !capabilities.methods.includes(method)) {
        throw new Error(`当前宿主未声明 ${method} 能力，操作已禁用`);
      }
      if (!capabilities.methods.includes('threads.actionStatus')) {
        throw new Error('当前宿主无法查询动作结果，操作已禁用');
      }
      return client;
    },
    [capabilities, clientRef, connectionStateRef, isSyncingRef]
  );

  const createThread = useCallback(
    async (projectID: string, options?: RemoteComposerOptions): Promise<RemoteThreadAction> => {
      const existing = threadActionsRef.current.find(
        (action) =>
          action.kind === 'create' &&
          action.projectID === projectID &&
          isUnresolvedThreadAction(action)
      );
      if (existing) return existing;

      const client = requireThreadWriteClient('threads.create');
      if (!projectsRef.current.some((project) => project.id === projectID)) {
        throw new Error('目标项目未加载或不可访问');
      }
      const navigationRequestID = threadsRequestRef.current;

      const actionID = makeActionID();
      const actionJournalID = capabilities?.methods.includes('threads.settleAction')
        ? capabilities.actionJournalID
        : undefined;
      const action: RemoteThreadAction = {
        actionID,
        actionJournalID,
        kind: 'create',
        projectID,
        state: 'submitting',
        safeToRecoverDraft: false
      };
      updateThreadActions((actions) => [...actions, action]);
      replaceThreadAction(actionID, { state: 'pending' });

      try {
        const result = await client.createThread({
          projectID,
          actionID,
          ...(options === undefined ? {} : { options }),
          ...(actionJournalID ? { actionJournalID } : {})
        });
        if (clientRef.current !== client) return action;
        const accepted = replaceThreadAction(actionID, {
          state: 'accepted',
          threadID: result.threadID,
          safeToRecoverDraft: false,
          error: undefined
        });
        if (navigationRequestID === threadsRequestRef.current) {
          await refreshThreadsForProjectRef.current(projectID, result.threadID, true);
        }
        return accepted ?? { ...action, state: 'accepted', threadID: result.threadID, safeToRecoverDraft: false };
      } catch (err) {
        if (clientRef.current !== client) return action;
        if (err instanceof RpcError && !action.actionJournalID) {
          const failed: RemoteThreadAction = {
            ...action,
            state: 'failed',
            safeToRecoverDraft: true,
            error: err.message
          };
          return replaceThreadAction(actionID, failed) ?? failed;
        }
        const unknown: RemoteThreadAction = {
          ...action,
          state: 'unknown',
          safeToRecoverDraft: false,
          error: `${err instanceof Error ? err.message : '网络异常'}；操作状态未确认，请查询结果，不会自动重放`
        };
        return replaceThreadAction(actionID, unknown) ?? unknown;
      }
    },
    [capabilities, clientRef, projectsRef, replaceThreadAction, requireThreadWriteClient, threadsRequestRef, updateThreadActions]
  );

  const sendThreadMessage = useCallback(
    async (
      threadID: string,
      text: string,
      params: Pick<RemoteThreadsSendMessageParams, 'whenRunning' | 'options' | 'attachments'> = {}
    ): Promise<RemoteThreadAction> => {
      const existing = threadActionsRef.current.find(
        (action) =>
          action.kind === 'send' &&
          action.threadID === threadID &&
          isUnresolvedThreadAction(action)
      );
      if (existing) return existing;
      if (text.trim().length === 0 && !(params.attachments && params.attachments.length > 0)) {
        throw new Error('消息内容不能为空');
      }

      const client = requireThreadWriteClient('threads.sendMessage');
      const current = currentThreadRef.current;
      if (
        !current ||
        current.id !== threadID ||
        !threadsRef.current.some((thread) => thread.id === threadID) ||
        !projectsRef.current.some((project) => project.id === current.projectID)
      ) {
        throw new Error('只能向当前已加载且可访问的会话发送消息');
      }

      const actionID = makeActionID();
      const actionJournalID = capabilities?.methods.includes('threads.settleAction')
        ? capabilities.actionJournalID
        : undefined;
      const action: RemoteThreadAction = {
        actionID,
        actionJournalID,
        kind: 'send',
        projectID: current.projectID,
        threadID,
        text,
        state: 'submitting',
        safeToRecoverDraft: false
      };
      updateThreadActions((actions) => [...actions, action]);
      replaceThreadAction(actionID, { state: 'pending' });

      try {
        const result = await client.sendThreadMessage({
          threadID,
          text,
          actionID,
          ...params,
          ...(actionJournalID ? { actionJournalID } : {})
        });
        if (clientRef.current !== client) return action;
        const accepted = replaceThreadAction(actionID, {
          state: 'accepted',
          threadID: result.threadID,
          messageID: result.messageID,
          turnID: result.turnID,
          queued: result.queued,
          followUpID: result.followUpID,
          localCommand: result.localCommand,
          delegatedThreadID: result.delegatedThreadID,
          safeToRecoverDraft: false,
          error: undefined
        });
        void refreshThreadRef.current(result.threadID);
        return accepted ?? {
          ...action,
          state: 'accepted',
          threadID: result.threadID,
          messageID: result.messageID,
          turnID: result.turnID,
          queued: result.queued,
          followUpID: result.followUpID,
          localCommand: result.localCommand,
          delegatedThreadID: result.delegatedThreadID,
          safeToRecoverDraft: false
        };
      } catch (err) {
        if (clientRef.current !== client) return action;
        if (err instanceof RpcError && !action.actionJournalID) {
          const failed: RemoteThreadAction = {
            ...action,
            state: 'failed',
            safeToRecoverDraft: true,
            error: err.message
          };
          return replaceThreadAction(actionID, failed) ?? failed;
        }
        const unknown: RemoteThreadAction = {
          ...action,
          state: 'unknown',
          safeToRecoverDraft: false,
          error: `${err instanceof Error ? err.message : '网络异常'}；操作状态未确认，请查询结果，不会自动重放`
        };
        return replaceThreadAction(actionID, unknown) ?? unknown;
      }
    },
    [capabilities, clientRef, currentThreadRef, projectsRef, replaceThreadAction, requireThreadWriteClient, threadsRef, updateThreadActions]
  );

  const checkThreadAction = useCallback(
    async (actionID: string): Promise<RemoteThreadAction> => {
      const existing = threadActionsRef.current.find((action) => action.actionID === actionID);
      if (!existing) {
        throw new Error('未找到本地远程动作记录');
      }
      if (existing.state === 'accepted' || existing.state === 'failed') return existing;
      const client = clientRef.current;
      if (!client || !client.isReady || connectionStateRef.current !== 'connected') {
        throw new Error('未与 Mac 桌面端建立有效连接，无法查询动作状态');
      }
      if (!capabilities || !capabilities.methods.includes('threads.actionStatus')) {
        throw new Error('当前宿主未声明 threads.actionStatus 能力，无法查询动作状态');
      }
      const navigationRequestID = threadsRequestRef.current;

      const canSettle = Boolean(
        capabilities.methods.includes('threads.settleAction') &&
        existing.actionJournalID &&
        existing.actionJournalID === capabilities.actionJournalID
      );

      try {
        if (canSettle && existing.actionJournalID) {
          const result = await client.settleThreadAction({
            actionID,
            actionJournalID: existing.actionJournalID,
            projectID: existing.projectID,
            threadID: existing.threadID
          });
          if (clientRef.current !== client) return existing;
          const stateError = result.error?.message ??
            (result.state === 'unknown'
              ? '操作状态未知，请在 Mac 端核对'
              : result.state === 'failed'
              ? '远程动作已被结算取消或执行失败'
              : undefined);
          const updated = replaceThreadAction(actionID, {
            state: result.state,
            safeToRecoverDraft: result.safeToRecoverDraft,
            threadID: result.threadID ?? existing.threadID,
            messageID: result.messageID ?? existing.messageID,
            turnID: result.turnID ?? existing.turnID,
            followUpID: result.followUpID ?? existing.followUpID,
            error: stateError
          });
          const next = updated ?? {
            ...existing,
            state: result.state,
            safeToRecoverDraft: result.safeToRecoverDraft,
            error: stateError
          };
          if (next.state === 'accepted' && next.kind === 'create' && next.threadID &&
              navigationRequestID === threadsRequestRef.current && !currentThreadRef.current) {
            await refreshThreadsForProjectRef.current(next.projectID, next.threadID, true);
          } else if (next.state === 'accepted' && next.kind === 'send' && next.threadID) {
            void refreshThreadRef.current(next.threadID);
          }
          return next;
        }

        const result = await client.getThreadActionStatus(actionID);
        if (clientRef.current !== client) return existing;
        const stateError = result.error?.message ??
          (result.state === 'unknown'
            ? '操作状态未知，请在 Mac 端核对'
            : result.state === 'failed'
            ? '远程动作执行失败'
            : undefined);
        const updated = replaceThreadAction(actionID, {
          state: result.state,
          safeToRecoverDraft: result.state === 'failed',
          threadID: result.threadID ?? existing.threadID,
          messageID: result.messageID ?? existing.messageID,
          turnID: result.turnID ?? existing.turnID,
          followUpID: result.followUpID ?? existing.followUpID,
          error: stateError
        });
        const next = updated ?? { ...existing, state: result.state, safeToRecoverDraft: result.state === 'failed', error: stateError };
        if (next.state === 'accepted' && next.kind === 'create' && next.threadID &&
            navigationRequestID === threadsRequestRef.current && !currentThreadRef.current) {
          await refreshThreadsForProjectRef.current(next.projectID, next.threadID, true);
        } else if (next.state === 'accepted' && next.kind === 'send' && next.threadID) {
          void refreshThreadRef.current(next.threadID);
        }
        return next;
      } catch (err) {
        if (clientRef.current !== client) return existing;
        const unknown: RemoteThreadAction = {
          ...existing,
          state: 'unknown',
          safeToRecoverDraft: false,
          error: `${err instanceof Error ? err.message : '网络异常'}；请在 Mac 端核对操作状态`
        };
        return replaceThreadAction(actionID, unknown) ?? unknown;
      }
    },
    [capabilities, clientRef, connectionStateRef, currentThreadRef, refreshThreadRef, refreshThreadsForProjectRef, replaceThreadAction, threadsRequestRef]
  );

  const dismissThreadAction = useCallback(
    (actionID: string): void => {
      updateThreadActions((actions) => actions.filter(
        (action) =>
          action.actionID !== actionID ||
          (action.state !== 'accepted' && action.state !== 'failed')
      ));
    },
    [updateThreadActions]
  );

  useEffect(() => {
    if (connectionState !== 'connected' || !capabilities?.methods.includes('threads.actionStatus')) return;
    for (const action of threadActionsRef.current) {
      if (action.state === 'pending' || action.state === 'unknown') {
        void checkThreadAction(action.actionID).catch(() => {});
      }
    }
  }, [capabilities, checkThreadAction, connectionState]);

  return {
    threadActions,
    reset,
    createThread,
    sendThreadMessage,
    checkThreadAction,
    dismissThreadAction
  };
}
