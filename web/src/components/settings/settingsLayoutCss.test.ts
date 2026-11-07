// web/ 还没有测试运行器（见 web/src/components/ModelPicker.test.tsx），沿用「断言失败直接抛 Error」的写法。
// 这里读取 web/src/index.css 源文本，锁定宽屏设置页的布局约束：导航分组顶部紧凑排列、弹窗高度固定，
// 避免切分类时弹窗随内容长短改变高度并重新垂直居中。
// web/ 未安装 @types/node，node:fs 由测试运行时提供，只在本文件里局部忽略类型错误。
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';

function assertIncludes(haystack: string, needle: string, what: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望${what}包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string, what: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望${what}不含 “${needle}”，实际为：${haystack}`);
  }
}

const css = readFileSync(new URL('../../index.css', import.meta.url), 'utf8');

/** 取以 "<selector> {" 开头的那条规则（到第一个 "}" 为止），允许行首缩进。 */
function ruleBody(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`(?:^|\\n)[ \\t]*${escaped}[ \\t]*\\{`).exec(css);
  if (!match) {
    throw new Error(`期望 index.css 里存在规则 “${selector}”`);
  }
  const open = css.indexOf('{', match.index);
  const end = css.indexOf('}', open);
  return css.slice(open + 1, end);
}

/** 取包含指定规则的 @media 块（括号配平），用来确认规则落在正确的断点里。 */
function mediaBlock(query: string): string {
  const head = css.indexOf(`@media ${query} {`);
  if (head < 0) {
    throw new Error(`期望 index.css 里存在 “@media ${query}”`);
  }
  let depth = 0;
  for (let i = css.indexOf('{', head); i < css.length; i += 1) {
    if (css[i] === '{') depth += 1;
    if (css[i] === '}') {
      depth -= 1;
      if (depth === 0) return css.slice(head, i + 1);
    }
  }
  throw new Error(`“@media ${query}” 块没有闭合`);
}

// 问题 1：flex 行会把 .settings-nav 拉伸到与右侧内容等高，grid 默认 align-content: stretch 会平分高度。
const nav = ruleBody('.settings-nav');
assertIncludes(nav, 'align-content: start;', '基础 .settings-nav 规则');

const wide = mediaBlock('(min-width: 900px)');
const wideNav = ruleBody('.settings-shell--wide .settings-nav');
assertIncludes(wideNav, 'gap: 12px;', '宽屏 .settings-shell--wide .settings-nav 规则');
assertIncludes(wideNav, 'align-content: start;', '宽屏 .settings-shell--wide .settings-nav 规则');
assertIncludes(wide, '.settings-shell--wide .settings-nav {', '宽屏断点');

// 问题 2：只有 max-height 时弹窗高度随页面内容变化，切页会重新垂直居中并闪一下。
const wideSheet = ruleBody('.settings-sheet.settings-shell--wide');
assertIncludes(wide, '.settings-sheet.settings-shell--wide {', '宽屏断点');
assertIncludes(wideSheet, 'height: min(760px, calc(var(--app-height) - 40px));', '宽屏设置弹窗规则');
assertIncludes(wideSheet, 'max-height: calc(var(--app-height) - 40px);', '宽屏设置弹窗规则');

// 切页不重播入场动画，也不存在依赖内容高度的 transition。
assertExcludes(wideSheet, 'transition:', '宽屏设置弹窗规则');
const baseSheet = ruleBody('.settings-sheet, .thread-action-sheet, .create-sheet');
assertExcludes(baseSheet, 'transition:', '基础 .settings-sheet 规则');
