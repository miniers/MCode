// 同 SwarmCapsules.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后顶层断言。
import { renderToStaticMarkup } from 'react-dom/server';
import { NAME_COLORS, ROSTER_COLORS, SwarmAvatar, pickByHash } from './swarmAvatar';

function assertEqual(actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`期望 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

assertEqual(Object.keys(ROSTER_COLORS).length, 25);
assertEqual(pickByHash('Aria'), '#3B82F6');
assertEqual(pickByHash('Bram'), '#E05A47');
assertEqual(pickByHash('Zoe'), '#9333EA');

// 大小写、首尾空格与轮次后缀归一化
assertEqual(pickByHash('Aria 2'), pickByHash('aria'));
assertEqual(pickByHash('  Aria 2  '), '#3B82F6');
assertEqual(pickByHash('ZOE 10'), '#9333EA');

// 未知名字（兼容旧历史会话与自定义名）返回值稳定且落在 NAME_COLORS 中
for (const unknownName of ['Aspen', 'CustomAgent']) {
  const first = pickByHash(unknownName);
  const second = pickByHash(unknownName);
  assertEqual(first, second);
  if (!NAME_COLORS.includes(first)) {
    throw new Error(`未知名字 ${unknownName} 返回色值 ${first} 不在 NAME_COLORS 中`);
  }
}

// 柔和微渐变圆形徽章渲染校验
const avatarHtml = renderToStaticMarkup(<SwarmAvatar name="Aria" tint={pickByHash('Aria')} />);
assertIncludes(avatarHtml, '>A<');
assertIncludes(avatarHtml, 'linear-gradient');
assertIncludes(avatarHtml, 'color:#3B82F6');
