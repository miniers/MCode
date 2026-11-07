import type {
  RemoteMediaVariant,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult
} from '../types/contract';

/** 与宿主约定的单块字节数（宿主再收紧到 256 KiB） */
export const MEDIA_CHUNK_SIZE = 256 * 1024;

/** 浏览器可直接显示的原图类型；其余（如 image/heic）改用缩略图 */
export const DISPLAYABLE_IMAGE_MIMES = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'];

export function decodeBase64ToBytes(base64: string): Uint8Array {
  const binaryString = atob(base64);
  const len = binaryString.length;
  const bytes = new Uint8Array(len);
  for (let i = 0; i < len; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes;
}

export function bytesToDataUrl(bytes: Uint8Array, mimeType: string): string {
  let binary = '';
  const len = bytes.byteLength;
  const chunkSize = 8192;
  for (let i = 0; i < len; i += chunkSize) {
    const chunk = bytes.subarray(i, Math.min(i + chunkSize, len));
    binary += String.fromCharCode.apply(null, chunk as unknown as number[]);
  }
  return `data:${mimeType};base64,${btoa(binary)}`;
}

/** 请求被取消（滚出视口、卸载、关闭查看器），调用方应静默忽略 */
export class MediaAbortError extends Error {
  constructor() {
    super('已取消');
  }
}

/** 首块返回的类型浏览器无法显示，已提前终止下载 */
export class MediaUnsupportedTypeError extends Error {
  constructor(readonly mimeType: string) {
    super('浏览器不支持该图片格式');
  }
}

export interface MediaDataUrlResult {
  dataUrl: string;
  mimeType: string;
  totalBytes: number;
}

/**
 * 按 nextOffset 逐块读取 threads.media.read 并拼成 data: URL（语义同 files.read 图片分块）。
 * acceptMime 返回 false 时在首块之后立刻抛出 MediaUnsupportedTypeError，不再下载后续块。
 */
export async function readMediaAsDataUrl(
  read: (params: RemoteThreadsMediaReadParams) => Promise<RemoteThreadsMediaReadResult>,
  base: { threadID: string; messageID: string; mediaID: string; variant: RemoteMediaVariant },
  options: {
    signal?: AbortSignal;
    acceptMime?: (mimeType: string) => boolean;
    onProgress?: (loadedBytes: number, totalBytes: number) => void;
  } = {}
): Promise<MediaDataUrlResult> {
  const { signal, acceptMime, onProgress } = options;
  const ensureActive = () => {
    if (signal?.aborted) throw new MediaAbortError();
  };

  ensureActive();
  const first = await read({ ...base, offset: 0, length: MEDIA_CHUNK_SIZE });
  ensureActive();
  if (acceptMime && !acceptMime(first.mimeType)) {
    throw new MediaUnsupportedTypeError(first.mimeType);
  }

  const mimeType = first.mimeType;
  const totalBytes = first.totalBytes;
  const chunks: Uint8Array[] = [];
  let loaded = 0;
  if (first.base64) {
    const bytes = decodeBase64ToBytes(first.base64);
    chunks.push(bytes);
    loaded += bytes.length;
  }
  onProgress?.(loaded, totalBytes);

  let currentOffset = first.offset;
  let nextOffset = first.nextOffset;
  while (nextOffset != null && nextOffset < totalBytes) {
    if (nextOffset <= currentOffset) {
      throw new Error('图片读取偏移量异常');
    }
    currentOffset = nextOffset;
    const chunk = await read({ ...base, offset: nextOffset, length: MEDIA_CHUNK_SIZE });
    ensureActive();
    if (!chunk.base64) {
      throw new Error('读取图片分块失败');
    }
    if (chunk.totalBytes !== totalBytes || chunk.offset !== currentOffset || chunk.mimeType !== mimeType) {
      throw new Error('图片分块校验失败');
    }
    const bytes = decodeBase64ToBytes(chunk.base64);
    chunks.push(bytes);
    loaded += bytes.length;
    onProgress?.(loaded, totalBytes);
    nextOffset = chunk.nextOffset;
  }

  const merged = new Uint8Array(loaded);
  let position = 0;
  for (const chunk of chunks) {
    merged.set(chunk, position);
    position += chunk.length;
  }
  return { dataUrl: bytesToDataUrl(merged, mimeType), mimeType, totalBytes };
}
