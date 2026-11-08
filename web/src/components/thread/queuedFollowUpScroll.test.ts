// web/ 没有测试运行器，顶层直接断言，失败时 throw。
import { followUpScrollTop } from './queuedFollowUpScroll';

function assertScrollTop(actual: number | null, expected: number | null, label: string): void {
  if (actual !== expected) throw new Error(`${label} 期望 ${expected}，实际 ${actual}`);
}

const viewport = { scrollTop: 100, clientHeight: 200 };
assertScrollTop(followUpScrollTop(viewport, { offsetTop: 150, height: 50 }), null, '完整可见');
assertScrollTop(followUpScrollTop(viewport, { offsetTop: 50, height: 30 }), 50, '在上方');
assertScrollTop(followUpScrollTop(viewport, { offsetTop: 280, height: 50 }), 130, '在下方');
