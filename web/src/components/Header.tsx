// 移动首页顶栏：项目范围、视图切换、连接胶囊与设置 sheet。

import React, { useEffect, useMemo, useState } from 'react';
import { useRemote } from '../context/RemoteContext';
import { isThreadBusy } from '../context/remoteGuards';
import { AlwaysAllowedSection } from './AlwaysAllowedSection';
import { AttentionSheet } from './AttentionSheet';
import { ConfirmDialog } from './ConfirmDialog';
import {
  REMOTE_ATTENTION_LIST_METHOD,
  useAttentionInbox
} from '../context/remoteAttentionApi';
import {
  REMOTE_MEMORY_PROPOSALS_LIST_METHOD,
  useMemoryProposals
} from '../context/remoteMemoryApi';
import { useRemoteCall } from '../context/useRemoteCall';
import { useRemoteSettingsApi } from '../context/remoteSettingsApi';
import {
  getStoredAutoCollapse,
  parsePairingInput,
  setStoredAutoCollapse,
  setStoredStewardAutoCollapse
} from '../services/storage';
import type { RemoteThreadSummary } from '../types/contract';
import {
  ArrowPathIcon,
  ChevronDownIcon,
  ComputerDesktopIcon,
  LayoutIcon,
  ListBulletIcon,
  MoonIcon,
  SettingsIcon,
  SunIcon
} from './Icons';
import { LazyBoundary } from './LazyFallback';
import type { SettingsFormPage } from './SettingsForm';
import { SettingsRow, SettingsSection, SettingsStackedRow, SettingsSwitch } from './settings/SettingsLayout';
import { SettingsShell } from './settings/SettingsShell';
import { DevicesSection } from './settings/DevicesSection';
import { DeviceAppSection } from './settings/DeviceAppSection';
import { AboutSection } from './settings/AboutSection';
import { MemoryProposalSettingsRowView } from './settings/MemoryProposalSettingsRowView';
import { AttentionSettingsRowView } from './settings/AttentionSettingsRowView';
import { SETTINGS_ITEMS } from './settings/settingsIndex';

// 子代理档位抽屉只在设置里手动打开，体积不小，改为按需加载。
const SwarmTiersSheet = React.lazy(() =>
  import('./SwarmTiersSheet').then((module) => ({ default: module.SwarmTiersSheet }))
);

// 设置表单与记忆提案抽屉只在打开对应面板时才会渲染，首屏用不到，做成懒加载；
// 关闭状态下不渲染组件，也就不会去下载它们所在的 chunk。
const SettingsForm = React.lazy(() =>
  import('./SettingsForm').then((module) => ({ default: module.SettingsForm }))
);
const MemoryProposalsSheet = React.lazy(() =>
  import('./MemoryProposalsSheet').then((module) => ({ default: module.MemoryProposalsSheet }))
);

type HomeView = 'sessions' | 'board';
type HomePairingCode = { token: string; expiresAt: string };

function issuedPairingToken(value: string): string | null {
  try {
    const url = new URL(value);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    if (url.username || url.password || url.search || !url.hash.startsWith('#pair=')) return null;
    const entries = Array.from(new URLSearchParams(url.hash.slice(1)).entries());
    if (entries.length !== 1 || entries[0][0] !== 'pair') return null;
    const token = entries[0][1]?.trim() ?? '';
    return /^[0-9a-fA-F]{64}$/.test(token) ? token.toLowerCase() : null;
  } catch {
    return null;
  }
}

interface HeaderProps {
  actionsSlotRef?: (el: HTMLDivElement | null) => void;
  leadingSlotRef?: (el: HTMLDivElement | null) => void;
  activeView?: HomeView;
  onSwitchView?: (view: HomeView) => void;
  settingsOpen: boolean;
  // 当前设置页（#/settings/<id>）；窄屏直接用它选中的 tab。
  settingsPageID?: string | null;
  onSettingsPageChange?: (pageID: string | null) => void;
  onOpenSettings: () => void;
  onCloseSettings: () => void;
}

const connectionCopy: Record<string, string> = {
  connected: '已连接',
  connecting: '连接中',
  reconnecting: '重连中',
  auth_failed: '凭据失效',
  unpaired: '未配对',
  disconnected: '已断开'
};

const STATUS_SHOW_DELAY_MS = 800;

// 与会话列表 ThreadList 中 scopedThreads 的过滤条件保持一致（此处不按项目过滤），
// 这样顶栏「全部项目」的会话数与列表「全部」chip 的数字相同。
function isListedThread(thread: RemoteThreadSummary): boolean {
  return (
    !thread.isArchived &&
    !Boolean(
      (thread as { isHelper?: boolean }).isHelper ||
      (thread as { parentThreadID?: string | null }).parentThreadID
    )
  );
}

function useDelayedConnectionStatus(connectionState: string, isSyncing: boolean) {
  const isInterrupted = connectionState !== 'connected' || isSyncing;
  const [visible, setVisible] = useState(false);

  useEffect(() => {
    if (!isInterrupted) {
      setVisible(false);
      return;
    }
    const timer = window.setTimeout(() => setVisible(true), STATUS_SHOW_DELAY_MS);
    return () => window.clearTimeout(timer);
  }, [isInterrupted]);

  return visible ? { state: connectionState, isSyncing } : null;
}

interface MemoryProposalSettingsRowProps {
  projectID: string | null;
  onOpen: () => void;
}

// 设置抽屉里的「记忆提议」入口：带待审核角标，点击打开收件箱抽屉。
// 只在抽屉打开时挂载，因此只有在设置里才会去拉待审核数量。
const MemoryProposalSettingsRow: React.FC<MemoryProposalSettingsRowProps> = ({
  projectID,
  onOpen
}) => {
  const { supportsMethod } = useRemoteCall();
  const { proposals } = useMemoryProposals(projectID, 'pending');

  if (!supportsMethod(REMOTE_MEMORY_PROPOSALS_LIST_METHOD)) return null;

  return <MemoryProposalSettingsRowView pendingCount={proposals.length} onOpen={onOpen} />;
};

interface AttentionSettingsRowProps {
  projectID: string | null;
  onOpen: () => void;
}

// 设置抽屉里的「待处理」入口：带未处理角标，点击打开待处理抽屉。
// 只在抽屉打开时挂载，因此只有在设置里才会去拉未处理事项。
const AttentionSettingsRow: React.FC<AttentionSettingsRowProps> = ({ projectID, onOpen }) => {
  const { supportsMethod } = useRemoteCall();
  const { unresolvedCount } = useAttentionInbox(projectID);

  if (!supportsMethod(REMOTE_ATTENTION_LIST_METHOD)) return null;

  return <AttentionSettingsRowView unresolvedCount={unresolvedCount} onOpen={onOpen} />;
};

export const Header: React.FC<HeaderProps> = ({
  actionsSlotRef,
  leadingSlotRef,
  activeView = 'sessions',
  onSwitchView,
  settingsOpen,
  settingsPageID,
  onSettingsPageChange,
  onOpenSettings,
  onCloseSettings
}) => {
  const {
    projects,
    threads,
    selectedProjectID,
    setSelectedProjectID,
    connectionState,
    isSyncing,
    theme,
    setTheme,
    refreshBoard,
    reconnect,
    clearAuth,
    rememberDevice,
    setRememberDevice,
    deviceIdentity,
    renameDevice,
    forgetInProgress,
    forgetDevice,
    newPairingCode,
    loadProjects,
    loadThreads,
    capabilities
  } = useRemote();

  // 「自动折叠思考过程」偏好与设置面板里的开关一一对应，默认开启
  const [autoCollapse, setAutoCollapse] = useState(() => getStoredAutoCollapse());
  // 宿主支持 settings.get 时，自动折叠交给 session.auto_collapse_reasoning 管，
  // 设置面板里由 SettingsForm 渲染，Header 这里只把远端值同步回本地偏好供时间线读取。
  const settingsApi = useRemoteSettingsApi();
  const { supportsMethod } = useRemoteCall();
  const remoteAutoCollapse = settingsApi.snapshot?.values['session.auto_collapse_reasoning'];
  useEffect(() => {
    if (typeof remoteAutoCollapse !== 'boolean') return;
    setStoredAutoCollapse(remoteAutoCollapse);
    setAutoCollapse(remoteAutoCollapse);
  }, [remoteAutoCollapse]);
  // 总管会话的自动折叠由 steward.auto_collapse_reasoning 单独管，这里只把远端值同步回
  // 本地偏好，供总管时间线读取；老宿主没有这个键时保持本地默认（开启）。
  const remoteStewardAutoCollapse = settingsApi.snapshot?.values['steward.auto_collapse_reasoning'];
  useEffect(() => {
    if (typeof remoteStewardAutoCollapse !== 'boolean') return;
    setStoredStewardAutoCollapse(remoteStewardAutoCollapse);
  }, [remoteStewardAutoCollapse]);
  // 每次打开设置面板重拉一次，补上断线期间错过的 settings.changed。
  const reloadSettings = settingsApi.reload;
  useEffect(() => {
    if (!settingsOpen) return;
    reloadSettings();
  }, [reloadSettings, settingsOpen]);
  const [scopeOpen, setScopeOpen] = useState(false);
  const [showForgetConfirm, setShowForgetConfirm] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshError, setRefreshError] = useState<string | null>(null);
  const [homePairingCode, setHomePairingCode] = useState<HomePairingCode | null>(null);
  const [homePairingBusy, setHomePairingBusy] = useState(false);
  const [homePairingCopied, setHomePairingCopied] = useState(false);
  const [homePairingError, setHomePairingError] = useState<string | null>(null);
  const [tiersOpen, setTiersOpen] = useState(false);
  const [memoryOpen, setMemoryOpen] = useState(false);
  const [attentionOpen, setAttentionOpen] = useState(false);
  const [renamingDevice, setRenamingDevice] = useState(false);
  const [deviceNameDraft, setDeviceNameDraft] = useState('');
  const [deviceRenameBusy, setDeviceRenameBusy] = useState(false);
  const [deviceRenameError, setDeviceRenameError] = useState<string | null>(null);

  const delayedStatus = useDelayedConnectionStatus(connectionState, isSyncing);

  const listedThreadCount = useMemo(() => threads.filter(isListedThread).length, [threads]);

  // 设置面板打开时收起项目范围菜单，避免两个浮层同时显示。
  useEffect(() => {
    if (settingsOpen) setScopeOpen(false);
  }, [settingsOpen]);

  useEffect(() => {
    if (!scopeOpen && !settingsOpen) return;
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setScopeOpen(false);
      if (settingsOpen) onCloseSettings();
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [onCloseSettings, scopeOpen, settingsOpen]);

  const projectName = projects.find((project) => project.id === selectedProjectID)?.name;
  const projectCounts = useMemo(() => {
    const counts = new Map<string, { attention: number; running: number }>();
    for (const thread of threads.filter(isListedThread)) {
      const current = counts.get(thread.projectID) ?? { attention: 0, running: 0 };
      if (thread.attention === 'approval' || thread.attention === 'input') current.attention += 1;
      if (isThreadBusy(thread)) current.running += 1;
      counts.set(thread.projectID, current);
    }
    return counts;
  }, [threads]);

  const selectProject = (projectID: string | null) => {
    setScopeOpen(false);
    setSelectedProjectID(projectID);
  };

  const handleRefresh = async () => {
    if (refreshing || connectionState !== 'connected') {
      if (connectionState !== 'connected') reconnect();
      return;
    }
    setRefreshing(true);
    setRefreshError(null);
    try {
      await Promise.all([
        refreshBoard(),
        loadProjects(),
        loadThreads(selectedProjectID)
      ]);
    } catch (error) {
      setRefreshError(error instanceof Error ? error.message : '刷新失败，请重试');
    } finally {
      setRefreshing(false);
    }
  };

  const handleForgetDevice = async () => {
    if (forgetInProgress) return;
    await forgetDevice();
    setShowForgetConfirm(false);
    onCloseSettings();
  };

  const handleGenerateHomePairingCode = async () => {
    if (homePairingBusy || connectionState !== 'connected') return;
    setHomePairingBusy(true);
    setHomePairingError(null);
    setHomePairingCopied(false);
    try {
      const result = await newPairingCode();
      const parsed = parsePairingInput(result.pairingURL);
      const token = parsed.token || issuedPairingToken(result.pairingURL);
      if (!token) throw new Error(parsed.error || 'Mac 端返回的配对链接无效');
      setHomePairingCode({ token, expiresAt: result.expiresAt });
    } catch (error) {
      setHomePairingError(error instanceof Error ? error.message : '生成主屏幕配对码失败');
    } finally {
      setHomePairingBusy(false);
    }
  };

  const handleCopyHomePairingCode = async () => {
    if (!homePairingCode) return;
    if (!navigator.clipboard?.writeText) {
      setHomePairingError('当前浏览器不支持复制，请长按配对码后手动复制');
      return;
    }
    try {
      await navigator.clipboard.writeText(homePairingCode.token);
      setHomePairingCopied(true);
      setHomePairingError(null);
    } catch {
      setHomePairingError('复制失败，请长按配对码后手动复制');
    }
  };

  const secureEntry = window.location.protocol === 'https:';
  const canGenerateHomePairing = capabilities?.methods.includes('session.newPairingCode') ?? true;
  // 离线时不可改名；宿主未声明 session.renameDevice 时也置灰，避免点了才报错
  const canRenameDevice =
    connectionState === 'connected' && (capabilities?.methods.includes('session.renameDevice') ?? true);

  const startRenameDevice = () => {
    if (!canRenameDevice) return;
    setDeviceNameDraft(deviceIdentity?.label || '');
    setDeviceRenameError(null);
    setRenamingDevice(true);
  };

  const cancelRenameDevice = () => {
    setRenamingDevice(false);
    setDeviceNameDraft('');
    setDeviceRenameError(null);
  };

  const handleRenameDevice = async () => {
    if (deviceRenameBusy || !canRenameDevice) return;
    const name = deviceNameDraft.trim();
    if (!name) {
      setDeviceRenameError('设备名称不能为空');
      return;
    }
    if (Array.from(name).length > 64) {
      setDeviceRenameError('设备名称不能超过 64 个字符');
      return;
    }
    setDeviceRenameBusy(true);
    setDeviceRenameError(null);
    try {
      await renameDevice(name);
      setRenamingDevice(false);
      setDeviceNameDraft('');
    } catch (error) {
      setDeviceRenameError(error instanceof Error ? error.message : '修改设备名称失败，请稍后重试');
    } finally {
      setDeviceRenameBusy(false);
    }
  };

  // 宿主未声明 swarm.tiers.get 时整块入口不显示
  const canManageSwarmTiers = capabilities?.methods.includes('swarm.tiers.get') ?? false;

  const openSwarmTiers = () => {
    onCloseSettings();
    setTiersOpen(true);
  };

  const openMemoryProposals = () => {
    onCloseSettings();
    setMemoryOpen(true);
  };

  const openAttention = () => {
    onCloseSettings();
    setAttentionOpen(true);
  };

  // 设置导航只列真正有内容的页：远程设置相关页在旧宿主（不支持 settings.get）时不出现，
  // 「记忆」页跟着记忆提议能力走。
  const isAttentionSupported = supportsMethod(REMOTE_ATTENTION_LIST_METHOD);
  const isMemorySupported = supportsMethod(REMOTE_MEMORY_PROPOSALS_LIST_METHOD);
  const visibleSettingsPageIDs = settingsApi.supportsSettings
    ? [
        'conversation',
        'swarm',
        'steward',
        ...(isMemorySupported ? ['nowledge'] : []),
        'general',
        'notifications',
        'appearance',
        'archive',
        'device',
        'about'
      ]
    : ['conversation', 'appearance', 'device', 'about'];
  // 搜索与跳转只认当前可见的设置项：宿主缺键、能力不足的项不出现在结果里。
  const visibleSettingsItemIDs = SETTINGS_ITEMS.filter((item) => {
    if (!visibleSettingsPageIDs.includes(item.page)) return false;
    if (item.id === 'block-swarm-tiers') return canManageSwarmTiers;
    if (item.id === 'web-attention-inbox') return isAttentionSupported;
    if (item.id === 'web-memory-proposals') return isMemorySupported;
    if (item.settingsKey) {
      // 宿主不支持远程设置时只剩本地的「自动折叠思考过程」，它不写远程键，照常可见。
      if (!settingsApi.supportsSettings) return item.id === 'row-auto-collapse-reasoning';
      return settingsApi.snapshot?.writableKeys.includes(item.settingsKey) ?? false;
    }
    return true;
  }).map((item) => item.id);
  // 直接打开 #/settings/<不存在或不可见的 id> 时回到导航首页。
  const activeSettingsPageID =
    settingsPageID && visibleSettingsPageIDs.includes(settingsPageID) ? settingsPageID : null;

  return (
    <>
      <header
        className="app-header sticky top-0 z-20 shrink-0"
        style={{ paddingTop: 'calc(max(env(safe-area-inset-top), 0px) + 8px)' }}
      >
        <div className="app-header__inner">
          <div className="relative min-w-0 flex items-center justify-start">
            <button
              type="button"
              aria-haspopup="menu"
              aria-expanded={scopeOpen}
              onClick={() => setScopeOpen((open) => !open)}
              className="scope-button"
            >
              <span className="scope-button__label font-semibold">{projectName || '全部项目'}</span>
              <ChevronDownIcon className={`h-3.5 w-3.5 shrink-0 transition-transform text-muted ${scopeOpen ? 'rotate-180' : ''}`} />
            </button>

            {scopeOpen && (
              <div className="scope-menu" role="menu" aria-label="项目范围">
                <div className="scope-menu__heading">项目范围</div>
                <button
                  type="button"
                  role="menuitemradio"
                  aria-checked={selectedProjectID === null}
                  onClick={() => selectProject(null)}
                  className={`scope-menu__item ${selectedProjectID === null ? 'scope-menu__item--active' : ''}`}
                >
                  <span className="min-w-0">
                    <span className="scope-menu__title">全部项目</span>
                    <span className="scope-menu__meta">{listedThreadCount} 个会话</span>
                  </span>
                  <span className="scope-menu__check">{selectedProjectID === null ? '✓' : ''}</span>
                </button>
                {projects.map((project) => {
                  const count = projectCounts.get(project.id) ?? { attention: 0, running: 0 };
                  const countsLoaded = selectedProjectID === null || selectedProjectID === project.id;
                  return (
                    <button
                      key={project.id}
                      type="button"
                      role="menuitemradio"
                      aria-checked={selectedProjectID === project.id}
                      onClick={() => selectProject(project.id)}
                      className={`scope-menu__item ${selectedProjectID === project.id ? 'scope-menu__item--active' : ''}`}
                    >
                      <span className="min-w-0">
                        <span className="scope-menu__title truncate">{project.name}</span>
                        {countsLoaded && (
                          <span className="scope-menu__meta">
                            等你 {count.attention}
                            <span aria-hidden="true"> · </span>
                            运行中 {count.running}
                          </span>
                        )}
                      </span>
                      <span className="scope-menu__check">{selectedProjectID === project.id ? '✓' : ''}</span>
                    </button>
                  );
                })}
              </div>
            )}
            <div ref={leadingSlotRef} className="ml-auto flex shrink-0 items-center" />
          </div>

          <div className="flex items-center justify-center min-w-0">
            {onSwitchView ? (
              <div className="view-switch view-switch--header" role="tablist" aria-label="首页视图">
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeView === 'sessions'}
                  aria-label="会话"
                  onClick={() => onSwitchView('sessions')}
                  className={`view-switch__item ${activeView === 'sessions' ? 'view-switch__item--active' : ''}`}
                >
                  <ListBulletIcon className="h-4 w-4" />
                  <span>会话</span>
                </button>
                <button
                  type="button"
                  role="tab"
                  aria-selected={activeView === 'board'}
                  aria-label="看板"
                  onClick={() => onSwitchView('board')}
                  className={`view-switch__item ${activeView === 'board' ? 'view-switch__item--active' : ''}`}
                >
                  <LayoutIcon className="h-4 w-4" />
                  <span>看板</span>
                </button>
              </div>
            ) : (
              <span className="text-base font-semibold text-foreground truncate">mcode</span>
            )}
          </div>

          <div className="flex items-center justify-end gap-1">
            {delayedStatus !== null && (
              <button
                type="button"
                onClick={delayedStatus.state === 'reconnecting' || delayedStatus.state === 'disconnected' ? reconnect : undefined}
                className="icon-button animate-fadeIn"
                aria-label={delayedStatus.isSyncing ? '同步中' : connectionCopy[delayedStatus.state] ?? '连接状态'}
                title={delayedStatus.state === 'reconnecting' ? '立即重连' : (delayedStatus.isSyncing ? '同步中' : connectionCopy[delayedStatus.state])}
              >
                {delayedStatus.isSyncing || delayedStatus.state === 'connecting' || delayedStatus.state === 'reconnecting' ? (
                  <span className="loading-spinner loading-spinner--small" />
                ) : (
                  <span className="h-2.5 w-2.5 rounded-full bg-danger shrink-0" />
                )}
              </button>
            )}
            <div ref={actionsSlotRef} className="contents" />
            <button
              type="button"
              onClick={() => {
                setScopeOpen(false);
                onOpenSettings();
              }}
              className="icon-button"
              aria-label="打开设置"
              title="设置"
            >
              <SettingsIcon className="h-4 w-4" />
            </button>
          </div>
        </div>
      </header>

      {settingsOpen && (
        <SettingsShell
          onClose={onCloseSettings}
          pageID={activeSettingsPageID}
          onPageChange={onSettingsPageChange}
          visiblePageIDs={visibleSettingsPageIDs}
          visibleItemIDs={visibleSettingsItemIDs}
          renderPage={(pageID, advanced) => {
            const toggleAdvanced = () => advanced.setOpen(!advanced.open);
            switch (pageID) {
              case 'conversation':
                // 宿主不支持远程设置时，「会话」页只放本地的自动折叠开关。
                return settingsApi.supportsSettings ? (
                  <LazyBoundary label="正在加载设置…">
                    <SettingsForm
                      api={settingsApi}
                      page="conversation"
                      advancedOpen={advanced.open}
                      onAdvancedToggle={toggleAdvanced}
                    />
                  </LazyBoundary>
                ) : (
                  <SettingsSection title="回复与思考">
                    <SettingsRow
                      itemID="row-auto-collapse-reasoning"
                      title="自动折叠思考过程"
                      desc="思考结束后自动收起，关闭则保持展开"
                      control={
                        <SettingsSwitch
                          checked={autoCollapse}
                          label="自动折叠思考过程"
                          onChange={(next) => {
                            setAutoCollapse(next);
                            setStoredAutoCollapse(next);
                          }}
                        />
                      }
                    />
                  </SettingsSection>
                );
              case 'archive':
              case 'general':
              case 'notifications':
                return (
                  <LazyBoundary label="正在加载设置…">
                    <SettingsForm
                      api={settingsApi}
                      page={pageID as SettingsFormPage}
                      advancedOpen={advanced.open}
                      onAdvancedToggle={toggleAdvanced}
                    />
                    {pageID === 'general' && (
                      <AlwaysAllowedSection projectID={selectedProjectID} projectName={projectName} />
                    )}
                  </LazyBoundary>
                );
              case 'swarm':
                return (
                  <>
                    <LazyBoundary label="正在加载设置…">
                      <SettingsForm
                        api={settingsApi}
                        page="swarm"
                        advancedOpen={advanced.open}
                        onAdvancedToggle={toggleAdvanced}
                        swarmTiersRow={
                          canManageSwarmTiers ? (
                            <SettingsRow
                              itemID="block-swarm-tiers"
                              title="子代理档位"
                              desc="按档位配置子代理的模型与写入白名单"
                              control={
                                <button
                                  type="button"
                                  className="secondary-button settings-inline-button"
                                  onClick={openSwarmTiers}
                                >
                                  管理
                                </button>
                              }
                            />
                          ) : undefined
                        }
                      />
                    </LazyBoundary>
                  </>
                );
              case 'steward':
                return (
                  <>
                    <LazyBoundary label="正在加载设置…">
                      <SettingsForm
                        api={settingsApi}
                        page="steward"
                        advancedOpen={advanced.open}
                        onAdvancedToggle={toggleAdvanced}
                      />
                    </LazyBoundary>
                    {isAttentionSupported && (
                      <AttentionSettingsRow projectID={selectedProjectID} onOpen={openAttention} />
                    )}
                  </>
                );
              case 'nowledge':
                return (
                  <MemoryProposalSettingsRow projectID={selectedProjectID} onOpen={openMemoryProposals} />
                );
              case 'appearance':
                return (
                  <SettingsSection title="主题与字体">
                    <SettingsStackedRow itemID="row-theme" title="主题" desc="跟随系统、浅色或深色">
                      <div className="theme-switch" role="radiogroup" aria-label="主题">
                        {([
                          ['system', '跟随系统', <ComputerDesktopIcon className="h-4 w-4" />],
                          ['light', '浅色', <SunIcon className="h-4 w-4" />],
                          ['dark', '深色', <MoonIcon className="h-4 w-4" />]
                        ] as const).map(([value, label, icon]) => (
                          <button
                            key={value}
                            type="button"
                            role="radio"
                            aria-checked={theme === value}
                            onClick={() => setTheme(value)}
                            className={`theme-switch__item ${theme === value ? 'theme-switch__item--active' : ''}`}
                          >
                            {icon}
                            <span>{label}</span>
                          </button>
                        ))}
                      </div>
                    </SettingsStackedRow>
                  </SettingsSection>
                );
              case 'about':
                return <AboutSection />;
              case 'device':
                return (
                  <>
                    <SettingsSection title="连接">
                      <SettingsRow
                        itemID="web-connection-refresh"
                        title="刷新会话与看板"
                        desc="重新拉取 Mac 上的会话、看板与项目列表"
                        error={refreshError}
                        control={
                          <button
                            type="button"
                            className="secondary-button settings-inline-button"
                            onClick={() => void handleRefresh()}
                            disabled={refreshing}
                          >
                            <ArrowPathIcon className={`h-4 w-4 ${refreshing ? 'animate-spin' : ''}`} />
                            <span>{refreshing ? '正在刷新' : '刷新'}</span>
                          </button>
                        }
                      />
                      <SettingsRow
                        itemID="web-connection-entry"
                        title="当前入口"
                        desc={`${secureEntry ? 'HTTPS' : '局域网 HTTP'} · ${window.location.origin || '本地'}`}
                      >
                        <p className="settings-row-note">
                          {secureEntry
                            ? '可在 Safari 的共享菜单选择“添加到主屏幕”。Safari 与主屏幕应用可能使用不同存储，安装后请在应用内重新配对。'
                            : '局域网 HTTP 可在线使用，但不能安装到主屏幕；请在 Mac 设置中配置固定 HTTPS 入口后再安装。'}
                        </p>
                      </SettingsRow>
                      {connectionState === 'connected' && canGenerateHomePairing && (
                        <SettingsRow
                          itemID="web-home-pairing"
                          title="主屏幕配对码"
                          desc="为主屏幕应用生成一次 64 位配对码"
                          error={homePairingError}
                          control={
                            <button
                              type="button"
                              className="secondary-button settings-inline-button"
                              onClick={() => void handleGenerateHomePairingCode()}
                              disabled={homePairingBusy}
                            >
                              {homePairingBusy ? '正在生成…' : '生成'}
                            </button>
                          }
                        >
                          {homePairingCode && (
                            <div className="settings-pairing-code" role="status" aria-live="polite">
                              <p className="settings-note">在主屏幕应用的配对页粘贴此 64 位配对码。有效至 {(() => {
                                const date = new Date(homePairingCode.expiresAt);
                                return Number.isNaN(date.getTime())
                                  ? homePairingCode.expiresAt
                                  : date.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
                              })()}</p>
                              <code className="settings-pairing-code__value">{homePairingCode.token}</code>
                              <button type="button" className="secondary-button" onClick={() => void handleCopyHomePairingCode()}>
                                {homePairingCopied ? '已复制' : '复制配对码'}
                              </button>
                            </div>
                          )}
                        </SettingsRow>
                      )}
                      {connectionState === 'connected' && !canGenerateHomePairing && (
                        <SettingsRow
                          itemID="web-home-pairing"
                          title="主屏幕配对码"
                          desc="当前 Mac 未声明主屏幕配对能力。"
                        />
                      )}
                    </SettingsSection>
                    <SettingsSection title="此设备">
                      {renamingDevice ? (
                        <SettingsStackedRow
                          itemID="web-device-name"
                          title="设备名称"
                          desc="修改这台设备在 Mac 上显示的名字"
                          error={deviceRenameError}
                        >
                          <form
                            className="settings-device-form"
                            onSubmit={(event) => {
                              event.preventDefault();
                              void handleRenameDevice();
                            }}
                          >
                            <input
                              className="settings-input"
                              type="text"
                              value={deviceNameDraft}
                              onChange={(event) => setDeviceNameDraft(event.target.value)}
                              maxLength={64}
                              autoFocus
                              disabled={deviceRenameBusy}
                              aria-label="设备名称"
                            />
                            <div className="settings-device-form__actions">
                              <button
                                type="submit"
                                className="primary-button"
                                disabled={deviceRenameBusy || !deviceNameDraft.trim()}
                              >
                                {deviceRenameBusy ? '保存中…' : '保存'}
                              </button>
                              <button
                                type="button"
                                className="secondary-button"
                                onClick={cancelRenameDevice}
                                disabled={deviceRenameBusy}
                              >
                                取消
                              </button>
                            </div>
                          </form>
                        </SettingsStackedRow>
                      ) : (
                        <SettingsRow
                          itemID="web-device-name"
                          title="设备名称"
                          desc={`当前名称：${deviceIdentity?.label || '当前设备'}`}
                          error={deviceRenameError}
                          control={
                            <button
                              type="button"
                              className="settings-device-edit"
                              onClick={startRenameDevice}
                              disabled={!canRenameDevice}
                              title={canRenameDevice ? '修改设备名称' : '离线或当前 Mac 未声明改名能力'}
                            >
                              编辑
                            </button>
                          }
                        />
                      )}
                      {connectionState === 'connected' && !canRenameDevice && (
                        <SettingsRow desc="当前 Mac 未声明设备改名能力，请升级 Mac 端。" />
                      )}
                      {connectionState !== 'connected' && (
                        <SettingsRow desc="离线时无法修改设备名称，请先连接 Mac。" />
                      )}
                      <SettingsRow
                        itemID="web-device-remember"
                        title="记住此设备"
                        desc={rememberDevice ? '关闭页面后保留配对信息。' : '仅当前标签页有效，关闭后需要重新配对。'}
                        control={
                          <SettingsSwitch
                            checked={rememberDevice}
                            label="记住此设备"
                            onChange={setRememberDevice}
                          />
                        }
                      />
                      <SettingsRow
                        itemID="web-device-forget"
                        title="忘记此设备"
                        desc="撤销 Mac 上的此设备并清除本地配对信息"
                        control={
                          <button
                            type="button"
                            className="settings-action settings-action--danger settings-inline-button"
                            onClick={() => setShowForgetConfirm(true)}
                            disabled={forgetInProgress}
                          >
                            <span>{forgetInProgress ? '正在忘记…' : '忘记'}</span>
                          </button>
                        }
                      />
                      {connectionState === 'auth_failed' && (
                        <SettingsRow
                          title="重新配对"
                          desc="凭据已失效，重新输入配对码"
                          control={
                            <button type="button" className="secondary-button settings-inline-button" onClick={clearAuth}>
                              重新配对
                            </button>
                          }
                        />
                      )}
                    </SettingsSection>
                    <DeviceAppSection />
                    <DevicesSection />
                  </>
                );
              default:
                return null;
            }
          }}
        />
      )}

      {tiersOpen && (
        <LazyBoundary label="正在加载档位…">
          <SwarmTiersSheet isOpen onClose={() => setTiersOpen(false)} />
        </LazyBoundary>
      )}

      {memoryOpen && (
        <LazyBoundary label="正在加载记忆提案…">
          <MemoryProposalsSheet
            isOpen={memoryOpen}
            onClose={() => setMemoryOpen(false)}
            projectID={selectedProjectID}
            projectName={projectName}
          />
        </LazyBoundary>
      )}

      <AttentionSheet
        isOpen={attentionOpen}
        onClose={() => setAttentionOpen(false)}
        projectID={selectedProjectID}
        projectName={projectName}
      />

      <ConfirmDialog
        isOpen={showForgetConfirm}
        title="忘记此设备？"
        message={connectionState === 'connected'
          ? '将撤销 Mac 上的此设备并清除本地配对信息与未发送的草稿，其他设备不受影响。'
          : '当前离线，只会清除本地配对信息与未发送的草稿；请到 Mac 设置中撤销此设备。'}
        confirmLabel={forgetInProgress ? '正在忘记…' : '忘记此设备'}
        confirmVariant="danger"
        isLoading={forgetInProgress}
        onConfirm={() => { void handleForgetDevice(); }}
        onCancel={() => setShowForgetConfirm(false)}
      />
    </>
  );
};
