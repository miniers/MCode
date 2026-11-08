import { createFrameCoalescer } from './frameCoalescer';

let nextHandle = 0;
let hidden = false;
let flushes = 0;
const frames = new Map<number, FrameRequestCallback>();
const coalescer = createFrameCoalescer(() => { flushes += 1; }, {
  requestFrame: (callback) => {
    const handle = ++nextHandle;
    frames.set(handle, callback);
    return handle;
  },
  cancelFrame: (handle) => { frames.delete(handle); },
  isHidden: () => hidden
});

function expect(condition: boolean, message: string) {
  if (!condition) throw new Error(message);
}

function runFrame() {
  const callbacks = [...frames.values()];
  frames.clear();
  callbacks.forEach((callback) => callback(0));
}

coalescer.schedule();
coalescer.schedule();
expect(frames.size === 1 && flushes === 0, '同一帧只应安排一次刷新');
runFrame();
expect(flushes === 1, '帧执行时应刷新一次');
coalescer.schedule();
runFrame();
expect(flushes === 2, '下一帧应能再次刷新');
coalescer.schedule();
coalescer.cancel();
runFrame();
expect(flushes === 2 && frames.size === 0, '取消后不应刷新');
coalescer.flushNow();
expect(flushes === 2, '没有待执行帧时不应刷新');
coalescer.schedule();
coalescer.flushNow();
expect(flushes === 3 && frames.size === 0, '立即刷新应取消待执行帧');
runFrame();
expect(flushes === 3, '已取消的帧不应再次刷新');
coalescer.schedule();
hidden = true;
coalescer.schedule();
expect(flushes === 4 && frames.size === 0, '隐藏页面应同步刷新并取消待执行帧');
coalescer.schedule();
expect(flushes === 5 && frames.size === 0, '隐藏页面的后续更新应同步刷新');
