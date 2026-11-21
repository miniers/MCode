// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

/**
 * 从 TSX 源码里提取所有包含指定属性的 `<button ...>` 开始标签文本，
 * 跳过属性表达式 `{...}`（包括 `=>`）和字符串字面量，避免被箭头函数里的 `>` 截断。
 */
function extractButtonOpeningTags(source: string): string[] {
  const tags: string[] = [];
  let i = 0;
  while (i < source.length) {
    const idx = source.indexOf('<button', i);
    if (idx < 0) break;
    const nextChar = source[idx + 7] ?? '';
    if (!/[\s/>]/.test(nextChar)) {
      i = idx + 7;
      continue;
    }
    let j = idx + 7;
    let braceDepth = 0;
    let quote: string | null = null;
    while (j < source.length) {
      const ch = source[j];
      if (quote) {
        if (ch === '\\') {
          j += 2;
          continue;
        }
        if (ch === quote) quote = null;
      } else if (ch === '"' || ch === "'" || ch === '`') {
        quote = ch;
      } else if (ch === '{') {
        braceDepth += 1;
      } else if (ch === '}') {
        braceDepth = Math.max(0, braceDepth - 1);
      } else if (ch === '>' && braceDepth === 0) {
        break;
      }
      j += 1;
    }
    tags.push(source.slice(idx, j + 1));
    i = j + 1;
  }
  return tags;
}

/**
 * 扫描 TSX 源码，校验 `<button>` 内不存在嵌套 `<button>`、`<a ...>` 或 `<ThreadMenuButton`。
 */
function assertNoNestedInteractiveInButtons(source: string, fileLabel: string): void {
  let i = 0;
  let buttonDepth = 0;
  while (i < source.length) {
    if (source[i] !== '<') {
      i += 1;
      continue;
    }
    if (source.startsWith('</button>', i)) {
      buttonDepth = Math.max(0, buttonDepth - 1);
      i += 9;
      continue;
    }
    const tagMatch = /^<(\/?)([A-Za-z][A-Za-z0-9]*)/.exec(source.slice(i, i + 40));
    if (!tagMatch) {
      i += 1;
      continue;
    }
    const isClosing = tagMatch[1] === '/';
    const tagName = tagMatch[2];
    let j = i + tagMatch[0].length;
    let braceDepth = 0;
    let quote: string | null = null;
    while (j < source.length) {
      const ch = source[j];
      if (quote) {
        if (ch === '\\') {
          j += 2;
          continue;
        }
        if (ch === quote) quote = null;
      } else if (ch === '"' || ch === "'" || ch === '`') {
        quote = ch;
      } else if (ch === '{') {
        braceDepth += 1;
      } else if (ch === '}') {
        braceDepth = Math.max(0, braceDepth - 1);
      } else if (ch === '>' && braceDepth === 0) {
        break;
      }
      j += 1;
    }
    const selfClosing = source[j - 1] === '/';
    if (!isClosing && buttonDepth > 0 && (tagName === 'button' || tagName === 'a' || tagName === 'ThreadMenuButton')) {
      throw new Error(`${fileLabel} 在 <button> 内部发现嵌套交互元素 <${tagName}>`);
    }
    if (!isClosing && !selfClosing && tagName === 'button') {
      buttonDepth += 1;
    }
    i = j + 1;
  }
}

// 1. index.html 与 main.tsx 不禁用双指缩放
const indexHtml = readText('../index.html');
const viewportMatch = /<meta\s+name="viewport"\s+content="([^"]+)"\s*\/?>/i.exec(indexHtml);
assert(Boolean(viewportMatch), 'web/index.html 缺少 viewport meta 标签');
const viewportContent = viewportMatch![1];
assert(!/user-scalable\s*=\s*no/i.test(viewportContent), 'viewport 不得设置 user-scalable=no');
assert(!/maximum-scale\s*=/i.test(viewportContent), 'viewport 不得设置 maximum-scale 限制缩放');
assert(viewportContent.includes('width=device-width'), 'viewport 应保留 width=device-width');
assert(viewportContent.includes('initial-scale=1.0'), 'viewport 应保留 initial-scale=1.0');
assert(viewportContent.includes('viewport-fit=cover'), 'viewport 应保留 viewport-fit=cover');

const mainTsx = readText('./main.tsx');
assert(!mainTsx.includes('gesturestart'), 'web/src/main.tsx 不得拦截 gesturestart');
assert(!mainTsx.includes("touchAction = 'pan-x pan-y'"), 'web/src/main.tsx 不得全局写入 touchAction = pan-x pan-y');
assert(!/event\.touches\.length\s*>\s*1/.test(mainTsx), 'web/src/main.tsx 不得全局拦截多指 touchmove');

// 2. ThreadComposer.tsx 与 ThreadMenuSwarmPanel.tsx 所有 role="switch" 具备 aria-label 与 focus-visible:ring-2
const switchFiles = [
  ['./components/ThreadComposer.tsx', 'web/src/components/ThreadComposer.tsx'],
  ['./components/thread/menu/ThreadMenuSwarmPanel.tsx', 'web/src/components/thread/menu/ThreadMenuSwarmPanel.tsx']
] as const;

for (const [relPath, label] of switchFiles) {
  const source = readText(relPath);
  const switchTags = extractButtonOpeningTags(source).filter((tag) => /role\s*=\s*["']switch["']/.test(tag));
  assert(switchTags.length > 0, `${label} 应包含 role="switch" 控件`);
  for (const tag of switchTags) {
    assert(
      /(?:aria-label|aria-labelledby)\s*=/.test(tag),
      `${label} 的 role="switch" 缺少 aria-label / aria-labelledby：\n${tag}`
    );
    assert(
      tag.includes('focus-visible:ring-2'),
      `${label} 的 role="switch" 缺少 focus-visible:ring-2 焦点环：\n${tag}`
    );
    assert(
      !/(?:^|[\s`'"])focus:outline-none(?:$|[\s`'"])/.test(tag),
      `${label} 的 role="switch" 不得使用裸 focus:outline-none：\n${tag}`
    );
  }
}

// 3. ThreadList.tsx 与 KanbanBoard.tsx 不再存在 <div role="button"> 或 <button> 内嵌套交互按钮
for (const [relPath, label] of [
  ['./components/ThreadList.tsx', 'web/src/components/ThreadList.tsx'],
  ['./components/KanbanBoard.tsx', 'web/src/components/KanbanBoard.tsx']
] as const) {
  const source = readText(relPath);
  assert(!/role\s*=\s*["']button["']/.test(source), `${label} 不应再使用 role="button" 模拟按钮容器`);
  assertNoNestedInteractiveInButtons(source, label);
}
