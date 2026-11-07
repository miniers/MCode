// 主会话视角的子代理提问条：列出所有等待答复的子代理，可跳转回答或转交主线程。

import React, { useEffect, useMemo, useState } from 'react';
import type { RemoteSwarmBee } from '../../types/swarm';
import { ChevronRightIcon } from '../Icons';

interface SwarmQuestionBannerProps {
  bees: RemoteSwarmBee[];
  /** 打开该子代理会话，直接回答 */
  onAnswer: (beeThreadID: string) => void;
  /** 把提问转交主线程回答 */
  onForward: (beeThreadID: string, requestID: string) => void;
}

const MAX_VISIBLE_ROWS = 3;

/** 距离自动转交的剩余秒数文案；没有 autoForwardAt 或时间无法解析时返回 null。 */
function autoForwardLabel(autoForwardAt: string | undefined, nowMs: number): string | null {
  if (!autoForwardAt) return null;
  const target = new Date(autoForwardAt).getTime();
  if (Number.isNaN(target)) return null;
  const seconds = Math.max(0, Math.ceil((target - nowMs) / 1000));
  return seconds > 0 ? `${seconds} 秒后自动转交` : '即将自动转交';
}

export const SwarmQuestionBanner: React.FC<SwarmQuestionBannerProps> = ({
  bees,
  onAnswer,
  onForward
}) => {
  const pendingBees = useMemo(() => bees.filter((bee) => Boolean(bee.pendingQuestion)), [bees]);
  const hasAutoForward = pendingBees.some((bee) => Boolean(bee.pendingQuestion?.autoForwardAt));
  const [nowMs, setNowMs] = useState(() => Date.now());

  // 只有存在倒计时目标时才需要每秒刷新，避免空闲时白跑定时器
  useEffect(() => {
    if (!hasAutoForward) return;
    setNowMs(Date.now());
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, [hasAutoForward]);

  if (pendingBees.length === 0) return null;

  const visibleBees = pendingBees.slice(0, MAX_VISIBLE_ROWS);
  const hiddenCount = pendingBees.length - visibleBees.length;

  return (
    <section
      role="status"
      aria-label="子代理提问"
      className="shrink-0 border-t border-[var(--border)] bg-[var(--card)] px-4 py-2 space-y-1.5"
    >
      <div className="flex items-center gap-1.5 text-xs font-semibold text-[var(--foreground)]">
        <span className="grid h-4 w-4 place-items-center rounded-full bg-[var(--primary)] text-mc-caption font-bold text-white">?</span>
        <span>子代理在等你回答</span>
      </div>
      {visibleBees.map((bee) => {
        const question = bee.pendingQuestion!;
        const countdown = autoForwardLabel(question.autoForwardAt, nowMs);
        return (
          <div
            key={bee.threadID}
            className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-2.5 py-1.5 text-xs"
          >
            <span className="shrink-0 font-semibold text-[var(--foreground)]">
              {bee.name || `子代理 #${bee.index}`}
            </span>
            <span className="min-w-0 flex-1 basis-full truncate text-[var(--foreground-muted)] sm:basis-0" title={question.prompt}>
              {question.prompt}
            </span>
            {(countdown || question.forwarded) && (
              <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">
                {countdown ?? '已转交主线程'}
              </span>
            )}
            <div className="flex shrink-0 items-center gap-1.5">
              <button
                type="button"
                onClick={() => onAnswer(bee.threadID)}
                className="inline-flex min-h-[28px] items-center gap-0.5 rounded-lg bg-[var(--primary)] px-2.5 font-medium text-white active:scale-95 transition"
              >
                <span>去回答</span>
                <ChevronRightIcon className="h-3 w-3" />
              </button>
              {question.forwarded ? (
                <span className="inline-flex min-h-[28px] items-center rounded-lg border border-[var(--border)] px-2.5 text-mc-caption font-medium text-[var(--foreground-muted)]">
                  已转交
                </span>
              ) : (
                <button
                  type="button"
                  onClick={() => onForward(bee.threadID, question.requestID)}
                  className="min-h-[28px] rounded-lg border border-[var(--border)] bg-[var(--card)] px-2.5 font-medium text-[var(--foreground)] hover:bg-[var(--user-bubble)] active:scale-95 transition"
                >
                  交给主线程回答
                </button>
              )}
            </div>
          </div>
        );
      })}
      {hiddenCount > 0 && (
        <div className="text-mc-caption text-[var(--foreground-muted)]">还有 {hiddenCount} 个</div>
      )}
    </section>
  );
};
