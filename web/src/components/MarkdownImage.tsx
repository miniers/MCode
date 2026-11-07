// Markdown 内联图片：远程图片直接显示，工作区内的本地图片经 files.read 读成 data URL 显示，
// 其余（工作区外、超大、格式不支持、无会话、宿主未声明能力）一律回退成原来的链接并附一句说明。

import React, { useEffect, useState } from 'react';
import { useRemoteStatic } from '../context/RemoteContext';
import { useMarkdownImageThreadID } from '../context/MarkdownImageContext';
import { bytesToDataUrl, decodeBase64ToBytes } from '../utils/mediaChunks';
import type { RemoteFilesReadResult } from '../types/contract';

/** 与宿主约定一致：单块 256 KiB，图片上限 8 MiB（internal/remote/files.go:531） */
const IMAGE_CHUNK_SIZE = 256 * 1024;
const MAX_IMAGE_BYTES = 8 * 1024 * 1024;
/** 绝对路径 → 工作区相对路径时最多剥掉的层级数（Web 端拿不到工作区根目录） */
const MAX_STRIP_DEPTH = 8;
/** 已读图片与进行中请求的缓存上限，防止长时间会话里无限增长 */
const CACHE_LIMIT = 50;

const EXTENSION_MIMES: Record<string, string> = {
  png: 'image/png',
  jpg: 'image/jpeg',
  jpeg: 'image/jpeg',
  gif: 'image/gif',
  webp: 'image/webp',
  svg: 'image/svg+xml',
  bmp: 'image/bmp',
  avif: 'image/avif'
};

type ReadRemoteFile = (
  threadID: string,
  path: string,
  offset?: number,
  length?: number
) => Promise<RemoteFilesReadResult>;

/** threadID + 原始 src → data URL（已完成的读取） */
const resolvedCache = new Map<string, string>();
/** threadID + 原始 src → 进行中的读取，避免同图多处渲染重复请求 */
const pendingCache = new Map<string, Promise<string>>();

function cachePut<T>(cache: Map<string, T>, key: string, value: T): void {
  if (cache.size >= CACHE_LIMIT && !cache.has(key)) {
    const oldest = cache.keys().next();
    if (!oldest.done) {
      cache.delete(oldest.value);
    }
  }
  cache.set(key, value);
}

function cacheKeyFor(threadID: string, raw: string): string {
  return `${threadID}\u0000${raw}`;
}

function safeDecodeURIComponent(value: string): string {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
}

/** file:///Users/x/a.png 与 file:/Users/x/a.png 都还原成 /Users/x/a.png */
function stripFileScheme(raw: string): string {
  const full = /^file:\/\/(?:localhost)?(\/.*)$/i.exec(raw);
  if (full) return safeDecodeURIComponent(full[1]);
  const bare = /^file:(\/.*)$/i.exec(raw);
  if (bare) return safeDecodeURIComponent(bare[1]);
  return raw;
}

function normalizeLocalPath(raw: string): string {
  // hast 里空格等字符已被百分号编码（/Users/a%20b.png），还原后再交给 files.read
  const decoded = safeDecodeURIComponent(stripFileScheme(raw));
  return decoded.replace(/\\/g, '/').replace(/^~\//, '/');
}

/**
 * 宿主 resolveWorkspacePath 只接受工作区相对路径（internal/remote/files.go:79 明确拒绝绝对路径与 ~ 前缀），
 * 而 Web 端拿不到工作区根目录，因此从绝对路径里逐级剥掉前缀去试，命中即用。
 */
function candidateReadPaths(raw: string): string[] {
  const path = normalizeLocalPath(raw);
  if (!path.startsWith('/')) return [path];
  const segments = path.split('/').filter((segment) => segment.length > 0);
  if (segments.length === 0) return [];
  const candidates: string[] = [];
  const maxStrip = Math.min(MAX_STRIP_DEPTH, segments.length);
  for (let strip = 1; strip <= maxStrip; strip += 1) {
    candidates.push(segments.slice(strip).join('/'));
  }
  return candidates.filter((candidate) => candidate.length > 0);
}

function mimeForPath(path: string): string {
  const match = /\.([a-z0-9]+)$/i.exec(path);
  if (!match) return '';
  return EXTENSION_MIMES[match[1].toLowerCase()] ?? '';
}

function mergeChunks(chunks: Uint8Array[], totalBytes: number): Uint8Array {
  const merged = new Uint8Array(totalBytes);
  let offset = 0;
  for (const chunk of chunks) {
    merged.set(chunk, offset);
    offset += chunk.length;
  }
  return offset === totalBytes ? merged : merged.subarray(0, offset);
}

async function readFileAsDataUrl(threadID: string, path: string, readFile: ReadRemoteFile): Promise<string> {
  const first = await readFile(threadID, path, 0, IMAGE_CHUNK_SIZE);
  if (first.kind !== 'image') {
    throw new Error('not an image file');
  }
  if (first.totalBytes > MAX_IMAGE_BYTES) {
    throw new Error(`image exceeds ${MAX_IMAGE_BYTES} bytes limit`);
  }
  const mimeType = (first.mimeType ?? '').toLowerCase() || mimeForPath(path);
  if (!mimeType.startsWith('image/')) {
    throw new Error('unsupported image mime type');
  }

  const chunks: Uint8Array[] = [];
  if (first.base64) {
    chunks.push(decodeBase64ToBytes(first.base64));
  }
  const totalBytes = first.totalBytes;
  let currentOffset = first.offset;
  let nextOffset = first.nextOffset;
  while (typeof nextOffset === 'number' && nextOffset > currentOffset && nextOffset < totalBytes) {
    currentOffset = nextOffset;
    const chunk = await readFile(threadID, path, currentOffset, IMAGE_CHUNK_SIZE);
    if (chunk.kind !== 'image' || !chunk.base64) {
      throw new Error('failed to read image chunk');
    }
    if (chunk.totalBytes !== totalBytes || chunk.offset !== currentOffset) {
      throw new Error('image chunk verification failed');
    }
    chunks.push(decodeBase64ToBytes(chunk.base64));
    nextOffset = chunk.nextOffset;
  }
  return bytesToDataUrl(mergeChunks(chunks, totalBytes), mimeType);
}

function loadWorkspaceImage(threadID: string, raw: string, readFile: ReadRemoteFile): Promise<string> {
  const key = cacheKeyFor(threadID, raw);
  const resolved = resolvedCache.get(key);
  if (resolved) return Promise.resolve(resolved);
  const pending = pendingCache.get(key);
  if (pending) return pending;

  const promise = (async () => {
    const candidates = candidateReadPaths(raw);
    if (candidates.length === 0) throw new Error('invalid path');
    let lastError: unknown = new Error('unable to read local image');
    for (const candidate of candidates) {
      try {
        const dataUrl = await readFileAsDataUrl(threadID, candidate, readFile);
        cachePut(resolvedCache, key, dataUrl);
        return dataUrl;
      } catch (err) {
        lastError = err;
      }
    }
    throw lastError instanceof Error ? lastError : new Error('unable to read local image');
  })();

  const tracked = promise.finally(() => {
    pendingCache.delete(key);
  });
  cachePut(pendingCache, key, tracked);
  return tracked;
}

function describeReadError(err: unknown): string {
  const message = err instanceof Error ? err.message : '';
  if (/invalid relative path|escapes workspace|not found|no such file/i.test(message)) {
    return '（不在当前会话的工作区内，仅显示链接）';
  }
  if (/exceeds|too large/i.test(message)) {
    return '（图片超过 8 MiB 上限，仅显示链接）';
  }
  if (/not an image|unsupported image mime/i.test(message)) {
    return '（该文件不是浏览器可显示的图片，仅显示链接）';
  }
  if (/未连接|未声明|not connected|not declared/i.test(message)) {
    return '（宿主未开放工作区文件读取，仅显示链接）';
  }
  return '（本地图片读取失败，仅显示链接）';
}

type ImageSourceKind = 'remote' | 'local' | 'other';

function classifySource(raw: string): ImageSourceKind {
  if (!raw) return 'other';
  const lower = raw.toLowerCase();
  // //host/x.png 是协议相对 URL，仍属远程资源
  if (lower.startsWith('//')) return 'remote';
  if (lower.startsWith('http://') || lower.startsWith('https://')) return 'remote';
  if (lower.startsWith('data:image/')) return 'remote';
  if (lower.startsWith('blob:')) return 'remote';
  if (lower.startsWith('file:')) return 'local';
  if (raw.startsWith('/') || raw.startsWith('~/')) return 'local';
  if (/^[a-z][a-z0-9+.-]*:/i.test(raw)) return 'other';
  return 'remote';
}

type LocalImageState =
  | { key: string; status: 'loading' }
  | { key: string; status: 'ready'; dataUrl: string }
  | { key: string; status: 'error'; note: string };

const IMAGE_CLASS = 'block max-w-full h-auto rounded-xl border border-[var(--border)]';

function ImageFallbackLink({ href, label, note }: { href: string; label: string; note?: string }) {
  return (
    <span className="block min-w-0 max-w-full">
      <a
        href={href || undefined}
        target="_blank"
        rel="noreferrer noopener"
        className="text-[var(--primary)] underline underline-offset-2 break-all rounded focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
      >
        {label}
      </a>
      {note ? (
        <span className="mt-0.5 block text-mc-caption text-[var(--foreground-muted)]">{note}</span>
      ) : null}
    </span>
  );
}

export interface MarkdownImageProps {
  src?: string | null;
  alt?: string | null;
}

export const MarkdownImage = React.memo(function MarkdownImage({ src, alt }: MarkdownImageProps) {
  const rawSrc = (src ?? '').trim();
  const label = (alt ?? '').trim() || rawSrc;
  const kind = classifySource(rawSrc);
  const threadID = useMarkdownImageThreadID();
  const { capabilities, readThreadFile } = useRemoteStatic();
  const canReadFiles = Boolean(
    capabilities?.features?.includes('files.read') && capabilities?.methods.includes('files.read')
  );
  const localKey = kind === 'local' && threadID ? cacheKeyFor(threadID, rawSrc) : null;
  const [remoteFailed, setRemoteFailed] = useState(false);
  const [local, setLocal] = useState<LocalImageState | null>(() => {
    if (!localKey) return null;
    const cached = resolvedCache.get(localKey);
    return cached ? { key: localKey, status: 'ready', dataUrl: cached } : null;
  });

  useEffect(() => {
    setRemoteFailed(false);
  }, [rawSrc]);

  useEffect(() => {
    if (kind !== 'local' || !threadID || !canReadFiles) return;
    const key = cacheKeyFor(threadID, rawSrc);
    const cached = resolvedCache.get(key);
    if (cached) {
      setLocal({ key, status: 'ready', dataUrl: cached });
      return;
    }
    let cancelled = false;
    setLocal({ key, status: 'loading' });
    loadWorkspaceImage(threadID, rawSrc, readThreadFile)
      .then((dataUrl) => {
        if (!cancelled) setLocal({ key, status: 'ready', dataUrl });
      })
      .catch((err) => {
        if (!cancelled) setLocal({ key, status: 'error', note: describeReadError(err) });
      });
    return () => {
      cancelled = true;
    };
  }, [kind, threadID, rawSrc, canReadFiles, readThreadFile]);

  if (kind === 'other' || !rawSrc) {
    return <ImageFallbackLink href={rawSrc} label={label} />;
  }

  if (kind === 'remote') {
    if (remoteFailed) {
      return <ImageFallbackLink href={rawSrc} label={label} note="（图片加载失败，仅显示链接）" />;
    }
    return (
      <a
        href={rawSrc}
        target="_blank"
        rel="noreferrer noopener"
        className="block min-w-0 max-w-full rounded-xl focus-visible:ring-2 focus-visible:ring-[var(--primary)]"
      >
        <img
          src={rawSrc}
          alt={alt ?? ''}
          loading="lazy"
          className={IMAGE_CLASS}
          onError={() => setRemoteFailed(true)}
        />
      </a>
    );
  }

  // 本地路径：只有在会话里、且宿主声明了 files.read 才可能读到内容
  if (!threadID) {
    return (
      <ImageFallbackLink href={rawSrc} label={label} note="（仅在会话里内联显示本地图片，这里仅显示链接）" />
    );
  }
  if (!canReadFiles) {
    return <ImageFallbackLink href={rawSrc} label={label} note="（宿主未开放工作区文件读取，仅显示链接）" />;
  }
  if (!local || local.key !== localKey || local.status === 'loading') {
    return (
      <span className="inline-flex max-w-full items-center gap-2 rounded-xl border border-[var(--border)] bg-[var(--user-bubble)] px-2.5 py-1.5 text-mc-caption text-[var(--foreground-muted)]">
        <span className="loading-spinner loading-spinner--small" aria-hidden="true" />
        正在读取本地图片…
      </span>
    );
  }
  if (local.status === 'error') {
    return <ImageFallbackLink href={rawSrc} label={label} note={local.note} />;
  }
  return <img src={local.dataUrl} alt={alt ?? ''} loading="lazy" title={rawSrc} className={IMAGE_CLASS} />;
});

export default MarkdownImage;
