// 移动端贮藏面板：查看贮藏列表，贮藏当前改动、应用、恢复与删除

import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  applyGitStash,
  dropGitStash,
  fetchGitStashList,
  popGitStash,
  pushGitStash,
  type GitExtCaller,
  type RemoteGitStashEntry
} from '../context/remoteGitApi';
import { ConfirmDialog } from './ConfirmDialog';
import { ArrowDownIcon, ArrowPathIcon, ExclamationTriangleIcon, PlusIcon } from './Icons';

export interface GitStashPanelProps {
  threadID: string;
  call: GitExtCaller;
  /** 贮藏/恢复后工作区会变，通知外层刷新状态。 */
  onWorkspaceChanged: () => void;
  /** 外层刷新令牌，变化时重新读取贮藏列表。 */
  refreshToken?: number;
}

/** 把 RFC3339 时间显示成短格式，解析不到时退回原文。 */
function gitExtFormatTime(value?: string): string {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit'
  });
}

export const GitStashPanel: React.FC<GitStashPanelProps> = ({
  threadID,
  call,
  onWorkspaceChanged,
  refreshToken = 0
}) => {
  const [entries, setEntries] = useState<RemoteGitStashEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busyKey, setBusyKey] = useState<string | null>(null);
  const [pushMessage, setPushMessage] = useState('');
  const [pendingDrop, setPendingDrop] = useState<RemoteGitStashEntry | null>(null);
  const [reloadToken, setReloadToken] = useState(0);

  const reqIdRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
    };
  }, []);

  // 读取贮藏列表
  useEffect(() => {
    const reqId = ++reqIdRef.current;
    setLoading(true);
    setLoadError(null);

    void fetchGitStashList(call, threadID)
      .then((result) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setEntries(result.entries);
      })
      .catch((err: unknown) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setEntries([]);
        setLoadError(err instanceof Error ? err.message : '读取贮藏列表失败');
      })
      .finally(() => {
        if (mountedRef.current && reqId === reqIdRef.current) {
          setLoading(false);
        }
      });
  }, [call, refreshToken, reloadToken, threadID]);

  const reload = useCallback(() => {
    setReloadToken((prev) => prev + 1);
  }, []);

  // 贮藏当前工作区的全部改动，说明可选
  const handlePush = useCallback(async () => {
    if (busyKey !== null) return;
    setBusyKey('push');
    setActionError(null);
    try {
      await pushGitStash(call, threadID, pushMessage.trim() || undefined);
      if (!mountedRef.current) return;
      setPushMessage('');
      reload();
      onWorkspaceChanged();
    } catch (err) {
      if (!mountedRef.current) return;
      setActionError(err instanceof Error ? err.message : '贮藏当前改动失败');
    } finally {
      if (mountedRef.current) {
        setBusyKey(null);
      }
    }
  }, [busyKey, call, onWorkspaceChanged, pushMessage, reload, threadID]);

  // 恢复最近一条贮藏（恢复后该记录从列表移除）
  const handlePop = useCallback(async () => {
    if (busyKey !== null) return;
    setBusyKey('pop');
    setActionError(null);
    try {
      await popGitStash(call, threadID);
      if (!mountedRef.current) return;
      reload();
      onWorkspaceChanged();
    } catch (err) {
      if (!mountedRef.current) return;
      setActionError(err instanceof Error ? err.message : '恢复贮藏失败');
    } finally {
      if (mountedRef.current) {
        setBusyKey(null);
      }
    }
  }, [busyKey, call, onWorkspaceChanged, reload, threadID]);

  // 应用指定序号的贮藏，记录保留
  const handleApply = useCallback(async (entry: RemoteGitStashEntry) => {
    if (busyKey !== null) return;
    setBusyKey(`apply-${entry.index}`);
    setActionError(null);
    try {
      await applyGitStash(call, threadID, entry.index);
      if (!mountedRef.current) return;
      reload();
      onWorkspaceChanged();
    } catch (err) {
      if (!mountedRef.current) return;
      setActionError(err instanceof Error ? err.message : '应用贮藏失败');
    } finally {
      if (mountedRef.current) {
        setBusyKey(null);
      }
    }
  }, [busyKey, call, onWorkspaceChanged, reload, threadID]);

  // 删除指定序号的贮藏记录（确认弹窗里点确认后执行）
  const handleDrop = useCallback(async () => {
    const entry = pendingDrop;
    if (!entry || busyKey !== null) return;
    setBusyKey(`drop-${entry.index}`);
    setActionError(null);
    try {
      await dropGitStash(call, threadID, entry.index);
      if (!mountedRef.current) return;
      setPendingDrop(null);
      reload();
    } catch (err) {
      if (!mountedRef.current) return;
      setActionError(err instanceof Error ? err.message : '删除贮藏记录失败');
    } finally {
      if (mountedRef.current) {
        setBusyKey(null);
      }
    }
  }, [busyKey, call, pendingDrop, reload, threadID]);

  const busy = busyKey !== null;

  return (
    <div className="space-y-3">
      {/* 贮藏当前改动 */}
      <div className="p-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-2">
        <span className="block text-xs font-semibold text-[var(--foreground)]">贮藏当前改动</span>
        <input
          type="text"
          value={pushMessage}
          onChange={(event) => {
            setPushMessage(event.target.value);
            setActionError(null);
          }}
          placeholder="贮藏说明（可选），例如 before rebase"
          disabled={busy}
          className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
        />
        <button
          type="button"
          onClick={() => void handlePush()}
          disabled={busy}
          className="primary-button min-h-[44px] w-full"
        >
          {busyKey === 'push' ? (
            <>
              <span className="loading-spinner loading-spinner--small shrink-0" />
              <span>贮藏中...</span>
            </>
          ) : (
            <>
              <PlusIcon className="w-4 h-4" />
              <span>贮藏当前改动</span>
            </>
          )}
        </button>
        <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
          贮藏会把工作区未提交的改动收起来，工作区回到干净状态，之后可应用或恢复。
        </p>
      </div>

      {actionError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{actionError}</span>
          </div>
        </div>
      )}

      {loadError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{loadError}</span>
          </div>
          <button
            type="button"
            onClick={reload}
            disabled={loading}
            className="inline-alert__action min-h-[44px] px-2 flex items-center"
          >
            重试
          </button>
        </div>
      )}

      {loading && entries.length === 0 && (
        <div className="empty-state">
          <span className="loading-spinner mb-1" />
          <h2>正在读取贮藏列表</h2>
          <p className="text-xs text-[var(--foreground-muted)]">正在与 Mac 宿主通信...</p>
        </div>
      )}

      {!loading && !loadError && entries.length === 0 && (
        <div className="empty-state">
          <h2>暂无贮藏记录</h2>
          <p className="text-xs text-[var(--foreground-muted)]">
            贮藏当前改动后，记录会出现在这里。
          </p>
        </div>
      )}

      {entries.length > 0 && (
        <div className="space-y-1.5">
          <div className="flex items-center justify-between gap-2 px-0.5">
            <span className="text-xs font-semibold text-[var(--foreground)]">
              贮藏记录 ({entries.length})
            </span>
            <button
              type="button"
              onClick={() => void handlePop()}
              disabled={busy}
              className="secondary-button min-h-[44px] px-2.5 text-xs"
              title="恢复最新一条贮藏，并从贮藏列表删除"
            >
              {busyKey === 'pop' ? (
                <span className="loading-spinner loading-spinner--small shrink-0" />
              ) : (
                <ArrowDownIcon className="w-3.5 h-3.5" />
              )}
              <span>恢复最新</span>
            </button>
          </div>

          <ul className="space-y-1">
            {entries.map((entry) => (
              <li
                key={`${entry.index}-${entry.ref}`}
                className="p-2.5 rounded-lg border border-[var(--border)] bg-[var(--card)] space-y-1.5"
              >
                <div className="min-w-0 space-y-0.5">
                  <span className="select-text block text-xs font-semibold text-[var(--foreground)] break-words">
                    {entry.message || entry.ref}
                  </span>
                  <span className="select-text block font-mono text-mc-caption text-[var(--foreground-muted)] break-all">
                    {entry.ref}
                    {entry.branch ? ` · ${entry.branch}` : ''}
                    {entry.createdAt ? ` · ${gitExtFormatTime(entry.createdAt)}` : ''}
                  </span>
                </div>
                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    onClick={() => void handleApply(entry)}
                    disabled={busy}
                    className="secondary-button min-h-[44px] px-2.5 text-xs"
                    title="应用这条贮藏，记录保留"
                  >
                    {busyKey === `apply-${entry.index}` ? (
                      <span className="loading-spinner loading-spinner--small shrink-0" />
                    ) : (
                      <ArrowPathIcon className="w-3.5 h-3.5" />
                    )}
                    <span>应用</span>
                  </button>
                  <button
                    type="button"
                    onClick={() => setPendingDrop(entry)}
                    disabled={busy}
                    className="secondary-button min-h-[44px] px-2.5 text-xs text-[var(--danger-foreground)]"
                    title="删除这条贮藏记录"
                  >
                    <span>删除</span>
                  </button>
                </div>
              </li>
            ))}
          </ul>
        </div>
      )}

      <ConfirmDialog
        isOpen={pendingDrop !== null}
        title="删除贮藏记录"
        message={
          pendingDrop
            ? `确认删除 ${pendingDrop.ref}${pendingDrop.message ? `（${pendingDrop.message}）` : ''}？删除后无法恢复，工作区不受影响。`
            : ''
        }
        confirmLabel="删除"
        confirmVariant="danger"
        isLoading={pendingDrop !== null && busyKey === `drop-${pendingDrop.index}`}
        onConfirm={() => void handleDrop()}
        onCancel={() => setPendingDrop(null)}
      />
    </div>
  );
};
