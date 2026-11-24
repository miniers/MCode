import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { RemoteSwarmBee } from '../types/swarm';
import { ArrowPathIcon, XMarkIcon } from './Icons';
import { SheetFrame } from './sheet/SheetFrame';

const DESIGN_VIEWPORT_WIDTH = 1280;

/** 仅显示文件名，去掉目录和 .html 后缀。 */
export function formatDesignFileName(filePath: string): string {
  const normalized = filePath.replace(/\\/g, '/');
  const base = normalized.split('/').pop() || filePath;
  return base.replace(/\.html$/i, '');
}

export interface DesignPreviewOverlayProps {
  threadID: string;
  bee: RemoteSwarmBee | null;
  activePath: string;
  onSelectFile: (path: string) => void;
  onClose: () => void;
  openThreadPreview: (threadID: string, path: string) => Promise<string>;
  onSendFeedback: (text: string) => Promise<void>;
  onFinalizeDesign?: () => Promise<void>;
  canSend?: boolean;
  isConnected?: boolean;
}

export const DesignPreviewOverlay: React.FC<DesignPreviewOverlayProps> = ({
  threadID,
  bee,
  activePath,
  onSelectFile,
  onClose,
  openThreadPreview,
  onSendFeedback,
  onFinalizeDesign,
  canSend = true,
  isConnected = true
}) => {
  const files = useMemo(() => {
    const list = bee?.designFiles ? [...bee.designFiles] : [];
    if (activePath && !list.includes(activePath)) {
      list.unshift(activePath);
    }
    return list;
  }, [activePath, bee?.designFiles]);

  const currentPath = activePath || files[0] || '';
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [reloadNonce, setReloadNonce] = useState(0);

  const [feedback, setFeedback] = useState('');
  const [pendingAction, setPendingAction] = useState<'send' | 'finalize' | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);

  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [viewportSize, setViewportSize] = useState<{ width: number; height: number }>({ width: 0, height: 0 });

  const mountedRef = useRef(true);
  const requestIdRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // 每次打开或切换稿件都现场请求临时预览地址，不缓存到 URL 或本地存储。
  useEffect(() => {
    if (!currentPath) {
      setPreviewUrl(null);
      setLoading(false);
      setLoadError('未指定设计稿路径');
      return;
    }
    if (!isConnected) {
      setPreviewUrl(null);
      setLoading(true);
      setLoadError(null);
      return;
    }

    const requestId = ++requestIdRef.current;
    let cancelled = false;
    setPreviewUrl(null);
    setLoading(true);
    setLoadError(null);

    openThreadPreview(threadID, currentPath)
      .then((url) => {
        if (cancelled || !mountedRef.current || requestId !== requestIdRef.current) return;
        setPreviewUrl(url);
        setLoading(false);
        setLoadError(null);
      })
      .catch((err) => {
        if (cancelled || !mountedRef.current || requestId !== requestIdRef.current) return;
        setPreviewUrl(null);
        setLoading(false);
        setLoadError(err instanceof Error ? err.message : '加载设计稿失败');
      });

    return () => {
      cancelled = true;
    };
  }, [currentPath, isConnected, openThreadPreview, reloadNonce, threadID]);

  // 测量容器宽高，用于将 1280px 桌面稿缩放到窄屏容器。
  useEffect(() => {
    const node = viewportRef.current;
    if (!node) return;
    const updateSize = () => {
      const rect = node.getBoundingClientRect();
      setViewportSize((prev) =>
        prev.width === rect.width && prev.height === rect.height
          ? prev
          : { width: rect.width, height: rect.height }
      );
    };
    updateSize();
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', updateSize);
      return () => window.removeEventListener('resize', updateSize);
    }
    const observer = new ResizeObserver(updateSize);
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  const handleRetry = useCallback(() => {
    setReloadNonce((value) => value + 1);
  }, []);

  const handleSend = async () => {
    const trimmed = feedback.trim();
    if (!trimmed || pendingAction || !canSend) return;
    setPendingAction('send');
    setActionError(null);
    try {
      await onSendFeedback(trimmed);
      if (!mountedRef.current) return;
      setFeedback('');
      setPendingAction(null);
      onClose();
    } catch (err) {
      if (!mountedRef.current) return;
      setPendingAction(null);
      setActionError(err instanceof Error ? err.message : '发送失败');
    }
  };

  const handleFinalize = async () => {
    if (!onFinalizeDesign || pendingAction) return;
    setPendingAction('finalize');
    setActionError(null);
    try {
      await onFinalizeDesign();
      if (!mountedRef.current) return;
      setPendingAction(null);
      onClose();
    } catch (err) {
      if (!mountedRef.current) return;
      setPendingAction(null);
      setActionError(err instanceof Error ? err.message : '定稿失败');
    }
  };

  const shouldScale = viewportSize.width > 0 && viewportSize.width < DESIGN_VIEWPORT_WIDTH;
  const scale = shouldScale ? viewportSize.width / DESIGN_VIEWPORT_WIDTH : 1;
  const iframeStyle: React.CSSProperties = shouldScale
    ? {
        width: `${DESIGN_VIEWPORT_WIDTH}px`,
        height: viewportSize.height > 0 ? `${Math.ceil(viewportSize.height / scale)}px` : '100%',
        transform: `scale(${scale})`,
        transformOrigin: 'top left',
        border: 0
      }
    : {
        width: '100%',
        height: '100%',
        border: 0
      };

  const activeName = formatDesignFileName(currentPath);
  const isBusy = pendingAction !== null;
  const showFinalize = Boolean(bee?.awaitingFinalize && onFinalizeDesign);

  const dialog = (
    <SheetFrame
      onRequestClose={onClose}
      size="bare"
      ariaLabel={`设计稿预览 ${activeName}`}
      layerClassName="fixed inset-0"
      layerStyle={{ zIndex: 100, padding: 0, alignItems: 'stretch', justifyContent: 'stretch' }}
      cardClassName="relative flex h-full w-full flex-col outline-none bg-[var(--mc-surface-main)] text-[var(--mc-text-primary)] animate-slideUp"
    >
      {/* 顶部栏：稿件名与关闭按钮；多份稿件时展示切换标签 */}
      <header
        className="shrink-0 border-b border-[var(--mc-border)] bg-[var(--mc-surface-card)]"
        style={{
          paddingTop: 'env(safe-area-inset-top, 0px)',
          paddingLeft: 'env(safe-area-inset-left, 0px)',
          paddingRight: 'env(safe-area-inset-right, 0px)'
        }}
      >
        <div className="flex min-h-[48px] items-center justify-between gap-2 px-3 py-1.5">
          <h2 className="min-w-0 truncate text-mc-title font-semibold text-[var(--mc-text-primary)]">
            {activeName}
          </h2>
          <button
            type="button"
            onClick={onClose}
            aria-label="关闭"
            className="inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-full text-[var(--mc-text-secondary)] hover:bg-[var(--mc-overlay-hover)]"
          >
            <XMarkIcon className="h-4 w-4" />
          </button>
        </div>

        {files.length > 1 && (
          <div
            role="tablist"
            aria-label="设计稿列表"
            className="no-scrollbar flex items-center gap-1.5 overflow-x-auto px-3 pb-2"
          >
            {files.map((file) => {
              const selected = file === currentPath;
              return (
                <button
                  key={file}
                  type="button"
                  role="tab"
                  aria-selected={selected}
                  onClick={() => {
                    if (!selected) onSelectFile(file);
                  }}
                  className={`shrink-0 rounded-[var(--mc-radius-pill)] px-3 py-1 text-mc-footnote font-medium transition ${
                    selected
                      ? 'bg-[var(--mc-accent-subtle)] text-[var(--mc-accent)] font-semibold'
                      : 'text-[var(--mc-text-secondary)] hover:bg-[var(--mc-overlay-hover)]'
                  }`}
                >
                  {formatDesignFileName(file)}
                </button>
              );
            })}
          </div>
        )}
      </header>

      {/* 中间预览区：1280px 桌面布局在窄容器中等比缩放 */}
      <div
        ref={viewportRef}
        className="relative flex-1 min-h-0 overflow-hidden bg-[var(--mc-surface-canvas)]"
      >
        {!loadError ? (
          <>
            <iframe
              sandbox="allow-scripts"
              src={previewUrl ?? undefined}
              title={activeName}
              style={iframeStyle}
              className="block bg-white"
            />
            {loading && (
              <div
                role="status"
                className="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-[var(--mc-surface-canvas)]"
              >
                <span className="h-6 w-6 rounded-full border-2 border-[var(--mc-accent)]/30 border-t-[var(--mc-accent)] animate-spin" />
                <p className="text-mc-footnote text-[var(--mc-text-secondary)]">正在加载设计稿…</p>
              </div>
            )}
          </>
        ) : (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
            <p role="alert" className="text-mc-footnote text-[var(--mc-danger)] break-words">
              {loadError}
            </p>
            <button type="button" onClick={handleRetry} className="secondary-button">
              <ArrowPathIcon className="h-3.5 w-3.5" />
              重试
            </button>
          </div>
        )}
      </div>

      {/* 底部操作栏：意见输入、发送与定稿 */}
      <footer
        className="shrink-0 border-t border-[var(--mc-border)] bg-[var(--mc-surface-card)] px-3 pt-2.5"
        style={{
          paddingBottom: 'max(10px, env(safe-area-inset-bottom, 0px))',
          paddingLeft: 'max(12px, env(safe-area-inset-left, 0px))',
          paddingRight: 'max(12px, env(safe-area-inset-right, 0px))'
        }}
      >
        {actionError && (
          <p role="alert" className="mb-2 text-mc-footnote text-[var(--mc-danger)] break-words">
            {actionError}
          </p>
        )}
        <div className="flex items-end gap-2">
          <textarea
            value={feedback}
            onChange={(event) => setFeedback(event.target.value)}
            rows={1}
            placeholder="输入意见"
            aria-label="意见"
            disabled={isBusy}
            className="min-h-[40px] max-h-24 flex-1 min-w-0 resize-none rounded-[var(--mc-radius-md)] border border-[var(--mc-border)] bg-[var(--mc-surface-main)] px-3 py-2 text-mc-body text-[var(--mc-text-primary)] placeholder:text-[var(--mc-text-tertiary)] focus:border-[var(--mc-accent)] focus:outline-none disabled:opacity-45"
          />
          <button
            type="button"
            onClick={() => void handleSend()}
            disabled={isBusy || !canSend || !feedback.trim()}
            className="primary-button shrink-0 disabled:opacity-40"
          >
            {pendingAction === 'send' ? '发送中…' : '发送'}
          </button>
          {showFinalize && (
            <button
              type="button"
              onClick={() => void handleFinalize()}
              disabled={isBusy}
              className="inline-flex min-h-[40px] shrink-0 items-center justify-center rounded-[var(--mc-radius-md)] border border-[var(--mc-warning)] px-3.5 text-mc-body font-semibold text-[var(--mc-warning)] hover:bg-[var(--mc-overlay-hover)] disabled:opacity-40"
            >
              {pendingAction === 'finalize' ? '定稿中…' : '定稿'}
            </button>
          )}
        </div>
      </footer>
    </SheetFrame>
  );

  return typeof document === 'undefined' ? dialog : createPortal(dialog, document.body);
};
