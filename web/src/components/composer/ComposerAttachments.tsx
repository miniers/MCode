import { useState } from 'react';
import { createPortal } from 'react-dom';
import type { RemoteAttachmentUploadResult } from '../../types/contract';
import { PlusIcon, XMarkIcon } from '../Icons';
import { SheetFrame } from '../sheet/SheetFrame';

function formatByteCount(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** 输入区图片缩略图的全屏预览：点遮罩或按 Esc 关闭，与时间线大图同款黑底 */
export function AttachmentPreviewOverlay({ url, name, onClose }: {
  url: string;
  name: string;
  onClose: () => void;
}) {
  const dialog = (
    <SheetFrame
      onRequestClose={onClose}
      size="bare"
      ariaLabel={`图片预览 ${name}`}
      layerClassName="fixed inset-0"
      layerStyle={{ zIndex: 100, padding: 0, background: '#000', alignItems: 'center', justifyContent: 'center' }}
      cardClassName="relative flex h-full w-full items-center justify-center outline-none"
    >
      <div
        className="flex h-full w-full items-center justify-center"
        onClick={(event) => {
          if (event.target === event.currentTarget) onClose();
        }}
      >
        <img src={url} alt={name} draggable={false} className="max-h-full max-w-full object-contain" />
        <button
          type="button"
          onClick={onClose}
          aria-label="关闭图片预览"
          className="absolute flex h-10 w-10 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur hover:bg-white/25"
          style={{
            top: 'calc(env(safe-area-inset-top, 0px) + 8px)',
            right: 'calc(env(safe-area-inset-right, 0px) + 12px)'
          }}
        >
          <XMarkIcon className="w-5 h-5" />
        </button>
      </div>
    </SheetFrame>
  );
  // 输入区容器带 backdrop-blur，会成为 fixed 的包含块，必须挂到 body 才能全屏
  return typeof document === 'undefined' ? dialog : createPortal(dialog, document.body);
}

export interface ComposerAttachmentStripProps {
  attachments: RemoteAttachmentUploadResult[];
  attachmentPreviews?: Record<string, string>;
  onRemoveAttachment: (attachmentID: string) => void;
}

/** 已添加的附件：图片显示缩略图（可点开大图），其余显示文件名与大小 */
export function ComposerAttachmentStrip({
  attachments,
  attachmentPreviews,
  onRemoveAttachment
}: ComposerAttachmentStripProps) {
  const [preview, setPreview] = useState<{ url: string; name: string } | null>(null);
  if (attachments.length === 0) return null;
  return (
    <>
      <div className="flex gap-2 overflow-x-auto pb-2 px-1" aria-label="已添加附件">
        {attachments.map((attachment) => {
          const previewURL = attachmentPreviews?.[attachment.attachmentID];
          const size = formatByteCount(attachment.byteCount);
          return (
            <div
              key={attachment.attachmentID}
              title={size ? `${attachment.name}（${size}）` : attachment.name}
              className="relative shrink-0 rounded-xl border border-[var(--border)] overflow-hidden bg-[var(--user-bubble)] flex items-center justify-center group"
              style={previewURL ? { width: 48, height: 48 } : { minWidth: 56, maxWidth: 168, height: 48 }}
            >
              {previewURL ? (
                <button
                  type="button"
                  onClick={() => setPreview({ url: previewURL, name: attachment.name })}
                  aria-label={`预览附件 ${attachment.name}`}
                  className="h-full w-full"
                >
                  <img
                    src={previewURL}
                    alt={attachment.name}
                    draggable={false}
                    className="h-full w-full object-cover"
                  />
                </button>
              ) : (
                <span className="min-w-0 px-1.5 py-1 text-left">
                  <span className="block truncate text-mc-caption leading-[14px] text-[var(--foreground)]">
                    {attachment.name || '附件'}
                  </span>
                  <span className="block truncate text-mc-caption leading-[13px] text-[var(--foreground-muted)]">
                    {size || '附件'}
                  </span>
                </span>
              )}
              <button
                type="button"
                onClick={(event) => {
                  // 移除按钮叠在缩略图上方，点击不能穿透成「打开大图」
                  event.stopPropagation();
                  onRemoveAttachment(attachment.attachmentID);
                }}
                aria-label={`移除附件 ${attachment.name}`}
                className="absolute top-0.5 right-0.5 w-4 h-4 rounded-full bg-black/60 text-white flex items-center justify-center text-mc-caption hover:bg-black"
              >
                ×
              </button>
            </div>
          );
        })}
      </div>
      {preview && (
        <AttachmentPreviewOverlay url={preview.url} name={preview.name} onClose={() => setPreview(null)} />
      )}
    </>
  );
}

export interface ComposerAttachButtonProps {
  disabled: boolean;
  title: string;
  onPicked: (files: FileList | null) => void;
}

/** 附件入口：只留加号的正方形图标按钮，文字说明交给 title 与 aria-label */
export function ComposerAttachButton({ disabled, title, onPicked }: ComposerAttachButtonProps) {
  return (
    <label
      title={title}
      className="h-[30px] w-[30px] rounded-full inline-flex items-center justify-center shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)] hover:text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] cursor-pointer active:scale-95 transition"
    >
      <PlusIcon className="w-4 h-4" />
      <input
        type="file"
        multiple
        disabled={disabled}
        aria-label="添加附件"
        onChange={(event) => {
          onPicked(event.target.files);
          event.currentTarget.value = '';
        }}
        className="sr-only"
      />
    </label>
  );
}

