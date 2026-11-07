import type {
  Board,
  ConnectionState,
  GetDiffResult,
  ThreadsGetDiffResult,
  RemoteCapabilityDeclaration,
  RemoteComposerOptions,
  RemoteProvider,
  RemoteSwarmComposerInfo,
  RemoteProvidersListResult,
  RemoteProjectSummary,
  RemoteThreadHistoryPage,
  RemoteThreadStreamSnapshot,
  RemoteThreadSummary,
  ReviewDecision,
  TaskItem,
  TaskStatus,
  RemoteRequestsListResult,
  RemoteRequestsRespondParams,
  RemoteRequestsRespondResult,
  RemoteThreadsInterruptParams,
  RemoteThreadsInterruptResult,
  RemoteAttachmentUploadResult,
  RemoteThreadsQueueListResult,
  RemoteThreadsQueueMutationResult,
  RemoteThreadsQueueInterruptAndSendResult,
  RemoteThreadsQueueMutationParams,
  RemoteThreadsSendMessageParams,
  RemoteThreadsSendMessageResult,
  ThreadsMoveParams,
  ThreadsUpdateParams,
  ThreadsEventParams,
  RemoteFilesListResult,
  RemoteFilesReadResult,
  RemoteFilesSearchResult,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult,
  RemoteGitStatusResult,
  RemoteGitBranchesResult,
  RemoteThreadsCommandsResult
} from '../types/contract';
import type { CommandScope } from '../utils/slashCommands';
import type { ComposerSelectionMemoryParams } from '../utils/composerOptions';
import type { DeviceIdentity } from '../services/storage';
import type {
  RemoteSwarmMergeDiffResult,
  RemoteThreadsTurnRecallResult,
  RuntimeCompactSuggestionParams,
  RuntimeQueueEditParams,
  RuntimeQueueEditResult,
  SessionNewPairingCodeResult,
  RemoteSwarmTier,
  RemoteSwarmTiersResult,
  RemoteThreadsTitleRegenerateResult,
  RuntimeWorktreeExecuteParams,
  RuntimeWorktreeExecuteResult,
  RuntimeWorktreePreviewParams,
  RuntimeWorktreePreviewResult,
  RuntimeLimitTextResult,
  RemoteThreadsBulkPreviewParams,
  RemoteThreadsBulkPreviewResult,
  RemoteThreadsBulkArchiveParams,
  RemoteThreadsBulkArchiveResult,
  RemoteThreadsBulkMarkDormantParams,
  RemoteThreadsBulkMarkDormantResult,
  RemoteThreadsBulkMarkReadParams,
  RemoteThreadsBulkMarkReadResult,
  RemoteThreadsBulkUndoParams,
  RemoteThreadsBulkUndoResult,
  RemoteThreadsKillRunningCommandParams,
  RemoteThreadsKillRunningCommandResult,
  RemoteThreadsQueueMoveParams
} from '../types/generated';
import type { RemoteSwarmState } from '../types/swarm';
import type { ComposerQuotaScope, RemoteComposerQuotaResult } from '../types/quota';
import type {
  RemoteScriptsFavoriteResult,
  RemoteScriptsListResult,
  RemoteScriptsRunResult,
  RemoteScriptsRunStatus,
  RemoteScriptsRunTarget,
  RemoteScriptsSaveScript
} from './remoteScriptsApi';

export interface RemoteThreadAction {
  actionID: string;
  actionJournalID?: string;
  kind: 'create' | 'send';
  projectID: string;
  threadID?: string;
  text?: string;
  state: 'submitting' | 'pending' | 'accepted' | 'failed' | 'unknown';
  error?: string;
  safeToRecoverDraft?: boolean;
  messageID?: string;
  turnID?: string;
  queued?: boolean;
  followUpID?: string;
  /** 本地命令（/plan、/compact）已在宿主执行：没有 messageID/turnID。 */
  localCommand?: 'plan' | 'compact';
  /** `#模型` 已委派给子代理线程：本会话没有新轮次。 */
  delegatedThreadID?: string;
}
export interface RemoteThreadSubscriptionHandlers {
  onSnapshot: (snapshot: RemoteThreadStreamSnapshot) => void;
  onEvent: (event: Extract<ThreadsEventParams, { kind: 'projection' }>) => void;
  onState: (state: 'subscribing' | 'subscribed' | 'disconnected' | 'error') => void;
  onError: (error: Error) => void;
}

export interface RemoteThreadSubscriptionHandle {
  subscriptionID: number;
  ready: Promise<void>;
}

export type RemoteProviderCatalogItem = {
  provider: string;
  modelID: string;
};

export type RemoteProviderListWithCatalog = RemoteProvider[] & {
  catalog?: RemoteProviderCatalogItem[];
};

export interface RemoteContextType {
  board: Board | null;
  connectionState: ConnectionState;
  connectionMessage: string;
  authError: string | null;
  pairingError: string | null;
  pairingInProgress: boolean;
  rememberDevice: boolean;
  setRememberDevice: (remember: boolean) => void;
  deviceIdentity: DeviceIdentity | null;
  /** 修改当前设备在 Mac 上显示的名称，成功后 deviceIdentity 会随凭据变更刷新。 */
  renameDevice: (name: string) => Promise<void>;
  credentialNotice: string | null;
  forgetInProgress: boolean;
  forgetDevice: () => Promise<void>;
  selectedProjectID: string | null;
  setSelectedProjectID: (id: string | null) => void;
  theme: 'system' | 'light' | 'dark';
  setTheme: (theme: 'system' | 'light' | 'dark') => void;
  activeActionTaskID: string | null;
  actionLoadingText: string | null;
  isSyncing: boolean;
  inspectedTask: TaskItem | null;
  setInspectedTask: (task: TaskItem | null) => void;

  projects: RemoteProjectSummary[];
  projectsLoading: boolean;
  projectsError: string | null;
  environmentID: string;
  threads: RemoteThreadSummary[];
  threadsLoading: boolean;
  threadsLoadingMore: boolean;
  threadsHasMore: boolean;
  threadsError: string | null;
  threadActions: RemoteThreadAction[];
  capabilities: RemoteCapabilityDeclaration | null;
  providers: RemoteProviderListWithCatalog;
  swarm: RemoteSwarmComposerInfo | null;
  providersLoading: boolean;
  providersError: string | null;
  currentThread: RemoteThreadSummary | null;
  openThreadError: string | null;
  setCurrentThread: (thread: RemoteThreadSummary | null) => void;
  requestSnapshots: Record<string, RemoteRequestsListResult>;
  queueSnapshots: Record<string, RemoteThreadsQueueListResult>;

  /** 通用 RPC 调用：按方法名直接调用宿主，无需为每个新方法扩展上下文。 */
  callRemote: <T>(method: string, params?: unknown, timeoutMs?: number) => Promise<T>;
  /** 宿主能力清单是否声明了某方法；清单尚未到位时按可用处理。 */
  supportsMethod: (method: string) => boolean;
  /** 订阅任意方法的服务端通知，返回取消订阅函数。 */
  onRemoteNotification: (method: string, handler: (params: unknown) => void) => () => void;

  reconnect: () => void;
  pairWithToken: (pairToken: string) => void;
  newPairingCode: () => Promise<SessionNewPairingCodeResult>;
  clearAuth: () => void;
  refreshBoard: () => Promise<void>;
  moveTask: (taskID: string, status: TaskStatus, expectedRevision?: number) => Promise<void>;
  startTask: (taskID: string, expectedRevision?: number) => Promise<void>;
  reviewTask: (taskID: string, decision: ReviewDecision, feedback?: string, expectedRevision?: number) => Promise<void>;
  getDiff: (taskID: string) => Promise<GetDiffResult>;
  getThreadDiff: (threadID: string, turnID?: string) => Promise<ThreadsGetDiffResult>;
  getThreadGitStatus: (threadID: string) => Promise<RemoteGitStatusResult>;
  getThreadGitBranches: (threadID: string) => Promise<RemoteGitBranchesResult>;
  listThreadFiles: (threadID: string, subpath?: string) => Promise<RemoteFilesListResult>;
  readThreadFile: (
    threadID: string,
    path: string,
    offset?: number,
    length?: number
  ) => Promise<RemoteFilesReadResult>;
  searchThreadFiles: (threadID: string, query: string) => Promise<RemoteFilesSearchResult>;
  readThreadMedia: (params: RemoteThreadsMediaReadParams) => Promise<RemoteThreadsMediaReadResult>;
  listThreadScripts: (threadID: string) => Promise<RemoteScriptsListResult>;
  runThreadScript: (threadID: string, target: RemoteScriptsRunTarget) => Promise<RemoteScriptsRunResult>;
  getThreadScriptRunStatus: (threadID: string, runID: string) => Promise<RemoteScriptsRunStatus>;
  setThreadScriptFavorite: (threadID: string, name: string, isFavorite: boolean) => Promise<RemoteScriptsFavoriteResult>;
  /** 整表保存项目级脚本：id 为空表示新建；expectedRevision 不一致时抛 ScriptsSaveConflictError。 */
  saveScripts: (
    projectID: string,
    scripts: RemoteScriptsSaveScript[],
    expectedRevision?: string
  ) => Promise<RemoteScriptsListResult>;
  previewBulkThreads: (params: RemoteThreadsBulkPreviewParams) => Promise<RemoteThreadsBulkPreviewResult>;
  archiveBulkThreads: (params: RemoteThreadsBulkArchiveParams) => Promise<RemoteThreadsBulkArchiveResult>;
  markDormantBulkThreads: (params: RemoteThreadsBulkMarkDormantParams) => Promise<RemoteThreadsBulkMarkDormantResult>;
  markBulkThreadsRead: (params: RemoteThreadsBulkMarkReadParams) => Promise<RemoteThreadsBulkMarkReadResult>;
  undoBulkThreads: (params: RemoteThreadsBulkUndoParams) => Promise<RemoteThreadsBulkUndoResult>;
  createTask: (projectID: string, title: string, notes?: string, threadID?: string) => Promise<void>;
  createGoal: (projectID: string, prompt: string) => Promise<void>;
  startGoal: (goalID: string, expectedRevision: number) => Promise<void>;
  pauseGoal: (goalID: string, expectedRevision: number) => Promise<void>;
  cancelGoal: (goalID: string, expectedRevision: number) => Promise<void>;
  replanGoal: (goalID: string, feedback: string, expectedRevision: number) => Promise<void>;
  retryGoalTask: (taskID: string, expectedRevision: number) => Promise<void>;
  skipGoalTask: (taskID: string, expectedRevision: number) => Promise<void>;
  loadProjects: (triggerRevision?: number) => Promise<void>;
  loadThreads: (projectID?: string | null) => Promise<void>;
  loadMoreThreads: () => Promise<void>;
  /** 静默同步当前范围的会话列表：不清空列表、不显示加载态；返回新结果第一页，丢弃或失败时为 null。 */
  resyncThreads: () => Promise<RemoteThreadSummary[] | null>;
  /** 拉取已归档会话（section 为 archived 的行）；不写入主列表。 */
  listArchivedThreads: (projectID?: string) => Promise<RemoteThreadSummary[]>;
  updateThread: (
    threadID: string,
    changes: Omit<ThreadsUpdateParams, 'threadID'>
  ) => Promise<RemoteThreadSummary>;
  moveThread: (params: ThreadsMoveParams) => Promise<RemoteThreadSummary>;
  createThread: (projectID: string, options?: RemoteComposerOptions) => Promise<RemoteThreadAction>;
  sendThreadMessage: (
    threadID: string,
    text: string,
    params?: Pick<RemoteThreadsSendMessageParams, 'whenRunning' | 'options' | 'attachments'>
  ) => Promise<RemoteThreadAction>;
  checkThreadAction: (actionID: string) => Promise<RemoteThreadAction>;
  dismissThreadAction: (actionID: string) => void;
  refreshThread: (threadID: string) => Promise<RemoteThreadSummary | null>;
  subscribeThread: (
    threadID: string,
    handlers: RemoteThreadSubscriptionHandlers
  ) => RemoteThreadSubscriptionHandle;
  retryThreadSubscription: () => Promise<void>;
  unsubscribeThread: (threadID: string, subscriptionID?: number) => void;
  openThread: (threadID: string) => void;
  openThreadByID: (threadID: string) => Promise<void>;
  closeThread: (leaveProject?: boolean) => void;
  getThreadHistory: (
    threadID: string,
    cursor?: string | null,
    limit?: number
  ) => Promise<RemoteThreadHistoryPage>;
  listRequests: (threadID: string) => Promise<RemoteRequestsListResult>;
  respondToRequest: (params: RemoteRequestsRespondParams) => Promise<RemoteRequestsRespondResult>;
  interruptThread: (params: RemoteThreadsInterruptParams) => Promise<RemoteThreadsInterruptResult>;
  /** 只终止一条工具消息正在运行的命令子进程，不中断本轮。 */
  killRunningCommand: (
    params: RemoteThreadsKillRunningCommandParams
  ) => Promise<RemoteThreadsKillRunningCommandResult>;
  listThreadQueue: (threadID: string) => Promise<RemoteThreadsQueueListResult>;
  updateThreadQueue: (params: Omit<RemoteThreadsQueueMutationParams, 'actionID'>) => Promise<RemoteThreadsQueueMutationResult>;
  removeThreadQueue: (threadID: string, followUpID: string) => Promise<RemoteThreadsQueueMutationResult>;
  /** 调整队列顺序：beforeFollowUpID 为空表示移到末尾（需宿主支持 threads.queue.move）。 */
  moveThreadQueue: (params: Omit<RemoteThreadsQueueMoveParams, 'actionID'>) => Promise<RemoteThreadsQueueMutationResult>;
  sendThreadQueueNow: (threadID: string, followUpID: string) => Promise<RemoteThreadsQueueMutationResult>;
  interruptAndSendThread: (
    threadID: string,
    text: string,
    attachments?: string[],
    actionID?: string
  ) => Promise<RemoteThreadsQueueInterruptAndSendResult>;
  /** 队列条目编辑租借：begin 借出 / save 落盘 / cancel 归还原草稿。 */
  editThreadQueueDraft: (params: RuntimeQueueEditParams) => Promise<RuntimeQueueEditResult>;
  /** 收回最近一轮尚未产生回复的用户提示词。 */
  recallTurn: (threadID: string) => Promise<RemoteThreadsTurnRecallResult>;
  /** 压缩上下文，等价于桌面的 /compact。 */
  compactThread: (threadID: string) => Promise<RemoteThreadsSendMessageResult>;
  /** 把子代理的提问转交主线程回答。 */
  forwardSwarmQuestion: (params: {
    threadID: string;
    beeThreadID: string;
    requestID: string;
    actionID: string;
  }) => Promise<RemoteSwarmState>;
  /** 订阅压缩建议通知（threads.compactSuggestion），连接重建后需重新订阅。 */
  subscribeCompactSuggestion: (listener: (offer: RuntimeCompactSuggestionParams) => void) => () => void;
  uploadAttachment: (file: File) => Promise<RemoteAttachmentUploadResult>;
  /** 传 projectID 时结果带 draft（该项目新建会话的默认值）。 */
  listProviders: (projectID?: string) => Promise<RemoteProvidersListResult>;
  /** 用户主动改动模型/档位/编排选择时写入宿主记忆；返回的 draft 是最新默认值。 */
  rememberProviderSelection: (params: ComposerSelectionMemoryParams) => Promise<RemoteProvidersListResult>;
  getComposerQuota: (scope: ComposerQuotaScope) => Promise<RemoteComposerQuotaResult>;
  getPushPublicKey: () => Promise<{ publicKey: string }>;
  subscribePush: (params: { endpoint: string; p256dh: string; auth: string }) => Promise<{ subscribed: boolean }>;
  unsubscribePush: (endpoint: string) => Promise<{ unsubscribed: boolean }>;
  branchThread: (params: { threadID: string; messageID: string; actionID: string }) => Promise<{ threadID: string }>;
  forwardThread: (params: { threadID: string; actionID: string }) => Promise<{ threadID: string }>;
  /** `/` 命令清单快照，按 commandScopeKey 缓存。 */
  commandSnapshots: Record<string, RemoteThreadsCommandsResult>;
  /** 拉取（并缓存）命令清单；宿主仍在后台拉取时自动重试。 */
  loadThreadCommands: (scope: CommandScope, force?: boolean) => Promise<RemoteThreadsCommandsResult>;
  getThreadSwarm: (threadID: string) => Promise<RemoteSwarmState>;
  setThreadSwarm: (params: { threadID: string; enabled: boolean; actionID: string }) => Promise<RemoteSwarmState>;
  stopSwarmBee: (params: { threadID: string; beeThreadID: string; actionID: string }) => Promise<RemoteSwarmState>;
  stopAllSwarmBees: (params: { threadID: string; actionID: string }) => Promise<RemoteSwarmState>;
  clearFinishedSwarmBees: (params: { threadID: string; actionID: string }) => Promise<RemoteSwarmState>;
  /** 读取全局子代理档位配置。 */
  getSwarmTiers: () => Promise<RemoteSwarmTiersResult>;
  /** 整表保存全局子代理档位配置（宿主侧校验与桌面设置页一致）。 */
  setSwarmTiers: (params: { tiers: RemoteSwarmTier[]; actionID: string }) => Promise<RemoteSwarmTiersResult>;
  /** 把子代理的提问转交主线程回答。 */
  forwardQuestion: (params: {
    threadID: string;
    beeThreadID: string;
    requestID: string;
    actionID: string;
  }) => Promise<RemoteSwarmState>;
  /** 读取单个子代理待合并改动的 diff。 */
  getMergeDiff: (params: {
    threadID: string;
    beeThreadID: string;
  }) => Promise<RemoteSwarmMergeDiffResult>;
  approveMerge: (params: { threadID: string; beeThreadID: string; actionID: string }) => Promise<RemoteSwarmState>;
  discardMerge: (params: { threadID: string; beeThreadID: string; actionID: string }) => Promise<RemoteSwarmState>;
  addSwarmListener: (listener: (state: RemoteSwarmState) => void) => () => void;
  /** 设置会话级编排工作模式。 */
  setThreadSwarmMode: (params: {
    threadID: string;
    mode: NonNullable<RemoteThreadSummary['swarmMode']>;
    actionID: string;
  }) => Promise<RemoteThreadSummary>;
  /** 设置/清除（null）会话级「主代理只规划」覆盖。 */
  setThreadSwarmPlansOnly: (params: {
    threadID: string;
    plansOnly: boolean | null;
    actionID: string;
  }) => Promise<RemoteSwarmState>;
  /** 保存会话级子代理档位覆盖；beeTiers 为按档位 ID 序列化的 JSON，空串表示清除。 */
  setThreadSwarmBeeTiers: (params: {
    threadID: string;
    beeTiers: string;
    actionID?: string;
  }) => Promise<RemoteSwarmState>;
  /** 重新生成会话标题。 */
  regenerateThreadTitle: (threadID: string) => Promise<RemoteThreadsTitleRegenerateResult>;
  /** 生成额度用尽时的精简交接提示词。 */
  limitProviderText: (threadID: string) => Promise<RuntimeLimitTextResult>;
  /** 预览把会话移到 worktree 的条件、建议分支名与将转移的文件。 */
  previewWorktreeMove: (params: RuntimeWorktreePreviewParams) => Promise<RuntimeWorktreePreviewResult>;
  /** 新建 Worktree 并（可选）转移未提交改动。 */
  executeWorktreeMove: (params: RuntimeWorktreeExecuteParams) => Promise<RuntimeWorktreeExecuteResult>;
}
