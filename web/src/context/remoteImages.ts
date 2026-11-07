// 远程附件辅助：图片解码/压缩，任意文件的读取与上传准入

export const MAX_IMAGE_EDGE = 2048;
export const MAX_IMAGE_BYTES = 2.8 * 1024 * 1024;

export type DecodedImage = {
  source: CanvasImageSource;
  width: number;
  height: number;
  dispose: () => void;
};

export async function decodeImage(file: File): Promise<DecodedImage> {
  try {
    const bitmap = await createImageBitmap(file);
    return { source: bitmap, width: bitmap.width, height: bitmap.height, dispose: () => bitmap.close() };
  } catch {
    const objectURL = URL.createObjectURL(file);
    try {
      const image = await new Promise<HTMLImageElement>((resolve, reject) => {
        const element = new Image();
        element.onload = () => resolve(element);
        element.onerror = () => reject(new Error('读取图片失败'));
        element.src = objectURL;
      });
      return {
        source: image,
        width: image.naturalWidth,
        height: image.naturalHeight,
        dispose: () => URL.revokeObjectURL(objectURL)
      };
    } catch (error) {
      URL.revokeObjectURL(objectURL);
      throw error;
    }
  }
}

export function canvasBlob(canvas: HTMLCanvasElement, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error('压缩图片失败')), 'image/jpeg', quality);
  });
}

export async function prepareImage(file: File): Promise<File> {
  const decoded = await decodeImage(file);
  try {
    if (!decoded.width || !decoded.height) throw new Error('图片尺寸无效');
    let scale = Math.min(1, MAX_IMAGE_EDGE / Math.max(decoded.width, decoded.height));
    const baseName = file.name.replace(/\.[^/.]+$/, '') || 'image';

    for (let resizeAttempt = 0; resizeAttempt < 5; resizeAttempt += 1) {
      const width = Math.max(1, Math.round(decoded.width * scale));
      const height = Math.max(1, Math.round(decoded.height * scale));
      const canvas = document.createElement('canvas');
      canvas.width = width;
      canvas.height = height;
      const context = canvas.getContext('2d');
      if (!context) throw new Error('当前浏览器不支持图片压缩');
      context.drawImage(decoded.source, 0, 0, width, height);

      for (const quality of [0.82, 0.72, 0.62, 0.52, 0.42, 0.32]) {
        const blob = await canvasBlob(canvas, quality);
        if (blob.size <= MAX_IMAGE_BYTES) return new File([blob], `${baseName}.jpg`, { type: 'image/jpeg' });
      }
      scale *= 0.82;
    }
    throw new Error('图片压缩后仍超过 2.8 MiB');
  } finally {
    decoded.dispose();
  }
}

export function readAsDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => typeof reader.result === 'string' ? resolve(reader.result) : reject(new Error('读取附件失败'));
    reader.onerror = () => reject(new Error('读取附件失败'));
    reader.readAsDataURL(file);
  });
}

/** 浏览器可直接解码的图片才走压缩链路，其余文件按普通附件原样上传 */
export function isImageFile(file: File): boolean {
  return file.type.toLowerCase().startsWith('image/');
}

// 输入区缩略图按扩展名兜底：iOS 从相册/文件选图时 file.type 可能为空或非 image/*
const PREVIEWABLE_IMAGE_EXTENSIONS = ['jpg', 'jpeg', 'png', 'gif', 'webp', 'heic', 'heif'];

/** 输入区缩略图判断：MIME 不可靠（iOS 常见空 type）时按扩展名识别图片 */
export function isPreviewableImageFile(file: File): boolean {
  if (isImageFile(file)) return true;
  const name = file.name.toLowerCase();
  const dot = name.lastIndexOf('.');
  return dot >= 0 && PREVIEWABLE_IMAGE_EXTENSIONS.includes(name.slice(dot + 1));
}

/** 图片压成 JPEG；非图片原样返回，由宿主按附件落盘（与桌面端一致） */
export async function prepareAttachment(file: File): Promise<File> {
  if (!isImageFile(file)) return file;
  return prepareImage(file);
}
