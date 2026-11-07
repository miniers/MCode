// 添加主屏幕文案的三种情况。和 useLongPress.test.ts 一样不依赖测试框架：
// 断言失败直接抛错。
import { installHintText, type InstallEnvironment } from './installHint';

function assertEqual(label: string, actual: unknown, expected: unknown): void {
  if (actual !== expected) {
    throw new Error(`${label}: 期望 ${String(expected)}，实际 ${String(actual)}`);
  }
}

function assertIncludes(label: string, haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`${label}: 期望包含 “${needle}”，实际为：${haystack}`);
  }
}

const base: InstallEnvironment = { secure: true, standalone: false, ios: false };

// 非安全上下文：标题改成明文入口，正文说明需要 HTTPS。
const insecure = installHintText({ ...base, secure: false });
assertEqual('非安全上下文标题', insecure.title, '当前为明文入口');
assertIncludes('非安全上下文正文', insecure.body, '需要 HTTPS');

// iOS：提示 Safari 分享按钮与从主屏幕打开。
const ios = installHintText({ ...base, ios: true });
assertEqual('iOS 标题', ios.title, '添加到主屏幕');
assertIncludes('iOS 正文', ios.body, '分享按钮');

// 其它平台：浏览器菜单里的安装入口。
const other = installHintText(base);
assertEqual('其它平台标题', other.title, '添加到主屏幕');
assertIncludes('其它平台正文', other.body, '安装应用');
