import React from 'react';
import { createPortal } from 'react-dom';
import type {
  RemoteMediaRef,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult
} from '../types/contract';
import { RpcError } from '../services/rpc';
import {
  DISPLAYABLE_IMAGE_MIMES,
  MediaAbortError,
  MediaUnsupportedTypeError,
  decodeBase64ToBytes,
  readMediaAsDataUrl
} from '../utils/mediaChunks';
import { ArrowDownIcon, ArrowPathIcon, ChevronRightIcon, DocumentTextIcon, XMarkIcon } from './Icons';
import {
  createGalleryItem,
  getThreadGallery,
  registerGalleryItem,
  type GalleryItem
} from './mediaGallery';

export type ReadThreadMedia = (params: RemoteThreadsMediaReadParams) => Promise<RemoteThreadsMediaReadResult>;

/** 宿主对原图的大小上限（8 MiB），超出时直接提示，不再请求 */
const MAX_FULL_BYTES = 8 * 1024 * 1024;
/** 同时进行的 threads.media.read 链路数 */
const MAX_CONCURRENT_READS = 3;
/** 内存缓存上限：张数与 data URL 字符串总长度 */
const CACHE_MAX_ENTRIES = 20;
const CACHE_MAX_CHARS = 30 * 1024 * 1024;

type MediaFailure = 'notFound' | 'unavailable' | 'tooLarge' | 'error';

// ---- 模块级 LRU 缓存：key = threadID:messageID:mediaID:variant ----
const dataUrlCache = new Map<string, string>();
let dataUrlCacheChars = 0;

function cacheGet(key: string): string | undefined {
  const value = dataUrlCache.get(key);
  if (value !== undefined) {
    // 命中后移到队尾，保持最近使用
    dataUrlCache.delete(key);
    dataUrlCache.set(key, value);
  }
  return value;
}

function cachePut(key: string, value: string): void {
  const existing = dataUrlCache.get(key);
  if (existing !== undefined) {
    dataUrlCacheChars -= existing.length;
    dataUrlCache.delete(key);
  }
  if (value.length > CACHE_MAX_CHARS) return;
  dataUrlCache.set(key, value);
  dataUrlCacheChars += value.length;
  while (dataUrlCache.size > CACHE_MAX_ENTRIES || dataUrlCacheChars > CACHE_MAX_CHARS) {
    const oldest = dataUrlCache.keys().next().value;
    if (oldest === undefined) break;
    dataUrlCacheChars -= dataUrlCache.get(oldest)?.length ?? 0;
    dataUrlCache.delete(oldest);
  }
}

// ---- 简单信号量：限制并发请求数；排队中被取消的任务不会启动 ----
let activeReads = 0;
const readWaiters: Array<() => void> = [];

async function withReadSlot<T>(signal: AbortSignal, task: () => Promise<T>): Promise<T> {
  if (signal.aborted) throw new MediaAbortError();
  if (activeReads < MAX_CONCURRENT_READS) {
    activeReads += 1;
  } else {
    await new Promise<void>((resolve, reject) => {
      const grant = () => {
        signal.removeEventListener('abort', onAbort);
        resolve();
      };
      const onAbort = () => {
        const index = readWaiters.indexOf(grant);
        if (index >= 0) readWaiters.splice(index, 1);
        reject(new MediaAbortError());
      };
      readWaiters.push(grant);
      signal.addEventListener('abort', onAbort, { once: true });
    });
    // 名额由释放方直接转交，activeReads 不变
  }
  try {
    return await task();
  } finally {
    const next = readWaiters.shift();
    if (next) next();
    else activeReads -= 1;
  }
}

function classifyFailure(err: unknown): MediaFailure {
  if (err instanceof RpcError) {
    if (err.code === -32004) return 'notFound';
    if (err.code === -32005) return 'tooLarge';
    if (err.code === -32602 && (err.reason === undefined || err.reason === 'unavailable')) return 'unavailable';
  }
  return 'error';
}

function failureText(failure: MediaFailure, variant: 'thumb' | 'full'): string {
  if (failure === 'notFound') return '图片已不可用';
  if (failure === 'unavailable') return '该图片无法显示';
  if (failure === 'tooLarge') return variant === 'full' ? '原图较大，请在 Mac 上查看原图' : '图片过大，无法预览';
  return variant === 'full' ? '原图加载失败' : '加载失败';
}

function tileBox(media: RemoteMediaRef, size: 'compact' | 'large'): { width: number; aspectRatio: number } {
  const longEdge = size === 'large' ? 200 : 112;
  const { width, height } = media;
  if (typeof width === 'number' && typeof height === 'number' && width > 0 && height > 0) {
    // 极端长宽比收窄到 1:2 ~ 2:1，查看器里仍显示完整图
    const ratio = Math.min(2, Math.max(0.5, width / height));
    return { width: Math.round(ratio >= 1 ? longEdge : longEdge * ratio), aspectRatio: ratio };
  }
  return { width: longEdge, aspectRatio: 1 };
}

function mediaKey(threadID: string, messageID: string, mediaID: string, variant: 'thumb' | 'full'): string {
  return `${threadID}:${messageID}:${mediaID}:${variant}`;
}

const Spinner: React.FC = () => (
  <span
    className="inline-block w-4 h-4 border-2 border-[var(--primary)] border-t-transparent rounded-full animate-spin"
    role="status"
    aria-label="加载中"
  />
);

/** 浏览器能直接解码的图片扩展名；mimeType 缺失时用来判断当成图片还是文件附件 */
const IMAGE_EXTENSIONS = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.heic', '.heif', '.avif', '.bmp'];

/** 图片走缩略图平铺（沿用 DISPLAYABLE_IMAGE_MIMES 的解码逻辑），其它附件一律渲染文件胶囊 */
function isImageMedia(media: RemoteMediaRef): boolean {
  const mimeType = (media.mimeType ?? '').toLowerCase();
  if (mimeType.startsWith('image/')) return true;
  const name = (media.name ?? '').toLowerCase();
  return IMAGE_EXTENSIONS.some((extension) => name.endsWith(extension));
}

function formatByteCount(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '';
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function fileExtension(name: string): string {
  const dot = name.lastIndexOf('.');
  if (dot <= 0 || dot === name.length - 1) return '';
  return name.slice(dot + 1).toUpperCase();
}

/** 把读取到的附件内容交给浏览器保存（download 属性用原名） */
function saveBlob(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = fileName;
  anchor.rel = 'noopener';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  // 立即 revoke 会让部分浏览器的下载中途失败
  window.setTimeout(() => URL.revokeObjectURL(url), 10000);
}

type CapsulePhase = 'idle' | 'downloading' | 'done' | 'error';

interface FileCapsuleProps {
  threadID: string;
  messageID: string;
  media: RemoteMediaRef;
  readThreadMedia: ReadThreadMedia;
}

/** 非图片附件：文件图标 + 原名，点击读取完整内容后触发下载（对应桌面端 renderUserMessageFileCapsule） */
const FileCapsule: React.FC<FileCapsuleProps> = ({ threadID, messageID, media, readThreadMedia }) => {
  const cleared = media.byteCount === 0;
  const tooLarge = media.byteCount > MAX_FULL_BYTES;
  const [phase, setPhase] = React.useState<CapsulePhase>('idle');
  const [percent, setPercent] = React.useState<number | null>(null);
  const [note, setNote] = React.useState<string | null>(null);
  const mountedRef = React.useRef(true);
  const readRef = React.useRef(readThreadMedia);
  readRef.current = readThreadMedia;

  React.useEffect(() => () => {
    mountedRef.current = false;
  }, []);

  const meta = [fileExtension(media.name), formatByteCount(media.byteCount)].filter(Boolean).join(' · ');
  const downloading = phase === 'downloading';
  const disabled = cleared || tooLarge || downloading;

  let hint = '';
  if (cleared) hint = '已清理';
  else if (tooLarge) hint = '文件较大，请在 Mac 上下载';
  else if (downloading) hint = percent === null ? '正在下载…' : `正在下载 ${percent}%`;
  else if (phase === 'done') hint = '已开始下载';
  else hint = note ?? meta;

  const startDownload = async () => {
    if (disabled) return;
    const controller = new AbortController();
    setPhase('downloading');
    setPercent(null);
    setNote(null);
    try {
      const result = await withReadSlot(controller.signal, () =>
        readMediaAsDataUrl(
          readRef.current,
          { threadID, messageID, mediaID: media.id, variant: 'full' },
          {
            signal: controller.signal,
            onProgress: (loaded, total) => {
              if (!mountedRef.current || controller.signal.aborted) return;
              setPercent(total > 0 ? Math.min(100, Math.floor((loaded / total) * 100)) : null);
            }
          }
        )
      );
      const comma = result.dataUrl.indexOf(',');
      const bytes = decodeBase64ToBytes(comma >= 0 ? result.dataUrl.slice(comma + 1) : '');
      // Blob 需要 ArrayBuffer 支撑的数据：拷贝一份，确保类型是 Uint8Array<ArrayBuffer>
      const blob = new Blob([new Uint8Array(bytes)], {
        type: result.mimeType || media.mimeType || 'application/octet-stream'
      });
      saveBlob(blob, media.name || '附件');
      if (!mountedRef.current) return;
      setPhase('done');
    } catch (err) {
      if (!mountedRef.current || err instanceof MediaAbortError) return;
      const failure = classifyFailure(err);
      setPhase('error');
      setNote(
        err instanceof MediaUnsupportedTypeError
          ? '该文件类型无法下载'
          : failure === 'notFound'
          ? '文件已不可用'
          : failure === 'tooLarge'
          ? '文件较大，请在 Mac 上下载'
          : '下载失败，点按重试'
      );
    }
  };

  return (
    <button
      type="button"
      onClick={() => void startDownload()}
      disabled={disabled}
      title={`${media.name}${hint ? `（${hint}）` : ''}`}
      aria-label={`${media.name || '附件'}，${hint || '点击下载'}`}
      className={`flex max-w-[248px] min-w-0 items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] px-2.5 py-1.5 text-left transition active:scale-[0.98] ${disabled ? 'opacity-60' : ''}`}
    >
      <DocumentTextIcon className="h-4 w-4 shrink-0 text-[var(--foreground-muted)]" />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-mc-body leading-[18px] text-[var(--foreground)]">{media.name || '附件'}</span>
        <span className="block truncate text-mc-caption leading-[15px] text-[var(--foreground-muted)]">
          {hint || '附件'}
        </span>
      </span>
      {downloading ? (
        <Spinner />
      ) : (
        !disabled && <ArrowDownIcon className="h-3.5 w-3.5 shrink-0 text-[var(--foreground-muted)]" />
      )}
    </button>
  );
};

interface MediaTileProps {
  threadID: string;
  messageID: string;
  media: RemoteMediaRef;
  size: 'compact' | 'large';
  readThreadMedia: ReadThreadMedia;
  onOpen: (item: GalleryItem) => void;
}

const MediaTile: React.FC<MediaTileProps> = ({ threadID, messageID, media, size, readThreadMedia, onOpen }) => {
  const key = mediaKey(threadID, messageID, media.id, 'thumb');
  const cleared = media.byteCount === 0;
  const [src, setSrc] = React.useState<string | null>(() => (cleared ? null : cacheGet(key) ?? null));
  const [failure, setFailure] = React.useState<MediaFailure | null>(null);
  const [visible, setVisible] = React.useState(false);
  const boxRef = React.useRef<HTMLDivElement | null>(null);
  const readRef = React.useRef(readThreadMedia);
  readRef.current = readThreadMedia;

  // 画廊条目：挂载时登记，点击打开查看器时交给它按文档顺序定位当前图片
  const entryRef = React.useRef<GalleryItem | null>(null);
  if (entryRef.current === null) {
    entryRef.current = createGalleryItem({ threadID, messageID, media, readThreadMedia });
  }
  const entry = entryRef.current;
  entry.media = media;
  entry.readThreadMedia = readThreadMedia;
  entry.thumbSrc = cleared ? null : src;

  React.useEffect(() => {
    entry.anchor = boxRef.current;
    return registerGalleryItem(entry);
  }, [entry]);

  const needsLoad = !cleared && src === null;

  // 进入视口才请求；滚出视口会取消（见下方 effect 的 cleanup）
  React.useEffect(() => {
    if (!needsLoad) return;
    const element = boxRef.current;
    if (!element) return;
    if (typeof IntersectionObserver === 'undefined') {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver(
      (entries) => {
        const last = entries[entries.length - 1];
        if (last) setVisible(last.isIntersecting);
      },
      { rootMargin: '200px 0px' }
    );
    observer.observe(element);
    return () => observer.disconnect();
  }, [needsLoad]);

  React.useEffect(() => {
    if (!needsLoad || !visible || failure) return;
    const controller = new AbortController();
    withReadSlot(controller.signal, () =>
      readMediaAsDataUrl(
        readRef.current,
        { threadID, messageID, mediaID: media.id, variant: 'thumb' },
        { signal: controller.signal }
      )
    )
      .then((result) => {
        cachePut(key, result.dataUrl);
        if (controller.signal.aborted) return;
        setSrc(result.dataUrl);
      })
      .catch((err) => {
        if (controller.signal.aborted || err instanceof MediaAbortError) return;
        setFailure(classifyFailure(err));
      });
    return () => controller.abort();
  }, [needsLoad, visible, failure, key, threadID, messageID, media.id]);

  const box = tileBox(media, size);
  const loading = needsLoad && visible && !failure;

  return (
    <div
      ref={boxRef}
      className="relative overflow-hidden rounded-xl border border-[var(--border)] bg-[var(--card-alt,var(--card))] shrink-0"
      style={{ width: box.width, maxWidth: '100%', aspectRatio: box.aspectRatio }}
      title={media.name}
    >
      {src ? (
        <button
          type="button"
          onClick={() => onOpen(entry)}
          aria-label={`查看图片 ${media.name}`}
          className="absolute inset-0 h-full w-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
        >
          <img src={src} alt={media.name} draggable={false} className="h-full w-full object-cover" />
        </button>
      ) : (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-1 p-1.5 text-center text-mc-caption leading-tight text-[var(--foreground-muted)]">
          {cleared ? (
            <span>已清理</span>
          ) : failure ? (
            <>
              <span>{failureText(failure, 'thumb')}</span>
              {failure !== 'notFound' && failure !== 'unavailable' && (
                <button
                  type="button"
                  onClick={() => setFailure(null)}
                  aria-label={`重试加载图片 ${media.name}`}
                  className="inline-flex min-h-[28px] items-center gap-1 rounded-lg border border-[var(--border)] bg-[var(--card)] px-2 text-mc-caption text-[var(--foreground)] active:scale-95"
                >
                  <ArrowPathIcon className="w-3 h-3" />
                  <span>重试</span>
                </button>
              )}
            </>
          ) : loading ? (
            <Spinner />
          ) : null}
        </div>
      )}
    </div>
  );
};

type ViewerPhase = 'loading' | 'done' | 'unsupported' | MediaFailure;

interface ViewerContentProps {
  item: GalleryItem;
  index: number;
  total: number;
  onClose: () => void;
  onNavigate: (next: number) => void;
}

/** 单张图片的加载与展示：按条目 key 重新挂载，切走时旧请求随卸载取消 */
const ViewerContent: React.FC<ViewerContentProps> = ({ item, index, total, onClose, onNavigate }) => {
  const fullKey = mediaKey(item.threadID, item.messageID, item.media.id, 'full');
  const [fullSrc, setFullSrc] = React.useState<string | null>(() => cacheGet(fullKey) ?? null);
  const [phase, setPhase] = React.useState<ViewerPhase>(() => {
    if (cacheGet(fullKey)) return 'done';
    return item.media.byteCount > MAX_FULL_BYTES ? 'tooLarge' : 'loading';
  });
  const [progress, setProgress] = React.useState<{ loaded: number; total: number } | null>(null);
  const closeRef = React.useRef<HTMLButtonElement | null>(null);
  const readRef = React.useRef(item.readThreadMedia);
  readRef.current = item.readThreadMedia;

  React.useEffect(() => {
    closeRef.current?.focus();
  }, []);

  React.useEffect(() => {
    if (phase !== 'loading') return;
    const controller = new AbortController();
    withReadSlot(controller.signal, () =>
      readMediaAsDataUrl(
        readRef.current,
        { threadID: item.threadID, messageID: item.messageID, mediaID: item.media.id, variant: 'full' },
        {
          signal: controller.signal,
          acceptMime: (mimeType) => DISPLAYABLE_IMAGE_MIMES.includes(mimeType.toLowerCase()),
          onProgress: (loaded, bytes) => {
            if (!controller.signal.aborted) setProgress({ loaded, total: bytes });
          }
        }
      )
    )
      .then((result) => {
        cachePut(fullKey, result.dataUrl);
        if (controller.signal.aborted) return;
        setFullSrc(result.dataUrl);
        setPhase('done');
      })
      .catch((err) => {
        if (controller.signal.aborted || err instanceof MediaAbortError) return;
        if (err instanceof MediaUnsupportedTypeError) {
          setPhase('unsupported');
          return;
        }
        setPhase(classifyFailure(err));
      });
    return () => controller.abort();
  }, [phase, fullKey, item.threadID, item.messageID, item.media.id]);

  const percent = progress && progress.total > 0
    ? Math.min(100, Math.floor((progress.loaded / progress.total) * 100))
    : null;
  const canRetry = phase === 'error';
  const note =
    phase === 'unsupported'
      ? '浏览器不支持原图格式，已显示缩略图'
      : phase === 'loading' || phase === 'done'
      ? null
      : failureText(phase, 'full');

  const displaySrc = fullSrc ?? item.thumbSrc;
  const showNav = total > 1;

  return (
    <>
      {displaySrc && (
        <img
          src={displaySrc}
          alt={item.media.name}
          draggable={false}
          className="max-h-full max-w-full object-contain select-none"
        />
      )}

      <div
        className="absolute right-16 flex items-center gap-2 text-xs text-white/80 pointer-events-none"
        style={{ top: 'calc(env(safe-area-inset-top, 0px) + 18px)', left: 'calc(env(safe-area-inset-left, 0px) + 12px)' }}
      >
        <span className="truncate">{item.media.name}</span>
        {showNav && (
          <span className="shrink-0 rounded-full bg-white/10 px-2 py-0.5 tabular-nums">
            {`第 ${index + 1} / 共 ${total} 张`}
          </span>
        )}
      </div>
      <button
        ref={closeRef}
        type="button"
        onClick={onClose}
        aria-label="关闭图片预览"
        className="absolute flex h-10 w-10 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur hover:bg-white/25 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
        style={{ top: 'calc(env(safe-area-inset-top, 0px) + 8px)', right: 'calc(env(safe-area-inset-right, 0px) + 12px)' }}
      >
        <XMarkIcon className="w-5 h-5" />
      </button>

      {/* 首尾不循环，与桌面端画廊一致：第一张没有「上一张」，最后一张没有「下一张」 */}
      {showNav && index > 0 && (
        <button
          type="button"
          onClick={() => onNavigate(index - 1)}
          aria-label="上一张"
          className="absolute left-2 top-1/2 flex h-10 w-10 -translate-y-1/2 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur hover:bg-white/25 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
          style={{ marginLeft: 'env(safe-area-inset-left, 0px)' }}
        >
          <ChevronRightIcon className="w-5 h-5 rotate-180" />
        </button>
      )}
      {showNav && index + 1 < total && (
        <button
          type="button"
          onClick={() => onNavigate(index + 1)}
          aria-label="下一张"
          className="absolute right-2 top-1/2 flex h-10 w-10 -translate-y-1/2 items-center justify-center rounded-full bg-white/15 text-white backdrop-blur hover:bg-white/25 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
          style={{ marginRight: 'env(safe-area-inset-right, 0px)' }}
        >
          <ChevronRightIcon className="w-5 h-5" />
        </button>
      )}

      {(phase === 'loading' || note) && (
        <div
          className="absolute left-3 right-3 flex items-center justify-center gap-2"
          style={{ bottom: 'calc(env(safe-area-inset-bottom, 0px) + 16px)' }}
        >
          <span className="inline-flex items-center gap-2 rounded-full bg-black/70 px-3 py-1.5 text-xs text-white/90">
            {phase === 'loading' ? (
              <>
                <span className="inline-block w-3 h-3 border-2 border-white/80 border-t-transparent rounded-full animate-spin" />
                <span>{percent === null ? '正在加载原图…' : `正在加载原图 ${percent}%`}</span>
              </>
            ) : (
              <span>{note}</span>
            )}
          </span>
          {canRetry && (
            <button
              type="button"
              onClick={() => {
                setProgress(null);
                setPhase('loading');
              }}
              aria-label="重试加载原图"
              className="inline-flex min-h-[32px] items-center gap-1 rounded-full bg-white/15 px-3 text-xs text-white hover:bg-white/25 active:scale-95"
            >
              <ArrowPathIcon className="w-3.5 h-3.5" />
              <span>重试</span>
            </button>
          )}
        </div>
      )}
    </>
  );
};

interface MediaViewerProps {
  /** 当前打开的图片；会话内其它图片由画廊注册表提供 */
  item: GalleryItem;
  onClose: () => void;
}

/** 大图浮层：打开时从画廊注册表取同会话的图片定位当前项，支持按钮、方向键与触屏滑动翻页 */
const MediaViewer: React.FC<MediaViewerProps> = ({ item, onClose }) => {
  // 列表只在打开时取一次：排序由注册表负责，翻页只在这份快照里移动
  const [gallery] = React.useState<GalleryItem[]>(() => {
    const listed = getThreadGallery(item.threadID);
    return listed.some((entry) => entry.key === item.key) ? listed : [item];
  });
  const total = gallery.length;
  const [index, setIndex] = React.useState(() => {
    const found = gallery.findIndex((entry) => entry.key === item.key);
    return found < 0 ? 0 : found;
  });
  const current = gallery[Math.min(index, total - 1)];
  const touchStartRef = React.useRef<{ x: number; y: number } | null>(null);

  const navigate = React.useCallback(
    (next: number) => {
      setIndex((prev) => {
        if (next < 0 || next >= total) return prev; // 首尾不循环
        return next;
      });
    },
    [total]
  );

  React.useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        onClose();
        return;
      }
      if (total <= 1) return;
      if (event.key === 'ArrowLeft') {
        event.preventDefault();
        setIndex((prev) => Math.max(0, prev - 1));
      } else if (event.key === 'ArrowRight') {
        event.preventDefault();
        setIndex((prev) => Math.min(total - 1, prev + 1));
      }
    };
    window.addEventListener('keydown', onKeyDown, true);
    return () => window.removeEventListener('keydown', onKeyDown, true);
  }, [onClose, total]);

  // 触屏滑动：水平位移要够大且明显大于垂直位移，避免和纵向滚动、缩放与点击冲突
  const onTouchStart = (event: React.TouchEvent<HTMLDivElement>) => {
    if (total <= 1) return;
    const touch = event.touches[0];
    if (!touch) return;
    touchStartRef.current = { x: touch.clientX, y: touch.clientY };
  };

  const onTouchEnd = (event: React.TouchEvent<HTMLDivElement>) => {
    const start = touchStartRef.current;
    touchStartRef.current = null;
    if (!start || total <= 1) return;
    const touch = event.changedTouches[0];
    if (!touch) return;
    const dx = touch.clientX - start.x;
    const dy = touch.clientY - start.y;
    if (Math.abs(dx) < 44 || Math.abs(dx) <= Math.abs(dy) * 1.2) return;
    // 右滑看上一张、左滑看下一张
    setIndex((prev) => (dx > 0 ? Math.max(0, prev - 1) : Math.min(total - 1, prev + 1)));
  };

  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`图片预览 ${current.media.name}`}
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black"
      style={{
        paddingTop: 'env(safe-area-inset-top, 0px)',
        paddingBottom: 'env(safe-area-inset-bottom, 0px)',
        paddingLeft: 'env(safe-area-inset-left, 0px)',
        paddingRight: 'env(safe-area-inset-right, 0px)'
      }}
      onTouchStart={onTouchStart}
      onTouchEnd={onTouchEnd}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <ViewerContent
        key={current.key}
        item={current}
        index={index}
        total={total}
        onClose={onClose}
        onNavigate={navigate}
      />
    </div>,
    document.body
  );
};

export interface MessageMediaProps {
  threadID: string;
  messageID: string;
  media?: RemoteMediaRef[];
  readThreadMedia: ReadThreadMedia;
  /** compact：用户气泡内小网格；large：助手/工具消息内稍大 */
  size?: 'compact' | 'large';
}

function isUsableMedia(item: unknown): item is RemoteMediaRef {
  return Boolean(item) && typeof item === 'object' &&
    typeof (item as RemoteMediaRef).id === 'string' &&
    typeof (item as RemoteMediaRef).name === 'string' &&
    typeof (item as RemoteMediaRef).byteCount === 'number';
}

export const MessageMedia = React.memo<MessageMediaProps>(({
  threadID,
  messageID,
  media,
  readThreadMedia,
  size = 'compact'
}) => {
  const [viewer, setViewer] = React.useState<GalleryItem | null>(null);
  const closeViewer = React.useCallback(() => setViewer(null), []);
  const openViewer = React.useCallback((entry: GalleryItem) => setViewer(entry), []);
  const items = Array.isArray(media) ? media.filter(isUsableMedia) : [];
  if (items.length === 0) return null;

  const imageItems = items.filter(isImageMedia);
  const fileItems = items.filter((item) => !isImageMedia(item));

  return (
    <>
      <div className="flex flex-wrap gap-2 min-w-0">
        {imageItems.map((item) => (
          <MediaTile
            key={item.id}
            threadID={threadID}
            messageID={messageID}
            media={item}
            size={size}
            readThreadMedia={readThreadMedia}
            onOpen={openViewer}
          />
        ))}
        {fileItems.map((item) => (
          <FileCapsule
            key={item.id}
            threadID={threadID}
            messageID={messageID}
            media={item}
            readThreadMedia={readThreadMedia}
          />
        ))}
      </div>
      {viewer && <MediaViewer item={viewer} onClose={closeViewer} />}
    </>
  );
});
MessageMedia.displayName = 'MessageMedia';
