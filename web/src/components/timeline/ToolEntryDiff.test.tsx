import { renderToStaticMarkup } from 'react-dom/server';
import { parseToolEditDiff, ToolEntryDiff } from './ToolEntryDiff';

for (const count of [200, 205]) {
  const content = Array.from({ length: count }, (_, index) => `line ${index + 1}`).join('\n');
  const files = parseToolEditDiff('write', JSON.stringify({ file_path: 'test.txt', content }));
  if (!files) throw new Error('未能解析 diff');
  const markup = renderToStaticMarkup(<ToolEntryDiff files={files} />);
  if (count > 200 && !markup.includes('显示其余 5 行')) throw new Error('未显示正确的剩余行数');
  if (count <= 200 && markup.includes('显示其余')) throw new Error('短 diff 不应显示展开按钮');
}
