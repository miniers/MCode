import { renderToStaticMarkup } from 'react-dom/server';
import { MarkdownView } from './MarkdownView';

const codeHtml = renderToStaticMarkup(<MarkdownView text={'```js\nconst value = 1;\n```'} />);
if (!/<button\b[^>]*aria-label="复制代码"/.test(codeHtml)) {
  throw new Error('代码块应包含复制代码按钮');
}

const plainHtml = renderToStaticMarkup(<MarkdownView text="普通文本" />);
if (plainHtml.includes('aria-label="复制代码"')) {
  throw new Error('没有代码块时不应包含复制代码按钮');
}
