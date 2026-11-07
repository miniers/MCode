// 转发到新会话确认弹窗：把整段对话上下文带入一个新会话

import React, { useCallback, useImperativeHandle, useRef } from 'react';
import { SheetFrame } from '../sheet/SheetFrame';
import { MenuSheetHeader } from './menu/MenuSheetHeader';
import type { ThreadMenuPageHandle } from './menu/menuPages';

export interface ForwardConfirmDialogProps {
  isForwarding: boolean;
  shareActionsBlocked: boolean;
  forwardError: string | null;
  setForwardConfirmOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setForwardError: React.Dispatch<React.SetStateAction<string | null>>;
  handleConfirmForward: () => Promise<void>;
}

export interface ForwardConfirmBodyProps extends ForwardConfirmDialogProps {
  /** 取消或外壳关闭后的收尾（栈内即出栈，兼容壳即关弹窗） */
  onClose: () => void;
}

/** 内容体：不含遮罩与标题栏；提交中拦截关闭。 */
export const ForwardConfirmBody = React.forwardRef<ThreadMenuPageHandle, ForwardConfirmBodyProps>(
  function ForwardConfirmBody(
    {
      isForwarding,
      shareActionsBlocked,
      forwardError,
      setForwardConfirmOpen,
      setForwardError,
      handleConfirmForward,
      onClose
    },
    ref
  ) {
    const requestClose = useCallback(() => (isForwarding ? false : true), [isForwarding]);

    useImperativeHandle(ref, () => ({ requestClose }), [requestClose]);

    const handleCancel = useCallback(() => {
      if (!requestClose()) return;
      setForwardConfirmOpen(false);
      setForwardError(null);
      onClose();
    }, [onClose, requestClose, setForwardConfirmOpen, setForwardError]);

    return (
      <div className="flex flex-col flex-1 min-h-0 min-w-0">
        <div className="space-y-2">
          <p className="text-xs text-[var(--foreground-muted)]">
            把整段对话上下文带入一个新会话。长对话会被截断，运行中的快照可能不完整。
          </p>
        </div>
        {forwardError && (
          <p className="mt-3 text-xs text-[var(--danger-foreground)]" role="alert">
            {forwardError}
          </p>
        )}
        <div className="mt-5 flex gap-2">
          <button
            type="button"
            onClick={() => void handleConfirmForward()}
            disabled={isForwarding || shareActionsBlocked}
            className="flex-1 min-h-[44px] rounded-xl bg-[var(--primary)] text-white text-xs font-semibold disabled:opacity-40 transition active:scale-95"
          >
            {isForwarding ? '转发中…' : '转发'}
          </button>
          <button
            type="button"
            onClick={handleCancel}
            disabled={isForwarding}
            className="min-h-[44px] px-4 rounded-xl border border-[var(--border)] text-xs text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] transition active:scale-95"
          >
            取消
          </button>
        </div>
      </div>
    );
  }
);

/** 兼容外壳：保留原 props，内部用 SheetFrame + MenuSheetHeader 包住内容体。 */
export const ForwardConfirmDialog = React.memo(function ForwardConfirmDialog({
  isForwarding,
  shareActionsBlocked,
  forwardError,
  setForwardConfirmOpen,
  setForwardError,
  handleConfirmForward
}: ForwardConfirmDialogProps) {
  const bodyRef = useRef<ThreadMenuPageHandle>(null);

  const requestClose = useCallback(() => {
    if (bodyRef.current?.requestClose() === false) return;
    setForwardConfirmOpen(false);
    setForwardError(null);
  }, [setForwardConfirmOpen, setForwardError]);

  return (
    <SheetFrame size="menu" labelledBy="forward-confirm-title" onRequestClose={requestClose}>
      <MenuSheetHeader
        eyebrow="分享"
        title="转发到新会话？"
        labelledBy="forward-confirm-title"
        leading="close"
        onLeading={requestClose}
      />
      <ForwardConfirmBody
        ref={bodyRef}
        isForwarding={isForwarding}
        shareActionsBlocked={shareActionsBlocked}
        forwardError={forwardError}
        setForwardConfirmOpen={setForwardConfirmOpen}
        setForwardError={setForwardError}
        handleConfirmForward={handleConfirmForward}
        onClose={requestClose}
      />
    </SheetFrame>
  );
});
