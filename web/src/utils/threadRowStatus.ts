// 会话行尾状态位：与桌面端 internal/ui/sidebar.go:1008 的 sidebarStatusSlotKind 保持一致，
// 行尾按优先级只展示一个状态位：待审批 > 等待后台任务 > 未读 > 置顶 > 最近活动时间。
// 运行中 / 待回答 / 失败在桌面端走行首图标位（internal/ui/sidebar.go:905 的状态前缀），不占行尾，
// 因此不进入这条优先级链；它们由组件自己渲染在行首。

import type { RemoteThreadSummary } from '../types/contract';
import { isThreadBusy } from '../context/remoteGuards';

export type ThreadRowStatus =
  | { kind: 'pending' }
  | { kind: 'waiting'; count: number }
  | { kind: 'unread' }
  | { kind: 'pinned' }
  | { kind: 'activity'; text: string }
  | { kind: 'none' };

// 桌面端 threadActivityTime（internal/ui/model.go:181）：优先取用户最后一次发消息的时间，
// 缺失或无法解析时回退到 updatedAt。
function threadActivitySource(thread: RemoteThreadSummary): string {
  return thread.lastUserMessageAt || thread.updatedAt || '';
}

// 桌面端 formatRelativeTime（internal/ui/sidebar.go:37）的同一套格式：
// 刚刚 / N分钟前 / N小时前 / N天前（一周内）/ MM-DD。now 显式传入，便于纯函数断言。
export function formatThreadActivityTime(
  isoString: string,
  now: number | Date = Date.now()
): string {
  if (!isoString) return '';
  const date = new Date(isoString);
  if (Number.isNaN(date.getTime())) return '';
  const nowMs = typeof now === 'number' ? now : now.getTime();
  const diffMs = nowMs - date.getTime();
  // 时间戳在未来时差值可能为负，桌面端用 time.Since 也归到「刚刚」。
  if (diffMs < 60_000) return '刚刚';
  const minutes = Math.floor(diffMs / 60_000);
  if (minutes < 60) return `${minutes}分钟前`;
  const hours = Math.floor(diffMs / 3_600_000);
  if (hours < 24) return `${hours}小时前`;
  const days = Math.floor(diffMs / 86_400_000);
  if (days < 7) return `${days}天前`;
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${month}-${day}`;
}

// 待审批判定：桌面端看 HasPendingRequest，Web 摘要里另有 attention='approval' 的展示态，两者都算。
export function threadHasPendingApproval(thread: RemoteThreadSummary): boolean {
  return thread.hasPendingRequest || thread.attention === 'approval';
}

// 回合已经结束但仍有后台任务在跑：桌面端 sidebarStatusSlotKind 把这种情况排在未读之前。
// 忙的时候不重复提示，运行中由行首状态负责。
export function threadBackgroundWaitCount(thread: RemoteThreadSummary): number {
  if (isThreadBusy(thread)) return 0;
  return Math.max(0, thread.backgroundTasks ?? 0);
}

export function threadRowStatus(
  thread: RemoteThreadSummary,
  now: number | Date = Date.now()
): ThreadRowStatus {
  if (threadHasPendingApproval(thread)) return { kind: 'pending' };
  const waitCount = threadBackgroundWaitCount(thread);
  if (waitCount > 0) return { kind: 'waiting', count: waitCount };
  // 仍在忙的会话不算未读（桌面端 hasThreadUnread，internal/ui/sidebar.go:114），跑完再看。
  if (thread.hasUnread && !isThreadBusy(thread)) return { kind: 'unread' };
  if (thread.isPinned) return { kind: 'pinned' };
  const text = formatThreadActivityTime(threadActivitySource(thread), now);
  if (text) return { kind: 'activity', text };
  return { kind: 'none' };
}

// 行尾只画一个位，读屏仍要能读到全部状态：拼成一句话挂在行的 aria-label 上。
export function threadRowStatusAriaLabel(
  thread: RemoteThreadSummary,
  now: number | Date = Date.now()
): string {
  const parts: string[] = [];
  if (threadHasPendingApproval(thread)) parts.push('待审批');
  if (thread.attention === 'input') parts.push('待回答');
  if (thread.attention === 'failed') parts.push('失败');
  if (isThreadBusy(thread)) parts.push('运行中');
  const waitCount = threadBackgroundWaitCount(thread);
  if (waitCount > 0) parts.push(`等待 ${waitCount} 个后台任务`);
  if (thread.hasUnread) parts.push('未读');
  if (thread.isPinned) parts.push('置顶');
  const text = formatThreadActivityTime(threadActivitySource(thread), now);
  if (text) parts.push(`最近活动 ${text}`);
  return parts.join('，');
}
