// 同 ComposerQuotaBar.test.tsx：顶层直接断言，失败时 throw；渲染使用 react-dom/server。
// import.meta.glob 需要通过 Vite 转换后运行。
import { renderToStaticMarkup } from 'react-dom/server';
import { ProviderIcon } from './ProviderIcon';

function coloredIconSrc(provider: string): string {
  const html = renderToStaticMarkup(<ProviderIcon provider={provider} colored />);
  const src = html.match(/<img\b[^>]*\bsrc="([^"]+)"/)?.[1];
  if (!src) {
    throw new Error(`${provider} 应渲染 src 非空的彩色 img`);
  }
  if (src.startsWith(`${import.meta.env.BASE_URL}provider-icons/`) || src.includes('/public/')) {
    throw new Error(`${provider} 不应使用 public 静态路径：${src}`);
  }
  return src;
}

const codexSrc = coloredIconSrc('codex');
if (!codexSrc.includes('codex')) {
  throw new Error(`codex 的导入 URL 应包含 codex：${codexSrc}`);
}

if (coloredIconSrc('openai-xxx') !== codexSrc) {
  throw new Error('openai-xxx 应映射到 codex 彩色图标');
}

for (const provider of [
  'antigravity', 'claude', 'cline', 'codex', 'commandcode', 'copilot',
  'cursor', 'deepseek', 'devin', 'droid', 'grok', 'meta', 'omp',
  'opencode', 'pi', 'zai'
]) {
  coloredIconSrc(provider);
}

const unknownHtml = renderToStaticMarkup(<ProviderIcon provider="unknownx" colored />);
if (unknownHtml.includes('<img') || !unknownHtml.includes('>U</span>')) {
  throw new Error('未知供应商应回退为首字母 U，不渲染 img');
}
