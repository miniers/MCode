// 会话操作菜单 sheet：一个页面栈容器——快捷操作与分组是栈底主页，
// 子代理、审阅、Git、脚本、文件、MCP 服务器、worktree、看板任务、转发，
// 以及「编排」「工作区」「导出会话」都是栈内页面。
// 整个栈只有一层遮罩：窄屏是底部抽屉，宽屏主页是锚定在 ⋯ 按钮下的 popover、详情页是右侧栏。

import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { RemoteComposerOptions, RemoteThreadSummary } from '../../types/contract';
import type { RemoteSwarmState } from '../../types/swarm';
import { useRemoteStatic } from '../../context/RemoteContext';
import { makeActionID } from '../../context/remoteGuards';
import { useThreadExportHistory } from '../../context/remoteThreadExportApi';
import {
  REMOTE_MCP_SERVERS_LIST_METHOD,
  REMOTE_THREADS_MCP_DISABLED_SET_METHOD
} from '../../context/remoteMCPServersApi';
// 导出渲染器按需加载：只有点「导出会话」或「复制为 Markdown」时才拉这一份体积较大的模块
import type { ThreadExportFormat } from '../../utils/threadExport';
import type { ThreadSwarmMode } from './useThreadMetaActions';
import { useRegenerateThreadTitle, useRemoteMethodSupport } from './useThreadMetaActions';
import { WorktreeMoveSheetBody } from './WorktreeMoveSheet';
import { ThreadMCPServersSheetBody } from './ThreadMCPServersSheet';
import { SheetFrame } from '../sheet/SheetFrame';
import { MenuGroup, MenuRow } from './menu/MenuRow';
import { MenuSheetHeader } from './menu/MenuSheetHeader';
import { MenuStackView } from './menu/MenuStackView';
import { MenuPageChromeContext } from './menu/menuPageChrome';
import type { MenuPageChrome } from './menu/menuPageChrome';
import {
  MENU_PAGE_TITLES,
  isRoutablePage,
  pageSizeFor,
  routeForPage
} from './menu/menuPages';
import type {
  ThreadMenuPage,
  ThreadMenuPageHandle,
  ThreadMenuPageKind,
  ThreadMenuPageSpec
} from './menu/menuPages';
import { sheetSurfaceFor, useIsWideSheet } from './menu/useIsWideSheet';
import { useMenuPageStack } from './menu/useMenuPageStack';
import { routeAfterPop, shouldIgnoreMenuEscape } from './menu/menuStackActions';
import { ThreadMenuSwarmPanel } from './menu/ThreadMenuSwarmPanel';
import { ThreadMenuWorkspacePanel } from './menu/ThreadMenuWorkspacePanel';
import { ThreadMenuExportPanel } from './menu/ThreadMenuExportPanel';
import {
  ArchiveBoxIcon, ArrowDownIcon, ArrowPathIcon, ArrowUturnRightIcon, CodeBracketIcon,
  DocumentTextIcon, EyeIcon, FolderIcon, GitBranchIcon, LayoutIcon, ListBulletIcon,
  MoonIcon, PencilIcon, PinIcon, PlayIcon, ServerIcon, SparklesIcon
} from '../Icons';

// 会话级编排模式在主页那一行上的短标签，与编排子页里的单选一一对应
const SWARM_MODE_LABELS: Record<ThreadSwarmMode, string> = {
  inherited: '跟随全局',
  collaborative: '协同',
  leadPlansOnly: '只规划',
  alwaysBees: '直达'
};

// 快捷操作按钮：等宽方格，按下时轻微缩放；开关型操作按下后换成主色描边
const QUICK_ACTION_CLASS =
  'flex flex-col items-center justify-center gap-1 min-h-[64px] rounded-xl border border-[var(--border)] bg-[var(--card)] text-mc-caption text-[var(--foreground)] transition active:scale-95 disabled:opacity-40';
const QUICK_ACTION_ACTIVE_CLASS = 'border-[var(--primary)] bg-[var(--user-bubble)] text-[var(--primary)]';

const NOTICE_CLASS = 'mt-1.5 text-mc-caption text-[var(--danger-foreground)]';
const SUCCESS_NOTICE_CLASS = 'mt-1.5 text-mc-caption text-emerald-600 dark:text-emerald-400';

/** 文件页的标题栏由 FileBrowserSheet 自带，外层 labelledBy 指向它的标题 id。 */
const FILE_BROWSER_MENU_TITLE_ID = 'file-browser-title';

/**
 * 窄屏抽屉卡片的尺寸与附加 class：size 固定为 panel，与栈顶页面种类无关。
 * 卡片 class 必须稳定，否则切页时 slide-up 入场动画会重播；高度仍由 MenuStackView 按页面的 size 处理。
 */
export function threadMenuDrawerFrameProps(_topKind: ThreadMenuPageKind): {
  size: 'panel';
  cardClassName: string;
} {
  return { size: 'panel', cardClassName: 'menu-stack-card' };
}

export interface ThreadMenuSheetProps {
  latestSummary: RemoteThreadSummary;
  branchOrWorkspace?: string;
  isRenaming: boolean;
  setIsRenaming: React.Dispatch<React.SetStateAction<boolean>>;
  renameDraft: string;
  setRenameDraft: React.Dispatch<React.SetStateAction<string>>;
  isUpdatingThread: boolean;
  handleRename: () => Promise<void>;
  composerOptions: RemoteComposerOptions;
  composerOptionsSupported: boolean;
  updateComposerOptions: (
    next: RemoteComposerOptions | ((previous: RemoteComposerOptions) => RemoteComposerOptions)
  ) => void;
  swarmSupported: boolean;
  /** 工作区入口的能力开关：原头部「工作区」下拉菜单已并入本 sheet */
  reviewSupported: boolean;
  gitSupported: boolean;
  filesSupported: boolean;
  scriptsSupported: boolean;
  swarmState: RemoteSwarmState | null;
  swarmAvailable: boolean;
  swarmEnabled: boolean;
  isTogglingSwarm: boolean;
  swarmInlineError: string | null;
  handleToggleSwarm: (nextEnabled: boolean) => Promise<void>;
  handleTogglePin: () => Promise<void>;
  handleToggleDormant: () => Promise<void>;
  handleArchive: () => Promise<void>;
  handleUnarchive: () => Promise<void>;
  saveTaskSupported: boolean;
  forwardSupported: boolean;
  shareActionsBlocked: boolean;
  handleOpenSaveTask: () => void;
  unarchiveError: string | null;
  setForwardError: React.Dispatch<React.SetStateAction<string | null>>;
  /** 栈底页：只在挂载时和 threadID 变化时使用，由 ThreadView 按路由 subview 计算 */
  initialPage: ThreadMenuPageSpec;
  /** suffix 为 null 表示回到会话根路由 */
  onNavigateRoute: (suffix: string | null) => void;
  /** 关闭整个菜单：由 ThreadView 负责清路由并收起重命名 */
  onCloseMenu: () => void;
  menuButtonRef?: React.RefObject<HTMLButtonElement>;
  /** agents/review/git/scripts/files/save-task/forward 的内容体由 ThreadView 提供 */
  renderExternalPage: (args: {
    page: ThreadMenuPage;
    ref: React.Ref<ThreadMenuPageHandle>;
    onClose: () => void;
  }) => React.ReactNode;
}

export const ThreadMenuSheet = React.memo(function ThreadMenuSheet({
  latestSummary,
  branchOrWorkspace,
  isRenaming,
  setIsRenaming,
  renameDraft,
  setRenameDraft,
  isUpdatingThread,
  handleRename,
  composerOptions,
  composerOptionsSupported,
  updateComposerOptions,
  swarmSupported,
  reviewSupported,
  gitSupported,
  filesSupported,
  scriptsSupported,
  swarmState,
  swarmAvailable,
  swarmEnabled,
  isTogglingSwarm,
  swarmInlineError,
  handleToggleSwarm,
  handleTogglePin,
  handleToggleDormant,
  handleArchive,
  handleUnarchive,
  saveTaskSupported,
  forwardSupported,
  shareActionsBlocked,
  handleOpenSaveTask,
  unarchiveError,
  setForwardError,
  initialPage,
  onNavigateRoute,
  onCloseMenu,
  menuButtonRef,
  renderExternalPage
}: ThreadMenuSheetProps) {
  const { setThreadSwarmMode: setSwarmModeRpc } = useRemoteStatic();
  const supportsMethod = useRemoteMethodSupport();
  const threadID = latestSummary.id;
  // 排队中的子代理也算未结束，与头部 ⋯ 上的角标口径一致
  const activeBeeCount =
    (swarmState?.bees?.filter((h) => h.status === 'running').length ?? 0) + (swarmState?.queued ?? 0);

  // 页面栈：栈底由路由决定；切换会话由 resetKey 重置回栈底
  const { stack, isAtBase, push, pop } = useMenuPageStack({ base: initialPage, resetKey: threadID });
  const topPage = stack[stack.length - 1];
  // 每个页面的命令式句柄与头部 trailing 节点都按 page.id 登记
  const handlesRef = useRef(new Map<string, ThreadMenuPageHandle>());
  const handleRefCallbacks = useRef(new Map<string, (handle: ThreadMenuPageHandle | null) => void>());
  const [trailingByPage, setTrailingByPage] = useState<Record<string, React.ReactNode>>({});
  const trailingSetterCallbacks = useRef(new Map<string, (node: React.ReactNode) => void>());

  /** 每页稳定的回调 ref：页面重新挂载时刷新句柄，卸载时清掉。 */
  const handleRefFor = useCallback((pageId: string): ((handle: ThreadMenuPageHandle | null) => void) => {
    const cached = handleRefCallbacks.current.get(pageId);
    if (cached) return cached;
    const callback = (handle: ThreadMenuPageHandle | null) => {
      if (handle) handlesRef.current.set(pageId, handle);
      else handlesRef.current.delete(pageId);
    };
    handleRefCallbacks.current.set(pageId, callback);
    return callback;
  }, []);

  /** 每页稳定的 setTrailing：节点只写进该 page.id 的槽位。 */
  const trailingSetterFor = useCallback((pageId: string) => {
    const cached = trailingSetterCallbacks.current.get(pageId);
    if (cached) return cached;
    const setter = (node: React.ReactNode) => {
      setTrailingByPage((previous) => (previous[pageId] === node ? previous : { ...previous, [pageId]: node }));
    };
    trailingSetterCallbacks.current.set(pageId, setter);
    return setter;
  }, []);

  /** 不经守卫的出栈：栈底时关闭整个菜单。 */
  const dismissTop = useCallback(() => {
    if (isAtBase) {
      onCloseMenu();
      return;
    }
    pop();
    onNavigateRoute(routeAfterPop(stack));
  }, [isAtBase, onCloseMenu, onNavigateRoute, pop, stack]);

  /** 先问栈顶页的 requestClose，被拦下就放弃本次返回/关闭/Esc。 */
  const closeTop = useCallback(() => {
    const top = stack[stack.length - 1];
    const handle = top ? handlesRef.current.get(top.id) : undefined;
    if (handle && !handle.requestClose()) return;
    dismissTop();
  }, [dismissTop, stack]);

  /** 入栈；可路由页同步 hash，菜单本身不卸载。 */
  const pushPage = useCallback(
    (spec: ThreadMenuPageSpec) => {
      push(spec);
      const route = routeForPage(spec);
      if (isRoutablePage(spec.kind) && route) onNavigateRoute(route);
    },
    [onNavigateRoute, push]
  );

  /** 工作区子页仍按旧的 Dispatch 签名调用，实际是入栈 worktree 页。 */
  const openWorktreePage = useCallback<React.Dispatch<React.SetStateAction<boolean>>>(
    (next) => {
      const shouldOpen = typeof next === 'function' ? next(false) : next;
      if (shouldOpen) pushPage({ kind: 'worktree' });
    },
    [pushPage]
  );

  // Esc 只出栈一层：嵌套确认框是第二层 aria-modal dialog，或已 preventDefault 时交给它
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      const openModalCount = document.querySelectorAll('[role="dialog"][aria-modal="true"]').length;
      if (shouldIgnoreMenuEscape({ key: event.key, defaultPrevented: event.defaultPrevented }, openModalCount)) {
        return;
      }
      closeTop();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [closeTop]);

  // 宽屏外壳：compact 页是 popover，详情页是右侧栏；外壳形态按栈顶种类判定
  const wide = useIsWideSheet();
  const surface = sheetSurfaceFor(wide, topPage.kind);
  const size = pageSizeFor(topPage.kind);

  // popover 锚点：⋯ 按钮下方 8px、右边缘对齐；拿不到按钮时交给 CSS 兜底
  const [popoverOffset, setPopoverOffset] = useState<{ top: number; right: number } | null>(null);
  useLayoutEffect(() => {
    if (surface !== 'popover') return;
    const update = () => {
      const button = menuButtonRef?.current;
      if (!button) {
        setPopoverOffset(null);
        return;
      }
      const rect = button.getBoundingClientRect();
      setPopoverOffset({ top: rect.bottom + 8, right: window.innerWidth - rect.right });
    };
    update();
    window.addEventListener('resize', update);
    window.addEventListener('scroll', update, true);
    return () => {
      window.removeEventListener('resize', update);
      window.removeEventListener('scroll', update, true);
    };
  }, [menuButtonRef, surface]);

  const popoverStyle =
    popoverOffset === null
      ? undefined
      : ({
          '--menu-popover-top': `${Math.round(popoverOffset.top)}px`,
          '--menu-popover-right': `${Math.round(popoverOffset.right)}px`
        } as React.CSSProperties);

  // 会话级编排模式：状态留在本组件，主页那一行的取值与编排子页的单选共用同一份
  const swarmModeSupported = supportsMethod('threads.swarm.setMode');
  const [swarmModeOverride, setSwarmModeOverride] = useState<ThreadSwarmMode | null>(null);
  const swarmMode = swarmModeOverride ?? latestSummary.swarmMode ?? 'inherited';
  const [isSettingSwarmMode, setIsSettingSwarmMode] = useState(false);
  const [swarmModeError, setSwarmModeError] = useState<string | null>(null);

  useEffect(() => {
    setSwarmModeOverride(null);
    setSwarmModeError(null);
  }, [latestSummary.swarmMode, threadID]);

  const handleSelectSwarmMode = useCallback(
    async (mode: ThreadSwarmMode) => {
      if (isSettingSwarmMode || mode === swarmMode) return;
      setIsSettingSwarmMode(true);
      setSwarmModeError(null);
      try {
        const updated = await setSwarmModeRpc({ threadID, mode, actionID: makeActionID() });
        setSwarmModeOverride(updated.swarmMode ?? mode);
      } catch (err) {
        setSwarmModeError(err instanceof Error ? err.message : '设置工作模式失败');
      } finally {
        setIsSettingSwarmMode(false);
      }
    },
    [swarmMode, isSettingSwarmMode, setSwarmModeRpc, threadID]
  );

  // 重新生成标题：菜单标题就地生效，列表与订阅推送随后覆盖
  const [titleOverride, setTitleOverride] = useState<string | null>(null);
  const displayTitle = titleOverride ?? latestSummary.title;
  useEffect(() => {
    setTitleOverride(null);
  }, [latestSummary.title, threadID]);
  const { isRegeneratingTitle, regenerateTitleError, regenerateTitle } = useRegenerateThreadTitle({
    threadID,
    onUpdated: setTitleOverride
  });
  const regenerateTitleSupported = supportsMethod('threads.title.regenerate');

  // 移到 worktree：预览与确认在 worktree 页里完成，执行成功后的提示回主页显示
  const worktreeSupported = supportsMethod('threads.worktree.preview');
  const [worktreeNotice, setWorktreeNotice] = useState<string | null>(null);

  // MCP 逐会话开关：列表与禁用名单两个能力都在才显示入口
  const mcpServersSupported =
    supportsMethod(REMOTE_MCP_SERVERS_LIST_METHOD) && supportsMethod(REMOTE_THREADS_MCP_DISABLED_SET_METHOD);

  // 导出会话：先按 threads.history 翻页拉全量历史，再在本地渲染成 md / html / json
  const { loadFullHistory } = useThreadExportHistory();
  const [isExporting, setIsExporting] = useState(false);
  const [isCopyingMarkdown, setIsCopyingMarkdown] = useState(false);
  const [exportNotice, setExportNotice] = useState<{ kind: 'ok' | 'error'; text: string } | null>(null);

  /** 触发浏览器下载：Blob + 临时 a[download]；iOS Safari 不支持 download 属性时改为新窗口打开。 */
  const downloadExportFile = useCallback((fileName: string, mimeType: string, content: string) => {
    const blob = new Blob([content], { type: mimeType });
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    // iOS Safari 上不支持 a.download，点链接只会跳转，直接开新窗口让用户自行「存储到文件」
    const supportsDownload = 'download' in link;
    if (supportsDownload) {
      link.href = url;
      link.download = fileName;
      link.rel = 'noopener';
      document.body.appendChild(link);
      link.click();
      link.remove();
    } else {
      window.open(url, '_blank', 'noopener');
    }
    window.setTimeout(() => URL.revokeObjectURL(url), 60000);
  }, []);

  const handleExportThread = useCallback(
    async (format: ThreadExportFormat) => {
      if (isExporting) return;
      setIsExporting(true);
      setExportNotice(null);
      try {
        const messages = await loadFullHistory(threadID);
        const { buildThreadExport } = await import('../../utils/threadExport');
        const file = buildThreadExport(latestSummary, messages, format);
        downloadExportFile(file.fileName, file.mimeType, file.text);
        // 成功后回主页，提示落在分享组下方；失败则留在导出页显示错误
        dismissTop();
        setExportNotice({
          kind: 'ok',
          text: file.degraded ? `已导出 ${file.fileName}（内容过长，已按 150k 预算降级）` : `已导出 ${file.fileName}`
        });
      } catch (err) {
        setExportNotice({ kind: 'error', text: err instanceof Error ? err.message : '导出会话失败' });
      } finally {
        setIsExporting(false);
      }
    },
    [dismissTop, downloadExportFile, isExporting, latestSummary, loadFullHistory, threadID]
  );

  const handleCopyThreadMarkdown = useCallback(async () => {
    if (isCopyingMarkdown) return;
    setIsCopyingMarkdown(true);
    setExportNotice(null);
    try {
      const messages = await loadFullHistory(threadID);
      // 与「导出 Markdown」走同一条渲染路径，保证剪贴板内容与导出文件一致
      const { buildThreadExport } = await import('../../utils/threadExport');
      const file = buildThreadExport(latestSummary, messages, 'md');
      if (!navigator.clipboard?.writeText) {
        throw new Error('当前浏览器环境不支持剪贴板写入');
      }
      await navigator.clipboard.writeText(file.text);
      setExportNotice({
        kind: 'ok',
        text: file.degraded ? '已复制会话 Markdown（内容过长，已按 150k 预算降级）' : '已复制会话 Markdown'
      });
    } catch (err) {
      setExportNotice({ kind: 'error', text: err instanceof Error ? err.message : '复制会话 Markdown 失败' });
    } finally {
      setIsCopyingMarkdown(false);
    }
  }, [isCopyingMarkdown, latestSummary, loadFullHistory, threadID]);

  // 分组可见性：分组内没有任何条目时整组不渲染
  const workspaceLabel =
    (composerOptions.workspaceMode ?? (latestSummary.worktreeRequested ? 'worktree' : 'local')) === 'worktree'
      ? 'Worktree'
      : '本地';
  const showOpenGroup =
    swarmSupported || reviewSupported || gitSupported || scriptsSupported || filesSupported;
  const showSwarmRow = swarmSupported && !swarmState?.isBee;
  const showMcpRow = mcpServersSupported && !latestSummary.isArchived;
  const showSaveTaskRow =
    saveTaskSupported && !latestSummary.taskID && !latestSummary.isArchived && (!swarmSupported || swarmState);
  const showForwardRow = forwardSupported && !latestSummary.isArchived && (!swarmSupported || swarmState);
  const swarmModeLabel = !swarmAvailable ? '不可用' : !swarmEnabled ? '已关闭' : SWARM_MODE_LABELS[swarmMode];
  const busyExport = isExporting || isCopyingMarkdown;

  /** 栈内每页：注入页头上下文，渲染共享标题栏 + 页面内容。 */
  const renderPageNode = (page: ThreadMenuPage): React.ReactNode => {
    const isMain = page.kind === 'main';
    // 栈底左上角是关闭，深层是返回；两者都经 closeTop 走页面守卫
    const chrome: MenuPageChrome = {
      leading: isAtBase ? 'close' : 'back',
      onLeading: closeTop,
      onClose: closeTop,
      setTrailing: trailingSetterFor(page.id)
    };

    let body: React.ReactNode;
    switch (page.kind) {
      case 'main':
        body = (
          <div className="flex-1 min-h-0 overflow-y-auto">
            {isRenaming ? (
              <div className="mt-4 space-y-2">
                <input
                  type="text"
                  value={renameDraft}
                  onChange={(e) => setRenameDraft(e.target.value)}
                  placeholder="输入新标题"
                  autoFocus
                  className="w-full min-h-[42px] px-3 rounded-xl border border-[var(--input-border)] bg-[var(--card)] text-sm text-[var(--foreground)] outline-none focus:border-[var(--primary)]"
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') void handleRename();
                    if (e.key === 'Escape') {
                      // Esc 只收起内联重命名，不再冒泡去关整个菜单
                      e.preventDefault();
                      setIsRenaming(false);
                    }
                  }}
                />
                <div className="flex gap-2">
                  <button
                    type="button"
                    onClick={() => void handleRename()}
                    disabled={isUpdatingThread || !renameDraft.trim()}
                    className="flex-1 min-h-[40px] rounded-xl bg-[var(--primary)] text-white text-xs font-semibold disabled:opacity-40 transition active:scale-95"
                  >
                    {isUpdatingThread ? '保存中…' : '保存'}
                  </button>
                  <button
                    type="button"
                    onClick={() => setIsRenaming(false)}
                    className="min-h-[40px] px-4 rounded-xl border border-[var(--border)] text-xs text-[var(--foreground-muted)] hover:bg-[var(--user-bubble)] transition active:scale-95"
                  >
                    取消
                  </button>
                </div>
              </div>
            ) : (
              <>
                {/* 快捷操作：会话自身最常用的四个动作 */}
                <div className={`mt-4 grid gap-2 ${regenerateTitleSupported ? 'grid-cols-4' : 'grid-cols-3'}`}>
                  <button
                    type="button"
                    className={QUICK_ACTION_CLASS}
                    onClick={() => {
                      setRenameDraft(latestSummary.title || '');
                      setIsRenaming(true);
                    }}
                    disabled={isUpdatingThread}
                  >
                    <PencilIcon className="h-5 w-5" />
                    <span>重命名</span>
                  </button>
                  {regenerateTitleSupported && (
                    <button
                      type="button"
                      className={QUICK_ACTION_CLASS}
                      onClick={() => void regenerateTitle()}
                      disabled={isUpdatingThread || isRegeneratingTitle}
                      aria-label="重新生成标题"
                      title="重新生成标题"
                    >
                      <ArrowPathIcon className="h-5 w-5" />
                      <span>{isRegeneratingTitle ? '生成中…' : '重新生成'}</span>
                    </button>
                  )}
                  <button
                    type="button"
                    className={`${QUICK_ACTION_CLASS} ${latestSummary.isPinned ? QUICK_ACTION_ACTIVE_CLASS : ''}`}
                    onClick={() => void handleTogglePin()}
                    disabled={isUpdatingThread}
                    aria-pressed={latestSummary.isPinned}
                  >
                    <PinIcon className="h-5 w-5" />
                    <span>{latestSummary.isPinned ? '取消置顶' : '置顶'}</span>
                  </button>
                  <button
                    type="button"
                    className={`${QUICK_ACTION_CLASS} ${latestSummary.isDormant ? QUICK_ACTION_ACTIVE_CLASS : ''}`}
                    onClick={() => void handleToggleDormant()}
                    disabled={isUpdatingThread}
                    aria-pressed={latestSummary.isDormant}
                  >
                    <MoonIcon className="h-5 w-5" />
                    <span>{latestSummary.isDormant ? '取消休眠' : '休眠'}</span>
                  </button>
                </div>
                {regenerateTitleError && (
                  <p className={NOTICE_CLASS} role="alert">
                    {regenerateTitleError}
                  </p>
                )}

                {/* 打开：工作区内的各个面板入口，全部入栈 */}
                {showOpenGroup && (
                  <MenuGroup title="打开">
                    {swarmSupported && (
                      <MenuRow icon={<ListBulletIcon className="h-4 w-4" />} label="子代理" badge={activeBeeCount} chevron onClick={() => pushPage({ kind: 'agents' })} />
                    )}
                    {reviewSupported && (
                      <MenuRow icon={<EyeIcon className="h-4 w-4" />} label="审阅" chevron onClick={() => pushPage({ kind: 'review' })} />
                    )}
                    {gitSupported && (
                      <MenuRow icon={<GitBranchIcon className="h-4 w-4" />} label="Git" chevron onClick={() => pushPage({ kind: 'git' })} />
                    )}
                    {scriptsSupported && (
                      <MenuRow icon={<PlayIcon className="h-4 w-4" />} label="脚本" chevron onClick={() => pushPage({ kind: 'scripts' })} />
                    )}
                    {filesSupported && (
                      <MenuRow icon={<FolderIcon className="h-4 w-4" />} label="文件" chevron onClick={() => pushPage({ kind: 'files' })} />
                    )}
                  </MenuGroup>
                )}

                {/* 本会话设置：编排与工作区各占一个子页，MCP 服务器也是栈内一页 */}
                <MenuGroup
                  title="本会话设置"
                  footer={
                    worktreeNotice ? (
                      <p className={SUCCESS_NOTICE_CLASS} role="status">
                        {worktreeNotice}
                      </p>
                    ) : null
                  }
                >
                  {showSwarmRow && (
                    <MenuRow icon={<SparklesIcon className="h-4 w-4" />} label="编排" value={swarmModeLabel} chevron onClick={() => pushPage({ kind: 'swarm' })} />
                  )}
                  <MenuRow icon={<CodeBracketIcon className="h-4 w-4" />} label="工作区" value={workspaceLabel} chevron onClick={() => pushPage({ kind: 'workspace' })} />
                  {showMcpRow && (
                    <MenuRow icon={<ServerIcon className="h-4 w-4" />} label="MCP 服务器" chevron disabled={isUpdatingThread} onClick={() => pushPage({ kind: 'mcp' })} />
                  )}
                </MenuGroup>

                {/* 分享：把会话内容搬成看板任务、转发到新会话，或导出/复制 */}
                <MenuGroup
                  title="分享"
                  footer={
                    exportNotice ? (
                      <p
                        className={exportNotice.kind === 'ok' ? SUCCESS_NOTICE_CLASS : NOTICE_CLASS}
                        role={exportNotice.kind === 'ok' ? 'status' : 'alert'}
                      >
                        {exportNotice.text}
                      </p>
                    ) : null
                  }
                >
                  {showSaveTaskRow && (
                    <MenuRow
                      icon={<LayoutIcon className="h-4 w-4" />}
                      label="存为看板任务"
                      disabled={isUpdatingThread || shareActionsBlocked}
                      onClick={() => {
                        handleOpenSaveTask();
                        pushPage({ kind: 'save-task' });
                      }}
                    />
                  )}
                  {showForwardRow && (
                    <MenuRow
                      icon={<ArrowUturnRightIcon className="h-4 w-4" />}
                      label="转发到新会话"
                      disabled={isUpdatingThread || shareActionsBlocked}
                      onClick={() => {
                        setForwardError(null);
                        pushPage({ kind: 'forward' });
                      }}
                    />
                  )}
                  <MenuRow
                    icon={<ArrowDownIcon className="h-4 w-4" />}
                    label={isExporting ? '正在导出…' : '导出会话'}
                    chevron
                    disabled={busyExport}
                    onClick={() => pushPage({ kind: 'export' })}
                  />
                  <MenuRow
                    icon={<DocumentTextIcon className="h-4 w-4" />}
                    label={isCopyingMarkdown ? '正在复制…' : '复制为 Markdown'}
                    disabled={busyExport}
                    onClick={() => void handleCopyThreadMarkdown()}
                  />
                </MenuGroup>

                {/* 归档：归档后会话离开列表，取消归档在同一行切换 */}
                <MenuGroup
                  footer={
                    unarchiveError ? (
                      <p className={NOTICE_CLASS} role="alert">
                        {unarchiveError}
                      </p>
                    ) : null
                  }
                >
                  {latestSummary.isArchived ? (
                    <MenuRow icon={<ArchiveBoxIcon className="h-4 w-4" />} label="取消归档" disabled={isUpdatingThread} onClick={() => void handleUnarchive()} />
                  ) : (
                    <MenuRow icon={<ArchiveBoxIcon className="h-4 w-4" />} label="归档" danger disabled={isUpdatingThread} onClick={() => void handleArchive()} />
                  )}
                </MenuGroup>
              </>
            )}
          </div>
        );
        break;
      case 'swarm':
        body = (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <ThreadMenuSwarmPanel
              threadID={threadID}
              latestSummary={latestSummary}
              swarmState={swarmState}
              swarmAvailable={swarmAvailable}
              swarmEnabled={swarmEnabled}
              isTogglingSwarm={isTogglingSwarm}
              swarmInlineError={swarmInlineError}
              handleToggleSwarm={handleToggleSwarm}
              swarmModeSupported={swarmModeSupported}
              swarmMode={swarmMode}
              isSettingSwarmMode={isSettingSwarmMode}
              swarmModeError={swarmModeError}
              onSelectSwarmMode={handleSelectSwarmMode}
            />
          </div>
        );
        break;
      case 'workspace':
        body = (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <ThreadMenuWorkspacePanel
              latestSummary={latestSummary}
              composerOptions={composerOptions}
              composerOptionsSupported={composerOptionsSupported}
              updateComposerOptions={updateComposerOptions}
              isUpdatingThread={isUpdatingThread}
              shareActionsBlocked={shareActionsBlocked}
              worktreeSupported={worktreeSupported}
              setWorktreeOpen={openWorktreePage}
              worktreeNotice={worktreeNotice}
              setWorktreeNotice={setWorktreeNotice}
            />
          </div>
        );
        break;
      case 'export':
        body = (
          <div className="flex-1 min-h-0 overflow-y-auto">
            <ThreadMenuExportPanel
              isExporting={isExporting}
              isCopyingMarkdown={isCopyingMarkdown}
              handleExportThread={handleExportThread}
              exportNotice={exportNotice}
            />
          </div>
        );
        break;
      case 'mcp':
        body = (
          <ThreadMCPServersSheetBody
            ref={handleRefFor(page.id)}
            threadID={threadID}
            projectID={latestSummary.projectID}
            threadTitle={displayTitle || '未命名会话'}
            onClose={dismissTop}
          />
        );
        break;
      case 'worktree':
        body = (
          <WorktreeMoveSheetBody
            ref={handleRefFor(page.id)}
            threadID={threadID}
            threadTitle={displayTitle || '未命名会话'}
            onClose={dismissTop}
            onExecuted={(result, transferred) => {
              dismissTop();
              setWorktreeNotice(
                `已移到 worktree：${result.branch}${transferred ? ` · 转移 ${result.transferredFiles} 个文件` : ''}`
              );
            }}
          />
        );
        break;
      default:
        body = renderExternalPage({ page, ref: handleRefFor(page.id), onClose: dismissTop });
        break;
    }

    return (
      <MenuPageChromeContext.Provider key={page.id} value={chrome}>
        <div className="flex flex-1 flex-col min-h-0 min-w-0">
          {/* 文件页的标题栏由 FileBrowserSheet 自带，避免出现两份 */}
          {page.kind !== 'files' && (
            <MenuSheetHeader
              eyebrow={
                isMain
                  ? `${branchOrWorkspace || '当前工作区'} · ${workspaceLabel}`
                  : displayTitle || '未命名会话'
              }
              title={isMain ? displayTitle || '未命名会话' : MENU_PAGE_TITLES[page.kind]}
              labelledBy={`thread-menu-title-${page.id}`}
              leading={chrome.leading}
              onLeading={closeTop}
              onClose={closeTop}
              trailing={trailingByPage[page.id] ?? null}
              showHandle={!wide}
            />
          )}
          {body}
        </div>
      </MenuPageChromeContext.Provider>
    );
  };

  const pages = stack.map((page) => ({ id: page.id, node: renderPageNode(page) }));
  const labelledBy =
    topPage.kind === 'files' ? FILE_BROWSER_MENU_TITLE_ID : `thread-menu-title-${topPage.id}`;

  return (
    <>
      {surface === 'drawer' && (
        // 卡片 class 必须稳定（固定 panel），否则切页时 slide-up 入场动画会重播
        <SheetFrame
          onRequestClose={closeTop}
          labelledBy={labelledBy}
          {...threadMenuDrawerFrameProps(topPage.kind)}
          closeOnEscape={false}
        >
          <MenuStackView pages={pages} activeId={topPage.id} size={size} />
        </SheetFrame>
      )}

      {surface === 'popover' && (
        <div className="menu-popover-layer" role="presentation" onMouseDown={closeTop}>
          <div
            className="menu-popover"
            role="dialog"
            aria-modal="true"
            aria-labelledby={labelledBy}
            style={popoverStyle}
            onMouseDown={(event) => event.stopPropagation()}
          >
            <MenuStackView pages={pages} activeId={topPage.id} size="menu" />
          </div>
        </div>
      )}

      {surface === 'side-panel' && (
        <div className="menu-side-layer" role="presentation" onMouseDown={closeTop}>
          <div
            className="menu-side-panel"
            role="dialog"
            aria-modal="true"
            aria-labelledby={labelledBy}
            onMouseDown={(event) => event.stopPropagation()}
          >
            <MenuStackView pages={pages} activeId={topPage.id} size="panel" fillParent />
          </div>
        </div>
      )}
    </>
  );
});
