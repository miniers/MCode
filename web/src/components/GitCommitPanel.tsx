// 移动端分文件提交面板：勾选改动文件、填写提交信息、只提交所选改动

import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  commitGitSelected,
  fetchGitChangedFiles,
  generateGitCommitMessage,
  pushGitBranch,
  type GitExtCaller,
  type RemoteGitChangedFile
} from '../context/remoteGitApi';
import { ConfirmDialog } from './ConfirmDialog';
import {
  CheckCircleIcon,
  ExclamationTriangleIcon,
  SparklesIcon
} from './Icons';

export interface GitCommitPanelProps {
  threadID: string;
  call: GitExtCaller;
  /** 提交成功后通知外层刷新工作区状态。 */
  onCommitted: () => void;
  /** 外层刷新令牌，变化时重新读取改动文件。 */
  refreshToken?: number;
  /** 宿主支持 git.push：显示「提交后推送」。 */
  canPush?: boolean;
  /** 宿主支持 git.generateCommitMessage：显示「AI 帮我写」。 */
  canGenerateMessage?: boolean;
  /** 外层其它 Git 写操作占用键，非空时锁定本面板。 */
  busyKey?: string | null;
  /** 通知外层占用/释放 Git 写操作。 */
  onBusyChange?: (key: string | null) => void;
}

const COMMIT_BUSY_KEY = 'commit';
const COMMIT_PUSH_BUSY_KEY = 'commit-push';
const GENERATE_BUSY_KEY = 'generate-message';
const PUSH_BUSY_KEY = 'push';
/** 本面板自己发起的占用键：这些键不算外部占用。 */
const OWN_BUSY_KEYS = new Set([
  COMMIT_BUSY_KEY,
  COMMIT_PUSH_BUSY_KEY,
  GENERATE_BUSY_KEY,
  PUSH_BUSY_KEY
]);

/** 把 git 状态字母翻成简短中文说明。 */
function gitExtStatusLabel(status: string): string {
  switch (status.trim().toUpperCase()) {
    case 'M':
      return '修改';
    case 'A':
      return '新增';
    case 'D':
      return '删除';
    case 'R':
      return '重命名';
    case 'C':
      return '复制';
    case 'U':
      return '冲突';
    case '?':
      return '未跟踪';
    case 'T':
      return '类型变更';
    default:
      return status.trim() || '改动';
  }
}

/** 状态标签配色：新增/修改用中性偏正向，删除与冲突用告警色。 */
function gitExtStatusClass(status: string): string {
  switch (status.trim().toUpperCase()) {
    case 'A':
    case '?':
      return 'bg-emerald-500/15 text-emerald-600 dark:text-emerald-400';
    case 'D':
    case 'U':
      return 'bg-red-500/15 text-red-600 dark:text-red-400';
    case 'R':
    case 'C':
      return 'bg-sky-500/15 text-sky-600 dark:text-sky-400';
    default:
      return 'bg-amber-500/15 text-amber-600 dark:text-amber-400';
  }
}

export const GitCommitPanel: React.FC<GitCommitPanelProps> = ({
  threadID,
  call,
  onCommitted,
  refreshToken = 0,
  canPush = false,
  canGenerateMessage = false,
  busyKey: externalBusyKey = null,
  onBusyChange
}) => {
  const [files, setFiles] = useState<RemoteGitChangedFile[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [message, setMessage] = useState('');
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const [localBusy, setLocalBusy] = useState<string | null>(null);
  const [pushAfter, setPushAfter] = useState(false);
  /** 已提交但推送失败的原因，单独展示并给出重试入口。 */
  const [pushError, setPushError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [pendingGenerateConfirm, setPendingGenerateConfirm] = useState(false);

  // 外层其它 Git 写操作进行中时锁定本面板；自己发起的操作不算外部占用
  const externalBusy = externalBusyKey !== null && !OWN_BUSY_KEYS.has(externalBusyKey);
  const busy = localBusy !== null || externalBusy;
  const submitting = localBusy === COMMIT_BUSY_KEY || localBusy === COMMIT_PUSH_BUSY_KEY;
  const generating = localBusy === GENERATE_BUSY_KEY;
  const retryingPush = localBusy === PUSH_BUSY_KEY;

  // 同时维护本地面板占用与外层占用，保证同一时间只有一个 Git 写操作
  const setBusy = useCallback(
    (key: string | null) => {
      setLocalBusy(key);
      onBusyChange?.(key);
    },
    [onBusyChange]
  );

  const reqIdRef = useRef(0);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      reqIdRef.current += 1;
    };
  }, []);

  // 读取改动文件：默认全选，方便一次提交全部改动
  useEffect(() => {
    const reqId = ++reqIdRef.current;
    setLoading(true);
    setLoadError(null);

    void fetchGitChangedFiles(call, threadID)
      .then((result) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setFiles(result.files);
        setSelected(result.files.map((file) => file.path));
      })
      .catch((err: unknown) => {
        if (!mountedRef.current || reqId !== reqIdRef.current) return;
        setFiles([]);
        setSelected([]);
        setLoadError(err instanceof Error ? err.message : '读取改动文件失败');
      })
      .finally(() => {
        if (mountedRef.current && reqId === reqIdRef.current) {
          setLoading(false);
        }
      });
  }, [call, refreshToken, reloadToken, threadID]);

  const toggleFile = useCallback((path: string) => {
    setSubmitError(null);
    setSelected((prev) =>
      prev.includes(path) ? prev.filter((item) => item !== path) : [...prev, path]
    );
  }, []);

  const selectAll = useCallback(() => {
    setSubmitError(null);
    setSelected(files.map((file) => file.path));
  }, [files]);

  const selectNone = useCallback(() => {
    setSubmitError(null);
    setSelected([]);
  }, []);

  const canSubmit =
    selected.length > 0 && message.trim() !== '' && !busy && !loading && !generating;

  const handleSubmit = useCallback(async () => {
    const trimmed = message.trim();
    if (selected.length === 0 || trimmed === '' || busy) return;

    const withPush = canPush && pushAfter;
    setBusy(withPush ? COMMIT_PUSH_BUSY_KEY : COMMIT_BUSY_KEY);
    setSubmitError(null);
    setPushError(null);
    setNotice(null);
    try {
      const result = await commitGitSelected(call, threadID, selected, trimmed, withPush);
      if (!mountedRef.current) return;
      // 提交成功后清空说明并重新读取改动列表，同时让外层刷新工作区状态
      setMessage('');
      setReloadToken((prev) => prev + 1);
      onCommitted();
      if (result.pushError) {
        // 提交已经成功，只有推送失败：单独提示并允许重试推送
        setPushError(result.pushError);
      } else {
        setNotice(withPush ? '已提交并推送' : '已提交');
      }
    } catch (err) {
      if (!mountedRef.current) return;
      setSubmitError(err instanceof Error ? err.message : '提交所选改动失败');
    } finally {
      if (mountedRef.current) {
        setBusy(null);
      }
    }
  }, [busy, call, canPush, message, onCommitted, pushAfter, selected, setBusy, threadID]);

  // 重试推送：提交已经在本地成功，只补一次推送
  const handleRetryPush = useCallback(async () => {
    if (busy) return;
    setBusy(PUSH_BUSY_KEY);
    setPushError(null);
    setNotice(null);
    try {
      await pushGitBranch(call, threadID);
      if (!mountedRef.current) return;
      setNotice('推送成功');
      onCommitted();
    } catch (err) {
      if (!mountedRef.current) return;
      setPushError(err instanceof Error ? err.message : '推送失败');
    } finally {
      if (mountedRef.current) {
        setBusy(null);
      }
    }
  }, [busy, call, onCommitted, setBusy, threadID]);

  const doGenerate = useCallback(async () => {
    if (busy) return;
    setPendingGenerateConfirm(false);
    setBusy(GENERATE_BUSY_KEY);
    setSubmitError(null);
    setNotice(null);
    try {
      const result = await generateGitCommitMessage(call, threadID);
      if (!mountedRef.current) return;
      if (result.message.trim() === '') {
        setSubmitError('AI 没有生成提交信息，请手动填写');
        return;
      }
      setMessage(result.message);
    } catch (err) {
      if (!mountedRef.current) return;
      setSubmitError(
        `生成提交信息失败：${err instanceof Error ? err.message : '未知错误'}`
      );
    } finally {
      if (mountedRef.current) {
        setBusy(null);
      }
    }
  }, [busy, call, setBusy, threadID]);

  // 已有提交信息时先确认是否覆盖，避免冲掉手写内容
  const requestGenerate = useCallback(() => {
    if (!canGenerateMessage || busy) return;
    if (message.trim() !== '') {
      setPendingGenerateConfirm(true);
      return;
    }
    void doGenerate();
  }, [busy, canGenerateMessage, doGenerate, message]);

  return (
    <div className="p-3.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-3">
      <div className="flex items-center justify-between gap-2 min-w-0">
        <span className="text-xs font-semibold text-[var(--foreground)] truncate">
          分文件提交
        </span>
        <div className="flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={selectAll}
            disabled={loading || files.length === 0}
            className="secondary-button min-h-[44px] px-2.5 text-xs"
          >
            全选
          </button>
          <button
            type="button"
            onClick={selectNone}
            disabled={loading || selected.length === 0}
            className="secondary-button min-h-[44px] px-2.5 text-xs"
          >
            全不选
          </button>
        </div>
      </div>

      {loadError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{loadError}</span>
          </div>
          <button
            type="button"
            onClick={() => setReloadToken((prev) => prev + 1)}
            disabled={loading}
            className="inline-alert__action min-h-[44px] px-2 flex items-center"
          >
            重试
          </button>
        </div>
      )}

      {!loadError && loading && files.length === 0 && (
        <div className="flex items-center gap-2 py-1.5 text-xs text-[var(--foreground-muted)]">
          <span className="loading-spinner loading-spinner--small shrink-0" />
          <span>正在读取改动文件...</span>
        </div>
      )}

      {!loadError && !loading && files.length === 0 && (
        <div className="p-3 rounded-lg border border-[var(--border)] bg-[var(--screen)] text-xs text-[var(--foreground-muted)] text-center">
          没有可提交的改动
        </div>
      )}

      {files.length > 0 && (
        <ul className="space-y-1">
          {files.map((file) => {
            const checked = selected.includes(file.path);
            return (
              <li key={`${file.path}-${file.origPath ?? ''}`}>
                <label
                  className={`p-2.5 rounded-lg border flex items-start gap-2 cursor-pointer select-none ${
                    checked
                      ? 'border-[var(--primary)] bg-[var(--card-alt,var(--card))]'
                      : 'border-[var(--border)] bg-[var(--card)]'
                  }`}
                >
                  <input
                    type="checkbox"
                    checked={checked}
                    onChange={() => toggleFile(file.path)}
                    disabled={submitting || generating}
                    className="mt-0.5 h-4 w-4 shrink-0 accent-[var(--primary)]"
                  />
                  <span className="min-w-0 flex-1 space-y-0.5">
                    <span className="block select-text font-mono text-xs break-all leading-snug text-[var(--foreground)]">
                      {file.origPath ? `${file.origPath} → ${file.path}` : file.path}
                    </span>
                    <span
                      className={`inline-block px-1.5 py-0.5 rounded text-mc-caption font-semibold ${gitExtStatusClass(file.status)}`}
                    >
                      {gitExtStatusLabel(file.status)}
                    </span>
                  </span>
                </label>
              </li>
            );
          })}
        </ul>
      )}

      <div className="space-y-1">
        <div className="flex items-center justify-between gap-2 min-w-0">
          <label
            htmlFor="git-commit-message"
            className="block text-xs text-[var(--foreground-muted)] truncate"
          >
            提交信息
          </label>
          {canGenerateMessage && (
            <button
              type="button"
              onClick={requestGenerate}
              disabled={busy || files.length === 0}
              className="secondary-button min-h-[44px] px-2.5 text-xs shrink-0"
              title="用这些改动写一条提交信息"
            >
              {generating ? (
                <>
                  <span className="loading-spinner loading-spinner--small shrink-0" />
                  <span>生成中...</span>
                </>
              ) : (
                <>
                  <SparklesIcon className="w-4 h-4" />
                  <span>AI 帮我写</span>
                </>
              )}
            </button>
          )}
        </div>
        <textarea
          id="git-commit-message"
          value={message}
          onChange={(event) => {
            setMessage(event.target.value);
            setSubmitError(null);
          }}
          rows={3}
          placeholder="填写这次提交的说明..."
          disabled={submitting || generating}
          className="w-full resize-y rounded-lg border border-[var(--input-border)] bg-[var(--card)] p-2.5 text-xs leading-relaxed text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
        />
      </div>

      {canPush && (
        <label className="flex items-center gap-2 min-h-[44px] cursor-pointer select-none">
          <input
            type="checkbox"
            checked={pushAfter}
            onChange={(event) => {
              setPushAfter(event.target.checked);
              setSubmitError(null);
              setPushError(null);
            }}
            disabled={busy}
            className="h-4 w-4 shrink-0 accent-[var(--primary)]"
          />
          <span className="text-xs text-[var(--foreground)]">提交后推送到远程仓库</span>
        </label>
      )}

      {notice && (
        <div
          className="flex items-start gap-2 px-3 py-2 rounded-lg border border-emerald-500/30 bg-emerald-500/10 text-xs text-emerald-600 dark:text-emerald-400"
          role="status"
        >
          <CheckCircleIcon className="w-4 h-4 shrink-0 mt-0.5" />
          <span className="break-words select-text leading-relaxed">{notice}</span>
        </div>
      )}

      {pushError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0 flex-1">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">
              已提交，推送失败：{pushError}
            </span>
          </div>
          <button
            type="button"
            onClick={() => void handleRetryPush()}
            disabled={busy}
            className="inline-alert__action min-h-[44px] px-2 flex items-center"
          >
            {retryingPush ? '推送中...' : '重试推送'}
          </button>
        </div>
      )}

      {submitError && (
        <div className="inline-alert inline-alert--danger" role="alert">
          <div className="flex items-start gap-2 min-w-0">
            <ExclamationTriangleIcon className="w-4 h-4 shrink-0 mt-0.5 text-[var(--danger-foreground)]" />
            <span className="break-words select-text">{submitError}</span>
          </div>
        </div>
      )}

      <div className="flex items-center justify-between gap-2 min-w-0">
        <span className="text-xs text-[var(--foreground-muted)] truncate">
          已选 {selected.length} / {files.length} 个文件
        </span>
        <button
          type="button"
          onClick={() => void handleSubmit()}
          disabled={!canSubmit}
          className="primary-button min-h-[44px] shrink-0"
        >
          {submitting ? (
            <>
              <span className="loading-spinner loading-spinner--small shrink-0" />
              <span>{localBusy === COMMIT_PUSH_BUSY_KEY ? '提交并推送中...' : '提交中...'}</span>
            </>
          ) : (
            <>
              <CheckCircleIcon className="w-4 h-4" />
              <span>{canPush && pushAfter ? '提交并推送' : '提交所选'}</span>
            </>
          )}
        </button>
      </div>

      {message.trim() === '' && selected.length > 0 && !busy && (
        <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
          填写提交信息后才能提交，提交只包含勾选的改动。
        </p>
      )}

      <ConfirmDialog
        isOpen={pendingGenerateConfirm}
        title="覆盖提交信息"
        message="提交信息框已有内容，用 AI 生成的内容替换吗？"
        confirmLabel="覆盖"
        isLoading={generating}
        onConfirm={() => void doGenerate()}
        onCancel={() => setPendingGenerateConfirm(false)}
      />
    </div>
  );
};
