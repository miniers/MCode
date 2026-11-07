// 会话元数据动作：重命名、重新生成标题、置顶、休眠、归档/取消归档与编排开关。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import type { RemoteThreadSummary } from '../../types/contract';
import type { RemoteContextType } from '../../context/remoteTypes';
import { useRemoteStatic } from '../../context/RemoteContext';

/** 会话级 Swarm 工作模式，取值与宿主 threads.swarm.setMode 一致。 */
export type ThreadSwarmMode = NonNullable<RemoteThreadSummary['swarmMode']>;

/** 宿主能力清单是否声明了某方法；清单尚未到位时按可用处理，与 ThreadView 的既有判断一致。 */
export function useRemoteMethodSupport(): (method: string) => boolean {
  const { capabilities } = useRemoteStatic();
  return useCallback(
    (method: string) => (capabilities ? capabilities.methods.includes(method) : true),
    [capabilities]
  );
}

export interface UseRegenerateThreadTitleResult {
  isRegeneratingTitle: boolean;
  regenerateTitleError: string | null;
  /** 成功返回新标题，失败或不可用时返回 null（错误写在 regenerateTitleError 里）。 */
  regenerateTitle: () => Promise<string | null>;
}

/**
 * 重新生成会话标题：成功后通过 onUpdated 把新标题写回调用方持有的摘要，
 * 会话列表与订阅推送会再由宿主同步覆盖。
 */
export function useRegenerateThreadTitle(params: {
  threadID: string;
  onUpdated?: (title: string) => void;
}): UseRegenerateThreadTitleResult {
  const { threadID, onUpdated } = params;
  const { regenerateThreadTitle: regenerate } = useRemoteStatic();
  const [isRegeneratingTitle, setIsRegeneratingTitle] = useState(false);
  const [regenerateTitleError, setRegenerateTitleError] = useState<string | null>(null);

  // onUpdated 常以内联函数传入，用 ref 保存避免让 regenerateTitle 每次渲染都换新引用
  const onUpdatedRef = useRef(onUpdated);
  onUpdatedRef.current = onUpdated;

  const regenerateTitle = useCallback(async (): Promise<string | null> => {
    if (isRegeneratingTitle) return null;
    setIsRegeneratingTitle(true);
    setRegenerateTitleError(null);
    try {
      const result = await regenerate(threadID);
      const title = result.title?.trim();
      if (!title) {
        setRegenerateTitleError('宿主没有返回新的标题');
        return null;
      }
      onUpdatedRef.current?.(title);
      return title;
    } catch (err) {
      setRegenerateTitleError(err instanceof Error ? err.message : '重新生成标题失败');
      return null;
    } finally {
      setIsRegeneratingTitle(false);
    }
  }, [isRegeneratingTitle, regenerate, threadID]);

  return { isRegeneratingTitle, regenerateTitleError, regenerateTitle };
}

interface UseThreadMetaActionsParams {
  latestSummary: RemoteThreadSummary;
  setLatestSummary: React.Dispatch<React.SetStateAction<RemoteThreadSummary>>;
  updateThread: RemoteContextType['updateThread'];
  refreshThread: RemoteContextType['refreshThread'];
  menuSheetOpen: boolean;
  setMenuSheetOpen: React.Dispatch<React.SetStateAction<boolean>>;
  swarmAvailable: boolean;
  setSwarmEnabled: (enabled: boolean) => Promise<void>;
  /** 编排开关真正切换成功后的回调：调用方在这里把结果同步进宿主记忆。 */
  onSwarmToggled?: (enabled: boolean) => void;
}

export function useThreadMetaActions({
  latestSummary,
  setLatestSummary,
  updateThread,
  refreshThread,
  menuSheetOpen,
  setMenuSheetOpen,
  swarmAvailable,
  setSwarmEnabled,
  onSwarmToggled
}: UseThreadMetaActionsParams) {
  const [isRenaming, setIsRenaming] = useState(false);
  const [renameDraft, setRenameDraft] = useState('');
  const [isUpdatingThread, setIsUpdatingThread] = useState(false);
  const [unarchiveError, setUnarchiveError] = useState<string | null>(null);
  // 编排切换中与行内错误
  const [isTogglingSwarm, setIsTogglingSwarm] = useState(false);
  const [swarmInlineError, setSwarmInlineError] = useState<string | null>(null);

  // 重新生成标题：成功后直接把新标题合并进本地摘要，与 handleRename 的写法一致
  const { isRegeneratingTitle, regenerateTitleError, regenerateTitle } = useRegenerateThreadTitle({
    threadID: latestSummary.id,
    onUpdated: (title) => setLatestSummary((prev) => ({ ...prev, title }))
  });

  const handleTogglePin = useCallback(async () => {
    if (isUpdatingThread) return;
    setIsUpdatingThread(true);
    try {
      const updated = await updateThread(latestSummary.id, { pinned: !latestSummary.isPinned });
      if (updated) setLatestSummary((prev) => ({ ...prev, ...updated, isPinned: !prev.isPinned }));
      setMenuSheetOpen(false);
    } catch {
      // ignore
    } finally {
      setIsUpdatingThread(false);
    }
  }, [isUpdatingThread, latestSummary.id, latestSummary.isPinned, updateThread]);

  const handleToggleDormant = useCallback(async () => {
    if (isUpdatingThread) return;
    setIsUpdatingThread(true);
    try {
      const updated = await updateThread(latestSummary.id, { dormant: !latestSummary.isDormant });
      if (updated) setLatestSummary((prev) => ({ ...prev, ...updated, isDormant: !prev.isDormant }));
      setMenuSheetOpen(false);
    } catch {
      // ignore
    } finally {
      setIsUpdatingThread(false);
    }
  }, [isUpdatingThread, latestSummary.id, latestSummary.isDormant, updateThread]);

  const handleArchive = useCallback(async () => {
    if (isUpdatingThread) return;
    setIsUpdatingThread(true);
    try {
      const updated = await updateThread(latestSummary.id, { archived: true });
      if (updated) setLatestSummary((prev) => ({ ...prev, ...updated, isArchived: true }));
      setMenuSheetOpen(false);
    } catch {
      // ignore
    } finally {
      setIsUpdatingThread(false);
    }
  }, [isUpdatingThread, latestSummary.id, updateThread]);

  useEffect(() => {
    if (!menuSheetOpen) setUnarchiveError(null);
  }, [menuSheetOpen]);

  const handleUnarchive = useCallback(async () => {
    if (isUpdatingThread) return;
    setIsUpdatingThread(true);
    setUnarchiveError(null);
    try {
      const updated = await updateThread(latestSummary.id, { archived: false });
      if (updated) setLatestSummary((prev) => ({ ...prev, ...updated, isArchived: false }));
      setMenuSheetOpen(false);
      void refreshThread(latestSummary.id);
    } catch (error) {
      setUnarchiveError(error instanceof Error ? error.message : '取消归档失败');
    } finally {
      setIsUpdatingThread(false);
    }
  }, [isUpdatingThread, latestSummary.id, refreshThread, updateThread]);

  const handleRename = useCallback(async () => {
    const trimmed = renameDraft.trim();
    if (!trimmed || isUpdatingThread) return;
    setIsUpdatingThread(true);
    try {
      const updated = await updateThread(latestSummary.id, { title: trimmed });
      if (updated) setLatestSummary((prev) => ({ ...prev, ...updated, title: trimmed }));
      setIsRenaming(false);
      setMenuSheetOpen(false);
    } catch {
      // ignore
    } finally {
      setIsUpdatingThread(false);
    }
  }, [isUpdatingThread, latestSummary.id, renameDraft, updateThread]);

  const handleToggleSwarm = useCallback(async (nextEnabled: boolean) => {
    if (isTogglingSwarm || !swarmAvailable) return;
    setIsTogglingSwarm(true);
    setSwarmInlineError(null);
    try {
      await setSwarmEnabled(nextEnabled);
      onSwarmToggled?.(nextEnabled);
    } catch (err) {
      setSwarmInlineError(err instanceof Error ? err.message : '切换编排失败');
    } finally {
      setIsTogglingSwarm(false);
    }
  }, [onSwarmToggled, swarmAvailable, isTogglingSwarm, setSwarmEnabled]);

  return {
    isRenaming,
    setIsRenaming,
    renameDraft,
    setRenameDraft,
    isUpdatingThread,
    unarchiveError,
    isTogglingSwarm,
    swarmInlineError,
    isRegeneratingTitle,
    regenerateTitleError,
    regenerateTitle,
    handleTogglePin,
    handleToggleDormant,
    handleArchive,
    handleUnarchive,
    handleRename,
    handleToggleSwarm
  };
}
