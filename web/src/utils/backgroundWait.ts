// 回合已结束但后台任务还在跑：输入框占位与顶栏状态都说明「在等后台任务」，
// 否则用户会以为会话彻底结束、可以关掉走人了。
// 「忙时不重复提示」的口径与会话列表行尾状态共用 threadRowStatus.ts 里的一份实现。
import type { RemoteThreadSummary } from '../types/contract';
import { threadBackgroundWaitCount } from './threadRowStatus';

export const BACKGROUND_WAIT_PLACEHOLDER = '后台任务运行中，完成后自动继续；也可以直接输入';
const IDLE_COMPOSER_PLACEHOLDER = '继续输入消息…';

export function backgroundWaitCount(summary: RemoteThreadSummary | null | undefined): number {
  return summary ? threadBackgroundWaitCount(summary) : 0;
}

export function composerPlaceholderFor(waitCount: number): string {
  return waitCount > 0 ? BACKGROUND_WAIT_PLACEHOLDER : IDLE_COMPOSER_PLACEHOLDER;
}

export function backgroundWaitStatusText(waitCount: number): string {
  return `等待后台 · ${waitCount} 项`;
}
