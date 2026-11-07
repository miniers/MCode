// 目录浏览器：从 Mac 用户主目录开始逐级浏览子目录，确认后把当前目录路径回填给调用方。
// 宿主 projects.listDir 只允许浏览用户主目录内的目录，越界时原样展示宿主错误。
// 外层沿用项目内的 sheet 抽屉写法；本组件通常嵌在另一个 sheet 里，因此用更高层级 + 阻止冒泡。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteCall } from '../context/useRemoteCall';
import { listDir, type RemoteProjectExtListDirResult } from '../context/remoteProjectApi';
import {
  ArrowPathIcon,
  ArrowUpIcon,
  ChevronRightIcon,
  ExclamationTriangleIcon,
  EyeIcon,
  XMarkIcon
} from './Icons';

export interface DirectoryPickerProps {
  isOpen: boolean;
  /** 面板标题，缺省「选择目录」。 */
  title?: string;
  /** 打开时定位到的目录；缺省从用户主目录开始。 */
  initialPath?: string;
  onCancel: () => void;
  /** 确认选择当前目录。 */
  onSelect: (path: string) => void;
}

/** 宿主单次最多返回 500 个子目录，超出时只提示不翻页。 */
const DIR_LIMIT_NOTICE = '仅显示前 500 项';

export const DirectoryPicker: React.FC<DirectoryPickerProps> = ({
  isOpen,
  title = '选择目录',
  initialPath,
  onCancel,
  onSelect
}) => {
  const { call } = useRemoteCall();
  const [data, setData] = useState<RemoteProjectExtListDirResult | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // 待读取的目录；undefined 表示让宿主从用户主目录开始。
  const [requestPath, setRequestPath] = useState<string | undefined>(undefined);
  // 刷新令牌：路径不变时也能重新读取当前目录。
  const [reloadToken, setReloadToken] = useState(0);
  // 目录连续切换时只有最后一次请求的结果能落到界面上。
  const requestRef = useRef(0);

  // 每次打开都回到 initialPath（或主目录），避免上次浏览位置串场。
  useEffect(() => {
    if (!isOpen) return;
    requestRef.current += 1;
    setRequestPath(initialPath?.trim() ? initialPath.trim() : undefined);
    setData(null);
    setShowHidden(false);
    setError(null);
    setLoading(false);
  }, [initialPath, isOpen]);

  useEffect(() => {
    if (!isOpen) return;
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;
    setLoading(true);
    setError(null);
    void listDir(call, {
      ...(requestPath ? { path: requestPath } : {}),
      ...(showHidden ? { showHidden: true } : {})
    })
      .then((next) => {
        if (requestRef.current !== requestId) return;
        setData(next);
      })
      .catch((err: unknown) => {
        if (requestRef.current !== requestId) return;
        setError(err instanceof Error ? err.message : '读取目录失败');
      })
      .finally(() => {
        if (requestRef.current !== requestId) return;
        setLoading(false);
      });
  }, [call, isOpen, reloadToken, requestPath, showHidden]);

  useEffect(() => {
    if (!isOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      onCancel();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [isOpen, onCancel]);

  // 进入子目录或返回上级：清掉旧列表，避免在加载失败时误选上一个目录。
  const navigateTo = useCallback((next: string | undefined) => {
    requestRef.current += 1;
    setRequestPath(next);
    setData(null);
    setError(null);
  }, []);

  const reload = useCallback(() => {
    requestRef.current += 1;
    setData(null);
    setError(null);
    setReloadToken((token) => token + 1);
  }, []);

  if (!isOpen) return null;

  const currentPath = data?.path ?? requestPath ?? '';
  const canConfirm = data !== null && !loading && error === null;

  return (
    <div
      className="fixed inset-0 z-[60] flex items-end justify-center bg-black/40"
      role="presentation"
      onMouseDown={(event) => {
        // 阻止冒泡到外层 sheet 的遮罩，否则会连外层面板一起关掉。
        event.stopPropagation();
        onCancel();
      }}
    >
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="directory-picker-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="mb-3 flex w-full min-w-0 shrink-0 items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">目录</p>
            <h2 id="directory-picker-title" className="sheet-title mt-0.5">
              {title}
            </h2>
          </div>
          <button
            type="button"
            onClick={onCancel}
            aria-label="关闭目录选择"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="w-full min-w-0 shrink-0 space-y-2">
          <p className="font-mono text-mc-caption leading-relaxed break-all select-text text-[var(--foreground-muted)]">
            {currentPath || '用户主目录'}
          </p>

          <div className="flex items-center gap-1.5">
            <button
              type="button"
              onClick={() => navigateTo(data?.parent)}
              disabled={!data?.parent || loading}
              aria-label="返回上级目录"
              className="secondary-button min-h-[44px] gap-1.5 px-2.5 text-xs"
            >
              <ArrowUpIcon className="h-3.5 w-3.5" />
              <span>上级</span>
            </button>
            <button
              type="button"
              onClick={() => setShowHidden((hidden) => !hidden)}
              aria-pressed={showHidden}
              aria-label="显示隐藏目录"
              className={`secondary-button min-h-[44px] gap-1.5 px-2.5 text-xs ${
                showHidden ? 'border-[var(--primary)] text-[var(--primary)]' : ''
              }`}
            >
              <EyeIcon className="h-3.5 w-3.5" />
              <span>隐藏目录</span>
            </button>
            <button
              type="button"
              onClick={reload}
              disabled={loading}
              aria-label="刷新目录"
              title="刷新目录"
              className="icon-button ml-auto shrink-0 min-h-[44px] min-w-[44px]"
            >
              <ArrowPathIcon className={`h-4 w-4 ${loading ? 'animate-spin text-[var(--primary)]' : ''}`} />
            </button>
          </div>
        </div>

        <div className="mt-3 min-h-0 w-full min-w-0 flex-1 space-y-1 overflow-y-auto pr-0.5">
          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0 flex-1">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
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

          {loading && !data && (
            <div className="flex items-center gap-2 px-3 py-2 text-xs text-[var(--foreground-muted)]">
              <span className="loading-spinner loading-spinner--small shrink-0" />
              <span>正在读取目录...</span>
            </div>
          )}

          {data && data.entries.length === 0 && (
            <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)] text-center">
              这个目录下没有子目录
            </div>
          )}

          {data?.entries.map((entry) => (
            <button
              key={entry.path}
              type="button"
              onClick={() => navigateTo(entry.path)}
              aria-label={`进入目录 ${entry.name}`}
              className="flex w-full min-h-[44px] min-w-0 items-center gap-2 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 py-2 text-left"
            >
              <ChevronRightIcon className="h-3.5 w-3.5 shrink-0 text-[var(--foreground-muted)]" />
              <span className="min-w-0 flex-1 select-text truncate font-mono text-xs font-semibold text-[var(--foreground)]">
                {entry.name}
              </span>
              {entry.isGitRepo && (
                <span className="shrink-0 rounded-full bg-[var(--border)] px-1.5 py-0.5 text-mc-caption font-semibold text-[var(--foreground-muted)]">
                  Git 仓库
                </span>
              )}
            </button>
          ))}

          {data?.truncated && (
            <p className="px-1 pt-1 text-mc-caption text-[var(--foreground-muted)]">{DIR_LIMIT_NOTICE}</p>
          )}
        </div>

        <div className="mt-3 flex w-full min-w-0 shrink-0 items-center justify-end gap-2 border-t border-[var(--border)] pt-3">
          <button type="button" onClick={onCancel} className="secondary-button min-h-[44px]">
            取消
          </button>
          <button
            type="button"
            onClick={() => {
              if (!data) return;
              onSelect(data.path);
            }}
            disabled={!canConfirm}
            className="primary-button min-h-[44px]"
          >
            选择此目录
          </button>
        </div>
      </section>
    </div>
  );
};
