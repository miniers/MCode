// @ts-expect-error web/ 未安装 @types/node，测试运行时由 node 提供 node:fs
import { readFileSync } from 'node:fs';

function readText(relativePath: string): string {
  return readFileSync(new URL(relativePath, import.meta.url), 'utf8');
}

function assert(condition: boolean, message: string): void {
  if (!condition) {
    throw new Error(message);
  }
}

// 1. index.css：键盘焦点环、安全区底部留白、移动端 44px 热区、开关扩展热区、16px 弹窗输入字号
const indexCss = readText('./index.css');
assert(indexCss.includes('input:focus-visible'), 'web/src/index.css 应包含 input:focus-visible 焦点环规则');
assert(!/^\s*input,\s*select,\s*textarea\s*\{\s*outline:\s*none;\s*\}/m.test(indexCss), 'web/src/index.css 不得无条件禁用 input/select/textarea 的 outline');
assert(indexCss.includes('safe-area-inset-bottom'), 'web/src/index.css 应包含 safe-area-inset-bottom');
assert(
  /--composer-bottom-inset:\s*max\([^)]*env\(safe-area-inset-bottom/.test(indexCss),
  'web/src/index.css 应使用 max(..., env(safe-area-inset-bottom...)) 定义 --composer-bottom-inset'
);
assert(
  indexCss.includes('.icon-button, .row-action-button { width: 44px; height: 44px;'),
  'web/src/index.css 应在粗指针/移动端将 .icon-button 与 .row-action-button 提升到 44px × 44px'
);
assert(indexCss.includes('.settings-switch::after'), 'web/src/index.css 应为 .settings-switch 补足扩展触控热区');
assert(indexCss.includes('font-size: 16px;'), 'web/src/index.css 应在移动端保证弹窗输入字号 >= 16px');

// 2. SheetFrame.tsx：结合 --app-height 计算高度上限，避免软键盘遮挡
const sheetFrameTsx = readText('./components/sheet/SheetFrame.tsx');
assert(sheetFrameTsx.includes('--app-height'), 'web/src/components/sheet/SheetFrame.tsx 应使用 --app-height');
assert(!sheetFrameTsx.includes('max-h-[92dvh]'), 'web/src/components/sheet/SheetFrame.tsx 不应再写死 max-h-[92dvh]');

// 3. ModelPicker.tsx：思考档位滑点补齐 focus-visible 焦点环
const modelPickerTsx = readText('./components/ModelPicker.tsx');
assert(
  modelPickerTsx.includes('focus-visible:ring-2 focus-visible:ring-[var(--primary)]'),
  'web/src/components/ModelPicker.tsx 思考档位按钮应包含 focus-visible:ring-2 focus-visible:ring-[var(--primary)]'
);

// 4. ThreadMenuSheet.tsx：桌面端 popover 与 side-panel 具备 dialog 语义、挂载聚焦、Esc 与焦点恢复
const threadMenuTsx = readText('./components/thread/ThreadMenuSheet.tsx');
assert(threadMenuTsx.includes('className="menu-popover"'), 'ThreadMenuSheet.tsx 应包含 .menu-popover');
assert(threadMenuTsx.includes('className="menu-side-panel"'), 'ThreadMenuSheet.tsx 应包含 .menu-side-panel');
assert(threadMenuTsx.includes('role="dialog"'), 'ThreadMenuSheet.tsx 桌面端弹窗应具备 role="dialog"');
assert(threadMenuTsx.includes('aria-modal="true"'), 'ThreadMenuSheet.tsx 桌面端弹窗应具备 aria-modal="true"');
assert(threadMenuTsx.includes('shouldIgnoreMenuEscape'), 'ThreadMenuSheet.tsx 应保留 Esc 关闭处理');
assert(threadMenuTsx.includes('desktopPanelRef') && threadMenuTsx.includes('previousFocusRef'), 'ThreadMenuSheet.tsx 应管理桌面端面板挂载聚焦与关闭焦点恢复');

// 5. ConfirmDialog.tsx & DiffViewer.tsx：移除硬编码色板，使用语义变量
const confirmDialogTsx = readText('./components/ConfirmDialog.tsx');
assert(!confirmDialogTsx.includes('bg-white'), 'ConfirmDialog.tsx 不得包含硬编码 bg-white');
assert(!confirmDialogTsx.includes('border-slate-200'), 'ConfirmDialog.tsx 不得包含硬编码 border-slate-200');
assert(confirmDialogTsx.includes('bg-[var(--card)]') && confirmDialogTsx.includes('border-[var(--border)]'), 'ConfirmDialog.tsx 应使用语义主题变量');

const diffViewerTsx = readText('./components/DiffViewer.tsx');
assert(!diffViewerTsx.includes('bg-slate-900'), 'DiffViewer.tsx 不得包含硬编码 bg-slate-900');
assert(!diffViewerTsx.includes('border-slate-200'), 'DiffViewer.tsx 不得包含硬编码 border-slate-200');
assert(!diffViewerTsx.includes('dark:border-zinc-800'), 'DiffViewer.tsx 不得包含硬编码 dark:border-zinc-800');
assert(!diffViewerTsx.includes('text-slate-200'), 'DiffViewer.tsx 不得包含硬编码 text-slate-200');

// 6. ThreadTimeline.tsx & ThreadView.tsx：移除原始 Emoji，首屏接入骨架屏并尊重 prefers-reduced-motion
const threadTimelineTsx = readText('./components/ThreadTimeline.tsx');
for (const emoji of ['🔐', '⏳', '📚', '🔁', '⏹', '❌']) {
  assert(!threadTimelineTsx.includes(emoji), `ThreadTimeline.tsx 不应再包含原始 Emoji ${emoji}`);
}
assert(threadTimelineTsx.includes('aria-hidden="true"'), 'ThreadTimeline.tsx 矢量图标应带 aria-hidden="true"');

const threadViewTsx = readText('./components/ThreadView.tsx');
assert(!threadViewTsx.includes('🗒️'), 'ThreadView.tsx 不得包含原始 Emoji 🗒️');
assert(!threadViewTsx.includes('⚠️'), 'ThreadView.tsx 不得包含原始 Emoji ⚠️');
assert(threadViewTsx.includes('LazyScreenFallback'), 'ThreadView.tsx 首屏加载应使用 LazyScreenFallback 骨架屏');

const skeletonCss = readText('./styles/skeleton.css');
assert(skeletonCss.includes('prefers-reduced-motion: reduce'), 'skeleton.css 应尊重 prefers-reduced-motion');
