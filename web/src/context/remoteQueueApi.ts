import { useCallback } from 'react';
import type { Dispatch, SetStateAction } from 'react';
import type { McodeRpcClient } from '../services/rpc';
import type {
  RemoteCapabilityDeclaration,
  RemoteFollowUp,
  RemoteRequestsListResult,
  RemoteRequestsRespondParams,
  RemoteRequestsRespondResult,
  RemoteThreadsInterruptParams,
  RemoteThreadsInterruptResult,
  RemoteThreadsQueueInterruptAndSendResult,
  RemoteThreadsQueueListResult,
  RemoteThreadsQueueMutationParams,
  RemoteThreadsQueueMutationResult,
  RemoteThreadsSendMessageResult
} from '../types/contract';
import type {
  RemoteThreadsKillRunningCommandParams,
  RemoteThreadsKillRunningCommandResult,
  RemoteThreadsQueueMoveParams,
  RemoteThreadsTurnRecallResult,
  RuntimeCompactSuggestionParams,
  RuntimeQueueEditParams,
  RuntimeQueueEditResult
} from '../types/generated';
import type { RemoteSwarmState } from '../types/swarm';
import { makeActionID } from './remoteGuards';
import { prepareAttachment, readAsDataURL } from './remoteImages';

interface Ref<T> {
  current: T;
}

export interface SteerThreadQueueParams {
  threadID: string;
  followUp: RemoteFollowUp;
  /** 撤回该队列项，通常传 useRemote().removeThreadQueue。 */
  removeFromQueue: (threadID: string, followUpID: string) => Promise<unknown>;
  /** 走普通发送路径（threads.sendMessage）；运行中时宿主会把它插进当前轮次。 */
  sendMessage: (threadID: string, text: string) => Promise<unknown>;
  /** 发送失败后把文本交回输入框，避免消息丢失。 */
  restoreDraft?: (text: string) => void;
}

/**
 * 「插入到当前轮次」不是单独的 RPC：照桌面端 steerQueued 的组合，先删除队列项再直接发送。
 * 属于尽力而为的两步调用：中途若被其他客户端改动队列，顺序可能变，但文本不会丢。
 * 带附件的队列项后端无法插入当前轮，直接拒绝（桌面端也不渲染插入按钮）。
 */
export async function steerThreadQueue({
  threadID,
  followUp,
  removeFromQueue,
  sendMessage,
  restoreDraft
}: SteerThreadQueueParams): Promise<void> {
  if (followUp.attachments.length > 0) {
    throw new Error('带附件的排队项无法插入当前轮次');
  }
  await removeFromQueue(threadID, followUp.id);
  try {
    await sendMessage(threadID, followUp.text);
  } catch (err) {
    // 队列项已经删掉了，发送又失败：文本退回输入框，宁可让用户重发也不能丢。
    restoreDraft?.(followUp.text);
    throw err;
  }
}

interface UseRemoteQueueApiOptions {
  clientRef: Ref<McodeRpcClient | null>;
  capabilities: RemoteCapabilityDeclaration | null;
  requestSnapshotsRef: Ref<Record<string, RemoteRequestsListResult>>;
  setRequestSnapshots: Dispatch<SetStateAction<Record<string, RemoteRequestsListResult>>>;
  setQueueSnapshots: Dispatch<SetStateAction<Record<string, RemoteThreadsQueueListResult>>>;
}

/**
 * 待处理请求、跟进队列、轮次级动作（撤回本轮 / 压缩上下文 / 转交子代理提问）与图片上传相关的 RPC 调用；
 * 快照写回由调用方传入的 setter 完成。
 */
export function useRemoteQueueApi({
  clientRef,
  capabilities,
  requestSnapshotsRef,
  setRequestSnapshots,
  setQueueSnapshots
}: UseRemoteQueueApiOptions) {
  const listRequests = useCallback(async (threadID: string): Promise<RemoteRequestsListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取待处理请求');
    const result = await client.listRequests({ threadID });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取待处理请求');
    const next = { ...requestSnapshotsRef.current, [threadID]: result };
    requestSnapshotsRef.current = next;
    setRequestSnapshots(next);
    return result;
  }, [clientRef, requestSnapshotsRef, setRequestSnapshots]);

  const respondToRequest = useCallback(async (
    params: RemoteRequestsRespondParams
  ): Promise<RemoteRequestsRespondResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法答复请求');
    return client.respondToRequest(params);
  }, [clientRef]);

  const interruptThread = useCallback(async (
    params: RemoteThreadsInterruptParams
  ): Promise<RemoteThreadsInterruptResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法停止会话');
    return client.interruptThread(params);
  }, [clientRef]);

  const killRunningCommand = useCallback(async (
    params: RemoteThreadsKillRunningCommandParams
  ): Promise<RemoteThreadsKillRunningCommandResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法终止命令');
    return client.killRunningCommand(params);
  }, [clientRef]);

  const listThreadQueue = useCallback(async (threadID: string): Promise<RemoteThreadsQueueListResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法读取跟进队列');
    const result = await client.listThreadQueue(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新读取跟进队列');
    setQueueSnapshots((previous) => {
      const current = previous[threadID];
      return current && current.revision > result.revision ? previous : { ...previous, [threadID]: result };
    });
    return result;
  }, [clientRef, setQueueSnapshots]);

  const updateThreadQueue = useCallback(async (
    params: Omit<RemoteThreadsQueueMutationParams, 'actionID'>
  ): Promise<RemoteThreadsQueueMutationResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法编辑跟进');
    const result = await client.updateThreadQueue({ ...params, actionID: makeActionID() });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认编辑结果');
    setQueueSnapshots((previous) => ({ ...previous, [params.threadID]: result }));
    return result;
  }, [clientRef, setQueueSnapshots]);

  const removeThreadQueue = useCallback(async (
    threadID: string,
    followUpID: string
  ): Promise<RemoteThreadsQueueMutationResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法撤回跟进');
    const result = await client.removeThreadQueue({ threadID, followUpID, actionID: makeActionID() });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认撤回结果');
    setQueueSnapshots((previous) => ({ ...previous, [threadID]: result }));
    return result;
  }, [clientRef, setQueueSnapshots]);

  const sendThreadQueueNow = useCallback(async (
    threadID: string,
    followUpID: string
  ): Promise<RemoteThreadsQueueMutationResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法立即发送跟进');
    const result = await client.sendThreadQueueNow({ threadID, followUpID, actionID: makeActionID() });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认发送结果');
    setQueueSnapshots((previous) => ({ ...previous, [threadID]: result }));
    return result;
  }, [clientRef, setQueueSnapshots]);

  /** 调整跟进队列顺序：beforeFollowUpID 为空表示移到末尾；需要宿主声明 threads.queue.move。 */
  const moveThreadQueue = useCallback(async (
    params: Omit<RemoteThreadsQueueMoveParams, 'actionID'>
  ): Promise<RemoteThreadsQueueMutationResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法调整跟进顺序');
    if (capabilities && !capabilities.methods.includes('threads.queue.move')) {
      throw new Error('当前宿主未声明 threads.queue.move 能力');
    }
    const result = await client.moveThreadQueue({ ...params, actionID: makeActionID() });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认移动结果');
    setQueueSnapshots((previous) => ({ ...previous, [params.threadID]: result }));
    return result;
  }, [capabilities, clientRef, setQueueSnapshots]);

  const interruptAndSendThread = useCallback(async (
    threadID: string,
    text: string,
    attachments: string[] = [],
    actionID: string = makeActionID()
  ): Promise<RemoteThreadsQueueInterruptAndSendResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法停止并发送跟进');
    const result = await client.interruptAndSendThread({
      threadID,
      text,
      attachments,
      actionID
    });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认停止并发送结果');
    setQueueSnapshots((previous) => ({ ...previous, [threadID]: result.queue }));
    return result;
  }, [clientRef, setQueueSnapshots]);

  /** 队列条目编辑租借：begin 借出，save 落盘，cancel 归还原草稿。 */
  const editThreadQueueDraft = useCallback(async (
    params: RuntimeQueueEditParams
  ): Promise<RuntimeQueueEditResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法编辑跟进');
    if (capabilities && !capabilities.methods.includes('threads.queue.edit')) {
      throw new Error('当前宿主未声明 threads.queue.edit 能力');
    }
    const result = await client.editThreadQueueDraft(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认编辑结果');
    return result;
  }, [capabilities, clientRef]);

  /** 撤回本轮：收回最近一轮尚未产生回复的用户提示词。 */
  const recallTurn = useCallback(async (threadID: string): Promise<RemoteThreadsTurnRecallResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法撤回本轮');
    if (capabilities && !capabilities.methods.includes('threads.turn.recall')) {
      throw new Error('当前宿主未声明 threads.turn.recall 能力');
    }
    const result = await client.recallTurn(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认撤回结果');
    return result;
  }, [capabilities, clientRef]);

  /** 压缩上下文：与桌面端 /compact 等价。 */
  const compactThread = useCallback(async (threadID: string): Promise<RemoteThreadsSendMessageResult> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法压缩上下文');
    if (capabilities && !capabilities.methods.includes('threads.compact')) {
      throw new Error('当前宿主未声明 threads.compact 能力');
    }
    const result = await client.compactThread(threadID);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认压缩结果');
    return result;
  }, [capabilities, clientRef]);

  /** 把子代理的提问转交主线程回答。 */
  const forwardSwarmQuestion = useCallback(async (params: {
    threadID: string;
    beeThreadID: string;
    requestID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法转交子代理提问');
    if (capabilities && !capabilities.methods.includes('threads.swarm.forwardQuestion')) {
      throw new Error('当前宿主未声明 threads.swarm.forwardQuestion 能力');
    }
    const result = await client.forwardSwarmQuestion(params);
    if (clientRef.current !== client) throw new Error('连接已更换，请重新确认转交结果');
    return result;
  }, [capabilities, clientRef]);

  /** 订阅压缩建议通知；连接实例重建后需重新订阅。 */
  const subscribeCompactSuggestion = useCallback(
    (listener: (offer: RuntimeCompactSuggestionParams) => void): (() => void) => {
      const client = clientRef.current;
      if (!client) return () => {};
      return client.addCompactSuggestionListener(listener);
    },
    [clientRef]
  );

  const uploadAttachment = useCallback(async (file: File) => {
    const client = clientRef.current;
    if (!client) throw new Error('未连接到 Mac 端，无法上传附件');
    if (capabilities && (
      !capabilities.methods.includes('attachments.upload') ||
      !capabilities.features?.includes('threads.attachments')
    )) {
      throw new Error('当前宿主未声明附件上传能力');
    }
    // 图片压成 JPEG；其它文件原样上传，MIME 由宿主按扩展名和内容嗅探
    const prepared = await prepareAttachment(file);
    const dataURL = await readAsDataURL(prepared);
    const separator = dataURL.indexOf(',');
    if (separator < 0) throw new Error('附件编码无效');
    const result = await client.uploadAttachment({
      name: prepared.name,
      mimeType: prepared.type || 'application/octet-stream',
      data: dataURL.slice(separator + 1)
    });
    if (clientRef.current !== client) throw new Error('连接已更换，请重新上传附件');
    return result;
  }, [capabilities, clientRef]);

  return {
    listRequests,
    respondToRequest,
    interruptThread,
    killRunningCommand,
    listThreadQueue,
    updateThreadQueue,
    removeThreadQueue,
    sendThreadQueueNow,
    moveThreadQueue,
    interruptAndSendThread,
    editThreadQueueDraft,
    recallTurn,
    compactThread,
    forwardSwarmQuestion,
    subscribeCompactSuggestion,
    uploadAttachment
  };
}
