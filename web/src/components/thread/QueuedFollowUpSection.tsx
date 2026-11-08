// 跟进队列（输入区上方）：按 bundleID 分组的编辑组 / 撤回组 / 立即发送，以及插入到当前轮次、上移、下移。
// 数据来自宿主的 threads.queue.* 快照与队列变更事件；RPC 由组件自己从 useRemote 取，接线只传少量 props。

import React, { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react';

import { useRemote } from '../../context/RemoteContext';
import { steerThreadQueue } from '../../context/remoteQueueApi';
import type { RemoteFollowUp } from '../../types/contract';
import { queueGroupID } from './threadHelpers';
import { followUpScrollTop } from './queuedFollowUpScroll';

/** 能在轮次执行中安全插话的 provider，取值与桌面端 internal/ui/composer_queue.go 的 steerableProviders 一致。 */
const STEERABLE_PROVIDERS = ['claude', 'codex', 'omp', 'pi'];

/** provider 是否支持把排队消息插进当前轮次；pi 线程的 provider 可能带 "pi:" 端点前缀，归一后再比对。 */
export function providerSupportsSteer(provider: string): boolean {
  const trimmed = provider.trim();
  if (!trimmed) return false;
  const base = trimmed.startsWith('pi:') && trimmed.length > 3 ? 'pi' : trimmed;
  return STEERABLE_PROVIDERS.includes(base);
}

export interface QueuedFollowUpSectionProps {
  threadID: string;
  /** 队列快照里的扁平顺序；分组与上移下移都以它为准。 */
  followUps: ReadonlyArray<RemoteFollowUp>;
  revealRequest?: { followUpID: string; nonce: number } | null;
  /** 会话是否正在运行：只有运行中才提供「插入到当前轮次」。 */
  isRunning: boolean;
  /** 会话 provider，用于判断是否支持中途插话。 */
  provider: string;
  /** 输入框当前草稿：编辑组借出（threads.queue.edit begin）时寄存给宿主。 */
  composerDraft: { text: string; attachmentIDs: string[] };
  /** 插话发送失败时把文本还回输入框，避免消息丢失。 */
  onRestoreDraft?: (text: string) => void;
}

export const QueuedFollowUpSection: React.FC<QueuedFollowUpSectionProps> = ({
  threadID,
  followUps,
  revealRequest,
  isRunning,
  provider,
  composerDraft,
  onRestoreDraft
}) => {
  const {
    capabilities,
    connectionState,
    listThreadQueue,
    updateThreadQueue,
    removeThreadQueue,
    sendThreadQueueNow,
    moveThreadQueue,
    editThreadQueueDraft,
    refreshThread,
    sendThreadMessage
  } = useRemote();
  const isConnected = connectionState === 'connected';

  const [queueError, setQueueError] = useState<string | null>(null);
  // 进行中的动作标识：send:/steer:/move:/edit:/edit-begin:/edit-cancel:/remove-group:，同一时刻只允许一个
  const [actionKey, setActionKey] = useState<string | null>(null);
  const [editDrafts, setEditDrafts] = useState<Record<string, string>>({});
  const [editingGroupID, setEditingGroupID] = useState<string | null>(null);
  // 已向宿主借出（begin）的队列条目：保存/取消要交给同一套租借协议收尾
  const [borrowedID, setBorrowedID] = useState<string | null>(null);

  const queueSupported = Boolean(
    capabilities?.features?.includes('threads.queue') &&
      capabilities?.methods.includes('threads.queue.list')
  );
  // 队列条目编辑租借（begin/save/cancel）；不声明时沿用 threads.queue.update
  const queueEditSupported = Boolean(capabilities?.methods.includes('threads.queue.edit'));
  const moveSupported = Boolean(capabilities?.methods.includes('threads.queue.move'));
  const scrollRef = useRef<HTMLElement | null>(null);
  const revealedNonceRef = useRef<number | null>(null);

  useLayoutEffect(() => {
    if (!revealRequest || revealedNonceRef.current === revealRequest.nonce) return;
    const container = scrollRef.current;
    if (!container) return;
    const item = Array.from(container.querySelectorAll<HTMLElement>('[data-follow-up-id]'))
      .find((node) => node.dataset.followUpId === revealRequest.followUpID);
    if (!item) return;
    const rect = item.getBoundingClientRect();
    const top = followUpScrollTop(container, {
      offsetTop: rect.top - container.getBoundingClientRect().top + container.scrollTop,
      height: rect.height
    });
    if (top !== null) container.scrollTop = top;
    revealedNonceRef.current = revealRequest.nonce;
  }, [revealRequest, followUps, queueSupported]);

  const groups = useMemo(() => {
    const grouped = new Map<string, RemoteFollowUp[]>();
    for (const followUp of followUps) {
      const key = queueGroupID(followUp);
      grouped.set(key, [...(grouped.get(key) ?? []), followUp]);
    }
    return [...grouped.entries()];
  }, [followUps]);

  // 扁平顺序索引：第一项不显示上移、最后一项不显示下移，上移下移的目标位置也按它算
  const orderIndex = useMemo(
    () => new Map(followUps.map((followUp, index) => [followUp.id, index] as const)),
    [followUps]
  );

  const handleSendNow = useCallback(async (followUpID: string) => {
    if (actionKey || !isConnected) return;
    setActionKey(`send:${followUpID}`);
    setQueueError(null);
    try {
      await sendThreadQueueNow(threadID, followUpID);
      void refreshThread(threadID);
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '立即发送跟进失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, isConnected, refreshThread, sendThreadQueueNow, threadID]);

  // 插入到当前轮次：先删队列项再普通发送，宿主在运行中会把它注入当前轮次
  const handleSteer = useCallback(async (followUp: RemoteFollowUp) => {
    if (actionKey || !isConnected) return;
    setActionKey(`steer:${followUp.id}`);
    setQueueError(null);
    try {
      await steerThreadQueue({
        threadID,
        followUp,
        removeFromQueue: removeThreadQueue,
        sendMessage: async (id, text) => {
          // sendThreadMessage 不抛 RPC 错误，失败会以 action.state 返回，这里统一转成异常
          const action = await sendThreadMessage(id, text);
          if (action.state !== 'accepted') {
            throw new Error(action.error || '插入失败，消息已退回输入框');
          }
        },
        restoreDraft: onRestoreDraft
      });
      void refreshThread(threadID);
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '插入到当前轮次失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, isConnected, onRestoreDraft, refreshThread, removeThreadQueue, sendThreadMessage, threadID]);

  // 上移 = 放到前一项之前；下移 = 放到后一项之后，即放到再下一项之前，末尾则不带 beforeFollowUpID（移到队尾）
  const handleMove = useCallback(async (followUp: RemoteFollowUp, direction: 'up' | 'down') => {
    if (actionKey || !isConnected || !moveSupported) return;
    const index = orderIndex.get(followUp.id);
    if (index === undefined) return;
    const beforeFollowUpID =
      direction === 'up' ? followUps[index - 1]?.id : followUps[index + 2]?.id;
    if (direction === 'up' && !beforeFollowUpID) return;
    if (direction === 'down' && !followUps[index + 1]) return;
    setActionKey(`move:${followUp.id}`);
    setQueueError(null);
    try {
      // 不本地重排：等这次响应的队列快照回填，避免与服务端顺序打架
      await moveThreadQueue({ threadID, followUpID: followUp.id, beforeFollowUpID });
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '调整跟进顺序失败');
      // 失败后拉一次整队，保证界面与后端一致
      void listThreadQueue(threadID).catch(() => {});
    } finally {
      setActionKey(null);
    }
  }, [actionKey, followUps, isConnected, listThreadQueue, moveSupported, moveThreadQueue, orderIndex, threadID]);

  // 进入编辑：宿主声明 threads.queue.edit 时先 begin 借出第一条，其余组内条目仍就地编辑
  const handleBeginGroupEdit = useCallback(async (group: RemoteFollowUp[]) => {
    if (actionKey || group.length === 0) return;
    const groupID = queueGroupID(group[0]);
    setEditDrafts((previous) => ({
      ...previous,
      ...Object.fromEntries(group.map((followUp) => [followUp.id, followUp.text]))
    }));
    setEditingGroupID(groupID);
    if (!queueEditSupported) return;
    const target = group[0];
    setActionKey(`edit-begin:${groupID}`);
    setQueueError(null);
    try {
      const result = await editThreadQueueDraft({
        threadID,
        action: 'begin',
        followUpID: target.id,
        // 借出时把输入框现有草稿寄存给宿主，保存/取消时由宿主还原
        draft: { text: composerDraft.text, attachments: composerDraft.attachmentIDs }
      });
      setBorrowedID(result.followUpID ?? target.id);
      if (result.draft.text && result.draft.text !== target.text) {
        setEditDrafts((previous) => ({ ...previous, [target.id]: result.draft.text }));
      }
    } catch (err) {
      // 借出失败不拦用户：退回原有的就地编辑（threads.queue.update）
      setBorrowedID(null);
      setQueueError(err instanceof Error ? err.message : '借出跟进编辑失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, composerDraft, editThreadQueueDraft, queueEditSupported, threadID]);

  const handleSaveGroup = useCallback(async (group: RemoteFollowUp[]) => {
    if (actionKey || group.length === 0) return;
    const groupID = queueGroupID(group[0]);
    setActionKey(`edit:${groupID}`);
    setQueueError(null);
    try {
      for (const followUp of group) {
        const nextText = editDrafts[followUp.id] ?? followUp.text;
        const attachments = followUp.attachments.map((attachment) => attachment.id);
        if (queueEditSupported && borrowedID === followUp.id) {
          // save 已覆盖 update 的效果：借出的那一条只发 save，并释放租借
          await editThreadQueueDraft({
            threadID,
            action: 'save',
            followUpID: followUp.id,
            draft: { text: nextText, attachments }
          });
          setBorrowedID(null);
          continue;
        }
        if (nextText === followUp.text) continue;
        await updateThreadQueue({ threadID, followUpID: followUp.id, text: nextText, attachments });
      }
      setEditingGroupID(null);
      setEditDrafts({});
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '编辑跟进失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, borrowedID, editDrafts, editThreadQueueDraft, queueEditSupported, threadID, updateThreadQueue]);

  const handleCancelGroupEdit = useCallback(async () => {
    if (actionKey) return;
    const borrowed = borrowedID;
    setEditDrafts({});
    setEditingGroupID(null);
    setBorrowedID(null);
    if (!borrowed || !queueEditSupported) return;
    setActionKey(`edit-cancel:${borrowed}`);
    setQueueError(null);
    try {
      const result = await editThreadQueueDraft({ threadID, action: 'cancel' });
      if (result.vanished) await listThreadQueue(threadID);
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '取消跟进编辑失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, borrowedID, editThreadQueueDraft, listThreadQueue, queueEditSupported, threadID]);

  const handleRemoveGroup = useCallback(async (group: RemoteFollowUp[]) => {
    if (actionKey || group.length === 0) return;
    const groupID = queueGroupID(group[0]);
    setActionKey(`remove-group:${groupID}`);
    setQueueError(null);
    try {
      for (const followUp of group) await removeThreadQueue(threadID, followUp.id);
      setEditingGroupID(null);
      setEditDrafts({});
      setBorrowedID(null);
    } catch (err) {
      setQueueError(err instanceof Error ? err.message : '撤回跟进组失败');
    } finally {
      setActionKey(null);
    }
  }, [actionKey, removeThreadQueue, threadID]);

  if (!queueSupported || groups.length === 0) return null;

  // 插话只在会话运行中、provider 支持、且这一条没有附件时提供（后端无法把附件注入当前轮）
  const steerAvailable = isRunning && providerSupportsSteer(provider);

  return (
    <section
      ref={scrollRef}
      className="shrink-0 max-h-[30dvh] overflow-y-auto border-t border-[var(--border)] bg-[var(--card-alt,var(--card))] px-4 py-2.5"
      aria-label="跟进队列"
    >
      <div className="mb-1.5 flex items-center justify-between gap-2">
        <span className="text-xs font-semibold text-[var(--foreground)]">
          跟进队列 · {followUps.length}
        </span>
        {queueError && (
          <span className="min-w-0 truncate text-mc-caption text-[var(--danger-foreground)]" role="alert">
            {queueError}
          </span>
        )}
      </div>
      <div className="space-y-1.5">
        {groups.map(([groupID, group]) => {
          // 系统条目（priority：子代理汇报、提问转交与通知）整组只读，不提供编辑与撤回。
          const isSystem = group.some((followUp) => followUp.system);
          const editing = !isSystem && editingGroupID === groupID;
          return (
            <div key={groupID} className="rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 py-2">
              <div className="mb-1.5 flex items-center justify-between gap-2">
                <span className="text-mc-caption text-[var(--foreground-muted)]">
                  {group.length > 1 ? `同组跟进 · ${group.length} 条` : '跟进'}
                </span>
                {isSystem ? (
                  <span className="rounded-lg border border-[var(--border)] px-2 py-1 text-mc-caption text-[var(--foreground-muted)]">
                    系统消息
                  </span>
                ) : (
                  <div className="flex shrink-0 gap-1">
                    <button
                      type="button"
                      disabled={Boolean(actionKey) || !isConnected}
                      onClick={() => {
                        if (editing) {
                          void handleSaveGroup(group);
                        } else {
                          void handleBeginGroupEdit(group);
                        }
                      }}
                      className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--foreground)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                    >
                      {actionKey === `edit-begin:${groupID}`
                        ? '准备中'
                        : actionKey === `edit:${groupID}`
                        ? '保存中'
                        : editing
                        ? '保存组'
                        : '编辑组'}
                    </button>
                    {editing && (
                      <button
                        type="button"
                        disabled={Boolean(actionKey) || !isConnected}
                        onClick={() => void handleCancelGroupEdit()}
                        className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                      >
                        {actionKey?.startsWith('edit-cancel:') ? '取消中' : '取消'}
                      </button>
                    )}
                    <button
                      type="button"
                      disabled={Boolean(actionKey) || !isConnected}
                      onClick={() => void handleRemoveGroup(group)}
                      className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--danger-foreground)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                    >
                      {actionKey === `remove-group:${groupID}` ? '撤回中' : '撤回组'}
                    </button>
                  </div>
                )}
              </div>
              <div className="space-y-1.5">
                {group.map((followUp) => {
                  const index = orderIndex.get(followUp.id);
                  const isFirst = index === 0;
                  const isLast = index !== undefined && index === followUps.length - 1;
                  return (
                    <div key={followUp.id} data-follow-up-id={followUp.id} className="flex items-center gap-2">
                      {editing ? (
                        <input
                          value={editDrafts[followUp.id] ?? followUp.text}
                          onChange={(event) =>
                            setEditDrafts((previous) => ({ ...previous, [followUp.id]: event.target.value }))
                          }
                          aria-label={`编辑跟进 ${followUp.id}`}
                          className="min-h-[32px] min-w-0 flex-1 rounded-lg border border-[var(--input-border)] bg-[var(--screen)] px-2 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
                        />
                      ) : (
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-xs text-[var(--foreground)]">{followUp.text || '图片跟进'}</div>
                          {followUp.attachments.length > 0 && (
                            <div className="mt-0.5 text-mc-caption text-[var(--foreground-muted)]">
                              {followUp.attachments.length} 张图片
                            </div>
                          )}
                        </div>
                      )}
                      {!editing && (
                        <div className="flex shrink-0 items-center gap-1">
                          {!isSystem && steerAvailable && followUp.attachments.length === 0 && (
                            <button
                              type="button"
                              disabled={Boolean(actionKey) || !isConnected}
                              onClick={() => void handleSteer(followUp)}
                              aria-label={`插入到当前轮次 ${followUp.text}`}
                              className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--primary)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                            >
                              {actionKey === `steer:${followUp.id}` ? '插入中' : '插入'}
                            </button>
                          )}
                          {moveSupported && !isFirst && (
                            <button
                              type="button"
                              disabled={Boolean(actionKey) || !isConnected}
                              onClick={() => void handleMove(followUp, 'up')}
                              aria-label={`上移 ${followUp.text}`}
                              className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--foreground)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                            >
                              {actionKey === `move:${followUp.id}` ? '移动中' : '上移'}
                            </button>
                          )}
                          {moveSupported && !isLast && (
                            <button
                              type="button"
                              disabled={Boolean(actionKey) || !isConnected}
                              onClick={() => void handleMove(followUp, 'down')}
                              aria-label={`下移 ${followUp.text}`}
                              className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--foreground)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                            >
                              {actionKey === `move:${followUp.id}` ? '移动中' : '下移'}
                            </button>
                          )}
                          <button
                            type="button"
                            disabled={Boolean(actionKey) || !isConnected}
                            onClick={() => void handleSendNow(followUp.id)}
                            className="min-h-[28px] rounded-lg border border-[var(--border)] px-2 text-mc-caption text-[var(--primary)] hover:bg-[var(--user-bubble)] disabled:opacity-45"
                          >
                            {actionKey === `send:${followUp.id}` ? '发送中' : '立即发送'}
                          </button>
                        </div>
                      )}
                    </div>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
};
