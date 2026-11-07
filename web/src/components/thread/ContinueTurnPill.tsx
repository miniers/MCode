import React, { useCallback, useState } from 'react';
import {
  CONTINUE_PROMPT,
  continuePillLabels,
  dismissContinuePill,
  isContinueDismissed,
  shouldShowContinuePill
} from '../../utils/continuation';

/**
 * 输入框上方的「继续」胶囊：上一轮被中断或失败结束时可一键续跑。
 * 显示条件与文案照桌面端 internal/ui/composer_continue.go 的 shouldShowContinuePill；
 * 点击发出的正文是桌面端 continueLastTurn 发送的 continuePrompt（CONTINUE_PROMPT）。
 * 桌面端胶囊只在草稿为空、未在编辑排队消息时出现，这两条由调用方在渲染处自行决定。
 */
export interface ContinueTurnPillProps {
  lastStatus: string | null | undefined;
  isRunning: boolean;
  locallyInterrupted: boolean;
  lastMessageID: string | undefined;
  lastMessageIsError: boolean;
  onContinue: (prompt: string) => void;
  threadID: string;
}

const PlayIcon = () => (
  <svg aria-hidden="true" className="h-[10px] w-[10px]" viewBox="0 0 24 24" fill="currentColor">
    <path d="M8 5.14v13.72a1 1 0 0 0 1.52.85l11-6.86a1 1 0 0 0 0-1.7l-11-6.86A1 1 0 0 0 8 5.14Z" />
  </svg>
);

const CloseIcon = () => (
  <svg
    aria-hidden="true"
    className="h-[10px] w-[10px]"
    viewBox="0 0 24 24"
    fill="none"
    strokeWidth="2.2"
    stroke="currentColor"
  >
    <path strokeLinecap="round" d="M5 5l14 14M19 5 5 19" />
  </svg>
);

export const ContinueTurnPill: React.FC<ContinueTurnPillProps> = ({
  lastStatus,
  isRunning,
  locallyInterrupted,
  lastMessageID,
  lastMessageIsError,
  onContinue,
  threadID
}) => {
  // 忽略记录按 threadID 记在模块级 Map 里，最后一条消息变化后自动失效；
  // 本地再记一份已忽略的消息 ID，让点击当场生效，不必等父组件重算。
  const [dismissedID, setDismissedID] = useState<string | null>(null);

  const handleDismiss = useCallback(() => {
    dismissContinuePill(threadID, lastMessageID);
    setDismissedID(lastMessageID ?? '');
  }, [lastMessageID, threadID]);

  const handleContinue = useCallback(() => {
    onContinue(CONTINUE_PROMPT);
  }, [onContinue]);

  const dismissed = dismissedID === (lastMessageID ?? '') || isContinueDismissed(threadID, lastMessageID);
  const { show, kind } = shouldShowContinuePill({
    locallyInterrupted,
    lastStatus,
    isRunning,
    dismissed,
    lastMessageID,
    lastMessageIsError
  });

  if (!threadID || !show || kind === 'none') return null;

  const { status, tooltip } = continuePillLabels(kind);

  return (
    <div className="flex items-center gap-1.5 px-1 pb-1.5">
      <span className="text-mc-footnote text-[var(--foreground-muted)]">{status}</span>
      <button
        type="button"
        onClick={handleContinue}
        title={tooltip}
        className="inline-flex min-h-[24px] shrink-0 items-center gap-1 rounded-full border border-[var(--border)] bg-[var(--card)] px-2 text-mc-footnote font-medium text-[var(--foreground)] transition hover:bg-[var(--user-bubble)] active:scale-95"
      >
        <span className="text-[var(--primary)]">
          <PlayIcon />
        </span>
        继续
      </button>
      <button
        type="button"
        onClick={handleDismiss}
        aria-label="关闭继续提示"
        title="关闭"
        className="grid h-[18px] w-[18px] shrink-0 place-items-center rounded-full text-[var(--foreground-muted)] transition hover:bg-[var(--user-bubble)] hover:text-[var(--foreground)] active:scale-95"
      >
        <CloseIcon />
      </button>
    </div>
  );
};
