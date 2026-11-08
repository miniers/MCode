// Generated from docs/remote-control.schema.json; run npm run generate:contract.

export type UUID = string;

export type ISO8601DateTime = string;

export type TaskStatus = "todo" | "in_progress" | "needs_approval" | "review" | "done" | "rejected";

export type TaskPriority = "urgent" | "high" | "medium" | "low" | "none";

export type ReviewDecision = "accept" | "reject" | "requestReview";

export type KanbanDisplayColumn = "todo" | "in_progress" | "needs_you" | "review" | "done";

export type GoalStatus = "planning" | "plan_failed" | "plan_review" | "running" | "paused" | "completed" | "cancelled";

export type RemoteThreadMessageKind = "user" | "assistant" | "tool" | "thinking" | "info" | "warning" | "error" | "plan" | "todo" | "swarm_report" | "swarm_task" | "question_answer" | "host_event" | "steward_wake";

export type RemoteTurnStatus = "running" | "completed" | "interrupted" | "failed";

export type JSONRPCId = string | number | null;

export type RPCErrorObject = {
  code: number;
  message: string;
  data?: unknown;
};

export type JSONRPCErrorResponse = {
  jsonrpc: "2.0";
  error: RPCErrorObject;
  id: JSONRPCId;
};

export type Project = {
  id: UUID;
  name: string;
};

export type Task = {
  id: UUID;
  projectID: string | null;
  threadID: string | null;
  reviewThreadID: string | null;
  goalID: string | null;
  dependsOn: Array<UUID>;
  title: string;
  notes: string;
  acceptanceCriteria?: string;
  status: TaskStatus;
  priority: TaskPriority;
  updatedAt: ISO8601DateTime;
  blocked: boolean;
  reviewSummary: string | null;
  reviewFeedback: string | null;
  reviewStatus: string | null;
  canStart: boolean;
  canMove: boolean;
  canReview: boolean;
  canApprove: boolean;
  attentionFlags: Array<string>;
  displayColumn: KanbanDisplayColumn;
  lastActivityText: string | null;
  attemptCount: number;
  rejectCount?: number;
  lastFailureReason: string | null;
  revision: number;
};

export type GoalPolicySummary = {
  maxConcurrency: number;
  previewPlan: boolean;
  autoAccept: boolean;
  maxAttempts: number;
};

export type GoalProgress = {
  total: number;
  done: number;
  running: number;
  waitingOnYou: number;
  failed: number;
  blocked: number;
  ready: number;
  fraction: number;
  needsAttention: boolean;
};

export type Goal = {
  id: UUID;
  projectID: UUID;
  title: string;
  prompt: string;
  status: GoalStatus;
  owner?: string;
  policy: GoalPolicySummary;
  progress: GoalProgress;
  planThreadID: string | null;
  planSummary: string | null;
  statusReason: string | null;
  resultSummary: string | null;
  revision: number;
  updatedAt: ISO8601DateTime;
};

export type Board = {
  schemaVersion: 1;
  revision: number;
  projects: Array<Project>;
  tasks: Array<Task>;
  goals: Array<Goal>;
};

export type SessionPairParams = {
  token: string;
};

export type SessionPairResult = {
  sessionToken: string;
  deviceID: UUID;
  label: string;
};

export type SessionPairRequest = {
  jsonrpc: "2.0";
  method: "session.pair";
  params: SessionPairParams;
  id: JSONRPCId;
};

export type SessionPairResponse = {
  jsonrpc: "2.0";
  result: SessionPairResult;
  id: JSONRPCId;
};

export type SessionAuthenticateParams = {
  token: string;
};

export type SessionAuthenticateResult = {
  authenticated: true;
  deviceID: string;
  label: string;
};

export type SessionAuthenticateRequest = {
  jsonrpc: "2.0";
  method: "session.authenticate";
  params: SessionAuthenticateParams;
  id: JSONRPCId;
};

export type SessionAuthenticateResponse = {
  jsonrpc: "2.0";
  result: SessionAuthenticateResult;
  id: JSONRPCId;
};

export type SessionNewPairingCodeParams = Record<string, never>;

export type SessionNewPairingCodeResult = {
  requested: true;
  pairingURL: string;
  lanPairingURL?: string;
  tailscalePairingURL?: string;
  expiresAt: ISO8601DateTime;
};

export type SessionNewPairingCodeRequest = {
  jsonrpc: "2.0";
  method: "session.newPairingCode";
  params?: SessionNewPairingCodeParams;
  id: JSONRPCId;
};

export type SessionNewPairingCodeResponse = {
  jsonrpc: "2.0";
  result: SessionNewPairingCodeResult;
  id: JSONRPCId;
};

export type SessionRevokeDeviceParams = {
  deviceID: UUID;
};

export type SessionRevokeDeviceResult = {
  deviceID: UUID;
  revoked: boolean;
};

export type SessionRevokeDeviceRequest = {
  jsonrpc: "2.0";
  method: "session.revokeDevice";
  params: SessionRevokeDeviceParams;
  id: JSONRPCId;
};

export type SessionRevokeDeviceResponse = {
  jsonrpc: "2.0";
  result: SessionRevokeDeviceResult;
  id: JSONRPCId;
};

export type SessionRenameDeviceParams = {
  name: string;
};

export type SessionRenameDeviceResult = {
  deviceID: UUID;
  label: string;
};

export type SessionRenameDeviceRequest = {
  jsonrpc: "2.0";
  method: "session.renameDevice";
  params: SessionRenameDeviceParams;
  id: JSONRPCId;
};

export type SessionRenameDeviceResponse = {
  jsonrpc: "2.0";
  result: SessionRenameDeviceResult;
  id: JSONRPCId;
};

export type AppUpdateParams = Record<string, never>;

export type AppUpdateStatus = {
  current: string;
  latest?: string;
  available: boolean;
  installable: boolean;
  url?: string;
  notes?: string;
};

export type AppUpdateInstallResult = {
  started: boolean;
};

export type AppUpdateGetRequest = {
  jsonrpc: "2.0";
  method: "app.update.get";
  params: AppUpdateParams;
  id: JSONRPCId;
};

export type AppUpdateGetResponse = {
  jsonrpc: "2.0";
  result: AppUpdateStatus;
  id: JSONRPCId;
};

export type AppUpdateCheckRequest = {
  jsonrpc: "2.0";
  method: "app.update.check";
  params: AppUpdateParams;
  id: JSONRPCId;
};

export type AppUpdateCheckResponse = {
  jsonrpc: "2.0";
  result: AppUpdateStatus;
  id: JSONRPCId;
};

export type AppUpdateInstallRequest = {
  jsonrpc: "2.0";
  method: "app.update.install";
  params: AppUpdateParams;
  id: JSONRPCId;
};

export type AppUpdateInstallResponse = {
  jsonrpc: "2.0";
  result: AppUpdateInstallResult;
  id: JSONRPCId;
};

export type SessionDevicesListParams = Record<string, never>;

export type SessionDeviceEntry = {
  deviceID: UUID;
  label: string;
  pairedAt: ISO8601DateTime;
  lastSeenAt: string;
  entry?: "lan" | "tls" | "tailscale";
  platform?: string;
  userAgent?: string;
  lastAddress?: string;
  online: boolean;
  current: boolean;
};

export type SessionDevicesListResult = {
  devices: Array<SessionDeviceEntry>;
};

export type SessionDevicesListRequest = {
  jsonrpc: "2.0";
  method: "session.devices.list";
  params: SessionDevicesListParams;
  id: JSONRPCId;
};

export type SessionDevicesListResponse = {
  jsonrpc: "2.0";
  result: SessionDevicesListResult;
  id: JSONRPCId;
};

export type SessionDevicesRevokeParams = {
  deviceID: UUID;
};

export type SessionDevicesRevokeResult = {
  deviceID: UUID;
  revoked: boolean;
};

export type SessionDevicesRevokeRequest = {
  jsonrpc: "2.0";
  method: "session.devices.revoke";
  params: SessionDevicesRevokeParams;
  id: JSONRPCId;
};

export type SessionDevicesRevokeResponse = {
  jsonrpc: "2.0";
  result: SessionDevicesRevokeResult;
  id: JSONRPCId;
};

export type KanbanGetBoardRequest = {
  jsonrpc: "2.0";
  method: "kanban.getBoard";
  params?: Record<string, never>;
  id: JSONRPCId;
};

export type KanbanGetBoardResponse = {
  jsonrpc: "2.0";
  result: Board;
  id: JSONRPCId;
};

export type KanbanMoveTaskParams = {
  taskID: UUID;
  status: TaskStatus;
  expectedRevision: number;
  actionID: UUID;
};

export type KanbanMoveTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.moveTask";
  params: KanbanMoveTaskParams;
  id: JSONRPCId;
};

export type KanbanStartTaskParams = {
  taskID: UUID;
  expectedRevision: number;
  actionID: UUID;
};

export type KanbanStartTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.startTask";
  params: KanbanStartTaskParams;
  id: JSONRPCId;
};

export type KanbanReviewTaskParams = {
  taskID: UUID;
  decision: ReviewDecision;
  feedback?: string;
  expectedRevision: number;
  actionID: UUID;
};

export type KanbanReviewTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.reviewTask";
  params: KanbanReviewTaskParams;
  id: JSONRPCId;
};

export type KanbanCreateTaskParams = {
  projectID: UUID;
  title: string;
  notes?: string | null;
  threadID?: UUID | null;
  actionID: UUID;
};

export type KanbanCreateTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.createTask";
  params: KanbanCreateTaskParams;
  id: JSONRPCId;
};

export type KanbanTaskMutationResult = {
  actionID: UUID;
  taskID: string | null;
  goalID: string | null;
  revision: number | null;
};

export type KanbanTaskMutationResponse = {
  jsonrpc: "2.0";
  result: KanbanTaskMutationResult;
  id: JSONRPCId;
};

export type GoalCreateParams = {
  projectID: UUID;
  prompt: string;
  actionID: UUID;
};

export type GoalCreateRequest = {
  jsonrpc: "2.0";
  method: "goals.create";
  params: GoalCreateParams;
  id: JSONRPCId;
};

export type GoalMutationParams = {
  goalID: UUID;
  expectedRevision: number;
  actionID: UUID;
};

export type GoalTaskMutationParams = {
  taskID: UUID;
  expectedRevision: number;
  actionID: UUID;
};

export type GoalReplanParams = {
  goalID: UUID;
  feedback: string;
  expectedRevision: number;
  actionID: UUID;
};

export type GoalStartRequest = {
  jsonrpc: "2.0";
  method: "goals.start";
  params: GoalMutationParams;
  id: JSONRPCId;
};

export type GoalPauseRequest = {
  jsonrpc: "2.0";
  method: "goals.pause";
  params: GoalMutationParams;
  id: JSONRPCId;
};

export type GoalCancelRequest = {
  jsonrpc: "2.0";
  method: "goals.cancel";
  params: GoalMutationParams;
  id: JSONRPCId;
};

export type GoalReplanRequest = {
  jsonrpc: "2.0";
  method: "goals.replan";
  params: GoalReplanParams;
  id: JSONRPCId;
};

export type GoalRetryTaskRequest = {
  jsonrpc: "2.0";
  method: "goals.retryTask";
  params: GoalTaskMutationParams;
  id: JSONRPCId;
};

export type GoalSkipTaskRequest = {
  jsonrpc: "2.0";
  method: "goals.skipTask";
  params: GoalTaskMutationParams;
  id: JSONRPCId;
};

export type KanbanGetDiffParams = {
  taskID: UUID;
};

export type KanbanGetDiffRequest = {
  jsonrpc: "2.0";
  method: "kanban.getDiff";
  params: KanbanGetDiffParams;
  id: JSONRPCId;
};

export type KanbanGetDiffResult = {
  taskID: UUID;
  diff: string;
  truncated: boolean;
};

export type KanbanGetDiffResponse = {
  jsonrpc: "2.0";
  result: KanbanGetDiffResult;
  id: JSONRPCId;
};

export type KanbanTaskUpdatedNotification = {
  jsonrpc: "2.0";
  method: "kanban.taskUpdated";
  params: Board;
};

export type RemoteProjectSummary = {
  id: UUID;
  name: string;
  path: string;
  isExpanded: boolean;
  threadCount: number;
};

export type RemoteThreadSummary = {
  id: UUID;
  projectID: UUID;
  title: string;
  provider: string;
  model: string | null;
  effort: string | null;
  isRunning: boolean;
  isArchived: boolean;
  isPinned: boolean;
  isDormant: boolean;
  hasUnread: boolean;
  attention: string;
  lastActivityText: string | null;
  lastErrorText?: string;
  section: string;
  orderKey: string;
  updatedAt: ISO8601DateTime;
  lastUserMessageAt?: ISO8601DateTime;
  lastStatus: string | null;
  hasPendingRequest: boolean;
  taskID: string | null;
  goalID: string | null;
  runtimeMode: "readonly" | "supervised" | "auto" | "fullAccess";
  interactionMode: "build" | "plan";
  persona?: string;
  contextUsedTokens?: number;
  contextWindow?: number;
  lastTurnCostUSD?: number;
  fastMode: boolean;
  worktreeRequested: boolean;
  swarmEnabled: boolean;
  swarmPairID: string | null;
  swarmBeeTiers?: string;
  swarmMode?: "inherited" | "collaborative" | "leadPlansOnly" | "alwaysBees";
  swarmBeesProvider: string | null;
  swarmBeesModel: string | null;
  swarmBeesEffort: string | null;
  isProvisional?: boolean;
  parentThreadID?: string | null;
  forkedFromThreadID?: string | null;
  forkedFromMessageID?: string | null;
  isHelper?: boolean;
  isSteward?: boolean;
  swarmBusy?: boolean;
  backgroundTasks?: number;
  mcpDisabled?: Array<string>;
};

export type RemoteThreadExecutionStatus = {
  threadID: UUID;
  isRunning: boolean;
  phase: string;
  turnCount: number;
  lastStatus: string | null;
};

export type RemoteThreadMessage = {
  id: UUID;
  role: string;
  text: string;
  createdAt: ISO8601DateTime;
  turnIndex: number;
  turnID?: UUID;
  kind?: RemoteThreadMessageKind;
  turnStatus?: RemoteTurnStatus;
  turnStartedAt?: string;
  turnCompletedAt?: string;
  isFinalResponse?: boolean;
  seq?: number;
  toolName?: string;
  toolInput?: string;
  toolStatus?: "running" | "completed" | "failed" | "blocked" | "background";
  toolCompletedAt?: string;
  errKind?: string;
  media?: Array<RemoteMediaRef>;
};

export type RemoteThreadHistoryPage = {
  threadID: UUID;
  messages: Array<RemoteThreadMessage>;
  nextCursor: string | null;
  hasMore: boolean;
  truncated: boolean;
};

export type RemoteThreadStreamSnapshot = {
  threadID: UUID;
  history: RemoteThreadHistoryPage;
  thread: RemoteThreadSummary;
  runID: string;
  sequence: number;
};

export type RemoteThreadsProjectionPayload = {
  messages: Array<RemoteThreadMessage>;
  removedMessageIDs: Array<UUID>;
  thread: RemoteThreadSummary;
};

export type RemoteThreadsResyncPayload = {
  thread: RemoteThreadSummary;
};

export type RemoteCapabilityDeclaration = {
  schemaVersion: number;
  methods: Array<string>;
  features: Array<string>;
  webBuildID: string;
  actionJournalID?: string;
  appVersion?: string;
};

export type RemoteProjectsListResult = {
  projects: Array<RemoteProjectSummary>;
  revision: number;
};

export type RemoteProjectsListRequest = {
  jsonrpc: "2.0";
  method: "projects.list";
  params?: Record<string, never>;
  id: JSONRPCId;
};

export type RemoteProjectsListResponse = {
  jsonrpc: "2.0";
  result: RemoteProjectsListResult;
  id: JSONRPCId;
};

export type RemoteThreadsListParams = {
  projectID?: string | null;
  includeArchived?: boolean;
  cursor?: string | null;
  limit?: number;
};

export type RemoteThreadsListResult = {
  threads: Array<RemoteThreadSummary>;
  revision: number;
  truncated: boolean;
  truncationReason: string | null;
  nextCursor: string | null;
  hasMore: boolean;
};

export type RemoteThreadsListRequest = {
  jsonrpc: "2.0";
  method: "threads.list";
  params: RemoteThreadsListParams;
  id: JSONRPCId;
};

export type RemoteThreadsListResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsListResult;
  id: JSONRPCId;
};

export type RemoteThreadsTitleRegenerateParams = {
  threadId: string;
};

export type RemoteThreadsTitleRegenerateResult = {
  title: string;
};

export type RemoteThreadsTitleRegenerateRequest = {
  jsonrpc: "2.0";
  method: "threads.title.regenerate";
  params: RemoteThreadsTitleRegenerateParams;
  id: JSONRPCId;
};

export type RemoteThreadsTitleRegenerateResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsTitleRegenerateResult;
  id: JSONRPCId;
};

export type RemoteThreadsUpdateParams = {
  threadID: UUID;
  pinned?: boolean;
  dormant?: boolean;
  archived?: boolean;
  read?: boolean;
  title?: string;
};

export type RemoteThreadsUpdateResult = {
  thread: RemoteThreadSummary;
  undoToken?: string;
};

export type RemoteThreadsUpdateRequest = {
  jsonrpc: "2.0";
  method: "threads.update";
  params: RemoteThreadsUpdateParams;
  id: JSONRPCId;
};

export type RemoteThreadsUpdateResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsUpdateResult;
  id: JSONRPCId;
};

export type RemoteThreadsBulkPreviewParams = {
  projectID: UUID;
  before?: ISO8601DateTime;
  includePinned: boolean;
  includeDormant: boolean;
};

export type RemoteThreadsBulkPreviewResult = {
  threadIDs: Array<UUID>;
  skippedRunning: number;
};

export type RemoteThreadsBulkPreviewRequest = {
  jsonrpc: "2.0";
  method: "threads.bulk.preview";
  params: RemoteThreadsBulkPreviewParams;
  id: JSONRPCId;
};

export type RemoteThreadsBulkPreviewResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBulkPreviewResult;
  id: JSONRPCId;
};

export type RemoteThreadsBulkArchiveParams = {
  projectID: UUID;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkArchiveResult = {
  undoToken: string;
  skippedRunning: number;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkArchiveRequest = {
  jsonrpc: "2.0";
  method: "threads.bulk.archive";
  params: RemoteThreadsBulkArchiveParams;
  id: JSONRPCId;
};

export type RemoteThreadsBulkArchiveResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBulkArchiveResult;
  id: JSONRPCId;
};

export type RemoteThreadsBulkMarkDormantParams = {
  projectID: UUID;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkMarkDormantResult = {
  undoToken: string;
  skippedRunning: number;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkMarkDormantRequest = {
  jsonrpc: "2.0";
  method: "threads.bulk.markDormant";
  params: RemoteThreadsBulkMarkDormantParams;
  id: JSONRPCId;
};

export type RemoteThreadsBulkMarkDormantResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBulkMarkDormantResult;
  id: JSONRPCId;
};

export type RemoteThreadsBulkMarkReadParams = {
  projectID: UUID;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkMarkReadResult = {
  undoToken: string;
  threadIDs: Array<UUID>;
};

export type RemoteThreadsBulkMarkReadRequest = {
  jsonrpc: "2.0";
  method: "threads.bulk.markRead";
  params: RemoteThreadsBulkMarkReadParams;
  id: JSONRPCId;
};

export type RemoteThreadsBulkMarkReadResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBulkMarkReadResult;
  id: JSONRPCId;
};

export type RemoteThreadsBulkUndoParams = {
  projectID: UUID;
  token: string;
};

export type RemoteThreadsBulkUndoResult = {
  ok: boolean;
};

export type RemoteThreadsBulkUndoRequest = {
  jsonrpc: "2.0";
  method: "threads.bulk.undo";
  params: RemoteThreadsBulkUndoParams;
  id: JSONRPCId;
};

export type RemoteThreadsBulkUndoResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBulkUndoResult;
  id: JSONRPCId;
};

export type RemoteThreadsRetainParams = {
  threadId: UUID;
};

export type RemoteThreadsRetainResult = Record<string, never>;

export type RemoteThreadsRetainRequest = {
  jsonrpc: "2.0";
  method: "threads.retain";
  params: RemoteThreadsRetainParams;
  id: JSONRPCId;
};

export type RemoteThreadsRetainResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsRetainResult;
  id: JSONRPCId;
};

export type RemoteThreadsProvisionalReleaseParams = {
  threadId: UUID;
};

export type RemoteThreadsProvisionalReleaseResult = Record<string, never>;

export type RemoteThreadsProvisionalReleaseRequest = {
  jsonrpc: "2.0";
  method: "threads.provisional.release";
  params: RemoteThreadsProvisionalReleaseParams;
  id: JSONRPCId;
};

export type RemoteThreadsProvisionalReleaseResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsProvisionalReleaseResult;
  id: JSONRPCId;
};

export type RemoteThreadsMoveParams = {
  threadID: UUID;
  direction: "up" | "down";
};

export type RemoteThreadsMoveResult = {
  thread: RemoteThreadSummary;
  moved: boolean;
};

export type RemoteThreadsMoveRequest = {
  jsonrpc: "2.0";
  method: "threads.move";
  params: RemoteThreadsMoveParams;
  id: JSONRPCId;
};

export type RemoteThreadsMoveResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsMoveResult;
  id: JSONRPCId;
};

export type RemoteThreadsHistoryParams = {
  threadID: UUID;
  cursor?: string | null;
  limit?: number;
};

export type RemoteThreadsHistoryRequest = {
  jsonrpc: "2.0";
  method: "threads.history";
  params: RemoteThreadsHistoryParams;
  id: JSONRPCId;
};

export type RemoteThreadsHistoryResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadHistoryPage;
  id: JSONRPCId;
};

export type RemoteThreadsSubscribeParams = {
  threadID: UUID;
};

export type RemoteThreadsSubscribeResult = {
  threadID: UUID;
  subscribed: boolean;
  snapshot?: RemoteThreadStreamSnapshot;
};

export type RemoteThreadsSubscribeRequest = {
  jsonrpc: "2.0";
  method: "threads.subscribe";
  params: RemoteThreadsSubscribeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSubscribeResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSubscribeResult;
  id: JSONRPCId;
};

export type RemoteThreadsUnsubscribeParams = {
  threadID: UUID;
};

export type RemoteThreadsUnsubscribeResult = {
  threadID: UUID;
  subscribed: boolean;
};

export type RemoteThreadsUnsubscribeRequest = {
  jsonrpc: "2.0";
  method: "threads.unsubscribe";
  params: RemoteThreadsUnsubscribeParams;
  id: JSONRPCId;
};

export type RemoteThreadsUnsubscribeResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsUnsubscribeResult;
  id: JSONRPCId;
};

export type RemoteComposerOptions = {
  provider?: string;
  model?: string;
  effort?: string;
  runtimeMode?: "readonly" | "supervised" | "auto" | "fullAccess";
  interactionMode?: "build" | "plan";
  workspaceMode?: "local" | "worktree";
  fastMode?: boolean;
  swarmEnabled?: boolean;
  swarmPairID?: string;
  swarmBeesProvider?: string;
  swarmBeesModel?: string;
  swarmBeesEffort?: string;
};

export type RemoteProviderModel = {
  id: string;
  name: string;
  detail: string | null;
  efforts: Array<string>;
  defaultEffort: string | null;
  isDefault: boolean;
  supportsFast: boolean;
  preferredEffort: string | null;
  contextLimit?: number;
  secondary?: boolean;
  aliasOf?: string;
  cost?: {
  input: number;
  output: number;
  cacheRead?: number;
  cacheWrite?: number;
};
};

export type RemoteProvider = {
  id: string;
  name: string;
  auth: "signedIn" | "signedOut" | "unknown";
  models: Array<RemoteProviderModel>;
  supportsPlanMode: boolean;
  supportsImages: boolean;
  supportsFast: boolean;
  available: boolean;
  unavailableReason: string | null;
  enabled?: boolean;
};

export type RemoteModelCatalogEntry = {
  provider: string;
  modelID: string;
};

export type RemoteSwarmTierOverride = {
  provider?: string;
  model?: string;
  effort?: string;
  fastMode?: boolean;
};

export type RemoteSwarmPair = {
  id: string;
  name: string | null;
  provider: string;
  leadModel: string | null;
  leadEffort: string | null;
  workerProvider: string | null;
  workerModel: string | null;
  maxBees: number | null;
  beeTiers?: Record<string, RemoteSwarmTierOverride>;
};

export type RemoteSwarmComposerInfo = {
  enabled: boolean;
  defaultEnabled: boolean;
  pairs: Array<RemoteSwarmPair>;
};

export type RemoteComposerDraftDefaults = {
  provider: string;
  model: string | null;
  effort: string | null;
  fastMode: boolean;
  runtimeMode: "readonly" | "supervised" | "auto" | "fullAccess";
  interactionMode: "build" | "plan";
  workspaceMode: "local" | "worktree";
  swarmEnabled: boolean;
  swarmPairID: string | null;
  swarmBeesProvider: string | null;
  swarmBeesModel: string | null;
  swarmBeesEffort: string | null;
};

export type RemoteProvidersListResult = {
  providers: Array<RemoteProvider>;
  catalog?: Array<RemoteModelCatalogEntry>;
  swarm: RemoteSwarmComposerInfo;
  draft?: RemoteComposerDraftDefaults;
};

export type RemoteProvidersListRequest = {
  jsonrpc: "2.0";
  method: "providers.list";
  params?: {
  projectID?: string;
};
  id: JSONRPCId;
};

export type RemoteProvidersListResponse = {
  jsonrpc: "2.0";
  result: RemoteProvidersListResult;
  id: JSONRPCId;
};

export type RemoteProvidersRememberSelectionParams = {
  projectID?: string;
  provider?: string;
  model?: string;
  effort?: string;
  fastMode?: boolean;
  swarmEnabled?: boolean;
  swarmBeesProvider?: string;
  swarmBeesModel?: string;
  swarmBeesEffort?: string;
};

export type RemoteProvidersRememberSelectionRequest = {
  jsonrpc: "2.0";
  method: "providers.rememberSelection";
  params?: RemoteProvidersRememberSelectionParams;
  id: JSONRPCId;
};

export type RemoteProvidersRememberSelectionResponse = {
  jsonrpc: "2.0";
  result: RemoteProvidersListResult;
  id: JSONRPCId;
};

export type RemoteQuotaWindow = {
  id?: string;
  period: string | null;
  title: string;
  usedPercent: number;
  resetsAt: string | null;
  label?: string;
  account?: string;
  active?: boolean;
  disabled?: boolean;
  source?: string;
  plan?: string;
  display?: string;
  text?: boolean;
  unlimited?: boolean;
  balance?: number;
  unit?: string;
  readAt?: string;
};

export type RemoteQuotaBudget = {
  provider: string;
  providerName: string;
  role: "lead" | "bees";
  profile: string | null;
  state: "used" | "stale" | "credits" | "loading" | "attention" | "unknown";
  windows: Array<RemoteQuotaWindow>;
  amount: string | null;
  readAt: string | null;
  account?: string;
  active?: boolean;
};

export type RemoteComposerQuotaResult = {
  budgets: Array<RemoteQuotaBudget>;
};

export type RemoteComposerQuotaRequest = {
  jsonrpc: "2.0";
  method: "composer.quota";
  params?: {
  threadID?: string;
  projectID?: string;
};
  id: JSONRPCId;
};

export type RemoteComposerQuotaResponse = {
  jsonrpc: "2.0";
  result: RemoteComposerQuotaResult;
  id: JSONRPCId;
};

export type RemotePushPublicKeyResult = {
  publicKey: string;
};

export type RemotePushPublicKeyRequest = {
  jsonrpc: "2.0";
  method: "push.publicKey";
  params?: Record<string, never>;
  id: JSONRPCId;
};

export type RemotePushPublicKeyResponse = {
  jsonrpc: "2.0";
  result: RemotePushPublicKeyResult;
  id: JSONRPCId;
};

export type RemotePushSubscribeResult = {
  subscribed: boolean;
};

export type RemotePushSubscribeRequest = {
  jsonrpc: "2.0";
  method: "push.subscribe";
  params: {
  endpoint: string;
  p256dh: string;
  auth: string;
};
  id: JSONRPCId;
};

export type RemotePushSubscribeResponse = {
  jsonrpc: "2.0";
  result: RemotePushSubscribeResult;
  id: JSONRPCId;
};

export type RemotePushUnsubscribeResult = {
  unsubscribed: boolean;
};

export type RemotePushUnsubscribeRequest = {
  jsonrpc: "2.0";
  method: "push.unsubscribe";
  params: {
  endpoint: string;
};
  id: JSONRPCId;
};

export type RemotePushUnsubscribeResponse = {
  jsonrpc: "2.0";
  result: RemotePushUnsubscribeResult;
  id: JSONRPCId;
};

export type RemoteThreadsCreateParams = {
  projectID: UUID;
  actionID: UUID;
  options?: RemoteComposerOptions;
  actionJournalID?: string;
  provisional?: boolean;
  mcpDisabled?: Array<string>;
};

export type RemoteThreadsCreateResult = {
  actionID: UUID;
  threadID: UUID;
};

export type RemoteThreadsCreateRequest = {
  jsonrpc: "2.0";
  method: "threads.create";
  params: RemoteThreadsCreateParams;
  id: JSONRPCId;
};

export type RemoteThreadsCreateResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsCreateResult;
  id: JSONRPCId;
};

export type RemoteThreadsSendMessageParams = {
  threadID: UUID;
  text?: string;
  actionID: UUID;
  whenRunning?: "queue";
  options?: RemoteComposerOptions;
  attachments?: Array<UUID>;
  actionJournalID?: string;
};

export type RemoteThreadsSendMessageResult = {
  actionID: UUID;
  threadID: UUID;
  messageID?: UUID;
  turnID?: UUID;
  queued?: boolean;
  followUpID?: UUID;
  localCommand?: "plan" | "compact";
  delegatedThreadID?: UUID;
};

export type RemoteThreadsSendMessageRequest = {
  jsonrpc: "2.0";
  method: "threads.sendMessage";
  params: RemoteThreadsSendMessageParams;
  id: JSONRPCId;
};

export type RemoteThreadsSendMessageResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSendMessageResult;
  id: JSONRPCId;
};

export type RemoteThreadsSplitSessionParams = {
  threadID: UUID;
};

export type RemoteThreadsSplitSessionResult = {
  threadID: UUID;
  pending: boolean;
};

export type RemoteThreadsSplitSessionRequest = {
  jsonrpc: "2.0";
  method: "threads.splitSession";
  params: RemoteThreadsSplitSessionParams;
  id: JSONRPCId;
};

export type RemoteThreadsSplitSessionResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSplitSessionResult;
  id: JSONRPCId;
};

export type RemoteAttachment = {
  id: UUID;
  name: string;
  mimeType: string;
  byteCount: number | null;
};

export type RemoteFollowUp = {
  id: UUID;
  text: string;
  attachments: Array<RemoteAttachment>;
  createdAt: ISO8601DateTime;
  bundleID: UUID | null;
  system?: boolean;
  intent?: "queue" | "insert";
};

export type RemoteThreadsQueueListResult = {
  threadID: UUID;
  revision: number;
  followUps: Array<RemoteFollowUp>;
};

export type RemoteThreadsQueueListParams = {
  threadID: UUID;
};

export type RemoteThreadsQueueListRequest = {
  jsonrpc: "2.0";
  method: "threads.queue.list";
  params: RemoteThreadsQueueListParams;
  id: JSONRPCId;
};

export type RemoteThreadsQueueListResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsQueueListResult;
  id: JSONRPCId;
};

export type RemoteThreadsQueueMutationParams = {
  threadID: UUID;
  followUpID: UUID;
  text?: string;
  attachments?: Array<UUID>;
  actionID: UUID;
};

export type RemoteThreadsQueueMutationResult = {
  actionID: UUID;
  threadID: UUID;
  revision: number;
  followUps: Array<RemoteFollowUp>;
  removedFollowUpIDs: Array<UUID>;
};

export type RemoteThreadsQueueMutationRequest = {
  jsonrpc: "2.0";
  method: "threads.queue.update" | "threads.queue.remove" | "threads.queue.sendNow";
  params: RemoteThreadsQueueMutationParams;
  id: JSONRPCId;
};

export type RemoteThreadsQueueMutationResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsQueueMutationResult;
  id: JSONRPCId;
};

export type RemoteThreadsQueueMoveParams = {
  threadID: UUID;
  followUpID: UUID;
  beforeFollowUpID?: UUID;
  actionID: UUID;
};

export type RemoteThreadsQueueMoveRequest = {
  jsonrpc: "2.0";
  method: "threads.queue.move";
  params: RemoteThreadsQueueMoveParams;
  id: JSONRPCId;
};

export type RemoteThreadsQueueMoveResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsQueueMutationResult;
  id: JSONRPCId;
};

export type RemoteThreadsMCPDisabledSetParams = {
  threadID: UUID;
  keys: Array<string>;
  actionID: UUID;
};

export type RemoteThreadsMCPDisabledSetRequest = {
  jsonrpc: "2.0";
  method: "threads.mcpDisabled.set";
  params: RemoteThreadsMCPDisabledSetParams;
  id: JSONRPCId;
};

export type RemoteThreadsMCPDisabledSetResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsMCPDisabledSetResult;
  id: JSONRPCId;
};

export type RemoteThreadsMCPDisabledSetResult = {
  threadID: UUID;
  keys: Array<string>;
};

export type RemoteThreadsQueueInterruptAndSendParams = {
  threadID: UUID;
  text?: string;
  attachments?: Array<UUID>;
  actionID: UUID;
};

export type RemoteThreadsQueueInterruptAndSendResult = {
  actionID: UUID;
  threadID: UUID;
  state: "stopping" | "queued" | "sent_to_swarm";
  turnID?: UUID;
  followUpID?: UUID;
  queue: RemoteThreadsQueueListResult;
};

export type RemoteThreadsQueueInterruptAndSendRequest = {
  jsonrpc: "2.0";
  method: "threads.queue.interruptAndSend";
  params: RemoteThreadsQueueInterruptAndSendParams;
  id: JSONRPCId;
};

export type RemoteThreadsQueueInterruptAndSendResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsQueueInterruptAndSendResult;
  id: JSONRPCId;
};

export type RemoteAttachmentUploadParams = {
  name: string;
  mimeType?: string;
  data: string;
};

export type RemoteAttachmentUploadResult = {
  attachmentID: UUID;
  name: string;
  mimeType: string;
  byteCount: number;
  expiresAt: ISO8601DateTime;
};

export type RemoteAttachmentUploadRequest = {
  jsonrpc: "2.0";
  method: "attachments.upload";
  params: RemoteAttachmentUploadParams;
  id: JSONRPCId;
};

export type RemoteAttachmentUploadResponse = {
  jsonrpc: "2.0";
  result: RemoteAttachmentUploadResult;
  id: JSONRPCId;
};

export type RemoteActionState = "pending" | "accepted" | "failed" | "unknown";

export type RemoteActionError = {
  code: number;
  message: string;
};

export type RemoteThreadsActionStatusParams = {
  actionID: UUID;
};

export type RemoteThreadsActionStatusResult = {
  actionID: UUID;
  state: RemoteActionState;
  threadID?: UUID;
  messageID?: UUID;
  turnID?: UUID;
  followUpID?: UUID;
  taskID?: string | null;
  goalID?: string | null;
  revision?: number | null;
  error?: RemoteActionError;
};

export type RemoteThreadsActionStatusRequest = {
  jsonrpc: "2.0";
  method: "threads.actionStatus";
  params: RemoteThreadsActionStatusParams;
  id: JSONRPCId;
};

export type RemoteThreadsActionStatusResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsActionStatusResult;
  id: JSONRPCId;
};

export type RemoteThreadsSettleActionParams = {
  actionID: UUID;
  actionJournalID: string;
  projectID?: UUID;
  threadID?: UUID;
};

export type RemoteThreadsSettleActionResult = {
  actionID: UUID;
  state: RemoteActionState;
  safeToRecoverDraft: boolean;
  threadID?: UUID;
  messageID?: UUID;
  turnID?: UUID;
  followUpID?: UUID;
  taskID?: string | null;
  goalID?: string | null;
  revision?: number | null;
  error?: RemoteActionError;
};

export type RemoteThreadsSettleActionRequest = {
  jsonrpc: "2.0";
  method: "threads.settleAction";
  params: RemoteThreadsSettleActionParams;
  id: JSONRPCId;
};

export type RemoteThreadsSettleActionResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSettleActionResult;
  id: JSONRPCId;
};

export type RemoteRequestOption = {
  id: string;
  title: string;
  role: "approve" | "approveAlways" | "decline" | "cancel";
};

export type RemoteQuestionChoice = {
  label: string;
  detail: string | null;
};

export type RemoteQuestion = {
  id: string;
  header: string;
  prompt: string;
  choices: Array<RemoteQuestionChoice>;
  allowsMultiple: boolean;
  allowsOther: boolean;
  isSecret: boolean;
};

export type RemoteRequest = {
  threadID: UUID;
  turnID: string | null;
  runID: string;
  requestID: string;
  kind: "command" | "fileChange" | "tool" | "permissions" | "plan" | "commit" | "question";
  title: string | null;
  detail: string | null;
  reason: string | null;
  deliveryError?: string | null;
  options: Array<RemoteRequestOption>;
  questions: Array<RemoteQuestion>;
  diff?: string;
};

export type RemoteRequestsListResult = {
  threadID: UUID;
  requests: Array<RemoteRequest>;
};

export type RemoteRequestsListParams = {
  threadID: UUID;
};

export type RemoteRequestsListRequest = {
  jsonrpc: "2.0";
  method: "requests.list";
  params: RemoteRequestsListParams;
  id: JSONRPCId;
};

export type RemoteRequestsListResponse = {
  jsonrpc: "2.0";
  result: RemoteRequestsListResult;
  id: JSONRPCId;
};

export type RemoteQuestionAnswers = Record<string, Array<string>>;

export type RemoteRequestsRespondParams = {
  threadID: UUID;
  requestID: string;
  turnID: UUID;
  runID: string;
  optionID?: string;
  answers?: RemoteQuestionAnswers;
  skip?: true;
};

export type RemoteRequestsRespondResult = {
  threadID: UUID;
  requestID: string;
  kind: string;
  status: "accepted";
};

export type RemoteRequestsRespondRequest = {
  jsonrpc: "2.0";
  method: "requests.respond";
  params: RemoteRequestsRespondParams;
  id: JSONRPCId;
};

export type RemoteRequestsRespondResponse = {
  jsonrpc: "2.0";
  result: RemoteRequestsRespondResult;
  id: JSONRPCId;
};

export type RemoteThreadsGetParams = {
  threadID: UUID;
};

export type RemoteThreadsGetResult = {
  thread: RemoteThreadSummary;
};

export type RemoteThreadsGetRequest = {
  jsonrpc: "2.0";
  method: "threads.get";
  params: RemoteThreadsGetParams;
  id: JSONRPCId;
};

export type RemoteThreadsGetResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsGetResult;
  id: JSONRPCId;
};

export type RemoteThreadsInterruptParams = {
  threadID: UUID;
  turnID: UUID;
};

export type RemoteThreadsInterruptResult = {
  threadID: UUID;
  turnID: UUID;
  state: "stopping" | "already_stopped";
};

export type RemoteThreadsInterruptRequest = {
  jsonrpc: "2.0";
  method: "threads.interrupt";
  params: RemoteThreadsInterruptParams;
  id: JSONRPCId;
};

export type RemoteThreadsInterruptResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsInterruptResult;
  id: JSONRPCId;
};

export type RemoteThreadsKillRunningCommandParams = {
  threadID: UUID;
  messageID?: UUID;
};

export type RemoteThreadsKillRunningCommandResult = {
  killed: number;
};

export type RemoteThreadsKillRunningCommandRequest = {
  jsonrpc: "2.0";
  method: "threads.killRunningCommand";
  params: RemoteThreadsKillRunningCommandParams;
  id: JSONRPCId;
};

export type RemoteThreadsKillRunningCommandResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsKillRunningCommandResult;
  id: JSONRPCId;
};

export type RemoteThreadsBackgroundTaskLogParams = {
  threadID: UUID;
  messageID: UUID;
};

export type RemoteThreadsBackgroundTaskLogResult = {
  text: string;
  available: boolean;
};

export type RemoteThreadsBackgroundTaskLogRequest = {
  jsonrpc: "2.0";
  method: "threads.backgroundTaskLog";
  params: RemoteThreadsBackgroundTaskLogParams;
  id: JSONRPCId;
};

export type RemoteThreadsBackgroundTaskLogResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBackgroundTaskLogResult;
  id: JSONRPCId;
};

export type RemoteThreadsStopBackgroundTaskParams = {
  threadID: UUID;
  messageID: UUID;
};

export type RemoteThreadsStopBackgroundTaskResult = {
  stopped: boolean;
};

export type RemoteThreadsStopBackgroundTaskRequest = {
  jsonrpc: "2.0";
  method: "threads.stopBackgroundTask";
  params: RemoteThreadsStopBackgroundTaskParams;
  id: JSONRPCId;
};

export type RemoteThreadsStopBackgroundTaskResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsStopBackgroundTaskResult;
  id: JSONRPCId;
};

export type RemotePersona = {
  id: string;
  name: string;
  description?: string;
};

export type RemoteProvidersPersonasParams = {
  provider: string;
  model: string;
  force?: boolean;
};

export type RemoteProvidersPersonasResult = {
  personas: Array<RemotePersona>;
};

export type RemoteProvidersPersonasRequest = {
  jsonrpc: "2.0";
  method: "providers.personas";
  params: RemoteProvidersPersonasParams;
  id: JSONRPCId;
};

export type RemoteProvidersPersonasResponse = {
  jsonrpc: "2.0";
  result: RemoteProvidersPersonasResult;
  id: JSONRPCId;
};

export type RemoteThreadsSetPersonaParams = {
  threadID: UUID;
  persona: string;
  provider?: string;
  model?: string;
};

export type RemoteThreadsSetPersonaRequest = {
  jsonrpc: "2.0";
  method: "threads.setPersona";
  params: RemoteThreadsSetPersonaParams;
  id: JSONRPCId;
};

export type RemoteThreadsSetPersonaResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadSummary;
  id: JSONRPCId;
};

export type RemoteThreadsTurnRecallParams = {
  threadID: UUID;
};

export type RemoteThreadsTurnRecallResult = {
  threadID: UUID;
  turnID: UUID;
  prompt: string;
  attachments: Array<RemoteMediaRef>;
};

export type RemoteThreadsTurnRecallRequest = {
  jsonrpc: "2.0";
  method: "threads.turn.recall";
  params: RemoteThreadsTurnRecallParams;
  id: JSONRPCId;
};

export type RemoteThreadsTurnRecallResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsTurnRecallResult;
  id: JSONRPCId;
};

export type RemoteThreadsBranchParams = {
  threadID: UUID;
  messageID: UUID;
  actionID: UUID;
};

export type RemoteThreadsBranchResult = {
  threadID: UUID;
};

export type RemoteThreadsBranchRequest = {
  jsonrpc: "2.0";
  method: "threads.branch";
  params: RemoteThreadsBranchParams;
  id: JSONRPCId;
};

export type RemoteThreadsBranchResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsBranchResult;
  id: JSONRPCId;
};

export type RemoteThreadsContinueParams = {
  threadID: UUID;
  actionID: UUID;
};

export type RemoteThreadsContinueResult = {
  threadID: UUID;
};

export type RemoteThreadsContinueRequest = {
  jsonrpc: "2.0";
  method: "threads.continue";
  params: RemoteThreadsContinueParams;
  id: JSONRPCId;
};

export type RemoteThreadsContinueResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsContinueResult;
  id: JSONRPCId;
};

export type RemoteCommand = {
  name: string;
  detail: string;
  isBuiltIn: boolean;
  kind: "builtin" | "command" | "skill";
};

export type RemoteThreadsCommandsParams = {
  threadID: UUID;
} | {
  projectID: UUID;
  provider: string;
};

export type RemoteThreadsCommandsResult = {
  commands: Array<RemoteCommand>;
  recent: Array<string>;
  loading: boolean;
  error: string | null;
};

export type RemoteThreadsCommandsRequest = {
  jsonrpc: "2.0";
  method: "threads.commands";
  params: RemoteThreadsCommandsParams;
  id: JSONRPCId;
};

export type RemoteThreadsCommandsResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsCommandsResult;
  id: JSONRPCId;
};

export type RemoteReviewTurn = {
  turnID: UUID;
  index: number;
  status: RemoteTurnStatus;
  hasChanges: boolean;
};

export type RemoteThreadsGetDiffParams = {
  threadID: UUID;
  turnID?: UUID;
};

export type RemoteThreadsGetDiffResult = {
  threadID: UUID;
  turnID?: UUID;
  diff: string;
  truncated: boolean;
  filesChanged: Array<string>;
  turns: Array<RemoteReviewTurn>;
  failureReason?: string;
  metadataTruncated?: boolean;
};

export type RemoteThreadsGetDiffRequest = {
  jsonrpc: "2.0";
  method: "threads.getDiff";
  params: RemoteThreadsGetDiffParams;
  id: JSONRPCId;
};

export type RemoteThreadsGetDiffResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsGetDiffResult;
  id: JSONRPCId;
};

export type RemoteCapabilitiesRequest = {
  jsonrpc: "2.0";
  method: "capabilities.get";
  params?: Record<string, never>;
  id: JSONRPCId;
};

export type RemoteCapabilitiesResponse = {
  jsonrpc: "2.0";
  result: RemoteCapabilityDeclaration;
  id: JSONRPCId;
};

export type RemoteThreadsEventParams = {
  threadID: UUID;
  runID: string;
  sequence: number;
  kind: "projection";
  payload: RemoteThreadsProjectionPayload;
} | {
  threadID: UUID;
  runID: string;
  sequence: number;
  kind: "resync";
  payload: RemoteThreadsResyncPayload;
};

export type RemoteThreadsEventNotification = {
  jsonrpc: "2.0";
  method: "threads.event";
  params: RemoteThreadsEventParams;
};

export type RemoteThreadsChangedParams = {
  revision: number;
  threads: Array<RemoteThreadSummary>;
  removedThreadIDs: Array<UUID>;
};

export type RemoteThreadsChangedNotification = {
  jsonrpc: "2.0";
  method: "threads.changed";
  params: RemoteThreadsChangedParams;
};

export type RemoteRequestsChangedParams = {
  threadID: UUID;
  requests: Array<RemoteRequest>;
};

export type RemoteRequestsChangedNotification = {
  jsonrpc: "2.0";
  method: "requests.changed";
  params: RemoteRequestsChangedParams;
};

export type RemoteThreadsQueueChangedParams = {
  threadID: UUID;
  revision: number;
  followUps: Array<RemoteFollowUp>;
};

export type RemoteThreadsQueueChangedNotification = {
  jsonrpc: "2.0";
  method: "threads.queue.changed";
  params: RemoteThreadsQueueChangedParams;
};

export type RemoteSwarmCredibility = {
  cited: number;
  verified: number;
  unverified?: Array<string>;
  notRead?: Array<string>;
};

export type RemoteSwarmPendingQuestion = {
  requestID: string;
  prompt: string;
  forwarded: boolean;
  autoForwardAt?: ISO8601DateTime;
};

export type RemoteSwarmTier = {
  id: string;
  name: string;
  description: string;
  readOnly: boolean;
  writePaths?: Array<string>;
  provider?: string;
  model?: string;
  effort?: string;
  systemPrompt?: string;
  fastMode?: boolean;
};

export type RemoteSwarmBee = {
  threadID: UUID;
  index: number;
  name: string;
  task: string;
  kind: "native";
  origin: "delegated" | "queued" | "sent";
  status: "running" | "completed" | "failed" | "stopped";
  summary?: string;
  activity?: string;
  toolCalls: number;
  tokens: number;
  startedAt: ISO8601DateTime;
  finishedAt?: ISO8601DateTime;
  provider?: string;
  model?: string;
  tier?: string;
  readOnly?: boolean;
  directModel?: boolean;
  shadowRepo?: boolean;
  sharedNoGit?: boolean;
  sharedNoGitReason?: string;
  mergeState?: "pending" | "merged" | "discarded" | "failed" | "reviewing" | "rejected";
  reviewState?: "running" | "approved" | "rejected" | "needs_human";
  reviewReason?: string;
  reviewThreadId?: string;
  reviewLayer?: "l0" | "l1" | "l2" | "l3" | "human";
  reviewConcerns?: Array<string>;
  reviewTokens?: number;
  reviewTokensEstimated?: boolean;
  conflictFiles?: Array<string>;
  changedFiles?: Array<string>;
  mergeError?: string;
  linesAdded?: number;
  linesDeleted?: number;
  credibility?: RemoteSwarmCredibility;
  pendingQuestion?: RemoteSwarmPendingQuestion;
  canOpen: boolean;
  canStop: boolean;
};

export type RemoteSwarmState = {
  threadID: UUID;
  swarmAvailable: boolean;
  swarmEnabled: boolean;
  isBee: boolean;
  leadThreadID?: UUID;
  bee?: RemoteSwarmBee;
  bees: Array<RemoteSwarmBee>;
  queued?: number;
  plansOnly: boolean;
  plansOnlyOverridden: boolean;
};

export type RemoteThreadsSwarmGetParams = {
  threadID: UUID;
};

export type RemoteThreadsSwarmSetParams = {
  threadID: UUID;
  enabled: boolean;
  actionID: string;
};

export type RemoteThreadsSwarmSetBeeTiersParams = {
  threadID: UUID;
  beeTiers: string;
  actionID?: string;
};

export type RemoteThreadsSwarmStopBeeParams = {
  threadID: UUID;
  beeThreadID: UUID;
  actionID: string;
};

export type RemoteThreadsSwarmSetPlansOnlyParams = {
  threadID: UUID;
  plansOnly: boolean | null;
  actionID: string;
};

export type RemoteThreadsSwarmSetModeParams = {
  threadID: UUID;
  mode: "inherited" | "collaborative" | "leadPlansOnly" | "alwaysBees";
  actionID: string;
};

export type RemoteThreadsSwarmForwardQuestionParams = {
  threadID: UUID;
  beeThreadID: UUID;
  requestID: string;
  actionID: string;
};

export type RemoteThreadsSwarmMergeDiffParams = {
  threadID: UUID;
  beeThreadID: UUID;
};

export type RemoteThreadsSwarmMergeParams = {
  threadID: UUID;
  beeThreadID: UUID;
  actionID: string;
};

export type RemoteSwarmMergeDiffResult = {
  diff: string;
  truncated: boolean;
  filesChanged: Array<string>;
};

export type RemoteSwarmTiersGetParams = Record<string, never>;

export type RemoteSwarmTiersSetParams = {
  tiers: Array<RemoteSwarmTier>;
  actionID: string;
};

export type RemoteSwarmTiersResult = {
  tiers: Array<RemoteSwarmTier>;
};

export type RemoteThreadsSwarmGetRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.get";
  params: RemoteThreadsSwarmGetParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.set";
  params: RemoteThreadsSwarmSetParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetBeeTiersRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.setBeeTiers";
  params: RemoteThreadsSwarmSetBeeTiersParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmStopBeeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.stopBee";
  params: RemoteThreadsSwarmStopBeeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmBatchParams = {
  threadID: UUID;
  actionID: UUID;
};

export type RemoteThreadsSwarmStopAllRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.stopAll";
  params: RemoteThreadsSwarmBatchParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmClearFinishedRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.clearFinished";
  params: RemoteThreadsSwarmBatchParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetBeeTiersResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetPlansOnlyRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.setPlansOnly";
  params: RemoteThreadsSwarmSetPlansOnlyParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetPlansOnlyResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetModeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.setMode";
  params: RemoteThreadsSwarmSetModeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmSetModeResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadSummary;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmForwardQuestionRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.forwardQuestion";
  params: RemoteThreadsSwarmForwardQuestionParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmForwardQuestionResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmMergeDiffRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.mergeDiff";
  params: RemoteThreadsSwarmMergeDiffParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmMergeDiffResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmMergeDiffResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmApproveMergeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.approveMerge";
  params: RemoteThreadsSwarmMergeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmApproveMergeResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmDiscardMergeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.discardMerge";
  params: RemoteThreadsSwarmMergeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmDiscardMergeResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmState;
  id: JSONRPCId;
};

export type RemoteSwarmTiersGetRequest = {
  jsonrpc: "2.0";
  method: "swarm.tiers.get";
  params: RemoteSwarmTiersGetParams;
  id: JSONRPCId;
};

export type RemoteSwarmTiersGetResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmTiersResult;
  id: JSONRPCId;
};

export type RemoteSwarmTiersSetRequest = {
  jsonrpc: "2.0";
  method: "swarm.tiers.set";
  params: RemoteSwarmTiersSetParams;
  id: JSONRPCId;
};

export type RemoteSwarmTiersSetResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmTiersResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmChangedNotification = {
  jsonrpc: "2.0";
  method: "threads.swarm.changed";
  params: RemoteSwarmState;
};

export type RemoteFileEntryKind = "directory" | "file" | "symlink";

export type RemoteFileContentKind = "text" | "image" | "binary";

export type RemoteFileEntry = {
  name: string;
  path: string;
  kind: RemoteFileEntryKind;
  size: number;
  ignored: boolean;
  protected: boolean;
};

export type RemoteFilesListParams = {
  threadID: UUID;
  subpath?: string;
};

export type RemoteFilesListResult = {
  path: string;
  entries: Array<RemoteFileEntry>;
  truncated: boolean;
};

export type RemoteFilesListRequest = {
  jsonrpc: "2.0";
  method: "files.list";
  params: RemoteFilesListParams;
  id: JSONRPCId;
};

export type RemoteFilesListResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesListResult;
  id: JSONRPCId;
};

export type RemoteFilesReadParams = {
  threadID: UUID;
  path: string;
  offset?: number;
  length?: number;
};

export type RemoteFilesReadResult = {
  path: string;
  kind: RemoteFileContentKind;
  text?: string;
  base64?: string;
  mimeType?: string;
  hash?: string;
  totalBytes: number;
  offset: number;
  nextOffset?: number;
  truncated: boolean;
};

export type RemoteFilesReadRequest = {
  jsonrpc: "2.0";
  method: "files.read";
  params: RemoteFilesReadParams;
  id: JSONRPCId;
};

export type RemoteFilesReadResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesReadResult;
  id: JSONRPCId;
};

export type RemoteFilesSearchParams = {
  threadID: UUID;
  query: string;
};

export type RemoteFilesSearchResult = {
  entries: Array<RemoteFileEntry>;
  truncated: boolean;
};

export type RemoteFilesSearchRequest = {
  jsonrpc: "2.0";
  method: "files.search";
  params: RemoteFilesSearchParams;
  id: JSONRPCId;
};

export type RemoteFilesSearchResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesSearchResult;
  id: JSONRPCId;
};

export type RemoteFilesWriteParams = {
  threadID: UUID;
  path: string;
  content: string;
  expectedHash?: string;
};

export type RemoteFilesWriteResult = {
  hash: string;
  size: number;
};

export type RemoteFilesWriteRequest = {
  jsonrpc: "2.0";
  method: "files.write";
  params: RemoteFilesWriteParams;
  id: JSONRPCId;
};

export type RemoteFilesWriteResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesWriteResult;
  id: JSONRPCId;
};

export type RemoteFilesCreateParams = {
  threadID: UUID;
  path: string;
  dir?: boolean;
};

export type RemoteFilesCreateResult = {
  path: string;
};

export type RemoteFilesCreateRequest = {
  jsonrpc: "2.0";
  method: "files.create";
  params: RemoteFilesCreateParams;
  id: JSONRPCId;
};

export type RemoteFilesCreateResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesCreateResult;
  id: JSONRPCId;
};

export type RemoteFilesRenameParams = {
  threadID: UUID;
  path: string;
  newPath: string;
};

export type RemoteFilesRenameResult = {
  path: string;
};

export type RemoteFilesRenameRequest = {
  jsonrpc: "2.0";
  method: "files.rename";
  params: RemoteFilesRenameParams;
  id: JSONRPCId;
};

export type RemoteFilesRenameResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesRenameResult;
  id: JSONRPCId;
};

export type RemoteFilesTrashParams = {
  threadID: UUID;
  path: string;
};

export type RemoteFilesTrashResult = Record<string, never>;

export type RemoteFilesTrashRequest = {
  jsonrpc: "2.0";
  method: "files.trash";
  params: RemoteFilesTrashParams;
  id: JSONRPCId;
};

export type RemoteFilesTrashResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesTrashResult;
  id: JSONRPCId;
};

export type RemoteFilesDuplicateParams = {
  threadID: UUID;
  path: string;
};

export type RemoteFilesDuplicateResult = {
  path: string;
};

export type RemoteFilesDuplicateRequest = {
  jsonrpc: "2.0";
  method: "files.duplicate";
  params: RemoteFilesDuplicateParams;
  id: JSONRPCId;
};

export type RemoteFilesDuplicateResponse = {
  jsonrpc: "2.0";
  result: RemoteFilesDuplicateResult;
  id: JSONRPCId;
};

export type RemoteMediaSource = "attachment" | "toolImage";

export type RemoteMediaVariant = "thumb" | "full";

export type RemoteMediaRef = {
  id: string;
  source: RemoteMediaSource;
  name: string;
  mimeType: string;
  byteCount: number;
  width?: number;
  height?: number;
};

export type RemoteThreadsMediaReadParams = {
  threadID: UUID;
  messageID: UUID;
  mediaID: string;
  variant: RemoteMediaVariant;
  offset?: number;
  length?: number;
};

export type RemoteThreadsMediaReadResult = {
  mimeType: string;
  totalBytes: number;
  offset: number;
  nextOffset?: number;
  truncated: boolean;
  base64: string;
};

export type RemoteThreadsMediaReadRequest = {
  jsonrpc: "2.0";
  method: "threads.media.read";
  params: RemoteThreadsMediaReadParams;
  id: JSONRPCId;
};

export type RemoteThreadsMediaReadResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsMediaReadResult;
  id: JSONRPCId;
};

export type RemoteGitStatusParams = {
  threadID: UUID;
};

export type RemoteGitStatusResult = {
  threadID: UUID;
  isRepository: boolean;
  branch?: string;
  upstream?: string;
  defaultBranch?: string;
  ahead: number;
  behind: number;
  modifiedFiles: Array<string>;
  untrackedFiles: Array<string>;
  isClean: boolean;
  truncated?: boolean;
};

export type RemoteGitStatusRequest = {
  jsonrpc: "2.0";
  method: "git.status";
  params: RemoteGitStatusParams;
  id: JSONRPCId;
};

export type RemoteGitStatusResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStatusResult;
  id: JSONRPCId;
};

export type RemoteGitBranchesParams = {
  threadID: UUID;
};

export type RemoteGitBranchesResult = {
  threadID: UUID;
  isRepository: boolean;
  current?: string;
  local: Array<string>;
  remote: Array<string>;
  truncated?: boolean;
};

export type RemoteGitBranchesRequest = {
  jsonrpc: "2.0";
  method: "git.branches";
  params: RemoteGitBranchesParams;
  id: JSONRPCId;
};

export type RemoteGitBranchesResponse = {
  jsonrpc: "2.0";
  result: RemoteGitBranchesResult;
  id: JSONRPCId;
};

export type ScriptsListParams = {
  threadID: UUID;
};

export type ScriptsPackageScript = {
  name: string;
  command: string;
  isFavorite: boolean;
};

export type ScriptsProjectScript = {
  id: UUID;
  name: string;
  symbol: string | null;
  command?: string;
  runOnWorktreeCreate?: boolean;
};

export type ScriptsListResult = {
  directory: string;
  runner: string;
  packageScripts: Array<ScriptsPackageScript>;
  projectScripts: Array<ScriptsProjectScript>;
  revision?: string;
};

export type ScriptsListRequest = {
  jsonrpc: "2.0";
  method: "scripts.list";
  params: ScriptsListParams;
  id: JSONRPCId;
};

export type ScriptsListResponse = {
  jsonrpc: "2.0";
  result: ScriptsListResult;
  id: JSONRPCId;
};

export type ScriptsRunParams = {
  threadID: UUID;
  kind: "package" | "project";
  name?: string;
  scriptID?: UUID;
};

export type ScriptsRunResult = {
  ok: true;
  runID: UUID;
};

export type ScriptsRunRequest = {
  jsonrpc: "2.0";
  method: "scripts.run";
  params: ScriptsRunParams;
  id: JSONRPCId;
};

export type ScriptsRunResponse = {
  jsonrpc: "2.0";
  result: ScriptsRunResult;
  id: JSONRPCId;
};

export type ScriptsRunStatusParams = {
  threadID: UUID;
  runID: UUID;
};

export type ScriptsRunStatusResult = {
  runID: UUID;
  label: string;
  status: "running" | "finished" | "rejected";
  exitCode?: number;
  output: string;
};

export type ScriptsRunStatusRequest = {
  jsonrpc: "2.0";
  method: "scripts.runStatus";
  params: ScriptsRunStatusParams;
  id: JSONRPCId;
};

export type ScriptsRunStatusResponse = {
  jsonrpc: "2.0";
  result: ScriptsRunStatusResult;
  id: JSONRPCId;
};

export type ScriptsSetFavoriteParams = {
  threadID: UUID;
  name: string;
  isFavorite: boolean;
};

export type ScriptsSetFavoriteResult = {
  favorites: Array<string>;
};

export type ScriptsSetFavoriteRequest = {
  jsonrpc: "2.0";
  method: "scripts.setFavorite";
  params: ScriptsSetFavoriteParams;
  id: JSONRPCId;
};

export type ScriptsSetFavoriteResponse = {
  jsonrpc: "2.0";
  result: ScriptsSetFavoriteResult;
  id: JSONRPCId;
};

export type JSONRPCBatchRequest = Array<SessionPairRequest | SessionAuthenticateRequest | SessionNewPairingCodeRequest | SessionRevokeDeviceRequest | SessionRenameDeviceRequest | KanbanGetBoardRequest | KanbanCreateTaskRequest | KanbanMoveTaskRequest | KanbanStartTaskRequest | KanbanReviewTaskRequest | GoalCreateRequest | GoalStartRequest | GoalPauseRequest | GoalCancelRequest | GoalReplanRequest | GoalRetryTaskRequest | GoalSkipTaskRequest | KanbanGetDiffRequest | RemoteThreadsCreateRequest | RemoteProvidersListRequest | RemoteProvidersRememberSelectionRequest | RemoteThreadsSendMessageRequest | RemoteThreadsSplitSessionRequest | RemoteThreadsActionStatusRequest | RemoteThreadsQueueListRequest | RemoteThreadsQueueMutationRequest | RemoteThreadsQueueMoveRequest | RemoteThreadsMCPDisabledSetRequest | RemoteThreadsQueueInterruptAndSendRequest | RemoteAttachmentUploadRequest | RemoteRequestsListRequest | RemoteRequestsRespondRequest | RemoteThreadsInterruptRequest | RemoteThreadsKillRunningCommandRequest | RemoteThreadsBackgroundTaskLogRequest | RemoteThreadsStopBackgroundTaskRequest | RemoteProvidersPersonasRequest | RemoteThreadsSetPersonaRequest | RemoteThreadsTurnRecallRequest | RemoteThreadsGetRequest | RemoteThreadsGetDiffRequest | RemoteFilesListRequest | RemoteFilesReadRequest | RemoteFilesSearchRequest | RemoteThreadsMediaReadRequest | RemoteGitStatusRequest | RemoteGitBranchesRequest | ScriptsListRequest | ScriptsRunRequest | ScriptsRunStatusRequest | ScriptsSetFavoriteRequest | RemoteThreadsBranchRequest | RemoteThreadsContinueRequest | RemoteThreadsCommandsRequest | RemoteThreadsSwarmStopAllRequest | RemoteThreadsSwarmClearFinishedRequest | RuntimeQueueEditRequest | RuntimeLimitTextRequest | RuntimeWorktreePreviewRequest | RuntimeWorktreeExecuteRequest | RuntimeCompactRequest | RemoteMessagesSearchRequest | RemoteGitChangedFilesRequest | RemoteGitCommitSelectedRequest | RemoteGitStashListRequest | RemoteGitStashPushRequest | RemoteGitStashPopRequest | RemoteGitStashApplyRequest | RemoteGitStashDropRequest | RemoteGitPRListRequest | RemoteGitPRDetailRequest | RemoteGitPushRequest | RemoteGitPullRequest | RemoteGitFetchRequest | RemoteGitCheckoutRequest | RemoteGitCreateBranchRequest | RemoteGitMergeRequest | RemoteGitGenerateCommitMessageRequest | RemoteGitPRCreateURLRequest | RemoteThreadsRevertPreviewRequest | RemoteThreadsRevertApplyRequest | RemoteThreadsSwarmInterruptedRequest | RemoteThreadsSwarmResumeRequest | RemoteThreadsSwarmDismissInterruptedRequest | RemoteThreadsSwarmRetryBeeRequest | RemoteRequestsAlwaysAllowedListRequest | RemoteRequestsAlwaysAllowedRevokeRequest | RemoteMemoryProposalsListRequest | RemoteMemoryProposalsAcceptRequest | RemoteMemoryProposalsRejectRequest | RemoteAttentionListRequest | RemoteAttentionResolveRequest | RemoteAttentionConfirmSuggestionRequest | RemoteStewardTopicsListRequest | RemoteStewardOpenRequest | RemoteStewardTopicsStartRequest | RemoteStewardTopicsReplanRequest | RemoteStewardTopicsConfirmRequest | RemoteStewardTopicsCloseRequest | RemoteStewardTopicsDeleteRequest | RemoteStewardTopicsTasksReviewRequest | RemoteAutomationsListRequest | RemoteAutomationsRunsRequest | RemoteAutomationsRunRequest | RemoteAutomationsMarkReadRequest | RemoteAutomationsArchiveRunRequest | RemoteAutomationsCreateRequest | RemoteAutomationsUpdateRequest | RemoteAutomationsDeleteRequest | RemoteAutomationsSetEnabledRequest | RemoteAutomationsResetRequest | RemoteProjectsAddRequest | RemoteProjectsValidatePathRequest | RemoteSettingsGetRequest | RemoteSettingsSetRequest | RemoteThreadsSwarmReportRequest | RemoteMCPServersListRequest | AppUpdateGetRequest | AppUpdateCheckRequest | AppUpdateInstallRequest | SessionDevicesListRequest | SessionDevicesRevokeRequest | RemoteFilesWriteRequest | RemoteFilesCreateRequest | RemoteFilesRenameRequest | RemoteFilesTrashRequest | RemoteFilesDuplicateRequest | RemoteProjectsCloneRequest | RemoteProjectsListDirRequest | ScriptsSaveRequest | KanbanDeleteTaskRequest | KanbanRescueTaskRequest | RemoteMemoryCreateRequest | RemoteMemorySearchRequest>;

export type JSONRPCBatchResponse = Array<JSONRPCErrorResponse | SessionPairResponse | SessionAuthenticateResponse | SessionNewPairingCodeResponse | SessionRevokeDeviceResponse | SessionRenameDeviceResponse | KanbanGetBoardResponse | KanbanTaskMutationResponse | KanbanGetDiffResponse | RemoteThreadsCreateResponse | RemoteProvidersListResponse | RemoteProvidersRememberSelectionResponse | RemoteThreadsSendMessageResponse | RemoteThreadsSplitSessionResponse | RemoteThreadsActionStatusResponse | RemoteThreadsQueueListResponse | RemoteThreadsQueueMutationResponse | RemoteThreadsQueueMoveResponse | RemoteThreadsMCPDisabledSetResponse | RemoteThreadsQueueInterruptAndSendResponse | RemoteAttachmentUploadResponse | RemoteRequestsListResponse | RemoteRequestsRespondResponse | RemoteThreadsInterruptResponse | RemoteThreadsKillRunningCommandResponse | RemoteThreadsBackgroundTaskLogResponse | RemoteThreadsStopBackgroundTaskResponse | RemoteProvidersPersonasResponse | RemoteThreadsSetPersonaResponse | RemoteThreadsTurnRecallResponse | RemoteThreadsGetResponse | RemoteThreadsGetDiffResponse | RemoteFilesListResponse | RemoteFilesReadResponse | RemoteFilesSearchResponse | RemoteThreadsMediaReadResponse | RemoteGitStatusResponse | RemoteGitBranchesResponse | ScriptsListResponse | ScriptsRunResponse | ScriptsRunStatusResponse | ScriptsSetFavoriteResponse | RemoteThreadsBranchResponse | RemoteThreadsContinueResponse | RemoteThreadsCommandsResponse | RemoteThreadsSwarmResponse | RemoteThreadsSwarmSetBeeTiersResponse | RuntimeQueueEditResponse | RuntimeLimitTextResponse | RuntimeWorktreePreviewResponse | RuntimeWorktreeExecuteResponse | RuntimeCompactResponse | RemoteMessagesSearchResponse | RemoteGitChangedFilesResponse | RemoteGitCommitSelectedResponse | RemoteGitStashListResponse | RemoteGitStashPushResponse | RemoteGitStashPopResponse | RemoteGitStashApplyResponse | RemoteGitStashDropResponse | RemoteGitPRListResponse | RemoteGitPRDetailResponse | RemoteGitPushResponse | RemoteGitPullResponse | RemoteGitFetchResponse | RemoteGitCheckoutResponse | RemoteGitCreateBranchResponse | RemoteGitMergeResponse | RemoteGitGenerateCommitMessageResponse | RemoteGitPRCreateURLResponse | RemoteThreadsRevertPreviewResponse | RemoteThreadsRevertApplyResponse | RemoteThreadsSwarmInterruptedResponse | RemoteThreadsSwarmResumeResponse | RemoteThreadsSwarmDismissInterruptedResponse | RemoteThreadsSwarmRetryBeeResponse | RemoteRequestsAlwaysAllowedListResponse | RemoteRequestsAlwaysAllowedRevokeResponse | RemoteMemoryProposalsListResponse | RemoteMemoryProposalsAcceptResponse | RemoteMemoryProposalsRejectResponse | RemoteAttentionListResponse | RemoteAttentionResolveResponse | RemoteAttentionConfirmSuggestionResponse | RemoteStewardTopicsListResponse | RemoteStewardOpenResponse | RemoteStewardTopicsStartResponse | RemoteStewardTopicsReplanResponse | RemoteStewardTopicsConfirmResponse | RemoteStewardTopicsCloseResponse | RemoteStewardTopicsDeleteResponse | RemoteStewardTopicsTasksReviewResponse | RemoteAutomationsListResponse | RemoteAutomationsRunsResponse | RemoteAutomationsRunResponse | RemoteAutomationsMarkReadResponse | RemoteAutomationsArchiveRunResponse | RemoteAutomationsCreateResponse | RemoteAutomationsUpdateResponse | RemoteAutomationsDeleteResponse | RemoteAutomationsSetEnabledResponse | RemoteAutomationsResetResponse | RemoteProjectsAddResponse | RemoteProjectsValidatePathResponse | RemoteSettingsGetResponse | RemoteSettingsSetResponse | RemoteThreadsSwarmReportResponse | RemoteMCPServersListResponse | AppUpdateGetResponse | AppUpdateCheckResponse | AppUpdateInstallResponse | SessionDevicesListResponse | SessionDevicesRevokeResponse | RemoteFilesWriteResponse | RemoteFilesCreateResponse | RemoteFilesRenameResponse | RemoteFilesTrashResponse | RemoteFilesDuplicateResponse | RemoteProjectsCloneResponse | RemoteProjectsListDirResponse | ScriptsSaveResponse | RemoteMemoryCreateResponse | RemoteMemorySearchResponse>;

export type RuntimeDraft = {
  text: string;
  attachments: Array<UUID>;
};

export type RuntimeQueueEditParams = {
  threadID: UUID;
  action: "begin" | "save" | "cancel";
  followUpID?: UUID;
  draft?: RuntimeDraft;
};

export type RuntimeQueueEditResult = {
  threadID: UUID;
  draft: RuntimeDraft;
  followUpID?: UUID;
  vanished: boolean;
};

export type RuntimeLimitTextResult = {
  threadID: UUID;
  text: string;
};

export type RuntimeQueueEditRequest = {
  jsonrpc: "2.0";
  method: "threads.queue.edit";
  params: RuntimeQueueEditParams;
  id: JSONRPCId;
};

export type RuntimeQueueEditResponse = {
  jsonrpc: "2.0";
  result: RuntimeQueueEditResult;
  id: JSONRPCId;
};

export type RuntimeLimitTextRequest = {
  jsonrpc: "2.0";
  method: "threads.provider.limitText";
  params: RemoteThreadsTurnRecallParams;
  id: JSONRPCId;
};

export type RuntimeLimitTextResponse = {
  jsonrpc: "2.0";
  result: RuntimeLimitTextResult;
  id: JSONRPCId;
};

export type RuntimeWorktreePreviewParams = {
  threadID: UUID;
};

export type RuntimeWorktreePreviewResult = {
  threadID: UUID;
  eligible: boolean;
  reason?: string;
  suggestedBranch: string;
  worktreePath: string;
  files: Array<string>;
  untracked: Array<string>;
};

export type RuntimeWorktreeExecuteParams = {
  threadID: UUID;
  branch?: string;
  transferFiles: boolean;
};

export type RuntimeWorktreeExecuteResult = {
  threadID: UUID;
  branch: string;
  worktreePath: string;
  transferredFiles: number;
};

export type RuntimeWorktreePreviewRequest = {
  jsonrpc: "2.0";
  method: "threads.worktree.preview";
  params: RuntimeWorktreePreviewParams;
  id: JSONRPCId;
};

export type RuntimeWorktreePreviewResponse = {
  jsonrpc: "2.0";
  result: RuntimeWorktreePreviewResult;
  id: JSONRPCId;
};

export type RuntimeWorktreeExecuteRequest = {
  jsonrpc: "2.0";
  method: "threads.worktree.execute";
  params: RuntimeWorktreeExecuteParams;
  id: JSONRPCId;
};

export type RuntimeWorktreeExecuteResponse = {
  jsonrpc: "2.0";
  result: RuntimeWorktreeExecuteResult;
  id: JSONRPCId;
};

export type RuntimeCompactRequest = {
  jsonrpc: "2.0";
  method: "threads.compact";
  params: RemoteThreadsTurnRecallParams;
  id: JSONRPCId;
};

export type RuntimeCompactResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSendMessageResult;
  id: JSONRPCId;
};

export type RuntimeCompactSuggestionParams = {
  threadID: UUID;
  usedTokens: number;
  idleSince: string;
};

export type RuntimeCompactSuggestionNotification = {
  jsonrpc: "2.0";
  method: "threads.compactSuggestion";
  params: RuntimeCompactSuggestionParams;
};

export type RemoteMessageSearchHit = {
  threadID: UUID;
  threadTitle: string;
  messageID: UUID;
  role: string;
  snippet: string;
  createdAt: ISO8601DateTime;
};

export type RemoteMessageSearchResult = {
  hits: Array<RemoteMessageSearchHit>;
};

export type RemoteMessagesSearchParams = {
  query: string;
  limit?: number | null;
};

export type RemoteMessagesSearchRequest = {
  jsonrpc: "2.0";
  method: "messages.search";
  params: RemoteMessagesSearchParams;
  id: JSONRPCId;
};

export type RemoteMessagesSearchResponse = {
  jsonrpc: "2.0";
  result: RemoteMessageSearchResult;
  id: JSONRPCId;
};

export type RemoteGitChangedFile = {
  path: string;
  origPath?: string;
  status: string;
};

export type RemoteGitChangedFilesResult = {
  threadID: UUID;
  files: Array<RemoteGitChangedFile>;
};

export type RemoteGitCommitSelectedResult = {
  ok: boolean;
  pushError?: string;
};

export type RemoteGitStashEntry = {
  index: number;
  ref: string;
  message: string;
  branch?: string;
  createdAt?: string;
};

export type RemoteGitStashListResult = {
  threadID: UUID;
  entries: Array<RemoteGitStashEntry>;
};

export type RemoteGitStashActionResult = {
  ok: boolean;
};

export type RemoteGitPRSummary = {
  number: number;
  title: string;
  state: string;
  isDraft: boolean;
  author: string;
  headBranch: string;
  baseBranch: string;
  url?: string;
  updatedAt?: string;
  reviewDecision?: string;
  checksState?: string;
};

export type RemoteGitPRListResult = {
  threadID: UUID;
  items: Array<RemoteGitPRSummary>;
};

export type RemoteGitPRCheck = {
  name: string;
  state: string;
};

export type RemoteGitPRComment = {
  author: string;
  body: string;
  createdAt?: string;
};

export type RemoteGitPRDetail = {
  number: number;
  title: string;
  state: string;
  isDraft: boolean;
  author: string;
  headBranch: string;
  baseBranch: string;
  url?: string;
  updatedAt?: string;
  reviewDecision?: string;
  checksState?: string;
  body: string;
  additions: number;
  deletions: number;
  changedFiles: number;
  checks: Array<RemoteGitPRCheck>;
  comments: Array<RemoteGitPRComment>;
};

export type RemoteGitChangedFilesParams = {
  threadID: UUID;
};

export type RemoteGitChangedFilesRequest = {
  jsonrpc: "2.0";
  method: "git.changedFiles";
  params: RemoteGitChangedFilesParams;
  id: JSONRPCId;
};

export type RemoteGitChangedFilesResponse = {
  jsonrpc: "2.0";
  result: RemoteGitChangedFilesResult;
  id: JSONRPCId;
};

export type RemoteGitCommitSelectedParams = {
  threadID: UUID;
  paths: Array<string>;
  message: string;
  pushAfter?: boolean;
};

export type RemoteGitCommitSelectedRequest = {
  jsonrpc: "2.0";
  method: "git.commitSelected";
  params: RemoteGitCommitSelectedParams;
  id: JSONRPCId;
};

export type RemoteGitCommitSelectedResponse = {
  jsonrpc: "2.0";
  result: RemoteGitCommitSelectedResult;
  id: JSONRPCId;
};

export type RemoteGitStashListParams = {
  threadID: UUID;
};

export type RemoteGitStashListRequest = {
  jsonrpc: "2.0";
  method: "git.stash.list";
  params: RemoteGitStashListParams;
  id: JSONRPCId;
};

export type RemoteGitStashListResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStashListResult;
  id: JSONRPCId;
};

export type RemoteGitStashPushParams = {
  threadID: UUID;
  message?: string;
};

export type RemoteGitStashPushRequest = {
  jsonrpc: "2.0";
  method: "git.stash.push";
  params: RemoteGitStashPushParams;
  id: JSONRPCId;
};

export type RemoteGitStashPushResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStashActionResult;
  id: JSONRPCId;
};

export type RemoteGitStashPopParams = {
  threadID: UUID;
};

export type RemoteGitStashPopRequest = {
  jsonrpc: "2.0";
  method: "git.stash.pop";
  params: RemoteGitStashPopParams;
  id: JSONRPCId;
};

export type RemoteGitStashPopResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStashActionResult;
  id: JSONRPCId;
};

export type RemoteGitStashApplyParams = {
  threadID: UUID;
  index: number;
};

export type RemoteGitStashApplyRequest = {
  jsonrpc: "2.0";
  method: "git.stash.apply";
  params: RemoteGitStashApplyParams;
  id: JSONRPCId;
};

export type RemoteGitStashApplyResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStashActionResult;
  id: JSONRPCId;
};

export type RemoteGitStashDropParams = {
  threadID: UUID;
  index: number;
};

export type RemoteGitStashDropRequest = {
  jsonrpc: "2.0";
  method: "git.stash.drop";
  params: RemoteGitStashDropParams;
  id: JSONRPCId;
};

export type RemoteGitStashDropResponse = {
  jsonrpc: "2.0";
  result: RemoteGitStashActionResult;
  id: JSONRPCId;
};

export type RemoteGitPRListParams = {
  threadID: UUID;
  limit?: number;
};

export type RemoteGitPRListRequest = {
  jsonrpc: "2.0";
  method: "git.pr.list";
  params: RemoteGitPRListParams;
  id: JSONRPCId;
};

export type RemoteGitPRListResponse = {
  jsonrpc: "2.0";
  result: RemoteGitPRListResult;
  id: JSONRPCId;
};

export type RemoteGitPRDetailParams = {
  threadID: UUID;
  number: number;
};

export type RemoteGitPRDetailRequest = {
  jsonrpc: "2.0";
  method: "git.pr.detail";
  params: RemoteGitPRDetailParams;
  id: JSONRPCId;
};

export type RemoteGitPRDetailResponse = {
  jsonrpc: "2.0";
  result: RemoteGitPRDetail;
  id: JSONRPCId;
};

export type RemoteGitWriteActionResult = {
  ok: boolean;
};

export type RemoteGitGenerateCommitMessageResult = {
  message: string;
};

export type RemoteGitPRCreateURLResult = {
  url: string;
  title?: string;
  body?: string;
};

export type RemoteGitPushParams = {
  threadID: UUID;
};

export type RemoteGitPushRequest = {
  jsonrpc: "2.0";
  method: "git.push";
  params: RemoteGitPushParams;
  id: JSONRPCId;
};

export type RemoteGitPushResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitPullParams = {
  threadID: UUID;
};

export type RemoteGitPullRequest = {
  jsonrpc: "2.0";
  method: "git.pull";
  params: RemoteGitPullParams;
  id: JSONRPCId;
};

export type RemoteGitPullResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitFetchParams = {
  threadID: UUID;
};

export type RemoteGitFetchRequest = {
  jsonrpc: "2.0";
  method: "git.fetch";
  params: RemoteGitFetchParams;
  id: JSONRPCId;
};

export type RemoteGitFetchResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitCheckoutParams = {
  threadID: UUID;
  branch: string;
};

export type RemoteGitCheckoutRequest = {
  jsonrpc: "2.0";
  method: "git.checkout";
  params: RemoteGitCheckoutParams;
  id: JSONRPCId;
};

export type RemoteGitCheckoutResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitCreateBranchParams = {
  threadID: UUID;
  name: string;
};

export type RemoteGitCreateBranchRequest = {
  jsonrpc: "2.0";
  method: "git.createBranch";
  params: RemoteGitCreateBranchParams;
  id: JSONRPCId;
};

export type RemoteGitCreateBranchResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitMergeParams = {
  threadID: UUID;
  branch: string;
};

export type RemoteGitMergeRequest = {
  jsonrpc: "2.0";
  method: "git.merge";
  params: RemoteGitMergeParams;
  id: JSONRPCId;
};

export type RemoteGitMergeResponse = {
  jsonrpc: "2.0";
  result: RemoteGitWriteActionResult;
  id: JSONRPCId;
};

export type RemoteGitGenerateCommitMessageParams = {
  threadID: UUID;
};

export type RemoteGitGenerateCommitMessageRequest = {
  jsonrpc: "2.0";
  method: "git.generateCommitMessage";
  params: RemoteGitGenerateCommitMessageParams;
  id: JSONRPCId;
};

export type RemoteGitGenerateCommitMessageResponse = {
  jsonrpc: "2.0";
  result: RemoteGitGenerateCommitMessageResult;
  id: JSONRPCId;
};

export type RemoteGitPRCreateURLParams = {
  threadID: UUID;
};

export type RemoteGitPRCreateURLRequest = {
  jsonrpc: "2.0";
  method: "git.pr.createUrl";
  params: RemoteGitPRCreateURLParams;
  id: JSONRPCId;
};

export type RemoteGitPRCreateURLResponse = {
  jsonrpc: "2.0";
  result: RemoteGitPRCreateURLResult;
  id: JSONRPCId;
};

export type RemoteSwarmExtInterruptedBee = {
  threadID: UUID;
  name: string;
  task: string;
};

export type RemoteSwarmExtInterruptedResult = {
  count: number;
  bees?: Array<RemoteSwarmExtInterruptedBee>;
};

export type RemoteSwarmExtResumeResult = {
  resumed: number;
};

export type RemoteSwarmExtDismissResult = {
  dismissed: number;
};

export type RemoteSwarmExtThreadParams = {
  threadID: UUID;
};

export type RemoteSwarmExtRetryBeeResult = {
  ok: boolean;
};

export type RemoteThreadsSwarmRetryBeeParams = {
  threadID: UUID;
  beeThreadID: UUID;
};

export type RemoteThreadsSwarmRetryBeeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.retryBee";
  params: RemoteThreadsSwarmRetryBeeParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmRetryBeeResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmExtRetryBeeResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmReportParams = {
  threadID: UUID;
  beeID: UUID;
};

export type RemoteThreadsSwarmReportResult = {
  beeID: UUID;
  name?: string;
  text: string;
  truncated: boolean;
};

export type RemoteThreadsSwarmReportRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.report";
  params: RemoteThreadsSwarmReportParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmReportResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsSwarmReportResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmInterruptedRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.interrupted";
  params: RemoteSwarmExtThreadParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmInterruptedResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmExtInterruptedResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmResumeRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.resume";
  params: RemoteSwarmExtThreadParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmResumeResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmExtResumeResult;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmDismissInterruptedRequest = {
  jsonrpc: "2.0";
  method: "threads.swarm.dismissInterrupted";
  params: RemoteSwarmExtThreadParams;
  id: JSONRPCId;
};

export type RemoteThreadsSwarmDismissInterruptedResponse = {
  jsonrpc: "2.0";
  result: RemoteSwarmExtDismissResult;
  id: JSONRPCId;
};

export type RemoteAlwaysAllowedListParams = {
  projectID: UUID;
};

export type RemoteAlwaysAllowedListResult = {
  keys: Array<string>;
};

export type RemoteRequestsAlwaysAllowedListRequest = {
  jsonrpc: "2.0";
  method: "requests.alwaysAllowed.list";
  params: RemoteAlwaysAllowedListParams;
  id: JSONRPCId;
};

export type RemoteRequestsAlwaysAllowedListResponse = {
  jsonrpc: "2.0";
  result: RemoteAlwaysAllowedListResult;
  id: JSONRPCId;
};

export type RemoteAlwaysAllowedRevokeParams = {
  projectID: UUID;
  key: string;
};

export type RemoteAlwaysAllowedRevokeResult = {
  keys: Array<string>;
};

export type RemoteRequestsAlwaysAllowedRevokeRequest = {
  jsonrpc: "2.0";
  method: "requests.alwaysAllowed.revoke";
  params: RemoteAlwaysAllowedRevokeParams;
  id: JSONRPCId;
};

export type RemoteRequestsAlwaysAllowedRevokeResponse = {
  jsonrpc: "2.0";
  result: RemoteAlwaysAllowedRevokeResult;
  id: JSONRPCId;
};

export type RemoteMemoryProposalStatus = "pending" | "accepted" | "rejected";

export type RemoteMemoryProposal = {
  id: string;
  projectPath: string;
  threadID?: UUID;
  title: string;
  content: string;
  unitType?: string;
  importance: number;
  labels?: Array<string>;
  source: string;
  status: RemoteMemoryProposalStatus;
  scope?: "project" | "global";
  remoteMemoryID?: string;
  error?: string;
  createdAt: string;
  updatedAt: string;
};

export type RemoteMemoryProposalListResult = {
  proposals: Array<RemoteMemoryProposal>;
};

export type RemoteMemoryProposalOKResult = {
  ok: boolean;
};

export type RemoteMemoryProposalChangedEvent = {
  id: string;
  status: RemoteMemoryProposalStatus;
};

export type RemoteMemoryProposalsListParams = {
  projectID?: UUID;
  status?: RemoteMemoryProposalStatus;
};

export type RemoteMemoryProposalsListRequest = {
  jsonrpc: "2.0";
  method: "memory.proposals.list";
  params: RemoteMemoryProposalsListParams;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsListResponse = {
  jsonrpc: "2.0";
  result: RemoteMemoryProposalListResult;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsIDParams = {
  id: string;
};

export type RemoteMemoryProposalsAcceptRequest = {
  jsonrpc: "2.0";
  method: "memory.proposals.accept";
  params: RemoteMemoryProposalsAcceptParams;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsAcceptResponse = {
  jsonrpc: "2.0";
  result: RemoteMemoryProposal;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsRejectRequest = {
  jsonrpc: "2.0";
  method: "memory.proposals.reject";
  params: RemoteMemoryProposalsIDParams;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsRejectResponse = {
  jsonrpc: "2.0";
  result: RemoteMemoryProposalOKResult;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsChangedNotification = {
  jsonrpc: "2.0";
  method: "memory.proposals.changed";
  params: RemoteMemoryProposalChangedEvent;
};

export type RemoteAttentionItem = {
  id: string;
  kind: string;
  subjectType: string;
  subjectID: string;
  projectID: string;
  threadID: string;
  title: string;
  detail: string;
  enteredAt: string;
  resolvedAt: string | null;
  suggestions?: Array<RemoteAttentionSuggestion>;
};

export type RemoteAttentionSuggestion = {
  requestID: string;
  kind: string;
  title: string;
  detail: string;
  optionID: string;
  reason: string;
};

export type RemoteAttentionListParams = {
  projectID?: string;
  includeResolved?: boolean;
  limit?: number;
};

export type RemoteAttentionListResult = {
  items: Array<RemoteAttentionItem>;
  unresolvedCount: number;
};

export type RemoteAttentionListRequest = {
  jsonrpc: "2.0";
  method: "attention.list";
  params: RemoteAttentionListParams;
  id: JSONRPCId;
};

export type RemoteAttentionListResponse = {
  jsonrpc: "2.0";
  result: RemoteAttentionListResult;
  id: JSONRPCId;
};

export type RemoteAttentionResolveParams = {
  id: string;
};

export type RemoteAttentionResolveResult = {
  ok: boolean;
};

export type RemoteAttentionResolveRequest = {
  jsonrpc: "2.0";
  method: "attention.resolve";
  params: RemoteAttentionResolveParams;
  id: JSONRPCId;
};

export type RemoteAttentionResolveResponse = {
  jsonrpc: "2.0";
  result: RemoteAttentionResolveResult;
  id: JSONRPCId;
};

export type RemoteAttentionConfirmSuggestionParams = {
  threadID: string;
  requestID: string;
  optionID: string;
};

export type RemoteAttentionConfirmSuggestionResult = {
  ok: boolean;
};

export type RemoteAttentionConfirmSuggestionRequest = {
  jsonrpc: "2.0";
  method: "attention.confirmSuggestion";
  params: RemoteAttentionConfirmSuggestionParams;
  id: JSONRPCId;
};

export type RemoteAttentionConfirmSuggestionResponse = {
  jsonrpc: "2.0";
  result: RemoteAttentionConfirmSuggestionResult;
  id: JSONRPCId;
};

export type RemoteStewardTopic = {
  threadID: UUID;
  projectID: UUID;
  title: string;
  brief: string;
  openQuestions: number;
  goalID: string;
  closed: boolean;
  updatedAt: ISO8601DateTime;
  phase: "drafting" | "planning" | "plan_failed" | "plan_review" | "running" | "paused" | "completed" | "cancelled";
  tasksDone: number;
  tasksTotal: number;
};

export type RemoteStewardTopicsListParams = {
  projectID?: UUID;
};

export type RemoteStewardTopicsListResult = {
  topics: Array<RemoteStewardTopic>;
};

export type RemoteStewardTopicsListRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.list";
  params: RemoteStewardTopicsListParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsListResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsListResult;
  id: JSONRPCId;
};

export type RemoteStewardOpenParams = {
  projectID: UUID;
};

export type RemoteStewardOpenResult = {
  threadID: UUID;
};

export type RemoteStewardOpenRequest = {
  jsonrpc: "2.0";
  method: "steward.open";
  params: RemoteStewardOpenParams;
  id: JSONRPCId;
};

export type RemoteStewardOpenResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardOpenResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsStartParams = {
  projectID: UUID;
  threadID?: UUID;
  request: string;
};

export type RemoteStewardTopicsStartResult = {
  threadID: UUID;
  queued: boolean;
};

export type RemoteStewardTopicsStartRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.start";
  params: RemoteStewardTopicsStartParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsStartResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsStartResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsReplanParams = {
  projectID: UUID;
  threadID: UUID;
};

export type RemoteStewardTopicsReplanResult = Record<string, never>;

export type RemoteStewardTopicsReplanRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.replan";
  params: RemoteStewardTopicsReplanParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsReplanResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsReplanResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsConfirmParams = {
  projectID: UUID;
  threadID: UUID;
};

export type RemoteStewardTopicsConfirmResult = Record<string, never>;

export type RemoteStewardTopicsConfirmRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.confirm";
  params: RemoteStewardTopicsConfirmParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsConfirmResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsConfirmResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsCloseParams = {
  projectID: UUID;
  threadID: UUID;
};

export type RemoteStewardTopicsCloseResult = Record<string, never>;

export type RemoteStewardTopicsCloseRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.close";
  params: RemoteStewardTopicsCloseParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsCloseResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsCloseResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsDeleteParams = {
  projectID: UUID;
  threadID: UUID;
};

export type RemoteStewardTopicsDeleteResult = Record<string, never>;

export type RemoteStewardTopicsDeleteRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.delete";
  params: RemoteStewardTopicsDeleteParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsDeleteResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsDeleteResult;
  id: JSONRPCId;
};

export type RemoteStewardTopicsTasksReviewParams = {
  projectID: UUID;
  threadID: UUID;
  taskID: UUID;
  decision: "accept" | "reject";
  feedback?: string;
  revision: number;
};

export type RemoteStewardTopicsTasksReviewResult = Record<string, never>;

export type RemoteStewardTopicsTasksReviewRequest = {
  jsonrpc: "2.0";
  method: "steward.topics.tasks.review";
  params: RemoteStewardTopicsTasksReviewParams;
  id: JSONRPCId;
};

export type RemoteStewardTopicsTasksReviewResponse = {
  jsonrpc: "2.0";
  result: RemoteStewardTopicsTasksReviewResult;
  id: JSONRPCId;
};

export type RemoteStewardChangedParams = {
  projectID: string;
};

export type RemoteStewardChangedNotification = {
  jsonrpc: "2.0";
  method: "steward.changed";
  params: RemoteStewardChangedParams;
};

export type RemoteSessionDevicesChangedParams = {
  deviceID: UUID;
  revoked: boolean;
};

export type RemoteSessionDevicesChangedNotification = {
  jsonrpc: "2.0";
  method: "session.devices.changed";
  params: RemoteSessionDevicesChangedParams;
};

export type RemoteAttentionChangedParams = {
  unresolvedCount: number;
};

export type RemoteAttentionChangedNotification = {
  jsonrpc: "2.0";
  method: "attention.changed";
  params: RemoteAttentionChangedParams;
};

export type RemoteAutomationExtSchedule = {
  type: string;
  seconds?: number;
  date?: string;
  timeOfDay?: string;
  timezone?: string;
  dayOfWeek?: number;
  expression?: string;
};

export type RemoteAutomationExtAutomation = {
  id: string;
  projectID?: UUID;
  name: string;
  prompt: string;
  stopWhen?: string;
  schedule: RemoteAutomationExtSchedule;
  scheduleText: string;
  enabled: boolean;
  status?: string;
  lastRunAt?: string;
  lastRunStatus?: string;
  nextRunAt?: string;
  unreadCount: number;
  consecutiveFailures: number;
  maxConsecutiveFailures: number;
  maxIterations: number;
  maxRuns?: number;
  catchUp?: boolean;
  provider?: string;
  model?: string;
  effort?: string;
  runtimeMode?: string;
  runMode?: string;
  revision: number;
};

export type RemoteAutomationExtInput = {
  name: string;
  prompt: string;
  stopWhen?: string;
  schedule: RemoteAutomationExtSchedule;
  enabled?: boolean;
  maxConsecutiveFailures?: number;
  maxIterations?: number;
  maxRuns?: number;
  catchUp?: boolean;
  provider?: string;
  model?: string;
  effort?: string;
  runtimeMode?: "readonly" | "supervised" | "auto" | "fullAccess" | "";
};

export type RemoteAutomationExtAutomationResult = {
  automation: RemoteAutomationExtAutomation;
};

export type RemoteAutomationExtDeleteResult = {
  automationID: UUID;
  deleted: boolean;
};

export type RemoteAutomationExtListResult = {
  automations: Array<RemoteAutomationExtAutomation>;
  unreadCounts?: Record<string, number>;
};

export type RemoteAutomationExtRun = {
  id: string;
  automationID: UUID;
  status: string;
  trigger?: "scheduled" | "catch_up" | "manual";
  missedCount?: number;
  startedAt: string;
  finishedAt?: string;
  threadID?: UUID;
  read: boolean;
  archived: boolean;
  summary?: string;
  error?: string;
};

export type RemoteAutomationExtRunsResult = {
  automationID: UUID;
  runs: Array<RemoteAutomationExtRun>;
};

export type RemoteAutomationExtRunResult = {
  run: RemoteAutomationExtRun;
};

export type RemoteAutomationExtMarkReadResult = {
  runID: UUID;
};

export type RemoteAutomationExtArchiveRunResult = {
  runID: UUID;
  archived: boolean;
};

export type RemoteAutomationExtChanged = {
  automationID?: string;
  runID?: string;
};

export type RemoteAutomationsListParams = {
  projectID: UUID;
};

export type RemoteAutomationsListRequest = {
  jsonrpc: "2.0";
  method: "automations.list";
  params: RemoteAutomationsListParams;
  id: JSONRPCId;
};

export type RemoteAutomationsListResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtListResult;
  id: JSONRPCId;
};

export type RemoteAutomationsRunsParams = {
  automationID: UUID;
  limit?: number | null;
  includeArchived?: boolean;
};

export type RemoteAutomationsRunsRequest = {
  jsonrpc: "2.0";
  method: "automations.runs";
  params: RemoteAutomationsRunsParams;
  id: JSONRPCId;
};

export type RemoteAutomationsRunsResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtRunsResult;
  id: JSONRPCId;
};

export type RemoteAutomationsRunParams = {
  automationID: UUID;
};

export type RemoteAutomationsRunRequest = {
  jsonrpc: "2.0";
  method: "automations.run";
  params: RemoteAutomationsRunParams;
  id: JSONRPCId;
};

export type RemoteAutomationsRunResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtRunResult;
  id: JSONRPCId;
};

export type RemoteAutomationsMarkReadParams = {
  runID: UUID;
};

export type RemoteAutomationsMarkReadRequest = {
  jsonrpc: "2.0";
  method: "automations.markRead";
  params: RemoteAutomationsMarkReadParams;
  id: JSONRPCId;
};

export type RemoteAutomationsMarkReadResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtMarkReadResult;
  id: JSONRPCId;
};

export type RemoteAutomationsArchiveRunParams = {
  runID: UUID;
  archived: boolean;
};

export type RemoteAutomationsArchiveRunRequest = {
  jsonrpc: "2.0";
  method: "automations.archiveRun";
  params: RemoteAutomationsArchiveRunParams;
  id: JSONRPCId;
};

export type RemoteAutomationsArchiveRunResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtArchiveRunResult;
  id: JSONRPCId;
};

export type RemoteAutomationsCreateParams = {
  projectID: UUID;
  input: RemoteAutomationExtInput;
};

export type RemoteAutomationsCreateRequest = {
  jsonrpc: "2.0";
  method: "automations.create";
  params: RemoteAutomationsCreateParams;
  id: JSONRPCId;
};

export type RemoteAutomationsCreateResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtAutomationResult;
  id: JSONRPCId;
};

export type RemoteAutomationsUpdateParams = {
  automationID: UUID;
  input: RemoteAutomationExtInput;
  revision?: number;
};

export type RemoteAutomationsUpdateRequest = {
  jsonrpc: "2.0";
  method: "automations.update";
  params: RemoteAutomationsUpdateParams;
  id: JSONRPCId;
};

export type RemoteAutomationsUpdateResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtAutomationResult;
  id: JSONRPCId;
};

export type RemoteAutomationsDeleteParams = {
  automationID: UUID;
  confirm: boolean;
};

export type RemoteAutomationsDeleteRequest = {
  jsonrpc: "2.0";
  method: "automations.delete";
  params: RemoteAutomationsDeleteParams;
  id: JSONRPCId;
};

export type RemoteAutomationsDeleteResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtDeleteResult;
  id: JSONRPCId;
};

export type RemoteAutomationsSetEnabledParams = {
  automationID: UUID;
  enabled: boolean;
  revision?: number;
};

export type RemoteAutomationsSetEnabledRequest = {
  jsonrpc: "2.0";
  method: "automations.setEnabled";
  params: RemoteAutomationsSetEnabledParams;
  id: JSONRPCId;
};

export type RemoteAutomationsSetEnabledResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtAutomationResult;
  id: JSONRPCId;
};

export type RemoteAutomationsResetParams = {
  automationID: UUID;
  revision?: number;
};

export type RemoteAutomationsResetRequest = {
  jsonrpc: "2.0";
  method: "automations.reset";
  params: RemoteAutomationsResetParams;
  id: JSONRPCId;
};

export type RemoteAutomationsResetResponse = {
  jsonrpc: "2.0";
  result: RemoteAutomationExtAutomationResult;
  id: JSONRPCId;
};

export type RemoteAutomationsChangedNotification = {
  jsonrpc: "2.0";
  method: "automations.changed";
  params: RemoteAutomationExtChanged;
};

export type RemoteSettingsQuickReply = {
  label: string;
  message: string;
};

export type RemoteSettingsValue = string | number | boolean | Array<string> | null;

export type RemoteSettingsValues = Record<string, RemoteSettingsValue>;

export type RemoteSettingsSnapshot = {
  values: RemoteSettingsValues;
  writableKeys: Array<string>;
  quickReplies: Array<RemoteSettingsQuickReply>;
};

export type RemoteSettingsGetParams = Record<string, never>;

export type RemoteSettingsGetResult = RemoteSettingsSnapshot;

export type RemoteSettingsGetRequest = {
  jsonrpc: "2.0";
  method: "settings.get";
  params: RemoteSettingsGetParams;
  id: JSONRPCId;
};

export type RemoteSettingsGetResponse = {
  jsonrpc: "2.0";
  result: RemoteSettingsGetResult;
  id: JSONRPCId;
};

export type RemoteSettingsSetParams = {
  values: RemoteSettingsValues;
  actionID?: string;
};

export type RemoteSettingsSetResult = RemoteSettingsSnapshot;

export type RemoteSettingsSetRequest = {
  jsonrpc: "2.0";
  method: "settings.set";
  params: RemoteSettingsSetParams;
  id: JSONRPCId;
};

export type RemoteSettingsSetResponse = {
  jsonrpc: "2.0";
  result: RemoteSettingsSetResult;
  id: JSONRPCId;
};

export type RemoteSettingsChangedParams = {
  values: RemoteSettingsValues;
};

export type RemoteSettingsChangedNotification = {
  jsonrpc: "2.0";
  method: "settings.changed";
  params: RemoteSettingsChangedParams;
};

export type RemoteMCPServersListParams = {
  threadID?: UUID;
  projectID?: UUID;
};

export type RemoteMCPExtServer = {
  key: string;
  name: string;
  agent?: string;
  scope: string;
  source?: string;
  transport?: string;
  url?: string;
  command?: string;
  disabled: boolean;
  locked?: boolean;
  lockReason?: string;
};

export type RemoteMCPExtListServersResult = {
  threadID?: UUID;
  projectID?: UUID;
  servers: Array<RemoteMCPExtServer>;
};

export type RemoteMCPServersListRequest = {
  jsonrpc: "2.0";
  method: "mcp.servers.list";
  params: RemoteMCPServersListParams;
  id: JSONRPCId;
};

export type RemoteMCPServersListResponse = {
  jsonrpc: "2.0";
  result: RemoteMCPExtListServersResult;
  id: JSONRPCId;
};

export type RemoteProjectExtAddParams = {
  path: string;
};

export type RemoteProjectExtAddResult = {
  project: RemoteProjectSummary;
};

export type RemoteProjectsAddRequest = {
  jsonrpc: "2.0";
  method: "projects.add";
  params: RemoteProjectExtAddParams;
  id: JSONRPCId;
};

export type RemoteProjectsAddResponse = {
  jsonrpc: "2.0";
  result: RemoteProjectExtAddResult;
  id: JSONRPCId;
};

export type RemoteProjectExtValidateParams = {
  path: string;
};

export type RemoteProjectExtValidateResult = {
  ok: boolean;
  normalizedPath: string;
  reason: string;
};

export type RemoteProjectsValidatePathRequest = {
  jsonrpc: "2.0";
  method: "projects.validatePath";
  params: RemoteProjectExtValidateParams;
  id: JSONRPCId;
};

export type RemoteProjectsValidatePathResponse = {
  jsonrpc: "2.0";
  result: RemoteProjectExtValidateResult;
  id: JSONRPCId;
};

export type RemoteProjectExtChangedParams = {
  projectID: UUID;
};

export type RemoteProjectsChangedNotification = {
  jsonrpc: "2.0";
  method: "projects.changed";
  params: RemoteProjectExtChangedParams;
};

export type RemoteThreadsRevertParams = {
  threadID: UUID;
  turnID: UUID;
};

export type RemoteThreadsRevertFile = {
  path: string;
  additions: number;
  deletions: number;
  kept: boolean;
  inBase: boolean;
  restorable: boolean;
};

export type RemoteThreadsRevertPreviewResult = {
  threadID: UUID;
  turnID: UUID;
  base?: string;
  finish?: string;
  total: number;
  restorableCount: number;
  keptCount: number;
  kept: Array<string>;
  files: Array<RemoteThreadsRevertFile>;
};

export type RemoteThreadsRevertApplyResult = {
  threadID: UUID;
  turnID: UUID;
  base?: string;
  finish?: string;
  total: number;
  restorableCount: number;
  keptCount: number;
  kept: Array<string>;
  files: Array<RemoteThreadsRevertFile>;
  ok: boolean;
};

export type RemoteThreadsRevertPreviewRequest = {
  jsonrpc: "2.0";
  method: "threads.revert.preview";
  params: RemoteThreadsRevertParams;
  id: JSONRPCId;
};

export type RemoteThreadsRevertPreviewResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsRevertPreviewResult;
  id: JSONRPCId;
};

export type RemoteThreadsRevertApplyRequest = {
  jsonrpc: "2.0";
  method: "threads.revert.apply";
  params: RemoteThreadsRevertParams;
  id: JSONRPCId;
};

export type RemoteThreadsRevertApplyResponse = {
  jsonrpc: "2.0";
  result: RemoteThreadsRevertApplyResult;
  id: JSONRPCId;
};

export type RemoteProjectExtCloneParams = {
  url: string;
  parentPath?: string;
  name?: string;
};

export type RemoteProjectsCloneRequest = {
  jsonrpc: "2.0";
  method: "projects.clone";
  params: RemoteProjectExtCloneParams;
  id: JSONRPCId;
};

export type RemoteProjectsCloneResponse = {
  jsonrpc: "2.0";
  result: RemoteProjectExtAddResult;
  id: JSONRPCId;
};

export type RemoteProjectExtListDirParams = {
  path?: string;
  showHidden?: boolean;
};

export type RemoteProjectExtDirEntry = {
  name: string;
  path: string;
  isGitRepo: boolean;
};

export type RemoteProjectExtListDirResult = {
  path: string;
  parent?: string;
  entries: Array<RemoteProjectExtDirEntry>;
  truncated: boolean;
};

export type RemoteProjectsListDirRequest = {
  jsonrpc: "2.0";
  method: "projects.listDir";
  params: RemoteProjectExtListDirParams;
  id: JSONRPCId;
};

export type RemoteProjectsListDirResponse = {
  jsonrpc: "2.0";
  result: RemoteProjectExtListDirResult;
  id: JSONRPCId;
};

export type ScriptsSaveParams = {
  projectID: UUID;
  scripts: Array<ScriptsSaveScript>;
  expectedRevision?: string;
};

export type ScriptsSaveScript = {
  id?: string;
  name: string;
  command: string;
  symbol?: string | null;
  runOnWorktreeCreate?: boolean;
};

export type ScriptsSaveRequest = {
  jsonrpc: "2.0";
  method: "scripts.save";
  params: ScriptsSaveParams;
  id: JSONRPCId;
};

export type ScriptsSaveResponse = {
  jsonrpc: "2.0";
  result: ScriptsListResult;
  id: JSONRPCId;
};

export type KanbanDeleteTaskParams = {
  taskID: UUID;
  expectedRevision: number;
  actionID: UUID;
};

export type KanbanDeleteTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.deleteTask";
  params: KanbanDeleteTaskParams;
  id: JSONRPCId;
};

export type KanbanRescueTaskParams = {
  taskID: UUID;
  action: "interrupt" | "nudge";
  expectedRevision: number;
  actionID: UUID;
};

export type KanbanRescueTaskRequest = {
  jsonrpc: "2.0";
  method: "kanban.rescueTask";
  params: KanbanRescueTaskParams;
  id: JSONRPCId;
};

export type RemoteMemoryCreateParams = {
  projectID: UUID;
  threadID?: UUID;
  title: string;
  content?: string;
  type?: string;
  importance?: number;
  scope?: "project" | "global";
};

export type RemoteMemoryCreateRequest = {
  jsonrpc: "2.0";
  method: "memory.create";
  params: RemoteMemoryCreateParams;
  id: JSONRPCId;
};

export type RemoteMemoryCreateResponse = {
  jsonrpc: "2.0";
  result: RemoteMemoryProposal;
  id: JSONRPCId;
};

export type RemoteMemorySearchParams = {
  projectID?: UUID;
  query: string;
  limit?: number;
};

export type RemoteMemorySearchEntry = {
  id: string;
  title: string;
  content: string;
  unitType?: string;
  importance: number;
  labels?: Array<string>;
  createdAt: string;
};

export type RemoteMemorySearchResult = {
  results: Array<RemoteMemorySearchEntry>;
};

export type RemoteMemorySearchRequest = {
  jsonrpc: "2.0";
  method: "memory.search";
  params: RemoteMemorySearchParams;
  id: JSONRPCId;
};

export type RemoteMemorySearchResponse = {
  jsonrpc: "2.0";
  result: RemoteMemorySearchResult;
  id: JSONRPCId;
};

export type RemoteMemoryProposalsAcceptParams = {
  id: string;
  title?: string;
  content?: string;
  type?: string;
  importance?: number;
};
