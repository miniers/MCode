import { renderToStaticMarkup } from 'react-dom/server';
import { TurnFilesCard, type TurnEditedFile } from './TurnFilesCard';

const longFile: TurnEditedFile = {
  path: 'docs/specs/harness-policy-version-2026-11-15-woolly-parnas.md',
  name: 'harness-policy-version-2026-11-15-woolly-parnas.md',
  dir: 'docs/specs',
  additions: 12,
  deletions: 3,
  diff: {
    id: 'docs/specs/harness-policy-version-2026-11-15-woolly-parnas.md',
    path: 'docs/specs/harness-policy-version-2026-11-15-woolly-parnas.md',
    additions: 12,
    deletions: 3,
    lines: [{ type: 'add', text: '+policy line' }],
    truncated: false
  }
};

const markup = renderToStaticMarkup(<TurnFilesCard files={[longFile]} />);
const nameMatch = markup.match(
  /<span\b([^>]*)>harness-policy-version-2026-11-15-woolly-parnas\.md<\/span>/
);
if (!nameMatch) {
  throw new Error('改动文件卡未渲染文件名元素');
}

const attrs = nameMatch[1];
const classMatch = attrs.match(/\bclass="([^"]*)"/);
const classes = (classMatch?.[1] ?? '').split(/\s+/).filter(Boolean);
if (!classes.includes('truncate') || !classes.includes('min-w-0')) {
  throw new Error(`文件名元素缺少 truncate 或 min-w-0：${classes.join(' ')}`);
}
if (classes.includes('shrink-0')) {
  throw new Error(`文件名元素不得包含 shrink-0：${classes.join(' ')}`);
}
if (!classes.includes('font-mono') || !classes.includes('font-medium')) {
  throw new Error(`文件名元素缺少字体样式类：${classes.join(' ')}`);
}
if (!attrs.includes(`title="${longFile.path}"`)) {
  throw new Error('文件名元素缺少完整路径的 title 属性');
}

// 同一行的目录可收缩、增删数保持 shrink-0，避免窄屏下横向撑破
if (!markup.includes('min-w-0 flex-1 truncate font-mono text-mc-caption')) {
  throw new Error('目录元素缺少收缩与截断样式');
}
if (!markup.includes('shrink-0 font-mono text-mc-caption')) {
  throw new Error('增删数元素未保持 shrink-0');
}
