import { isImeComposing } from './ime';

function assertComposing(event: Partial<KeyboardEvent>, expected: boolean): void {
  const actual = isImeComposing(event as KeyboardEvent);
  if (actual !== expected) {
    throw new Error(`期望输入法组合状态为 ${expected}，实际为 ${actual}`);
  }
}

assertComposing({ isComposing: true, keyCode: 13 }, true);
assertComposing({ isComposing: false, keyCode: 229 }, true);
assertComposing({ isComposing: false, keyCode: 13, key: 'Enter' }, false);
