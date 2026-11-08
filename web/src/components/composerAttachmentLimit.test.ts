import { composerAttachmentLimit } from './ThreadComposer';

function assertLimit(limit: number | undefined, expected: number): void {
  const actual = composerAttachmentLimit(limit);
  if (actual !== expected) {
    throw new Error(`期望附件上限为 ${expected}，实际为 ${actual}`);
  }
}

assertLimit(undefined, 4);
assertLimit(10, 10);
