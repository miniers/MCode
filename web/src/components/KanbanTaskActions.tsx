// 看板任务卡片操作：救援（催促 / 打断）与删除的菜单，以及待办列底部的行内快速添加。

import React, { useRef, useState } from 'react';
import type { TaskItem } from '../types/contract';
import { isImeComposing } from '../utils/ime';
import { ArrowPathIcon, PlusIcon, XMarkIcon } from './Icons';

/** 与桌面端救援菜单一致：进行中或带注意力标记的卡片才提供救援入口。 */
export function kanbanTaskCanRescue(task: TaskItem): boolean {
  return task.status === 'in_progress' || task.attentionFlags.length > 0;
}

/** 运行中的任务不能直接删除：服务端会拒绝，必须先打断会话。 */
export function kanbanTaskNeedsInterrupt(task: TaskItem): boolean {
  return task.status === 'in_progress';
}

interface KanbanTaskActionSheetProps {
  task: TaskItem;
  canDelete: boolean;
  canRescue: boolean;
  busy: boolean;
  busyText: string | null;
  onNudge: () => void;
  onInterrupt: () => void;
  onDelete: () => void;
  onClose: () => void;
}

/** 单张卡片的「更多」菜单：底部弹层收纳救援与删除，避免卡片行里堆按钮。 */
export const KanbanTaskActionSheet: React.FC<KanbanTaskActionSheetProps> = ({
  task,
  canDelete,
  canRescue,
  busy,
  busyText,
  onNudge,
  onInterrupt,
  onDelete,
  onClose
}) => {
  const rescue = canRescue && kanbanTaskCanRescue(task);
  const needsInterrupt = kanbanTaskNeedsInterrupt(task);
  // 运行中的任务只能「先打断再删除」；宿主没声明打断能力时不能删除。
  const deletable = canDelete && (!needsInterrupt || canRescue);

  return (
    <div className="sheet-layer" role="presentation" onMouseDown={onClose}>
      <section
        className="thread-action-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="task-actions-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />
        <div className="flex items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="eyebrow">任务操作</p>
            <h2 id="task-actions-title" className="sheet-title truncate">{task.title}</h2>
            {busy ? (
              <p className="mt-1 text-micro text-status-running">{busyText || '处理中…'}</p>
            ) : needsInterrupt ? (
              <p className="mt-1 text-micro text-muted">
                {canRescue ? '任务正在运行：删除前需要先打断执行会话。' : '任务正在运行，且当前 Mac 未声明打断能力，无法删除。'}
              </p>
            ) : null}
          </div>
          <button
            type="button"
            className="icon-button"
            onClick={onClose}
            aria-label="关闭任务菜单"
            title="关闭"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>
        <div className="sheet-actions">
          {rescue && (
            <button
              type="button"
              className="sheet-action"
              onClick={onNudge}
              disabled={busy}
              data-host-actionable="true"
            >
              <ArrowPathIcon className="h-4 w-4" />
              <span className="min-w-0 flex-1">
                <span className="block">催促 Agent</span>
                <span className="mt-0.5 block text-micro text-muted">给执行会话追加一条继续推进的提醒</span>
              </span>
            </button>
          )}
          {rescue && (
            <button
              type="button"
              className="sheet-action"
              onClick={onInterrupt}
              disabled={busy}
              data-host-actionable="true"
            >
              <XMarkIcon className="h-4 w-4" />
              <span className="min-w-0 flex-1">
                <span className="block">打断并退回待办</span>
                <span className="mt-0.5 block text-micro text-muted">终止执行会话，任务回到待办</span>
              </span>
            </button>
          )}
          {deletable && (
            <button
              type="button"
              className="sheet-action sheet-action--danger"
              onClick={onDelete}
              disabled={busy}
              data-host-actionable="true"
            >
              <XMarkIcon className="h-4 w-4" />
              <span className="min-w-0 flex-1">
                <span className="block">删除任务</span>
                <span className="mt-0.5 block text-micro text-muted">删除后不可撤销</span>
              </span>
            </button>
          )}
        </div>
      </section>
    </div>
  );
};

interface KanbanQuickAddProps {
  onSubmit: (title: string) => Promise<void>;
}

/** 待办列底部的行内快速添加：回车提交，成功后清空并保持焦点，失败保留输入并提示。 */
export const KanbanQuickAdd: React.FC<KanbanQuickAddProps> = ({ onSubmit }) => {
  const [title, setTitle] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const submit = async () => {
    const value = title.trim();
    if (!value || submitting) return;
    setSubmitting(true);
    setError(null);
    try {
      await onSubmit(value);
      // 提交期间用户可能继续输入，只清掉本次提交的内容
      setTitle((current) => (current === value ? '' : current));
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '创建任务失败');
    } finally {
      setSubmitting(false);
      inputRef.current?.focus();
    }
  };

  return (
    <div className="gutter-x pb-2 pt-1">
      <div className="flex min-h-11 items-center gap-2 rounded-mc-md border border-border-subtle bg-card px-2.5">
        <PlusIcon className="h-3.5 w-3.5 shrink-0 text-primary" />
        <input
          ref={inputRef}
          type="text"
          value={title}
          onChange={(event) => setTitle(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Enter') {
              if (isImeComposing(event)) return;
              event.preventDefault();
              void submit();
            }
            if (event.key === 'Escape') {
              setTitle('');
              setError(null);
            }
          }}
          placeholder="添加任务，回车创建"
          aria-label="快速添加待办任务"
          aria-busy={submitting}
          enterKeyHint="done"
          className="min-h-11 min-w-0 flex-1 border-0 bg-transparent text-sm text-foreground outline-none placeholder:text-muted"
        />
        {submitting && <span className="loading-spinner shrink-0" aria-hidden="true" />}
      </div>
      {error && (
        <p className="mt-1 text-micro text-danger" role="alert">{error}</p>
      )}
    </div>
  );
};
