// KaTeX 是首屏用不到的最大一块体积：remark-math、rehype-katex 与 katex 样式都改成按需加载，
// 只有文本里可能出现公式时才拉取，普通会话的首屏不必下载它们。
//
// 用法：textMayContainMath 先宽松判断一遍（宁可多加载，也不能漏渲染），
// MarkdownView 在未加载时先按无公式渲染并触发 loadMathPlugins()，
// 加载完成后再用 getLoadedMathPlugins() 的结果重渲染。

import type { PluggableList } from 'unified';

export interface MathPlugins {
  /** remark-math：把 $…$ / $$…$$ 解析成 math 节点 */
  remark: PluggableList;
  /** rehype-katex：把 math 节点渲染成 KaTeX HTML */
  rehype: PluggableList;
}

let loadPromise: Promise<MathPlugins> | null = null;
let loadedPlugins: MathPlugins | null = null;

/** 已加载完成时同步返回插件集合，未加载或加载中返回 null。 */
export function getLoadedMathPlugins(): MathPlugins | null {
  return loadedPlugins;
}

/** 触发加载（幂等）；插件与结果都在模块级缓存，重复调用不会重复下载。 */
export function loadMathPlugins(): Promise<MathPlugins> {
  if (loadPromise) return loadPromise;
  loadPromise = Promise.all([
    import('remark-math'),
    import('rehype-katex'),
    // 样式放在同一批加载里，插件能用时样式已经就位
    import('katex/dist/katex.min.css')
  ])
    .then(([remarkMath, rehypeKatex]) => {
      loadedPlugins = {
        remark: [remarkMath.default],
        rehype: [rehypeKatex.default]
      };
      return loadedPlugins;
    })
    .catch((error: unknown) => {
      // 失败时清掉缓存：下次遇到公式文本还能重试；错误抛给调用方，由它退回无公式渲染。
      loadPromise = null;
      throw error;
    });
  return loadPromise;
}

export function shouldUseMath(text: string, mathEnabled: boolean): boolean {
  return mathEnabled && textMayContainMath(text);
}

/**
 * 纯函数：文本里是否可能出现公式。
 * 判定刻意宽松（代码块里的美元符号也会算命中），多加载一次只是浪费带宽，
 * 漏判会让公式原样显示成 `$…$`，所以宁可多加载。
 */
export function textMayContainMath(text: string): boolean {
  if (!text) return false;
  // \( … \) 与 \[ … \] 分隔符
  if (text.includes('\\(') || text.includes('\\[')) return true;
  // 成对的 $ 或 $$：出现两个及以上未转义的美元符号就算命中
  let dollarCount = 0;
  for (let index = 0; index < text.length; index += 1) {
    if (text[index] !== '$') continue;
    if (index > 0 && text[index - 1] === '\\') continue;
    dollarCount += 1;
    if (dollarCount >= 2) return true;
  }
  return false;
}
