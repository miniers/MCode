// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';
// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:assert
import { strict as assert } from 'node:assert';

const css = readFileSync(new URL('../index.css', import.meta.url), 'utf8');

function ruleBody(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = new RegExp(`(?:^|\\n)[ \\t]*${escaped}[ \\t]*\\{`).exec(css);
  if (!match) {
    throw new Error(`期望 index.css 里存在规则 “${selector}”`);
  }
  const open = css.indexOf('{', match.index);
  return css.slice(open + 1, css.indexOf('}', open));
}

function assertIncludes(haystack: string, needle: string): void {
  assert.ok(haystack.includes(needle), `期望规则包含 “${needle}”`);
}

function assertExcludes(haystack: string, needle: string): void {
  assert.ok(!haystack.includes(needle), `期望规则不含 “${needle}”`);
}

const layer = ruleBody('.sheet-layer');
assertIncludes(layer, 'height: var(--app-height)');
assertExcludes(layer, 'inset: 0');

const keyboardSheets = ruleBody('html[data-keyboard-open] .settings-sheet, html[data-keyboard-open] .thread-action-sheet, html[data-keyboard-open] .create-sheet');
assertIncludes(keyboardSheets, 'padding-bottom: 20px;');
assertIncludes(ruleBody('html[data-keyboard-open] .settings-sheet.model-sheet'), 'padding-bottom: 0;');
