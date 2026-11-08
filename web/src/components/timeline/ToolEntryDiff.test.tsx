import { renderToStaticMarkup } from 'react-dom/server';
import { parseToolEditDiff, toolDiffVisibleLines, ToolEntryDiff } from './ToolEntryDiff';
import type { ToolDiffFile } from './ToolEntryDiff';

for (const count of [200, 205]) {
  const content = Array.from({ length: count }, (_, index) => `line ${index + 1}`).join('\n');
  const files = parseToolEditDiff('write', JSON.stringify({ file_path: 'test.txt', content }));
  if (!files) throw new Error('未能解析 diff');
  const markup = renderToStaticMarkup(<ToolEntryDiff files={files} />);
  if (count > 200 && !markup.includes('显示其余 5 行')) throw new Error('未显示正确的剩余行数');
  if (count <= 200 && markup.includes('显示其余')) throw new Error('短 diff 不应显示展开按钮');
}

// 展开后首次最多渲染 2000 行，剩余行数交给末尾按钮。
const bigLines: ToolDiffFile['lines'] = Array.from({ length: 2500 }, (_, index) => ({
  type: 'add',
  text: `line ${index + 1}`
}));
const bigFile: ToolDiffFile = {
  id: 'big',
  path: 'big.txt',
  additions: 2500,
  deletions: 0,
  lines: bigLines.slice(0, 200),
  truncated: true,
  fullLines: bigLines
};
const expanded = toolDiffVisibleLines(bigFile, true, false);
if (expanded.lines.length !== 2000 || expanded.hidden !== 500) throw new Error('展开后应只渲染前 2000 行');
const shownAll = toolDiffVisibleLines(bigFile, true, true);
if (shownAll.lines.length !== 2500 || shownAll.hidden !== 0) throw new Error('点击后应渲染全部行');
const collapsed = toolDiffVisibleLines(bigFile, false, false);
if (collapsed.lines.length !== 200 || collapsed.hidden !== 0) throw new Error('收起时仍渲染前 200 行');
