import React from 'react';
import { CheckCircleIcon } from '../Icons';

// 回合完成统计条：一条细分割线 + 完成图标 + 低调小字（耗时、工具调用、失败数、结束时间）。
// 参考桌面端 internal/ui/timeline_rows.go 的 renderTurnFinishedBlock。

export interface TurnFinishedStats {
  /** 回合首条消息到最后一次活动（含工具完成时间）的跨度，毫秒 */
  durationMs: number;
  /** 本回合工具调用次数 */
  toolCount: number;
  /** 本回合失败的工具调用次数，0 时不显示 */
  failedCount: number;
  /** 回合最后活动时间（ISO8601），按本地时区显示 */
  endedAt: string;
}

/** 时长文本：不足 1 秒说「不到 1 秒」，10 秒内保留一位小数，超过 1 分钟拆成分秒。 */
export function formatTurnDuration(durationMs: number): string {
  if (!Number.isFinite(durationMs) || durationMs <= 0) return '';
  if (durationMs < 1000) return '不到 1 秒';
  if (durationMs < 10000) return `${(durationMs / 1000).toFixed(1)} 秒`;
  const totalSeconds = Math.round(durationMs / 1000);
  if (totalSeconds < 60) return `${totalSeconds} 秒`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  if (seconds === 0) return `${minutes} 分`;
  return `${minutes} 分 ${seconds} 秒`;
}

/** 结束时间：本地时区的「M月D日 HH:mm」，解析失败返回空串。 */
export function formatTurnEndClock(iso: string): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return '';
  const hours = date.getHours().toString().padStart(2, '0');
  const minutes = date.getMinutes().toString().padStart(2, '0');
  return `${date.getMonth() + 1}月${date.getDate()}日 ${hours}:${minutes}`;
}

export const TurnFinishedBar: React.FC<{ stats: TurnFinishedStats }> = ({ stats }) => {
  const parts: React.ReactNode[] = [];
  const duration = formatTurnDuration(stats.durationMs);
  if (duration) parts.push(<span key="duration">耗时 {duration}</span>);
  if (stats.toolCount > 0) parts.push(<span key="tools">工具调用 {stats.toolCount} 次</span>);
  if (stats.failedCount > 0) {
    parts.push(
      <span key="failed" className="text-[var(--danger-foreground)]">
        失败 {stats.failedCount}
      </span>
    );
  }
  const clock = formatTurnEndClock(stats.endedAt);
  if (clock) parts.push(<span key="clock">结束 {clock}</span>);
  if (parts.length === 0) return null;

  return (
    <div className="mb-4 flex min-w-0 items-center gap-2 text-mc-caption text-[var(--foreground-muted)] select-text">
      <span className="h-px min-w-4 flex-1 bg-[var(--border)]" aria-hidden="true" />
      <CheckCircleIcon className="h-3 w-3 shrink-0 text-[var(--primary)]" />
      <span className="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-0.5">
        {parts.map((part, index) => (
          <React.Fragment key={index}>
            {index > 0 && <span className="opacity-50">·</span>}
            {part}
          </React.Fragment>
        ))}
      </span>
    </div>
  );
};

TurnFinishedBar.displayName = 'TurnFinishedBar';
