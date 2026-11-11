// web/ 没有接入测试运行器（package.json 无 vitest/jest），沿用 src/components/*.test.tsx 的写法：
// 不使用测试框架，断言失败直接抛 Error，tsc --noEmit 会检查本文件。
import { computeViewport, isEditableElement } from './viewport';
import type { ViewportInput, ViewportMetrics } from './viewport';

const BASE: ViewportInput = {
  windowHeight: 800,
  visualViewportHeight: 800,
  editableFocused: false,
  isIOS: false,
  standalone: false,
  screenWidth: 390,
  screenHeight: 844,
  innerWidth: 390,
  safeAreaTop: 0
};

const cases: { name: string; input: Partial<ViewportInput>; expected: ViewportMetrics }[] = [
  {
    // 后台切回时可视视口停在键盘高度、但键盘其实已经收起
    name: '可视视口矮 400px 但焦点不在可编辑元素上：按键盘收起处理',
    input: { visualViewportHeight: 400 },
    expected: { height: 800, keyboardOpen: false }
  },
  {
    name: '可视视口矮 400px 且焦点在可编辑元素上：高度取可视视口',
    input: { visualViewportHeight: 400, editableFocused: true },
    expected: { height: 400, keyboardOpen: true }
  },
  {
    name: '可视视口只矮 100px 且有焦点：不到阈值，不算键盘',
    input: { visualViewportHeight: 700, editableFocused: true },
    expected: { height: 800, keyboardOpen: false }
  },
  {
    name: '没有视觉视口：一律按键盘收起处理',
    input: { visualViewportHeight: null, editableFocused: true },
    expected: { height: 800, keyboardOpen: false }
  },
  {
    // 半透明状态栏（安全区 59）下 iOS 少算了顶部安全区，按物理屏幕高度补回
    name: 'iOS standalone + 半透明状态栏：按物理屏幕高度补偿',
    input: {
      windowHeight: 812,
      visualViewportHeight: 812,
      isIOS: true,
      standalone: true,
      safeAreaTop: 59,
      innerWidth: 390,
      screenWidth: 390,
      screenHeight: 844
    },
    expected: { height: 844, keyboardOpen: false }
  },
  {
    // default 状态栏安全区为 0，视口本来就完整，不补偿，否则多算一个状态栏高度
    name: 'iOS standalone + default 状态栏：不补偿，高度等于 windowHeight',
    input: {
      windowHeight: 812,
      visualViewportHeight: 812,
      isIOS: true,
      standalone: true,
      safeAreaTop: 0,
      innerWidth: 390,
      screenWidth: 390,
      screenHeight: 844
    },
    expected: { height: 812, keyboardOpen: false }
  },
  {
    // 键盘弹起时按可视视口高度，不做物理屏幕补偿
    name: 'iOS standalone 键盘弹起：按可视视口高度，不补偿',
    input: {
      windowHeight: 812,
      visualViewportHeight: 460,
      editableFocused: true,
      isIOS: true,
      standalone: true,
      safeAreaTop: 59,
      innerWidth: 390,
      screenWidth: 390,
      screenHeight: 844
    },
    expected: { height: 460, keyboardOpen: true }
  }
];

for (const testCase of cases) {
  const actual = computeViewport({ ...BASE, ...testCase.input });
  if (actual.height !== testCase.expected.height || actual.keyboardOpen !== testCase.expected.keyboardOpen) {
    throw new Error(
      `期望「${testCase.name}」得到 ${JSON.stringify(testCase.expected)}，实际为 ${JSON.stringify(actual)}`
    );
  }
}

// 单选、复选等聚焦时不弹键盘：提问卡点选项会把焦点移到 radio 上，不能当成键盘还开着
const element = (tagName: string, type = ''): Element => ({ tagName, type } as unknown as Element);
const editableCases: { name: string; element: Element | null; expected: boolean }[] = [
  { name: '文本 input', element: element('INPUT', 'text'), expected: true },
  { name: '密码 input', element: element('INPUT', 'password'), expected: true },
  { name: 'textarea', element: element('TEXTAREA'), expected: true },
  { name: 'radio', element: element('INPUT', 'radio'), expected: false },
  { name: 'checkbox', element: element('INPUT', 'checkbox'), expected: false },
  { name: 'button', element: element('BUTTON'), expected: false },
  { name: '无焦点', element: null, expected: false }
];

for (const testCase of editableCases) {
  const actual = isEditableElement(testCase.element);
  if (actual !== testCase.expected) {
    throw new Error(`期望「${testCase.name}」isEditableElement 为 ${testCase.expected}，实际为 ${actual}`);
  }
}
