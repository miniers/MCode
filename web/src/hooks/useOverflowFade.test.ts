// W-15 渐隐纯函数的表驱动测试。与 ModelPicker.test.tsx 一致：不引入测试框架与 node 内置模块，
// 断言失败直接抛错；接入运行器后本文件即可直接执行，tsc 也能单独通过。
import { OVERFLOW_FADE_SIZE, overflowFade } from './useOverflowFade';

function assertFade(label: string, actual: { left: number; right: number }, expected: { left: number; right: number }): void {
  if (actual.left !== expected.left || actual.right !== expected.right) {
    throw new Error(`${label}: 期望 ${JSON.stringify(expected)}，实际 ${JSON.stringify(actual)}`);
  }
}

interface Case {
  label: string;
  scrollLeft: number;
  scrollWidth: number;
  clientWidth: number;
  expected: { left: number; right: number };
}

const cases: Case[] = [
  // 内容没超出容器：两侧都不遮。
  { label: '不溢出', scrollLeft: 0, scrollWidth: 320, clientWidth: 320, expected: { left: 0, right: 0 } },
  { label: '内容比容器窄', scrollLeft: 0, scrollWidth: 200, clientWidth: 320, expected: { left: 0, right: 0 } },
  // 在最左：右边还有内容，只遮右侧。
  { label: '最左', scrollLeft: 0, scrollWidth: 640, clientWidth: 320, expected: { left: 0, right: OVERFLOW_FADE_SIZE } },
  // 在中间：两侧都遮。
  { label: '中间', scrollLeft: 160, scrollWidth: 640, clientWidth: 320, expected: { left: OVERFLOW_FADE_SIZE, right: OVERFLOW_FADE_SIZE } },
  // 在最右：左边还有内容，只遮左侧。
  { label: '最右', scrollLeft: 320, scrollWidth: 640, clientWidth: 320, expected: { left: OVERFLOW_FADE_SIZE, right: 0 } },
  // 亚像素滚动位置：离右端仍超过 1px 时继续遮右侧，进入 1px 内视为到头。
  {
    label: '接近最右但未到头',
    scrollLeft: 318.5,
    scrollWidth: 640,
    clientWidth: 320,
    expected: { left: OVERFLOW_FADE_SIZE, right: OVERFLOW_FADE_SIZE }
  },
  {
    label: '最右带亚像素误差',
    scrollLeft: 319.2,
    scrollWidth: 640,
    clientWidth: 320,
    expected: { left: OVERFLOW_FADE_SIZE, right: 0 }
  }
];

for (const item of cases) {
  assertFade(item.label, overflowFade(item.scrollLeft, item.scrollWidth, item.clientWidth), item.expected);
}
