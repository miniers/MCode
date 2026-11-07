// 移动端会话脚本抽屉：列出 package.json scripts 与项目脚本，在 Mac 端执行；package 脚本可收藏

import React, { useCallback, useEffect, useImperativeHandle, useRef, useState } from 'react';
import { ScriptsSaveConflictError } from '../context/remoteScriptsApi';
import type {
  RemotePackageScript,
  RemoteProjectScript,
  RemoteScriptsListResult,
  RemoteScriptsRunStatus,
  RemoteScriptsRunTarget,
  RemoteScriptsSaveScript
} from '../context/remoteScriptsApi';
import { useRemoteStatic } from '../context/RemoteContext';
import { ConfirmDialog } from './ConfirmDialog';
import { ScriptEditForm, type ScriptDraft } from './ScriptEditForm';
import {
  ArrowPathIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ExclamationTriangleIcon,
  PlayIcon,
  PlusIcon,
  StarIcon
} from './Icons';
import { SheetFrame } from './sheet/SheetFrame';
import { MenuSheetHeader } from './thread/menu/MenuSheetHeader';
import { MenuPageChromeContext, useMenuPageChrome } from './thread/menu/menuPageChrome';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';

export interface ScriptsSheetProps {
  isOpen: boolean;
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
  /** 当前会话所属项目 ID；缺省时项目脚本只读（无法保存到项目级存储）。 */
  projectID?: string;
}

/** 内容体：供菜单页面栈与兼容外壳复用；不含遮罩、卡片与标题栏。 */
export interface ScriptsSheetBodyProps {
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
  projectID?: string;
}

// 运行结果轮询间隔：宿主只在内存里记录脚本状态，移动端定时拉取
const RUN_STATUS_POLL_INTERVAL_MS = 1500;
// 旧宿主（未声明 scripts.runStatus）只能确认脚本已启动
const RUN_STATUS_UNSUPPORTED_NOTICE = '已在 Mac 端启动，宿主版本过旧无法回显结果';

type RunTone = 'running' | 'success' | 'danger' | 'warning';

function runToneOf(status: RemoteScriptsRunStatus | null, notice: string | null): RunTone {
  if (notice) return 'warning';
  if (!status || status.status === 'running') return 'running';
  if (status.status === 'rejected') return 'warning';
  return status.exitCode === 0 ? 'success' : 'danger';
}

function runStatusTextOf(status: RemoteScriptsRunStatus | null, notice: string | null): string {
  if (notice) return notice;
  if (!status) return '正在启动…';
  if (status.status === 'running') return '运行中…';
  if (status.status === 'rejected') return '被拒绝（并发上限）';
  if (status.exitCode === 0) return '已完成 · 退出码 0';
  if (status.exitCode === -1) return '无法启动 · 退出码 -1';
  return `失败 · 退出码 ${status.exitCode ?? '未知'}`;
}

export const ScriptsSheetBody = React.forwardRef<ThreadMenuPageHandle, ScriptsSheetBodyProps>(
  function ScriptsSheetBody({ onClose, threadID, projectID }, ref) {
  const {
    capabilities,
    connectionState,
    connectionMessage,
    listThreadScripts,
    runThreadScript,
    getThreadScriptRunStatus,
    setThreadScriptFavorite,
    saveScripts,
    supportsMethod
  } = useRemoteStatic();
  const { setTrailing } = useMenuPageChrome();

  useImperativeHandle(ref, () => ({ requestClose: () => true }), []);

  const [result, setResult] = useState<RemoteScriptsListResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [favoriteError, setFavoriteError] = useState<string | null>(null);
  const [groupExpanded, setGroupExpanded] = useState(false);

  const [runningKey, setRunningKey] = useState<string | null>(null);
  const [runError, setRunError] = useState<string | null>(null);
  const [runLabel, setRunLabel] = useState<string | null>(null);
  const [runStatus, setRunStatus] = useState<RemoteScriptsRunStatus | null>(null);
  const [runNotice, setRunNotice] = useState<string | null>(null);
  // 正在轮询的 runID；置空即停止轮询
  const [activeRunID, setActiveRunID] = useState<string | null>(null);

  // 项目脚本增删改：editor 非空表示表单打开，script 为 null 表示新建
  const [editor, setEditor] = useState<{ mode: 'create' | 'edit'; script: RemoteProjectScript | null } | null>(
    null
  );
  const [editorSaving, setEditorSaving] = useState(false);
  const [editorError, setEditorError] = useState<string | null>(null);
  const [deleteTarget, setDeleteTarget] = useState<RemoteProjectScript | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [scriptNotice, setScriptNotice] = useState<string | null>(null);
  const [scriptActionError, setScriptActionError] = useState<string | null>(null);

  const reqIdRef = useRef(0);
  const mountedRef = useRef(true);

  const hasScriptsFeature = Boolean(capabilities?.features?.includes('scripts.run'));
  const hasListMethod = Boolean(capabilities?.methods?.includes('scripts.list'));
  const hasRunStatusMethod = Boolean(capabilities?.methods?.includes('scripts.runStatus'));
  const isConnected = connectionState === 'connected';
  // 增删改需要会话所属项目与宿主 scripts.save 方法，缺一就只保留只读列表。
  const canEditScripts = Boolean(projectID) && supportsMethod('scripts.save');

  // 读取脚本列表，使用代际防止慢响应串会话
  const fetchScripts = useCallback(async () => {
    if (!hasScriptsFeature || !hasListMethod) {
      setError('当前宿主未声明脚本能力 (scripts.run / scripts.list)');
      return;
    }

    if (!isConnected) {
      setError(`未连接到 Mac 宿主（当前连接：${connectionMessage || connectionState}）`);
      return;
    }

    const reqId = ++reqIdRef.current;
    setLoading(true);
    setError(null);

    try {
      const next = await listThreadScripts(threadID);
      if (!mountedRef.current || reqId !== reqIdRef.current) return;
      setResult(next);
    } catch (err) {
      if (!mountedRef.current || reqId !== reqIdRef.current) return;
      setError(err instanceof Error ? err.message : '获取脚本列表失败');
    } finally {
      if (mountedRef.current && reqId === reqIdRef.current) {
        setLoading(false);
      }
    }
  }, [
    connectionMessage,
    connectionState,
    hasListMethod,
    hasScriptsFeature,
    isConnected,
    listThreadScripts,
    threadID
  ]);

  const applyFavorite = useCallback((name: string, isFavorite: boolean) => {
    setResult((prev) => prev && {
      ...prev,
      packageScripts: prev.packageScripts.map((script) =>
        script.name === name ? { ...script, isFavorite } : script
      )
    });
  }, []);

  // 先乐观更新，再以服务端返回的 favorites 校正；失败只回滚该项
  const handleToggleFavorite = useCallback(async (script: RemotePackageScript) => {
    const nextFavorite = !script.isFavorite;
    setFavoriteError(null);
    applyFavorite(script.name, nextFavorite);

    try {
      const { favorites } = await setThreadScriptFavorite(threadID, script.name, nextFavorite);
      if (!mountedRef.current) return;
      const favoriteSet = new Set(favorites);
      setResult((prev) => prev && {
        ...prev,
        packageScripts: prev.packageScripts.map((item) => ({
          ...item,
          isFavorite: favoriteSet.has(item.name)
        }))
      });
    } catch (err) {
      if (!mountedRef.current) return;
      applyFavorite(script.name, script.isFavorite);
      setFavoriteError(err instanceof Error ? err.message : '更新收藏失败');
    }
  }, [applyFavorite, setThreadScriptFavorite, threadID]);

  // 触发运行后立即进入“运行中”，后续状态由轮询填充；运行中禁用所有行避免重复触发
  const handleRun = useCallback(async (key: string, target: RemoteScriptsRunTarget, label: string) => {
    if (runningKey) return;
    setActiveRunID(null);
    setRunError(null);
    setRunNotice(null);
    setRunStatus(null);
    setRunLabel(label);
    setRunningKey(key);

    try {
      const { runID } = await runThreadScript(threadID, target);
      if (!mountedRef.current) return;
      if (!hasRunStatusMethod || !runID) {
        setRunNotice(RUN_STATUS_UNSUPPORTED_NOTICE);
        setRunningKey(null);
        return;
      }
      setActiveRunID(runID);
    } catch (err) {
      if (!mountedRef.current) return;
      setRunError(err instanceof Error ? err.message : '运行脚本失败');
      setRunningKey(null);
    }
  }, [hasRunStatusMethod, runThreadScript, runningKey, threadID]);

  const clearRunResult = useCallback(() => {
    setActiveRunID(null);
    setRunningKey(null);
    setRunLabel(null);
    setRunStatus(null);
    setRunNotice(null);
    setRunError(null);
  }, []);

  // 列表项转成 scripts.save 入参：symbol 统一发字符串，空串即清除图标。
  const toSaveScript = useCallback(
    (script: RemoteProjectScript): RemoteScriptsSaveScript => ({
      id: script.id,
      name: script.name,
      command: script.command ?? '',
      symbol: script.symbol ?? '',
      runOnWorktreeCreate: Boolean(script.runOnWorktreeCreate)
    }),
    []
  );

  // 保存成功只合并脚本列表与 revision：宿主 scripts.save 不回 directory/runner/packageScripts。
  const applySavedScripts = useCallback((saved: RemoteScriptsListResult) => {
    setResult(
      (prev) =>
        prev && {
          ...prev,
          projectScripts: saved.projectScripts,
          ...(saved.revision ? { revision: saved.revision } : {})
        }
    );
  }, []);

  const openCreateScript = useCallback(() => {
    setEditorError(null);
    setScriptActionError(null);
    setScriptNotice(null);
    setEditor({ mode: 'create', script: null });
  }, []);

  const openEditScript = useCallback((script: RemoteProjectScript) => {
    setEditorError(null);
    setScriptActionError(null);
    setScriptNotice(null);
    setEditor({ mode: 'edit', script });
  }, []);

  // 表单提交：整表替换，成功后关表单；版本冲突则提示并重新拉取最新列表。
  const handleEditorSubmit = useCallback(async (draft: ScriptDraft) => {
    if (!projectID || !result || editorSaving) return;
    setEditorSaving(true);
    setEditorError(null);
    setScriptNotice(null);
    setScriptActionError(null);

    const others = result.projectScripts.filter((script) => script.id !== draft.id);
    const next = others.map(toSaveScript);
    const payload: RemoteScriptsSaveScript = {
      name: draft.name,
      command: draft.command,
      symbol: draft.symbol,
      runOnWorktreeCreate: draft.runOnWorktreeCreate,
      ...(draft.id ? { id: draft.id } : {})
    };
    if (draft.id) {
      // 编辑保持原位置，避免保存后列表跳动
      const index = result.projectScripts.findIndex((script) => script.id === draft.id);
      next.splice(index < 0 ? next.length : index, 0, payload);
    } else {
      next.push(payload);
    }

    try {
      const saved = await saveScripts(projectID, next, result.revision);
      if (!mountedRef.current) return;
      applySavedScripts(saved);
      setEditor(null);
      setScriptNotice(
        draft.id ? `已保存脚本 ${draft.name}` : `已新增脚本 ${draft.name}（共 ${next.length} 个）`
      );
    } catch (err) {
      if (!mountedRef.current) return;
      if (err instanceof ScriptsSaveConflictError) {
        setEditor(null);
        setScriptNotice('脚本已在别处被修改，已重新载入最新列表');
        await fetchScripts();
      } else {
        setEditorError(err instanceof Error ? err.message : '保存脚本失败');
      }
    } finally {
      if (mountedRef.current) setEditorSaving(false);
    }
  }, [applySavedScripts, editorSaving, fetchScripts, projectID, result, saveScripts, toSaveScript]);

  // 删除：同样整表替换；冲突时关掉确认框并刷新。
  const handleDeleteConfirm = useCallback(async () => {
    if (!projectID || !result || !deleteTarget || deleting) return;
    setDeleting(true);
    setScriptActionError(null);
    setScriptNotice(null);
    const removed = deleteTarget;
    const next = result.projectScripts
      .filter((script) => script.id !== removed.id)
      .map(toSaveScript);

    try {
      const saved = await saveScripts(projectID, next, result.revision);
      if (!mountedRef.current) return;
      applySavedScripts(saved);
      setDeleteTarget(null);
      setScriptNotice(`已删除脚本 ${removed.name}`);
    } catch (err) {
      if (!mountedRef.current) return;
      if (err instanceof ScriptsSaveConflictError) {
        setDeleteTarget(null);
        setScriptNotice('脚本已在别处被修改，已重新载入最新列表');
        await fetchScripts();
      } else {
        setScriptActionError(err instanceof Error ? err.message : '删除脚本失败');
      }
    } finally {
      if (mountedRef.current) setDeleting(false);
    }
  }, [applySavedScripts, deleting, deleteTarget, fetchScripts, projectID, result, saveScripts, toSaveScript]);

  // 轮询运行状态：切线程、关闭面板、组件卸载或再次运行时由清理函数取消，避免旧结果回写
  useEffect(() => {
    const runID = activeRunID;
    if (!runID) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const tick = async (activeID: string) => {
      timer = null;
      try {
        const next = await getThreadScriptRunStatus(threadID, activeID);
        if (cancelled || !mountedRef.current) return;
        setRunStatus(next);
        if (next.status === 'running') {
          timer = setTimeout(() => void tick(activeID), RUN_STATUS_POLL_INTERVAL_MS);
          return;
        }
        setRunningKey(null);
        setActiveRunID(null);
      } catch (err) {
        if (cancelled || !mountedRef.current) return;
        setRunError(err instanceof Error ? err.message : '获取脚本运行结果失败');
        setRunningKey(null);
        setActiveRunID(null);
      }
    };

    void tick(runID);

    return () => {
      cancelled = true;
      if (timer !== null) clearTimeout(timer);
    };
  }, [activeRunID, getThreadScriptRunStatus, threadID]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
    };
  }, []);

  // 切换会话时清掉上一会话的运行结果与脚本编辑状态，避免串台
  useEffect(() => {
    setActiveRunID(null);
    setRunningKey(null);
    setRunError(null);
    setRunLabel(null);
    setRunStatus(null);
    setRunNotice(null);
    setEditor(null);
    setEditorSaving(false);
    setEditorError(null);
    setDeleteTarget(null);
    setDeleting(false);
    setScriptNotice(null);
    setScriptActionError(null);
  }, [threadID]);

  // 挂载即拉取；卸载由 mountedRef 与请求代际作废在途响应
  useEffect(() => {
    void fetchScripts();
  }, [fetchScripts]);

  // 标题栏刷新按钮由内容体上报，loading 变化时重建以驱动图标旋转
  useEffect(() => {
    setTrailing?.(
      <button
        type="button"
        onClick={() => void fetchScripts()}
        disabled={loading}
        aria-label="刷新脚本列表"
        title="刷新脚本列表"
        className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
      >
        <ArrowPathIcon className={`h-4 w-4 ${loading ? 'animate-spin text-[var(--primary)]' : ''}`} />
      </button>
    );
    return () => setTrailing?.(null);
  }, [fetchScripts, loading, setTrailing]);

  const packageScripts = result?.packageScripts ?? [];
  const projectScripts = result?.projectScripts ?? [];
  const favoriteScripts = packageScripts.filter((script) => script.isFavorite);
  const otherScripts = packageScripts.filter((script) => !script.isFavorite);
  const hasFavorites = favoriteScripts.length > 0;
  const isEmpty = result !== null && packageScripts.length === 0 && projectScripts.length === 0;

  const activeRunLabel = runStatus?.label || runLabel;
  const runTone = runToneOf(runStatus, runNotice);
  const runStatusText = runStatusTextOf(runStatus, runNotice);

  const renderPackageScript = (script: RemotePackageScript) => {
    const key = `package:${script.name}`;
    return (
      <div
        key={script.name}
        className="flex items-center gap-1 pl-1 pr-0.5 rounded-lg border border-[var(--border)] bg-[var(--card)]"
      >
        <button
          type="button"
          onClick={() => void handleRun(key, { kind: 'package', name: script.name }, script.name)}
          disabled={runningKey !== null}
          aria-label={`运行脚本 ${script.name}`}
          className="flex flex-1 min-w-0 min-h-[44px] items-center gap-2 py-2 px-1.5 text-left disabled:opacity-60"
        >
          {runningKey === key ? (
            <span className="loading-spinner loading-spinner--small shrink-0" />
          ) : (
            <PlayIcon className="w-3.5 h-3.5 shrink-0 text-[var(--primary)]" />
          )}
          <span className="min-w-0 flex-1">
            <span className="block select-text font-mono text-xs font-bold text-[var(--foreground)] truncate">
              {script.name}
            </span>
            <span className="block select-text font-mono text-mc-caption text-[var(--foreground-muted)] truncate">
              {script.command}
            </span>
          </span>
        </button>
        <button
          type="button"
          onClick={() => void handleToggleFavorite(script)}
          aria-label={script.isFavorite ? `取消收藏 ${script.name}` : `收藏 ${script.name}`}
          aria-pressed={script.isFavorite}
          title={script.isFavorite ? '取消收藏' : '收藏'}
          className="shrink-0 min-h-[44px] min-w-[44px] inline-flex items-center justify-center rounded-lg transition"
        >
          <StarIcon
            className={`w-4 h-4 ${
              script.isFavorite ? 'fill-current text-amber-500' : 'text-[var(--foreground-muted)]'
            }`}
          />
        </button>
      </div>
    );
  };

  const renderProjectScript = (script: RemoteProjectScript) => {
    const key = `project:${script.id}`;
    return (
      <div
        key={script.id}
        className="flex items-center gap-1 pl-1 pr-0.5 rounded-lg border border-[var(--border)] bg-[var(--card)]"
      >
        <button
          type="button"
          onClick={() => void handleRun(key, { kind: 'project', scriptID: script.id }, script.name)}
          disabled={runningKey !== null}
          aria-label={`运行项目脚本 ${script.name}`}
          className="flex flex-1 min-w-0 min-h-[44px] items-center gap-2 py-2 px-1.5 text-left disabled:opacity-60"
        >
          {runningKey === key ? (
            <span className="loading-spinner loading-spinner--small shrink-0" />
          ) : (
            <PlayIcon className="w-3.5 h-3.5 shrink-0 text-[var(--primary)]" />
          )}
          <span className="min-w-0 flex-1">
            <span className="block select-text font-mono text-xs font-bold text-[var(--foreground)] truncate">
              {script.name}
            </span>
            {script.command ? (
              <span className="block select-text font-mono text-mc-caption text-[var(--foreground-muted)] truncate">
                {script.command}
              </span>
            ) : null}
          </span>
          {script.runOnWorktreeCreate ? (
            <span className="shrink-0 rounded-full bg-[var(--border)] px-1.5 py-0.5 text-mc-caption font-medium text-[var(--foreground-muted)]">
              自动
            </span>
          ) : null}
        </button>
        {canEditScripts && (
          <div className="flex shrink-0 items-center">
            <button
              type="button"
              onClick={() => openEditScript(script)}
              aria-label={`编辑项目脚本 ${script.name}`}
              className="min-h-[44px] px-2 text-mc-caption font-semibold text-[var(--primary)]"
            >
              编辑
            </button>
            <button
              type="button"
              onClick={() => {
                setScriptActionError(null);
                setScriptNotice(null);
                setDeleteTarget(script);
              }}
              aria-label={`删除项目脚本 ${script.name}`}
              className="min-h-[44px] px-2 text-mc-caption font-semibold text-[var(--danger-foreground)]"
            >
              删除
            </button>
          </div>
        )}
      </div>
    );
  };

  return (
    <div className="flex flex-col flex-1 min-h-0 min-w-0">
        {/* 主体滚动展示区 */}
        <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 max-w-full w-full pr-0.5 space-y-3">
          {runError && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{runError}</span>
              </div>
            </div>
          )}

          {/* 运行结果：状态从轮询回显，不依赖会话时间线 */}
          {activeRunLabel && (
            <section
              className={`script-run script-run--${runTone}`}
              aria-label="脚本运行结果"
              aria-live="polite"
            >
              <div className="script-run__head">
                <span className="script-run__icon" aria-hidden="true">
                  {runTone === 'running' ? (
                    <span className="loading-spinner loading-spinner--small" />
                  ) : runTone === 'success' ? (
                    <CheckCircleIcon className="w-4 h-4" />
                  ) : (
                    <ExclamationTriangleIcon className="w-4 h-4" />
                  )}
                </span>
                <span className="script-run__name select-text">{activeRunLabel}</span>
                <button
                  type="button"
                  onClick={clearRunResult}
                  className="script-run__clear min-h-[32px]"
                  aria-label="清除运行结果"
                >
                  清除
                </button>
              </div>
              <p className="script-run__status select-text">{runStatusText}</p>
              {runStatus && runStatus.output ? (
                <pre className="script-run__output select-text">{runStatus.output}</pre>
              ) : null}
            </section>
          )}

          {/* 列表读取错误及单独重试 */}
          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
              <button
                type="button"
                onClick={() => void fetchScripts()}
                disabled={loading}
                className="inline-alert__action min-h-[44px] px-2 flex items-center"
              >
                重试
              </button>
            </div>
          )}

          {/* 刷新现有数据时的加载提示条 */}
          {loading && result && (
            <div className="flex items-center gap-2 py-1.5 px-3 rounded-lg bg-[var(--border)]/40 text-xs text-[var(--foreground-muted)]">
              <span className="loading-spinner loading-spinner--small shrink-0" />
              <span>正在刷新脚本列表...</span>
            </div>
          )}

          {/* 初次拉取中的空态加载器 */}
          {loading && !result && (
            <div className="empty-state">
              <span className="loading-spinner mb-1" />
              <h2>正在读取脚本列表</h2>
              <p className="text-xs text-[var(--foreground-muted)]">
                正在与 Mac 宿主通信获取脚本...
              </p>
            </div>
          )}

          {/* 无数据且非加载中的空状态 */}
          {!loading && !result && !error && (
            <div className="empty-state">
              <h2>暂无脚本数据</h2>
              <button
                type="button"
                onClick={() => void fetchScripts()}
                className="primary-button min-h-[44px]"
              >
                读取脚本
              </button>
            </div>
          )}

          {isEmpty && (
            <div className="empty-state">
              <h2>暂无脚本</h2>
              <p className="text-xs text-[var(--foreground-muted)] leading-relaxed">
                当前目录没有 package.json scripts，也没有项目脚本
              </p>
            </div>
          )}

          {result && !isEmpty && (
            <>
              {/* package.json 分组：有收藏时只平铺收藏项，其余收进折叠组 */}
              <section className="space-y-1.5" aria-label="package.json 脚本">
                <div className="flex items-center justify-between gap-2 text-xs text-[var(--foreground-muted)] px-0.5">
                  <span className="font-semibold text-[var(--foreground)]">package.json</span>
                  {result.runner && (
                    <span className="px-1.5 py-0.5 rounded-full text-mc-caption bg-[var(--border)] text-[var(--foreground-muted)] font-mono font-medium">
                      {result.runner}
                    </span>
                  )}
                </div>

                {favoriteError && (
                  <div className="inline-alert inline-alert--danger" role="alert">
                    <div className="flex items-start gap-2 min-w-0 flex-1">
                      <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                      <span className="break-words select-text">{favoriteError}</span>
                    </div>
                  </div>
                )}

                {packageScripts.length === 0 ? (
                  <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                    package.json 中没有 scripts
                  </div>
                ) : (
                  <>
                    <div className="space-y-1">
                      {(hasFavorites ? favoriteScripts : packageScripts).map(renderPackageScript)}
                    </div>

                    {hasFavorites && otherScripts.length > 0 && (
                      <>
                        <button
                          type="button"
                          onClick={() => setGroupExpanded((open) => !open)}
                          aria-expanded={groupExpanded}
                          aria-controls="scripts-all-group"
                          className="w-full min-h-[44px] flex items-center justify-between gap-2 px-3 rounded-lg border border-[var(--border)] bg-[var(--screen)] text-xs font-semibold text-[var(--foreground-muted)] hover:text-[var(--foreground)] transition"
                        >
                          <span>全部脚本 ({otherScripts.length})</span>
                          {groupExpanded ? (
                            <ChevronDownIcon className="w-3.5 h-3.5" />
                          ) : (
                            <ChevronRightIcon className="w-3.5 h-3.5" />
                          )}
                        </button>
                        <div id="scripts-all-group" hidden={!groupExpanded} className="space-y-1">
                          {otherScripts.map(renderPackageScript)}
                        </div>
                      </>
                    )}
                  </>
                )}
              </section>

              {/* 项目脚本分组 */}
              <section className="space-y-1.5" aria-label="项目脚本">
                <div className="flex items-center justify-between gap-2 text-xs text-[var(--foreground-muted)] px-0.5">
                  <span className="font-semibold text-[var(--foreground)]">
                    项目脚本 ({projectScripts.length})
                  </span>
                  {canEditScripts && (
                    <button
                      type="button"
                      onClick={openCreateScript}
                      aria-label="新增项目脚本"
                      className="inline-flex min-h-[36px] shrink-0 items-center gap-1 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2 text-mc-caption font-semibold text-[var(--primary)]"
                    >
                      <PlusIcon className="w-3.5 h-3.5" />
                      <span>新增</span>
                    </button>
                  )}
                </div>

                {scriptNotice && (
                  <div
                    className="flex items-start gap-2 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 py-2 text-mc-caption text-[var(--foreground-muted)]"
                    role="status"
                  >
                    <CheckCircleIcon className="w-3.5 h-3.5 shrink-0 mt-0.5 text-[var(--primary)]" />
                    <span className="min-w-0 flex-1 break-words select-text">{scriptNotice}</span>
                    <button
                      type="button"
                      onClick={() => setScriptNotice(null)}
                      className="shrink-0 min-h-[32px] px-1 font-semibold text-[var(--primary)]"
                    >
                      关闭
                    </button>
                  </div>
                )}

                {scriptActionError && (
                  <div className="inline-alert inline-alert--danger" role="alert">
                    <div className="flex items-start gap-2 min-w-0 flex-1">
                      <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                      <span className="break-words select-text">{scriptActionError}</span>
                    </div>
                  </div>
                )}
                {projectScripts.length === 0 ? (
                  <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
                    {canEditScripts ? '暂无项目脚本，可点「新增」添加' : '暂无项目脚本'}
                  </div>
                ) : (
                  <div className="space-y-1">
                    {projectScripts.map(renderProjectScript)}
                  </div>
                )}
              </section>

              {/* 只读提示 */}
              <div className="p-3 rounded-xl border border-[var(--border)] bg-[var(--screen)] text-xs text-[var(--foreground-muted)]">
                脚本在 Mac 端执行，运行结果在本面板内回显，不再写入会话。
              </div>
            </>
          )}
        </div>

        {/* 底部信息与操作栏 */}
        <div className="mt-3 shrink-0 border-t border-[var(--border)] pt-3 pb-0 min-w-0 max-w-full w-full flex items-center justify-between gap-2">
          <div className="text-xs text-[var(--foreground-muted)] truncate min-w-0 flex-1">
            {result ? (
              <span className="select-text font-mono truncate" title={result.directory}>
                {result.directory}
              </span>
            ) : (
              <span>在 Mac 端执行</span>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <button
              type="button"
              onClick={onClose}
              className="primary-button min-h-[44px] min-w-[72px]"
            >
              关闭
            </button>
          </div>
        </div>

      {/* 表单与删除确认：包一层阻止冒泡，避免点到弹层时把抽屉关掉 */}
      {(editor !== null || deleteTarget !== null) && (
        <div onMouseDown={(event) => event.stopPropagation()}>
          {editor !== null && (
            <ScriptEditForm
              key={editor.script ? `edit:${editor.script.id}` : 'create'}
              mode={editor.mode}
              initial={{
                ...(editor.script ? { id: editor.script.id } : {}),
                name: editor.script?.name ?? '',
                command: editor.script?.command ?? '',
                symbol: editor.script?.symbol ?? '',
                runOnWorktreeCreate: Boolean(editor.script?.runOnWorktreeCreate)
              }}
              otherNames={projectScripts
                .filter((script) => script.id !== editor.script?.id)
                .map((script) => script.name)}
              saving={editorSaving}
              error={editorError}
              onSubmit={(draft) => void handleEditorSubmit(draft)}
              onCancel={() => {
                if (editorSaving) return;
                setEditor(null);
                setEditorError(null);
              }}
            />
          )}
          <ConfirmDialog
            isOpen={deleteTarget !== null}
            title="删除项目脚本"
            message={`确定删除「${deleteTarget?.name ?? ''}」吗？删除后会立即写入 Mac 端项目配置，无法撤销。`}
            confirmLabel="删除"
            confirmVariant="danger"
            isLoading={deleting}
            onConfirm={() => void handleDeleteConfirm()}
            onCancel={() => {
              if (deleting) return;
              setDeleteTarget(null);
            }}
          />
        </div>
      )}
    </div>
  );
  }
);

export const ScriptsSheet: React.FC<ScriptsSheetProps> = ({
  isOpen,
  onClose,
  threadID,
  threadTitle,
  projectID
}) => {
  const bodyRef = useRef<ThreadMenuPageHandle | null>(null);
  const [trailing, setTrailing] = useState<React.ReactNode>(null);

  const handleRequestClose = useCallback(() => {
    if (bodyRef.current?.requestClose() === false) return;
    onClose();
  }, [onClose]);

  if (!isOpen) return null;

  return (
    <SheetFrame onRequestClose={handleRequestClose} labelledBy="scripts-sheet-title" size="panel">
      <MenuPageChromeContext.Provider
        value={{
          leading: 'close',
          onLeading: handleRequestClose,
          onClose: handleRequestClose,
          setTrailing
        }}
      >
        <MenuSheetHeader
          eyebrow={threadTitle ? `脚本 · ${threadTitle}` : '工作区脚本'}
          title="运行脚本"
          labelledBy="scripts-sheet-title"
          leading="close"
          onLeading={handleRequestClose}
          onClose={handleRequestClose}
          trailing={trailing}
        />
        <ScriptsSheetBody
          ref={bodyRef}
          onClose={onClose}
          threadID={threadID}
          threadTitle={threadTitle}
          projectID={projectID}
        />
      </MenuPageChromeContext.Provider>
    </SheetFrame>
  );
};
