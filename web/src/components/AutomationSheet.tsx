// 自动化运行记录面板：一级为当前项目的自动化列表，点进去看某个自动化的运行记录。
// 数据来自宿主扩展方法 automations.*（internal/remote/automation_ext_methods.go）。
// 除查看、立即运行、标记已读与归档外，宿主声明写方法时还支持新建、编辑、暂停/恢复、重置、删除。

import React, { useCallback, useMemo, useState } from 'react';
import {
  useAutomationRuns,
  useProjectAutomations,
  useRemoteAutomationApi,
  type RemoteAutomationExtAutomation,
  type RemoteAutomationExtInput,
  type RemoteAutomationExtRun
} from '../context/remoteAutomationApi';
import { useRemoteStatic } from '../context/RemoteContext';
import { formatRelativeTime } from '../utils/date';
import { AutomationEditForm } from './AutomationEditForm';
import { ConfirmDialog } from './ConfirmDialog';
import { ArchiveBoxIcon, ChevronRightIcon, PlayIcon, XMarkIcon } from './Icons';

interface AutomationSheetProps {
  projectID: string;
  onClose: () => void;
  /** 打开某次运行产生的会话；由 ThreadList 用它自己的 onSelectThread 实现。 */
  onOpenThread: (threadID: string) => void;
}

/** 运行状态的中文文案与配色，与桌面端 automationRunStatus 保持一致。 */
function runStatusInfo(status: string): { label: string; className: string } {
  switch (status) {
    case 'running':
      return { label: '运行中', className: 'text-sky-600 dark:text-sky-400' };
    case 'succeeded':
    case 'success':
    case 'completed':
      return { label: '成功', className: 'text-emerald-600 dark:text-emerald-400' };
    case 'failed':
    case 'error':
      return { label: '失败', className: 'text-rose-600 dark:text-rose-400' };
    case 'stopped':
    case 'canceled':
    case 'cancelled':
      return { label: '已停止', className: 'text-[var(--foreground-muted)]' };
    case '':
      return { label: '未知', className: 'text-[var(--foreground-muted)]' };
    default:
      return { label: status, className: 'text-[var(--foreground-muted)]' };
  }
}

/** 运行耗时文案，如「12s」「3m 05s」「1h 02m」；未结束或时间异常时返回空串。 */
function runDurationText(run: RemoteAutomationExtRun): string {
  if (!run.startedAt || !run.finishedAt) return '';
  const started = new Date(run.startedAt).getTime();
  const finished = new Date(run.finishedAt).getTime();
  if (!Number.isFinite(started) || !Number.isFinite(finished) || finished < started) return '';
  const seconds = Math.round((finished - started) / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, '0')}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, '0')}m`;
}

const CARD_CLASS =
  'rounded-2xl border border-slate-200/80 dark:border-zinc-800 bg-white/60 dark:bg-zinc-900/40 p-3.5 space-y-2.5';
const SMALL_BUTTON_CLASS =
  'min-h-[32px] px-2.5 rounded-xl text-mc-caption font-medium border border-[var(--border)] bg-[var(--card)] ' +
  'text-[var(--foreground)] hover:bg-[color-mix(in_srgb,var(--border)_48%,transparent)] disabled:opacity-45 transition';
const UNREAD_BADGE_CLASS =
  'inline-flex h-4 min-w-4 shrink-0 items-center justify-center rounded-full bg-[var(--primary)] px-1 ' +
  'text-mc-caption font-bold leading-none text-white';

export const AutomationSheet: React.FC<AutomationSheetProps> = ({ projectID, onClose, onOpenThread }) => {
  const api = useRemoteAutomationApi();
  // 项目列表用于新建时选择归属项目，默认选当前项目。
  const { projects } = useRemoteStatic();
  const { automations, loading, error, reload } = useProjectAutomations(projectID);
  const [activeID, setActiveID] = useState<string | null>(null);
  const [includeArchived, setIncludeArchived] = useState(false);
  const {
    runs,
    loading: runsLoading,
    error: runsError,
    reload: reloadRuns
  } = useAutomationRuns(activeID, includeArchived);
  const [runningAutomationID, setRunningAutomationID] = useState<string | null>(null);
  const [busyRunID, setBusyRunID] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  // 编辑态：null 表示不在编辑；automation 为 null 表示新建。
  const [editor, setEditor] = useState<{ automation: RemoteAutomationExtAutomation | null } | null>(null);
  const [savingEdit, setSavingEdit] = useState(false);
  const [editError, setEditError] = useState<string | null>(null);
  const [busyAutomationID, setBusyAutomationID] = useState<string | null>(null);
  // 删除与重置需要二次确认，对应桌面端的 confirmDelete 流程。
  const [confirm, setConfirm] = useState<{
    kind: 'delete' | 'reset';
    automation: RemoteAutomationExtAutomation;
  } | null>(null);

  const active = useMemo(() => automations.find((item) => item.id === activeID) ?? null, [automations, activeID]);

  const openRuns = useCallback((item: RemoteAutomationExtAutomation) => {
    setActiveID(item.id);
    setActionError(null);
    setNotice(null);
  }, []);

  const backToList = useCallback(() => {
    setActiveID(null);
    setIncludeArchived(false);
    setActionError(null);
    setNotice(null);
  }, []);

  const handleRunNow = useCallback(
    async (automationID: string) => {
      if (runningAutomationID) return;
      setRunningAutomationID(automationID);
      setActionError(null);
      setNotice(null);
      try {
        await api.runNow(automationID);
        setNotice('已触达一次运行，稍后可在运行记录里查看结果');
        reload();
        if (activeID === automationID) reloadRuns();
      } catch (err: unknown) {
        setActionError(err instanceof Error ? err.message : '启动运行失败');
      } finally {
        setRunningAutomationID(null);
      }
    },
    [activeID, api, reload, reloadRuns, runningAutomationID]
  );

  const handleMarkRead = useCallback(
    async (runID: string) => {
      setBusyRunID(runID);
      setActionError(null);
      try {
        await api.markRunRead(runID);
        reload();
        reloadRuns();
      } catch (err: unknown) {
        setActionError(err instanceof Error ? err.message : '标记已读失败');
      } finally {
        setBusyRunID(null);
      }
    },
    [api, reload, reloadRuns]
  );

  const handleArchive = useCallback(
    async (run: RemoteAutomationExtRun) => {
      setBusyRunID(run.id);
      setActionError(null);
      try {
        await api.archiveRun(run.id, !run.archived);
        reloadRuns();
      } catch (err: unknown) {
        setActionError(err instanceof Error ? err.message : run.archived ? '取消归档失败' : '归档失败');
      } finally {
        setBusyRunID(null);
      }
    },
    [api, reloadRuns]
  );

  const failureText = error || runsError || actionError;

  /** 新建或保存；成功后关闭表单并刷新列表（automations.changed 也会触发一次刷新）。 */
  const handleSubmitEdit = useCallback(
    async (formProjectID: string, input: RemoteAutomationExtInput) => {
      if (savingEdit) return;
      setSavingEdit(true);
      setEditError(null);
      try {
        if (editor?.automation) {
          await api.updateAutomation(editor.automation.id, input, editor.automation.revision);
          setNotice('已保存自动化');
        } else {
          await api.createAutomation(formProjectID, input);
          setNotice('已创建自动化');
        }
        setEditor(null);
        setActionError(null);
        reload();
      } catch (err: unknown) {
        setEditError(err instanceof Error ? err.message : '保存自动化失败');
      } finally {
        setSavingEdit(false);
      }
    },
    [api, editor, reload, savingEdit]
  );

  /** 暂停/恢复：恢复（enabled=true）时宿主同时清零连续失败计数。 */
  const handleToggleEnabled = useCallback(
    async (item: RemoteAutomationExtAutomation) => {
      if (busyAutomationID) return;
      const nextEnabled = !item.enabled;
      setBusyAutomationID(item.id);
      setActionError(null);
      setNotice(null);
      try {
        await api.setAutomationEnabled(item.id, nextEnabled, item.revision);
        setNotice(nextEnabled ? `已恢复「${item.name}」` : `已暂停「${item.name}」`);
        reload();
      } catch (err: unknown) {
        setActionError(err instanceof Error ? err.message : nextEnabled ? '恢复自动化失败' : '暂停自动化失败');
      } finally {
        setBusyAutomationID(null);
      }
    },
    [api, busyAutomationID, reload]
  );

  /** 确认后执行删除或重置。 */
  const handleConfirmAction = useCallback(async () => {
    if (!confirm) return;
    const { kind, automation } = confirm;
    setBusyAutomationID(automation.id);
    setActionError(null);
    setNotice(null);
    try {
      if (kind === 'delete') {
        await api.deleteAutomation(automation.id);
        setNotice(`已删除「${automation.name}」`);
      } else {
        await api.resetAutomation(automation.id, automation.revision);
        setNotice(`已重置「${automation.name}」的连续失败计数`);
      }
      setConfirm(null);
      reload();
    } catch (err: unknown) {
      setActionError(err instanceof Error ? err.message : kind === 'delete' ? '删除自动化失败' : '重置自动化失败');
    } finally {
      setBusyAutomationID(null);
    }
  }, [api, confirm, reload]);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="settings-sheet max-h-[85dvh] overflow-y-auto"
        role="dialog"
        aria-modal="true"
        aria-labelledby="automation-sheet-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />

        {/* 标题栏：二级页显示返回 */}
        <div className="flex items-center justify-between gap-3 mb-4">
          <div className="flex min-w-0 items-center gap-2">
            {activeID && (
              <button
                type="button"
                className="icon-button"
                onClick={backToList}
                aria-label="返回自动化列表"
                title="返回自动化列表"
              >
                <ChevronRightIcon className="h-4 w-4 rotate-180" />
              </button>
            )}
            <div className="min-w-0">
              <p className="eyebrow">自动化</p>
              <h2 id="automation-sheet-title" className="sheet-title truncate">
                {active ? active.name : '自动化与运行记录'}
              </h2>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            {api.supportsCreate && !editor && !activeID && (
              <button
                type="button"
                className="min-h-[32px] rounded-xl bg-[var(--primary)] px-3 text-mc-caption font-semibold text-white transition"
                onClick={() => {
                  setEditor({ automation: null });
                  setEditError(null);
                }}
              >
                新建自动化
              </button>
            )}
            <button type="button" className="icon-button" onClick={onClose} aria-label="关闭自动化面板" title="关闭">
              <XMarkIcon className="h-4 w-4" />
            </button>
          </div>
        </div>

        {/* 编辑态：表单占满面板，避免窄屏嵌套弹窗 */}
        {editor && (
          <AutomationEditForm
            automation={editor.automation}
            projects={projects}
            defaultProjectID={projectID}
            saving={savingEdit}
            error={editError}
            onCancel={() => {
              setEditor(null);
              setEditError(null);
            }}
            onSubmit={(formProjectID, input) => void handleSubmitEdit(formProjectID, input)}
          />
        )}

        {active && (
          <p className="mb-3 text-xs text-[var(--foreground-muted)]">
            {`${active.scheduleText} · ${active.enabled ? '已启用' : '已停用'}`}
          </p>
        )}

        {notice && (
          <div className="inline-alert" role="status" aria-live="polite">
            <span className="min-w-0 select-text break-words">{notice}</span>
          </div>
        )}

        {failureText && (
          <div className="inline-alert inline-alert--danger" role="alert">
            <span className="min-w-0 select-text break-words">{failureText}</span>
            <button
              type="button"
              className="inline-alert__action"
              onClick={() => {
                setActionError(null);
                reload();
                reloadRuns();
              }}
            >
              重试
            </button>
          </div>
        )}

        {/* 一级：自动化列表（编辑时隐藏，让表单独占面板） */}
        {!activeID && !editor && (
          <>
            {loading && <p className="py-8 text-center text-xs text-[var(--foreground-muted)]">加载中…</p>}
            {!loading && automations.length === 0 && (
              <div className="py-12 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                <div className="text-2xl">🕒</div>
                <p>当前项目还没有自动化</p>
                {api.supportsCreate ? (
                  <button
                    type="button"
                    className="mx-auto min-h-[36px] rounded-xl bg-[var(--primary)] px-3 text-mc-caption font-semibold text-white transition"
                    onClick={() => {
                      setEditor({ automation: null });
                      setEditError(null);
                    }}
                  >
                    新建自动化
                  </button>
                ) : (
                  <p>自动化在桌面端创建，创建后这里可以查看运行记录。</p>
                )}
              </div>
            )}
            <div className="space-y-3 pb-2">
              {automations.map((item) => (
                <article key={item.id} className={CARD_CLASS}>
                  <div className="flex items-center justify-between gap-2">
                    <button
                      type="button"
                      className="flex min-w-0 items-center gap-1.5 text-left text-xs font-semibold text-[var(--foreground)]"
                      onClick={() => openRuns(item)}
                      title="查看运行记录"
                    >
                      <span className="truncate">{item.name}</span>
                      {item.unreadCount > 0 && (
                        <span className={UNREAD_BADGE_CLASS} aria-label={`${item.unreadCount} 条未读运行记录`}>
                          {item.unreadCount > 99 ? '99+' : item.unreadCount}
                        </span>
                      )}
                    </button>
                    <span
                      className={`shrink-0 rounded px-1.5 py-0.5 text-mc-caption font-medium ${
                        item.enabled
                          ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400'
                          : 'bg-slate-100 text-slate-500 dark:bg-zinc-800 dark:text-zinc-400'
                      }`}
                    >
                      {item.enabled ? '已启用' : '已停用'}
                    </span>
                  </div>

                  <p className="text-mc-caption text-[var(--foreground-muted)]">{item.scheduleText}</p>

                  <div className="flex flex-wrap items-center justify-between gap-2">
                    <span className="min-w-0 flex-1 truncate text-mc-caption text-[var(--foreground-muted)]">
                      {item.lastRunAt ? `上次运行 ${formatRelativeTime(item.lastRunAt)}` : '尚未运行'}
                    </span>
                    <div className="flex flex-wrap items-center justify-end gap-1.5">
                      {api.supportsRuns && (
                        <button type="button" className={SMALL_BUTTON_CLASS} onClick={() => openRuns(item)}>
                          运行记录
                        </button>
                      )}
                      {api.supportsRun && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => void handleRunNow(item.id)}
                          disabled={runningAutomationID === item.id}
                        >
                          <span className="inline-flex items-center gap-1">
                            <PlayIcon className="h-3 w-3" />
                            {runningAutomationID === item.id ? '启动中…' : '立即运行'}
                          </span>
                        </button>
                      )}
                      {api.supportsUpdate && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => {
                            setEditor({ automation: item });
                            setEditError(null);
                          }}
                        >
                          编辑
                        </button>
                      )}
                      {api.supportsSetEnabled && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => void handleToggleEnabled(item)}
                          disabled={busyAutomationID === item.id}
                        >
                          {item.enabled ? '暂停' : '恢复'}
                        </button>
                      )}
                      {api.supportsReset && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => setConfirm({ kind: 'reset', automation: item })}
                          disabled={busyAutomationID === item.id}
                        >
                          重置
                        </button>
                      )}
                      {api.supportsDelete && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => setConfirm({ kind: 'delete', automation: item })}
                          disabled={busyAutomationID === item.id}
                        >
                          删除
                        </button>
                      )}
                    </div>
                  </div>
                </article>
              ))}
            </div>
          </>
        )}

        {/* 二级：运行记录 */}
        {activeID && (
          <>
            <div className="mb-3 flex items-center justify-between gap-3">
              <div className="min-w-0">
                <p className="text-xs font-semibold text-[var(--foreground)]">运行记录</p>
                <p className="text-mc-caption leading-relaxed text-[var(--foreground-muted)]">
                  默认只显示未归档记录，未读用圆点标记
                </p>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={includeArchived}
                aria-label="切换显示已归档"
                onClick={() => setIncludeArchived((value) => !value)}
                className={`relative h-5 w-9 shrink-0 rounded-full transition ${
                  includeArchived ? 'bg-mc-accent' : 'bg-[var(--border)]'
                }`}
              >
                <span
                  className={`absolute left-0.5 top-0.5 h-4 w-4 rounded-full bg-white transition-transform ${
                    includeArchived ? 'translate-x-4' : ''
                  }`}
                />
              </button>
            </div>
            {includeArchived && (
              <p className="mb-3 text-mc-caption text-[var(--foreground-muted)]">已一并显示归档记录</p>
            )}

            {runsLoading && runs.length === 0 && (
              <p className="py-8 text-center text-xs text-[var(--foreground-muted)]">加载中…</p>
            )}
            {!runsLoading && runs.length === 0 && (
              <div className="py-12 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                <div className="text-2xl">📄</div>
                <p>还没有运行记录</p>
                <p>点「立即运行」可以手动触发一次。</p>
              </div>
            )}

            <div className="space-y-3 pb-2">
              {runs.map((run) => {
                const status = runStatusInfo(run.status);
                const duration = runDurationText(run);
                const busy = busyRunID === run.id;
                return (
                  <article key={run.id} className={CARD_CLASS}>
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex min-w-0 items-center gap-1.5">
                        {!run.read && (
                          <span className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--primary)]" aria-label="未读" />
                        )}
                        <span className={`text-mc-caption font-semibold ${status.className}`}>{status.label}</span>
                        <span className="truncate text-mc-caption text-[var(--foreground-muted)]">
                          {formatRelativeTime(run.startedAt) || '时间未知'}
                        </span>
                        {run.archived && (
                          <span className="shrink-0 rounded bg-slate-100 px-1.5 py-0.5 text-mc-caption text-slate-500 dark:bg-zinc-800 dark:text-zinc-400">
                            已归档
                          </span>
                        )}
                      </div>
                      {duration && (
                        <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">{duration}</span>
                      )}
                    </div>

                    {(run.error || run.summary) && (
                      <p
                        className={`whitespace-pre-wrap break-words text-mc-caption leading-relaxed ${
                          run.error ? 'text-rose-600 dark:text-rose-400' : 'text-[var(--foreground)]'
                        }`}
                      >
                        {run.error || run.summary}
                      </p>
                    )}

                    <div className="flex flex-wrap items-center gap-2">
                      {run.threadID && (
                        <button type="button" className={SMALL_BUTTON_CLASS} onClick={() => onOpenThread(run.threadID!)}>
                          打开会话
                        </button>
                      )}
                      {!run.read && api.supportsMarkRead && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => void handleMarkRead(run.id)}
                          disabled={busy}
                        >
                          标记已读
                        </button>
                      )}
                      {api.supportsArchiveRun && (
                        <button
                          type="button"
                          className={SMALL_BUTTON_CLASS}
                          onClick={() => void handleArchive(run)}
                          disabled={busy}
                        >
                          <span className="inline-flex items-center gap-1">
                            <ArchiveBoxIcon className="h-3 w-3" />
                            {run.archived ? '取消归档' : '归档'}
                          </span>
                        </button>
                      )}
                    </div>
                  </article>
                );
              })}
            </div>
          </>
        )}

        {/* 删除与重置的二次确认 */}
        <ConfirmDialog
          isOpen={confirm !== null}
          title={confirm?.kind === 'delete' ? '删除自动化' : '重置连续失败计数'}
          message={
            confirm?.kind === 'delete'
              ? `确定删除「${confirm.automation.name}」吗？删除后不可恢复。`
              : `确定重置「${confirm?.automation.name ?? ''}」吗？重置会清零连续失败计数并恢复启用。`
          }
          confirmLabel={confirm?.kind === 'delete' ? '确认删除' : '确认重置'}
          confirmVariant={confirm?.kind === 'delete' ? 'danger' : 'primary'}
          isLoading={busyAutomationID !== null}
          onConfirm={() => void handleConfirmAction()}
          onCancel={() => {
            if (busyAutomationID !== null) return;
            setConfirm(null);
          }}
        />
      </section>
    </div>
  );
};
