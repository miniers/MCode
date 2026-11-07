// 工作区文本文件的内联编辑器：等宽 textarea，保存走 files.write。
// 保存带 expectedHash 做冲突检测：冲突时不丢用户输入，让用户选择重新载入或仍然覆盖。
// 单文件上限与宿主一致（512 KiB），超限时禁用保存。

import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  filesWriteErrorText,
  isFileWriteConflict,
  useRemoteFilesWriteApi,
  type RemoteFilesWriteResult
} from '../context/remoteFilesWriteApi';
import { ConfirmDialog } from './ConfirmDialog';
import { ArrowPathIcon } from './Icons';

/** 宿主允许覆盖保存的最大字节数（internal/remote/files_write_ext_methods.go 的 512 KiB 上限）。 */
const MAX_EDITABLE_BYTES = 512 * 1024;

export interface FileEditorReloadResult {
  content: string;
  hash: string;
}

export interface FileEditorProps {
  threadID: string;
  /** 工作区相对路径。 */
  path: string;
  /** 仅用于界面文案。 */
  fileName: string;
  initialContent: string;
  initialHash: string;
  /** 未保存改动状态变化时回调，父组件据此在返回目录/关闭抽屉前二次确认。 */
  onDirtyChange: (dirty: boolean) => void;
  /** 用户点「返回预览」：父组件决定是否先弹确认。 */
  onRequestExit: () => void;
  /** 保存成功：父组件用最新内容与 hash 刷新预览。 */
  onSaved: (content: string, result: RemoteFilesWriteResult) => void;
  /** 冲突时重新从宿主读取整文件内容。 */
  reload: (path: string) => Promise<FileEditorReloadResult>;
}

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i] || 'B'}`;
}

export const FileEditor: React.FC<FileEditorProps> = ({
  threadID,
  path,
  fileName,
  initialContent,
  initialHash,
  onDirtyChange,
  onRequestExit,
  onSaved,
  reload
}) => {
  const { write } = useRemoteFilesWriteApi();

  // baseline 是磁盘上的内容：content 与它不同即为未保存改动。
  const [baseline, setBaseline] = useState(initialContent);
  const [content, setContent] = useState(initialContent);
  const [hash, setHash] = useState(initialHash);
  const [saving, setSaving] = useState(false);
  const [reloading, setReloading] = useState(false);
  const [error, setError] = useState('');
  const [conflict, setConflict] = useState(false);
  const [confirmOverwrite, setConfirmOverwrite] = useState(false);

  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const dirty = content !== baseline;

  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    textareaRef.current?.focus();
  }, []);

  const contentBytes = useMemo(() => new TextEncoder().encode(content).byteLength, [content]);
  const overLimit = contentBytes > MAX_EDITABLE_BYTES;

  const persist = useCallback(
    async (expectedHash?: string) => {
      if (saving) return;
      setSaving(true);
      setError('');
      setConflict(false);
      try {
        const result = await write({
          threadID,
          path,
          content,
          ...(expectedHash ? { expectedHash } : {})
        });
        setHash(result.hash);
        setBaseline(content);
        onSaved(content, result);
      } catch (err) {
        if (expectedHash && isFileWriteConflict(err)) {
          setConflict(true);
        } else {
          setError(filesWriteErrorText(err));
        }
      } finally {
        setSaving(false);
      }
    },
    [content, onSaved, path, saving, threadID, write]
  );

  const handleSave = useCallback(() => {
    if (saving || overLimit) return;
    void persist(hash);
  }, [hash, overLimit, persist, saving]);

  const handleOverwrite = useCallback(() => {
    setConfirmOverwrite(false);
    void persist();
  }, [persist]);

  const handleReload = useCallback(async () => {
    if (reloading || saving) return;
    setReloading(true);
    setError('');
    try {
      const next = await reload(path);
      setContent(next.content);
      setBaseline(next.content);
      setHash(next.hash);
      setConflict(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : '重新载入失败，请稍后重试');
    } finally {
      setReloading(false);
    }
  }, [path, reload, reloading, saving]);

  // Tab 在 textarea 里默认会切走焦点：拦下来插入制表符，保留文件里的缩进风格。
  const handleKeyDown = useCallback(
    (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (event.key !== 'Tab' || event.metaKey || event.ctrlKey || event.altKey) return;
      event.preventDefault();
      const el = event.currentTarget;
      const start = el.selectionStart;
      const end = el.selectionEnd;
      const next = `${content.slice(0, start)}\t${content.slice(end)}`;
      setContent(next);
      requestAnimationFrame(() => {
        el.selectionStart = start + 1;
        el.selectionEnd = start + 1;
      });
    },
    [content]
  );

  return (
    <div className="flex-1 min-h-0 flex flex-col gap-2 min-w-0 max-w-full overflow-hidden">
      {/* 编辑工具条：改动状态与保存/返回 */}
      <div className="shrink-0 flex items-center justify-between gap-2 border-b border-[var(--border)] pb-2 min-w-0 flex-wrap gap-y-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <span className="text-xs font-semibold text-[var(--foreground)] truncate">
            {fileName}
          </span>
          <span
            className={`text-mc-caption shrink-0 ${
              dirty ? 'text-amber-600 dark:text-amber-400' : 'text-[var(--foreground-muted)]'
            }`}
          >
            {dirty ? '有未保存改动' : '与磁盘一致'}
          </span>
          <span className="text-mc-caption text-[var(--foreground-muted)] font-mono shrink-0">
            {formatBytes(contentBytes)}
          </span>
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          <button
            type="button"
            onClick={onRequestExit}
            disabled={saving}
            className="secondary-button text-xs py-1 px-2.5"
          >
            返回预览
          </button>
          <button
            type="button"
            onClick={handleSave}
            disabled={saving || overLimit || !dirty}
            className="primary-button text-xs py-1 px-2.5 inline-flex items-center gap-1 disabled:opacity-50"
          >
            {saving && <ArrowPathIcon className="w-3 h-3 animate-spin" />}
            <span>{saving ? '保存中…' : '保存'}</span>
          </button>
        </div>
      </div>

      {/* 冲突处理：不丢用户输入，交给用户选重新载入或仍然覆盖 */}
      {conflict && (
        <div
          className="shrink-0 p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] text-xs space-y-2"
          role="alert"
        >
          <p className="font-semibold text-[var(--foreground)]">文件已在别处被修改</p>
          <p className="text-[var(--foreground-muted)]">
            磁盘内容与读取时不一致，保存已取消。可以重新载入磁盘版本（放弃我的改动），或仍然用当前内容覆盖。
          </p>
          <div className="flex items-center gap-1.5 flex-wrap">
            <button
              type="button"
              onClick={() => void handleReload()}
              disabled={reloading || saving}
              className="secondary-button text-xs py-1 px-2.5"
            >
              {reloading ? '正在重新载入…' : '重新载入（放弃我的改动）'}
            </button>
            <button
              type="button"
              onClick={() => setConfirmOverwrite(true)}
              disabled={reloading || saving}
              className="settings-action settings-action--danger settings-inline-button text-xs"
            >
              仍然覆盖
            </button>
          </div>
        </div>
      )}

      {error && (
        <div
          className="shrink-0 p-2.5 rounded-xl border border-[var(--danger-border,var(--border))] bg-[var(--card)] text-xs"
          role="alert"
        >
          <p className="font-semibold text-[var(--danger-foreground)]">保存失败</p>
          <p className="text-[var(--foreground-muted)] break-words">{error}</p>
        </div>
      )}

      {overLimit && (
        <div className="shrink-0 p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)]">
          内容已超过单文件保存上限 512 KiB（当前 {formatBytes(contentBytes)}），保存已禁用，请删减后再试。
        </div>
      )}

      <textarea
        ref={textareaRef}
        value={content}
        onChange={(event) => setContent(event.target.value)}
        onKeyDown={handleKeyDown}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        aria-label={`编辑 ${fileName}`}
        className="flex-1 min-h-[200px] w-full resize-none rounded-xl border border-[var(--border)] bg-[var(--user-bubble)] p-2.5 font-mono text-xs leading-5 text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
      />

      <ConfirmDialog
        isOpen={confirmOverwrite}
        title="仍然覆盖？"
        message={`「${fileName}」在磁盘上已被改过，覆盖会丢弃磁盘上的那部分改动，且无法撤销。`}
        confirmLabel="覆盖保存"
        confirmVariant="danger"
        isLoading={saving}
        onConfirm={handleOverwrite}
        onCancel={() => setConfirmOverwrite(false)}
      />
    </div>
  );
};
