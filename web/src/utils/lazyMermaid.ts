// Mermaid 运行时体积较大，不进主包也不走外链 CDN（远程端 CSP 为 script-src 'self'），
// 通过 vite 插件复制到 public/vendor/mermaid.min.js 后按需以 <script> 方式懒加载。

import type { Mermaid } from 'mermaid';

declare global {
  interface Window {
    mermaid?: Mermaid;
  }
}

let loadPromise: Promise<Mermaid> | null = null;
let initialized = false;

function mermaidScriptSrc(): string {
  const base = import.meta.env.BASE_URL ?? './';
  const normalizedBase = base.endsWith('/') ? base : `${base}/`;
  return `${normalizedBase}vendor/mermaid.min.js`;
}

function ensureInitialized(mermaid: Mermaid): Mermaid {
  if (!initialized) {
    mermaid.initialize({
      startOnLoad: false,
      securityLevel: 'strict',
      suppressErrorRendering: true
    });
    initialized = true;
  }
  return mermaid;
}

/** 纯函数：判断代码块语言标识是否为 mermaid。 */
export function isMermaidLanguage(lang: string): boolean {
  return lang.trim().toLowerCase() === 'mermaid';
}

/** 按需加载 vendor/mermaid.min.js（幂等）；加载失败时清掉缓存以便后续重试。 */
export function loadMermaid(): Promise<Mermaid> {
  if (loadPromise) return loadPromise;

  loadPromise = new Promise<Mermaid>((resolve, reject) => {
    if (window.mermaid) {
      resolve(ensureInitialized(window.mermaid));
      return;
    }

    const script = document.createElement('script');
    script.src = mermaidScriptSrc();
    script.async = true;

    script.onload = () => {
      if (!window.mermaid) {
        script.remove();
        reject(new Error('mermaid 全局对象未就绪'));
        return;
      }
      resolve(ensureInitialized(window.mermaid));
    };

    script.onerror = () => {
      script.remove();
      reject(new Error('加载 mermaid.min.js 失败'));
    };

    (document.head ?? document.documentElement).appendChild(script);
  }).catch((error: unknown) => {
    // 失败时清掉缓存，下次遇到 mermaid 代码块仍可重试。
    loadPromise = null;
    throw error;
  });

  return loadPromise;
}
