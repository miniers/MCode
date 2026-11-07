// 同 ComposerQuotaBar.test.tsx：web/ 没有测试运行器，用 react-dom/server 渲染成字符串后直接断言。
// 覆盖三件事：iOS 空 MIME 的图片识别、附件缩略图与大图预览、附件按钮只留加号。
import { renderToStaticMarkup } from 'react-dom/server';
import type { RemoteAttachmentUploadResult } from '../types/contract';
import { isPreviewableImageFile } from '../context/remoteImages';
import {
  AttachmentPreviewOverlay,
  ComposerAttachButton,
  ComposerAttachmentStrip,
  modelSheetPageOnEscape
} from './ThreadComposer';

function assertIncludes(haystack: string, needle: string): void {
  if (!haystack.includes(needle)) {
    throw new Error(`期望渲染结果包含 “${needle}”，实际为：${haystack}`);
  }
}

function assertExcludes(haystack: string, needle: string): void {
  if (haystack.includes(needle)) {
    throw new Error(`期望渲染结果不含 “${needle}”，实际为：${haystack}`);
  }
}

function assertEqual<T>(actual: T, expected: T, what: string): void {
  if (actual !== expected) {
    throw new Error(`期望${what}为 ${String(expected)}，实际为 ${String(actual)}`);
  }
}

// ---- isPreviewableImageFile：iOS 选图时 file.type 可能为空，要按扩展名兜底 ----
// 只构造判断用到的两个字段，避免依赖运行时是否提供 File 构造器。
function fakeFile(name: string, type: string): File {
  return { name, type } as File;
}

assertEqual(isPreviewableImageFile(fakeFile('IMG_0550.jpg', '')), true, '空 MIME 的 .jpg 识别');
assertEqual(isPreviewableImageFile(fakeFile('IMG_0550.HEIC', '')), true, '空 MIME 的 .HEIC 识别');
assertEqual(isPreviewableImageFile(fakeFile('scan.PNG', 'application/octet-stream')), true, '错 MIME 的 .PNG 识别');
assertEqual(isPreviewableImageFile(fakeFile('photo', 'image/jpeg')), true, 'MIME 为 image/jpeg 识别');
assertEqual(isPreviewableImageFile(fakeFile('report.pdf', 'application/pdf')), false, 'PDF 不识别为图片');
assertEqual(isPreviewableImageFile(fakeFile('notes', '')), false, '无扩展名且无 MIME 不识别');

// ---- 附件条：有预览 URL 时渲染可点击的缩略图 ----
function attachment(overrides: Partial<RemoteAttachmentUploadResult>): RemoteAttachmentUploadResult {
  return {
    attachmentID: 'att-1',
    name: 'IMG_0550.jpg',
    mimeType: 'image/jpeg',
    byteCount: 2048,
    expiresAt: '2026-11-06T00:00:00Z',
    ...overrides
  };
}

const imageHtml = renderToStaticMarkup(
  <ComposerAttachmentStrip
    attachments={[attachment({})]}
    attachmentPreviews={{ 'att-1': 'blob:preview-1' }}
    onRemoveAttachment={() => {}}
  />
);
assertIncludes(imageHtml, 'src="blob:preview-1"');
// 缩略图本身是按钮，点它才打开大图；移除按钮单独一个 aria-label，不会触发预览。
assertIncludes(imageHtml, 'aria-label="预览附件 IMG_0550.jpg"');
assertIncludes(imageHtml, 'aria-label="移除附件 IMG_0550.jpg"');
assertExcludes(imageHtml, 'role="dialog"');

// 没有预览 URL 的附件仍显示文件名与大小，不渲染 img。
const fileHtml = renderToStaticMarkup(
  <ComposerAttachmentStrip
    attachments={[attachment({ attachmentID: 'att-2', name: 'notes.pdf', mimeType: 'application/pdf', byteCount: 512 })]}
    onRemoveAttachment={() => {}}
  />
);
assertIncludes(fileHtml, 'notes.pdf');
assertIncludes(fileHtml, '512 B');
assertExcludes(fileHtml, '<img');

// 没有附件时整条不渲染。
const emptyHtml = renderToStaticMarkup(
  <ComposerAttachmentStrip attachments={[]} onRemoveAttachment={() => {}} />
);
assertEqual(emptyHtml, '', '无附件时的渲染结果');

// ---- 大图预览：全屏黑底、Esc 与遮罩关闭由组件内的 effect 负责，这里断言结构 ----
const overlayHtml = renderToStaticMarkup(
  <AttachmentPreviewOverlay url="blob:preview-1" name="IMG_0550.jpg" onClose={() => {}} />
);
assertIncludes(overlayHtml, 'role="dialog"');
assertIncludes(overlayHtml, 'aria-modal="true"');
assertIncludes(overlayHtml, 'aria-label="图片预览 IMG_0550.jpg"');
assertIncludes(overlayHtml, 'src="blob:preview-1"');
assertIncludes(overlayHtml, 'aria-label="关闭图片预览"');
assertIncludes(overlayHtml, 'fixed inset-0');

// ---- 附件按钮：正方形图标按钮，没有「附件」文字，靠 aria-label 找到 ----
const attachHtml = renderToStaticMarkup(
  <ComposerAttachButton disabled={false} title="添加附件" onPicked={() => {}} />
);
assertIncludes(attachHtml, 'aria-label="添加附件"');
assertIncludes(attachHtml, 'title="添加附件"');
assertIncludes(attachHtml, 'h-[30px]');
assertIncludes(attachHtml, 'w-[30px]');
assertIncludes(attachHtml, 'justify-center');
assertExcludes(attachHtml, '>附件<');
assertExcludes(attachHtml, 'px-2.5');
assertExcludes(attachHtml, 'disabled=""');

const attachDisabledHtml = renderToStaticMarkup(
  <ComposerAttachButton disabled title="添加附件" onPicked={() => {}} />
);
assertIncludes(attachDisabledHtml, 'disabled=""');

// ---- 模型浮层两级导航：Esc 退级 ----
// 整个 ThreadComposer 需要 RemoteProvider 上下文，静态渲染拿不到，这里只测抽出来的纯函数。
// Esc：二级页先退回摘要页，摘要页才关整个浮层。
assertEqual(modelSheetPageOnEscape('lead'), 'summary', '主代理页 Esc 的目标页');
assertEqual(modelSheetPageOnEscape('bees'), 'summary', '子代理页 Esc 的目标页');
assertEqual(modelSheetPageOnEscape('summary'), null, '摘要页 Esc 的结果（null 表示关浮层）');
