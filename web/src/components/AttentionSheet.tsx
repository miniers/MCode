// 待处理收件箱抽屉：把会话卡住、出错、额度用尽、等待审批、看板与自动化失败等
// 需要用户处理的事项汇总成一张清单，按类型分组，逐条可跳转会话或标记已处理。
// 外层结构照 MemoryProposalsSheet：sheet-layer + settings-sheet，点击遮罩关闭。

import React, { useState } from 'react';
import {
  useAttentionInbox,
  type RemoteAttentionItem,
  type RemoteAttentionSuggestion
} from '../context/remoteAttentionApi';
import { formatRelativeTime } from '../utils/date';
import { XMarkIcon } from './Icons';

export interface AttentionSheetProps {
  isOpen: boolean;
  onClose: () => void;
  /** 当前选中项目；为空（全部项目）时列全部项目的待处理事项。 */
  projectID: string | null;
  /** 项目名，仅用于说明当前列的是哪个项目。 */
  projectName?: string;
}

/** 分组顺序与中文标题；取值与宿主 attention 的 kind 一致，未知取值归入「其他」。 */
const KIND_GROUPS: { kind: string; label: string }[] = [
  { kind: 'thread_stalled', label: '会话卡住' },
  { kind: 'thread_failed', label: '会话出错' },
  { kind: 'thread_quota', label: '额度用尽' },
  { kind: 'thread_approval', label: '等待审批' },
  { kind: 'kanban_failed', label: '看板失败' },
  { kind: 'kanban_needs_approval', label: '看板待审批' },
  { kind: 'kanban_review', label: '看板待验收' },
  { kind: 'automation_failed', label: '自动化失败' },
  { kind: 'automation_finished', label: '自动化完成' },
  { kind: 'bee_failed', label: 'Bee 失败' },
  { kind: 'thread_no_progress', label: '任务无进展' }
];

const OTHER_GROUP_LABEL = '其他';

const ACTION_BASE =
  'shrink-0 min-h-[30px] px-2.5 rounded-xl text-mc-caption font-medium border disabled:opacity-45 transition';

/** 按 kind 分组，保持 KIND_GROUPS 的顺序，未知 kind 落到「其他」。 */
function groupItems(items: RemoteAttentionItem[]): { label: string; items: RemoteAttentionItem[] }[] {
  const buckets = new Map<string, RemoteAttentionItem[]>();
  KIND_GROUPS.forEach((group) => buckets.set(group.kind, []));
  const others: RemoteAttentionItem[] = [];
  items.forEach((item) => {
    const bucket = buckets.get(item.kind);
    if (bucket) bucket.push(item);
    else others.push(item);
  });
  const groups = KIND_GROUPS.map((group) => ({
    label: group.label,
    items: buckets.get(group.kind) ?? []
  })).filter((group) => group.items.length > 0);
  if (others.length > 0) groups.push({ label: OTHER_GROUP_LABEL, items: others });
  return groups;
}

export const AttentionSheet: React.FC<AttentionSheetProps> = ({
  isOpen,
  onClose,
  projectID,
  projectName
}) => {
  // 关闭时不渲染正文，避免抽屉收起后仍在拉取待处理列表。
  if (!isOpen) return null;
  return (
    <AttentionSheetBody
      isOpen={isOpen}
      onClose={onClose}
      projectID={projectID}
      projectName={projectName}
    />
  );
};

const AttentionSheetBody: React.FC<AttentionSheetProps> = ({ onClose, projectID, projectName }) => {
  const { supported, items, unresolvedCount, loading, error, resolve, canConfirmSuggestion, confirmSuggestion } =
    useAttentionInbox(projectID);
  const [busyID, setBusyID] = useState<string | null>(null);
  const [actionError, setActionError] = useState('');

  if (!supported) return null;

  const scopeText = projectName ? `当前项目：${projectName}` : '全部项目';
  const shownError = actionError || error;
  const groups = groupItems(items);

  const openThread = (threadID: string) => {
    onClose();
    window.location.hash = `#/t/${encodeURIComponent(threadID)}`;
  };

  const handleResolve = async (item: RemoteAttentionItem) => {
    if (busyID) return;
    setBusyID(item.id);
    setActionError('');
    try {
      await resolve(item.id);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : '标记已处理失败，请稍后重试');
    } finally {
      setBusyID(null);
    }
  };

  // 确认总管的建议：optionID 原样传回宿主，宿主发现建议已变更会报错，交给调用方提示。
  const handleConfirmSuggestion = async (
    item: RemoteAttentionItem,
    suggestion: RemoteAttentionSuggestion
  ) => {
    if (busyID) return;
    setBusyID(`${item.id}:${suggestion.requestID}`);
    setActionError('');
    try {
      await confirmSuggestion(item.threadID, suggestion);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : '确认建议失败，请稍后重试');
    } finally {
      setBusyID(null);
    }
  };

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="settings-sheet max-h-[85dvh] overflow-y-auto"
        role="dialog"
        aria-modal="true"
        aria-labelledby="attention-sheet-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />

        {/* 标题栏 */}
        <div className="flex items-center justify-between gap-3 mb-4">
          <div>
            <p className="eyebrow">收件箱</p>
            <h2 id="attention-sheet-title" className="sheet-title">
              待处理
            </h2>
          </div>
          <button type="button" className="icon-button" onClick={onClose} aria-label="关闭" title="关闭">
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <p className="text-xs text-[var(--foreground-muted)]">
          {`${scopeText} · 共 ${unresolvedCount} 条未处理，标记后不再出现在这里。`}
        </p>

        {shownError && (
          <p className="settings-error" role="alert">
            {shownError}
          </p>
        )}

        {loading && items.length === 0 && <p className="settings-note">加载中…</p>}

        {!loading && !shownError && items.length === 0 && (
          <div className="py-12 text-center text-xs text-slate-400 dark:text-zinc-500">
            <p>没有待处理事项</p>
          </div>
        )}

        <div className="mt-3 space-y-3 pb-2">
          {groups.map((group) => (
            <div key={group.label} className="space-y-2">
              <p className="settings-label">{`${group.label}（${group.items.length}）`}</p>
              {group.items.map((item) => {
                const canOpen = Boolean(item.threadID);
                const isBusy = busyID === item.id;
                return (
                  <article
                    key={item.id}
                    className={`p-3 rounded-2xl border border-slate-200/80 dark:border-zinc-800 bg-white/60 dark:bg-zinc-900/40 space-y-2 ${
                      canOpen ? 'cursor-pointer hover:border-slate-300 dark:hover:border-zinc-700' : ''
                    }`}
                    onClick={canOpen ? () => openThread(item.threadID) : undefined}
                    role={canOpen ? 'button' : undefined}
                    tabIndex={canOpen ? 0 : undefined}
                    onKeyDown={
                      canOpen
                        ? (event) => {
                            if (event.key === 'Enter' || event.key === ' ') {
                              event.preventDefault();
                              openThread(item.threadID);
                            }
                          }
                        : undefined
                    }
                  >
                    <div className="flex items-start justify-between gap-2">
                      <h3 className="min-w-0 text-sm font-semibold text-slate-800 dark:text-slate-100 break-words">
                        {item.title || '（无标题）'}
                      </h3>
                      <span className="shrink-0 text-mc-caption text-slate-500 dark:text-zinc-400">
                        {formatRelativeTime(item.enteredAt)}
                      </span>
                    </div>

                    {item.detail && (
                      <p
                        className="truncate text-xs text-slate-600 dark:text-slate-300"
                        title={item.detail}
                      >
                        {item.detail}
                      </p>
                    )}

                    {/* 总管的审批建议：详情完整展示，滚动区域不截断。 */}
                    {item.suggestions && item.suggestions.length > 0 && (
                      <div className="space-y-2">
                        {item.suggestions.map((suggestion) => {
                          // 除 allow 以外一律按拒绝展示，避免未知取值被误认为允许。
                          const isAllow = suggestion.optionID === 'allow';
                          const suggestionBusy = busyID === `${item.id}:${suggestion.requestID}`;
                          return (
                            <div
                              key={suggestion.requestID}
                              className="space-y-1.5 rounded-xl border border-slate-200 dark:border-zinc-800 bg-slate-50/70 dark:bg-zinc-900/60 p-2.5"
                            >
                              <p className="text-mc-caption font-medium text-slate-700 dark:text-slate-200 break-words">
                                {`${isAllow ? '总管建议允许' : '总管建议拒绝'} · ${suggestion.title}`}
                              </p>
                              {suggestion.detail && (
                                <p
                                  className="max-h-40 overflow-y-auto whitespace-pre-wrap break-words text-mc-caption text-slate-600 dark:text-slate-300"
                                  onClick={(event) => event.stopPropagation()}
                                >
                                  {suggestion.detail}
                                </p>
                              )}
                              {suggestion.reason && (
                                <p className="text-mc-caption text-slate-500 dark:text-zinc-400 break-words">
                                  {`理由：${suggestion.reason}`}
                                </p>
                              )}
                              {canConfirmSuggestion ? (
                                <div className="flex justify-end">
                                  <button
                                    type="button"
                                    onClick={(event) => {
                                      event.stopPropagation();
                                      void handleConfirmSuggestion(item, suggestion);
                                    }}
                                    disabled={busyID !== null}
                                    className={
                                      isAllow
                                        ? `${ACTION_BASE} border-emerald-200 dark:border-emerald-900/40 bg-emerald-50 dark:bg-emerald-950/20 hover:bg-emerald-100 text-emerald-700 dark:text-emerald-400`
                                        : `${ACTION_BASE} border-rose-200 dark:border-rose-900/40 bg-rose-50 dark:bg-rose-950/20 hover:bg-rose-100 text-rose-700 dark:text-rose-400`
                                    }
                                  >
                                    {suggestionBusy ? '处理中…' : isAllow ? '确认允许' : '确认拒绝'}
                                  </button>
                                </div>
                              ) : (
                                <p className="text-mc-caption text-slate-400 dark:text-zinc-500">
                                  当前宿主不支持确认，请打开会话处理
                                </p>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}

                    <div className="flex flex-wrap items-center justify-between gap-2 text-mc-caption text-slate-500 dark:text-zinc-400">
                      <span className="min-w-0 break-words">
                        {canOpen ? '点此打开会话' : '无关联会话'}
                      </span>
                      <button
                        type="button"
                        onClick={(event) => {
                          event.stopPropagation();
                          void handleResolve(item);
                        }}
                        disabled={busyID !== null}
                        className={`${ACTION_BASE} border-emerald-200 dark:border-emerald-900/40 bg-emerald-50 dark:bg-emerald-950/20 hover:bg-emerald-100 text-emerald-700 dark:text-emerald-400`}
                      >
                        {isBusy ? '处理中…' : '标记已处理'}
                      </button>
                    </div>
                  </article>
                );
              })}
            </div>
          ))}
        </div>
      </section>
    </div>
  );
};
