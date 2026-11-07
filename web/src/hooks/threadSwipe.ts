// 会话行左滑跟手（W-10）：前 96px 线性跟随，之后的行程乘 0.3 做阻尼，不硬停。
export const SWIPE_REVEAL_WIDTH = 96;
export const SWIPE_OPEN_THRESHOLD = 42;
export const SWIPE_DAMPING = 0.3;

export function dampOffset(raw: number): number {
  if (raw >= 0) return 0;
  if (raw >= -SWIPE_REVEAL_WIDTH) return raw;
  const overshoot = raw + SWIPE_REVEAL_WIDTH;
  return -SWIPE_REVEAL_WIDTH + overshoot * SWIPE_DAMPING;
}
