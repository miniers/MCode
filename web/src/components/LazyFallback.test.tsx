// 同 ModelPicker.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 这里只校验骨架形状（行数、气泡数、默认变体），颜色与动画由 styles/skeleton.css 负责。
import { renderToStaticMarkup } from 'react-dom/server';
import { LazyScreenFallback, LazySheetFallback } from './LazyFallback';

function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

function assertEqual(actual: number, expected: number, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${expected}，实际为 ${actual}`);
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

const listHtml = renderToStaticMarkup(<LazyScreenFallback variant="list" label="正在加载会话…" />);
const threadHtml = renderToStaticMarkup(<LazyScreenFallback variant="thread" label="正在加载会话…" />);
const defaultHtml = renderToStaticMarkup(<LazyScreenFallback label="正在加载会话…" />);

// 列表骨架：4 行 76px 会话行，没有气泡。
assertIncludes(listHtml, 'mc-skeleton--list');
assertIncludes(listHtml, 'width:60%');
assertIncludes(listHtml, 'width:40%');
assertEqual(countOccurrences(listHtml, 'data-skeleton-row'), 4, '列表骨架行数');
assertEqual(countOccurrences(listHtml, 'data-skeleton-bubble'), 0, '列表骨架气泡数');

// 会话骨架：3 组气泡（用户 55% / 助手 80% 两行 / 用户 40%），没有列表行。
assertIncludes(threadHtml, 'mc-skeleton--thread');
assertIncludes(threadHtml, 'width:55%');
assertIncludes(threadHtml, 'width:80%');
assertIncludes(threadHtml, 'width:40%');
assertEqual(countOccurrences(threadHtml, 'data-skeleton-bubble'), 3, '会话骨架气泡数');
assertEqual(countOccurrences(threadHtml, 'data-skeleton-row'), 0, '会话骨架列表行数');

// 不传 variant 时与 list 完全一致，保证现有调用方行为不变。
if (defaultHtml !== listHtml) {
  throw new Error(`不传 variant 时应等于 list 骨架，实际为：${defaultHtml}`);
}

// 抽屉占位：浮层外壳（sheet-layer）+ 文案 + role=status，不再占页面流。
const sheetPanelHtml = renderToStaticMarkup(<LazySheetFallback label="正在加载菜单…" />);
const sheetMenuHtml = renderToStaticMarkup(<LazySheetFallback label="正在加载菜单…" size="menu" />);

assertIncludes(sheetPanelHtml, 'sheet-layer');
assertIncludes(sheetPanelHtml, '正在加载菜单…');
assertIncludes(sheetPanelHtml, 'role="status"');
assertIncludes(sheetPanelHtml, 'settings-sheet');
assertIncludes(sheetPanelHtml, 'min-h-[45dvh]');
assertIncludes(sheetMenuHtml, 'sheet-layer');
assertIncludes(sheetMenuHtml, 'thread-action-sheet');
assertIncludes(sheetMenuHtml, 'min-h-[240px]');
