// 触感反馈（W-18）：只走 Web Vibration API。
// iOS Safari 没有 navigator.vibrate，这里静默跳过，不做任何降级；开关存在本地，默认开启。
import { getStoredHapticsEnabled } from '../services/storage';

export type HapticKind = 'light' | 'medium';

export const HAPTIC_DURATION_MS: Record<HapticKind, number> = {
  light: 8,
  medium: 16
};

// 测试与设置页可以传入假实现，避免依赖真实 navigator。
export interface HapticVibrator {
  vibrate?: (pattern: number) => unknown;
}

export function haptic(
  kind: HapticKind,
  vibrator: HapticVibrator | null | undefined = typeof navigator === 'undefined' ? undefined : navigator,
  enabled: boolean = getStoredHapticsEnabled()
): void {
  if (!enabled) return;
  if (!vibrator) return;
  try {
    vibrator.vibrate?.(HAPTIC_DURATION_MS[kind]);
  } catch {
    // 部分浏览器在没有用户手势时会抛错，触感失败不应影响交互。
  }
}

// 左滑越过吸附阈值的那一下打一次：只在「从阈值内跨到阈值外」时返回 true。
// 停留在阈值外继续拖动、或退回阈值内都不会重复触发；退回后再越过才算新的一次。
export function shouldHapticOnCross(
  previousOffset: number,
  nextOffset: number,
  threshold: number
): boolean {
  return previousOffset > threshold && nextOffset <= threshold;
}
