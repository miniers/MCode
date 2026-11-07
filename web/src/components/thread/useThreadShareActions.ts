// 分享动作：从回复分支、转发到新会话与存为看板任务。

import { useCallback, useRef, useState } from 'react';
import { RpcError } from '../../services/rpc';
import { makeActionID } from '../../context/remoteGuards';
import type { RemoteContextType } from '../../context/remoteTypes';

interface UseThreadShareActionsParams {
  threadID: string;
  projectID: string;
  threadTitle: string;
  shareActionsBlocked: boolean;
  branchThread: RemoteContextType['branchThread'];
  forwardThread: RemoteContextType['forwardThread'];
  createTask: RemoteContextType['createTask'];
  refreshBoard: RemoteContextType['refreshBoard'];
  refreshThread: RemoteContextType['refreshThread'];
  loadThreads: RemoteContextType['loadThreads'];
}

export function useThreadShareActions({
  threadID,
  projectID,
  threadTitle,
  shareActionsBlocked,
  branchThread,
  forwardThread,
  createTask,
  refreshBoard,
  refreshThread,
  loadThreads
}: UseThreadShareActionsParams) {
  // 分支到新会话确认弹窗状态
  const [branchConfirmMessageID, setBranchConfirmMessageID] = useState<string | null>(null);
  const [isBranching, setIsBranching] = useState(false);
  const [branchError, setBranchError] = useState<string | null>(null);
  // 转发到新会话
  const [forwardConfirmOpen, setForwardConfirmOpen] = useState(false);
  const [isForwarding, setIsForwarding] = useState(false);
  const [forwardError, setForwardError] = useState<string | null>(null);
  const forwardActionRef = useRef<string | null>(null);
  // 存为看板任务
  const [saveTaskOpen, setSaveTaskOpen] = useState(false);
  const [saveTaskTitle, setSaveTaskTitle] = useState('');
  const [saveTaskNotes, setSaveTaskNotes] = useState('');
  const [isSavingTask, setIsSavingTask] = useState(false);
  const [saveTaskError, setSaveTaskError] = useState<string | null>(null);

  const handleConfirmBranch = useCallback(async () => {
    if (!branchConfirmMessageID || isBranching) return;
    setIsBranching(true);
    setBranchError(null);
    try {
      const actionID = makeActionID();
      const res = await branchThread({
        threadID,
        messageID: branchConfirmMessageID,
        actionID
      });
      if (res?.threadID) {
        setBranchConfirmMessageID(null);
        void loadThreads(projectID || null);
        window.location.hash = `#/t/${encodeURIComponent(res.threadID)}`;
      } else {
        throw new Error('Mac 端未返回新会话 ID');
      }
    } catch (err) {
      setBranchError(err instanceof Error ? err.message : '分支到新会话失败');
    } finally {
      setIsBranching(false);
    }
  }, [branchConfirmMessageID, isBranching, branchThread, threadID, projectID, loadThreads]);

  // 转发到新会话：把整段对话上下文带入新会话。同一份确认超时后沿用原 actionID，宿主按 actionID 幂等。
  const handleConfirmForward = useCallback(async () => {
    if (isForwarding || shareActionsBlocked) return;
    setIsForwarding(true);
    setForwardError(null);
    const actionID = forwardActionRef.current ?? makeActionID();
    forwardActionRef.current = actionID;
    try {
      const res = await forwardThread({ threadID, actionID });
      if (!res?.threadID) throw new Error('Mac 端未返回新会话 ID');
      forwardActionRef.current = null;
      setForwardConfirmOpen(false);
      void loadThreads(projectID || null);
      window.location.hash = `#/t/${encodeURIComponent(res.threadID)}`;
    } catch (err) {
      if (err instanceof RpcError) {
        forwardActionRef.current = null;
        setForwardError(err.message || '转发到新会话失败');
      } else {
        setForwardError(
          `${err instanceof Error ? err.message : '网络异常'}；转发结果待确认，再次点击将沿用同一动作确认，不会重复创建。`
        );
      }
    } finally {
      setIsForwarding(false);
    }
  }, [forwardThread, isForwarding, projectID, loadThreads, shareActionsBlocked, threadID]);

  const handleOpenSaveTask = useCallback(() => {
    setSaveTaskTitle(threadTitle || '');
    setSaveTaskNotes('');
    setSaveTaskError(null);
    setSaveTaskOpen(true);
  }, [threadTitle]);

  const handleSubmitSaveTask = useCallback(async () => {
    const title = saveTaskTitle.trim();
    if (!title || isSavingTask || shareActionsBlocked) return;
    setIsSavingTask(true);
    setSaveTaskError(null);
    try {
      await createTask(projectID, title, saveTaskNotes, threadID);
      setSaveTaskOpen(false);
      // 任务与会话已在宿主关联：刷新看板与会话摘要，让关联任务条出现
      void refreshBoard().catch(() => undefined);
      void refreshThread(threadID);
    } catch (err) {
      setSaveTaskError(err instanceof Error ? err.message : '存为看板任务失败');
    } finally {
      setIsSavingTask(false);
    }
  }, [createTask, isSavingTask, projectID, refreshBoard, refreshThread, saveTaskNotes, saveTaskTitle, shareActionsBlocked, threadID]);

  const handleBranchMessage = useCallback((messageID: string) => {
    setBranchError(null);
    setBranchConfirmMessageID(messageID);
  }, []);

  return {
    branchConfirmMessageID,
    setBranchConfirmMessageID,
    isBranching,
    branchError,
    setBranchError,
    handleConfirmBranch,
    forwardConfirmOpen,
    setForwardConfirmOpen,
    isForwarding,
    forwardError,
    setForwardError,
    forwardActionRef,
    handleConfirmForward,
    saveTaskOpen,
    setSaveTaskOpen,
    saveTaskTitle,
    setSaveTaskTitle,
    saveTaskNotes,
    setSaveTaskNotes,
    isSavingTask,
    saveTaskError,
    setSaveTaskError,
    handleOpenSaveTask,
    handleSubmitSaveTask,
    handleBranchMessage
  };
}
