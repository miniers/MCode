import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  RemoteAttachmentUploadResult,
  RemoteComposerDraftDefaults,
  RemoteComposerOptions,
  RemoteProjectSummary
} from '../types/contract';
import {
  mergeComposerDefaults,
  mergeTouchedGroups,
  rememberParamsForChange,
  touchedComposerOptions
} from '../utils/composerOptions';
import type { ComposerSelectionMemoryParams, ComposerTouchedGroups } from '../utils/composerOptions';
import { useRemoteStatic } from '../context/RemoteContext';
import { isPreviewableImageFile } from '../context/remoteImages';
import type { RemoteThreadAction } from '../context/RemoteContext';
import { ChevronRightIcon, ExclamationTriangleIcon, XMarkIcon } from './Icons';
import { ThreadComposer } from './ThreadComposer';

interface ThreadDraftViewProps {
  project: RemoteProjectSummary;
  onBack?: () => void;
  onOpenThread: (threadID: string) => void;
}

const DRAFT_STORAGE_PREFIX = 'mcode.remote.draft.';

interface StoredDraft {
  text: string;
  // 只含非模型字段；模型类选择一律以宿主下发的 draft 为准，不落本机
  options?: RemoteComposerOptions;
}

// 允许随草稿留存的只是执行模式、权限、工作区这类本机偏好
function storedComposerOptions(options?: RemoteComposerOptions): RemoteComposerOptions {
  if (!options) return {};
  const stored: RemoteComposerOptions = {};
  if (options.runtimeMode !== undefined) stored.runtimeMode = options.runtimeMode;
  if (options.interactionMode !== undefined) stored.interactionMode = options.interactionMode;
  if (options.workspaceMode !== undefined) stored.workspaceMode = options.workspaceMode;
  return stored;
}

// 草稿存本机 localStorage；存取失败（隐私模式、配额）静默忽略，只是不保留草稿
// 旧数据里可能带着模型类字段，读取时直接丢弃，不报错
function loadStoredDraft(projectID: string): StoredDraft | null {
  try {
    const raw = window.localStorage.getItem(DRAFT_STORAGE_PREFIX + projectID);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as StoredDraft;
    if (typeof parsed?.text !== 'string') return null;
    const options = storedComposerOptions(parsed.options);
    return Object.keys(options).length > 0 ? { text: parsed.text, options } : { text: parsed.text };
  } catch {
    return null;
  }
}

function saveStoredDraft(projectID: string, value: StoredDraft | null) {
  try {
    if (value) window.localStorage.setItem(DRAFT_STORAGE_PREFIX + projectID, JSON.stringify(value));
    else window.localStorage.removeItem(DRAFT_STORAGE_PREFIX + projectID);
  } catch {
    // 忽略
  }
}

function draftDefaultsToOptions(defaults: RemoteComposerDraftDefaults): RemoteComposerOptions {
  return {
    provider: defaults.provider,
    model: defaults.model ?? undefined,
    effort: defaults.effort ?? undefined,
    fastMode: defaults.fastMode,
    runtimeMode: defaults.runtimeMode,
    interactionMode: defaults.interactionMode,
    workspaceMode: defaults.workspaceMode,
    swarmEnabled: defaults.swarmEnabled,
    swarmPairID: defaults.swarmPairID ?? undefined,
    swarmBeesProvider: defaults.swarmBeesProvider ?? undefined,
    swarmBeesModel: defaults.swarmBeesModel ?? undefined,
    swarmBeesEffort: defaults.swarmBeesEffort ?? undefined
  };
}

function isUnresolved(action?: RemoteThreadAction): boolean {
  return Boolean(action && (action.state === 'submitting' || action.state === 'pending' || action.state === 'unknown'));
}

export const ThreadDraftView: React.FC<ThreadDraftViewProps> = ({ project, onBack, onOpenThread }) => {
  const {
    connectionState,
    isSyncing,
    capabilities,
    providers,
    providersLoading,
    providersError,
    projects,
    threadActions,
    createThread,
    listProviders,
    rememberProviderSelection,
    sendThreadMessage,
    checkThreadAction,
    loadThreads,
    openThread,
    uploadAttachment
  } = useRemoteStatic();
  const isConnected = connectionState === 'connected';
  const [currentProject, setCurrentProject] = useState<RemoteProjectSummary>(project);
  const composerOptionsSupported = Boolean(capabilities?.features?.includes('threads.composerOptions'));
  const attachmentsSupported = Boolean(
    capabilities?.features?.includes('threads.attachments') &&
    capabilities.methods.includes('attachments.upload')
  );
  const canCreate = Boolean(
    capabilities?.methods.includes('threads.create') && capabilities.methods.includes('threads.actionStatus')
  );
  const canSend = Boolean(
    capabilities?.methods.includes('threads.sendMessage') && capabilities.methods.includes('threads.actionStatus')
  );

  const [draft, setDraft] = useState(() => loadStoredDraft(project.id)?.text ?? '');
  const [composerOptions, setComposerOptions] = useState<RemoteComposerOptions>(
    () => loadStoredDraft(project.id)?.options ?? {}
  );
  // Mac 上“在该项目新建会话”会得到的默认值（含桌面端记忆的模型选择）；模型类字段永远从这里取
  const [draftBase, setDraftBase] = useState<RemoteComposerOptions | null>(null);
  const optionsDirtyRef = useRef(Boolean(loadStoredDraft(project.id)?.options));
  // 用户在本草稿里动过的记忆分组：只有动过的组不再跟随宿主默认值
  const touchedGroupsRef = useRef<ComposerTouchedGroups>({ lead: false, bees: false, swarm: false });
  const composerOptionsRef = useRef(composerOptions);
  const rememberSelectionSupported = Boolean(capabilities?.methods.includes('providers.rememberSelection'));

  /** 把宿主下发的 draft 合并进当前选择：没动过的记忆组跟随新默认值，动过的保留用户选择。 */
  const applyDraftDefaults = useCallback((defaults: RemoteComposerOptions) => {
    const merged = mergeComposerDefaults(
      composerOptionsRef.current,
      defaults,
      touchedGroupsRef.current,
      optionsDirtyRef.current
    );
    composerOptionsRef.current = merged;
    setDraftBase(defaults);
    setComposerOptions(merged);
  }, []);

  /** 只有用户主动改动才写宿主记忆；失败只记日志，不打扰用户。 */
  const rememberSelection = useCallback((params: ComposerSelectionMemoryParams) => {
    if (!rememberSelectionSupported) return;
    void rememberProviderSelection(params)
      .then((result) => {
        // 返回的 draft 是最新默认值，按同样规则合并
        if (result.draft) applyDraftDefaults(draftDefaultsToOptions(result.draft));
      })
      .catch((error) => {
        console.warn('同步模型选择记忆失败', error);
      });
  }, [applyDraftDefaults, rememberProviderSelection, rememberSelectionSupported]);

  const updateComposerOptions = useCallback(
    (next: RemoteComposerOptions | ((previous: RemoteComposerOptions) => RemoteComposerOptions)) => {
      const previous = composerOptionsRef.current;
      const resolved = typeof next === 'function' ? next(previous) : next;
      optionsDirtyRef.current = true;
      touchedGroupsRef.current = mergeTouchedGroups(previous, resolved, touchedGroupsRef.current);
      composerOptionsRef.current = resolved;
      setComposerOptions(resolved);
      // 用户交互触发的变更才写记忆；同步宿主数据不会走到这里
      const params = rememberParamsForChange(previous, resolved, currentProject.id);
      if (params) rememberSelection(params);
    },
    [currentProject.id, rememberSelection]
  );
  const [attachments, setAttachments] = useState<RemoteAttachmentUploadResult[]>([]);
  /** 输入区的图片缩略图：附件上传结果里没有可直接渲染的地址，用本地 object URL 兜底 */
  const [attachmentPreviews, setAttachmentPreviews] = useState<Record<string, string>>({});
  const attachmentPreviewURLsRef = useRef<Record<string, string>>({});
  const [isUploadingAttachment, setIsUploadingAttachment] = useState(false);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [attachmentError, setAttachmentError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [createActionID, setCreateActionID] = useState<string | null>(null);
  const [createdThreadID, setCreatedThreadID] = useState<string | null>(null);
  const [sendActionID, setSendActionID] = useState<string | null>(null);
  const [checkingActionID, setCheckingActionID] = useState<string | null>(null);
  const [workspaceSheetOpen, setWorkspaceSheetOpen] = useState(false);

  const attachmentsRef = useRef(attachments);
  const draftRef = useRef(draft);
  const submitInFlightRef = useRef(false);

  attachmentsRef.current = attachments;
  draftRef.current = draft;

  useEffect(() => {
    if (!composerOptionsSupported || !isConnected || !capabilities?.methods.includes('providers.list')) return;
    let cancelled = false;
    const loadDefaults = () => {
      void listProviders(currentProject.id)
        .then((result) => {
          if (cancelled || !result.draft) return;
          applyDraftDefaults(draftDefaultsToOptions(result.draft));
        })
        .catch(() => {
          // Provider 目录读取失败由 providersError 提示，草稿沿用宿主默认
        });
    };
    loadDefaults();
    // 回到页面（切回标签页 / 窗口重新获得焦点）时重新取默认值：桌面端可能刚改过记忆
    const handleVisibilityChange = () => {
      if (document.visibilityState === 'visible') loadDefaults();
    };
    document.addEventListener('visibilitychange', handleVisibilityChange);
    window.addEventListener('focus', loadDefaults);
    return () => {
      cancelled = true;
      document.removeEventListener('visibilitychange', handleVisibilityChange);
      window.removeEventListener('focus', loadDefaults);
    };
  }, [applyDraftDefaults, capabilities, composerOptionsSupported, currentProject.id, isConnected, listProviders]);

  // 草稿正文与非模型字段按项目存到本机，返回或刷新后还在；发送成功后清除
  useEffect(() => {
    if (createdThreadID) return;
    const hasText = draft.trim().length > 0;
    const storedOptions = optionsDirtyRef.current ? storedComposerOptions(composerOptions) : {};
    if (!hasText && Object.keys(storedOptions).length === 0) {
      saveStoredDraft(currentProject.id, null);
      return;
    }
    saveStoredDraft(currentProject.id, {
      text: draft,
      options: Object.keys(storedOptions).length > 0 ? storedOptions : undefined
    });
  }, [composerOptions, createdThreadID, currentProject.id, draft]);

  const createAction = useMemo(
    () => createActionID ? threadActions.find((action) => action.actionID === createActionID) : undefined,
    [createActionID, threadActions]
  );
  const sendAction = useMemo(
    () => sendActionID ? threadActions.find((action) => action.actionID === sendActionID) : undefined,
    [sendActionID, threadActions]
  );

  const setAttachmentPreview = useCallback((attachmentID: string, url: string) => {
    attachmentPreviewURLsRef.current = { ...attachmentPreviewURLsRef.current, [attachmentID]: url };
    setAttachmentPreviews(attachmentPreviewURLsRef.current);
  }, []);

  const releaseAttachmentPreviews = useCallback((attachmentIDs: string[]) => {
    const next = { ...attachmentPreviewURLsRef.current };
    for (const attachmentID of attachmentIDs) {
      const url = next[attachmentID];
      if (!url) continue;
      URL.revokeObjectURL(url);
      delete next[attachmentID];
    }
    attachmentPreviewURLsRef.current = next;
    setAttachmentPreviews(next);
  }, []);

  // 离开草稿页（或组件重建）时释放所有本地缩略图
  useEffect(() => () => {
    Object.values(attachmentPreviewURLsRef.current).forEach((url) => URL.revokeObjectURL(url));
    attachmentPreviewURLsRef.current = {};
  }, []);

  const handleAddAttachments = useCallback(async (files: FileList | null) => {
    if (!attachmentsSupported || !files || files.length === 0 || isUploadingAttachment) return;
    const incoming = Array.from(files);
    if (attachmentsRef.current.length + incoming.length > 4) {
      setAttachmentError('每条消息最多添加 4 个附件');
      return;
    }
    setAttachmentError(null);
    setIsUploadingAttachment(true);
    const uploaded: RemoteAttachmentUploadResult[] = [];
    try {
      let totalBytes = attachmentsRef.current.reduce((sum, attachment) => sum + attachment.byteCount, 0);
      for (const file of incoming) {
        const result = await uploadAttachment(file);
        totalBytes += result.byteCount;
        if (totalBytes > 8 * 1024 * 1024) throw new Error('文件总大小不能超过 8 MiB');
        if (isPreviewableImageFile(file)) {
          setAttachmentPreview(result.attachmentID, URL.createObjectURL(file));
        }
        uploaded.push(result);
      }
      setAttachments((previous) => [...previous, ...uploaded]);
    } catch (error) {
      releaseAttachmentPreviews(uploaded.map((attachment) => attachment.attachmentID));
      setAttachmentError(error instanceof Error ? error.message : '附件上传失败');
    } finally {
      setIsUploadingAttachment(false);
    }
  }, [attachmentsSupported, isUploadingAttachment, releaseAttachmentPreviews, setAttachmentPreview, uploadAttachment]);

  const removeAttachment = useCallback((attachmentID: string) => {
    setAttachments((previous) => previous.filter((attachment) => attachment.attachmentID !== attachmentID));
    releaseAttachmentPreviews([attachmentID]);
    setAttachmentError(null);
  }, [releaseAttachmentPreviews]);

  const sendCreatedThread = useCallback(async (threadID: string, text: string, uploadedAttachments: RemoteAttachmentUploadResult[]) => {
    await loadThreads(currentProject.id);
    openThread(threadID);
    // 会话已按 options 创建，这条消息不再带选择
    const params: Parameters<typeof sendThreadMessage>[2] = {};
    if (attachmentsSupported) params.attachments = uploadedAttachments.map((attachment) => attachment.attachmentID);
    const action = await sendThreadMessage(threadID, text, params);
    setSendActionID(action.actionID);
    if (action.state === 'accepted') {
      saveStoredDraft(currentProject.id, null);
      setDraft('');
      releaseAttachmentPreviews(attachmentsRef.current.map((attachment) => attachment.attachmentID));
      setAttachments([]);
      onOpenThread(threadID);
    } else if (action.state === 'failed') {
      setActionError(action.error || '发送失败，草稿已保留');
    }
  }, [attachmentsSupported, currentProject.id, loadThreads, onOpenThread, openThread, releaseAttachmentPreviews, sendThreadMessage]);

  const handleSend = useCallback(async () => {
    const text = draftRef.current;
    const uploadedAttachments = attachmentsRef.current;
    if (
      submitInFlightRef.current ||
      (!text.trim() && uploadedAttachments.length === 0) ||
      !isConnected ||
      isSyncing ||
      isUploadingAttachment ||
      !canCreate ||
      !canSend ||
      isUnresolved(createAction) ||
      isUnresolved(sendAction)
    ) return;

    submitInFlightRef.current = true;
    setIsSubmitting(true);
    setActionError(null);
    try {
      let threadID = createdThreadID;
      if (!threadID) {
        // 只发用户动过的记忆组（整组下发）；没动过的组省略，交给服务端按最新记忆补齐
        const options = touchedComposerOptions(composerOptionsRef.current, touchedGroupsRef.current, draftBase);
        const action = await createThread(
          currentProject.id,
          composerOptionsSupported && Object.keys(options).length > 0 ? options : undefined
        );
        setCreateActionID(action.actionID);
        if (action.state !== 'accepted' || !action.threadID) {
          if (action.state === 'failed') setActionError(action.error || '创建会话失败，草稿已保留');
          return;
        }
        threadID = action.threadID;
        setCreatedThreadID(threadID);
      }
      await sendCreatedThread(threadID, text, uploadedAttachments);
    } catch (error) {
      setActionError(error instanceof Error ? error.message : '无法创建或发送会话，草稿已保留');
    } finally {
      submitInFlightRef.current = false;
      setIsSubmitting(false);
    }
  }, [canCreate, canSend, composerOptionsSupported, createAction, createThread, draftBase, createdThreadID, currentProject.id, isConnected, isSyncing, isUploadingAttachment, sendAction, sendCreatedThread]);

  const handleCheckAction = useCallback(async (action: RemoteThreadAction) => {
    if (!isConnected || checkingActionID) return;
    setCheckingActionID(action.actionID);
    setActionError(null);
    try {
      const updated = await checkThreadAction(action.actionID);
      if (updated.kind === 'create' && updated.state === 'accepted' && updated.threadID) {
        setCreatedThreadID(updated.threadID);
        setCreateActionID(updated.actionID);
      } else if (updated.kind === 'send' && updated.state === 'accepted' && updated.threadID) {
        saveStoredDraft(currentProject.id, null);
        setDraft('');
        releaseAttachmentPreviews(attachmentsRef.current.map((attachment) => attachment.attachmentID));
        setAttachments([]);
        onOpenThread(updated.threadID);
      } else if (updated.state === 'failed') {
        setActionError(updated.error || '动作未执行或已结算失败，草稿已保留');
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : '查询动作结果失败');
    } finally {
      setCheckingActionID(null);
    }
  }, [checkThreadAction, checkingActionID, currentProject.id, isConnected, onOpenThread, releaseAttachmentPreviews]);

  const createNotice = createAction?.state === 'pending' || createAction?.state === 'unknown'
    ? '创建结果待确认；确认前不会重复创建会话。'
    : createAction?.state === 'accepted' && !createdThreadID
    ? '会话已创建，正在同步。'
    : null;
  const sendNotice = sendAction && isUnresolved(sendAction)
    ? '发送结果待确认；请查询结果，不会自动重放。'
    : null;
  const blockedReason = !isConnected
    ? '当前未连接到 Mac，发送已暂停'
    : isSyncing
    ? '正在同步最新状态，请稍候'
    : !canCreate || !canSend
    ? '当前 Mac 未声明完整的新建会话能力'
    : isUploadingAttachment
    ? '正在上传图片'
    : createNotice || sendNotice
    ? createNotice || sendNotice
    : null;

  const quotaScope = useMemo(() => ({ projectID: currentProject.id }), [currentProject.id]);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
      <header
        className="flex shrink-0 items-center gap-2 border-b border-[var(--border)] bg-[var(--card)]"
        style={{
          paddingTop: 'calc(var(--sat) + 0.5rem)',
          paddingBottom: '0.5rem',
          paddingLeft: 'max(0.75rem, calc(0.75rem + var(--sal)))',
          paddingRight: 'max(0.75rem, calc(0.75rem + var(--sar)))'
        }}
      >
        {onBack && (
          <button
            type="button"
            onClick={onBack}
            aria-label="返回会话列表"
            className="min-h-[36px] min-w-[36px] inline-flex items-center justify-center rounded-xl text-[var(--foreground-muted)] hover:text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] transition"
          >
            <ChevronRightIcon className="h-5 w-5 rotate-180" />
          </button>
        )}
        <div className="min-w-0 flex-1">
          <div className="truncate text-[16px] font-semibold text-[var(--foreground)]">新会话草稿</div>
          <div className="truncate text-xs text-[var(--foreground-muted)]">{currentProject.name}</div>
        </div>
        <button
          type="button"
          onClick={() => setWorkspaceSheetOpen(true)}
          className="shrink-0 inline-flex items-center gap-1 px-2.5 py-1 rounded-xl text-xs font-medium border border-[var(--border)] bg-[var(--user-bubble)] text-[var(--foreground)] hover:bg-[var(--card-alt,var(--card))] transition active:scale-95"
          aria-label="选择工作区"
        >
          <span>工作区：{composerOptions.workspaceMode === 'worktree' ? 'Worktree' : composerOptions.workspaceMode === 'local' ? '本地' : '默认'}</span>
        </button>
      </header>

      <div className="flex flex-1 min-h-0 items-center justify-center overflow-y-auto p-4">
        <div className="w-full max-w-xl space-y-3">
          <div className="rounded-2xl border border-[var(--border)] bg-[var(--card)] p-4 shadow-sm text-[var(--foreground)]">
            <h1 className="text-base font-semibold text-[var(--foreground)]">在 {currentProject.name} 开始对话</h1>
            <p className="mt-1 text-xs text-[var(--foreground-muted)]">点击发送后才会在 Mac 创建会话。</p>
            {providersLoading && <p className="mt-3 text-xs text-[var(--foreground-muted)]">正在读取 Provider 目录…</p>}
            {providersError && <p className="mt-3 text-xs text-[var(--warning-foreground)]">Provider 目录读取失败，将使用宿主默认设置。</p>}
          </div>
          {(createNotice || sendNotice || actionError) && (
            <div className="rounded-xl border border-[var(--border)] bg-[var(--card)] px-3 py-2 text-xs text-[var(--warning-foreground)]" role="status">
              <div className="flex items-start gap-2">
                <ExclamationTriangleIcon className="mt-0.5 h-4 w-4 shrink-0" />
                <span>{actionError || createNotice || sendNotice}</span>
              </div>
              {createAction && isUnresolved(createAction) && (
                <button
                  type="button"
                  onClick={() => void handleCheckAction(createAction)}
                  disabled={checkingActionID === createAction.actionID}
                  className="mt-2 min-h-[32px] rounded-lg border border-[var(--border)] px-2.5 text-mc-caption font-medium disabled:opacity-45"
                >
                  {checkingActionID === createAction.actionID ? '查询中…' : '查询创建结果'}
                </button>
              )}
              {sendAction && isUnresolved(sendAction) && (
                <button
                  type="button"
                  onClick={() => void handleCheckAction(sendAction)}
                  disabled={checkingActionID === sendAction.actionID}
                  className="mt-2 min-h-[32px] rounded-lg border border-[var(--border)] px-2.5 text-mc-caption font-medium disabled:opacity-45"
                >
                  {checkingActionID === sendAction.actionID ? '查询中…' : '查询发送结果'}
                </button>
              )}
            </div>
          )}
        </div>
      </div>

      <ThreadComposer
        draft={draft}
        isSending={isSubmitting}
        sendError={attachmentError || actionError}
        sendBlockedReason={blockedReason}
        isPolling={false}
        sendDisabled={Boolean(blockedReason) || isSubmitting || (!draft.trim() && attachments.length === 0)}
        attachments={attachments}
        attachmentPreviews={attachmentPreviews}
        isUploadingAttachment={isUploadingAttachment}
        submitLabel="发送并创建"
        quotaScope={quotaScope}
        composerOptions={composerOptionsSupported ? composerOptions : undefined}
        providers={providers}
        // 草稿阶段会话未创建：`/` 命令按 项目 + 所选 Provider 取
        commandScope={composerOptions.provider
          ? { projectID: currentProject.id, provider: composerOptions.provider }
          : undefined}
        composerOptionsDisabled={isSubmitting || Boolean(createdThreadID)}
        composerOptionsMessage={!composerOptionsSupported ? '当前宿主未声明 composer options，将使用 Mac 默认设置。' : null}
        attachmentsSupported={attachmentsSupported}
        onComposerOptionsChange={composerOptionsSupported ? updateComposerOptions : undefined}
        swarmEnabled={composerOptions.swarmEnabled ?? false}
        swarmToggleDisabled={isSubmitting || Boolean(createdThreadID)}
        onToggleSwarm={composerOptionsSupported
          ? (on) => updateComposerOptions((prev) => ({
              ...prev,
              swarmEnabled: on,
              // 关闭编排同时退出组合，否则宿主会因组合而重新开启
              swarmPairID: on ? prev.swarmPairID : undefined
            }))
          : undefined}
        onDraftChange={setDraft}
        onAddAttachments={(files) => void handleAddAttachments(files)}
        onRemoveAttachment={removeAttachment}
        onSend={() => void handleSend()}
        projects={projects}
        selectedProjectID={currentProject.id}
        onSelectProject={(id) => {
          const found = projects?.find((p) => p.id === id);
          if (found) setCurrentProject(found);
        }}
      />

      {/* 工作区选择面板 */}
      {workspaceSheetOpen && (
        <div
          className="sheet-layer"
          role="presentation"
          onMouseDown={() => setWorkspaceSheetOpen(false)}
        >
          <section
            className="settings-sheet"
            role="dialog"
            aria-modal="true"
            aria-labelledby="draft-workspace-title"
            onMouseDown={(event) => event.stopPropagation()}
          >
            <div className="sheet-handle" aria-hidden="true" />
            <div className="flex items-start justify-between gap-3 mb-4">
              <div className="min-w-0">
                <p className="eyebrow">设置</p>
                <h2 id="draft-workspace-title" className="sheet-title">工作区模式</h2>
              </div>
              <button
                type="button"
                className="icon-button"
                onClick={() => setWorkspaceSheetOpen(false)}
                aria-label="关闭"
              >
                <XMarkIcon className="h-4 w-4" />
              </button>
            </div>
            <p className="text-xs text-[var(--foreground-muted)] mb-3">
              选择新会话创建时使用的工作区环境。
            </p>
            <div className="sheet-actions">
              {(
                [
                  { mode: 'local', label: '本地 (Local)', desc: '直接在当前本地代码树中操作' },
                  { mode: 'worktree', label: 'Worktree', desc: '使用独立的 Git 工作树隔离变更' }
                ] as const
              ).map(({ mode, label, desc }) => {
                const isSelected = composerOptions.workspaceMode === mode;
                return (
                  <button
                    key={mode}
                    type="button"
                    className="sheet-action flex items-center justify-between"
                    onClick={() => {
                      updateComposerOptions((prev) => ({ ...prev, workspaceMode: mode }));
                      setWorkspaceSheetOpen(false);
                    }}
                  >
                    <div className="min-w-0 text-left">
                      <div className={`text-sm ${isSelected ? 'font-semibold text-[var(--primary)]' : 'text-[var(--foreground)]'}`}>
                        {label}
                      </div>
                      <div className="text-mc-caption text-[var(--foreground-muted)] mt-0.5">
                        {desc}
                      </div>
                    </div>
                    {isSelected && (
                      <span className="text-[var(--primary)] font-bold text-sm shrink-0 ml-3">
                        ✓
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          </section>
        </div>
      )}
    </div>
  );
};
