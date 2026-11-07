// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 覆盖三种 leading 的互斥按钮、handle 开关、trailing 位置与标题/eyebrow 文本。
import { renderToStaticMarkup } from 'react-dom/server';
import { MenuSheetHeader } from './MenuSheetHeader';

function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

// 静态渲染没有 DOM，按标签流维护祖先栈来模拟“从含指定文字的节点向上 closest(.class)”。
function textHasClosestClass(html: string, text: string, className: string): boolean {
  const voidTags = new Set(['br', 'hr', 'img', 'input', 'meta', 'link', 'source']);
  const stack: Array<{ tag: string; classes: string[] }> = [];
  const tokenRe = /<(\/?)([a-zA-Z][\w-]*)([^>]*?)(\/?)>|([^<]+)/g;
  let token: RegExpExecArray | null;
  while ((token = tokenRe.exec(html)) !== null) {
    const textContent = token[5];
    if (textContent !== undefined) {
      if (textContent.includes(text) && stack.some((open) => open.classes.includes(className))) {
        return true;
      }
      continue;
    }
    const closing = token[1];
    const tag = token[2].toLowerCase();
    const attrs = token[3] ?? '';
    const selfClosing = token[4];
    if (closing === '/') {
      for (let i = stack.length - 1; i >= 0; i -= 1) {
        if (stack[i].tag === tag) {
          stack.length = i;
          break;
        }
      }
      continue;
    }
    if (selfClosing === '/' || voidTags.has(tag)) continue;
    const classMatch = /class="([^"]*)"/.exec(attrs);
    stack.push({ tag, classes: classMatch ? classMatch[1].split(/\s+/).filter(Boolean) : [] });
  }
  return false;
}

const backHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="编排 · 会话标题"
    title="子代理进度与汇报"
    labelledBy="agents-page-title"
    leading="back"
    onLeading={() => {}}
  />
);

const closeHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="工作区 Git"
    title="Git 工作区"
    labelledBy="git-sheet-title"
    leading="close"
    onLeading={() => {}}
    onClose={() => {}}
  />
);

const noneHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="会话菜单"
    title="未命名会话"
    labelledBy="thread-actions-title"
    leading="none"
    onLeading={() => {}}
    showHandle={false}
  />
);

// back：只出现返回按钮。
assertIncludes(backHtml, 'aria-label="返回上一页"');
assertEqual(countOccurrences(backHtml, 'aria-label="返回上一页"'), 1, '返回按钮数量');
assertEqual(countOccurrences(backHtml, 'aria-label="关闭菜单"'), 0, '返回态的关闭按钮数量');
assertIncludes(backHtml, 'class="sheet-handle"');
assertIncludes(backHtml, 'rotate-180');

// close：只出现关闭按钮。
assertIncludes(closeHtml, 'aria-label="关闭菜单"');
assertEqual(countOccurrences(closeHtml, 'aria-label="关闭菜单"'), 1, '关闭按钮数量');
assertEqual(countOccurrences(closeHtml, 'aria-label="返回上一页"'), 0, '关闭态的返回按钮数量');

// none：两个按钮都不出现，showHandle=false 时不渲染拖拽条。
assertEqual(countOccurrences(noneHtml, 'aria-label="返回上一页"'), 0, '无 leading 的返回按钮数量');
assertEqual(countOccurrences(noneHtml, 'aria-label="关闭菜单"'), 0, '无 leading 的关闭按钮数量');
assertEqual(countOccurrences(noneHtml, 'sheet-handle'), 0, 'showHandle=false 的拖拽条数量');

// 标题与 eyebrow 原样输出，labelledBy 落到 h2 id。
assertIncludes(backHtml, '子代理进度与汇报');
assertIncludes(backHtml, '编排 · 会话标题');
assertIncludes(backHtml, 'id="agents-page-title"');
assertIncludes(closeHtml, 'id="git-sheet-title"');
assertIncludes(closeHtml, 'Git 工作区');

// trailing：出现在标题右侧（同一 header 行内）。
const trailingHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="工作区 Git"
    title="Git 工作区"
    labelledBy="git-sheet-title"
    leading="back"
    onLeading={() => {}}
    trailing={<button type="button" aria-label="刷新 Git 数据">刷新</button>}
  />
);
assertIncludes(trailingHtml, 'aria-label="刷新 Git 数据"');
assertIncludes(trailingHtml, '刷新');

// 自定义关闭文案：文件页保留自己的语义。
const customCloseHtml = renderToStaticMarkup(
  <MenuSheetHeader
    eyebrow="文件 · 会话标题"
    title="工作区文件"
    labelledBy="files-sheet-title"
    leading="close"
    onLeading={() => {}}
    closeLabel="关闭文件浏览"
  />
);
assertIncludes(customCloseHtml, 'aria-label="关闭文件浏览"');
assertEqual(countOccurrences(customCloseHtml, 'aria-label="关闭菜单"'), 0, '自定义关闭文案');

// 宽屏侧栏依赖这个 class 给标题栏留出下边距：从可见标题文字向上应能找到它。
assertEqual(
  textHasClosestClass(backHtml, '子代理进度与汇报', 'menu-sheet-header'),
  true,
  '标题 closest(.menu-sheet-header) 的存在性'
);
