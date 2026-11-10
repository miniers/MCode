// 存为看板任务表单：填写标题与备注，提交后与当前会话关联

import React, { useCallback, useImperativeHandle } from 'react';
import { isImeComposing } from '../../utils/ime';
import type { ThreadMenuPageHandle } from './menu/menuPages';

export interface SaveTaskFormProps {
  saveTaskTitle: string;
  setSaveTaskTitle: React.Dispatch<React.SetStateAction<string>>;
  saveTaskNotes: string;
  setSaveTaskNotes: React.Dispatch<React.SetStateAction<string>>;
  saveTaskError: string | null;
  isSavingTask: boolean;
  shareActionsBlocked: boolean;
  setSaveTaskOpen: React.Dispatch<React.SetStateAction<boolean>>;
  handleSubmitSaveTask: () => Promise<void>;
}

export interface SaveTaskBodyProps extends SaveTaskFormProps {
  /** 取消或外壳关闭后的收尾（栈内即出栈，兼容壳即关表单） */
  onClose: () => void;
}

/** 内容体：不含遮罩与标题栏；保存中拦截关闭。 */
export const SaveTaskBody = React.forwardRef<ThreadMenuPageHandle, SaveTaskBodyProps>(
  function SaveTaskBody(
    {
      saveTaskTitle,
      setSaveTaskTitle,
      saveTaskNotes,
      setSaveTaskNotes,
      saveTaskError,
      isSavingTask,
      shareActionsBlocked,
      setSaveTaskOpen,
      handleSubmitSaveTask,
      onClose
    },
    ref
  ) {
    const requestClose = useCallback(() => (isSavingTask ? false : true), [isSavingTask]);

    useImperativeHandle(ref, () => ({ requestClose }), [requestClose]);

    const handleCancel = useCallback(() => {
      if (!requestClose()) return;
      setSaveTaskOpen(false);
      onClose();
    }, [onClose, requestClose, setSaveTaskOpen]);

    return (
      <div className="flex flex-col flex-1 min-h-0 min-w-0">
        <p className="mt-1 text-xs text-[var(--foreground-muted)]">
          新任务会与当前会话关联，等同 Mac 端会话菜单里的“Save as Kanban Task”。
        </p>
        <div className="mt-3 space-y-2">
          <input
            type="text"
            value={saveTaskTitle}
            onChange={(event) => setSaveTaskTitle(event.target.value)}
            placeholder="任务标题"
            aria-label="任务标题"
            autoFocus
            disabled={isSavingTask}
            className="w-full min-h-[42px] px-3 rounded-xl border border-[var(--input-border)] bg-[var(--card)] text-sm text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
            onKeyDown={(event) => {
              if (event.key === 'Enter' && !isImeComposing(event)) void handleSubmitSaveTask();
              if (event.key === 'Escape' && !isSavingTask) handleCancel();
            }}
          />
          <textarea
            value={saveTaskNotes}
            onChange={(event) => setSaveTaskNotes(event.target.value)}
            placeholder="备注（可选）"
            aria-label="任务备注"
            rows={3}
            disabled={isSavingTask}
            className="w-full px-3 py-2 rounded-xl border border-[var(--input-border)] bg-[var(--card)] text-sm text-[var(--foreground)] outline-none focus:border-[var(--primary)] resize-none"
          />
        </div>
        {saveTaskError && (
          <p className="mt-3 text-xs text-[var(--danger-foreground)]" role="alert">
            {saveTaskError}
          </p>
        )}
        <div className="mt-4 flex gap-2">
          <button
            type="button"
            onClick={() => void handleSubmitSaveTask()}
            disabled={isSavingTask || shareActionsBlocked || !saveTaskTitle.trim()}
            className="flex-1 min-h-[44px] rounded-xl bg-[var(--primary)] text-white text-xs font-semibold disabled:opacity-40 transition active:scale-95"
          >
            {isSavingTask ? '保存中…' : '保存'}
          </button>
          <button
            type="button"
            onClick={handleCancel}
            disabled={isSavingTask}
            className="min-h-[44px] px-4 rounded-xl border border-[var(--border)] text-xs text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] transition active:scale-95"
          >
            取消
          </button>
        </div>
      </div>
    );
  }
);

