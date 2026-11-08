import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import { createPortal } from 'react-dom';
import type {
  RemoteAttachmentUploadResult,
  RemoteComposerOptions,
  RemoteFileEntry,
  RemoteSwarmPair,
  RemoteProjectSummary,
  RemoteProvider
} from '../types/contract';
import { useRemoteStatic, useRemote } from '../context/RemoteContext';
import { isImageFile } from '../context/remoteImages';
import { makeActionID } from '../context/remoteGuards';
import type { RemoteProviderListWithCatalog } from '../context/remoteTypes';
import type { ComposerQuotaScope } from '../types/quota';
import type { RemotePersona, RemoteSwarmTier } from '../types/generated';
import { PERSONA_NONE_VALUE, useThreadPersonas } from '../context/remotePersonaApi';
import { CommandSuggestions } from './CommandSuggestions';
import { ComposerQuotaBar } from './ComposerQuotaBar';
import { QuickReplyBar } from './QuickReplyBar';
import { ThreadMentionChips } from './composer/ThreadMentionChips';
import { useThreadMentionCandidates, useThreadMentionSendText } from '../hooks/useThreadMentionCandidates';
import type { DraftThreadMention } from '../utils/threadMention';
import {
  commandScopeKey,
  commandToSuggestion,
  extractTriggerQuery,
  fileEntryToSuggestion,
  filterSkillCommands,
  filterSlashCommands,
  localCommandName,
  modelSuggestions,
  skillCommandToSuggestion
} from '../utils/slashCommands';
import type { CommandScope, SuggestionItem, TriggerQueryInfo } from '../utils/slashCommands';
import { modelDisplayName } from '../utils/modelLabel';
import { isImeComposing } from '../utils/ime';
import {
  caretOnFirstLine,
  caretOnLastLine,
  composerHistoryNext,
  composerHistoryPrev,
  composerHistoryReset,
  composerHistoryRestore,
  emptyComposerHistoryState
} from '../utils/composerHistory';
import type { ComposerHistoryState } from '../utils/composerHistory';
import { overrideCount, parseBeeTiers, serializeBeeTiers } from '../utils/beeTiers';
import type { TierOverrideDraft } from '../utils/beeTiers';
import {
  expandPastedText,
  insertPlaceholderAt,
  isLongPastedText,
  nextPastedPlaceholder
} from '../utils/pastedText';
import {
  buildPickerSections,
  isMissingSelection,
  pairUnavailableReason
} from '../utils/modelIndex';
import { isAutoPairName, modelShortDisplayName } from '../utils/modelShortName';
import { EffortTrack, ModelPickerPanel, ModelSummaryRow } from './ModelPicker';
import { useRemoteMethodSupport } from './thread/useThreadMetaActions';
import {
  ArrowUpIcon,
  CheckCircleIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  PlusIcon,
  XMarkIcon
} from './Icons';
import '../styles/send-morph.css';

// 按下时保持输入框焦点，避免键盘收起引起布局移位导致点击落空。
function keepComposerFocus(event: React.MouseEvent): void {
  event.preventDefault();
}

/** 模型浮层的两级页面：一级是摘要，二级是主代理 / 子代理的模型列表。 */
export type ModelSheetPage = 'summary' | 'lead' | 'bees';

/** Esc：二级页先退回摘要页（返回 'summary'），已在摘要页才关整个浮层（返回 null）。 */
export function modelSheetPageOnEscape(page: ModelSheetPage): ModelSheetPage | null {
  return page === 'summary' ? null : 'summary';
}

/** 拖拽意图：命中 Files 就先拦浏览器默认行为，附件可用才接收文件。 */
export function fileDragIntent(types: Iterable<string>, attachmentsAvailable: boolean): {
  preventDefault: boolean;
  acceptsFiles: boolean;
} {
  const hasFiles = Array.from(types).includes('Files');
  return { preventDefault: hasFiles, acceptsFiles: hasFiles && attachmentsAvailable };
}

/**
 * 主操作按钮的图标形变（W-16）：发送箭头与停止方块同时渲染、叠放在同一格子里，
 * 由 data-state 决定谁可见，切换时用透明度 + 45° 旋转交叉过渡。
 * 停止态不转圈：旋转只用于进场/退场的 45°，不是持续旋转。
 */
function PrimaryActionIcon({ state }: { state: 'send' | 'stop' }) {
  return (
    <span className="send-morph" data-state={state} aria-hidden="true">
      <ArrowUpIcon className="send-morph__icon send-morph__icon--send w-4 h-4" />
      <span className="send-morph__icon send-morph__icon--stop w-2.5 h-2.5 bg-white rounded-xs" />
    </span>
  );
}

/** 引用到回复：按桌面 quoteForReply 的格式（去尾部换行、每行加 `> `）生成引用块，空白返回空串 */
function quoteForDraft(text: string): string {
  const trimmed = text.replace(/\n+$/, '');
  if (!trimmed.trim()) return '';
  return trimmed
    .split('\n')
    .map((line) => `> ${line}`)
    .join('\n');
}

/** 输入区对外的命令式入口：时间线「引用到回复」用它把引用块写进草稿 */
export interface ThreadComposerHandle {
  /** 把引用块追加到草稿末尾并聚焦输入框（对应 internal/ui/timeline_menu.go:35 的 quoteIntoDraft） */
  insertDraft: (text: string) => void;
}

export interface ThreadComposerProps {
  draft: string;
  isSending: boolean;
  sendError: string | null;
  sendBlockedReason: string | null;
  isPolling: boolean;
  sendDisabled: boolean;
  attachments: RemoteAttachmentUploadResult[];
  /** 草稿阶段图片附件的本地 object URL（按 attachmentID）：有则显示缩略图，缺失时只显示文件名 */
  attachmentPreviews?: Record<string, string>;
  isUploadingAttachment: boolean;
  submitLabel: string;
  composerOptions?: RemoteComposerOptions;
  providers?: RemoteProviderListWithCatalog;
  composerOptionsDisabled?: boolean;
  composerOptionsMessage?: string | null;
  attachmentsSupported?: boolean;
  onComposerOptionsChange?: (options: RemoteComposerOptions) => void;
  onInterruptAndSend?: () => void;
  interruptAndSendDisabled?: boolean;
  isInterrupting?: boolean;
  onDraftChange: (value: string) => void;
  onAddAttachments: (files: FileList | null) => void;
  onRemoveAttachment: (attachmentID: string) => void;
  /**
   * 发送回调。带 `@会话引用` 时传入展开后的最终文本（overrideText），此时草稿保持用户可见的原文；
   * 返回 `false` 表示宿主没有受理（发送失败/被拦截），输入框里的引用胶囊会保留。
   */
  onSend: (overrideText?: string) => void | Promise<boolean | void>;
  /** 输入框占位文字：会话空闲但仍在等后台任务时由调用方换成等待提示 */
  placeholder?: string;
  isRunning?: boolean;
  onStop?: () => void;
  /** 子代理页：停止按钮走子代理停止确认流程时的可用条件 */
  stopDisabled?: boolean;
  /** 子代理页：模型已固定，传入时模型胶囊变为不可点击的静态标签 */
  lockedModelLabel?: string;
  projects?: RemoteProjectSummary[];
  selectedProjectID?: string;
  onSelectProject?: (projectID: string) => void;
  threadID?: string;
  /**
   * `/` 命令清单的取数范围：已有会话传 `{ threadID }`，草稿页传 `{ projectID, provider }`。
   * 不传则没有 `/` 补全；`#模型` 委派只在已有会话范围里提供。
   */
  commandScope?: CommandScope;
  /** 会话级编排开关：已有会话立即生效，草稿只写入 options。 */
  swarmEnabled?: boolean;
  swarmToggleDisabled?: boolean;
  onToggleSwarm?: (on: boolean) => void;
  /** 已开始的会话没有编排组合入口时置 false（子代理会话等）。 */
  swarmPairsAllowed?: boolean;
  /** 输入框下方的账户额度条范围；不传则不显示。 */
  quotaScope?: ComposerQuotaScope;
  /** 会话摘要里的子代理档位覆盖（JSON 字符串）：整表覆盖保存，所以需要显式传入基线 */
  swarmBeeTiers?: string;
  /**
   * 会话里是否已有消息：快捷回复只在有消息的会话里显示（桌面端 quickRepliesVisible 同义）。
   * 不传按「已有消息」处理；ThreadView 还没接线，接线时传「消息列表非空」。
   */
  hasMessages?: boolean;
  /**
   * 传入时在输入框右侧渲染「分割」按钮；只有总管会话、已有消息、宿主支持该方法时由 ThreadView 传入。
   * 对应桌面 internal/ui/steward_split.go 的 splitSessionView.Show。
   */
  onSplitSession?: () => void;
  /** 这一轮还没结束、正在请求或已断线：按钮保留但禁用。 */
  splitSessionDisabled?: boolean;
  /**
   * 本会话按时间顺序排列的用户消息文本（来自会话消息，过滤交给调用方或 composerHistoryEntries）。
   * 不传或为空时不启用方向键历史导航；ThreadView 还没接线，接线时传当前会话历史。
   */
  historyEntries?: string[];
}

type ActiveSheetType = 'model-effort' | 'mode' | 'permission' | 'project' | null;

const TIER_INPUT_CLASS =
  'w-full min-h-[32px] px-2 rounded-lg border border-[var(--input-border)] bg-[var(--screen)] text-mc-caption font-mono text-[var(--foreground)] outline-none focus:border-[var(--primary)] disabled:opacity-50';

// 档位下拉沿用同一套输入框外观，直接复用原生下拉箭头
const TIER_SELECT_CLASS = `${TIER_INPUT_CLASS} cursor-pointer`;

// 人设下拉同样用原生下拉箭头，但收在分区行右侧，所以限宽、不再撑满整行
const PERSONA_SELECT_CLASS =
  'min-h-[32px] max-w-[160px] shrink-0 rounded-lg border border-[var(--input-border)] bg-[var(--screen)] px-2 text-mc-caption text-[var(--foreground)] outline-none focus:border-[var(--primary)] disabled:opacity-50 cursor-pointer';

function permissionLabel(mode?: RemoteComposerOptions['runtimeMode']): string {
  if (mode === 'supervised') return '逐项确认';
  if ((mode as string | undefined) === 'autoAcceptEdits' || mode === 'auto') return '自动';
  if (mode === 'fullAccess') return '完全访问';
  return '权限: 默认';
}

function modeLabel(mode?: RemoteComposerOptions['interactionMode']): string {
  if (mode === 'plan') return '计划模式';
  if (mode === 'build') return '执行模式';
  return '默认模式';
}

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
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      onClose();
    };
    window.addEventListener('keydown', handleKeyDown, true);
    return () => window.removeEventListener('keydown', handleKeyDown, true);
  }, [onClose]);

  const dialog = (
    <div
      role="dialog"
      aria-modal="true"
      aria-label={`图片预览 ${name}`}
      className="fixed inset-0 z-[100] flex items-center justify-center bg-black"
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

/** 上下文 token 数：小数值按 k/M 缩写，避免徽标被撑宽 */
function formatTokenCount(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return '0';
  if (value < 1000) return String(Math.round(value));
  if (value < 10000) return `${(value / 1000).toFixed(1)}k`;
  if (value < 1000000) return `${Math.round(value / 1000)}k`;
  return `${(value / 1000000).toFixed(1)}M`;
}

/** 与桌面端一致：小额保留三位，避免高价模型也被四舍五入成 $0.00 */
function formatCostUSD(value: number): string {
  return value >= 1 ? value.toFixed(2) : value.toFixed(3);
}

const CONTEXT_RING_RADIUS = 6;
const CONTEXT_RING_LENGTH = 2 * Math.PI * CONTEXT_RING_RADIUS;

const CONTEXT_POPOVER_MAX_WIDTH = 260;
const CONTEXT_POPOVER_MARGIN = 8;

/**
 * 上下文占用与费用入口：只订阅会话摘要（useRemote），摘要不变时不会拖着整个输入区重渲染。
 * 宿主既没上报窗口（contextWindow 为 0）也没上报费用时整块不渲染，旧宿主完全看不到变化。
 * 详情浮层用 portal 挂到 body：胶囊行是横向滚动容器，挂在行内的浮层会被裁掉、也会被输入框压住。
 */
const ContextUsageBadge: React.FC<{ threadID?: string }> = ({ threadID }) => {
  const { threads, currentThread } = useRemote();
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<{ left: number; bottom: number; width: number } | null>(null);
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const summary = useMemo(() => {
    if (!threadID) return null;
    if (currentThread && currentThread.id === threadID) return currentThread;
    return threads.find((item) => item.id === threadID) ?? null;
  }, [threadID, currentThread, threads]);

  // 左对齐到按钮；右侧越界时左移，窄屏始终留出 8px 边距
  const updateAnchor = useCallback(() => {
    const button = buttonRef.current;
    if (!button) return;
    const rect = button.getBoundingClientRect();
    const viewportWidth = document.documentElement.clientWidth || window.innerWidth;
    const width = Math.min(CONTEXT_POPOVER_MAX_WIDTH, viewportWidth - CONTEXT_POPOVER_MARGIN * 2);
    const left = Math.max(
      CONTEXT_POPOVER_MARGIN,
      Math.min(rect.left, viewportWidth - width - CONTEXT_POPOVER_MARGIN)
    );
    const bottom = Math.max(CONTEXT_POPOVER_MARGIN, window.innerHeight - rect.top + 6);
    setAnchor({ left, bottom, width });
  }, []);

  // 点击外部、按 Esc、滚动或窗口尺寸变化都重新收起/重新贴合按钮
  useEffect(() => {
    if (!open) return;
    updateAnchor();
    const handlePointerDown = (event: Event) => {
      const target = event.target as Node | null;
      if (!target) return;
      if (popoverRef.current?.contains(target) || buttonRef.current?.contains(target)) return;
      setOpen(false);
    };
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      setOpen(false);
    };
    window.addEventListener('pointerdown', handlePointerDown, true);
    window.addEventListener('keydown', handleKeyDown, true);
    window.addEventListener('resize', updateAnchor);
    window.addEventListener('scroll', updateAnchor, true);
    return () => {
      window.removeEventListener('pointerdown', handlePointerDown, true);
      window.removeEventListener('keydown', handleKeyDown, true);
      window.removeEventListener('resize', updateAnchor);
      window.removeEventListener('scroll', updateAnchor, true);
    };
  }, [open, updateAnchor]);

  if (!summary) return null;
  const used = Math.max(0, Math.round(summary.contextUsedTokens ?? 0));
  const limit = Math.max(0, Math.round(summary.contextWindow ?? 0));
  const cost = summary.lastTurnCostUSD ?? 0;
  if (limit <= 0 && cost <= 0) return null;
  const percent = limit > 0 ? Math.min(100, Math.round((used / limit) * 100)) : 0;
  const remaining = Math.max(0, limit - used);
  const costLabel = cost > 0 ? `$${formatCostUSD(cost)}` : '-';
  // 有窗口信息时按钮只留进度圆环（方形图标按钮）；没有窗口信息时退化成费用文字入口，仍然可点开看详情
  const ariaLabel =
    limit > 0
      ? `上下文已用 ${formatTokenCount(used)} / ${formatTokenCount(limit)} tokens，本轮费用 ${costLabel}，点击查看详情`
      : `本轮费用 ${costLabel}，点击查看详情`;

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        onClick={() => {
          updateAnchor();
          setOpen((value) => !value);
        }}
        title={ariaLabel}
        aria-label={ariaLabel}
        aria-haspopup="dialog"
        aria-expanded={open}
        className={`h-[30px] rounded-full text-mc-body font-medium inline-flex items-center gap-1.5 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)] hover:text-[var(--foreground)] active:scale-95 transition ${
          limit > 0 ? 'w-[30px] justify-center' : 'px-2'
        }`}
      >
        {limit > 0 ? (
          <svg viewBox="0 0 16 16" className="h-4 w-4 -rotate-90" aria-hidden="true">
            <circle cx="8" cy="8" r={CONTEXT_RING_RADIUS} fill="none" stroke="var(--border)" strokeWidth="2" />
            <circle
              cx="8"
              cy="8"
              r={CONTEXT_RING_RADIUS}
              fill="none"
              stroke="var(--primary)"
              strokeWidth="2"
              strokeLinecap="round"
              strokeDasharray={`${((CONTEXT_RING_LENGTH * percent) / 100).toFixed(2)} ${CONTEXT_RING_LENGTH.toFixed(2)}`}
            />
          </svg>
        ) : (
          <span>{costLabel}</span>
        )}
      </button>
      {open &&
        anchor &&
        createPortal(
          <div
            ref={popoverRef}
            role="dialog"
            aria-label="上下文与费用"
            style={{ left: anchor.left, bottom: anchor.bottom, width: anchor.width }}
            className="fixed z-[60] rounded-xl border border-[var(--border)] bg-[var(--card)] p-3 text-mc-footnote leading-4 text-[var(--foreground)] shadow-lg"
          >
            {limit > 0 && (
              <div className="space-y-1.5">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="shrink-0 text-[var(--foreground-muted)]">已用 / 上限</span>
                  <span className="font-mono">{`${formatTokenCount(used)} / ${formatTokenCount(limit)} tokens`}</span>
                </div>
                <div className="flex items-baseline justify-between gap-3">
                  <span className="shrink-0 text-[var(--foreground-muted)]">占比</span>
                  <span className="font-mono">{`${percent}%`}</span>
                </div>
                <div className="flex items-baseline justify-between gap-3">
                  <span className="shrink-0 text-[var(--foreground-muted)]">剩余</span>
                  <span className="font-mono">{`${formatTokenCount(remaining)} tokens`}</span>
                </div>
              </div>
            )}
            <div
              className={`flex items-baseline justify-between gap-3 ${
                limit > 0 ? 'mt-2 border-t border-[var(--border)] pt-2' : ''
              }`}
            >
              <span className="shrink-0 text-[var(--foreground-muted)]">本轮费用</span>
              <span className="font-mono">{costLabel}</span>
            </div>
          </div>,
          document.body
        )}
    </>
  );
};

/** 会话当前人设值：空串表示从未设置，none 表示显式无人设，两者都按「默认」处理。 */
const useThreadPersonaValue = (threadID?: string): string => {
  const { threads, currentThread } = useRemote();
  return useMemo(() => {
    if (!threadID) return '';
    const summary =
      currentThread && currentThread.id === threadID
        ? currentThread
        : threads.find((item) => item.id === threadID);
    return summary?.persona ?? '';
  }, [threadID, currentThread, threads]);
};

/**
 * 人设标签：与桌面端同口径，人设已被删或列表还没拉回来时退回显示 id。
 * 单独做成小组件，免得整个输入区为了摘要里的 persona 去订阅 useRemote。
 */
const PersonaBadge: React.FC<{ threadID?: string; personas: RemotePersona[] }> = ({
  threadID,
  personas
}) => {
  const persona = useThreadPersonaValue(threadID);
  const label = useMemo(() => {
    if (!persona || persona === PERSONA_NONE_VALUE) return '';
    return personas.find((item) => item.id === persona)?.name || persona;
  }, [persona, personas]);
  if (!label) return null;
  return (
    <span
      title={`当前人设：${label}`}
      className="h-[30px] max-w-[120px] truncate rounded-full text-mc-footnote font-medium px-2 inline-flex items-center shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)]"
    >
      {label}
    </span>
  );
};

/**
 * 模型弹窗里的人设分区：列表与桌面端 renderHeaderPersonaSection 同源，但收成一行下拉，
 * 写法照同一弹窗里的「子代理档位」行：左侧标题与说明，右侧当前值加下拉箭头（原生 select）。
 * 当前人设已不在列表里时选中「默认」（与桌面端 personaMenuChecked 一致），列表为空时整块不渲染。
 */
const PersonaSection: React.FC<{
  threadID?: string;
  personas: RemotePersona[];
  saving: boolean;
  message: string | null;
  onSelect: (persona: string) => void;
}> = ({ threadID, personas, saving, message, onSelect }) => {
  const persona = useThreadPersonaValue(threadID);
  // 当前人设已不在列表里时选中「默认」，与原平铺列表的勾选口径一致。
  const current = persona === PERSONA_NONE_VALUE ? '' : persona;
  const selected = current !== '' && personas.some((item) => item.id === current) ? current : '';
  if (personas.length === 0) return null;
  return (
    <div className="py-3">
      <div className="flex items-center justify-between gap-2">
        <span className="min-w-0">
          <span className="block text-sm font-semibold text-[var(--foreground)]">人设</span>
          <span className="block text-xs text-[var(--foreground-muted)]">
            决定本会话的回答风格与角色设定
          </span>
        </span>
        <select
          value={selected}
          onChange={(event) => onSelect(event.target.value)}
          disabled={saving}
          aria-label="人设"
          className={PERSONA_SELECT_CLASS}
        >
          <option value="">默认（不使用人设）</option>
          {personas.map((item) => (
            <option key={item.id} value={item.id} title={item.description || undefined}>
              {item.name || item.id}
            </option>
          ))}
        </select>
      </div>
      {message && (
        <p role="alert" className="mt-2 text-mc-footnote leading-4 text-[var(--danger-foreground)]">
          {message}
        </p>
      )}
    </div>
  );
};

// React.memo：props 已在 ThreadView 稳定化，流式增量到来时输入区不再重渲染（draft 变化仍会重渲染，符合预期）。
const ThreadComposerInner = React.forwardRef<ThreadComposerHandle, ThreadComposerProps>(function ThreadComposer({
  draft,
  isSending,
  sendBlockedReason,
  isPolling,
  sendDisabled,
  attachments,
  attachmentPreviews,
  isUploadingAttachment,
  submitLabel,
  composerOptions,
  providers = [] as RemoteProviderListWithCatalog,
  composerOptionsDisabled = false,
  composerOptionsMessage,
  attachmentsSupported = false,
  onComposerOptionsChange,
  onInterruptAndSend,
  interruptAndSendDisabled = false,
  isInterrupting = false,
  onDraftChange,
  onSend,
  onAddAttachments,
  onRemoveAttachment,
  placeholder,
  isRunning = false,
  onStop,
  stopDisabled = false,
  lockedModelLabel,
  projects,
  selectedProjectID,
  onSelectProject,
  threadID,
  commandScope,
  swarmEnabled,
  swarmToggleDisabled = false,
  onToggleSwarm,
  swarmPairsAllowed = true,
  quotaScope,
  swarmBeeTiers,
  hasMessages = true,
  onSplitSession,
  splitSessionDisabled = false,
  historyEntries
}: ThreadComposerProps, ref) {
  const {
    capabilities,
    searchThreadFiles,
    listThreadFiles,
    swarm,
    commandSnapshots,
    loadThreadCommands,
    getSwarmTiers,
    setThreadSwarmBeeTiers: setBeeTiersRpc
  } = useRemoteStatic();
  const canMention = Boolean(
    threadID &&
    capabilities?.features?.includes('files.read') &&
    capabilities?.methods?.includes('files.search')
  );
  // `/` 命令与 `#模型` 委派都由宿主 feature `threads.commands` 管；旧宿主不弹，也不请求。
  const commandsSupported = Boolean(
    capabilities?.features?.includes('threads.commands') &&
    capabilities.methods.includes('threads.commands')
  );
  const canSlash = commandsSupported && Boolean(commandScope);
  // `$` 技能候选同样来自 threads.commands，取数范围与 `/` 相同（草稿页用项目 + Provider）。
  const canSkill = commandsSupported && Boolean(commandScope);
  // 委派只对已有会话有意义；运行中宿主拒绝委派，不弹候选（发送侧另有提示）。
  const canDelegate = commandsSupported && Boolean(commandScope && 'threadID' in commandScope) && !isRunning;
  const scopeKey = commandScope ? commandScopeKey(commandScope) : null;
  const commandSnapshot = scopeKey ? commandSnapshots[scopeKey] : undefined;
  // 草稿阶段 scope 随 provider 变化：key 变了就重新拉。
  const scopeRef = useRef(commandScope);
  scopeRef.current = commandScope;

  const [activeSheet, setActiveSheet] = useState<ActiveSheetType>(null);
  const [modelSheetPage, setModelSheetPage] = useState<ModelSheetPage>('summary');
  const [modelQuery, setModelQuery] = useState('');
  const [popoverAnchor, setPopoverAnchor] = useState<{
    left: number;
    bottom: number;
    maxHeight: number;
  } | null>(null);
  const modelPillRef = useRef<HTMLButtonElement | null>(null);
  const supportsMethod = useRemoteMethodSupport();
  const beeTiersSupported = supportsMethod('threads.swarm.setBeeTiers');

  // 子代理档位覆盖：草稿按档位 ID 索引，保存时整表序列化下发；基线用来判断是否有未保存改动
  const [tierPanelOpen, setTierPanelOpen] = useState(false);
  const [tiers, setTiers] = useState<RemoteSwarmTier[] | null>(null);
  const [tierDraft, setTierDraft] = useState<TierOverrideDraft>(() => parseBeeTiers(swarmBeeTiers));
  const [tierBaseline, setTierBaseline] = useState<string>(() =>
    serializeBeeTiers(parseBeeTiers(swarmBeeTiers))
  );
  const [isLoadingTiers, setIsLoadingTiers] = useState(false);
  const [isSavingTiers, setIsSavingTiers] = useState(false);
  const [tierError, setTierError] = useState<string | null>(null);
  const [tierNotice, setTierNotice] = useState<string | null>(null);
  const [attachNotice, setAttachNotice] = useState<string | null>(null);
  const [isDraggingFiles, setIsDraggingFiles] = useState(false);
  const dragDepthRef = useRef(0);
  const textareaRef = useRef<HTMLTextAreaElement | null>(null);

  // 方向键历史导航状态（对应 internal/ui/composer_history.go:7 的 composerHistoryState）
  const [historyState, setHistoryState] = useState<ComposerHistoryState>(emptyComposerHistoryState);
  // 长文本粘贴：占位符 -> 原文，序号从 1 自增（对应 internal/ui/composer_paste.go:34）
  const [pastedPlaceholders, setPastedPlaceholders] = useState<Record<string, string>>({});
  const pastedSeqRef = useRef(0);
  // 草稿上方的会话引用胶囊（对应 internal/ui/composer.go:520 的 draftThreadMentions）
  const [draftThreadMentions, setDraftThreadMentions] = useState<DraftThreadMention[]>([]);
  // 发送前展开引用时要拉取被引用会话的历史，这期间禁用发送，避免重复提交
  const [isExpandingMentions, setIsExpandingMentions] = useState(false);
  const expandingMentionsRef = useRef(false);
  const sendThreadMentionText = useThreadMentionSendText();
  // 回填历史后要把光标移到文末：用请求计数器触发，避免 draft 未变（回填同一段文本）时光标不动
  const pendingCaretRef = useRef<number | null>(null);
  const [caretRequestID, setCaretRequestID] = useState(0);

  const requestCaret = useCallback((caret: number) => {
    pendingCaretRef.current = caret;
    setCaretRequestID((id) => id + 1);
  }, []);

  useEffect(() => {
    if (pendingCaretRef.current === null) return;
    const caret = pendingCaretRef.current;
    pendingCaretRef.current = null;
    const element = textareaRef.current;
    if (!element) return;
    element.setSelectionRange(caret, caret);
  }, [caretRequestID]);

  // 切换会话时退出历史导航、清空粘贴映射与会话引用（对应 internal/ui/composer.go:510：不把上一个会话的输入带过来）
  useEffect(() => {
    setHistoryState(emptyComposerHistoryState);
    setPastedPlaceholders({});
    setDraftThreadMentions([]);
    pastedSeqRef.current = 0;
  }, [threadID]);

  // 草稿随发送清空时一并丢弃粘贴映射（对应 internal/ui/composer.go:429 清空草稿时重置输入区状态）
  useEffect(() => {
    if (draft !== '') return;
    setPastedPlaceholders((previous) => (Object.keys(previous).length === 0 ? previous : {}));
  }, [draft]);

  // 发送前把占位符展开成原文：先写回草稿，让调用方的 draftRef 立刻拿到全文，再走原有发送回调。
  // 提交表单、⌘/Ctrl+Enter、中断并发送这三条发送路径都经过这里（对应 internal/ui/model.go:1920 等处的展开）。
  // 会话引用的展开是异步的，而且会在正文前拼一大段背景资料，所以只作为 overrideText 传给发送回调、
  // 不写回草稿：发送失败时输入框里仍是用户看得懂的原文（对应 internal/ui/model.go:1932 发出后才消费引用）。
  const sendWithExpandedPaste = useCallback(
    (send: (overrideText?: string) => void | Promise<boolean | void>) => {
      // 展开期间忽略重复发送
      if (expandingMentionsRef.current) return;
      const expanded = expandPastedText(draft, pastedPlaceholders);
      if (expanded !== draft) onDraftChange(expanded);
      const mentions = draftThreadMentions;
      if (mentions.length === 0) {
        void send();
        return;
      }
      expandingMentionsRef.current = true;
      setIsExpandingMentions(true);
      void (async () => {
        let text = expanded;
        try {
          text = await sendThreadMentionText(expanded, mentions);
        } catch {
          // 拉取失败时展开逻辑本身会跳过该引用块，这里兜底成原文发送，不阻止发送
          text = expanded;
        } finally {
          expandingMentionsRef.current = false;
          setIsExpandingMentions(false);
        }
        try {
          const accepted = await send(text);
          // 发送被受理后才消费引用胶囊（与桌面端一致）：失败时保留，方便重发
          if (accepted !== false) setDraftThreadMentions([]);
        } catch {
          // 发送回调自身已负责错误提示，这里只避免未捕获的 Promise 拒绝
        }
      })();
    },
    [draft, draftThreadMentions, onDraftChange, pastedPlaceholders, sendThreadMentionText]
  );

  // 点胶囊上的 ✕ 移除对应会话引用
  const handleRemoveDraftThreadMention = useCallback((mentionThreadID: string) => {
    setDraftThreadMentions((previous) => previous.filter((mention) => mention.threadID !== mentionThreadID));
  }, []);

  // 宿主推送的会话摘要变了就以它为准（整表覆盖，不能只合并本地的改动）
  useEffect(() => {
    const parsed = parseBeeTiers(swarmBeeTiers);
    setTierDraft(parsed);
    setTierBaseline(serializeBeeTiers(parsed));
  }, [swarmBeeTiers, threadID]);

  const loadSwarmTiers = useCallback(async () => {
    setIsLoadingTiers(true);
    setTierError(null);
    try {
      const result = await getSwarmTiers();
      setTiers(result.tiers);
    } catch (err) {
      setTiers([]);
      setTierError(err instanceof Error ? err.message : '读取子代理档位失败');
    } finally {
      setIsLoadingTiers(false);
    }
  }, [getSwarmTiers]);

  useEffect(() => {
    if (tierPanelOpen && tiers === null) void loadSwarmTiers();
  }, [loadSwarmTiers, tierPanelOpen, tiers]);

  const updateTierDraft = useCallback((tierIDKey: string, field: 'provider' | 'model', value: string) => {
    setTierDraft((previous) => {
      const nextEntry: Record<string, unknown> = { ...(previous[tierIDKey] ?? {}), [field]: value };
      // 清空输入等于取消该字段的覆盖
      if (!value.trim()) delete nextEntry[field];
      const next: TierOverrideDraft = { ...previous };
      if (Object.keys(nextEntry).length > 0) next[tierIDKey] = nextEntry;
      else delete next[tierIDKey];
      return next;
    });
    setTierNotice(null);
  }, []);

  // 切换 provider 时顺带清掉不属于新 provider 的 model 覆盖，避免留下无效组合
  const applyTierProvider = useCallback(
    (tierIDKey: string, providerID: string) => {
      setTierDraft((previous) => {
        const nextEntry: Record<string, unknown> = { ...(previous[tierIDKey] ?? {}) };
        if (providerID.trim()) nextEntry.provider = providerID;
        else delete nextEntry.provider;
        const nextModels = providers.find((candidate) => candidate.id === providerID)?.models ?? [];
        const currentModel = typeof nextEntry.model === 'string' ? nextEntry.model : '';
        if (currentModel && !nextModels.some((candidate) => candidate.id === currentModel)) {
          delete nextEntry.model;
        }
        const next: TierOverrideDraft = { ...previous };
        if (Object.keys(nextEntry).length > 0) next[tierIDKey] = nextEntry;
        else delete next[tierIDKey];
        return next;
      });
      setTierNotice(null);
    },
    [providers]
  );

  const resetTierDraft = useCallback((tierIDKey: string) => {
    setTierDraft((previous) => {
      if (!previous[tierIDKey]) return previous;
      const next = { ...previous };
      delete next[tierIDKey];
      return next;
    });
    setTierNotice(null);
  }, []);

  const handleSaveTiers = useCallback(async () => {
    if (isSavingTiers || !threadID) return;
    setIsSavingTiers(true);
    setTierError(null);
    setTierNotice(null);
    try {
      const payload = serializeBeeTiers(tierDraft);
      await setBeeTiersRpc({ threadID, beeTiers: payload, actionID: makeActionID() });
      setTierBaseline(payload);
      setTierNotice(payload ? '已保存档位覆盖' : '已恢复默认档位');
    } catch (err) {
      setTierError(err instanceof Error ? err.message : '保存档位覆盖失败');
    } finally {
      setIsSavingTiers(false);
    }
  }, [isSavingTiers, setBeeTiersRpc, threadID, tierDraft]);

  const tierDirty = serializeBeeTiers(tierDraft) !== tierBaseline;

  // Esc 关闭底部弹层，与点击遮罩关闭等价
  useEffect(() => {
    if (!activeSheet) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      setActiveSheet(null);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [activeSheet]);

  // 补全状态：@ 文件（异步搜索）、/ 命令与 # 模型（本地过滤）共用一个弹层
  const [triggerInfo, setTriggerInfo] = useState<TriggerQueryInfo | null>(null);
  const [fileSuggestions, setFileSuggestions] = useState<SuggestionItem[]>([]);
  const [selectedSuggestionIndex, setSelectedSuggestionIndex] = useState<number>(0);
  const [isLoadingSuggestions, setIsLoadingSuggestions] = useState<boolean>(false);
  const mentionDebounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mentionGenRef = useRef<number>(0);
  const triggerInfoRef = useRef<TriggerQueryInfo | null>(null);

  // `@` 补全里的会话候选：与文件候选共用一个菜单，会话排在文件之前，最多 5 条
  // （对应 internal/ui/composer_complete.go:303 的 mentionCompletionItems 顺序与 composer_complete.go:490 的上限）。
  const threadMentionCandidates = useThreadMentionCandidates({
    query: triggerInfo?.trigger === '@' ? triggerInfo.query : '',
    limit: 5,
    excludeThreadID: threadID,
    projectID: selectedProjectID
  });
  // 会话候选的 SuggestionItem 用 key 区分，选中时再取回 threadID / 标题
  const threadMentionPicks = useMemo(() => {
    if (!triggerInfo || triggerInfo.trigger !== '@') {
      return [] as { item: SuggestionItem; mention: DraftThreadMention }[];
    }
    return threadMentionCandidates.slice(0, 5).map((candidate) => ({
      item: {
        key: `thread:${candidate.threadID}`,
        title: candidate.insert,
        detail: candidate.detail ? `会话 · ${candidate.detail}` : '会话',
        icon: 'link' as const,
        kind: 'command' as const,
        insertText: ''
      },
      mention: { threadID: candidate.threadID, title: candidate.title } as DraftThreadMention
    }));
  }, [threadMentionCandidates, triggerInfo]);

  // 进入输入区拉一次命令清单（已有新鲜缓存时直接复用）；scope 变化（草稿切 Provider/项目）重新拉。
  const [commandsFailed, setCommandsFailed] = useState(false);
  const hasCommandSnapshot = Boolean(commandSnapshot);
  const hasCommandSnapshotRef = useRef(hasCommandSnapshot);
  hasCommandSnapshotRef.current = hasCommandSnapshot;
  const requestCommands = useCallback(() => {
    const scope = scopeRef.current;
    if (!scope) return;
    loadThreadCommands(scope).then(
      () => setCommandsFailed(false),
      () => setCommandsFailed(true)
    );
  }, [loadThreadCommands]);
  // 每个 scope 进入时请求一次；快照被清掉（重连重置）后再补拉一次，已有快照时不重复请求。
  const requestedScopeKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!canSlash) {
      requestedScopeKeyRef.current = null;
      return;
    }
    if (requestedScopeKeyRef.current === scopeKey && hasCommandSnapshot) return;
    requestedScopeKeyRef.current = scopeKey;
    requestCommands();
  }, [canSlash, hasCommandSnapshot, requestCommands, scopeKey]);

  const resetTrigger = useCallback(() => {
    if (mentionDebounceTimerRef.current) {
      clearTimeout(mentionDebounceTimerRef.current);
      mentionDebounceTimerRef.current = null;
    }
    mentionGenRef.current++;
    triggerInfoRef.current = null;
    setTriggerInfo(null);
    setFileSuggestions([]);
    setIsLoadingSuggestions(false);
  }, []);

  // 侦测光标处是否正在输入 @ / / / $ / # 片段；@ 防抖搜索文件，其余直接本地过滤
  const checkTrigger = useCallback(
    (text: string, cursor: number) => {
      const candidates: TriggerQueryInfo[] = [];
      if (canMention) {
        const info = extractTriggerQuery(text, cursor, '@');
        if (info) candidates.push(info);
      }
      if (canSlash) {
        const info = extractTriggerQuery(text, cursor, '/');
        if (info) candidates.push(info);
      }
      if (canSkill) {
        const info = extractTriggerQuery(text, cursor, '$');
        if (info) candidates.push(info);
      }
      if (canDelegate) {
        const info = extractTriggerQuery(text, cursor, '#');
        if (info) candidates.push(info);
      }
      // 多个触发符同时满足时取离光标最近的一个
      const info = candidates.sort((lhs, rhs) => rhs.startIndex - lhs.startIndex)[0] ?? null;
      if (!info) {
        resetTrigger();
        return;
      }

      const previousInfo = triggerInfoRef.current;
      if (!previousInfo || previousInfo.trigger !== info.trigger || previousInfo.query !== info.query) {
        setSelectedSuggestionIndex(0);
      }
      triggerInfoRef.current = info;
      setTriggerInfo(info);
      if ((info.trigger === '/' || info.trigger === '$') && !hasCommandSnapshotRef.current) requestCommands();
      if (info.trigger !== '@') {
        // 本地过滤：丢弃在途的文件搜索
        if (mentionDebounceTimerRef.current) {
          clearTimeout(mentionDebounceTimerRef.current);
          mentionDebounceTimerRef.current = null;
        }
        mentionGenRef.current++;
        setFileSuggestions([]);
        setIsLoadingSuggestions(false);
        return;
      }

      if (mentionDebounceTimerRef.current) {
        clearTimeout(mentionDebounceTimerRef.current);
      }

      const currentGen = ++mentionGenRef.current;
      setIsLoadingSuggestions(true);

      mentionDebounceTimerRef.current = setTimeout(async () => {
        try {
          let entries: RemoteFileEntry[] = [];
          const trimmed = info.query.trim();
          if (trimmed.length > 0) {
            const res = await searchThreadFiles(threadID!, trimmed);
            entries = res.entries;
          } else {
            // 查询词为空（仅输入了 @）：若具备 list 能力则优先拉取根目录，否则走搜索空串
            if (capabilities?.methods?.includes('files.list')) {
              const res = await listThreadFiles(threadID!, '');
              entries = res.entries;
            } else {
              const res = await searchThreadFiles(threadID!, '');
              entries = res.entries;
            }
          }

          if (mentionGenRef.current !== currentGen) return;
          // 受保护项不得补全，并限制建议项数量
          const filtered = entries.filter((entry) => !entry.protected).slice(0, 8);
          setFileSuggestions(filtered.map(fileEntryToSuggestion));
          setSelectedSuggestionIndex(0);
        } catch {
          if (mentionGenRef.current === currentGen) {
            setFileSuggestions([]);
          }
        } finally {
          if (mentionGenRef.current === currentGen) {
            setIsLoadingSuggestions(false);
          }
        }
      }, 180);
    },
    [canDelegate, canMention, canSkill, canSlash, capabilities, listThreadFiles, requestCommands, resetTrigger, searchThreadFiles, threadID]
  );

  // 清理补全定时器及在途代际
  useEffect(() => {
    return () => {
      if (mentionDebounceTimerRef.current) {
        clearTimeout(mentionDebounceTimerRef.current);
        mentionDebounceTimerRef.current = null;
      }
      mentionGenRef.current++;
    };
  }, []);

  // 会话切换或对应能力消失时清理补全状态（运行中开始后 # 候选也随之收起）
  useEffect(() => {
    if (!triggerInfo) return;
    const stillAllowed =
      (triggerInfo.trigger === '@' && canMention) ||
      (triggerInfo.trigger === '/' && canSlash) ||
      (triggerInfo.trigger === '$' && canSkill) ||
      (triggerInfo.trigger === '#' && canDelegate);
    if (!stillAllowed) resetTrigger();
  }, [canDelegate, canMention, canSkill, canSlash, resetTrigger, triggerInfo]);
  useEffect(() => {
    resetTrigger();
  }, [resetTrigger, scopeKey, threadID]);

  // `/` 与 `$` 共用同一份命令快照，未就绪时弹层显示加载中
  const commandTriggerLoading =
    (triggerInfo?.trigger === '/' || triggerInfo?.trigger === '$') &&
    !commandsFailed &&
    (!commandSnapshot || commandSnapshot.loading);
  const suggestionItems = useMemo<SuggestionItem[]>(() => {
    if (!triggerInfo) return [];
    if (triggerInfo.trigger === '@') return [...threadMentionPicks.map((pick) => pick.item), ...fileSuggestions];
    if (triggerInfo.trigger === '$') {
      if (!commandSnapshot) return [];
      return filterSkillCommands(commandSnapshot.commands, triggerInfo.query).slice(0, 30).map(skillCommandToSuggestion);
    }
    if (triggerInfo.trigger === '/') {
      if (!commandSnapshot) return [];
      return filterSlashCommands(commandSnapshot.commands, triggerInfo.query, commandSnapshot.recent)
        .slice(0, 30)
        .map(commandToSuggestion);
    }
    return modelSuggestions(providers, triggerInfo.query);
  }, [commandSnapshot, fileSuggestions, providers, threadMentionPicks, triggerInfo]);
  const suggestionIndex = Math.min(selectedSuggestionIndex, Math.max(suggestionItems.length - 1, 0));
  const showSuggestions = Boolean(triggerInfo) && (suggestionItems.length > 0 || Boolean(commandTriggerLoading));
  const suggestionFootnote = (triggerInfo?.trigger === '/' || triggerInfo?.trigger === '$') && commandSnapshot
    ? commandSnapshot.loading
      ? 'Mac 正在读取命令列表，稍后会自动补全'
      : commandSnapshot.error
      ? '命令列表读取失败，仅显示内置命令'
      : null
    : null;

  // 选择建议项，精准替换触发片段，保留光标前后内容
  const handleSelectSuggestion = useCallback(
    (item: SuggestionItem) => {
      if (!triggerInfo) return;
      const pick = threadMentionPicks.find((entry) => entry.item.key === item.key);
      const before = draft.slice(0, triggerInfo.startIndex);
      const after = draft.slice(triggerInfo.endIndex);
      if (pick) {
        // 会话引用：草稿里只删掉 `@查询` 片段，再在草稿上方挂一颗引用胶囊
        // （对应 internal/ui/composer_complete.go:612 与 addDraftThreadMention 的去重规则）。
        onDraftChange(before + after);
        setDraftThreadMentions((previous) =>
          previous.some((mention) => mention.threadID === pick.mention.threadID)
            ? previous.map((mention) =>
                mention.threadID === pick.mention.threadID ? pick.mention : mention
              )
            : [...previous, pick.mention]
        );
        resetTrigger();
        requestAnimationFrame(() => {
          if (textareaRef.current) {
            const nextCursor = before.length;
            textareaRef.current.setSelectionRange(nextCursor, nextCursor);
            textareaRef.current.focus();
          }
        });
        return;
      }
      const replacement = item.insertText;
      const nextDraft = before + replacement + after;
      onDraftChange(nextDraft);

      resetTrigger();

      requestAnimationFrame(() => {
        if (textareaRef.current) {
          const nextCursor = before.length + replacement.length;
          textareaRef.current.setSelectionRange(nextCursor, nextCursor);
          textareaRef.current.focus();
        }
      });
    },
    [draft, onDraftChange, resetTrigger, threadMentionPicks, triggerInfo]
  );

  // Auto-grow textarea height
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${Math.min(el.scrollHeight, 140)}px`;
  }, [draft]);

  // 快捷回复点击后聚焦输入框并定位到末尾（桌面端 applyQuickReply 同义：只填草稿、不发送）
  const focusComposer = useCallback(() => {
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      const end = el.value.length;
      el.setSelectionRange(end, end);
    });
  }, []);

  // 引用到回复：空草稿直接置为引用块，否则去掉草稿尾部换行后用空行分隔追加，然后聚焦输入框。
  // 与桌面 quoteIntoDraft 一致：引用文本不改写，只加前缀（对应 internal/ui/timeline_menu.go:35）。
  const insertDraft = useCallback(
    (text: string) => {
      const quoted = quoteForDraft(text);
      if (!quoted) return;
      const base = draft.replace(/\n+$/, '');
      onDraftChange(base ? `${base}\n\n${quoted}` : quoted);
      focusComposer();
    },
    [draft, onDraftChange, focusComposer]
  );

  useImperativeHandle(ref, () => ({ insertDraft }), [insertDraft]);

  const provider = providers.find((candidate) => candidate.id === composerOptions?.provider) ??
    providers.find((candidate) => candidate.models.some((model) => model.isDefault)) ??
    providers[0];
  const model = provider?.models.find((candidate) => candidate.id === composerOptions?.model) ??
    provider?.models.find((candidate) => candidate.isDefault) ??
    provider?.models[0];
  // 档位为空表示 CLI 默认，不是最低档；宿主按偏好档位 preferredEffort 解析
  const effort = composerOptions?.effort ?? model?.preferredEffort ?? '';
  const fastMode = composerOptions?.fastMode ?? false;

  // 附件不再限于图片：任意文件都可附加，图片由宿主压缩；当前模型不支持图片时只跳过图片
  const imagesUnsupported = provider?.supportsImages === false;
  const handlePickedAttachments = useCallback(
    (files: FileList | null) => {
      if (!files || files.length === 0) return;
      if (!imagesUnsupported) {
        setAttachNotice(null);
        onAddAttachments(files);
        return;
      }
      const picked = Array.from(files);
      const images = picked.filter(isImageFile);
      const others = picked.filter((file) => !isImageFile(file));
      setAttachNotice(images.length > 0 ? '当前模型不支持图片，已跳过选中的图片' : null);
      if (others.length === 0) return;
      try {
        // FileList 不能直接构造，用 DataTransfer 把非图片文件重新打包交给上传
        const transfer = new DataTransfer();
        others.forEach((file) => transfer.items.add(file));
        onAddAttachments(transfer.files);
      } catch {
        setAttachNotice('当前浏览器不支持筛选附件，请只选择非图片文件');
      }
    },
    [imagesUnsupported, onAddAttachments]
  );

  const swarmPairs = swarmPairsAllowed && swarm?.enabled ? swarm.pairs : [];
  const activePair = swarmPairs.find((pair) => pair.id === composerOptions?.swarmPairID);
  // 组合标题与桌面 swarmPairTitle 一致：主导显示名 + 执行显示名，未指定时写占位词
  // 旧版按全名保存的自动标题当作未命名，只显示短名，避免胶囊里出现大段全名
  const pairName = (pair: RemoteSwarmPair) => {
    const leadFull = modelDisplayName(providers, pair.provider, pair.leadModel);
    const workerFull = modelDisplayName(providers, pair.workerProvider ?? pair.provider, pair.workerModel);
    if (pair.name && !isAutoPairName(pair.name, leadFull, workerFull)) return pair.name;
    return `${modelShortDisplayName(providers, pair.provider, pair.leadModel) || '任意模型'} + ${
      modelShortDisplayName(providers, pair.workerProvider ?? pair.provider, pair.workerModel) || '跟随主代理'
    }`;
  };
  // 组合的全名：胶囊 title 用，可见文字只放短名
  const pairFullName = (pair: RemoteSwarmPair) =>
    pair.name ||
    `${modelDisplayName(providers, pair.provider, pair.leadModel) || '任意模型'} + ${
      modelDisplayName(providers, pair.workerProvider ?? pair.provider, pair.workerModel) || '跟随主代理'
    }`;
  const pairDetail = (pair: RemoteSwarmPair) => {
    const leadModelLabel = modelShortDisplayName(providers, pair.provider, pair.leadModel);
    const lead = [leadModelLabel || providers.find((p) => p.id === pair.provider)?.name, pair.leadEffort]
      .filter(Boolean)
      .join(' · ');
    const worker = modelShortDisplayName(providers, pair.workerProvider ?? pair.provider, pair.workerModel) ||
      (pair.workerProvider ? providers.find((p) => p.id === pair.workerProvider)?.name : null);
    return worker ? `${lead} → ${worker}` : lead;
  };

  // 编排开关的唯一真值：草稿页由 composerOptions 承载，已开始的会话则以宿主下发的 swarmEnabled 为准（
  // 来自 swarmState / 会话摘要）。开关、子代理 tab、档位入口、模型胶囊必须读同一个值，否则会出现
  // 打开开关 tab 不出现、关闭后 tab 也不消失。
  const swarmOn = swarmEnabled ?? (composerOptions?.swarmEnabled === true);
  const beesAllowed = swarmPairsAllowed && Boolean(swarm?.enabled) && swarmOn;
  const effectiveSide = modelSheetPage === 'bees' && beesAllowed ? 'bees' : 'lead';
  // 编排关掉后，停在子代理页就退回摘要页（停在摘要或主代理页则不动，避免打断浏览）
  useEffect(() => {
    if (beesAllowed || modelSheetPage !== 'bees') return;
    setModelSheetPage('summary');
  }, [beesAllowed, modelSheetPage]);
  // 档位覆盖按会话存：没有会话、编排未开启或宿主不支持时不显示入口
  const tiersSectionVisible = beesAllowed && Boolean(threadID) && beeTiersSupported;

  // 宽屏浮层锚点：贴在模型胶囊上方；窄屏返回 null，继续走底部 sheet。
  const computePopoverAnchor = useCallback(() => {
    if (!window.matchMedia('(min-width: 900px)').matches) return null;
    const button = modelPillRef.current;
    if (!button) return null;
    const rect = button.getBoundingClientRect();
    const width = Math.min(440, window.innerWidth - 32);
    const left = Math.min(Math.max(rect.left, 16), window.innerWidth - width - 16);
    return {
      left,
      bottom: window.innerHeight - rect.top + 8,
      maxHeight: rect.top - 16
    };
  }, []);

  // 窗口尺寸变化时重新贴合胶囊，收起后不再监听。
  useEffect(() => {
    if (activeSheet !== 'model-effort') return;
    const handleResize = () => setPopoverAnchor(computePopoverAnchor());
    window.addEventListener('resize', handleResize);
    return () => window.removeEventListener('resize', handleResize);
  }, [activeSheet, computePopoverAnchor]);

  // 所有 sheet 都支持 Esc 关闭；模型浮层的二级页先退回摘要页。中文输入法组字时不拦截。
  useEffect(() => {
    if (activeSheet === null) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || isImeComposing(event)) return;
      if (activeSheet === 'model-effort') {
        const next = modelSheetPageOnEscape(modelSheetPage);
        if (next !== null) {
          setModelSheetPage(next);
          setModelQuery('');
          return;
        }
      }
      setActiveSheet(null);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [activeSheet, modelSheetPage]);

  const beesProvider = composerOptions?.swarmBeesProvider
    ? providers.find((candidate) => candidate.id === composerOptions.swarmBeesProvider)
    : undefined;
  const beesModel = beesProvider?.models.find((candidate) => candidate.id === composerOptions?.swarmBeesModel)
    ?? (composerOptions?.swarmBeesModel
      ? providers.flatMap((candidate) => candidate.models).find((candidate) => candidate.id === composerOptions?.swarmBeesModel)
      : undefined);

  const beesModelName = useMemo(() => {
    if (!composerOptions?.swarmBeesModel) return null;
    return beesModel?.name ??
      modelDisplayName(
        providers,
        composerOptions.swarmBeesProvider ?? composerOptions.provider,
        composerOptions.swarmBeesModel
      );
  }, [composerOptions?.swarmBeesModel, composerOptions?.swarmBeesProvider, composerOptions?.provider, beesModel, providers]);

  const selectBees = (providerID: string, modelID: string) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      swarmBeesProvider: providerID,
      swarmBeesModel: modelID,
      swarmBeesEffort: undefined,
      swarmPairID: undefined,
      swarmEnabled: true
    });
    setModelQuery('');
  };

  const selectBeesEffort = (nextEffort: string) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      swarmBeesEffort: nextEffort || undefined,
      swarmPairID: undefined
    });
  };

  const selectProviderAndModel = (providerID: string, modelID: string) => {
    if (!onComposerOptionsChange) return;
    const nextProvider = providers.find((candidate) => candidate.id === providerID);
    const nextModel = nextProvider?.models.find((candidate) => candidate.id === modelID) ??
      nextProvider?.models.find((candidate) => candidate.isDefault) ??
      nextProvider?.models[0];
    onComposerOptionsChange({
      ...composerOptions,
      provider: providerID,
      model: nextModel?.id,
      // 换模型不带档位，宿主按该模型的偏好档位解析；普通模型即退出编排组合
      effort: undefined,
      fastMode: undefined,
      swarmPairID: undefined
    });
    // 选择后清空搜索，sheet 保持打开。
    setModelQuery('');
  };

  const selectPair = (pair: RemoteSwarmPair) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      provider: pair.provider,
      model: pair.leadModel ?? undefined,
      effort: pair.leadEffort ?? undefined,
      fastMode: undefined,
      swarmPairID: pair.id,
      swarmEnabled: true,
      swarmBeesProvider: pair.workerProvider ?? (pair.workerModel ? pair.provider : undefined),
      swarmBeesModel: pair.workerModel ?? undefined,
      swarmBeesEffort: undefined
    });
  };

  const selectEffort = (nextEffort: string) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      effort: nextEffort || undefined
    });
  };

  const selectInteractionMode = (mode: RemoteComposerOptions['interactionMode']) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      interactionMode: mode || undefined
    });
    setActiveSheet(null);
  };

  const selectRuntimeMode = (mode: RemoteComposerOptions['runtimeMode']) => {
    if (!onComposerOptionsChange) return;
    onComposerOptionsChange({
      ...composerOptions,
      runtimeMode: mode || undefined
    });
    setActiveSheet(null);
  };

  // 人设：宿主同时声明 providers.personas 与 threads.setPersona 才有入口，旧宿主整块隐藏。
  const {
    personas: personaOptions,
    setPersona: setThreadPersona,
    supported: personaSupported
  } = useThreadPersonas(threadID, composerOptions?.provider, composerOptions?.model);
  const [personaMessage, setPersonaMessage] = useState<string | null>(null);
  // 切换中禁用下拉，避免连点重复下发写入
  const [personaSwitching, setPersonaSwitching] = useState(false);
  const selectPersona = async (persona: string) => {
    if (!personaSupported) return;
    setPersonaMessage(null);
    setPersonaSwitching(true);
    try {
      await setThreadPersona(persona);
      // 人设入口就在模型弹窗里，切换后不关闭弹窗，让下拉里的当前值留在原地可见。
    } catch (err) {
      // 失败时留在菜单里提示，别让用户以为已经切好了。
      setPersonaMessage(
        err instanceof Error && err.message ? `切换人设失败：${err.message}` : '切换人设失败'
      );
    } finally {
      setPersonaSwitching(false);
    }
  };

  // 运行中发精确的本地命令（/plan、/compact）走排队，本轮结束后由宿主执行；不能用“中断并发送”。
  const queuesLocalCommand = commandsSupported && isRunning && localCommandName(draft) !== null;
  const queueSendLabel = queuesLocalCommand ? '排队，本轮结束后执行' : '排队发送';

  // 主代理与子代理的短名：胶囊和摘要行只放短名，全名走 title 供悬停查看
  const leadModelShortName = provider
    ? modelShortDisplayName(providers, provider.id, model?.id) || model?.name || provider.name
    : '';
  const beesModelShortName = composerOptions?.swarmBeesModel
    ? modelShortDisplayName(
        providers,
        composerOptions.swarmBeesProvider ?? composerOptions.provider,
        composerOptions.swarmBeesModel
      ) || beesModelName
    : null;
  // 胶囊按钮的可见文字，同时给 aria-label 拼后缀用
  const modelPillLabel = activePair
    ? `编排 · ${pairName(activePair)}`
    : `${leadModelShortName}${beesAllowed && beesModelShortName ? ` → ${beesModelShortName}` : ''}`;
  // 悬停提示用全名，胶囊里的短名省略了品牌与附加说明
  const modelPillFullName = activePair
    ? `编排 · ${pairFullName(activePair)}`
    : `${model?.name || provider?.name || ''}${beesAllowed && beesModelName ? ` → ${beesModelName}` : ''}`;
  const modelPillSuffix = `${effort ? ` · ${effort}` : ''}${fastMode ? ' · ⚡' : ''}`;
  const modePillLabel = modeLabel(composerOptions?.interactionMode);
  const permissionPillLabel = permissionLabel(composerOptions?.runtimeMode);

  // 当前会话正在用的 provider：桌面设置里关掉了也要留在列表里显示
  const currentProviderID = composerOptions?.provider ?? provider?.id;
  const providerVisible = useCallback(
    (candidate: RemoteProvider, keepID?: string) =>
      candidate.enabled !== false || candidate.id === currentProviderID || candidate.id === keepID,
    [currentProviderID]
  );

  // 选择器分区：同一套纯函数产出「当前 / 收藏 / 按 Agent 分组」。
  const pickerSections = useMemo(() => {
    // 子代理侧：当前已选的 provider 即使被禁用也要留在列表里可见
    const pinnedProviderID = effectiveSide === 'bees' ? composerOptions?.swarmBeesProvider : undefined;
    const current = effectiveSide === 'bees'
      ? composerOptions?.swarmBeesProvider && composerOptions?.swarmBeesModel
        ? { provider: composerOptions.swarmBeesProvider, model: composerOptions.swarmBeesModel }
        : null
      : provider && model
        ? { provider: provider.id, model: model.id }
        : null;
    return buildPickerSections({
      providers,
      query: modelQuery,
      isProviderVisible: (candidate) => providerVisible(candidate, pinnedProviderID),
      current
    });
  }, [
    composerOptions?.swarmBeesProvider,
    composerOptions?.swarmBeesModel,
    effectiveSide,
    model,
    modelQuery,
    provider,
    providerVisible,
    providers
  ]);

  const currentProject = projects?.find((p) => p.id === selectedProjectID);

  // 编排模板区块：摘要页里模型入口行上方
  const swarmTemplateHeader = swarmPairs.length > 0 ? (
    <div className="space-y-1">
      <div className="text-mc-caption font-semibold text-[var(--foreground-muted)] px-1 uppercase tracking-wide">
        编排组合
      </div>
      <div className="sheet-actions !mt-1">
        {swarmPairs.map((pair) => {
          const unavailableReason = pairUnavailableReason(pair, providers);
          const unavailable = unavailableReason !== null;
          return (
            <button
              key={pair.id}
              type="button"
              disabled={unavailable}
              title={pairFullName(pair)}
              className="sheet-action justify-between disabled:opacity-40"
              onClick={() => selectPair(pair)}
            >
              <div className="min-w-0 text-left">
                <div className="font-semibold text-sm text-[var(--foreground)] truncate">{pairName(pair)}</div>
                <div className="text-xs text-[var(--foreground-muted)] truncate">
                  {unavailable
                    ? unavailableReason
                    : `${pairDetail(pair)}${pair.maxBees !== null ? ` · 最多 ${pair.maxBees} 个子代理` : ''}`}
                </div>
              </div>
              {activePair?.id === pair.id && <CheckCircleIcon className="w-4 h-4 text-[var(--primary)] shrink-0" />}
            </button>
          );
        })}
      </div>
    </div>
  ) : null;

  // 子代理侧模型列表顶部：跟随主代理模型
  const followLeadHeader = (
    <div className="sheet-actions !mt-0">
      <button
        type="button"
        className="sheet-action justify-between"
        onClick={() => {
          // 选完自动回到摘要页，让当前值在一级页立即可见。
          setModelSheetPage('summary');
          setModelQuery('');
          if (!onComposerOptionsChange) return;
          onComposerOptionsChange({
            ...composerOptions,
            swarmBeesProvider: undefined,
            swarmBeesModel: undefined,
            swarmBeesEffort: undefined,
            swarmPairID: undefined
          });
        }}
      >
        <div className="font-semibold text-sm text-[var(--foreground)]">跟随主代理模型</div>
        {!composerOptions?.swarmBeesProvider && (
          <CheckCircleIcon className="w-4 h-4 text-[var(--primary)] shrink-0" />
        )}
      </button>
    </div>
  );

  // 运行中且没有草稿/附件时，「主按钮」本身就是停止键；否则主按钮是发送键，
  // 运行中再在旁边显示一个独立的停止键（与改版前的布局一致）。
  const primaryActionStops = Boolean(isRunning && !draft.trim() && attachments.length === 0);
  const attachmentsAvailable = attachmentsSupported && !isUploadingAttachment && attachments.length < 4;
  useEffect(() => {
    if (!attachmentsAvailable) {
      dragDepthRef.current = 0;
      setIsDraggingFiles(false);
    }
  }, [attachmentsAvailable]);

  return (
    <div
      className="shrink-0 px-3 sm:px-4 pt-1"
      style={{ paddingBottom: 'var(--composer-bottom-inset, 26px)' }}
    >
      <QuickReplyBar
        draft={draft}
        isRunning={isRunning}
        attachmentCount={attachments.length}
        hasMessages={hasMessages}
        onDraftChange={onDraftChange}
        onFocusComposer={focusComposer}
      />
      {/* 提示行和额度条都悬浮在表单外，不占文档流。 */}
      <div className="relative">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          if (isRunning && !draft.trim() && attachments.length === 0) {
            onStop?.();
          } else if (!sendDisabled) {
            sendWithExpandedPaste(onSend);
          }
        }}
        onDragEnter={(event) => {
          const intent = fileDragIntent(event.dataTransfer.types, attachmentsAvailable);
          if (!intent.preventDefault) return;
          event.preventDefault();
          if (!intent.acceptsFiles) return;
          dragDepthRef.current += 1;
          setIsDraggingFiles(true);
        }}
        onDragOver={(event) => {
          const intent = fileDragIntent(event.dataTransfer.types, attachmentsAvailable);
          if (!intent.preventDefault) return;
          event.preventDefault();
          if (!intent.acceptsFiles) return;
          event.dataTransfer.dropEffect = 'copy';
        }}
        onDragLeave={(event) => {
          if (!Array.from(event.dataTransfer.types).includes('Files')) return;
          dragDepthRef.current = Math.max(0, dragDepthRef.current - 1);
          if (dragDepthRef.current === 0) setIsDraggingFiles(false);
        }}
        onDrop={(event) => {
          const intent = fileDragIntent(event.dataTransfer.types, attachmentsAvailable);
          if (!intent.preventDefault) return;
          event.preventDefault();
          if (!intent.acceptsFiles) return;
          dragDepthRef.current = 0;
          setIsDraggingFiles(false);
          handlePickedAttachments(event.dataTransfer.files);
        }}
        className={`rounded-3xl bg-[var(--composer-surface)] border ${isDraggingFiles && attachmentsAvailable ? 'border-[var(--primary)]' : 'border-[var(--input-border)]'} backdrop-blur-md p-2.5 shadow-sm min-w-0 relative`}
      >
        {/* @ 文件 / 斜杠命令 / $ 技能 / # 模型 补全建议弹层 */}
        {showSuggestions && triggerInfo && (
          <CommandSuggestions
            trigger={triggerInfo.trigger}
            suggestions={suggestionItems}
            selectedIndex={suggestionIndex}
            onSelect={handleSelectSuggestion}
            onClose={resetTrigger}
            isLoading={triggerInfo.trigger === '@' ? isLoadingSuggestions : Boolean(commandTriggerLoading)}
            footnote={suggestionFootnote}
          />
        )}

        {/* 已添加的附件：图片显示缩略图，其余显示文件名与大小 */}
        <ComposerAttachmentStrip
          attachments={attachments}
          attachmentPreviews={attachmentPreviews}
          onRemoveAttachment={onRemoveAttachment}
        />

        {/* 已选中的会话引用：草稿上方一排「@标题」胶囊（对应 internal/ui/composer.go:302 的 renderDraftThreadMentions） */}
        <ThreadMentionChips
          mentions={draftThreadMentions}
          onRemove={handleRemoveDraftThreadMention}
          className="px-1 pb-2"
        />

        {/* Textarea */}
        <textarea
          ref={textareaRef}
          value={draft}
          onChange={(event) => {
            const value = event.target.value;
            const cursor = event.target.selectionStart ?? value.length;
            // 用户改动了回填的历史文本就退出导航，保留已输入的文本（对应 internal/ui/composer.go:804）
            if (historyState.index !== 0 && value !== historyState.recalled) {
              setHistoryState(composerHistoryReset());
            }
            onDraftChange(value);
            checkTrigger(value, cursor);
          }}
          onPaste={(event) => {
            // 附件可用时将粘贴的文件交给上传入口。
            const clipboard = event.clipboardData;
            if (!clipboard) return;
            if (clipboard.files.length > 0) {
              if (attachmentsAvailable) {
                event.preventDefault();
                handlePickedAttachments(clipboard.files);
              }
              return;
            }
            const pasted = clipboard.getData('text/plain');
            if (!pasted || !isLongPastedText(pasted)) return;
            event.preventDefault();
            const target = event.currentTarget;
            const start = target.selectionStart ?? draft.length;
            const end = target.selectionEnd ?? start;
            const { seq, label } = nextPastedPlaceholder(pastedSeqRef.current, pasted);
            pastedSeqRef.current = seq;
            const inserted = insertPlaceholderAt(draft, start, end, label);
            setPastedPlaceholders((previous) => ({ ...previous, [label]: pasted }));
            setHistoryState(composerHistoryReset());
            requestCaret(inserted.caret);
            onDraftChange(inserted.text);
          }}
          onSelect={(event) => {
            const cursor = event.currentTarget.selectionStart ?? draft.length;
            checkTrigger(draft, cursor);
          }}
          onKeyUp={(event) => {
            if (
              event.key === 'ArrowLeft' ||
              event.key === 'ArrowRight' ||
              event.key === 'Home' ||
              event.key === 'End'
            ) {
              const cursor = event.currentTarget.selectionStart ?? draft.length;
              checkTrigger(draft, cursor);
            }
          }}
          onClick={(event) => {
            const cursor = event.currentTarget.selectionStart ?? draft.length;
            checkTrigger(draft, cursor);
          }}
          onKeyDown={(event) => {
            if (showSuggestions && suggestionItems.length > 0 && !isImeComposing(event)) {
              if (event.key === 'ArrowDown') {
                event.preventDefault();
                setSelectedSuggestionIndex((suggestionIndex + 1) % suggestionItems.length);
                return;
              }
              if (event.key === 'ArrowUp') {
                event.preventDefault();
                setSelectedSuggestionIndex((suggestionIndex - 1 + suggestionItems.length) % suggestionItems.length);
                return;
              }
              if (event.key === 'Enter' || event.key === 'Tab') {
                event.preventDefault();
                handleSelectSuggestion(suggestionItems[suggestionIndex]);
                return;
              }
              if (event.key === 'Escape') {
                event.preventDefault();
                resetTrigger();
                return;
              }
            }
            // 方向键历史导航：首行 ↑ 翻更旧、末行 ↓ 翻更新（对应 internal/ui/composer.go:832）。
            // 补全菜单打开时方向键已在上面归菜单，这里只在无修饰键、无选区、有历史时生效。
            if (
              !isImeComposing(event) &&
              !event.shiftKey &&
              !event.altKey &&
              !event.metaKey &&
              !event.ctrlKey &&
              (event.key === 'ArrowUp' || event.key === 'ArrowDown') &&
              event.currentTarget.selectionStart === event.currentTarget.selectionEnd &&
              (historyEntries?.length ?? 0) > 0
            ) {
              const entries = historyEntries ?? [];
              const text = event.currentTarget.value;
              const caret = event.currentTarget.selectionStart ?? 0;
              const move =
                event.key === 'ArrowUp'
                  ? caretOnFirstLine(text, caret)
                    ? composerHistoryPrev(historyState, entries, text)
                    : null
                  : caretOnLastLine(text, caret) && historyState.index !== 0
                  ? composerHistoryNext(historyState, entries)
                  : null;
              if (move) {
                event.preventDefault();
                setHistoryState(move.state);
                requestCaret(move.text.length);
                onDraftChange(move.text);
                return;
              }
            }
            // Esc 退出历史导航并恢复进入导航前的草稿（对应 internal/ui/composer.go:857）
            if (event.key === 'Escape' && !isImeComposing(event) && historyState.index !== 0) {
              const restored = composerHistoryRestore(historyState);
              if (restored) {
                event.preventDefault();
                setHistoryState(restored.state);
                requestCaret(restored.text.length);
                onDraftChange(restored.text);
                return;
              }
            }
            if (event.key === 'Enter' && (event.metaKey || event.ctrlKey) && !isImeComposing(event)) {
              event.preventDefault();
              if (!sendDisabled) sendWithExpandedPaste(onSend);
            }
          }}
          rows={1}
          aria-label="输入要发送的消息"
          placeholder={placeholder ?? '继续输入消息…'}
          className="w-full bg-transparent border-0 outline-none resize-none text-[16px] leading-[22px] text-[var(--foreground)] placeholder:text-[var(--foreground-muted)] px-1.5 py-1 max-h-36 min-h-[44px]"
        />

        {/* Controls row */}
        <div className="flex items-center justify-between gap-1.5 pt-1.5 min-w-0">
          {/* Horizontally scrollable row of compact pills */}
          <div className="flex items-center gap-1.5 flex-nowrap overflow-x-auto no-scrollbar flex-1 min-w-0 py-0.5">
            {/* Attach File Pill：任意文件都可添加，图片由宿主压缩后上传 */}
            {attachmentsSupported && (
              <ComposerAttachButton
                disabled={!attachmentsAvailable}
                title={imagesUnsupported ? '添加附件（当前模型不支持图片）' : '添加附件'}
                onPicked={handlePickedAttachments}
              />
            )}

            {/* Project Picker Pill (when projects available in DraftView) */}
            {projects && projects.length > 0 && onSelectProject && (
              <button
                type="button"
                onClick={() => setActiveSheet('project')}
                className="h-[30px] rounded-full text-mc-body font-medium px-2.5 inline-flex items-center gap-1 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] active:scale-95 transition"
              >
                <span className="truncate max-w-[120px]">{currentProject?.name || '选择项目'}</span>
                <ChevronDownIcon className="w-3 h-3 text-[var(--foreground-muted)]" />
              </button>
            )}

            {/* Unified Model · Effort Pill（子代理页模型固定，只做静态展示） */}
            {lockedModelLabel && (
              <span className="h-[30px] rounded-full text-mc-body font-medium px-2.5 inline-flex items-center gap-1 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)] max-w-[160px] sm:max-w-[200px]">
                <span className="truncate min-w-0">
                  {lockedModelLabel}
                  {effort ? ` · ${effort}` : ''}
                </span>
              </span>
            )}

            {/* Unified Model · Effort Pill */}
            {!lockedModelLabel && provider && onComposerOptionsChange && (
              <button
                type="button"
                ref={modelPillRef}
                disabled={composerOptionsDisabled}
                onClick={() => {
                  setModelQuery('');
                  setModelSheetPage('summary');
                  setPopoverAnchor(computePopoverAnchor());
                  setActiveSheet('model-effort');
                }}
                aria-haspopup="dialog"
                title={modelPillFullName}
                aria-label={`模型与思考设置：${modelPillLabel}${modelPillSuffix}`}
                className="h-[30px] rounded-full text-mc-body font-medium px-2.5 inline-flex items-center gap-1 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] disabled:opacity-40 active:scale-95 transition max-w-[160px] sm:max-w-[200px]"
              >
                <span className="truncate min-w-0">
                  {modelPillLabel}
                  {modelPillSuffix}
                </span>
                <ChevronDownIcon className="w-3 h-3 text-[var(--foreground-muted)] shrink-0" />
              </button>
            )}

            {/* 上下文占用与累计费用（宿主没上报时不渲染） */}
            <ContextUsageBadge threadID={threadID} />
            {/* Mode Pill (默认 / 计划) */}
            {onComposerOptionsChange && (
              <button
                type="button"
                disabled={composerOptionsDisabled}
                onClick={() => setActiveSheet('mode')}
                aria-haspopup="dialog"
                aria-label={`执行模式：${modePillLabel}`}
                className="h-[30px] rounded-full text-mc-body font-medium px-2.5 inline-flex items-center gap-1 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] disabled:opacity-40 active:scale-95 transition"
              >
                <span>{modePillLabel}</span>
                <ChevronDownIcon className="w-3 h-3 text-[var(--foreground-muted)]" />
              </button>
            )}

            {/* Permission Pill */}
            {onComposerOptionsChange && (
              <button
                type="button"
                disabled={composerOptionsDisabled}
                onClick={() => setActiveSheet('permission')}
                aria-haspopup="dialog"
                aria-label={`权限模式：${permissionPillLabel}`}
                className="h-[30px] rounded-full text-mc-body font-medium px-2.5 inline-flex items-center gap-1 shrink-0 border border-[var(--border)] bg-[var(--card)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] disabled:opacity-40 active:scale-95 transition"
              >
                <span>{permissionPillLabel}</span>
                <ChevronDownIcon className="w-3 h-3 text-[var(--foreground-muted)]" />
              </button>
            )}

            {personaSupported && (
              <PersonaBadge threadID={threadID} personas={personaOptions} />
            )}
          </div>

          {/* Right side actions: Interrupt & Send or Stop / Send button */}
          <div className="flex items-center gap-1.5 shrink-0">
            {/* 总管会话的分割入口：放在右侧组里离发送键最近，避免被左侧可滚动胶囊行挤出屏幕 */}
            {onSplitSession && (
              <button
                type="button"
                onClick={onSplitSession}
                disabled={splitSessionDisabled}
                aria-label="分割会话"
                title={splitSessionDisabled ? '等这一轮结束再分割' : '分割会话：下一条消息不再带上面的历史'}
                className="h-[30px] rounded-full px-2.5 text-mc-caption font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--foreground-muted)] hover:text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] disabled:opacity-40 active:scale-95 transition"
              >
                分割
              </button>
            )}
            {onInterruptAndSend && draft.trim().length > 0 && isRunning && !queuesLocalCommand && (
              <button
                type="button"
                onMouseDown={keepComposerFocus}
                onClick={() => {
                  if (onInterruptAndSend) sendWithExpandedPaste(onInterruptAndSend);
                }}
                disabled={interruptAndSendDisabled || isExpandingMentions}
                className="h-[30px] rounded-full px-2.5 text-mc-caption font-medium border border-[var(--border)] bg-[var(--card)] text-[var(--warning-foreground)] hover:bg-[var(--card-alt,var(--card))] transition active:scale-95"
              >
                {isInterrupting ? '中断中…' : '中断并发送'}
              </button>
            )}

            {/* Primary / Stop Button：同一颗按钮在停止与发送之间做图标形变（W-16），
                运行中已有草稿/附件时，主按钮变成发送（排队），旁边再显示独立停止键。 */}
            <div className="flex items-center gap-1.5">
              {/* 始终挂载、用 hidden 控制显隐：主按钮在兄弟节点中的位置不随状态变化，
                  否则 React 会重建 DOM，形变过渡就会丢。 */}
              <button
                type="button"
                onMouseDown={keepComposerFocus}
                onClick={onStop}
                disabled={stopDisabled}
                aria-label="停止"
                title="停止当前执行"
                className={`w-8 h-8 rounded-full bg-[var(--danger-foreground)] text-white flex items-center justify-center shrink-0 shadow-sm active:scale-95 transition disabled:opacity-40 ${
                  isRunning && !primaryActionStops ? '' : 'hidden'
                }`}
              >
                <PrimaryActionIcon state="stop" />
              </button>
              <button
                type={primaryActionStops ? 'button' : 'submit'}
                onMouseDown={keepComposerFocus}
                onClick={primaryActionStops ? onStop : undefined}
                disabled={primaryActionStops ? stopDisabled : sendDisabled || isExpandingMentions}
                aria-label={primaryActionStops ? '停止' : isRunning ? queueSendLabel : submitLabel}
                title={primaryActionStops ? '停止当前执行' : isRunning ? queueSendLabel : submitLabel}
                aria-busy={!primaryActionStops && (isSending || isExpandingMentions) ? true : undefined}
                className={`w-8 h-8 rounded-full text-white flex items-center justify-center shrink-0 shadow-sm disabled:opacity-40 active:scale-95 transition ${
                  primaryActionStops ? 'bg-[var(--danger-foreground)]' : 'bg-[var(--primary)]'
                }`}
              >
                <PrimaryActionIcon state={primaryActionStops ? 'stop' : 'send'} />
              </button>
            </div>
          </div>
        </div>
      </form>
      {(sendBlockedReason || isPolling || composerOptionsMessage || queuesLocalCommand || attachNotice) && (
        <div className="absolute left-0 right-0 bottom-full mb-2 z-20 flex justify-center pointer-events-none" aria-live="polite">
          <div className="floating-status__pill flex-col">
            {attachNotice && <p>{attachNotice}</p>}
            {queuesLocalCommand && !sendBlockedReason && (
              <p>本地命令将排队，本轮结束后执行。</p>
            )}
            {sendBlockedReason && <p>{sendBlockedReason}</p>}
            {!sendBlockedReason && isPolling && (
              <p>正在同步最新会话状态...</p>
            )}
            {composerOptionsMessage && <p>{composerOptionsMessage}</p>}
          </div>
        </div>
      )}
      {/* 额度条悬浮在输入区底部留白里，提示行悬浮在输入卡片上方，均不占文档流。 */}
      {quotaScope && <ComposerQuotaBar scope={quotaScope} isRunning={isRunning} floating />}
      </div>

      {/* Pill selection bottom sheets */}
      {activeSheet && (
        <div
          className={`sheet-layer${activeSheet === 'model-effort' && popoverAnchor ? ' sheet-layer--popover' : ''}`}
          role="presentation"
          onMouseDown={() => setActiveSheet(null)}
        >
          <section
            className={`settings-sheet${activeSheet === 'model-effort' ? ' model-sheet' : ''}${
              activeSheet === 'model-effort' && popoverAnchor ? ' model-popover' : ''
            }`}
            style={
              activeSheet === 'model-effort' && popoverAnchor
                ? {
                    left: popoverAnchor.left,
                    bottom: popoverAnchor.bottom,
                    maxHeight: popoverAnchor.maxHeight,
                    top: 'auto'
                  }
                : undefined
            }
            role="dialog"
            aria-modal="true"
            onMouseDown={(e) => e.stopPropagation()}
          >
            <div className="sheet-handle" aria-hidden="true" />
            <div className="flex items-center justify-between gap-3 mb-3">
              <h3 className="sheet-title text-base font-bold text-[var(--foreground)]">
                {activeSheet === 'model-effort'
                  ? '模型与思考设置'
                  : activeSheet === 'mode'
                  ? '选择模式'
                  : activeSheet === 'permission'
                  ? '选择权限模式'
                  : '选择项目'}
              </h3>
              <button
                type="button"
                className="icon-button"
                onClick={() => setActiveSheet(null)}
                aria-label="关闭"
              >
                <XMarkIcon className="h-5 w-5" />
              </button>
            </div>

            {activeSheet === 'model-effort' && (
              <>
                {modelSheetPage === 'summary' ? (
                  <div className="space-y-3">
                    {isMissingSelection(providers, composerOptions?.provider, composerOptions?.model) && (
                      <p className="text-xs text-[var(--danger-foreground)]">
                        已保存的模型 {composerOptions?.provider}/{composerOptions?.model} 不在当前目录中，请重新选择
                      </p>
                    )}
                    {beesAllowed &&
                      isMissingSelection(providers, composerOptions?.swarmBeesProvider, composerOptions?.swarmBeesModel) && (
                        <p className="text-xs text-[var(--danger-foreground)]">
                          已保存的模型 {composerOptions?.swarmBeesProvider}/{composerOptions?.swarmBeesModel} 不在当前目录中，请重新选择
                        </p>
                      )}

                    {swarmTemplateHeader}

                    <ModelSummaryRow
                      label="主代理"
                      value={leadModelShortName || '未选择'}
                      title={model?.name}
                      detail={composerOptions?.effort || '默认档位'}
                      onClick={() => {
                        setModelSheetPage('lead');
                        setModelQuery('');
                      }}
                    />

                    <div className="model-picker-footer space-y-3">
                      <EffortTrack
                        efforts={model?.efforts ?? []}
                        value={composerOptions?.effort ?? ''}
                        defaultLabel="默认"
                        displayValue={
                          composerOptions?.effort ? undefined : `默认（${model?.preferredEffort ?? 'CLI'}）`
                        }
                        onChange={selectEffort}
                      />
                      {activePair?.leadEffort &&
                        composerOptions?.effort &&
                        activePair.leadEffort !== composerOptions.effort && (
                          <p className="text-xs text-[var(--foreground-muted)]">
                            已偏离组合预设档位（预设：{activePair.leadEffort}）
                          </p>
                        )}
                      {model?.supportsFast && (
                        <div className="flex items-center justify-between gap-3">
                          <div className="min-w-0">
                            <div className="text-sm font-semibold text-[var(--foreground)]">Fast 模式</div>
                            <div className="text-xs text-[var(--foreground-muted)]">更快的输出速度，可能消耗更多额度</div>
                          </div>
                          <button
                            type="button"
                            role="switch"
                            aria-checked={fastMode}
                            onClick={() => onComposerOptionsChange?.({ ...composerOptions, fastMode: !fastMode })}
                            className={`relative h-6 w-11 shrink-0 rounded-full transition ${fastMode ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'}`}
                          >
                            <span className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform ${fastMode ? 'translate-x-5' : ''}`} />
                          </button>
                        </div>
                      )}
                    </div>

                    {beesAllowed && (
                      <>
                        <ModelSummaryRow
                          label="子代理"
                          value={beesModelShortName || '跟随主代理'}
                          title={beesModelName ?? undefined}
                          detail={beesProvider?.name}
                          onClick={() => {
                            setModelSheetPage('bees');
                            setModelQuery('');
                          }}
                        />

                        <div className="space-y-3">
                          {beesModel && beesModel.efforts.length > 0 ? (
                            <EffortTrack
                              efforts={beesModel.efforts}
                              value={composerOptions?.swarmBeesEffort ?? ''}
                              defaultLabel="默认"
                              onChange={selectBeesEffort}
                            />
                          ) : (
                            <p className="text-xs text-[var(--foreground-muted)]">
                              {composerOptions?.swarmBeesProvider
                                ? '该模型无思考档位选项'
                                : '子代理跟随主代理的模型与档位'}
                            </p>
                          )}
                        </div>
                      </>
                    )}

                    {(onToggleSwarm || personaSupported || tiersSectionVisible) && (
                      <div>
                        <div className="text-mc-caption font-semibold text-[var(--foreground-muted)]">
                          更多设置
                        </div>

                        {onToggleSwarm && (
                          <div className="mt-1.5 flex items-center justify-between gap-3 border-t border-[var(--border)] py-3">
                            <div className="min-w-0">
                              <div className="text-sm font-semibold text-[var(--foreground)]">编排</div>
                              <div className="text-xs text-[var(--foreground-muted)]">
                                {swarm && !swarm.enabled ? '请先在 Mac 设置里开启编排' : '主代理派出多个子代理并行工作'}
                              </div>
                            </div>
                            <button
                              type="button"
                              role="switch"
                              aria-checked={swarmOn}
                              disabled={swarmToggleDisabled || (swarm ? !swarm.enabled : false)}
                              onClick={() => onToggleSwarm(!swarmOn)}
                              className={`relative h-6 w-11 shrink-0 rounded-full transition disabled:opacity-40 ${swarmOn ? 'bg-[var(--primary)]' : 'bg-[var(--border)]'}`}
                            >
                              <span className={`absolute left-0.5 top-0.5 h-5 w-5 rounded-full bg-white transition-transform ${swarmOn ? 'translate-x-5' : ''}`} />
                            </button>
                          </div>
                        )}

                        {personaSupported && (
                          <div className="border-t border-[var(--border)]">
                            <PersonaSection
                              threadID={threadID}
                              personas={personaOptions}
                              saving={personaSwitching}
                              message={personaMessage}
                              onSelect={selectPersona}
                            />
                          </div>
                        )}

                        {tiersSectionVisible && (
                          <div className="border-t border-[var(--border)]">
                            <button
                              type="button"
                              aria-expanded={tierPanelOpen}
                              onClick={() => setTierPanelOpen((previous) => !previous)}
                              className="w-full min-h-[44px] flex items-center justify-between gap-2 py-3 text-left"
                            >
                              <span className="min-w-0">
                                <span className="block text-sm font-semibold text-[var(--foreground)]">子代理档位</span>
                                <span className="block text-xs text-[var(--foreground-muted)]">
                                  本会话派出子代理时用哪套 provider / model
                                </span>
                              </span>
                              <span className="flex shrink-0 items-center gap-1 text-mc-caption text-[var(--foreground-muted)]">
                                {overrideCount(tierDraft) > 0
                                  ? `${overrideCount(tierDraft)} 个档位已覆盖`
                                  : '跟随全局'}
                                <ChevronDownIcon
                                  className={`h-3.5 w-3.5 transition-transform ${tierPanelOpen ? 'rotate-180' : ''}`}
                                />
                              </span>
                            </button>

                            {tierPanelOpen && (
                              <div className="pb-3 pl-3 space-y-2">
                                {isLoadingTiers && (
                                  <p className="text-mc-caption text-[var(--foreground-muted)]">正在读取档位…</p>
                                )}
                                {!isLoadingTiers && tiers && tiers.length === 0 && (
                                  <p className="text-mc-caption text-[var(--foreground-muted)]">
                                    宿主没有可用的子代理档位
                                  </p>
                                )}
                                {tiers && tiers.length > 0 && (
                                  <div className="space-y-2 max-h-[46dvh] overflow-y-auto pr-0.5">
                                    {tiers.map((tier) => {
                                      const override = tierDraft[tier.id];
                                      const tierLabel = tier.name || tier.id;
                                      const providerValue =
                                        override && typeof override.provider === 'string' ? override.provider : '';
                                      const modelValue =
                                        override && typeof override.model === 'string' ? override.model : '';
                                      // 只列设置里没关掉的 provider，但当前覆盖里的那一个必须留在列表顶部可见
                                      const providerOptions = providers.filter((candidate) =>
                                        providerVisible(candidate, providerValue)
                                      );
                                      const providerInList = providerOptions.some(
                                        (candidate) => candidate.id === providerValue
                                      );
                                      const selectedProvider = providers.find(
                                        (candidate) => candidate.id === providerValue
                                      );
                                      const modelOptions = selectedProvider?.models ?? [];
                                      const modelInList = modelOptions.some((candidate) => candidate.id === modelValue);
                                      return (
                                        <div
                                          key={tier.id}
                                          className="p-2.5 rounded-xl border border-[var(--border)] bg-[var(--card)] space-y-1.5"
                                        >
                                          <div className="flex items-center justify-between gap-2">
                                            <span className="min-w-0 truncate text-mc-caption font-semibold text-[var(--foreground)]">
                                              {tierLabel}
                                            </span>
                                            {override ? (
                                              <button
                                                type="button"
                                                disabled={isSavingTiers}
                                                onClick={() => resetTierDraft(tier.id)}
                                                className="shrink-0 text-mc-caption font-medium text-[var(--primary)] disabled:opacity-50"
                                              >
                                                恢复默认
                                              </button>
                                            ) : (
                                              <span className="shrink-0 text-mc-caption text-[var(--foreground-muted)]">
                                                默认
                                              </span>
                                            )}
                                          </div>
                                          <div className="grid grid-cols-2 gap-1.5">
                                            <select
                                              value={providerValue}
                                              onChange={(event) => applyTierProvider(tier.id, event.target.value)}
                                              aria-label={`${tierLabel} 的 provider 覆盖`}
                                              disabled={isSavingTiers}
                                              className={TIER_SELECT_CLASS}
                                            >
                                              <option value="">跟随默认</option>
                                              {providerOptions.map((candidate) => (
                                                <option
                                                  key={candidate.id}
                                                  value={candidate.id}
                                                  disabled={!candidate.available}
                                                >
                                                  {candidate.available
                                                    ? candidate.name || candidate.id
                                                    : `${candidate.name || candidate.id}（不可用）`}
                                                </option>
                                              ))}
                                              {/* 旧数据或自定义 provider 不在列表里时保留原值，避免一打开面板就被改掉 */}
                                              {providerValue && !providerInList && (
                                                <option value={providerValue}>{`${providerValue}（不在列表中）`}</option>
                                              )}
                                            </select>
                                            <select
                                              value={modelValue}
                                              onChange={(event) =>
                                                updateTierDraft(tier.id, 'model', event.target.value)
                                              }
                                              aria-label={`${tierLabel} 的 model 覆盖`}
                                              disabled={isSavingTiers}
                                              className={TIER_SELECT_CLASS}
                                            >
                                              <option value="">该 provider 默认</option>
                                              {modelOptions.map((candidate) => (
                                                <option key={candidate.id} value={candidate.id}>
                                                  {candidate.name || candidate.id}
                                                </option>
                                              ))}
                                              {modelValue && !modelInList && (
                                                <option value={modelValue}>{`${modelValue}（不在列表中）`}</option>
                                              )}
                                            </select>
                                          </div>
                                        </div>
                                      );
                                    })}
                                  </div>
                                )}

                                <div className="flex items-center justify-between gap-2">
                                  <span className="min-w-0 text-mc-caption text-[var(--foreground-muted)]">
                                    {tierNotice ||
                                      (tierDirty ? '有未保存的改动' : '留空的档位跟随全局档位配置')}
                                  </span>
                                  <button
                                    type="button"
                                    onClick={() => void handleSaveTiers()}
                                    disabled={isSavingTiers || !tierDirty}
                                    className="min-h-[36px] shrink-0 px-3 rounded-xl text-xs font-semibold bg-[var(--primary)] text-white disabled:opacity-40 transition"
                                  >
                                    {isSavingTiers ? '保存中…' : '保存'}
                                  </button>
                                </div>

                                {tierError && (
                                  <p className="text-mc-caption text-[var(--danger-foreground)]" role="alert">
                                    {tierError}
                                  </p>
                                )}
                              </div>
                            )}
                          </div>
                        )}
                      </div>
                    )}

                  </div>
                ) : (
                  <div className="space-y-3">
                    <button
                      type="button"
                      onClick={() => {
                        setModelSheetPage('summary');
                        setModelQuery('');
                      }}
                      className="flex min-h-[36px] items-center gap-1 rounded-lg px-1 text-sm font-semibold text-[var(--foreground)] hover:text-[var(--primary)]"
                    >
                      <ChevronRightIcon className="h-4 w-4 rotate-180 text-[var(--foreground-muted)]" />
                      {modelSheetPage === 'bees' ? '子代理' : '主代理'}
                    </button>

                    {modelSheetPage === 'bees' ? (
                      <ModelPickerPanel
                        sections={pickerSections}
                        query={modelQuery}
                        onQueryChange={setModelQuery}
                        selectedKey={
                          composerOptions?.swarmBeesProvider && composerOptions?.swarmBeesModel
                            ? `${composerOptions.swarmBeesProvider}/${composerOptions.swarmBeesModel}`
                            : null
                        }
                        onSelect={(providerID, modelID) => {
                          selectBees(providerID, modelID);
                          setModelSheetPage('summary');
                        }}
                        header={followLeadHeader}
                        notice={
                          isMissingSelection(providers, composerOptions?.swarmBeesProvider, composerOptions?.swarmBeesModel) ? (
                            <p className="text-xs text-[var(--danger-foreground)]">
                              已保存的模型 {composerOptions?.swarmBeesProvider}/{composerOptions?.swarmBeesModel} 不在当前目录中，请重新选择
                            </p>
                          ) : undefined
                        }
                        autoFocusSearch={popoverAnchor !== null}
                      />
                    ) : (
                      <ModelPickerPanel
                        sections={pickerSections}
                        query={modelQuery}
                        onQueryChange={setModelQuery}
                        selectedKey={
                          activePair || !provider || !model ? null : `${provider.id}/${model.id}`
                        }
                        onSelect={(providerID, modelID) => {
                          selectProviderAndModel(providerID, modelID);
                          setModelSheetPage('summary');
                        }}
                        notice={
                          isMissingSelection(providers, composerOptions?.provider, composerOptions?.model) ? (
                            <p className="text-xs text-[var(--danger-foreground)]">
                              已保存的模型 {composerOptions?.provider}/{composerOptions?.model} 不在当前目录中，请重新选择
                            </p>
                          ) : undefined
                        }
                        autoFocusSearch={popoverAnchor !== null}
                      />
                    )}
                  </div>
                )}
              </>
            )}

            {activeSheet !== 'model-effort' && (
            <div className="sheet-actions">
              {activeSheet === 'mode' && (
                <>
                  {!composerOptions?.interactionMode && (
                    <button
                      type="button"
                      className="sheet-action justify-between"
                      onClick={() => selectInteractionMode(undefined)}
                    >
                      <span className="text-sm text-[var(--foreground)]">默认模式</span>
                      <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
                    </button>
                  )}
                  <button
                    type="button"
                    className="sheet-action justify-between"
                    onClick={() => selectInteractionMode('build')}
                  >
                    <span className="text-sm text-[var(--foreground)]">执行 (Build)</span>
                    {composerOptions?.interactionMode === 'build' && (
                      <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
                    )}
                  </button>
                  <button
                    type="button"
                    className="sheet-action justify-between disabled:opacity-40"
                    disabled={provider?.supportsPlanMode === false}
                    onClick={() => selectInteractionMode('plan')}
                  >
                    <span className="text-sm text-[var(--foreground)]">
                      计划 (Plan){provider?.supportsPlanMode === false ? ' · 当前 Provider 不支持' : ''}
                    </span>
                    {composerOptions?.interactionMode === 'plan' && (
                      <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
                    )}
                  </button>
                </>
              )}

              {activeSheet === 'permission' && (
                <>
                  {[
                    ...(composerOptions?.runtimeMode ? [] : [{ id: undefined, label: '宿主默认' }]),
                    { id: 'supervised' as const, label: '逐项确认' },
                    { id: 'auto' as const, label: '自动' },
                    { id: 'fullAccess' as const, label: '完全访问' }
                  ].map((item) => (
                    <button
                      key={item.label}
                      type="button"
                      className="sheet-action justify-between"
                      onClick={() => selectRuntimeMode(item.id)}
                    >
                      <span className="text-sm text-[var(--foreground)]">{item.label}</span>
                      {(composerOptions?.runtimeMode === item.id || ((composerOptions?.runtimeMode as string | undefined) === 'autoAcceptEdits' && item.id === 'auto')) && (
                        <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
                      )}
                    </button>
                  ))}
                </>
              )}

              {activeSheet === 'project' &&
                projects?.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    className="sheet-action justify-between"
                    onClick={() => {
                      onSelectProject?.(p.id);
                      setActiveSheet(null);
                    }}
                  >
                    <span className="text-sm text-[var(--foreground)]">{p.name}</span>
                    {selectedProjectID === p.id && (
                      <CheckCircleIcon className="w-4 h-4 text-[var(--primary)]" />
                    )}
                  </button>
                ))}
            </div>
            )}
          </section>
        </div>
      )}
    </div>
  );
});

// memo 包在 forwardRef 外层：时间线「引用到回复」通过 ref 的 insertDraft 写入草稿。
export const ThreadComposer = React.memo(ThreadComposerInner);
