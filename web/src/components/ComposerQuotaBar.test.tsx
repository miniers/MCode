// 同 LazyFallback.test.tsx：web/ 没有测试运行器，顶层直接断言，失败时 throw。
// 这里只校验额度条的 class 字符串：真实排版（是否占文档流、是否被裁切）由浏览器负责。
// 额度数据来自远端，组件本体依赖 RemoteContext，因此不渲染，直接断言导出的常量。
import {
  QUOTA_STRIP_CLASS,
  QUOTA_STRIP_FLOATING_CLASS,
  quotaStripClass,
} from './ComposerQuotaBar';

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望 “${haystack}” 包含 “${needle}”`);
  }
}

function assertExcludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望 “${haystack}” 不包含 “${needle}”`);
  }
}

// 悬浮态：脱离文档流贴在容器下边缘，因此不能带文档流的 mt-1 间距。
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'absolute');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'left-0');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'right-0');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'top-full');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'pt-0.5');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'z-10');
assertExcludes(QUOTA_STRIP_FLOATING_CLASS, 'mt-1');
// 悬浮后仍要能横向滚动，交互样式不能丢。
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'overflow-x-auto');
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'no-scrollbar');
// 键盘弹起时靠这个标记 class 隐藏悬浮额度条。
assertIncludes(QUOTA_STRIP_FLOATING_CLASS, 'composer-quota-floating');

// 非悬浮态（默认）：与改动前的文档流行为一致。
assertIncludes(QUOTA_STRIP_CLASS, 'mt-1');
assertExcludes(QUOTA_STRIP_CLASS, 'absolute');
assertExcludes(QUOTA_STRIP_CLASS, 'top-full');
assertIncludes(QUOTA_STRIP_CLASS, 'overflow-x-auto');
assertIncludes(QUOTA_STRIP_CLASS, 'no-scrollbar');
assertExcludes(QUOTA_STRIP_CLASS, 'composer-quota-floating');

// 函数入口与常量必须一致，避免两处各说各话。
if (quotaStripClass(true) !== QUOTA_STRIP_FLOATING_CLASS) {
  throw new Error(`quotaStripClass(true) 应等于 QUOTA_STRIP_FLOATING_CLASS，实际为：${quotaStripClass(true)}`);
}
if (quotaStripClass(false) !== QUOTA_STRIP_CLASS) {
  throw new Error(`quotaStripClass(false) 应等于 QUOTA_STRIP_CLASS，实际为：${quotaStripClass(false)}`);
}
