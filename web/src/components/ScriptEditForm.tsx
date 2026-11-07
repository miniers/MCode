// 项目脚本的新建/编辑表单：名称、命令、可选图标符号、「创建 worktree 时自动运行」开关。
// 字段与校验对齐桌面端脚本编辑弹窗（名称与命令非空、名称不重名），叠在脚本抽屉之上显示。

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ExclamationTriangleIcon, XMarkIcon } from './Icons';

/** 表单值：id 为空表示新建；symbol 统一用字符串，空串表示不设图标。 */
export interface ScriptDraft {
  id?: string;
  name: string;
  command: string;
  symbol: string;
  runOnWorktreeCreate: boolean;
}

export interface ScriptEditFormProps {
  mode: 'create' | 'edit';
  /** 初始值：编辑时是当前脚本，新建时是空表单。 */
  initial: ScriptDraft;
  /** 除当前编辑项之外已有的脚本名，用于重名校验。 */
  otherNames: string[];
  saving: boolean;
  /** 服务端返回的保存错误原文。 */
  error: string | null;
  onSubmit: (draft: ScriptDraft) => void;
  onCancel: () => void;
}

const NAME_MAX_LENGTH = 256;
const SYMBOL_MAX_LENGTH = 8;

/** 校验表单：返回空串表示可保存，否则是中文原因（与桌面端文案一致）。 */
export function validateScriptDraft(draft: ScriptDraft, otherNames: string[]): string {
  const name = draft.name.trim();
  if (!name) return '脚本名称不能为空';
  if (otherNames.some((item) => item.trim() === name)) return '脚本名称不能重复';
  if (!draft.command.trim()) return '每个脚本都需要命令';
  return '';
}

export const ScriptEditForm: React.FC<ScriptEditFormProps> = ({
  mode,
  initial,
  otherNames,
  saving,
  error,
  onSubmit,
  onCancel
}) => {
  const [name, setName] = useState(initial.name);
  const [command, setCommand] = useState(initial.command);
  const [symbol, setSymbol] = useState(initial.symbol);
  const [runOnWorktreeCreate, setRunOnWorktreeCreate] = useState(initial.runOnWorktreeCreate);
  const [touched, setTouched] = useState(false);

  const nameRef = useRef<HTMLInputElement | null>(null);
  const savingRef = useRef(saving);
  savingRef.current = saving;

  useEffect(() => {
    nameRef.current?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      if (!savingRef.current) onCancel();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [onCancel]);

  const draft: ScriptDraft = {
    ...(initial.id ? { id: initial.id } : {}),
    name,
    command,
    symbol,
    runOnWorktreeCreate
  };
  const validation = validateScriptDraft(draft, otherNames);
  const showValidation = touched && validation !== '';
  const canSubmit = !saving && validation === '';

  const handleSubmit = useCallback(() => {
    setTouched(true);
    const next: ScriptDraft = {
      ...(initial.id ? { id: initial.id } : {}),
      name,
      command,
      symbol,
      runOnWorktreeCreate
    };
    if (saving || validateScriptDraft(next, otherNames) !== '') return;
    onSubmit({
      ...(initial.id ? { id: initial.id } : {}),
      name: name.trim(),
      command: command.trim(),
      symbol: symbol.trim(),
      runOnWorktreeCreate
    });
  }, [command, initial.id, name, onSubmit, otherNames, runOnWorktreeCreate, saving, symbol]);

  return (
    <div
      className="fixed inset-0 z-[60] flex items-end justify-center bg-black/40 px-3 pb-0 pt-12"
      role="presentation"
      onMouseDown={() => {
        if (!saving) onCancel();
      }}
    >
      <section
        className="settings-sheet flex w-full min-w-0 max-h-[90dvh] flex-col overflow-hidden outline-none"
        role="dialog"
        aria-modal="true"
        aria-labelledby="script-edit-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle shrink-0" aria-hidden="true" />

        <div className="mb-3 flex w-full min-w-0 shrink-0 items-start justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="eyebrow truncate">项目脚本</p>
            <h2 id="script-edit-title" className="sheet-title mt-0.5">
              {mode === 'create' ? '新建脚本' : '编辑脚本'}
            </h2>
          </div>
          <button
            type="button"
            onClick={onCancel}
            disabled={saving}
            aria-label="关闭"
            title="关闭"
            className="icon-button shrink-0 min-h-[44px] min-w-[44px]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        <div className="min-h-0 w-full min-w-0 flex-1 space-y-2 overflow-y-auto pr-0.5">
          <label htmlFor="script-edit-name" className="block text-xs font-semibold text-[var(--foreground)]">
            脚本名称
          </label>
          <input
            id="script-edit-name"
            ref={nameRef}
            type="text"
            value={name}
            autoComplete="off"
            spellCheck={false}
            maxLength={NAME_MAX_LENGTH}
            onChange={(event) => setName(event.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="例如 测试"
            disabled={saving}
            aria-invalid={showValidation}
            className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
          />

          <label htmlFor="script-edit-command" className="block text-xs font-semibold text-[var(--foreground)]">
            命令
          </label>
          <textarea
            id="script-edit-command"
            value={command}
            rows={3}
            spellCheck={false}
            onChange={(event) => setCommand(event.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="例如 go test ./..."
            disabled={saving}
            aria-invalid={showValidation}
            className="w-full min-h-[88px] resize-y rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 py-2 font-mono text-xs leading-relaxed text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
          />
          <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
            命令在 Mac 端该项目的根目录执行，支持多行。
          </p>

          <label htmlFor="script-edit-symbol" className="block text-xs font-semibold text-[var(--foreground)]">
            图标符号（可选）
          </label>
          <input
            id="script-edit-symbol"
            type="text"
            value={symbol}
            autoComplete="off"
            spellCheck={false}
            maxLength={SYMBOL_MAX_LENGTH}
            onChange={(event) => setSymbol(event.target.value)}
            placeholder="例如 ⚡ 或 ▶"
            disabled={saving}
            className="w-full min-h-[44px] rounded-lg border border-[var(--input-border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
          />
          <p className="text-mc-caption text-[var(--foreground-muted)] leading-relaxed">
            只用于在 Mac 端脚本菜单里区分脚本，留空即不显示图标。
          </p>

          <label
            htmlFor="script-edit-run-on-create"
            className="flex min-h-[44px] w-full items-center gap-2 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 text-xs text-[var(--foreground)]"
          >
            <input
              id="script-edit-run-on-create"
              type="checkbox"
              checked={runOnWorktreeCreate}
              onChange={(event) => setRunOnWorktreeCreate(event.target.checked)}
              disabled={saving}
              className="h-4 w-4 shrink-0"
            />
            <span className="min-w-0 break-words">创建 worktree 时自动运行</span>
          </label>

          {showValidation && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex min-w-0 items-start gap-2">
                <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{validation}</span>
              </div>
            </div>
          )}

          {error && (
            <div className="inline-alert inline-alert--danger" role="alert">
              <div className="flex min-w-0 items-start gap-2">
                <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0 text-[var(--danger-foreground)]" />
                <span className="break-words select-text">{error}</span>
              </div>
            </div>
          )}
        </div>

        <div className="mt-3 flex w-full min-w-0 shrink-0 items-center justify-end gap-2 border-t border-[var(--border)] pt-3">
          <button
            type="button"
            onClick={onCancel}
            disabled={saving}
            className="secondary-button min-h-[44px]"
          >
            取消
          </button>
          <button
            type="button"
            onClick={handleSubmit}
            disabled={!canSubmit}
            className="primary-button min-h-[44px] min-w-[88px]"
          >
            {saving ? (
              <>
                <span className="loading-spinner loading-spinner--small shrink-0" />
                <span>保存中...</span>
              </>
            ) : (
              <span>保存</span>
            )}
          </button>
        </div>
      </section>
    </div>
  );
};
