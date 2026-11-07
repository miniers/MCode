// 代码高亮：highlight.js 的 core 加上首屏最常见的几种语法包静态引入，
// 冷门语言等到第一次遇到对应代码块时再动态 import 并注册，
// 注册完成后通知正在显示的代码块重新高亮（见 subscribeHighlightLanguages）。

import hljs from 'highlight.js/lib/core';
import typescript from 'highlight.js/lib/languages/typescript';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import bash from 'highlight.js/lib/languages/bash';
import go from 'highlight.js/lib/languages/go';
import python from 'highlight.js/lib/languages/python';
import diff from 'highlight.js/lib/languages/diff';
import plaintext from 'highlight.js/lib/languages/plaintext';

hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('json', json);
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('go', go);
hljs.registerLanguage('python', python);
hljs.registerLanguage('diff', diff);
hljs.registerLanguage('plaintext', plaintext);

// 别名只写映射，highlight.js 解析别名时再查语言表，因此目标语言稍后注册也能生效。
hljs.registerAliases('tsx', { languageName: 'typescript' });
hljs.registerAliases('jsx', { languageName: 'javascript' });
hljs.registerAliases(['shell', 'zsh'], { languageName: 'bash' });
hljs.registerAliases('html', { languageName: 'xml' });
hljs.registerAliases('yml', { languageName: 'yaml' });
hljs.registerAliases('md', { languageName: 'markdown' });
hljs.registerAliases('py', { languageName: 'python' });
hljs.registerAliases('rs', { languageName: 'rust' });
hljs.registerAliases('golang', { languageName: 'go' });
hljs.registerAliases('jsonc', { languageName: 'json' });

type LanguageDefinition = Parameters<typeof hljs.registerLanguage>[1];
type LanguageLoader = () => Promise<{ default: LanguageDefinition }>;

// 冷门语言：首次遇到时才下载对应的语法包
const DEFERRED_LANGUAGES: Record<string, LanguageLoader> = {
  swift: () => import('highlight.js/lib/languages/swift'),
  rust: () => import('highlight.js/lib/languages/rust'),
  xml: () => import('highlight.js/lib/languages/xml'),
  css: () => import('highlight.js/lib/languages/css'),
  yaml: () => import('highlight.js/lib/languages/yaml'),
  markdown: () => import('highlight.js/lib/languages/markdown'),
  sql: () => import('highlight.js/lib/languages/sql'),
  java: () => import('highlight.js/lib/languages/java'),
  kotlin: () => import('highlight.js/lib/languages/kotlin'),
  c: () => import('highlight.js/lib/languages/c'),
  cpp: () => import('highlight.js/lib/languages/cpp'),
  ruby: () => import('highlight.js/lib/languages/ruby'),
  php: () => import('highlight.js/lib/languages/php')
};

// 别名指向冷门语言时，按目标语言去加载
const DEFERRED_ALIASES: Record<string, string> = {
  html: 'xml',
  yml: 'yaml',
  md: 'markdown',
  rs: 'rust'
};

// 语法包注册后会自增，正在显示的代码块据此重新高亮
let languagesVersion = 0;
const versionListeners = new Set<() => void>();
const pendingLoads = new Map<string, Promise<void>>();

/** 订阅「已注册语言集合变化」，返回取消订阅函数（配合 React.useSyncExternalStore 使用）。 */
export function subscribeHighlightLanguages(listener: () => void): () => void {
  versionListeners.add(listener);
  return () => {
    versionListeners.delete(listener);
  };
}

export function getHighlightLanguagesVersion(): number {
  return languagesVersion;
}

// 每个语言只请求一次；失败也不删记录，避免渲染时反复重试同一次失败请求。
function requestLanguage(name: string): void {
  if (pendingLoads.has(name)) return;
  const loader = DEFERRED_LANGUAGES[name];
  if (!loader) return;
  const task = loader()
    .then((module) => {
      hljs.registerLanguage(name, module.default);
      languagesVersion += 1;
      // 先复制再通知：监听器回调节点挂载/卸载时会增删 Set
      [...versionListeners].forEach((listener) => listener());
    })
    .catch(() => {
      // 语法包加载失败：保持纯文本显示，不影响其它代码块。
    });
  pendingLoads.set(name, task);
}

// 别名与冷门语言都要落到具体语言名上，注册进度按具体语言名判断。
function concreteLanguage(token: string): string {
  if (hljs.getLanguage(token) || DEFERRED_LANGUAGES[token]) return token;
  return DEFERRED_ALIASES[token] ?? token;
}

export const HIGHLIGHT_MAX_LENGTH = 20000;

/**
 * 高亮一段代码；语言未注册（冷门语言语法包还没到）时返回 null，
 * 调用方退回纯文本显示，等注册完成的通知到达后重新高亮。
 */
export function highlightCode(code: string, language: string): string | null {
  const token = (language || '').trim().split(/\s+/)[0]?.toLowerCase() || '';
  if (!token || code.length > HIGHLIGHT_MAX_LENGTH) {
    return null;
  }
  const resolved = concreteLanguage(token);
  if (!hljs.getLanguage(resolved)) {
    requestLanguage(resolved);
    return null;
  }
  try {
    return hljs.highlight(code, { language: resolved, ignoreIllegals: true }).value;
  } catch {
    return null;
  }
}
