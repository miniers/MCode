// 添加项目面板：两种方式——填 Mac 上的绝对路径（防抖 400ms 调 projects.validatePath 实时提示，
// 也可打开目录浏览器选择），或在宿主上 git clone 一个仓库并登记成项目。
// 成功后统一走 onAdded 回调；宿主未声明 projects.listDir / projects.clone 时隐藏对应入口。
// 外层结构沿用项目内 settings-sheet 抽屉（sheet-layer + sheet-handle + 标题栏 + 主体 + 底部操作）。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useRemoteCall } from '../context/useRemoteCall';
import {
  addProject,
  cloneProject,
  PROJECTS_CLONE_METHOD,
  PROJECTS_LIST_DIR_METHOD,
  validateProjectPath,
  type RemoteProjectExtSummary,
  type RemoteProjectExtValidateResult
} from '../context/remoteProjectApi';
import { DirectoryPicker } from './DirectoryPicker';
import { ExclamationTriangleIcon, PlusIcon, XMarkIcon } from './Icons';

export interface AddProjectSheetProps {
  onClose: () => void;
  /** 项目登记成功后回调：调用方负责刷新项目列表并跳转。 */
  onAdded: (project: RemoteProjectExtSummary) => void;
}

/** 输入防抖等待时间（毫秒）。 */
const VALIDATE_DEBOUNCE_MS = 400;

type AddMode = 'local' | 'clone';

/** 目录浏览器的回填目标：本机路径输入框，或克隆的存放目录。 */
type BrowseTarget = 'local' | 'clone-parent';

/**
 * 前端做与宿主同口径的基本校验：只接受 https:// 与 git@host:path 两种形式。
 * 返回空串表示合法，否则是可直接展示的中文原因。
 */
function validateGitURL(raw: string): string {
  const value = raw.trim();
  if (!value) return '请填写仓库地址';
  if (/\s/.test(value)) return '仓库地址不能包含空白字符';
  if (value.startsWith('https://')) {
    return value.length > 'https://'.length ? '' : '克隆地址缺少主机与仓库路径';
  }
  if (value.startsWith('git@')) {
    const rest = value.slice('git@'.length);
    const colon = rest.indexOf(':');
    if (colon <= 0 || colon === rest.length - 1) {
      return 'git@ 地址必须写成 git@host:owner/repo.git';
    }
    return '';
  }
  return '只支持 https:// 或 git@ 形式的 Git 地址';
}

/** 校验显示名：留空表示由宿主从地址推导；非空时只允许字母、数字和 . _ - +。 */
function validateCloneName(raw: string): string {
  const value = raw.trim();
  if (!value) return '';
  if (value === '.' || value === '..') return '显示名不合法';
  if (!/^[A-Za-z0-9._+-]+$/.test(value)) return '显示名只能包含字母、数字和 . _ - +';
  return '';
}

export const AddProjectSheet: React.FC<AddProjectSheetProps> = ({ onClose, onAdded }) => {
  const { call, supportsMethod } = useRemoteCall();
  const [mode, setMode] = useState<AddMode>('local');

  // 本机文件夹：手填路径 + 防抖校验
  const [path, setPath] = useState('');
  const [validation, setValidation] = useState<RemoteProjectExtValidateResult | null>(null);
  const [checking, setChecking] = useState(false);

  // 从 Git 克隆
  const [url, setUrl] = useState('');
  const [parentPath, setParentPath] = useState('');
  const [name, setName] = useState('');
  const [cloning, setCloning] = useState(false);

  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [browseTarget, setBrowseTarget] = useState<BrowseTarget | null>(null);

  // 路径连续变化时，只有最后一次请求的结果能落到界面上。
  const requestRef = useRef(0);
  const closeRef = useRef<HTMLButtonElement | null>(null);
  // Escape 与遮罩点击的处理函数只订阅一次，用 ref 读取最新状态。
  const busyRef = useRef(false);
  const browseRef = useRef(false);

  const canClone = supportsMethod(PROJECTS_CLONE_METHOD);
  const canBrowse = supportsMethod(PROJECTS_LIST_DIR_METHOD);
  const busy = adding || cloning;
  busyRef.current = busy;
  browseRef.current = browseTarget !== null;

  const requestClose = useCallback(() => {
    if (busyRef.current) return;
    onClose();
  }, [onClose]);

  useEffect(() => {
    closeRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      // 目录浏览器是叠在本面板上的独立弹层，Esc 由它自己处理。
      if (browseRef.current) return;
      event.stopPropagation();
      requestClose();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [requestClose]);

  useEffect(() => {
    if (mode !== 'local') {
      setChecking(false);
      return;
    }
    const trimmed = path.trim();
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;
    if (!trimmed) {
      setValidation(null);
      setChecking(false);
      setError(null);
      return;
    }
    let cancelled = false;
    setChecking(true);
    setError(null);
    const timer = window.setTimeout(() => {
      void validateProjectPath(call, trimmed)
        .then((result) => {
          if (cancelled || requestRef.current !== requestId) return;
          setValidation(result);
        })
        .catch((err: unknown) => {
          if (cancelled || requestRef.current !== requestId) return;
          setValidation(null);
          setError(err instanceof Error ? err.message : '校验路径失败');
        })
        .finally(() => {
          if (cancelled || requestRef.current !== requestId) return;
          setChecking(false);
        });
    }, VALIDATE_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [call, mode, path]);

  const canAdd = mode === 'local' && validation?.ok === true && !checking && !busy;
  const urlError = url.trim() ? validateGitURL(url) : '';
  const nameError = validateCloneName(name);
  const canSubmitClone = canClone && !busy && url.trim() !== '' && urlError === '' && nameError === '';

  const handleAddLocal = useCallback(async () => {
    if (!canAdd) return;
    setAdding(true);
    setError(null);
    try {
      const project = await addProject(call, path.trim());
      onAdded(project);
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : '添加项目失败');
    } finally {
      setAdding(false);
    }
  }, [call, canAdd, onAdded, onClose, path]);

  const handleClone = useCallback(async () => {
    if (!canSubmitClone) return;
    setCloning(true);
    setError(null);
    try {
      const project = await cloneProject(call, {
        url: url.trim(),
        ...(parentPath.trim() ? { parentPath: parentPath.trim() } : {}),
        ...(name.trim() ? { name: name.trim() } : {})
      });
      onAdded(project);
      onClose();
    } catch (err) {
      // 宿主回的是中文原因（地址不合法、目录已存在、git 报错首行），原样展示。
      setError(err instanceof Error ? err.message : '克隆项目失败');
    } finally {
      setCloning(false);
    }
  }, [canSubmitClone, call, name, onAdded, onClose, parentPath, url]);

  const handleBrowseSelect = useCallback((picked: string) => {
    if (browseTarget === 'clone-parent') setParentPath(picked);
    else setPath(picked);
    setBrowseTarget(null);
  }, [browseTarget]);

  const localHint = checking
    ? '正在检查路径...'
    : validation?.ok
      ? `将添加：${validation.normalizedPath}`
      : (validation?.reason ?? '');

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={requestClose}>
      <section
        className="settings-sheet flex flex-col max-h-[92dvh] outline-none w-full min-w-0 overflow-hidden"
        role="dialog"
        aria-modal="true"
        aria-labelledby="add-project-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="mb-3 flex w-full min-w-0 shrink-0 items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">项目</p>
            <h2 id="add-project-title" className="sheet-title mt-0.5">
              添加项目
            </h2>
          </div>
          <button
            ref={closeRef}
            type="button"
            onClick={requestClose}
            disabled={busy}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div
          role="tablist"
          aria-label="添加方式"
          className="mb-3 flex w-full min-w-0 shrink-0 gap-1.5 rounded-xl border border-[var(--border)] bg-[var(--screen)] p-1"
        >
          <button
            type="button"
            role="tab"
            aria-selected={mode === 'local'}
            onClick={() => {
              setMode('local');
              setError(null);
            }}
            disabled={busy}
            className={`min-h-[40px] flex-1 rounded-lg px-2 text-xs font-semibold transition ${
              mode === 'local'
                ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                : 'text-[var(--foreground-muted)]'
            }`}
          >
            本机文件夹
          </button>
          {canClone && (
            <button
              type="button"
              role="tab"
              aria-selected={mode === 'clone'}
              onClick={() => {
                setMode('clone');
                setError(null);
              }}
              disabled={busy}
              className={`min-h-[40px] flex-1 rounded-lg px-2 text-xs font-semibold transition ${
                mode === 'clone'
                  ? 'bg-[var(--card)] text-[var(--foreground)] shadow-sm'
                  : 'text-[var(--foreground-muted)]'
              }`}
            >
              从 Git 克隆
            </button>
          )}
        </div>

        <div className="min-h-0 w-full min-w-0 flex-1 space-y-2 overflow-y-auto pr-0.5">
          {mode === 'local' ? (
            <>
              <label htmlFor="add-project-path" className="block text-xs font-semibold text-[var(--foreground)]">
                Mac 上的目录路径
              </label>
              <div className="flex w-full min-w-0 items-center gap-1.5">
                <input
                  id="add-project-path"
                  type="text"
                  value={path}
                  autoComplete="off"
                  spellCheck={false}
                  inputMode="text"
                  onChange={(event) => setPath(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key !== 'Enter') return;
                    event.preventDefault();
                    void handleAddLocal();
                  }}
                  placeholder="~/code/my-app"
                  disabled={busy}
                  aria-invalid={validation?.ok === false}
                  aria-describedby="add-project-hint"
                  className="min-w-0 flex-1 min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs font-mono text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
                />
                {canBrowse && (
                  <button
                    type="button"
                    onClick={() => setBrowseTarget('local')}
                    disabled={busy}
                    className="secondary-button shrink-0 min-h-[44px] px-2.5 text-xs"
                  >
                    浏览…
                  </button>
                )}
              </div>

              <p
                id="add-project-hint"
                role="status"
                aria-live="polite"
                className={`min-h-[16px] text-mc-caption leading-relaxed break-words ${
                  validation?.ok
                    ? 'text-[var(--foreground-muted)]'
                    : validation
                      ? 'text-[var(--danger-foreground)]'
                      : 'text-[var(--foreground-muted)]'
                }`}
              >
                {localHint}
              </p>

              <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
                只能添加 Mac 上已存在的目录；路径为绝对路径，开头的 ~ 会展开为当前用户主目录。
              </p>
            </>
          ) : (
            <>
              <label htmlFor="add-project-url" className="block text-xs font-semibold text-[var(--foreground)]">
                仓库地址
              </label>
              <input
                id="add-project-url"
                type="text"
                value={url}
                autoComplete="off"
                spellCheck={false}
                inputMode="url"
                onChange={(event) => setUrl(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key !== 'Enter') return;
                  event.preventDefault();
                  void handleClone();
                }}
                placeholder="https://github.com/owner/repo.git 或 git@github.com:owner/repo.git"
                disabled={busy}
                aria-invalid={urlError !== ''}
                aria-describedby="add-project-url-hint"
                className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs font-mono text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
              />
              <p
                id="add-project-url-hint"
                role="status"
                aria-live="polite"
                className={`min-h-[16px] text-mc-caption leading-relaxed break-words ${
                  urlError ? 'text-[var(--danger-foreground)]' : 'text-[var(--foreground-muted)]'
                }`}
              >
                {urlError || '支持 https:// 或 git@host:owner/repo.git 形式的地址。'}
              </p>

              <label
                htmlFor="add-project-parent"
                className="block text-xs font-semibold text-[var(--foreground)]"
              >
                存放目录（可选）
              </label>
              <div className="flex w-full min-w-0 items-center gap-1.5">
                <input
                  id="add-project-parent"
                  type="text"
                  value={parentPath}
                  autoComplete="off"
                  spellCheck={false}
                  inputMode="text"
                  onChange={(event) => setParentPath(event.target.value)}
                  placeholder="留空则克隆到用户主目录"
                  disabled={busy}
                  className="min-w-0 flex-1 min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs font-mono text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
                />
                {canBrowse && (
                  <button
                    type="button"
                    onClick={() => setBrowseTarget('clone-parent')}
                    disabled={busy}
                    className="secondary-button shrink-0 min-h-[44px] px-2.5 text-xs"
                  >
                    浏览…
                  </button>
                )}
              </div>

              <label
                htmlFor="add-project-name"
                className="block text-xs font-semibold text-[var(--foreground)]"
              >
                显示名（可选）
              </label>
              <input
                id="add-project-name"
                type="text"
                value={name}
                autoComplete="off"
                spellCheck={false}
                inputMode="text"
                onChange={(event) => setName(event.target.value)}
                placeholder="留空则用仓库名"
                disabled={busy}
                aria-invalid={nameError !== ''}
                aria-describedby="add-project-name-hint"
                className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
              />
              <p
                id="add-project-name-hint"
                role="status"
                aria-live="polite"
                className={`min-h-[16px] text-mc-caption leading-relaxed break-words ${
                  nameError ? 'text-[var(--danger-foreground)]' : 'text-[var(--foreground-muted)]'
                }`}
              >
                {nameError || '同时用作克隆出来的目录名，只能包含字母、数字和 . _ - +。'}
              </p>

              <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
                克隆在 Mac 端执行，大仓库可能需要几分钟；目标目录已存在时会失败。
              </p>

              {cloning && (
                <p
                  role="status"
                  aria-live="polite"
                  className="flex items-center gap-2 rounded-lg bg-[var(--border)]/40 px-2.5 py-2 text-mc-caption text-[var(--foreground-muted)]"
                >
                  <span className="loading-spinner loading-spinner--small shrink-0" />
                  <span>正在克隆…请保持页面打开，最多等待 10 分钟。</span>
                </p>
              )}
            </>
          )}

          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex items-start gap-2 min-w-0">
                <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
            </div>
          )}
        </div>

        <div className="mt-3 flex w-full min-w-0 shrink-0 items-center justify-end gap-2 border-t border-[var(--border)] pt-3">
          <button
            type="button"
            onClick={requestClose}
            disabled={busy}
            className="secondary-button min-h-[44px]"
          >
            取消
          </button>
          {mode === 'local' ? (
            <button
              type="button"
              onClick={() => void handleAddLocal()}
              disabled={!canAdd}
              className="primary-button min-h-[44px]"
            >
              {adding ? (
                <>
                  <span className="loading-spinner loading-spinner--small shrink-0" />
                  <span>添加中...</span>
                </>
              ) : (
                <>
                  <PlusIcon className="w-4 h-4" />
                  <span>添加</span>
                </>
              )}
            </button>
          ) : (
            <button
              type="button"
              onClick={() => void handleClone()}
              disabled={!canSubmitClone}
              className="primary-button min-h-[44px]"
            >
              {cloning ? (
                <>
                  <span className="loading-spinner loading-spinner--small shrink-0" />
                  <span>正在克隆...</span>
                </>
              ) : (
                <>
                  <PlusIcon className="w-4 h-4" />
                  <span>克隆并添加</span>
                </>
              )}
            </button>
          )}
        </div>
      </section>

      {browseTarget !== null && (
        <DirectoryPicker
          isOpen
          title={browseTarget === 'local' ? '选择项目目录' : '选择存放目录'}
          initialPath={browseTarget === 'local' ? path : parentPath}
          onCancel={() => setBrowseTarget(null)}
          onSelect={handleBrowseSelect}
        />
      )}
    </div>
  );
};
