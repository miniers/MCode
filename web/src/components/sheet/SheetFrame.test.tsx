// 同 LazyFallback.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 这里只校验外壳的语义与 class：遮罩层、dialog 角色、两种卡片样式、aria-labelledby 透传。
import { renderToStaticMarkup } from 'react-dom/server';
import { SheetFrame, SHEET_LAYER_CLASS, SHEET_MENU_CARD_CLASS, SHEET_PANEL_CARD_CLASS, pushSheet, removeSheet, isTopSheet, nextFocusIndex } from './SheetFrame';

function assertIncludes(haystack: string, needle: string, what: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望${what}包含 “${needle}”，实际为：${haystack}`);
  }
}

const defaultHtml = renderToStaticMarkup(
  <SheetFrame onRequestClose={() => {}}>
    <p>内容</p>
  </SheetFrame>
);
const menuHtml = renderToStaticMarkup(
  <SheetFrame onRequestClose={() => {}} size="menu">
    <p>内容</p>
  </SheetFrame>
);
const labelledHtml = renderToStaticMarkup(
  <SheetFrame onRequestClose={() => {}} labelledBy="sheet-title">
    <h2 id="sheet-title">标题</h2>
  </SheetFrame>
);

// 默认 panel：settings-sheet 与高度上限。
assertIncludes(defaultHtml, SHEET_PANEL_CARD_CLASS, '默认 card class ');
assertIncludes(defaultHtml, 'settings-sheet', '默认 card class ');
assertIncludes(defaultHtml, 'max-h-[calc(var(--app-height,100dvh)*0.92)]', '默认 card class ');
assertIncludes(defaultHtml, '内容', '子节点');

// 外层遮罩与 dialog 语义。
assertIncludes(defaultHtml, SHEET_LAYER_CLASS, '外层遮罩 ');
assertIncludes(defaultHtml, 'role="dialog"', 'dialog ');
assertIncludes(defaultHtml, 'aria-modal="true"', 'aria-modal ');

// menu：换成 thread-action-sheet，且不带 settings-sheet。
assertIncludes(menuHtml, SHEET_MENU_CARD_CLASS, 'menu card class ');
assertIncludes(menuHtml, 'thread-action-sheet', 'menu card class ');
if (menuHtml.includes('settings-sheet')) {
  throw new Error(`size 为 menu 时不应出现 settings-sheet，实际为：${menuHtml}`);
}

// labelledBy 输出为 aria-labelledby。
assertIncludes(labelledHtml, 'aria-labelledby="sheet-title"', 'aria-labelledby ');

const stack = pushSheet(pushSheet([], 'outer'), 'inner');
if (!isTopSheet(stack, 'inner') || isTopSheet(stack, 'outer') || !isTopSheet(removeSheet(stack, 'inner'), 'outer')) {
  throw new Error('弹层栈顺序错误');
}
for (const [index, count, backwards, expected] of [[1, 2, false, 0], [0, 2, true, 1], [0, 0, false, -1], [-1, 2, true, 1]] as const) {
  if (nextFocusIndex(index, count, backwards) !== expected) throw new Error('焦点循环错误');
}
const variantHtml = renderToStaticMarkup(<SheetFrame onRequestClose={() => {}} layerClassName={`${SHEET_LAYER_CLASS}--popover`} role="alertdialog">内容</SheetFrame>);
assertIncludes(variantHtml, `${SHEET_LAYER_CLASS} ${SHEET_LAYER_CLASS}--popover`, '遮罩变体');
assertIncludes(variantHtml, 'role="alertdialog"', '警告对话框');

const bareHtml = renderToStaticMarkup(<SheetFrame onRequestClose={() => {}} size="bare" ariaLabel="自定义弹层" cardClassName="custom" cardStyle={{ left: 12 }} layerStyle={{ top: 24 }}>内容</SheetFrame>);
assertIncludes(bareHtml, 'class="custom" style="left:12px"', 'bare 卡片及样式');
assertIncludes(bareHtml, `class="${SHEET_LAYER_CLASS}" style="top:24px"`, '遮罩样式');

assertIncludes(bareHtml, 'aria-label="自定义弹层"', 'aria-label');

const siblingHtml = renderToStaticMarkup(<SheetFrame onRequestClose={() => {}} layerChildren={<aside>选择器</aside>}>卡片</SheetFrame>);
assertIncludes(siblingHtml, '</section><aside>选择器</aside></div>', '遮罩内卡片后的节点');
