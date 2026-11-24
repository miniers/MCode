import { renderToStaticMarkup } from 'react-dom/server';
import type { Root as MdastRoot } from 'mdast';
import { RemoteProvider } from '../context/RemoteContext';
import { resolveMarkdownImageSource } from './MarkdownImage';
import { extractImagesFromHtml, MarkdownView, remarkHtmlImages } from './MarkdownView';

function assertEqual<T>(actual: T, expected: T, message: string): void {
  const actualJson = JSON.stringify(actual);
  const expectedJson = JSON.stringify(expected);
  if (actualJson !== expectedJson) {
    throw new Error(`${message}: 期望 ${expectedJson}，实际 ${actualJson}`);
  }
}

function assertMatch(actual: string, pattern: RegExp, message: string): void {
  if (!pattern.test(actual)) {
    throw new Error(`${message}: ${actual}`);
  }
}

const renderMarkdown = (text: string, baseDir?: string): string =>
  renderToStaticMarkup(
    <RemoteProvider>
      <MarkdownView text={text} baseDir={baseDir} />
    </RemoteProvider>
  );

const codeHtml = renderToStaticMarkup(<MarkdownView text={'```js\nconst value = 1;\n```'} />);
if (!/<button\b[^>]*aria-label="复制代码"/.test(codeHtml)) {
  throw new Error('代码块应包含复制代码按钮');
}

const plainHtml = renderToStaticMarkup(<MarkdownView text="普通文本" />);
if (plainHtml.includes('aria-label="复制代码"')) {
  throw new Error('没有代码块时不应包含复制代码按钮');
}

// (a) 块级 <img src="a.png" alt="x"> 转为图片
{
  const html = renderMarkdown('<img src="a.png" alt="x" width="100%">');
  assertMatch(html, /<img\b[^>]*src="a\.png"[^>]*alt="x"/, '块级 img 应渲染为图片');

  const tree: MdastRoot = {
    type: 'root',
    children: [{ type: 'html', value: '<img src="a.png" alt="x">' }]
  };
  remarkHtmlImages({ isDark: false })(tree);
  assertEqual(
    tree.children,
    [{ type: 'paragraph', children: [{ type: 'image', url: 'a.png', alt: 'x' }] }],
    '块级 html 应转为包含 image 的 paragraph'
  );
}

// (b) <picture> 深色/浅色各取对应地址
{
  const pictureMd = [
    '<picture>',
    '  <source media="(prefers-color-scheme: dark)" srcset="docs/images/hero-dark.png 1x, docs/images/hero-dark@2x.png 2x">',
    '  <source media="(prefers-color-scheme: light)" srcset="docs/images/hero-light-source.png">',
    '  <img src="docs/images/hero-light.png" alt="Hero">',
    '</picture>'
  ].join('\n');

  assertEqual(
    extractImagesFromHtml(pictureMd, true),
    [{ url: 'docs/images/hero-dark.png', alt: 'Hero' }],
    '深色模式应取 dark source 的 srcset 首地址'
  );
  assertEqual(
    extractImagesFromHtml(pictureMd, false),
    [{ url: 'docs/images/hero-light-source.png', alt: 'Hero' }],
    '浅色模式应取 light source 的 srcset 首地址'
  );

  const fallbackLightOnly = [
    '<picture>',
    '  <source media="(prefers-color-scheme: dark)" srcset="dark.png">',
    '  <img src="light.png" alt="Fallback">',
    '</picture>'
  ].join('\n');
  assertEqual(
    extractImagesFromHtml(fallbackLightOnly, false),
    [{ url: 'light.png', alt: 'Fallback' }],
    '缺少对应 source 时应回退到 img src'
  );

  const originalMatchMedia = window.matchMedia;
  try {
    window.matchMedia = ((query: string) => ({
      matches: query.includes('dark'),
      media: query,
      onchange: null,
      addListener() {},
      removeListener() {},
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {
        return false;
      }
    })) as typeof window.matchMedia;
    const darkHtml = renderMarkdown(pictureMd);
    assertMatch(darkHtml, /<img\b[^>]*src="docs\/images\/hero-dark\.png"[^>]*alt="Hero"/, '深色模式应渲染深色图片');

    window.matchMedia = ((query: string) => ({
      matches: query.includes('light'),
      media: query,
      onchange: null,
      addListener() {},
      removeListener() {},
      addEventListener() {},
      removeEventListener() {},
      dispatchEvent() {
        return false;
      }
    })) as typeof window.matchMedia;
    const lightHtml = renderMarkdown(pictureMd);
    assertMatch(lightHtml, /<img\b[^>]*src="docs\/images\/hero-light-source\.png"[^>]*alt="Hero"/, '浅色模式应渲染浅色图片');
  } finally {
    window.matchMedia = originalMatchMedia;
  }
}

// (c) 普通 <div>hi</div> 不产生图片也不渲染原始 HTML
{
  const html = renderMarkdown('<div>hi</div>');
  assertEqual(html.includes('<img'), false, '普通 HTML 不应产生图片');
  assertEqual(html.includes('<div>'), false, '普通 HTML 不应渲染原始标签');
  assertEqual(html.includes('&lt;div&gt;hi&lt;/div&gt;'), true, '普通 HTML 应以转义文本保留而非作为 DOM 渲染');

  const tree: MdastRoot = {
    type: 'root',
    children: [{ type: 'html', value: '<div>hi</div>' }]
  };
  remarkHtmlImages({ isDark: false })(tree);
  assertEqual(tree.children, [{ type: 'html', value: '<div>hi</div>' }], '不含图片的 html 节点应保持原样');
}

// (d) baseDir='/repo/docs' 时 ![](img/a.png) 被归为 local 且路径为 /repo/docs/img/a.png
{
  assertEqual(
    resolveMarkdownImageSource('img/a.png', '/repo/docs'),
    { kind: 'local', src: '/repo/docs/img/a.png' },
    'baseDir=/repo/docs 时相对路径应归为 local 并拼出绝对路径'
  );
  assertEqual(
    resolveMarkdownImageSource('../assets/a.png', '/repo/docs'),
    { kind: 'local', src: '/repo/assets/a.png' },
    '相对路径里的 .. 应正确归并'
  );
  assertEqual(
    resolveMarkdownImageSource('img/a.png', 'docs'),
    { kind: 'local', src: 'docs/img/a.png' },
    '工作区相对 baseDir 应拼为相对路径并归为 local'
  );
  assertEqual(
    resolveMarkdownImageSource('img/a.png', '.'),
    { kind: 'local', src: 'img/a.png' },
    '工作区根目录 baseDir="." 应保持相对路径并归为 local'
  );
  assertEqual(
    resolveMarkdownImageSource('img/a.png'),
    { kind: 'remote', src: 'img/a.png' },
    '没有 baseDir 时保持原有行为'
  );
  assertEqual(
    resolveMarkdownImageSource('https://example.com/a.png', '/repo/docs'),
    { kind: 'remote', src: 'https://example.com/a.png' },
    '带 scheme 的远程地址不应拼 baseDir'
  );
  assertEqual(
    resolveMarkdownImageSource('/abs/a.png', '/repo/docs'),
    { kind: 'local', src: '/abs/a.png' },
    '以 / 开头的绝对路径不应拼 baseDir'
  );
  assertEqual(
    resolveMarkdownImageSource('~/a.png', '/repo/docs'),
    { kind: 'local', src: '~/a.png' },
    '以 ~ 开头的路径不应拼 baseDir'
  );
  assertEqual(
    resolveMarkdownImageSource('#anchor', '/repo/docs'),
    { kind: 'remote', src: '#anchor' },
    '以 # 开头的锚点不应拼 baseDir'
  );
}
