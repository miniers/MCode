// 记忆抽屉：Mac 上待审的记忆先落本地收件箱，这里分三个分页处理。
//   - 待审提议：列出提议，接受（写入 Nowledge Mem）、编辑后接受、或驳回；
//   - 搜索：只读查 Nowledge Mem 里的记忆（MemorySearchPanel）；
//   - 新建：手动记录一条记忆并加入待审列表（MemoryCreateForm）。
// 外层结构照抄 SwarmBeesSheet：sheet-layer + settings-sheet，点击遮罩关闭。
// 每个分页跟着宿主声明的能力走，缺哪个方法就少哪个分页。

import React, { useState } from 'react';
import {
  REMOTE_MEMORY_CREATE_METHOD,
  REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD,
  REMOTE_MEMORY_PROPOSALS_LIST_METHOD,
  REMOTE_MEMORY_PROPOSALS_REJECT_METHOD,
  REMOTE_MEMORY_SEARCH_METHOD,
  useMemoryProposals,
  type MemoryProposalEdits,
  type RemoteMemoryProposal,
  type RemoteMemoryProposalStatus
} from '../context/remoteMemoryApi';
import { useRemoteCall } from '../context/useRemoteCall';
import { ConfirmDialog } from './ConfirmDialog';
import { XMarkIcon } from './Icons';
import {
  MEMORY_UNIT_TYPE_OPTIONS,
  MemoryCreateForm,
  memoryUnitTypeLabel
} from './MemoryCreateForm';
import { MemorySearchPanel } from './MemorySearchPanel';

export interface MemoryProposalsSheetProps {
  isOpen: boolean;
  onClose: () => void;
  /** 当前选中项目；为空（全部项目）时列全部项目的提议。 */
  projectID: string | null;
  /** 项目名，仅用于说明当前列的是哪个项目。 */
  projectName?: string;
}

type SheetTab = 'proposals' | 'search' | 'create';

/** 状态筛选的顺序与文案，取值与宿主 store 的 memory_proposals.status 一致。 */
const STATUS_TABS: { value: RemoteMemoryProposalStatus; label: string }[] = [
  { value: 'pending', label: '待审核' },
  { value: 'accepted', label: '已接受' },
  { value: 'rejected', label: '已驳回' }
];

/** 状态徽章文案。 */
const STATUS_LABELS: Record<RemoteMemoryProposalStatus, string> = {
  pending: '待审核',
  accepted: '已接受',
  rejected: '已驳回'
};

/** 来源文案：agent 会话内提议、distill 自动提炼、user 用户手写。 */
const SOURCE_LABELS: Record<string, string> = {
  agent: '会话提议',
  distill: '自动提炼',
  user: '手动记录'
};

/** 提议里没写重要度时按 0.5 处理，与桌面端 formatImportance 一致。 */
function baseImportance(proposal: RemoteMemoryProposal): number {
  return proposal.importance > 0 ? proposal.importance : 0.5;
}

/** 编辑草稿：只在展开编辑器时存在，提交前与提议逐字段比对。 */
interface EditDraft {
  id: string;
  title: string;
  content: string;
  type: string;
  importance: number;
}

/** 把编辑草稿收拾成 accept 的可选字段；没改动时返回 undefined，等同于原「接受」。 */
function buildEdits(
  proposal: RemoteMemoryProposal,
  draft: EditDraft
): MemoryProposalEdits | undefined {
  const edits: MemoryProposalEdits = {};
  const title = draft.title.trim();
  if (title && title !== proposal.title) edits.title = title;
  if (draft.content !== proposal.content) edits.content = draft.content;
  const type = draft.type.trim();
  if (type && type !== (proposal.unitType ?? '').trim()) edits.type = type;
  if (Math.abs(draft.importance - baseImportance(proposal)) > 0.001) {
    edits.importance = draft.importance;
  }
  return Object.keys(edits).length > 0 ? edits : undefined;
}

function formatTime(value: string): string {
  if (!value) return '时间未知';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  });
}

function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? (source || '未知来源');
}

const ACTION_BASE =
  'shrink-0 min-h-[34px] px-3 rounded-xl text-xs font-medium border disabled:opacity-45 transition';
const ACTION_ACCEPT =
  'border-emerald-200 dark:border-emerald-900/40 bg-emerald-50 dark:bg-emerald-950/20 hover:bg-emerald-100 text-emerald-700 dark:text-emerald-400';
const ACTION_DANGER =
  'border-rose-200 dark:border-rose-900/40 bg-rose-50 dark:bg-rose-950/20 hover:bg-rose-100 text-rose-600 dark:text-rose-400';
const ACTION_NEUTRAL =
  'border-slate-200 dark:border-zinc-700 bg-white/60 dark:bg-zinc-900/40 hover:bg-slate-100 dark:hover:bg-zinc-800 text-slate-600 dark:text-zinc-300';

export const MemoryProposalsSheet: React.FC<MemoryProposalsSheetProps> = ({
  isOpen,
  onClose,
  projectID,
  projectName
}) => {
  // 关闭时不渲染正文，避免抽屉收起后仍在轮询收件箱。
  if (!isOpen) return null;
  return (
    <MemoryProposalsSheetBody
      isOpen={isOpen}
      onClose={onClose}
      projectID={projectID}
      projectName={projectName}
    />
  );
};

const MemoryProposalsSheetBody: React.FC<MemoryProposalsSheetProps> = ({
  onClose,
  projectID,
  projectName
}) => {
  const { supportsMethod } = useRemoteCall();
  const listSupported = supportsMethod(REMOTE_MEMORY_PROPOSALS_LIST_METHOD);
  const searchSupported = supportsMethod(REMOTE_MEMORY_SEARCH_METHOD);
  const createSupported = supportsMethod(REMOTE_MEMORY_CREATE_METHOD);
  // 接受 / 驳回各自跟着方法走：宿主没声明就只展示提议，不给按钮。
  const acceptSupported = supportsMethod(REMOTE_MEMORY_PROPOSALS_ACCEPT_METHOD);
  const rejectSupported = supportsMethod(REMOTE_MEMORY_PROPOSALS_REJECT_METHOD);
  const tabs: { value: SheetTab; label: string }[] = [
    ...(listSupported ? [{ value: 'proposals' as SheetTab, label: '待审提议' }] : []),
    ...(searchSupported ? [{ value: 'search' as SheetTab, label: '搜索' }] : []),
    ...(createSupported ? [{ value: 'create' as SheetTab, label: '新建' }] : [])
  ];
  const [tab, setTab] = useState<SheetTab>(tabs[0]?.value ?? 'proposals');
  const [status, setStatus] = useState<RemoteMemoryProposalStatus>('pending');
  const { proposals, loading, error, accept, reject, reload } = useMemoryProposals(projectID, status);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [busyID, setBusyID] = useState<string | null>(null);
  const [actionError, setActionError] = useState('');
  const [createdNotice, setCreatedNotice] = useState('');
  const [editing, setEditing] = useState<EditDraft | null>(null);
  const [rejecting, setRejecting] = useState<RemoteMemoryProposal | null>(null);
  const [rejectingBusy, setRejectingBusy] = useState(false);

  const toggleExpanded = (id: string) => {
    setExpanded((prev) => ({ ...prev, [id]: !prev[id] }));
  };

  const updateDraft = (patch: Partial<Omit<EditDraft, 'id'>>) => {
    setEditing((prev) => (prev ? { ...prev, ...patch } : prev));
  };

  const startEdit = (proposal: RemoteMemoryProposal) => {
    setActionError('');
    setEditing({
      id: proposal.id,
      title: proposal.title,
      content: proposal.content,
      type: proposal.unitType ?? '',
      importance: baseImportance(proposal)
    });
  };

  // 接受会写入 Nowledge Mem：失败时宿主返回原始错误（如 Nowledge 未配置），这里直接展示。
  const handleAccept = async (proposal: RemoteMemoryProposal, edits?: MemoryProposalEdits) => {
    if (busyID) return;
    setBusyID(proposal.id);
    setActionError('');
    setCreatedNotice('');
    try {
      await accept(proposal.id, edits);
      setEditing(null);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : '接受失败，请稍后重试');
    } finally {
      setBusyID(null);
    }
  };

  const handleAcceptEdited = (proposal: RemoteMemoryProposal) => {
    if (!editing || editing.id !== proposal.id) return;
    void handleAccept(proposal, buildEdits(proposal, editing));
  };

  const handleConfirmReject = async () => {
    if (!rejecting || rejectingBusy) return;
    setRejectingBusy(true);
    setActionError('');
    try {
      await reject(rejecting.id);
      setRejecting(null);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : '驳回失败，请稍后重试');
    } finally {
      setRejectingBusy(false);
    }
  };

  // 新建成功后切回提议页并刷新列表，提示放在提议页顶部。
  const handleCreated = (notice: string) => {
    setCreatedNotice(notice);
    setActionError('');
    setStatus('pending');
    setTab('proposals');
    void reload();
  };

  if (tabs.length === 0) return null;

  const scopeText = projectName ? `当前项目：${projectName}` : '全部项目';
  const shownError = actionError || (tab === 'proposals' ? error : '');
  const hint =
    tab === 'proposals'
      ? `${scopeText} · 接受后写入 Nowledge Mem，驳回只改本地状态。`
      : tab === 'search'
        ? `${scopeText} · 只读搜索，不会改动任何记忆。`
        : '手动记录一条记忆，来源记为「我」，加入待审列表后再确认写入。';

  return (
    <>
      <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
        <section
          className="settings-sheet max-h-[85dvh] overflow-y-auto"
          role="dialog"
          aria-modal="true"
          aria-labelledby="memory-proposals-title"
          onMouseDown={(event) => event.stopPropagation()}
        >
          <div className="sheet-handle" aria-hidden="true" />

          {/* 标题栏 */}
          <div className="flex items-center justify-between gap-3 mb-4">
            <div>
              <p className="eyebrow">记忆</p>
              <h2 id="memory-proposals-title" className="sheet-title">
                记忆
              </h2>
            </div>
            <button
              type="button"
              className="icon-button"
              onClick={onClose}
              aria-label="关闭"
              title="关闭"
            >
              <XMarkIcon className="h-4 w-4" />
            </button>
          </div>

          {/* 分页：待审提议 / 搜索 / 新建，缺哪个宿主能力就少哪个分页 */}
          {tabs.length > 1 && (
            <div className="filter-row" role="tablist" aria-label="记忆分页">
              {tabs.map((item) => (
                <button
                  key={item.value}
                  type="button"
                  role="tab"
                  aria-selected={tab === item.value}
                  onClick={() => setTab(item.value)}
                  className={`filter-chip ${tab === item.value ? 'filter-chip--active' : ''}`}
                >
                  {item.label}
                </button>
              ))}
            </div>
          )}

          <p className="mt-3 text-xs text-[var(--foreground-muted)]">{hint}</p>

          {tab === 'proposals' && (
            <>
              {/* 状态筛选 */}
              <div className="filter-row" role="tablist" aria-label="审核状态">
                {STATUS_TABS.map((item) => (
                  <button
                    key={item.value}
                    type="button"
                    role="tab"
                    aria-selected={status === item.value}
                    onClick={() => {
                      setStatus(item.value);
                      setCreatedNotice('');
                      setEditing(null);
                    }}
                    className={`filter-chip ${status === item.value ? 'filter-chip--active' : ''}`}
                  >
                    {item.label}
                  </button>
                ))}
              </div>

              {createdNotice && (
                <p className="settings-note" role="status">
                  {createdNotice}
                </p>
              )}

              {shownError && (
                <p className="settings-error" role="alert">
                  {shownError}
                </p>
              )}

              {loading && proposals.length === 0 && <p className="settings-note">加载中…</p>}

              {!loading && !shownError && proposals.length === 0 && (
                <div className="py-12 text-center text-xs text-slate-400 dark:text-zinc-500">
                  <p>暂无记忆提议</p>
                </div>
              )}

              <div className="mt-3 space-y-3 pb-2">
                {proposals.map((proposal) => {
                  const isExpanded = Boolean(expanded[proposal.id]);
                  const isPending = proposal.status === 'pending';
                  const isBusy = busyID === proposal.id;
                  const isEditing = editing?.id === proposal.id;
                  return (
                    <article
                      key={proposal.id}
                      className="p-3.5 rounded-2xl border border-slate-200/80 dark:border-zinc-800 bg-white/60 dark:bg-zinc-900/40 space-y-2.5"
                    >
                      {/* 标题与状态 */}
                      <div className="flex items-start justify-between gap-2">
                        <h3 className="min-w-0 text-sm font-semibold text-slate-800 dark:text-slate-100 break-words">
                          {proposal.title || '（无标题）'}
                        </h3>
                        <span className="shrink-0 px-1.5 py-0.5 rounded text-mc-caption bg-slate-100 dark:bg-zinc-800 text-slate-600 dark:text-zinc-300">
                          {STATUS_LABELS[proposal.status] ?? proposal.status}
                        </span>
                      </div>

                      {/* 正文摘要，点一下展开全文；编辑时换成可编辑正文 */}
                      {!isEditing && (
                        <p
                          onClick={() => toggleExpanded(proposal.id)}
                          className={`cursor-pointer whitespace-pre-wrap break-words text-xs leading-relaxed text-slate-700 dark:text-slate-300 ${
                            isExpanded ? '' : 'line-clamp-3'
                          }`}
                          title={isExpanded ? '点击收起正文' : '点击展开全文'}
                        >
                          {proposal.content || '（无正文）'}
                        </p>
                      )}

                      {proposal.error && (
                        <p className="text-mc-caption text-rose-600 dark:text-rose-400 break-words">
                          {proposal.error}
                        </p>
                      )}

                      {/* 编辑后接受：标题、正文、类型、重要度，照桌面端提议编辑区 */}
                      {isEditing && editing && (
                        <div className="space-y-2.5 rounded-xl border border-slate-200 dark:border-zinc-800 p-2.5">
                          <div>
                            <span className="settings-label">标题</span>
                            <input
                              className="settings-input"
                              type="text"
                              value={editing.title}
                              maxLength={200}
                              aria-label="编辑标题"
                              onChange={(event) => updateDraft({ title: event.target.value })}
                            />
                          </div>
                          <div>
                            <span className="settings-label">正文</span>
                            <textarea
                              className="settings-input"
                              style={{ minHeight: 120, paddingTop: 8, paddingBottom: 8, lineHeight: 1.6 }}
                              rows={5}
                              value={editing.content}
                              aria-label="编辑正文"
                              onChange={(event) => updateDraft({ content: event.target.value })}
                            />
                          </div>
                          <div>
                            <span className="settings-label">类型</span>
                            <div className="filter-row" role="group" aria-label="编辑类型">
                              {MEMORY_UNIT_TYPE_OPTIONS.map((value) => (
                                <button
                                  key={value}
                                  type="button"
                                  aria-pressed={editing.type === value}
                                  onClick={() => updateDraft({ type: value })}
                                  className={`filter-chip ${
                                    editing.type === value ? 'filter-chip--active' : ''
                                  }`}
                                >
                                  {memoryUnitTypeLabel(value)}
                                </button>
                              ))}
                            </div>
                          </div>
                          <div>
                            <div className="flex items-center justify-between gap-2">
                              <span className="settings-label">重要度</span>
                              <span className="text-mc-caption text-slate-500 dark:text-zinc-400">
                                {editing.importance.toFixed(1)}
                              </span>
                            </div>
                            <input
                              type="range"
                              min={0}
                              max={1}
                              step={0.1}
                              value={editing.importance}
                              aria-label="编辑重要度"
                              className="w-full"
                              style={{ accentColor: 'var(--primary)' }}
                              onChange={(event) =>
                                updateDraft({ importance: Number(event.target.value) })
                              }
                            />
                          </div>
                          <div className="flex items-center justify-end gap-2">
                            <button
                              type="button"
                              onClick={() => setEditing(null)}
                              disabled={busyID !== null}
                              className={`${ACTION_BASE} ${ACTION_NEUTRAL}`}
                            >
                              取消
                            </button>
                            <button
                              type="button"
                              onClick={() => handleAcceptEdited(proposal)}
                              disabled={busyID !== null}
                              className={`${ACTION_BASE} ${ACTION_ACCEPT}`}
                            >
                              {isBusy ? '保存中…' : '保存并接受'}
                            </button>
                          </div>
                        </div>
                      )}

                      {/* 来源与时间；待审项带编辑/接受/驳回 */}
                      <div className="flex flex-wrap items-center justify-between gap-2 text-mc-caption text-slate-500 dark:text-zinc-400">
                        <span className="min-w-0 break-words">
                          {`${sourceLabel(proposal.source)} · ${formatTime(proposal.createdAt)}`}
                        </span>
                        {isPending && !isEditing && (acceptSupported || rejectSupported) && (
                          <div className="flex items-center gap-2">
                            {acceptSupported && (
                              <button
                                type="button"
                                onClick={() => startEdit(proposal)}
                                disabled={busyID !== null}
                                className={`${ACTION_BASE} ${ACTION_NEUTRAL}`}
                              >
                                编辑后接受
                              </button>
                            )}
                            {acceptSupported && (
                              <button
                                type="button"
                                onClick={() => void handleAccept(proposal)}
                                disabled={busyID !== null}
                                className={`${ACTION_BASE} ${ACTION_ACCEPT}`}
                              >
                                {isBusy ? '写入中…' : '接受'}
                              </button>
                            )}
                            {rejectSupported && (
                              <button
                                type="button"
                                onClick={() => {
                                  setActionError('');
                                  setRejecting(proposal);
                                }}
                                disabled={busyID !== null}
                                className={`${ACTION_BASE} ${ACTION_DANGER}`}
                              >
                                驳回
                              </button>
                            )}
                          </div>
                        )}
                      </div>
                    </article>
                  );
                })}
              </div>
            </>
          )}

          {tab === 'search' && (
            <MemorySearchPanel projectID={projectID} projectName={projectName} />
          )}

          {tab === 'create' && (
            <MemoryCreateForm projectID={projectID} projectName={projectName} onCreated={handleCreated} />
          )}
        </section>
      </div>

      {/* 驳回前二次确认 */}
      <ConfirmDialog
        isOpen={rejecting !== null}
        title="驳回记忆提议"
        message={
          rejecting
            ? `确定驳回「${rejecting.title || '这条提议'}」吗？驳回后不会再写入 Nowledge Mem。`
            : ''
        }
        confirmLabel="驳回"
        confirmVariant="danger"
        isLoading={rejectingBusy}
        onConfirm={() => void handleConfirmReject()}
        onCancel={() => {
          if (!rejectingBusy) setRejecting(null);
        }}
      />
    </>
  );
};
