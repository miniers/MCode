import { renderToStaticMarkup } from 'react-dom/server';
// @ts-ignore Vitest is supplied by the targeted npx test command.
import { describe, expect, it, vi } from 'vitest';
import { DiffViewer, getRevertHunkRanges } from './DiffViewer';
import { parseFilesFromUnifiedDiff } from '../utils/diff';

vi.mock('../context/RemoteContext', () => ({
  useRemoteStatic: () => ({ getDiff: async () => ({ diff: '' }) })
}));

const lf = String.fromCharCode(10);
const diffText = [
  'diff --git a/a.go b/a.go', '--- a/a.go', '+++ b/a.go', '@@ -1,5 +1,5 @@',
  ' one', '-two', '+TWO', ' three', '-four', '+FOUR', ' five'
].join(lf);

describe('DiffViewer hunk actions', () => {
  it('computes server line coordinates for separated changes and start-of-file deletion', () => {
    const file = parseFilesFromUnifiedDiff(diffText)[0];
    expect(getRevertHunkRanges(file.lines).map(({ startLine, lineCount }) => [startLine, lineCount])).toEqual([[2, 1], [4, 1]]);
    const deletion = parseFilesFromUnifiedDiff([
      'diff --git a/a.go b/a.go', '--- a/a.go', '+++ b/a.go', '@@ -1 +0,0 @@', '-old'
    ].join(lf))[0];
    expect(getRevertHunkRanges(deletion.lines).map(({ startLine, lineCount }) => [startLine, lineCount])).toEqual([[1, 0]]);
  });

  it('does not show revert actions when no callback is provided', () => {
    const html = renderToStaticMarkup(<DiffViewer diffText={diffText} />);
    expect(html).not.toContain('aria-label="撤销此块"');
  });
});
