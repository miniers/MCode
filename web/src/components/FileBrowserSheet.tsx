// 移动端工作区文件树与安全预览抽屉组件（宿主提供 files.write 时支持内联编辑保存）

import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import type {
  RemoteFileEntry,
  RemoteFilesListResult,
  RemoteFilesReadResult,
  RemoteFilesSearchResult
} from '../types/contract';
import { useRemoteStatic } from '../context/RemoteContext';
import { useRemoteFilesWriteApi } from '../context/remoteFilesWriteApi';
import type { RemoteFilesWriteResult } from '../context/remoteFilesWriteApi';
import {
  parseMarkdown,
  type MarkdownBlock,
  type MarkdownInlineToken
} from '../utils/markdown';
import { ConfirmDialog } from './ConfirmDialog';
import { FileEditor } from './FileEditor';
import { MenuSheetHeader } from './thread/menu/MenuSheetHeader';
import { useMenuPageChrome } from './thread/menu/menuPageChrome';
import type { ThreadMenuPageHandle } from './thread/menu/menuPages';
import {
  ArrowPathIcon,
  ChevronRightIcon,
  CodeBracketIcon,
  DocumentTextIcon,
  EyeIcon,
  LockIcon,
  SearchIcon,
  XMarkIcon
} from './Icons';

export interface FileBrowserSheetProps {
  isOpen: boolean;
  onClose: () => void;
  threadID: string;
  threadTitle?: string;
  initialPath?: string;
  onPathChange?: (path: string) => void;
  onReference?: (path: string) => void;
}

const TEXT_CHUNK_SIZE = 64 * 1024;
const MAX_TEXT_PREVIEW_BYTES = 512 * 1024;
const MAX_IMAGE_BYTES = 8 * 1024 * 1024;
const ALLOWED_IMAGE_MIMES = ['image/jpeg', 'image/png', 'image/gif', 'image/webp'];

function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(1024));
  const val = bytes / Math.pow(1024, i);
  return `${val.toFixed(i === 0 ? 0 : 1)} ${units[i] || 'B'}`;
}

function decodeBase64ToBytes(base64: string): Uint8Array {
  const binaryString = atob(base64);
  const len = binaryString.length;
  const bytes = new Uint8Array(len);
  for (let i = 0; i < len; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes;
}

function bytesToDataUrl(bytes: Uint8Array, mimeType: string): string {
  let binary = '';
  const len = bytes.byteLength;
  const chunkSize = 8192;
  for (let i = 0; i < len; i += chunkSize) {
    const chunk = bytes.subarray(i, Math.min(i + chunkSize, len));
    binary += String.fromCharCode.apply(null, chunk as unknown as number[]);
  }
  return `data:${mimeType};base64,${btoa(binary)}`;
}

function isSafeHref(href: string): boolean {
  return /^(https?:\/\/|\/|#|mailto:)/i.test(href);
}

const FolderIcon: React.FC<{ className?: string }> = ({ className = 'w-4 h-4' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M2.25 12.75V12A2.25 2.25 0 0 1 4.5 9.75h3.379c.456 0 .895-.138 1.266-.395l1.01-1.01A2.25 2.25 0 0 1 11.75 7.5H19.5A2.25 2.25 0 0 1 21.75 9.75v3m-19.5 0A2.25 2.25 0 0 0 4.5 15h15a2.25 2.25 0 0 0 2.25-2.25m-19.5 0v5.25A2.25 2.25 0 0 0 4.5 20.25h15a2.25 2.25 0 0 0 2.25-2.25V12.75"
    />
  </svg>
);

const LinkIcon: React.FC<{ className?: string }> = ({ className = 'w-4 h-4' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="M13.19 8.688a4.5 4.5 0 0 1 1.242 7.244l-4.5 4.5a4.5 4.5 0 0 1-6.364-6.364l1.757-1.757m13.35-3.636 1.757-1.757a4.5 4.5 0 0 0-6.364-6.364l-4.5 4.5a4.5 4.5 0 0 0 1.242 7.244"
    />
  </svg>
);

const ArrowLeftIcon: React.FC<{ className?: string }> = ({ className = 'w-4 h-4' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="2" stroke="currentColor">
    <path strokeLinecap="round" strokeLinejoin="round" d="M10.5 19.5 3 12m0 0 7.5-7.5M3 12h18" />
  </svg>
);

const PhotoIcon: React.FC<{ className?: string }> = ({ className = 'w-4 h-4' }) => (
  <svg className={className} fill="none" viewBox="0 0 24 24" strokeWidth="1.8" stroke="currentColor">
    <path
      strokeLinecap="round"
      strokeLinejoin="round"
      d="m2.25 15.75 5.159-5.159a2.25 2.25 0 0 1 3.182 0l5.159 5.159m-1.5-1.5 1.409-1.409a2.25 2.25 0 0 1 3.182 0l2.909 2.909m-18 3.75h16.5a1.5 1.5 0 0 0 1.5-1.5V6a1.5 1.5 0 0 0-1.5-1.5H3.75A1.5 1.5 0 0 0 2.25 6v12a1.5 1.5 0 0 0 1.5 1.5Zm10.5-11.25h.008v.008h-.008V8.25Zm.375 0a.375.375 0 1 1-.75 0 .375.375 0 0 1 .75 0Z"
    />
  </svg>
);

/** 内容体 props：外壳的 isOpen 之外的全部入参，另加必填 onClose。 */
export type FileBrowserSheetBodyProps = Omit<FileBrowserSheetProps, 'isOpen'>;

const FILE_BROWSER_TITLE_ID = 'file-browser-title';

export const FileBrowserSheetBody = React.forwardRef<ThreadMenuPageHandle, FileBrowserSheetBodyProps>(
  function FileBrowserSheetBody(
    { onClose, threadID, threadTitle, initialPath, onPathChange, onReference },
    ref
  ) {
  const chrome = useMenuPageChrome();
  const {
    capabilities,
    connectionState,
    listThreadFiles,
    readThreadFile,
    searchThreadFiles
  } = useRemoteStatic();

  // 当前所处目录相对路径（根目录为空串 ""）
  const [currentPath, setCurrentPath] = useState<string>('');
  // 目录列表条目及状态
  const [directoryEntries, setDirectoryEntries] = useState<RemoteFileEntry[]>([]);
  const [directoryLoading, setDirectoryLoading] = useState<boolean>(false);
  const [directoryError, setDirectoryError] = useState<string | null>(null);
  const [directoryTruncated, setDirectoryTruncated] = useState<boolean>(false);

  // 搜索相关状态
  const [searchQuery, setSearchQuery] = useState<string>('');
  const [searchResults, setSearchResults] = useState<RemoteFilesSearchResult | null>(null);
  const [searchLoading, setSearchLoading] = useState<boolean>(false);
  const [searchError, setSearchError] = useState<string | null>(null);

  // 文件预览相关状态
  const [activeFile, setActiveFile] = useState<{ path: string; name: string } | null>(null);
  const [fileLoading, setFileLoading] = useState<boolean>(false);
  const [fileLoadingMore, setFileLoadingMore] = useState<boolean>(false);
  const [fileError, setFileError] = useState<string | null>(null);
  const [fileKind, setFileKind] = useState<'text' | 'image' | 'binary' | null>(null);
  const [fileTotalBytes, setFileTotalBytes] = useState<number>(0);
  const [fileText, setFileText] = useState<string>('');
  const [fileLoadedBytes, setFileLoadedBytes] = useState<number>(0);
  const [fileNextOffset, setFileNextOffset] = useState<number | null>(null);
  const [fileTruncated, setFileTruncated] = useState<boolean>(false);
  const [markdownMode, setMarkdownMode] = useState<'preview' | 'source'>('preview');
  const [imageSrc, setImageSrc] = useState<string | null>(null);
  const [imageLoading, setImageLoading] = useState<boolean>(false);
  // 整文件读取时的内容哈希：只有它存在时才能带 expectedHash 保存
  const [fileHash, setFileHash] = useState<string | null>(null);
  // 编辑态：进入编辑时锁定当时的路径、内容与 hash
  const [editingFile, setEditingFile] = useState<{ path: string; content: string; hash: string } | null>(null);
  const [editorDirty, setEditorDirty] = useState<boolean>(false);
  // 需要二次确认丢弃编辑改动的动作：回到预览、返回目录或关闭抽屉
  const [discardPrompt, setDiscardPrompt] = useState<'preview' | 'exit' | 'close' | null>(null);

  // 请求代际与取消控制
  const requestIdRef = useRef<number>(0);
  const fileRequestIdRef = useRef<number>(0);
  const searchGenRef = useRef<number>(0);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const hasFilesFeature = Boolean(capabilities?.features?.includes('files.read'));
  const hasListMethod = Boolean(capabilities?.methods?.includes('files.list'));
  const hasReadMethod = Boolean(capabilities?.methods?.includes('files.read'));
  const hasSearchMethod = Boolean(capabilities?.methods?.includes('files.search'));
  // 覆盖保存能力：宿主没提供 files.write 时不显示编辑入口
  const { supported: canWriteFiles } = useRemoteFilesWriteApi();
  const isConnected = connectionState === 'connected';

  // 清除搜索定时器
  const clearSearchTimer = useCallback(() => {
    if (searchTimerRef.current) {
      clearTimeout(searchTimerRef.current);
      searchTimerRef.current = null;
    }
  }, []);

  // 释放图片与旧文件内容
  const cleanupFileData = useCallback(() => {
    setImageSrc(null);
    setFileText('');
    setFileLoadedBytes(0);
    setFileNextOffset(null);
    setFileTruncated(false);
    setFileError(null);
    setFileKind(null);
    setFileTotalBytes(0);
    setFileHash(null);
  }, []);

  // 读取目录条目
  const loadDirectory = useCallback(
    async (subpath: string, reqId?: number) => {
      if (!hasFilesFeature || !hasListMethod) {
        setDirectoryError('宿主未声明 files.read / files.list 能力');
        return;
      }
      if (!isConnected) {
        setDirectoryError('未连接到 Mac，无法读取文件列表');
        return;
      }

      const id = reqId ?? ++requestIdRef.current;
      setDirectoryLoading(true);
      setDirectoryError(null);

      try {
        const result: RemoteFilesListResult = await listThreadFiles(threadID, subpath || undefined);
        if (requestIdRef.current !== id) return;
        setDirectoryEntries(result.entries);
        setDirectoryTruncated(result.truncated);
        setDirectoryError(null);
      } catch (err) {
        if (requestIdRef.current !== id) return;
        setDirectoryError(err instanceof Error ? err.message : '读取目录失败');
      } finally {
        if (requestIdRef.current === id) {
          setDirectoryLoading(false);
        }
      }
    },
    [hasFilesFeature, hasListMethod, isConnected, listThreadFiles, threadID]
  );

  // 逐块读取并校验图片
  const loadImageChunks = useCallback(
    async (path: string, initialResult: RemoteFilesReadResult, fileReqId: number) => {
      const mime = (initialResult.mimeType || '').toLowerCase();
      if (!ALLOWED_IMAGE_MIMES.includes(mime)) {
        setFileKind('binary');
        setFileTotalBytes(initialResult.totalBytes);
        return;
      }

      if (initialResult.totalBytes > MAX_IMAGE_BYTES) {
        setFileError('图片大小超过 8 MiB 限制，不支持预览');
        return;
      }

      setImageLoading(true);
      setImageSrc(null);

      const chunks: Uint8Array[] = [];
      try {
        if (initialResult.base64) {
          chunks.push(decodeBase64ToBytes(initialResult.base64));
        }

        let nextOffset = initialResult.nextOffset;
        let currentOffset = initialResult.offset;
        const totalBytes = initialResult.totalBytes;

        while (nextOffset != null && nextOffset < totalBytes) {
          if (fileRequestIdRef.current !== fileReqId) return;
          if (nextOffset <= currentOffset) {
            throw new Error('图片读取偏移量异常');
          }
          currentOffset = nextOffset;

          const chunkResult = await readThreadFile(threadID, path, nextOffset, 256 * 1024);
          if (fileRequestIdRef.current !== fileReqId) return;

          if (chunkResult.kind !== 'image' || !chunkResult.base64) {
            throw new Error('读取图片分块失败');
          }
          if (chunkResult.totalBytes !== totalBytes || chunkResult.offset !== currentOffset) {
            throw new Error('图片分块校验失败');
          }

          chunks.push(decodeBase64ToBytes(chunkResult.base64));
          nextOffset = chunkResult.nextOffset;
        }

        const totalLen = chunks.reduce((acc, c) => acc + c.length, 0);
        const merged = new Uint8Array(totalLen);
        let offset = 0;
        for (const chunk of chunks) {
          merged.set(chunk, offset);
          offset += chunk.length;
        }

        const dataUrl = bytesToDataUrl(merged, mime);
        if (fileRequestIdRef.current === fileReqId) {
          setImageSrc(dataUrl);
          setFileLoadedBytes(totalLen);
          setFileError(null);
        }
      } catch (err) {
        if (fileRequestIdRef.current === fileReqId) {
          setImageSrc(null);
          setFileError(err instanceof Error ? err.message : '加载图片失败');
        }
      } finally {
        if (fileRequestIdRef.current === fileReqId) {
          setImageLoading(false);
        }
      }
    },
    [readThreadFile, threadID]
  );

  // 处理文件读取结果
  const handleFileReadResult = useCallback(
    (filePath: string, result: RemoteFilesReadResult, fileReqId: number) => {
      setFileKind(result.kind);
      setFileTotalBytes(result.totalBytes);
      // hash 只在整文件一次读完时返回，正是保存时要回传的 expectedHash
      setFileHash(result.kind === 'text' ? result.hash ?? null : null);
      setFileError(null);

      if (result.kind === 'text') {
        const textContent = result.text || '';
        setFileText(textContent);
        const loadedBytes = result.offset + new TextEncoder().encode(textContent).byteLength;
        setFileLoadedBytes(loadedBytes);
        setFileNextOffset(result.nextOffset ?? null);

        const isOverLimit = loadedBytes >= MAX_TEXT_PREVIEW_BYTES ||
          (result.totalBytes > MAX_TEXT_PREVIEW_BYTES && loadedBytes >= MAX_TEXT_PREVIEW_BYTES);
        setFileTruncated(Boolean(
          (isOverLimit && result.totalBytes > loadedBytes) ||
          (result.truncated && result.nextOffset == null)
        ));
        if (isOverLimit) {
          setFileNextOffset(null);
        }

        const lowerPath = filePath.toLowerCase();
        if (lowerPath.endsWith('.md') || lowerPath.endsWith('.markdown')) {
          setMarkdownMode('preview');
        } else {
          setMarkdownMode('source');
        }
      } else if (result.kind === 'image') {
        void loadImageChunks(filePath, result, fileReqId);
      } else {
        // binary
        setFileKind('binary');
      }
    },
    [loadImageChunks]
  );

  // 打开并预览文件
  const openFile = useCallback(
    async (filePath: string) => {
      if (!hasFilesFeature || !hasReadMethod) {
        setFileError('宿主未声明 files.read 读文件能力');
        return;
      }
      if (!isConnected) {
        setFileError('未连接到 Mac，无法读取文件');
        return;
      }

      const fileReqId = ++fileRequestIdRef.current;
      cleanupFileData();
      const fileName = filePath.includes('/') ? filePath.slice(filePath.lastIndexOf('/') + 1) : filePath;
      setActiveFile({ path: filePath, name: fileName });
      setFileLoading(true);
      onPathChange?.(filePath);

      try {
        const result: RemoteFilesReadResult = await readThreadFile(
          threadID,
          filePath,
          0,
          TEXT_CHUNK_SIZE
        );
        if (fileRequestIdRef.current !== fileReqId) return;
        handleFileReadResult(filePath, result, fileReqId);
      } catch (err) {
        if (fileRequestIdRef.current !== fileReqId) return;
        setFileError(err instanceof Error ? err.message : '打开文件失败');
      } finally {
        if (fileRequestIdRef.current === fileReqId) {
          setFileLoading(false);
        }
      }
    },
    [
      hasFilesFeature,
      hasReadMethod,
      isConnected,
      cleanupFileData,
      onPathChange,
      readThreadFile,
      threadID,
      handleFileReadResult
    ]
  );

  // 加载文本下一段内容
  const loadNextTextChunk = useCallback(async () => {
    if (!activeFile || fileNextOffset == null || fileNextOffset < fileLoadedBytes) return;
    if (fileLoadedBytes >= MAX_TEXT_PREVIEW_BYTES) return;

    const fileReqId = ++fileRequestIdRef.current;
    setFileLoadingMore(true);

    try {
      const chunkLimit = Math.min(
        TEXT_CHUNK_SIZE,
        MAX_TEXT_PREVIEW_BYTES - fileLoadedBytes
      );
      const result: RemoteFilesReadResult = await readThreadFile(
        threadID,
        activeFile.path,
        fileNextOffset,
        chunkLimit
      );
      if (fileRequestIdRef.current !== fileReqId) return;

      if (result.kind !== 'text' || typeof result.text !== 'string') {
        setFileError('读取文本分段失败');
        return;
      }

      if (result.nextOffset != null && result.nextOffset <= fileNextOffset) {
        setFileNextOffset(null);
        return;
      }

      setFileText((prev) => prev + result.text);
      const newNextOffset = result.offset + new TextEncoder().encode(result.text).byteLength;
      setFileLoadedBytes(newNextOffset);
      setFileNextOffset(result.nextOffset ?? null);
      // 分段拼接后的内容不再对应整文件 hash，保存前必须先重新整读
      setFileHash(null);

      if (
        newNextOffset >= MAX_TEXT_PREVIEW_BYTES ||
        (result.totalBytes > MAX_TEXT_PREVIEW_BYTES && newNextOffset >= MAX_TEXT_PREVIEW_BYTES)
      ) {
        setFileTruncated(Boolean(result.truncated || result.totalBytes > newNextOffset));
        setFileNextOffset(null);
      } else {
        setFileTruncated(Boolean(result.truncated && result.nextOffset == null));
      }
    } catch (err) {
      if (fileRequestIdRef.current === fileReqId) {
        setFileError(err instanceof Error ? err.message : '加载更多文本失败');
      }
    } finally {
      if (fileRequestIdRef.current === fileReqId) {
        setFileLoadingMore(false);
      }
    }
  }, [activeFile, fileNextOffset, fileLoadedBytes, readThreadFile, threadID]);

  // 退出编辑：丢掉编辑态，回到只读预览
  const exitEditing = useCallback(() => {
    setEditingFile(null);
    setEditorDirty(false);
    setDiscardPrompt(null);
  }, []);

  // 回到只读预览（编辑器的「返回预览」）：有未保存改动时先二次确认
  const requestExitEditing = useCallback(() => {
    if (editorDirty) {
      setDiscardPrompt('preview');
      return;
    }
    exitEditing();
  }, [editorDirty, exitEditing]);

  // 返回目录：有未保存改动时先二次确认
  const requestBackToDirectory = useCallback(() => {
    if (editingFile && editorDirty) {
      setDiscardPrompt('exit');
      return;
    }
    exitEditing();
    cleanupFileData();
    setActiveFile(null);
    onPathChange?.(currentPath);
  }, [cleanupFileData, currentPath, editingFile, editorDirty, exitEditing, onPathChange]);

  // 丢弃改动确认：按触发动作分别收尾
  const handleDiscardConfirm = useCallback(() => {
    const target = discardPrompt;
    setDiscardPrompt(null);
    if (target === 'close') {
      onClose();
      return;
    }
    if (target === 'preview') {
      exitEditing();
      return;
    }
    if (target === 'exit') {
      exitEditing();
      cleanupFileData();
      setActiveFile(null);
      onPathChange?.(currentPath);
    }
  }, [cleanupFileData, currentPath, discardPrompt, exitEditing, onClose, onPathChange]);

  // 关闭请求：有未保存改动时先弹丢弃确认并返回 false，不直接关闭
  const requestClose = useCallback((): boolean => {
    if (editingFile && editorDirty) {
      setDiscardPrompt('close');
      return false;
    }
    return true;
  }, [editingFile, editorDirty]);

  useImperativeHandle(ref, () => ({ requestClose }), [requestClose]);

  // 外壳/底栏的关闭：守卫通过后才关闭
  const handleCloseIntent = useCallback(() => {
    if (!requestClose()) return;
    onClose();
  }, [onClose, requestClose]);

  // 标题栏左侧：优先走栈容器提供的 leading，否则关闭自身
  const handleHeaderLeading = useCallback(() => {
    if (!requestClose()) return;
    if (chrome.leading !== 'none') {
      chrome.onLeading();
      return;
    }
    onClose();
  }, [chrome.leading, chrome.onLeading, onClose, requestClose]);

  // 引用文件后回到会话（栈内即出栈）
  const handleReference = useCallback(
    (path: string) => {
      onReference?.(path);
      onClose();
    },
    [onClose, onReference]
  );

  // 进入编辑：只有整文件读取成功（带 hash、未截断）时才允许
  const startEditing = useCallback(() => {
    if (!activeFile || fileKind !== 'text' || !fileHash || fileTruncated) return;
    setEditingFile({ path: activeFile.path, content: fileText, hash: fileHash });
    setEditorDirty(false);
  }, [activeFile, fileHash, fileKind, fileText, fileTruncated]);

  // 保存成功：用返回的 hash 与内容刷新预览并退出编辑
  const handleEditorSaved = useCallback((savedContent: string, result: RemoteFilesWriteResult) => {
    setFileKind('text');
    setFileText(savedContent);
    setFileLoadedBytes(result.size);
    setFileTotalBytes(result.size);
    setFileNextOffset(null);
    setFileTruncated(false);
    setFileError(null);
    setFileHash(result.hash);
    setEditingFile(null);
    setEditorDirty(false);
  }, []);

  // 冲突时重新整文件读取，同时把预览同步成磁盘内容
  const reloadEditorFile = useCallback(
    async (filePath: string) => {
      const result = await readThreadFile(threadID, filePath, 0, MAX_TEXT_PREVIEW_BYTES);
      if (result.kind !== 'text' || typeof result.text !== 'string' || !result.hash) {
        throw new Error('无法重新读取该文件（可能已被删除、不再是文本或超过 512 KiB）');
      }
      setFileKind('text');
      setFileText(result.text);
      setFileLoadedBytes(result.totalBytes);
      setFileTotalBytes(result.totalBytes);
      setFileNextOffset(null);
      setFileTruncated(false);
      setFileError(null);
      setFileHash(result.hash);
      return { content: result.text, hash: result.hash };
    },
    [readThreadFile, threadID]
  );

  // 导航到指定目录
  const navigateToDirectory = useCallback(
    (dirPath: string) => {
      cleanupFileData();
      setActiveFile(null);
      setCurrentPath(dirPath);
      onPathChange?.(dirPath);
      void loadDirectory(dirPath);
    },
    [cleanupFileData, onPathChange, loadDirectory]
  );

  // 卸载时取消在途请求与防抖定时器
  useEffect(
    () => () => {
      requestIdRef.current++;
      fileRequestIdRef.current++;
      searchGenRef.current++;
      clearSearchTimer();
    },
    [clearSearchTimer]
  );

  // 挂载时按 initialPath 恢复路径并加载
  useEffect(() => {
    if (!hasFilesFeature) {
      setDirectoryError('宿主未声明 files.read 功能特性');
      return;
    }

    const reqId = ++requestIdRef.current;
    fileRequestIdRef.current++;
    cleanupFileData();
    const target = initialPath || '';
    setDirectoryLoading(true);
    setFileLoading(Boolean(target));
    setFileLoadingMore(false);
    setImageLoading(false);

    const initialize = async () => {
      if (!target) {
        setCurrentPath('');
        setActiveFile(null);
        await loadDirectory('', reqId);
        return;
      }

      const hasExtension = /\.[a-zA-Z0-9_-]+$/.test(target);

      if (hasExtension && hasReadMethod) {
        try {
          const fileReqId = ++fileRequestIdRef.current;
          const readResult = await readThreadFile(threadID, target, 0, TEXT_CHUNK_SIZE);
          if (requestIdRef.current !== reqId) return;

          const parent = target.includes('/') ? target.slice(0, target.lastIndexOf('/')) : '';
          setCurrentPath(parent);
          const fileName = target.includes('/') ? target.slice(target.lastIndexOf('/') + 1) : target;
          setActiveFile({ path: target, name: fileName });
          handleFileReadResult(target, readResult, fileReqId);
          void loadDirectory(parent, reqId);
          return;
        } catch {
          if (requestIdRef.current !== reqId) return;
        }
      }

      if (hasListMethod) {
        try {
          const listResult = await listThreadFiles(threadID, target);
          if (requestIdRef.current !== reqId) return;

          setCurrentPath(target);
          setActiveFile(null);
          setDirectoryEntries(listResult.entries);
          setDirectoryTruncated(listResult.truncated);
          setDirectoryError(null);
          return;
        } catch {
          if (requestIdRef.current !== reqId) return;
        }
      }

      if (!hasExtension && hasReadMethod) {
        try {
          const fileReqId = ++fileRequestIdRef.current;
          const readResult = await readThreadFile(threadID, target, 0, TEXT_CHUNK_SIZE);
          if (requestIdRef.current !== reqId) return;

          const parent = target.includes('/') ? target.slice(0, target.lastIndexOf('/')) : '';
          setCurrentPath(parent);
          const fileName = target.includes('/') ? target.slice(target.lastIndexOf('/') + 1) : target;
          setActiveFile({ path: target, name: fileName });
          handleFileReadResult(target, readResult, fileReqId);
          void loadDirectory(parent, reqId);
          return;
        } catch {
          if (requestIdRef.current !== reqId) return;
        }
      }

      setCurrentPath(target);
      setActiveFile(null);
      setDirectoryError(`无法打开路径 "${target}"`);
    };

    void initialize().finally(() => {
      if (requestIdRef.current !== reqId) return;
      setDirectoryLoading(false);
      setFileLoading(false);
    });
  }, [
    threadID,
    initialPath,
    hasFilesFeature,
    hasListMethod,
    hasReadMethod,
    readThreadFile,
    listThreadFiles,
    loadDirectory,
    handleFileReadResult,
    cleanupFileData,
    clearSearchTimer
  ]);

  // 处理搜索输入变更（~200ms 防抖）
  const handleSearchChange = (queryText: string) => {
    setSearchQuery(queryText);
    clearSearchTimer();

    const trimmed = queryText.trim();
    if (!trimmed) {
      searchGenRef.current++;
      setSearchResults(null);
      setSearchLoading(false);
      setSearchError(null);
      return;
    }

    if (!hasSearchMethod) {
      setSearchError('宿主未声明 files.search 搜索方法');
      return;
    }

    setSearchLoading(true);
    setSearchError(null);
    const gen = ++searchGenRef.current;

    searchTimerRef.current = setTimeout(async () => {
      try {
        const result: RemoteFilesSearchResult = await searchThreadFiles(threadID, trimmed);
        if (searchGenRef.current !== gen) return;
        setSearchResults(result);
        setSearchError(null);
      } catch (err) {
        if (searchGenRef.current !== gen) return;
        setSearchError(err instanceof Error ? err.message : '搜索文件失败');
      } finally {
        if (searchGenRef.current === gen) {
          setSearchLoading(false);
        }
      }
    }, 200);
  };

  // 面包屑段落
  const breadcrumbs = useMemo(() => {
    const list: Array<{ name: string; path: string; isFile?: boolean }> = [
      { name: '根目录', path: '' }
    ];

    const currentSegments = currentPath ? currentPath.split('/').filter(Boolean) : [];
    let accumulated = '';
    for (const seg of currentSegments) {
      accumulated = accumulated ? `${accumulated}/${seg}` : seg;
      list.push({ name: seg, path: accumulated });
    }

    if (activeFile) {
      list.push({ name: activeFile.name, path: activeFile.path, isFile: true });
    }

    return list;
  }, [currentPath, activeFile]);

  // 行号计算（生成单段字符串以通过单个 pre 渲染）
  const lineCount = useMemo(() => {
    if (!fileText) return 1;
    return fileText.split('\n').length;
  }, [fileText]);

  const lineNumbersText = useMemo(() => {
    return Array.from({ length: lineCount }, (_, i) => i + 1).join('\n');
  }, [lineCount]);

  // 安全 Markdown 渲染
  const renderInlineTokens = (inlines: MarkdownInlineToken[], keyPrefix: string): React.ReactNode => {
    return inlines.map((token, idx) => {
      const key = `${keyPrefix}-${idx}`;
      switch (token.type) {
        case 'code':
          return (
            <code
              key={key}
              className="px-1 py-0.5 rounded bg-[var(--user-bubble)] font-mono text-mc-footnote text-[var(--foreground)]"
            >
              {token.content}
            </code>
          );
        case 'strong':
          return (
            <strong key={key} className="font-semibold text-[var(--foreground)]">
              {renderInlineTokens(token.inlines, key)}
            </strong>
          );
        case 'emphasis':
          return (
            <em key={key} className="italic text-[var(--foreground)]">
              {renderInlineTokens(token.inlines, key)}
            </em>
          );
        case 'strike':
          return (
            <del key={key} className="line-through text-[var(--foreground-muted)]">
              {renderInlineTokens(token.inlines, key)}
            </del>
          );
        case 'link': {
          const safe = isSafeHref(token.href);
          return (
            <a
              key={key}
              href={safe ? token.href : '#'}
              target={safe ? '_blank' : undefined}
              rel="noreferrer noopener"
              className="text-[var(--primary)] underline underline-offset-2 break-all"
            >
              {renderInlineTokens(token.inlines, key)}
            </a>
          );
        }
        default:
          return <React.Fragment key={key}>{token.content}</React.Fragment>;
      }
    });
  };

  const renderMarkdownBlocks = (blocks: MarkdownBlock[], prefix = 'md'): React.ReactNode => {
    return blocks.map((block, index) => {
      const key = `${prefix}-${index}`;
      switch (block.type) {
        case 'heading': {
          const headingStyles = {
            1: 'text-base font-bold mt-3 mb-1.5',
            2: 'text-sm font-bold mt-2.5 mb-1',
            3: 'text-xs font-semibold mt-2 mb-1',
            4: 'text-xs font-semibold mt-1.5 mb-0.5'
          };
          return (
            <div key={key} className={`${headingStyles[block.depth]} text-[var(--foreground)]`}>
              {renderInlineTokens(block.inlines, key)}
            </div>
          );
        }
        case 'paragraph':
          return (
            <p key={key} className="text-xs leading-relaxed text-[var(--foreground)] my-1.5 whitespace-pre-wrap break-words">
              {renderInlineTokens(block.inlines, key)}
            </p>
          );
        case 'list': {
          const items = block.items.map((item, itemIdx) => (
            <li key={`${key}-${itemIdx}`} className="text-xs leading-relaxed text-[var(--foreground)]">
              {renderInlineTokens(item.inlines, `${key}-${itemIdx}`)}
              {item.children.length > 0 && renderMarkdownBlocks(item.children, `${key}-${itemIdx}-c`)}
            </li>
          ));
          return block.ordered ? (
            <ol key={key} start={block.start} className="list-decimal pl-4 my-1.5 space-y-0.5">
              {items}
            </ol>
          ) : (
            <ul key={key} className="list-disc pl-4 my-1.5 space-y-0.5">
              {items}
            </ul>
          );
        }
        case 'code':
          return (
            <div key={key} className="my-2 rounded-xl bg-[var(--user-bubble)] border border-[var(--border)] p-2.5 overflow-x-auto">
              {block.language && (
                <div className="text-mc-caption text-[var(--foreground-muted)] mb-1 font-mono uppercase">
                  {block.language}
                </div>
              )}
              <pre className="font-mono text-xs leading-relaxed text-[var(--foreground)] whitespace-pre overflow-x-auto">
                {block.content}
              </pre>
            </div>
          );
        case 'quote':
          return (
            <blockquote key={key} className="border-l-2 border-[var(--primary)] pl-2.5 my-1.5 text-[var(--foreground-muted)] italic text-xs">
              {renderMarkdownBlocks(block.blocks, key)}
            </blockquote>
          );
        case 'table':
          return (
            <div key={key} className="my-2 overflow-x-auto rounded-xl border border-[var(--border)]">
              <table className="w-full border-collapse text-xs text-[var(--foreground)]">
                <thead className="bg-[var(--user-bubble)]">
                  <tr>
                    {block.header.map((cell, col) => (
                      <th
                        key={`${key}-h${col}`}
                        style={{ textAlign: block.align[col] ?? undefined }}
                        className="px-2.5 py-1 font-semibold border-b border-[var(--border)] whitespace-nowrap"
                      >
                        {renderInlineTokens(cell.inlines, `${key}-h${col}`)}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {block.rows.map((row, rowIdx) => (
                    <tr key={`${key}-r${rowIdx}`} className="border-t border-[var(--border)] first:border-t-0">
                      {row.map((cell, col) => (
                        <td
                          key={`${key}-r${rowIdx}c${col}`}
                          style={{ textAlign: block.align[col] ?? undefined }}
                          className="px-2.5 py-1 align-top"
                        >
                          {renderInlineTokens(cell.inlines, `${key}-r${rowIdx}c${col}`)}
                        </td>
                      ))}
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          );
        case 'divider':
          return <hr key={key} className="my-2 border-[var(--border)]" />;
        default:
          return null;
      }
    });
  };

  const displayedEntries = searchResults ? searchResults.entries : directoryEntries;
  const isSearchActive = searchQuery.trim().length > 0;
  // 编辑入口：宿主支持 files.write，且文件是整文件读完的文本（有 hash、未截断）
  const canEditFile =
    canWriteFiles && fileKind === 'text' && Boolean(fileHash) && !fileTruncated && !fileLoading;

  return (
    <div
      className="flex flex-col flex-1 min-h-0 min-w-0 overflow-x-hidden"
      onKeyDown={(event) => {
        if (event.key !== 'Escape') return;
        // 丢弃确认自己处理 Esc：拦下冒泡，避免外层页面栈同时出栈
        if (discardPrompt !== null) {
          event.preventDefault();
          event.stopPropagation();
          setDiscardPrompt(null);
          return;
        }
        // 编辑器内部的 <dialog> 自己处理 Esc，只拦冒泡
        if (event.target instanceof Element && event.target.closest('dialog[open]')) {
          event.stopPropagation();
        }
      }}
    >
      <div className="shrink-0 mb-2">
        <MenuSheetHeader
          eyebrow={threadTitle ? `文件 · ${threadTitle}` : '工作区文件'}
          title={activeFile ? activeFile.name : currentPath || '根目录'}
          labelledBy={FILE_BROWSER_TITLE_ID}
          leading={chrome.leading !== 'none' ? chrome.leading : 'close'}
          onLeading={handleHeaderLeading}
          closeLabel="关闭文件浏览"
          showHandle={false}
        />
      </div>

        {/* 面包屑导航栏 */}
        <div className="shrink-0 flex items-center gap-1 overflow-x-auto no-scrollbar py-1 text-xs text-[var(--foreground-muted)] border-b border-[var(--border)] mb-2.5">
          {breadcrumbs.map((crumb, idx) => {
            const isLast = idx === breadcrumbs.length - 1;
            return (
              <React.Fragment key={crumb.path || 'root'}>
                {idx > 0 && <ChevronRightIcon className="w-3 h-3 text-[var(--foreground-muted)] shrink-0" />}
                {isLast ? (
                  <span className="font-semibold text-[var(--foreground)] truncate max-w-[140px] shrink-0">
                    {crumb.name}
                  </span>
                ) : (
                  <button
                    type="button"
                    onClick={() => navigateToDirectory(crumb.path)}
                    className="hover:text-[var(--foreground)] hover:underline truncate max-w-[120px] shrink-0 text-left"
                  >
                    {crumb.name}
                  </button>
                )}
              </React.Fragment>
            );
          })}
        </div>

        {/* 文件预览视图 */}
        {activeFile ? (
          <div className="flex-1 min-h-0 flex flex-col min-w-0 max-w-full overflow-hidden space-y-2">
            {/* 预览工具条 */}
            <div className="shrink-0 flex items-center justify-between gap-2 border-b border-[var(--border)] pb-2 min-w-0 flex-wrap gap-y-1.5">
              <div className="flex items-center gap-1.5 min-w-0">
                <button
                  type="button"
                  onClick={editingFile ? requestBackToDirectory : () => {
                    cleanupFileData();
                    setActiveFile(null);
                    onPathChange?.(currentPath);
                  }}
                  className="secondary-button text-xs py-1 px-2.5 inline-flex items-center gap-1 shrink-0"
                >
                  <ArrowLeftIcon className="w-3 h-3" />
                  <span>返回目录</span>
                </button>
                {fileTotalBytes > 0 && (
                  <span className="text-mc-caption text-[var(--foreground-muted)] font-mono shrink-0">
                    {formatBytes(fileTotalBytes)}
                  </span>
                )}
              </div>

              <div className="flex items-center gap-1.5 shrink-0">
                {!editingFile && canEditFile && (
                  <button
                    type="button"
                    onClick={startEditing}
                    className="secondary-button text-xs py-1 px-2.5 shrink-0"
                    title="编辑这个文本文件并保存回工作区"
                  >
                    编辑
                  </button>
                )}
                {!editingFile && fileKind === 'text' && (activeFile.name.endsWith('.md') || activeFile.name.endsWith('.markdown')) && (
                  <div className="inline-flex rounded-lg border border-[var(--border)] bg-[var(--card)] p-0.5 text-xs">
                    <button
                      type="button"
                      onClick={() => setMarkdownMode('preview')}
                      className={`flex items-center gap-1 px-2 py-0.5 rounded-md font-medium transition ${
                        markdownMode === 'preview'
                          ? 'bg-[var(--primary)] text-white'
                          : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                      }`}
                    >
                      <EyeIcon className="w-3 h-3" />
                      <span>预览</span>
                    </button>
                    <button
                      type="button"
                      onClick={() => setMarkdownMode('source')}
                      className={`flex items-center gap-1 px-2 py-0.5 rounded-md font-medium transition ${
                        markdownMode === 'source'
                          ? 'bg-[var(--primary)] text-white'
                          : 'text-[var(--foreground-muted)] hover:text-[var(--foreground)]'
                      }`}
                    >
                      <CodeBracketIcon className="w-3 h-3" />
                      <span>源码</span>
                    </button>
                  </div>
                )}

                {!editingFile && onReference && (
                  <button
                    type="button"
                    onClick={() => handleReference(activeFile.path)}
                    className="primary-button text-xs py-1 px-2.5 shrink-0"
                    title="将相对路径引用到当前会话草稿输入框"
                  >
                    引用到输入框
                  </button>
                )}
              </div>
            </div>

            {/* 编辑态：整文件文本直接在内联编辑器里修改，保存回工作区 */}
            {editingFile ? (
              <FileEditor
                key={editingFile.path}
                threadID={threadID}
                path={editingFile.path}
                fileName={activeFile.name}
                initialContent={editingFile.content}
                initialHash={editingFile.hash}
                onDirtyChange={setEditorDirty}
                onRequestExit={requestExitEditing}
                onSaved={handleEditorSaved}
                reload={reloadEditorFile}
              />
            ) : (
            /* 预览主体内容 */
            <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 pr-0.5">
              {fileLoading && (
                <div className="p-8 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                  <ArrowPathIcon className="w-5 h-5 mx-auto animate-spin text-[var(--primary)]" />
                  <p>正在读取文件内容…</p>
                </div>
              )}

              {fileError && !fileLoading && (
                <div className="p-4 rounded-xl border border-[var(--danger-border,var(--border))] bg-[var(--card)] text-xs space-y-2">
                  <p className="font-semibold text-[var(--danger-foreground)]">读取文件失败</p>
                  <p className="text-[var(--foreground-muted)]">{fileError}</p>
                  <button
                    type="button"
                    onClick={() => void openFile(activeFile.path)}
                    className="secondary-button text-xs py-1 px-2.5 inline-flex items-center gap-1"
                  >
                    <ArrowPathIcon className="w-3 h-3" />
                    <span>重试</span>
                  </button>
                </div>
              )}

              {!fileLoading && !fileError && fileKind === 'binary' && (
                <div className="p-8 text-center rounded-xl bg-[var(--card)] border border-[var(--border)] space-y-2">
                  <DocumentTextIcon className="w-8 h-8 mx-auto text-[var(--foreground-muted)] opacity-60" />
                  <p className="text-sm font-semibold text-[var(--foreground)]">二进制文件</p>
                  <p className="text-xs text-[var(--foreground-muted)]">
                    该文件为二进制格式，暂不支持在移动端查看内容。
                  </p>
                  {fileTotalBytes > 0 && (
                    <p className="text-mc-caption text-[var(--foreground-muted)] font-mono">
                      文件大小：{formatBytes(fileTotalBytes)}
                    </p>
                  )}
                </div>
              )}

              {!fileLoading && !fileError && fileKind === 'image' && (
                <div className="space-y-3">
                  {imageLoading && (
                    <div className="p-8 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                      <ArrowPathIcon className="w-5 h-5 mx-auto animate-spin text-[var(--primary)]" />
                      <p>正在分块加载图片…</p>
                    </div>
                  )}
                  {imageSrc && !imageLoading && (
                    <div className="flex items-center justify-center p-3 rounded-xl bg-[var(--card)] border border-[var(--border)] overflow-hidden">
                      <img
                        src={imageSrc}
                        alt={activeFile.name}
                        className="max-w-full max-h-[45dvh] object-contain rounded-lg shadow-sm"
                      />
                    </div>
                  )}
                </div>
              )}

              {!fileLoading && !fileError && fileKind === 'text' && (
                <div className="space-y-2 min-w-0">
                  {markdownMode === 'preview' && (activeFile.name.endsWith('.md') || activeFile.name.endsWith('.markdown')) ? (
                    <div className="p-3 rounded-xl bg-[var(--card)] border border-[var(--border)] select-text min-w-0">
                      {renderMarkdownBlocks(parseMarkdown(fileText))}
                    </div>
                  ) : (
                    <div className="flex font-mono text-xs leading-5 p-2.5 overflow-x-auto bg-[var(--user-bubble)] rounded-xl border border-[var(--border)] select-text min-h-[140px]">
                      {/* 单个 pre 渲染行号，避免按行创建 DOM */}
                      <pre className="select-none text-right pr-3 border-r border-[var(--border)] text-[var(--foreground-muted)] opacity-50 font-mono text-xs leading-5 shrink-0">
                        {lineNumbersText}
                      </pre>
                      {/* 单个 pre 渲染文本正文 */}
                      <pre className="pl-3 font-mono text-xs leading-5 text-[var(--foreground)] overflow-x-visible whitespace-pre font-normal flex-1">
                        {fileText}
                      </pre>
                    </div>
                  )}

                  {/* 文本分段加载与截断提示 */}
                  {fileTruncated && (
                    <div className="p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] text-xs text-[var(--foreground-muted)]">
                      {fileLoadedBytes >= MAX_TEXT_PREVIEW_BYTES
                        ? '已达到单文件文本预览上限 512 KiB，后续内容已截断。'
                        : '宿主返回的文本内容已截断，无法完整预览。'}
                    </div>
                  )}

                  {fileNextOffset != null && !fileTruncated && fileLoadedBytes < MAX_TEXT_PREVIEW_BYTES && (
                    <div className="pt-1">
                      <button
                        type="button"
                        disabled={fileLoadingMore}
                        onClick={() => void loadNextTextChunk()}
                        className="secondary-button text-xs py-1.5 px-3 w-full justify-center disabled:opacity-50"
                      >
                        {fileLoadingMore ? (
                          <span className="inline-flex items-center gap-1.5">
                            <ArrowPathIcon className="w-3.5 h-3.5 animate-spin" />
                            <span>加载中…</span>
                          </span>
                        ) : (
                          `加载更多内容 (${formatBytes(fileLoadedBytes)} / ${formatBytes(fileTotalBytes)})`
                        )}
                      </button>
                    </div>
                  )}
                </div>
              )}
            </div>
            )}
          </div>
        ) : (
          /* 目录列表视图 */
          <div className="flex-1 min-h-0 flex flex-col min-w-0 max-w-full overflow-hidden space-y-2.5">
            {/* 搜索框 */}
            <div className="shrink-0 relative">
              <SearchIcon className="w-4 h-4 absolute left-3 top-1/2 -translate-y-1/2 text-[var(--foreground-muted)]" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => handleSearchChange(e.target.value)}
                placeholder="按文件名或相对路径搜索…"
                className="w-full bg-[var(--input-surface,var(--card))] border border-[var(--border)] rounded-xl pl-9 pr-8 py-1.5 text-xs text-[var(--foreground)] placeholder:text-[var(--foreground-muted)] outline-none focus:border-[var(--primary)]"
              />
              {searchQuery && (
                <button
                  type="button"
                  onClick={() => handleSearchChange('')}
                  className="absolute right-2.5 top-1/2 -translate-y-1/2 text-[var(--foreground-muted)] hover:text-[var(--foreground)]"
                  aria-label="清空搜索"
                >
                  <XMarkIcon className="w-3.5 h-3.5" />
                </button>
              )}
            </div>

            {/* 搜索或目录截断提示 */}
            {((isSearchActive && searchResults?.truncated) || (!isSearchActive && directoryTruncated)) && (
              <div className="shrink-0 px-3 py-1.5 rounded-xl border border-[var(--border)] bg-[var(--card)] text-mc-caption text-[var(--foreground-muted)]">
                {isSearchActive ? '搜索结果已截断（仅显示部分前序条目）' : '目录条目已截断（仅显示前 1000 个条目）'}
              </div>
            )}

            {/* 目录列表主体 */}
            <div className="flex-1 min-h-0 overflow-y-auto overflow-x-hidden min-w-0 pr-0.5 space-y-1">
              {(directoryLoading || searchLoading) && (
                <div className="p-8 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                  <ArrowPathIcon className="w-5 h-5 mx-auto animate-spin text-[var(--primary)]" />
                  <p>{isSearchActive ? '正在搜索文件…' : '正在读取目录…'}</p>
                </div>
              )}

              {(directoryError || searchError) && !(directoryLoading || searchLoading) && (
                <div className="p-4 rounded-xl border border-[var(--danger-border,var(--border))] bg-[var(--card)] text-xs space-y-2">
                  <p className="font-semibold text-[var(--danger-foreground)]">
                    {searchError ? '搜索失败' : '读取目录失败'}
                  </p>
                  <p className="text-[var(--foreground-muted)]">{searchError || directoryError}</p>
                  <button
                    type="button"
                    onClick={() => {
                      if (isSearchActive) {
                        handleSearchChange(searchQuery);
                      } else {
                        void loadDirectory(currentPath);
                      }
                    }}
                    className="secondary-button text-xs py-1 px-2.5 inline-flex items-center gap-1"
                  >
                    <ArrowPathIcon className="w-3 h-3" />
                    <span>重试</span>
                  </button>
                </div>
              )}

              {!(directoryLoading || searchLoading) && !(directoryError || searchError) && displayedEntries.length === 0 && (
                <div className="p-8 text-center text-xs text-[var(--foreground-muted)] space-y-2">
                  <DocumentTextIcon className="w-6 h-6 mx-auto opacity-50" />
                  <p>{isSearchActive ? '未找到匹配的文件' : '此目录下没有文件'}</p>
                </div>
              )}

              {!(directoryLoading || searchLoading) &&
                !(directoryError || searchError) &&
                displayedEntries.map((entry) => {
                  const isDir = entry.kind === 'directory';
                  const isSymlink = entry.kind === 'symlink';
                  const isIgnored = entry.ignored;
                  const isProtected = entry.protected;

                  return (
                    <div
                      key={entry.path}
                      className={`flex items-center justify-between gap-2 px-3 py-2 rounded-xl border border-[var(--border)] bg-[var(--card)] transition ${
                        isIgnored ? 'opacity-60 text-[var(--foreground-muted)]' : 'text-[var(--foreground)]'
                      }`}
                    >
                      <button
                        type="button"
                        disabled={isProtected}
                        onClick={() => {
                          if (isProtected) return;
                          if (isDir) {
                            navigateToDirectory(entry.path);
                          } else {
                            void openFile(entry.path);
                          }
                        }}
                        className={`flex items-center gap-2 min-w-0 flex-1 text-left ${
                          isProtected ? 'cursor-not-allowed opacity-75' : 'hover:opacity-80'
                        }`}
                      >
                        {isDir ? (
                          <FolderIcon className="w-4 h-4 text-amber-500 shrink-0" />
                        ) : isSymlink ? (
                          <LinkIcon className="w-4 h-4 text-sky-500 shrink-0" />
                        ) : entry.name.match(/\.(png|jpe?g|gif|webp)$/i) ? (
                          <PhotoIcon className="w-4 h-4 text-emerald-500 shrink-0" />
                        ) : (
                          <DocumentTextIcon className="w-4 h-4 text-[var(--foreground-muted)] shrink-0" />
                        )}

                        <span className="font-mono text-xs font-medium truncate">{entry.name}</span>

                        {isProtected && (
                          <span className="inline-flex items-center gap-0.5 px-1.5 py-0.5 rounded text-mc-caption font-medium bg-amber-500/10 text-amber-600 dark:text-amber-400 shrink-0">
                            <LockIcon className="w-2.5 h-2.5" />
                            <span>受保护</span>
                          </span>
                        )}

                        {isSymlink && (
                          <span className="px-1.5 py-0.5 rounded text-mc-caption font-medium bg-sky-500/10 text-sky-600 dark:text-sky-400 shrink-0">
                            链接
                          </span>
                        )}
                      </button>

                      <div className="flex items-center gap-2 shrink-0">
                        {entry.size > 0 && !isDir && (
                          <span className="text-mc-caption font-mono text-[var(--foreground-muted)]">
                            {formatBytes(entry.size)}
                          </span>
                        )}

                        {onReference && !isProtected && (
                          <button
                            type="button"
                            onClick={() => handleReference(entry.path)}
                            className="text-mc-caption font-medium text-[var(--primary)] hover:underline px-1.5 py-0.5 rounded hover:bg-[var(--card-alt,var(--card))]"
                            title={`引用 @${entry.path} 到输入框`}
                          >
                            @引用
                          </button>
                        )}

                        {isDir && !isProtected && (
                          <ChevronRightIcon className="w-3.5 h-3.5 text-[var(--foreground-muted)]" />
                        )}
                      </div>
                    </div>
                  );
                })}
            </div>
          </div>
        )}

        {/* 底部操作关闭栏 */}
        <div className="mt-2 shrink-0 border-t border-[var(--border)] pt-2 pb-0 flex items-center justify-between text-xs text-[var(--foreground-muted)]">
          <span>
            {activeFile
              ? `已加载 ${formatBytes(fileLoadedBytes)} / 共 ${formatBytes(fileTotalBytes)}`
              : `共 ${displayedEntries.length} 项`}
          </span>
          <button type="button" onClick={handleCloseIntent} className="secondary-button text-xs py-1 px-3">
            关闭
          </button>
        </div>

        <ConfirmDialog
          isOpen={discardPrompt !== null}
          title="放弃未保存的修改？"
          message={
            discardPrompt === 'close'
              ? '关闭文件浏览会丢弃当前编辑内容，此操作不可撤销。'
              : discardPrompt === 'exit'
                ? '返回目录会丢弃当前编辑内容，此操作不可撤销。'
                : '返回预览会丢弃当前编辑内容，此操作不可撤销。'
          }
          confirmLabel="放弃修改"
          confirmVariant="danger"
          cancelLabel="继续编辑"
          onConfirm={handleDiscardConfirm}
          onCancel={() => setDiscardPrompt(null)}
        />
    </div>
  );
  }
);

