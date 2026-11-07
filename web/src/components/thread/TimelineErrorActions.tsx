import React, { useCallback, useEffect, useRef, useState } from 'react';
import type { ThreadMessage } from '../ThreadTimeline';
import {
  CAPACITY_RETRY_MAX,
  capacityNoticeKind,
  capacityRetrySeconds,
  cancelCapacityRetry,
  continuationPromptFor,
  errorRetryVisible,
  tickCapacityRetry
} from '../../utils/continuation';

/**
 * 时间线错误消息下方的「继续」：点击发一条从中断处继续的提示，不重发上一条用户消息。
 * 满载/限流类错误在「仍是最新一条且没在跑」时自动开始倒计时，到点自动发送，可随时取消。
 * 桌面端对应 internal/ui/timeline_error_retry.go（续跑提示与退避）与
 * internal/ui/timeline_error_row.go（错误卡片里的「继续」按钮）。
 */
export interface TimelineErrorActionsProps {
  message: ThreadMessage;
  isLast: boolean;
  isRunning: boolean;
  onContinue: (prompt: string) => Promise<void> | void;
  /** 退避状态按 threadID + messageID 记录；会话 ID 全局唯一，缺省时按消息 ID 记。 */
  threadID?: string;
}

export const TimelineErrorActions: React.FC<TimelineErrorActionsProps> = ({
  message,
  isLast,
  isRunning,
  onContinue,
  threadID
}) => {
  const messageID = message.id ?? '';
  const retryThreadKey = threadID ?? '';
  const capacityKind = capacityNoticeKind(message);
  const visible = errorRetryVisible(isLast, isRunning);

  const [remainingMs, setRemainingMs] = useState(0);
  const [attempts, setAttempts] = useState(0);
  const [sending, setSending] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const messageRef = useRef(message);
  messageRef.current = message;
  const onContinueRef = useRef(onContinue);
  onContinueRef.current = onContinue;
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const clearTimer = useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  // 与桌面端 tickCapacityRetry 同节奏：到点自动发续跑提示，等待期间只刷新倒计时。
  useEffect(() => {
    clearTimer();
    if (!capacityKind || !visible || !messageID) {
      setRemainingMs(0);
      return;
    }
    let disposed = false;
    const step = () => {
      timerRef.current = null;
      const current = messageRef.current;
      const result = tickCapacityRetry({
        threadID: retryThreadKey,
        message: current,
        isLast,
        running: isRunning
      });
      if (disposed) return;
      setAttempts(result.attempts);
      setRemainingMs(result.armed ? Math.max(0, result.remainingMs) : 0);
      if (result.shouldSend) {
        void onContinueRef.current(continuationPromptFor(current));
        return;
      }
      if (result.armed) {
        // 半秒一跳，倒计时才会逐秒变化；到点那次由 tick 自己判 due。
        timerRef.current = setTimeout(step, Math.max(250, Math.min(result.remainingMs, 500)));
      }
    };
    step();
    return () => {
      disposed = true;
      clearTimer();
    };
  }, [attempts, capacityKind, clearTimer, isLast, isRunning, messageID, retryThreadKey, visible]);

  const send = useCallback((prompt: string) => {
    setSending(true);
    void Promise.resolve(onContinueRef.current(prompt)).finally(() => {
      if (mountedRef.current) setSending(false);
    });
  }, []);

  const handleContinue = useCallback(() => {
    // 手动点了就不再等自动重试，避免两条续跑提示一起发出去。
    cancelCapacityRetry(retryThreadKey, messageID);
    clearTimer();
    setRemainingMs(0);
    send(continuationPromptFor(messageRef.current));
  }, [clearTimer, messageID, retryThreadKey, send]);

  const handleCancelRetry = useCallback(() => {
    cancelCapacityRetry(retryThreadKey, messageID);
    clearTimer();
    setRemainingMs(0);
  }, [clearTimer, messageID, retryThreadKey]);

  if (!visible) return null;

  const armed = remainingMs > 0;
  const seconds = capacityRetrySeconds(remainingMs);
  const attemptLabel = Math.min(attempts + 1, CAPACITY_RETRY_MAX);

  return (
    <div className="mt-2 flex flex-wrap items-center gap-2">
      {armed && (
        <span className="text-mc-caption text-[var(--foreground-muted)]">
          {`将在 ${seconds} 秒后自动重试（第 ${attemptLabel}/${CAPACITY_RETRY_MAX} 次）`}
        </span>
      )}
      <button
        type="button"
        onClick={handleContinue}
        disabled={sending}
        title="从中断处继续执行，不重发上一条消息"
        className="inline-flex min-h-[28px] shrink-0 items-center gap-1.5 rounded-full border border-[var(--border)] bg-[var(--card)] px-2.5 text-mc-caption font-semibold text-[var(--foreground)] transition hover:bg-[var(--user-bubble)] active:scale-95 disabled:opacity-40"
      >
        {sending ? '继续中…' : '继续'}
      </button>
      {armed && (
        <button
          type="button"
          onClick={handleCancelRetry}
          className="inline-flex min-h-[28px] shrink-0 items-center rounded-full px-2 text-mc-caption text-[var(--foreground-muted)] transition hover:text-[var(--foreground)] active:scale-95"
        >
          取消自动重试
        </button>
      )}
    </div>
  );
};
