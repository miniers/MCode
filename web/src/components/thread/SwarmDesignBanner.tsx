// 主会话视角的设计稿待确认提示条：列出等待用户确认定稿的设计子代理，可直接打开设计稿预览。

import React, { useMemo } from 'react';
import type { RemoteSwarmBee } from '../../types/swarm';
import { ChevronRightIcon, EyeIcon } from '../Icons';

interface SwarmDesignBannerProps {
  bees: RemoteSwarmBee[];
  /** 跳转到对应子代理会话并打开设计稿预览 */
  onOpenDesign: (beeThreadID: string, path: string) => void;
}

const MAX_VISIBLE_ROWS = 3;

export const SwarmDesignBanner: React.FC<SwarmDesignBannerProps> = ({
  bees,
  onOpenDesign
}) => {
  const awaitingBees = useMemo(
    () => bees.filter((bee) => Boolean(bee.awaitingFinalize && bee.designFiles && bee.designFiles.length > 0)),
    [bees]
  );

  if (awaitingBees.length === 0) return null;

  const visibleBees = awaitingBees.slice(0, MAX_VISIBLE_ROWS);
  const hiddenCount = awaitingBees.length - visibleBees.length;

  return (
    <section
      role="status"
      aria-label="设计稿待确认"
      className="shrink-0 border-t border-[var(--border)] bg-[var(--card)] px-4 py-2 space-y-1.5"
    >
      <div className="flex items-center gap-1.5 text-xs font-semibold text-[var(--foreground)]">
        <span className="grid h-4 w-4 place-items-center rounded-full bg-amber-500 text-white">
          <EyeIcon className="h-2.5 w-2.5" />
        </span>
        <span>设计稿待你确认</span>
      </div>
      {visibleBees.map((bee) => {
        const designPath = bee.designFiles![0];
        return (
          <div
            key={bee.threadID}
            className="flex flex-wrap items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-2.5 py-1.5 text-xs"
          >
            <span className="shrink-0 font-semibold text-[var(--foreground)]">
              {bee.name || `子代理 #${bee.index}`}
            </span>
            <span className="min-w-0 flex-1 basis-full truncate text-[var(--foreground-muted)] sm:basis-0" title={bee.task}>
              {bee.task}
            </span>
            <div className="flex shrink-0 items-center gap-1.5">
              <button
                type="button"
                onClick={() => onOpenDesign(bee.threadID, designPath)}
                className="inline-flex min-h-[28px] items-center gap-0.5 rounded-lg bg-[var(--primary)] px-2.5 font-medium text-white active:scale-95 transition"
              >
                <span>看稿</span>
                <ChevronRightIcon className="h-3 w-3" />
              </button>
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
