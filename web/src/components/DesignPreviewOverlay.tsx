import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type { RemoteSwarmDesignChoice } from '../types/generated';
import type { RemoteSwarmBee } from '../types/swarm';
import { ArrowPathIcon, XMarkIcon } from './Icons';
import { SheetFrame } from './sheet/SheetFrame';

const DESIGN_VIEWPORT_WIDTH = 1280;
const WIDE_SCREEN_QUERY = '(min-width: 900px)';
const REDUCED_MOTION_QUERY = '(prefers-reduced-motion: reduce)';
const SUMMARY_DIALOG_ID = 'mc-design-choice-summary';
const MAX_DESIGN_CHOICES = 30;

export interface DesignPickChoiceState {
  question: string;
  option: string;
  recommended?: boolean;
  options: string[];
}

export interface DesignPickState {
  version: number;
  changed: number;
  locked?: boolean;
  choices: DesignPickChoiceState[];
}

export interface DesignFinalizeOptions {
  choices?: RemoteSwarmDesignChoice[];
  note?: string;
}

/** 仅显示文件名，去掉目录和 .html 后缀。 */
export function formatDesignFileName(filePath: string): string {
  const normalized = filePath.replace(/\\/g, '/');
  const base = normalized.split('/').pop() || filePath;
  return base.replace(/\.html$/i, '');
}

/** 进度胶囊文案：有改选时显示「已改 X/Y」，全为推荐时显示「Y 项推荐」。 */
export function formatDesignChoiceLabel(state: Pick<DesignPickState, 'changed' | 'choices'>): string {
  const total = state.choices.length;
  return state.changed > 0 ? `已改 ${state.changed}/${total}` : `${total} 项推荐`;
}

/** 将最新点选状态映射为 RPC 所需的 choices 数组（上限 30 项）。 */
export function mapDesignPickChoices(state: DesignPickState | null): RemoteSwarmDesignChoice[] | undefined {
  if (!state || state.choices.length === 0) return undefined;
  return state.choices.slice(0, MAX_DESIGN_CHOICES).map((choice) => ({
    question: choice.question,
    option: choice.option,
    recommended: Boolean(choice.recommended)
  }));
}

function readMediaMatch(queryText: string): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false;
  return Boolean(window.matchMedia(queryText)?.matches);
}

function useMediaMatch(queryText: string): boolean {
  const [matches, setMatches] = useState<boolean>(() => readMediaMatch(queryText));

  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return;
    const query = window.matchMedia(queryText);
    if (!query) return;
    setMatches(Boolean(query.matches));
    if (typeof query.addEventListener !== 'function') return;
    const onChange = (event: MediaQueryListEvent) => setMatches(event.matches);
    query.addEventListener('change', onChange);
    return () => query.removeEventListener?.('change', onChange);
  }, [queryText]);

  return matches;
}

const ChevronUpSmallIcon: React.FC = () => (
  <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="m18 15-6-6-6 6" />
  </svg>
);

const LocatePinIcon: React.FC = () => (
  <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className="shrink-0">
    <circle cx="12" cy="12" r="3" />
    <path d="M12 2v3m0 14v3M2 12h3m14 0h3" />
  </svg>
);

export interface DesignPreviewOverlayProps {
  threadID: string;
  bee: RemoteSwarmBee | null;
  activePath: string;
  onSelectFile: (path: string) => void;
  onClose: () => void;
  openThreadPreview: (threadID: string, path: string) => Promise<string>;
  onSendFeedback: (text: string) => Promise<void>;
  onFinalizeDesign?: (options?: DesignFinalizeOptions) => Promise<void>;
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

  const [pickState, setPickState] = useState<DesignPickState | null>(null);
  const [summaryOpen, setSummaryOpen] = useState(false);

  const isWide = useMediaMatch(WIDE_SCREEN_QUERY);
  const reducedMotion = useMediaMatch(REDUCED_MOTION_QUERY);

  const viewportRef = useRef<HTMLDivElement | null>(null);
  const iframeRef = useRef<HTMLIFrameElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const sheetRef = useRef<HTMLDivElement | null>(null);
  const [viewportSize, setViewportSize] = useState<{ width: number; height: number }>({ width: 0, height: 0 });

  const mountedRef = useRef(true);
  const requestIdRef = useRef(0);
  const sentReduceMotionRef = useRef(false);
  const reducedMotionRef = useRef(reducedMotion);
  const finalizeRequestedRef = useRef(Boolean(bee?.finalizeRequested));
  reducedMotionRef.current = reducedMotion;
  finalizeRequestedRef.current = Boolean(bee?.finalizeRequested);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const postToPicker = useCallback((payload: Record<string, unknown>) => {
    const target = iframeRef.current?.contentWindow;
    if (!target) return;
    target.postMessage(payload, '*');
  }, []);

  // 切换稿件或重载时清空旧点选状态与抽屉展开态
  useEffect(() => {
    setPickState(null);
    setSummaryOpen(false);
    sentReduceMotionRef.current = false;
  }, [currentPath, previewUrl, reloadNonce]);

  // 监听来自预览 iframe 的 mcode-pick 状态消息
  useEffect(() => {
    if (typeof window === 'undefined' || typeof window.addEventListener !== 'function') return;
    const handleMessage = (event: MessageEvent) => {
      const contentWindow = iframeRef.current?.contentWindow;
      if (!contentWindow || event.source !== contentWindow) return;
      const data = event.data as { source?: unknown; type?: unknown; state?: unknown } | null;
      if (!data || typeof data !== 'object' || data.source !== 'mcode-pick' || data.type !== 'state') return;
      const rawState = data.state as Partial<DesignPickState> | null;
      if (!rawState || typeof rawState !== 'object' || !Array.isArray(rawState.choices)) return;

      const nextState: DesignPickState = {
        version: typeof rawState.version === 'number' ? rawState.version : 0,
        changed: typeof rawState.changed === 'number' ? rawState.changed : 0,
        locked: typeof rawState.locked === 'boolean' ? rawState.locked : undefined,
        choices: rawState.choices
      };
      setPickState(nextState);

      if (!sentReduceMotionRef.current) {
        sentReduceMotionRef.current = true;
        postToPicker({ source: 'mcode-pick', type: 'reduceMotion', value: reducedMotionRef.current });
      }
      if (finalizeRequestedRef.current && !nextState.locked) {
        postToPicker({ source: 'mcode-pick', type: 'lock', value: true });
      }
    };
    window.addEventListener('message', handleMessage);
    return () => window.removeEventListener('message', handleMessage);
  }, [postToPicker]);

  // 减少动效设置变更后同步给已连接的 iframe
  useEffect(() => {
    if (!sentReduceMotionRef.current) return;
    postToPicker({ source: 'mcode-pick', type: 'reduceMotion', value: reducedMotion });
  }, [postToPicker, reducedMotion]);

  // 已进入定稿状态时锁死 iframe 内的点选交互
  useEffect(() => {
    if (!bee?.finalizeRequested) return;
    setSummaryOpen(false);
    if (previewUrl) {
      postToPicker({ source: 'mcode-pick', type: 'lock', value: true });
    }
  }, [bee?.finalizeRequested, postToPicker, previewUrl]);

  // 一览展开时支持 Esc 与点击外部关闭
  useEffect(() => {
    if (!summaryOpen || typeof window === 'undefined') return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        setSummaryOpen(false);
      }
    };
    const handleMouseDown = (event: MouseEvent) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (triggerRef.current?.contains(target)) return;
      if (popoverRef.current?.contains(target)) return;
      if (sheetRef.current?.contains(target)) return;
      setSummaryOpen(false);
    };
    window.addEventListener('keydown', handleKeyDown);
    if (typeof document !== 'undefined') {
      document.addEventListener('mousedown', handleMouseDown);
    }
    return () => {
      window.removeEventListener('keydown', handleKeyDown);
      if (typeof document !== 'undefined') {
        document.removeEventListener('mousedown', handleMouseDown);
      }
    };
  }, [summaryOpen]);

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
    const choices = mapDesignPickChoices(pickState);
    const trimmedNote = feedback.trim();
    try {
      await onFinalizeDesign({
        ...(choices ? { choices } : {}),
        ...(trimmedNote ? { note: trimmedNote } : {})
      });
      if (!mountedRef.current) return;
      setFeedback('');
      setSummaryOpen(false);
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
  const isFinalizeRequested = Boolean(bee?.finalizeRequested);
  const isFinalizeRunning = isFinalizeRequested && bee?.status === 'running';
  const isFinalizeDone = isFinalizeRequested && bee?.status !== 'running';
  const showProgressCapsule = Boolean(
    bee?.awaitingFinalize && !isFinalizeRequested && pickState && pickState.choices.length > 0
  );
  const showFinalizeButton = Boolean(bee?.awaitingFinalize && !isFinalizeRequested && onFinalizeDesign);

  const renderSummaryContent = () => {
    if (!pickState) return null;
    const total = pickState.choices.length;
    return (
      <>
        <div
          style={{
            padding: 'var(--mc-space-1) var(--mc-space-2)',
            borderColor: 'var(--mc-hairline)',
            fontSize: 'var(--mc-font-footnote)',
            color: 'var(--mc-text-primary)'
          }}
          className="flex shrink-0 items-center justify-between border-b font-semibold"
        >
          <span>方案选择（已改 {pickState.changed}/{total}）</span>
          <button
            type="button"
            onClick={() => setSummaryOpen(false)}
            aria-label="关闭一览"
            style={{
              width: 'var(--mc-control-xs)',
              height: 'var(--mc-control-xs)',
              borderRadius: 'var(--mc-radius-sm)',
              color: 'var(--mc-text-secondary)'
            }}
            className="inline-flex items-center justify-center hover:bg-[var(--mc-overlay-hover)] hover:text-[var(--mc-text-primary)]"
          >
            <XMarkIcon className="h-3.5 w-3.5" />
          </button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col gap-[var(--mc-space-1)] overflow-y-auto">
          {pickState.choices.map((choice, idx) => (
            <div
              key={`${idx}-${choice.question}`}
              style={{
                padding: 'var(--mc-space-1) var(--mc-space-2)',
                borderRadius: 'var(--mc-radius-sm)',
                gap: 'var(--mc-space-2)'
              }}
              className="flex items-center justify-between hover:bg-[var(--mc-overlay-hover)]"
            >
              <button
                type="button"
                onClick={() => {
                  postToPicker({ source: 'mcode-pick', type: 'scrollTo', question: choice.question });
                  setSummaryOpen(false);
                }}
                aria-label={`定位到第 ${idx + 1} 题：${choice.question}`}
                style={{
                  fontSize: 'var(--mc-font-footnote)',
                  color: 'var(--mc-text-primary)',
                  gap: 'var(--mc-space-1)'
                }}
                className="flex min-w-0 flex-1 items-center text-left hover:text-[var(--mc-accent)]"
              >
                <LocatePinIcon />
                <span className="truncate">{idx + 1}. {choice.question}</span>
              </button>

              <div
                role="group"
                aria-label={`${choice.question} 选项`}
                style={{
                  padding: '2px',
                  borderRadius: 'var(--mc-radius-sm)',
                  backgroundColor: 'var(--mc-surface-canvas)',
                  borderColor: 'var(--mc-hairline)',
                  gap: '2px'
                }}
                className="inline-flex shrink-0 items-center border"
              >
                {choice.options.map((opt) => {
                  const selected = choice.option === opt;
                  return (
                    <button
                      key={opt}
                      type="button"
                      aria-pressed={selected}
                      aria-label={`${choice.question} 选择 ${opt}`}
                      onClick={() => {
                        postToPicker({
                          source: 'mcode-pick',
                          type: 'select',
                          question: choice.question,
                          option: opt
                        });
                      }}
                      style={{
                        minWidth: '22px',
                        height: '20px',
                        padding: '0 5px',
                        borderRadius: 'var(--mc-radius-xs)',
                        fontSize: 'var(--mc-font-caption)',
                        backgroundColor: selected ? 'var(--mc-accent)' : 'transparent',
                        color: selected ? 'var(--mc-on-accent)' : 'var(--mc-text-secondary)',
                        transitionDuration: reducedMotion ? '0ms' : 'var(--mc-dur-fast)'
                      }}
                      className={`font-semibold transition-colors ${
                        selected
                          ? ''
                          : 'hover:bg-[var(--mc-overlay-hover)] hover:text-[var(--mc-text-primary)]'
                      }`}
                    >
                      {opt}
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
        </div>
      </>
    );
  };

  const dialog = (
    <SheetFrame
      onRequestClose={onClose}
      closeOnEscape={!summaryOpen}
      size="bare"
      ariaLabel={`设计稿预览 ${activeName}`}
      layerClassName="fixed inset-0"
      layerStyle={{ zIndex: 100, padding: 0, alignItems: 'stretch', justifyContent: 'stretch' }}
      cardClassName="relative flex h-full w-full flex-col outline-none bg-[var(--mc-surface-main)] text-[var(--mc-text-primary)] animate-slideUp motion-reduce:animate-none"
      cardStyle={reducedMotion ? { animation: 'none' } : undefined}
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

      {/* 中间预览区：1280px 桌面布局在窄容器中等比缩放；窄屏底部抽屉锚定在预览区底部 */}
      <div
        ref={viewportRef}
        className="relative flex-1 min-h-0 overflow-hidden bg-[var(--mc-surface-canvas)]"
      >
        {!loadError ? (
          <>
            <iframe
              ref={iframeRef}
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
                <span className="h-6 w-6 rounded-full border-2 border-[var(--mc-accent)]/30 border-t-[var(--mc-accent)] animate-spin motion-reduce:animate-none" />
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

        {/* 宽屏浮层打开时，覆盖预览区的透明点击层用于捕获 iframe 区域点击并关闭浮层 */}
        {showProgressCapsule && isWide && summaryOpen && (
          <div
            aria-hidden="true"
            onClick={() => setSummaryOpen(false)}
            className="absolute inset-0 z-[18] bg-transparent"
          />
        )}

        {/* 窄屏方案一览：底部抽屉 + 遮罩 */}
        {showProgressCapsule && pickState && !isWide && (
          <>
            <div
              aria-hidden="true"
              onClick={() => setSummaryOpen(false)}
              style={{
                backgroundColor: 'rgba(0, 0, 0, var(--mc-scrim))',
                opacity: summaryOpen ? 1 : 0,
                visibility: summaryOpen ? 'visible' : 'hidden',
                pointerEvents: summaryOpen ? 'auto' : 'none',
                transitionProperty: 'opacity, visibility',
                transitionDuration: reducedMotion ? '0ms' : 'var(--mc-dur-base), 0s',
                transitionTimingFunction: 'cubic-bezier(0.22, 1, 0.36, 1)',
                transitionDelay: reducedMotion ? '0ms' : summaryOpen ? '0s, 0s' : '0s, var(--mc-dur-base)'
              }}
              className="absolute inset-0 z-[18]"
            />
            <div
              ref={sheetRef}
              id={SUMMARY_DIALOG_ID}
              role="dialog"
              aria-label="方案选择"
              aria-hidden={!summaryOpen}
              style={{
                backgroundColor: 'var(--mc-surface-popover)',
                borderColor: 'var(--mc-border)',
                borderTopLeftRadius: 'var(--mc-radius-xl)',
                borderTopRightRadius: 'var(--mc-radius-xl)',
                boxShadow: 'var(--mc-elev-dialog)',
                padding: 'var(--mc-space-3)',
                transform: summaryOpen ? 'translateY(0)' : 'translateY(104%)',
                visibility: summaryOpen ? 'visible' : 'hidden',
                pointerEvents: summaryOpen ? 'auto' : 'none',
                transitionProperty: 'transform, visibility',
                transitionDuration: reducedMotion ? '0ms' : 'var(--mc-dur-sheet), 0s',
                transitionTimingFunction: 'cubic-bezier(0.22, 1, 0.36, 1)',
                transitionDelay: reducedMotion ? '0ms' : summaryOpen ? '0s, 0s' : '0s, var(--mc-dur-sheet)'
              }}
              className="absolute inset-x-0 bottom-0 z-[19] flex max-h-[70%] flex-col gap-[var(--mc-space-2)] border-t"
            >
              <div
                aria-hidden="true"
                style={{
                  width: '36px',
                  height: '4px',
                  borderRadius: 'var(--mc-radius-pill)',
                  backgroundColor: 'var(--mc-border-strong)'
                }}
                className="mx-auto mb-[var(--mc-space-1)] shrink-0"
              />
              {renderSummaryContent()}
            </div>
          </>
        )}
      </div>

      {/* 底部操作栏：意见输入、发送、进度胶囊与定稿 */}
      <footer
        className="relative z-[22] shrink-0 border-t border-[var(--mc-border)] bg-[var(--mc-surface-card)] px-3 pt-2.5"
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

          {showProgressCapsule && pickState && (
            <div className="relative shrink-0">
              {isWide && (
                <div
                  ref={popoverRef}
                  id={SUMMARY_DIALOG_ID}
                  role="dialog"
                  aria-label="方案选择"
                  aria-hidden={!summaryOpen}
                  style={{
                    bottom: 'calc(100% + var(--mc-space-2))',
                    right: 0,
                    width: 'min(390px, calc(100vw - 24px))',
                    backgroundColor: 'var(--mc-surface-popover)',
                    borderColor: 'var(--mc-border)',
                    borderRadius: 'var(--mc-radius-lg)',
                    boxShadow: 'var(--mc-elev-popover)',
                    padding: 'var(--mc-space-2)',
                    opacity: summaryOpen ? 1 : 0,
                    transform: summaryOpen ? 'translateY(0) scale(1)' : 'translateY(6px) scale(0.98)',
                    visibility: summaryOpen ? 'visible' : 'hidden',
                    pointerEvents: summaryOpen ? 'auto' : 'none',
                    transitionProperty: 'opacity, transform, visibility',
                    transitionDuration: reducedMotion ? '0ms' : 'var(--mc-dur-base), var(--mc-dur-base), 0s',
                    transitionTimingFunction: 'cubic-bezier(0.2, 0.9, 0.3, 1)',
                    transitionDelay: reducedMotion ? '0ms' : summaryOpen ? '0s, 0s, 0s' : '0s, 0s, var(--mc-dur-base)'
                  }}
                  className="absolute z-20 flex max-h-[60dvh] flex-col gap-[var(--mc-space-1)] border"
                >
                  {renderSummaryContent()}
                </div>
              )}

              <button
                ref={triggerRef}
                type="button"
                onClick={() => setSummaryOpen((prev) => !prev)}
                aria-expanded={summaryOpen}
                aria-haspopup="dialog"
                aria-controls={SUMMARY_DIALOG_ID}
                aria-label={`方案选择：${formatDesignChoiceLabel(pickState)}`}
                style={{
                  minHeight: '40px',
                  padding: '0 var(--mc-space-3)',
                  borderRadius: 'var(--mc-radius-md)',
                  fontSize: 'var(--mc-font-footnote)',
                  borderColor: summaryOpen || pickState.changed > 0 ? 'var(--mc-accent)' : 'var(--mc-border)',
                  backgroundColor: pickState.changed > 0 ? 'var(--mc-accent-subtle)' : 'var(--mc-surface-main)',
                  color: pickState.changed > 0 ? 'var(--mc-accent)' : 'var(--mc-text-secondary)',
                  boxShadow: summaryOpen ? '0 0 0 1px var(--mc-accent)' : undefined,
                  transitionDuration: reducedMotion ? '0ms' : 'var(--mc-dur-fast)'
                }}
                className="inline-flex shrink-0 items-center gap-[var(--mc-space-1)] border font-semibold tabular-nums whitespace-nowrap transition-colors hover:bg-[var(--mc-overlay-hover)]"
              >
                <span>{formatDesignChoiceLabel(pickState)}</span>
                <ChevronUpSmallIcon />
              </button>
            </div>
          )}

          {showFinalizeButton && (
            <button
              type="button"
              onClick={() => void handleFinalize()}
              disabled={isBusy}
              className="inline-flex min-h-[40px] shrink-0 items-center justify-center rounded-[var(--mc-radius-md)] border border-[var(--mc-warning)] px-3.5 text-mc-body font-semibold text-[var(--mc-warning)] hover:bg-[var(--mc-overlay-hover)] disabled:opacity-40"
            >
              {pendingAction === 'finalize' ? '定稿中…' : '定稿'}
            </button>
          )}

          {isFinalizeRunning && (
            <span
              role="status"
              aria-live="polite"
              aria-busy="true"
              style={{
                borderColor: 'var(--mc-border)',
                backgroundColor: 'var(--mc-surface-main)',
                color: 'var(--mc-text-secondary)',
                borderRadius: 'var(--mc-radius-md)',
                fontSize: 'var(--mc-font-body)'
              }}
              className="inline-flex min-h-[40px] shrink-0 items-center gap-[var(--mc-space-2)] border px-3.5 font-semibold whitespace-nowrap"
            >
              <span
                aria-hidden="true"
                style={reducedMotion ? { animation: 'none' } : undefined}
                className="h-3.5 w-3.5 rounded-full border-2 border-[var(--mc-text-tertiary)] border-t-[var(--mc-accent)] animate-spin motion-reduce:animate-none"
              />
              <span>定稿中…</span>
            </span>
          )}

          {isFinalizeDone && (
            <span
              role="status"
              aria-label="已定稿"
              style={{
                borderColor: 'var(--mc-success)',
                backgroundColor: 'var(--mc-surface-main)',
                color: 'var(--mc-success)',
                borderRadius: 'var(--mc-radius-md)',
                fontSize: 'var(--mc-font-body)',
                animation: reducedMotion ? 'none' : undefined
              }}
              className="inline-flex min-h-[40px] shrink-0 select-none items-center rounded-[var(--mc-radius-md)] border px-3.5 font-semibold whitespace-nowrap animate-fadeIn motion-reduce:animate-none"
            >
              ✓ 已定稿
            </span>
          )}
        </div>
      </footer>
    </SheetFrame>
  );

  return typeof document === 'undefined' ? dialog : createPortal(dialog, document.body);
};
