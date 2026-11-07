import React from 'react';
import { useThreadChangeStats } from '../../hooks/useThreadChangeStats';
import {
  formatChangeStatsAdditions,
  formatChangeStatsDeletions,
  formatChangeStatsFileCount,
  formatChangeStatsLabel,
  hasChangeStats
} from '../../utils/diffStats';

export interface ThreadChangeStatsProps {
  threadID: string | null;
  isRunning: boolean;
  /** 点击回调：由接线方决定打开 Git 面板还是审阅面板；不传时胶囊只做展示。 */
  onOpen?: () => void;
}

/**
 * 会话头部的改动统计胶囊，对应桌面端 renderThreadChangesTab
 * （internal/ui/composer_changes_tab.go）：左侧「N 个文件」+ 增删行数，点击打开改动面板。
 * 窄屏只显示文件数，增删行数收进 sm 以上。
 * 零改动（files 为 0）时不渲染，与桌面端 composer.go 中 `files > 0` 的判断一致。
 */
export const ThreadChangeStats: React.FC<ThreadChangeStatsProps> = ({ threadID, isRunning, onOpen }) => {
  const { additions, deletions, files } = useThreadChangeStats(threadID, isRunning);
  if (!hasChangeStats({ additions, deletions, files })) return null;

  const fileCountLabel = formatChangeStatsFileCount(files);
  const additionsLabel = formatChangeStatsAdditions(additions);
  const deletionsLabel = formatChangeStatsDeletions(deletions);
  const summaryLabel = formatChangeStatsLabel({ additions, deletions, files });

  return (
    <button
      type="button"
      onClick={onOpen}
      title="查看改动"
      aria-label={`查看改动 ${summaryLabel}`}
      className="h-6 rounded-full px-2 inline-flex items-center gap-1.5 shrink-0 border border-[var(--border)] bg-[var(--card)] text-mc-caption font-medium text-[var(--foreground)] transition hover:bg-[var(--card-alt,var(--card))] active:scale-95"
    >
      <span>{fileCountLabel}</span>
      {additionsLabel && (
        <span className="hidden sm:inline text-mc-caption font-medium text-emerald-600 dark:text-emerald-400">
          {additionsLabel}
        </span>
      )}
      {deletionsLabel && (
        <span className="hidden sm:inline text-mc-caption font-medium text-rose-600 dark:text-rose-400">
          {deletionsLabel}
        </span>
      )}
    </button>
  );
};

export default ThreadChangeStats;
