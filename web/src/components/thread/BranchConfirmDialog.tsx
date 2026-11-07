// 从回复分支确认弹窗：把到指定回复为止的对话带入一个新会话

import React from 'react';

export interface BranchConfirmDialogProps {
  isBranching: boolean;
  branchError: string | null;
  setBranchConfirmMessageID: React.Dispatch<React.SetStateAction<string | null>>;
  setBranchError: React.Dispatch<React.SetStateAction<string | null>>;
  handleConfirmBranch: () => Promise<void>;
}

export const BranchConfirmDialog = React.memo(function BranchConfirmDialog({
  isBranching,
  branchError,
  setBranchConfirmMessageID,
  setBranchError,
  handleConfirmBranch
}: BranchConfirmDialogProps) {
  return (
    <div
      className="sheet-layer"
      role="presentation"
      onMouseDown={() => {
        if (!isBranching) {
          setBranchConfirmMessageID(null);
          setBranchError(null);
        }
      }}
    >
      <section
        className="thread-action-sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="branch-confirm-title"
        onMouseDown={(event) => event.stopPropagation()}
      >
        <div className="sheet-handle" aria-hidden="true" />
        <div className="space-y-2">
          <h2 id="branch-confirm-title" className="sheet-title">
            从这条回复分支？
          </h2>
          <p className="text-xs text-[var(--foreground-muted)]">
            新会话将包含到这条回复为止的对话。
          </p>
        </div>
        {branchError && (
          <p className="mt-3 text-xs text-[var(--danger-foreground)]" role="alert">
            {branchError}
          </p>
        )}
        <div className="mt-5 flex gap-2">
          <button
            type="button"
            onClick={() => void handleConfirmBranch()}
            disabled={isBranching}
            className="flex-1 min-h-[44px] rounded-xl bg-[var(--primary)] text-white text-xs font-semibold disabled:opacity-40 transition active:scale-95"
          >
            {isBranching ? '分支中…' : '分支'}
          </button>
          <button
            type="button"
            onClick={() => {
              setBranchConfirmMessageID(null);
              setBranchError(null);
            }}
            disabled={isBranching}
            className="min-h-[44px] px-4 rounded-xl border border-[var(--border)] text-xs text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] transition active:scale-95"
          >
            取消
          </button>
        </div>
      </section>
    </div>
  );
});
