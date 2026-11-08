// 将密集的更新合并到下一帧执行。
export function createFrameCoalescer(
  flush: () => void,
  options: {
    requestFrame?: (callback: FrameRequestCallback) => number;
    cancelFrame?: (handle: number) => void;
    isHidden?: () => boolean;
  } = {}
) {
  const requestFrame = options.requestFrame ?? ((callback) => window.requestAnimationFrame(callback));
  const cancelFrame = options.cancelFrame ?? ((handle) => window.cancelAnimationFrame(handle));
  const isHidden = options.isHidden ?? (() => document.visibilityState === 'hidden');
  let pending: number | null = null;

  const cancel = () => {
    if (pending === null) return;
    cancelFrame(pending);
    pending = null;
  };

  const flushNow = () => {
    if (pending === null) return;
    cancel();
    flush();
  };

  return {
    schedule() {
      if (isHidden()) {
        cancel();
        flush();
        return;
      }
      if (pending !== null) return;
      pending = requestFrame(() => {
        pending = null;
        flush();
      });
    },
    flushNow,
    cancel
  };
}
