// 同 SheetFrame.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 覆盖抽屉卡片 class 的稳定性：主页（menu）与详情页（panel）的卡片 class 必须完全一致，
// 否则 index.css 里 slide-up 入场动画会在切页时重播。只渲染外壳，不渲染整个会话菜单。
import { renderToStaticMarkup } from 'react-dom/server';
import { SheetFrame } from '../sheet/SheetFrame';
import { threadMenuDrawerFrameProps } from './ThreadMenuSheet';
import { pageSizeFor } from './menu/menuPages';

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertIncludes(haystack: string, needle: string, what: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望${what}包含 “${needle}”，实际为：${haystack}`);
  }
}

function extractCardClass(html: string): string {
  const match = html.match(/<section class="([^"]*)"/);
  if (!match) throw new Error(`没有找到抽屉卡片，实际为：${html}`);
  return match[1];
}

function renderCard(props: { size: 'panel'; cardClassName: string }, label: string): string {
  return extractCardClass(
    renderToStaticMarkup(
      <SheetFrame onRequestClose={() => {}} {...props}>
        <p>{label}</p>
      </SheetFrame>
    )
  );
}

// 主页与详情页在页面栈里的尺寸本来就不同：高度仍由 MenuStackView 的 size 区分。
assertEqual(pageSizeFor('main'), 'menu', '主页尺寸');
assertEqual(pageSizeFor('scripts'), 'panel', '脚本页尺寸');

// 抽屉外壳不跟着页面尺寸走：两次渲染的卡片 class 必须一字不差。
const mainClass = renderCard(threadMenuDrawerFrameProps('main'), '会话菜单');
const scriptsClass = renderCard(threadMenuDrawerFrameProps('scripts'), '运行脚本');

if (mainClass !== scriptsClass) {
  throw new Error(`主页与详情页的抽屉卡片 class 必须相同，实际为：${mainClass} / ${scriptsClass}`);
}

// 固定走 settings-sheet，且仍带 menu-stack-card（flex 列容器 + overflow hidden）。
assertIncludes(mainClass, 'settings-sheet', '抽屉卡片 class ');
assertIncludes(mainClass, 'menu-stack-card', '抽屉卡片 class ');
if (mainClass.includes('thread-action-sheet')) {
  throw new Error(`抽屉卡片不应再切到 thread-action-sheet，实际为：${mainClass}`);
}
