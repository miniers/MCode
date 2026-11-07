// 回到前台或重连后首次同步失败时的退避节奏：1s、2s、4s，用完即放弃并保留错误提示。
const RESUME_RETRY_DELAYS = [1000, 2000, 4000] as const;

// attempt 从 1 开始；返回 null 表示不再重试。
export function resumeRetryDelay(attempt: number): number | null {
  if (!Number.isFinite(attempt)) return null;
  const index = Math.floor(attempt) - 1;
  if (index < 0 || index >= RESUME_RETRY_DELAYS.length) return null;
  return RESUME_RETRY_DELAYS[index];
}
