// JSON-RPC 2.0 客户端实现，专为 mcode 远程控制设计
// 满足超时处理、递增 ID、挂起请求拒绝、指数退避重连与凭据安全性规范

import {
  Board,
  ConnectionState,
  GoalCreateParams,
  GoalMutationParams,
  GoalReplanParams,
  GoalTaskMutationParams,
  GetDiffParams,
  GetDiffResult,
  JsonRpcRequest,
  JsonRpcResponse,
  MoveTaskParams,
  RemoteCapabilityDeclaration,
  RemoteProjectSummary,
  RemoteThreadsActionStatusParams,
  RemoteThreadsActionStatusResult,
  RemoteThreadsSettleActionParams,
  RemoteThreadsSettleActionResult,
  RemoteRequest,
  RemoteRequestsChangedParams,
  RemoteRequestsListParams,
  RemoteRequestsListResult,
  RemoteRequestsRespondParams,
  RemoteRequestsRespondResult,
  RemoteThreadsInterruptParams,
  RemoteThreadsInterruptResult,
  ThreadsChangedParams,
  ThreadsListResult,
  ThreadsMoveParams,
  ThreadsMoveResult,
  ThreadsUpdateParams,
  ThreadsUpdateResult,
  RemoteThreadsCreateParams,
  RemoteThreadsCreateResult,
  RemoteProvidersListResult,
  RemoteThreadsQueueChangedParams,
  RemoteThreadsQueueInterruptAndSendParams,
  RemoteThreadsQueueInterruptAndSendResult,
  RemoteThreadsQueueListResult,
  RemoteThreadsQueueMutationParams,
  RemoteThreadsQueueMutationResult,
  RemoteAttachmentUploadParams,
  RemoteAttachmentUploadResult,
  RemoteThreadHistoryPage,
  RemoteThreadSummary,
  RemoteThreadsSendMessageParams,
  RemoteThreadsSendMessageResult,
  ReviewTaskParams,
  StartTaskParams,
  ThreadsListParams,
  ThreadsEventParams,
  ThreadsSubscribeParams,
  ThreadsSubscribeResult,
  ThreadsUnsubscribeParams,
  ThreadsUnsubscribeResult,
  ThreadsGetDiffParams,
  ThreadsGetDiffResult,
  RemoteFilesListResult,
  RemoteFilesReadResult,
  RemoteFilesSearchResult,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult,
  RemoteGitStatusResult,
  RemoteGitBranchesResult,
  RemoteThreadsCommandsParams,
  RemoteThreadsCommandsResult,
  RemoteThreadsContinueParams,
  RemoteThreadsContinueResult
} from '../types/contract';
import type { KanbanCreateTaskParams, KanbanTaskMutationResult } from '../types/contract';
import type {
  RemoteSwarmMergeDiffResult,
  RemoteSwarmTier,
  RemoteSwarmTiersResult,
  RemoteThreadsBulkArchiveParams,
  RemoteThreadsBulkArchiveResult,
  RemoteThreadsBulkMarkReadParams,
  RemoteThreadsBulkMarkReadResult,
  RemoteThreadsBulkPreviewParams,
  RemoteThreadsBulkPreviewResult,
  RemoteThreadsBulkMarkDormantParams,
  RemoteThreadsBulkMarkDormantResult,
  RemoteProvidersRememberSelectionParams,
  RemoteThreadsBulkUndoParams,
  RemoteThreadsBulkUndoResult,
  RemoteThreadsKillRunningCommandParams,
  RemoteThreadsKillRunningCommandResult,
  RemoteThreadsProvisionalReleaseParams,
  RemoteThreadsProvisionalReleaseResult,
  RemoteThreadsRetainParams,
  RemoteThreadsRetainResult,
  RemoteThreadsTitleRegenerateParams,
  RemoteThreadsTitleRegenerateResult,
  RemoteThreadsQueueMoveParams,
  RemoteThreadsTurnRecallParams,
  RemoteThreadsTurnRecallResult,
  RuntimeCompactSuggestionParams,
  RuntimeWorktreeExecuteParams,
  RuntimeWorktreeExecuteResult,
  RuntimeWorktreePreviewParams,
  RuntimeWorktreePreviewResult,
  RuntimeLimitTextResult,
  RuntimeQueueEditParams,
  RuntimeQueueEditResult,
  SessionAuthenticateResult,
  SessionNewPairingCodeResult,
  SessionPairResult,
  SessionRenameDeviceResult,
  SessionRevokeDeviceResult
} from '../types/generated';
import type { RemoteSwarmState } from '../types/swarm';
import type { ComposerQuotaScope, RemoteComposerQuotaResult } from '../types/quota';
import type {
  RemoteScriptsFavoriteParams,
  RemoteScriptsFavoriteResult,
  RemoteScriptsListResult,
  RemoteScriptsRunParams,
  RemoteScriptsRunResult,
  RemoteScriptsRunStatus,
  RemoteScriptsRunStatusParams
} from '../context/remoteScriptsApi';
import {
  clearSessionCredentials,
  DeviceIdentity,
  saveSessionCredentials,
  SessionCredentials
} from './storage';

export interface RpcClientOptions {
  onConnectionChange: (state: ConnectionState, message?: string) => void;
  onBoardUpdated: (board: Board) => void;
  onAuthFailed: (errorMessage: string) => void;
  onCredentialsChanged: (credentials: SessionCredentials | null, notice: string | null) => void;
  onThreadEvent?: (event: ThreadsEventParams) => void;
  onThreadsChanged?: (change: ThreadsChangedParams) => void;
  onRequestsChanged?: (change: RemoteRequestsChangedParams) => void;
  onQueueChanged?: (change: RemoteThreadsQueueChangedParams) => void;
  onSwarmChanged?: (state: RemoteSwarmState) => void;
}

export type RemoteProviderCatalogItem = {
  provider: string;
  modelID: string;
};

export type RemoteProvidersListResultWithCatalog = RemoteProvidersListResult & {
  catalog?: RemoteProviderCatalogItem[];
};

export interface ThreadsBranchParams {
  threadID: string;
  messageID: string;
  actionID: string;
}

export interface ThreadsBranchResult {
  threadID: string;
}

export interface PushPublicKeyResult {
  publicKey: string;
}
export type RemotePushPublicKeyResult = PushPublicKeyResult;

export interface PushSubscribeParams {
  endpoint: string;
  p256dh: string;
  auth: string;
}
export type RemotePushSubscribeParams = PushSubscribeParams;

export interface PushSubscribeResult {
  subscribed: boolean;
}
export type RemotePushSubscribeResult = PushSubscribeResult;

export interface PushUnsubscribeParams {
  endpoint: string;
}
export type RemotePushUnsubscribeParams = PushUnsubscribeParams;

export interface PushUnsubscribeResult {
  unsubscribed: boolean;
}
export type RemotePushUnsubscribeResult = PushUnsubscribeResult;

const HEARTBEAT_INTERVAL_MS = 25000;
const HEARTBEAT_TIMEOUT_MS = 8000;
// 建连超时：10 秒内未进入 OPEN 就放弃该 socket 并退避重连
const CONNECT_TIMEOUT_MS = 10000;
// 稳定连接判定：鉴权成功后维持 10 秒才把重连退避计数清零
const STABLE_CONNECTION_MS = 10000;

export class RpcError extends Error {
  constructor(readonly code: number, message: string, readonly data?: unknown) {
    super(message);
  }

  /** 宿主附带的 error.data.reason（如 notFound / unavailable / tooLarge），没有则为 undefined */
  get reason(): string | undefined {
    return isRecord(this.data) && typeof this.data.reason === 'string' ? this.data.reason : undefined;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null;
}

function isNullableString(value: unknown): value is string | null {
  return value === null || typeof value === 'string';
}

function deviceIdentityFrom(value: unknown): DeviceIdentity {
  if (
    !isRecord(value) ||
    typeof value.deviceID !== 'string' ||
    !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(value.deviceID) ||
    typeof value.label !== 'string' || !value.label.trim()
  ) {
    throw new Error('Mac 端未返回有效的设备身份，请重新连接');
  }
  return { deviceID: value.deviceID, label: value.label };
}

function isThreadSummary(value: unknown): value is RemoteThreadSummary {
  if (!isRecord(value)) return false;
  return (
    typeof value.id === 'string' &&
    typeof value.projectID === 'string' &&
    typeof value.title === 'string' &&
    typeof value.provider === 'string' &&
    isNullableString(value.model) &&
    isNullableString(value.effort) &&
    typeof value.isRunning === 'boolean' &&
    typeof value.isArchived === 'boolean' &&
    typeof value.isPinned === 'boolean' &&
    typeof value.isDormant === 'boolean' &&
    typeof value.hasUnread === 'boolean' &&
    typeof value.attention === 'string' &&
    isNullableString(value.lastActivityText) &&
    typeof value.section === 'string' &&
    typeof value.orderKey === 'string' &&
    typeof value.updatedAt === 'string' &&
    (value.lastUserMessageAt === undefined || typeof value.lastUserMessageAt === 'string') &&
    isNullableString(value.lastStatus) &&
    typeof value.hasPendingRequest === 'boolean'
  );
}

function parseThreadsChanged(value: unknown): ThreadsChangedParams | null {
  if (!isRecord(value) || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision)) return null;
  if (!Array.isArray(value.threads) || !value.threads.every(isThreadSummary)) return null;
  if (!Array.isArray(value.removedThreadIDs) || !value.removedThreadIDs.every((id) => typeof id === 'string')) return null;
  return value as ThreadsChangedParams;
}

function isRemoteRequest(value: unknown): value is RemoteRequest {
  if (!isRecord(value)) return false;
  return (
    typeof value.threadID === 'string' &&
    (value.turnID === null || typeof value.turnID === 'string') &&
    typeof value.runID === 'string' && value.runID.length > 0 &&
    typeof value.requestID === 'string' && value.requestID.length > 0 &&
    typeof value.kind === 'string' &&
    isNullableString(value.title) &&
    isNullableString(value.detail) &&
    isNullableString(value.reason) &&
    Array.isArray(value.options) &&
    value.options.every((option) =>
      isRecord(option) &&
      typeof option.id === 'string' &&
      typeof option.title === 'string' &&
      typeof option.role === 'string'
    ) &&
    Array.isArray(value.questions) &&
    value.questions.every((question) =>
      isRecord(question) &&
      typeof question.id === 'string' &&
      typeof question.header === 'string' &&
      typeof question.prompt === 'string' &&
      typeof question.allowsMultiple === 'boolean' &&
      typeof question.allowsOther === 'boolean' &&
      typeof question.isSecret === 'boolean' &&
      Array.isArray(question.choices) &&
      question.choices.every((choice) =>
        isRecord(choice) && typeof choice.label === 'string' && isNullableString(choice.detail)
      )
    )
  );
}

function parseRequestsChanged(value: unknown): RemoteRequestsChangedParams | null {
  if (!isRecord(value) || typeof value.threadID !== 'string' || !Array.isArray(value.requests)) return null;
  if (!value.requests.every(isRemoteRequest)) return null;
  return value as RemoteRequestsChangedParams;
}

function parseQueueChanged(value: unknown): RemoteThreadsQueueChangedParams | null {
  if (!isRecord(value) || typeof value.threadID !== 'string' ||
      typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision) || value.revision < 0 ||
      !Array.isArray(value.followUps)) {
    return null;
  }
  if (!value.followUps.every((followUp) => {
    if (!isRecord(followUp) || typeof followUp.id !== 'string' || typeof followUp.text !== 'string' ||
        typeof followUp.createdAt !== 'string' ||
        !(followUp.bundleID === null || typeof followUp.bundleID === 'string') ||
        !Array.isArray(followUp.attachments)) {
      return false;
    }
    return followUp.attachments.every((attachment) =>
      isRecord(attachment) && typeof attachment.id === 'string' &&
      typeof attachment.name === 'string' && typeof attachment.mimeType === 'string' &&
      (attachment.byteCount === null || (typeof attachment.byteCount === 'number' && Number.isSafeInteger(attachment.byteCount)))
    );
  })) return null;
  return value as RemoteThreadsQueueChangedParams;
}

function isThreadMessage(value: unknown): boolean {
  if (!isRecord(value)) return false;
  return (
    typeof value.id === 'string' &&
    typeof value.role === 'string' &&
    typeof value.text === 'string' &&
    typeof value.createdAt === 'string' &&
    Number.isSafeInteger(value.turnIndex) &&
    (value.turnID === undefined || typeof value.turnID === 'string') &&
    (value.kind === undefined || typeof value.kind === 'string') &&
    (value.turnStatus === undefined || typeof value.turnStatus === 'string') &&
    (value.isFinalResponse === undefined || typeof value.isFinalResponse === 'boolean')
  );
}

function parseThreadEvent(value: unknown): ThreadsEventParams | null {
  if (
    !isRecord(value) ||
    typeof value.threadID !== 'string' ||
    value.threadID.length === 0 ||
    typeof value.runID !== 'string' ||
    value.runID.length === 0
  ) {
    return null;
  }
  if (typeof value.sequence !== 'number' || !Number.isSafeInteger(value.sequence) || value.sequence < 1) return null;
  if (
    !isRecord(value.payload) ||
    !isThreadSummary(value.payload.thread) ||
    value.payload.thread.id !== value.threadID
  ) return null;

  if (
    value.kind === 'projection' &&
    Array.isArray(value.payload.messages) &&
    value.payload.messages.every(isThreadMessage) &&
    Array.isArray(value.payload.removedMessageIDs) &&
    value.payload.removedMessageIDs.every((id) => typeof id === 'string')
  ) {
    return value as unknown as ThreadsEventParams;
  }

  if (value.kind === 'resync') {
    return value as unknown as ThreadsEventParams;
  }

  return null;
}

function parseCompactSuggestion(value: unknown): RuntimeCompactSuggestionParams | null {
  if (!isRecord(value)) return null;
  if (typeof value.threadID !== 'string') return null;
  if (typeof value.usedTokens !== 'number') return null;
  if (typeof value.idleSince !== 'string') return null;
  return value as unknown as RuntimeCompactSuggestionParams;
}

function parseSwarmState(value: unknown): RemoteSwarmState | null {
  if (!isRecord(value)) return null;
  if (typeof value.threadID !== 'string') return null;
  if (typeof value.swarmAvailable !== 'boolean') return null;
  if (typeof value.swarmEnabled !== 'boolean') return null;
  if (typeof value.isBee !== 'boolean') return null;
  if (!Array.isArray(value.bees)) return null;
  return value as unknown as RemoteSwarmState;
}

export class McodeRpcClient {
  private ws: WebSocket | null = null;
  private swarmListeners = new Set<(state: RemoteSwarmState) => void>();
  private compactSuggestionListeners = new Set<(offer: RuntimeCompactSuggestionParams) => void>();
  /** 通用通知监听器：按方法名精确匹配，服务端新增方法时无需改动此处。 */
  private notificationListeners = new Set<{ method: string; handler: (params: unknown) => void }>();
  private nextId = 1;
  private pendingRequests = new Map<
    number,
    {
      resolve: (data: unknown) => void;
      reject: (err: Error) => void;
      timer: ReturnType<typeof setTimeout>;
      method: string;
    }
  >();
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private connectTimer: ReturnType<typeof setTimeout> | null = null;
  private stableTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectAttempts = 0;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private isDisposed = false;
  private isAuthFailed = false;
  private isAuthenticated = false;
  private authenticatedGeneration = -1;
  private authGeneration = 0;
  private pendingPairToken: string | null = null;
  private credentials: SessionCredentials | null;
  private rememberDevice: boolean;
  private isRevokingDevice = false;
  private lastBoardRevision = -1;
  private currentBoard: Board | null = null;
  // 同一时刻只保留一个在途 kanban.getBoard：鉴权后的初始快照与页面恢复/手动刷新可能同时发起。
  private boardInFlight: Promise<Board> | null = null;
  private capabilities: RemoteCapabilityDeclaration | null = null;
  private options: RpcClientOptions;

  constructor(
    options: RpcClientOptions,
    pairToken: string | null,
    credentials: SessionCredentials | null = null,
    rememberDevice = true
  ) {
    this.options = options;
    this.pendingPairToken = pairToken;
    this.credentials = credentials;
    this.rememberDevice = rememberDevice;

    // 绑定页面关闭时卸载清理
    if (typeof window !== 'undefined') {
      window.addEventListener('pagehide', this.handlePageHide);
    }
  }

  // iOS/PWA 上 beforeunload 不可靠；pagehide 且非 bfcache 缓存时才是真正的页面卸载。
  private handlePageHide = (event: PageTransitionEvent): void => {
    if (!event.persisted) this.dispose();
  };

  /**
   * 当前是否已建立并通过鉴权的可用连接。
   * 页面恢复探测据此决定「只刷新看板」还是「走既有重连流程」。
   */
  public get isReady(): boolean {
    return (
      !this.isDisposed &&
      !this.isAuthFailed &&
      this.isAuthenticated &&
      this.authenticatedGeneration === this.authGeneration &&
      this.ws !== null &&
      this.ws.readyState === WebSocket.OPEN
    );
  }

  public get canReconnect(): boolean {
    return !this.isDisposed && !this.isAuthFailed && !this.isRevokingDevice && Boolean(this.pendingPairToken || this.credentials);
  }

  public setRememberDevice(rememberDevice: boolean): void {
    if (this.isDisposed) return;
    this.rememberDevice = rememberDevice;
    if (this.credentials) {
      this.saveCredentials({ ...this.credentials, rememberDevice });
    }
  }

  private saveCredentials(credentials: SessionCredentials): void {
    this.credentials = credentials;
    const notice = saveSessionCredentials(credentials);
    this.options.onCredentialsChanged(credentials, notice);
  }

  /**
   * 启动连接流程
   */
  public start(): void {
    if (this.isDisposed || this.isAuthFailed || this.isRevokingDevice) return;
    this.authGeneration++;
    this.isAuthenticated = false;
    this.authenticatedGeneration = -1;
    this.reconnectAttempts = 0;
    this.connect();
  }

  /**
   * 重置鉴权状态并重新配对
   */
  public setPairTokenAndConnect(pairToken: string): void {
    if (this.isDisposed || this.isRevokingDevice) return;
    this.authGeneration++;
    this.pendingPairToken = pairToken;
    this.isAuthFailed = false;
    this.isAuthenticated = false;
    this.authenticatedGeneration = -1;
    this.lastBoardRevision = -1;
    this.reconnectAttempts = 0;
    this.cleanupPendingRequests('切换认证，请求已终止；状态待确认，请刷新看板确认最新状态');
    this.connect();
  }

  /**
   * 建立 WebSocket 连接
   */
  private connect(): void {
    if (this.isDisposed || this.isAuthFailed || this.isRevokingDevice) return;

    this.authGeneration++;

    // 清理之前的重连计时器
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.clearConnectTimer();
    this.clearStableTimer();

    const sessionToken = this.credentials?.token;
    if (!this.pendingPairToken && !sessionToken) {
      this.options.onConnectionChange('unpaired', '未检测到配对凭据，请在 Mac 端扫描二维码配对');
      return;
    }

    this.stopHeartbeat();

    // 关闭已有尚未清理的旧 socket
    if (this.ws) {
      this.isAuthenticated = false;
      this.authenticatedGeneration = -1;
      this.cleanupPendingRequests('连接已更换，请求已终止；状态待确认，请刷新看板确认最新状态');
      try {
        this.ws.onclose = null;
        this.ws.onerror = null;
        this.ws.onmessage = null;
        this.ws.onopen = null;
        this.ws.close();
      } catch {
        // 忽略
      }
      this.ws = null;
    } else {
      this.cleanupPendingRequests('连接已重置，请求已终止；状态待确认，请刷新看板确认最新状态');
    }

    const isReconnecting = this.reconnectAttempts > 0;
    this.options.onConnectionChange(
      isReconnecting ? 'reconnecting' : 'connecting',
      isReconnecting ? `正在尝试重新连接 (第 ${this.reconnectAttempts} 次)...` : '正在连接 Mac 端 mcode...'
    );

    try {
      const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
      const wsUrl = `${protocol}//${window.location.host}/ws`;
      const socket = new WebSocket(wsUrl);
      this.ws = socket;

      socket.onopen = this.handleOpen;
      socket.onmessage = this.handleMessage;
      socket.onclose = this.handleClose;
      socket.onerror = this.handleError;

      // 浏览器可能长时间停留在 CONNECTING：主动放弃这个 socket，走退避重连
      this.connectTimer = setTimeout(() => {
        this.connectTimer = null;
        if (this.isDisposed || this.ws !== socket || socket.readyState === WebSocket.OPEN) return;
        try {
          socket.onopen = null;
          socket.onclose = null;
          socket.onerror = null;
          socket.onmessage = null;
          socket.close();
        } catch {
          // 忽略
        }
        if (this.ws === socket) this.ws = null;
        this.cleanupPendingRequests('连接超时，请求已终止；状态待确认，请刷新看板确认最新状态');
        this.options.onConnectionChange('reconnecting', '连接 Mac 超时，正在重试...');
        this.scheduleReconnect();
      }, CONNECT_TIMEOUT_MS);
    } catch {
      this.scheduleReconnect();
    }
  }

  private handleOpen = async (event: Event): Promise<void> => {
    const socket = event.currentTarget instanceof WebSocket ? event.currentTarget : this.ws;
    const currentAuthGen = this.authGeneration;
    if (this.isDisposed || !socket || this.ws !== socket || currentAuthGen !== this.authGeneration) return;
    this.clearConnectTimer();
    const pairToken = this.pendingPairToken;
    let authenticating = true;

    try {
      if (pairToken) {
        // 1. 发起短期配对请求
        const res = await this.sendRpc<SessionPairResult>('session.pair', { token: pairToken }, 15000, socket);
        if (this.isDisposed || this.ws !== socket || socket.readyState !== WebSocket.OPEN || currentAuthGen !== this.authGeneration) return;
        if (res && typeof res.sessionToken === 'string' && /^[0-9a-f]{64}$/i.test(res.sessionToken)) {
          this.credentials = { token: res.sessionToken, deviceIdentity: null, rememberDevice: this.rememberDevice };
          this.pendingPairToken = null;
          this.saveCredentials({ ...this.credentials, deviceIdentity: deviceIdentityFrom(res) });
        } else {
          throw new Error('未返回有效的 sessionToken');
        }
      } else {
        // 2. 发起会话重新鉴权
        const sessionToken = this.credentials?.token;
        if (!sessionToken) {
          throw new Error('缺少会话凭据');
        }
        const res = await this.sendRpc<SessionAuthenticateResult>('session.authenticate', { token: sessionToken }, 15000, socket);
        if (this.isDisposed || this.ws !== socket || socket.readyState !== WebSocket.OPEN || currentAuthGen !== this.authGeneration) return;
        if (!res || res.authenticated !== true) throw new Error('Mac 端未确认会话认证，请重新连接');
        this.saveCredentials({ token: sessionToken, deviceIdentity: deviceIdentityFrom(res), rememberDevice: this.rememberDevice });
      }

      if (this.isDisposed || this.ws !== socket || socket.readyState !== WebSocket.OPEN || currentAuthGen !== this.authGeneration) return;
      this.isAuthenticated = true;
      this.authenticatedGeneration = currentAuthGen;
      authenticating = false;
      this.capabilities = null;
      this.currentBoard = null;
      // 每次重新认证都重新获取本次服务的 revision。
      this.lastBoardRevision = -1;
      // 获取看板初始状态
      const initialBoard = await this.getBoard();
      if (this.isDisposed || this.ws !== socket || socket.readyState !== WebSocket.OPEN || currentAuthGen !== this.authGeneration) return;
      this.publishBoard(initialBoard);
      this.options.onConnectionChange('connected', '已连接至 Mac 桌面端');
      this.startHeartbeat(socket);

      // 连接稳定 10 秒后才清零退避计数，避免刚连上又断开时延迟反复退回约 1 秒
      this.clearStableTimer();
      this.stableTimer = setTimeout(() => {
        this.stableTimer = null;
        if (this.isDisposed || this.ws !== socket || !this.isReady) return;
        this.reconnectAttempts = 0;
      }, STABLE_CONNECTION_MS);
    } catch (err) {
      if (this.isDisposed || this.ws !== socket || currentAuthGen !== this.authGeneration) return;
      if (!(err instanceof RpcError && (err.code === -32001 || (authenticating && err.code === -32602)))) {
        this.options.onConnectionChange('reconnecting', '连接暂时中断，正在重连；请检查任务状态后再操作');
        socket.close();
        return;
      }
      // 只有明确的认证错误才撤销本地凭据。
      this.isAuthenticated = false;
      this.authenticatedGeneration = -1;
      this.lastBoardRevision = -1;
      this.cleanupPendingRequests('认证失败，请求已终止');
      this.credentials = null;
      this.options.onCredentialsChanged(null, clearSessionCredentials());
      this.pendingPairToken = null;
      this.isAuthFailed = true;

      this.options.onAuthFailed(pairToken
        ? '配对码无效、已使用或已过期，请在 Mac 设置 → 远程控制生成新码后重试。'
        : '会话凭据已过期或已被撤销，请使用 Mac 上的新配对码重新配对。');
      this.options.onConnectionChange('auth_failed', '配对凭据已失效');

      if (this.ws) {
        try {
          this.ws.close();
        } catch {
          // 忽略
        }
      }
    }
  };

  private handleMessage = (event: MessageEvent): void => {
    if (this.isDisposed || event.currentTarget !== this.ws) return;
    if (typeof event.data !== 'string') return;

    try {
      const message = JSON.parse(event.data);

      // 通用通知分发：任何方法名（含尚未识别的）都会回调已注册的监听器；
      // 下面按方法名的既有处理保持原样，不受影响。
      if (message.jsonrpc === '2.0' && typeof message.method === 'string' && this.isReady) {
        this.dispatchNotification(message.method, message.params);
      }

      // 处理服务端主动推送的通知 (JSON-RPC notification)。通知只能来自当前已鉴权 socket。
      if (
        message.jsonrpc === '2.0' &&
        message.method === 'kanban.taskUpdated' &&
        message.params &&
        this.isReady
      ) {
        this.publishBoard(message.params as Board);
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'threads.changed' && this.isReady) {
        const changed = parseThreadsChanged(message.params);
        if (changed) this.options.onThreadsChanged?.(changed);
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'requests.changed' && this.isReady) {
        const changed = parseRequestsChanged(message.params);
        if (changed) this.options.onRequestsChanged?.(changed);
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'threads.queue.changed' && this.isReady) {
        const changed = parseQueueChanged(message.params);
        if (changed) this.options.onQueueChanged?.(changed);
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'threads.event' && this.isReady) {
        const parsed = parseThreadEvent(message.params);
        if (parsed) this.options.onThreadEvent?.(parsed);
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'threads.swarm.changed' && this.isReady) {
        const swarmState = parseSwarmState(message.params);
        if (swarmState) {
          this.options.onSwarmChanged?.(swarmState);
          for (const listener of this.swarmListeners) {
            try {
              listener(swarmState);
            } catch {
              // 忽略单个监听器内部异常
            }
          }
        }
        return;
      }

      if (message.jsonrpc === '2.0' && message.method === 'threads.compactSuggestion' && this.isReady) {
        const offer = parseCompactSuggestion(message.params);
        if (offer) {
          for (const listener of this.compactSuggestionListeners) {
            try {
              listener(offer);
            } catch {
              // 忽略单个监听器内部异常
            }
          }
        }
        return;
      }

      // 处理请求响应 (JSON-RPC response with id)
      if (typeof message.id === 'number') {
        const pending = this.pendingRequests.get(message.id);
        if (pending) {
          clearTimeout(pending.timer);
          this.pendingRequests.delete(message.id);

          const rpcRes = message as JsonRpcResponse;
          if (rpcRes.error) {
            pending.reject(new RpcError(rpcRes.error.code, rpcRes.error.message || `RPC Error ${rpcRes.error.code}`, rpcRes.error.data));
          } else {
            pending.resolve(rpcRes.result);
          }
        }
      }
    } catch {
      // 忽略无法解析的脏数据帧
    }
  };

  private handleClose = (event: CloseEvent): void => {
    if (this.isDisposed || event.currentTarget !== this.ws) return;
    this.clearConnectTimer();
    this.clearStableTimer();
    this.stopHeartbeat();
    this.isAuthenticated = false;
    this.authenticatedGeneration = -1;
    this.capabilities = null;
    this.currentBoard = null;
    this.cleanupPendingRequests('连接已断开，请求已终止；状态待确认，请刷新看板确认最新状态');

    if (this.isDisposed || this.isAuthFailed) return;
    if (this.isRevokingDevice) {
      this.options.onConnectionChange('disconnected', '撤销设备期间连接已断开，正在确认结果');
      return;
    }

    this.options.onConnectionChange('reconnecting', '与 Mac 端连接中断，等待重连...');
    this.scheduleReconnect();
  };

  private publishBoard(board: Board): boolean {
    if (
      !board || board.schemaVersion !== 1 || !Array.isArray(board.tasks) ||
      !Array.isArray(board.projects) || !Number.isSafeInteger(board.revision) || board.revision < 0
    ) {
      throw new Error('看板快照无效，请重新同步');
    }
    if (board.revision < this.lastBoardRevision) return false;
    this.lastBoardRevision = board.revision;
    this.currentBoard = board;
    this.options.onBoardUpdated(board);
    return true;
  }

  private supportsFeature(feature: string): boolean {
    return this.capabilities?.features?.includes(feature) === true;
  }

  private taskRevision(taskID: string): number {
    const revision = this.currentBoard?.tasks.find((task) => task.id === taskID)?.revision;
    if (typeof revision !== 'number' || !Number.isSafeInteger(revision) || revision < 0) {
      throw new Error('看板状态已过期，请先刷新后再操作');
    }
    return revision;
  }

  private actionID(): string {
    return crypto.randomUUID();
  }

  /**
   * 前台应用层心跳：手机切换网络或 NAT 超时后 WebSocket 可能半开，浏览器迟迟不触发 close。
   * 用最轻量的 capabilities.get 探测；只有超时或发送失败才判定连接已死并主动重连，
   * 服务端返回 RPC 错误说明链路仍然通畅。
   */
  private startHeartbeat(socket: WebSocket): void {
    this.stopHeartbeat();
    this.heartbeatTimer = setInterval(() => {
      if (this.isDisposed || this.ws !== socket || !this.isReady) return;
      if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return;
      this.sendRpc('capabilities.get', {}, HEARTBEAT_TIMEOUT_MS, socket).catch((err: unknown) => {
        if (err instanceof RpcError) return;
        if (this.isDisposed || this.ws !== socket || this.isRevokingDevice) return;
        this.handleHeartbeatFailure();
      });
    }, HEARTBEAT_INTERVAL_MS);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer !== null) {
      clearInterval(this.heartbeatTimer);
      this.heartbeatTimer = null;
    }
  }

  private clearConnectTimer(): void {
    if (this.connectTimer !== null) {
      clearTimeout(this.connectTimer);
      this.connectTimer = null;
    }
  }

  private clearStableTimer(): void {
    if (this.stableTimer !== null) {
      clearTimeout(this.stableTimer);
      this.stableTimer = null;
    }
  }

  private handleHeartbeatFailure(): void {
    this.stopHeartbeat();
    this.clearConnectTimer();
    this.clearStableTimer();
    this.isAuthenticated = false;
    this.authenticatedGeneration = -1;
    this.capabilities = null;
    this.currentBoard = null;
    // 判死后第一次重试要快，这里保留清零
    this.reconnectAttempts = 0;
    this.options.onConnectionChange('reconnecting', '与 Mac 的连接无响应，正在重新连接...');

    // 主动关闭判死的旧连接，避免它的迟到消息继续进入 handleMessage
    const socket = this.ws;
    if (socket) {
      try {
        socket.onopen = null;
        socket.onclose = null;
        socket.onerror = null;
        socket.onmessage = null;
        socket.close();
      } catch {
        // 忽略
      }
      this.ws = null;
      this.cleanupPendingRequests('与 Mac 的连接无响应，请求已终止；状态待确认，请刷新看板确认最新状态');
    }

    this.scheduleReconnect();
  }

  private handleError = (): void => {
    // 触发 onerror 后通常紧接着触发 onclose，由 onclose 统一调度重连
  };

  /**
   * 拒绝所有挂起的 RPC 请求并清理计时器
   */
  private cleanupPendingRequests(reason: string): void {
    const error = new Error(reason);
    for (const [, req] of this.pendingRequests.entries()) {
      clearTimeout(req.timer);
      req.reject(error);
    }
    this.pendingRequests.clear();
  }

  /**
   * 指数退避调度重连（有上限）
   */
  private scheduleReconnect(): void {
    if (this.isDisposed || this.isAuthFailed || this.isRevokingDevice) return;

    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
    }

    // 离线期间不设定时器、不累加次数；网络恢复由 remoteConnection 的 online 事件驱动重连
    if (typeof navigator !== 'undefined' && navigator.onLine === false) {
      this.reconnectTimer = null;
      this.options.onConnectionChange('reconnecting', '网络已断开，恢复网络后将自动重连');
      return;
    }

    // 指数退避：基准 1000ms，上限 15000ms，加入轻微随机抖动
    const base = 1000;
    const factor = Math.min(Math.pow(1.5, this.reconnectAttempts), 15);
    const delay = Math.round(base * factor + Math.random() * 400);

    this.reconnectAttempts++;
    this.options.onConnectionChange(
      'reconnecting',
      `连接中断，将在 ${Math.round(delay / 100) / 10} 秒后重新连接 (第 ${this.reconnectAttempts} 次)...`
    );

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }

  /**
   * 基础 JSON-RPC 2.0 发送方法
   */
  private sendRpc<T = unknown>(
    method: string,
    params?: Record<string, unknown>,
    timeoutMs = 15000,
    socket: WebSocket | null = this.ws
  ): Promise<T> {
    return new Promise<T>((resolve, reject) => {
      if (this.ws !== socket || !socket || socket.readyState !== WebSocket.OPEN) {
        reject(new Error('当前未与 Mac 建立连接，无法执行此操作'));
        return;
      }

      const id = this.nextId++;
      const timer = setTimeout(() => {
        if (this.pendingRequests.has(id)) {
          this.pendingRequests.delete(id);
          reject(new Error(`操作超时（${timeoutMs / 1000} 秒），状态待确认：请刷新看板确认最新状态，请勿重复提交`));
        }
      }, timeoutMs);

      this.pendingRequests.set(id, {
        resolve: resolve as (data: unknown) => void,
        reject,
        timer,
        method
      });

      const payload: JsonRpcRequest = {
        jsonrpc: '2.0',
        id,
        method,
        params: params || {}
      };

      try {
        socket.send(JSON.stringify(payload));
      } catch (err) {
        clearTimeout(timer);
        this.pendingRequests.delete(id);
        reject(err instanceof Error ? err : new Error('发送失败'));
      }
    });
  }

  /**
   * 通用 RPC 调用：按方法名直接转发，params 缺省时传空对象。
   * 后续新增方法无需再往客户端里加包装。
   */
  public call<T = unknown>(method: string, params?: unknown, timeoutMs = 30000): Promise<T> {
    const payload: Record<string, unknown> =
      params && typeof params === 'object' ? (params as Record<string, unknown>) : {};
    return this.sendRpc<T>(method, payload, timeoutMs);
  }

  /**
   * 订阅任意方法的服务端通知，返回取消订阅函数。
   * 单个 handler 抛异常不会影响其它 handler。
   */
  public onNotification(method: string, handler: (params: unknown) => void): () => void {
    const entry = { method, handler };
    this.notificationListeners.add(entry);
    return () => {
      this.notificationListeners.delete(entry);
    };
  }

  /** 把一条通知分发给方法名匹配的监听器；单个监听器异常被吞掉。 */
  private dispatchNotification(method: string, params: unknown): void {
    if (this.notificationListeners.size === 0) return;
    for (const entry of this.notificationListeners) {
      if (entry.method !== method) continue;
      try {
        entry.handler(params);
      } catch {
        // 忽略单个监听器内部异常
      }
    }
  }

  // --- 契约规定的业务方法 ---

  public async revokeDevice(deviceID: string): Promise<void> {
    if (!this.isReady || this.isRevokingDevice) throw new Error('当前未与 Mac 建立可用连接');
    this.isRevokingDevice = true;
    try {
      const result = await this.sendRpc<SessionRevokeDeviceResult>('session.revokeDevice', { deviceID });
      if (
        !result || typeof result.deviceID !== 'string' ||
        result.deviceID.toLowerCase() !== deviceID.toLowerCase() || result.revoked !== true
      ) {
        throw new Error('Mac 端未确认此设备的配对已撤销');
      }
    } finally {
      this.isRevokingDevice = false;
    }
  }

  /**
   * session.renameDevice { name } -> 修改当前设备在 Mac 上显示的设备名称
   */
  public async renameDevice(name: string): Promise<void> {
    if (!this.isReady) throw new Error('当前未与 Mac 建立可用连接');
    const label = name.trim();
    if (!label) throw new Error('设备名称不能为空');
    if (Array.from(label).length > 64) throw new Error('设备名称不能超过 64 个字符');
    const result = await this.sendRpc<SessionRenameDeviceResult>('session.renameDevice', { name: label });
    const identity = deviceIdentityFrom(result);
    const current = this.credentials;
    if (!current) throw new Error('缺少本地设备凭据，请重新连接');
    if (
      current.deviceIdentity &&
      current.deviceIdentity.deviceID.toLowerCase() !== identity.deviceID.toLowerCase()
    ) {
      throw new Error('Mac 端返回的设备身份不一致，请重新连接');
    }
    // 保存后通过 onCredentialsChanged 通知界面刷新 deviceIdentity。
    this.saveCredentials({ ...current, deviceIdentity: identity });
  }

  /**
   * session.newPairingCode {} -> a one-time pairing link for another app instance
   */
  public async newPairingCode(): Promise<SessionNewPairingCodeResult> {
    if (!this.isReady || this.isRevokingDevice) throw new Error('当前未与 Mac 建立可用连接');
    const result = await this.sendRpc<SessionNewPairingCodeResult>('session.newPairingCode', {});
    if (
      !result ||
      result.requested !== true ||
      typeof result.pairingURL !== 'string' ||
      typeof result.expiresAt !== 'string'
    ) {
      throw new Error('Mac 端未返回有效的主屏幕配对码');
    }
    return result;
  }

  /**
   * kanban.getBoard {}
   */
  public async getBoard(timeoutMs = 15000): Promise<Board> {
    // 合并并发请求：不缓存结果，请求结束（成功或失败）后再次调用仍会重新发起。
    if (this.boardInFlight) return this.boardInFlight;
    const request = this.sendRpc<Board>('kanban.getBoard', {}, timeoutMs);
    this.boardInFlight = request;
    try {
      return await request;
    } finally {
      // 只清理自己登记的那次请求，写操作后新发起的请求不能被上一次的收尾误清。
      if (this.boardInFlight === request) this.boardInFlight = null;
    }
  }

  /**
   * kanban.createTask { projectID, title, notes, actionID } -> mutation result
   */
  public async createTask(params: KanbanCreateTaskParams): Promise<KanbanTaskMutationResult> {
    const res = await this.sendRpc<KanbanTaskMutationResult>('kanban.createTask', params, 30000);
    this.syncBoardInBackground();
    return res;
  }

  /**
   * kanban.moveTask { taskID, status } -> { taskID }
   */
  public async moveTask(params: MoveTaskParams): Promise<KanbanTaskMutationResult> {
    const rpcParams: Record<string, unknown> = {
      taskID: params.taskID,
      status: params.status
    };
    if (this.supportsFeature('kanban.linking')) {
      rpcParams.expectedRevision = params.expectedRevision ?? this.taskRevision(params.taskID);
      rpcParams.actionID = params.actionID ?? this.actionID();
    }
    const res = await this.sendRpc<KanbanTaskMutationResult>('kanban.moveTask', rpcParams);
    // 变更后拉取最新 Board
    this.syncBoardInBackground();
    return res;
  }

  /**
   * kanban.startTask { taskID } -> { taskID }
   */
  public async startTask(params: StartTaskParams): Promise<KanbanTaskMutationResult> {
    const rpcParams: Record<string, unknown> = {
      taskID: params.taskID
    };
    if (this.supportsFeature('kanban.linking')) {
      rpcParams.expectedRevision = params.expectedRevision ?? this.taskRevision(params.taskID);
      rpcParams.actionID = params.actionID ?? this.actionID();
    }
    const res = await this.sendRpc<KanbanTaskMutationResult>('kanban.startTask', rpcParams, 60000);
    this.syncBoardInBackground();
    return res;
  }

  /**
   * kanban.reviewTask { taskID, decision, feedback? } -> { taskID }
   */
  public async reviewTask(params: ReviewTaskParams): Promise<KanbanTaskMutationResult> {
    const rpcParams: Record<string, unknown> = {
      taskID: params.taskID,
      decision: params.decision
    };
    if (params.feedback !== undefined && params.feedback.trim().length > 0) {
      rpcParams.feedback = params.feedback.trim();
    }
    if (this.supportsFeature('kanban.linking')) {
      rpcParams.expectedRevision = params.expectedRevision ?? this.taskRevision(params.taskID);
      rpcParams.actionID = params.actionID ?? this.actionID();
    }
    const res = await this.sendRpc<KanbanTaskMutationResult>('kanban.reviewTask', rpcParams, 60000);
    this.syncBoardInBackground();
    return res;
  }

  public async createGoal(params: GoalCreateParams): Promise<KanbanTaskMutationResult> {
    const res = await this.sendRpc<KanbanTaskMutationResult>('goals.create', params, 30000);
    this.syncBoardInBackground();
    return res;
  }

  public async mutateGoal(
    method: 'goals.start' | 'goals.pause' | 'goals.cancel',
    params: GoalMutationParams
  ): Promise<KanbanTaskMutationResult> {
    const res = await this.sendRpc<KanbanTaskMutationResult>(method, params, 60000);
    this.syncBoardInBackground();
    return res;
  }

  public async replanGoal(params: GoalReplanParams): Promise<KanbanTaskMutationResult> {
    const res = await this.sendRpc<KanbanTaskMutationResult>('goals.replan', params, 60000);
    this.syncBoardInBackground();
    return res;
  }

  public async mutateGoalTask(
    method: 'goals.retryTask' | 'goals.skipTask',
    params: GoalTaskMutationParams
  ): Promise<KanbanTaskMutationResult> {
    const res = await this.sendRpc<KanbanTaskMutationResult>(method, params, 60000);
    this.syncBoardInBackground();
    return res;
  }

  /**
   * kanban.getDiff { taskID } -> { taskID, diff, truncated }
   */
  public async getDiff(params: GetDiffParams): Promise<GetDiffResult> {
    return this.sendRpc<GetDiffResult>('kanban.getDiff', {
      taskID: params.taskID
    });
  }

  /**
   * threads.getDiff { threadID, turnID? } -> { threadID, turnID?, diff, truncated, filesChanged }
   */
  public async getThreadDiff(
    params: ThreadsGetDiffParams
  ): Promise<ThreadsGetDiffResult> {
    const rpcParams: Record<string, unknown> = {
      threadID: params.threadID
    };
    if (params.turnID) {
      rpcParams.turnID = params.turnID;
    }
    return this.sendRpc<ThreadsGetDiffResult>('threads.getDiff', rpcParams);
  }

  /**
   * git.status { threadID } -> RemoteGitStatusResult
   */
  public async getThreadGitStatus(threadID: string): Promise<RemoteGitStatusResult> {
    return this.sendRpc<RemoteGitStatusResult>('git.status', { threadID }, 30000);
  }

  /**
   * git.branches { threadID } -> RemoteGitBranchesResult
   */
  public async getThreadGitBranches(threadID: string): Promise<RemoteGitBranchesResult> {
    return this.sendRpc<RemoteGitBranchesResult>('git.branches', { threadID }, 30000);
  }

  /**
   * scripts.list { threadID } -> RemoteScriptsListResult
   */
  public async listThreadScripts(threadID: string): Promise<RemoteScriptsListResult> {
    return this.sendRpc<RemoteScriptsListResult>('scripts.list', { threadID }, 30000);
  }

  /**
   * scripts.run { threadID, kind, name?, scriptID? } -> { ok: true, runID }
   */
  public async runThreadScript(params: RemoteScriptsRunParams): Promise<RemoteScriptsRunResult> {
    return this.sendRpc<RemoteScriptsRunResult>('scripts.run', { ...params }, 30000);
  }

  /**
   * scripts.runStatus { threadID, runID } -> RemoteScriptsRunStatus
   */
  public async getScriptRunStatus(params: RemoteScriptsRunStatusParams): Promise<RemoteScriptsRunStatus> {
    return this.sendRpc<RemoteScriptsRunStatus>('scripts.runStatus', { ...params }, 30000);
  }

  /**
   * scripts.setFavorite { threadID, name, isFavorite } -> { favorites }
   */
  public async setThreadScriptFavorite(params: RemoteScriptsFavoriteParams): Promise<RemoteScriptsFavoriteResult> {
    return this.sendRpc<RemoteScriptsFavoriteResult>('scripts.setFavorite', { ...params }, 30000);
  }

  /**
   * files.list { threadID, subpath? } -> RemoteFilesListResult
   */
  public async listThreadFiles(
    threadID: string,
    subpath?: string
  ): Promise<RemoteFilesListResult> {
    const rpcParams: Record<string, unknown> = { threadID };
    if (subpath !== undefined && subpath !== null) {
      rpcParams.subpath = subpath;
    }
    return this.sendRpc<RemoteFilesListResult>('files.list', rpcParams, 30000);
  }

  /**
   * files.read { threadID, path, offset?, length? } -> RemoteFilesReadResult
   */
  public async readThreadFile(
    threadID: string,
    path: string,
    offset?: number,
    length?: number
  ): Promise<RemoteFilesReadResult> {
    const rpcParams: Record<string, unknown> = { threadID, path };
    if (offset !== undefined && offset !== null) {
      rpcParams.offset = offset;
    }
    if (length !== undefined && length !== null) {
      rpcParams.length = length;
    }
    return this.sendRpc<RemoteFilesReadResult>('files.read', rpcParams, 30000);
  }

  /**
   * files.search { threadID, query } -> RemoteFilesSearchResult
   */
  public async searchThreadFiles(
    threadID: string,
    query: string
  ): Promise<RemoteFilesSearchResult> {
    return this.sendRpc<RemoteFilesSearchResult>('files.search', { threadID, query }, 30000);
  }

  /**
   * threads.media.read { threadID, messageID, mediaID, variant, offset?, length? } -> RemoteThreadsMediaReadResult
   * 错误保留 RpcError 的 code 与 data.reason（notFound / unavailable / tooLarge）。
   */
  public async readThreadMedia(
    params: RemoteThreadsMediaReadParams
  ): Promise<RemoteThreadsMediaReadResult> {
    const rpcParams: Record<string, unknown> = {
      threadID: params.threadID,
      messageID: params.messageID,
      mediaID: params.mediaID,
      variant: params.variant
    };
    if (params.offset !== undefined && params.offset !== null) {
      rpcParams.offset = params.offset;
    }
    if (params.length !== undefined && params.length !== null) {
      rpcParams.length = params.length;
    }
    return this.sendRpc<RemoteThreadsMediaReadResult>('threads.media.read', rpcParams, 30000);
  }

  /**
   * projects.list {} -> { projects, revision }
   */
  public async listProjects(): Promise<{ projects: RemoteProjectSummary[]; revision: number }> {
    return this.sendRpc<{ projects: RemoteProjectSummary[]; revision: number }>('projects.list', {});
  }

  /**
   * threads.list { projectID?, includeArchived?, cursor?, limit? } -> { threads, revision, nextCursor? }
   */
  public async listThreads(
    params: ThreadsListParams = {}
  ): Promise<ThreadsListResult> {
    const rpcParams: Record<string, unknown> = {};
    if (params.projectID) rpcParams.projectID = params.projectID;
    if (params.includeArchived !== undefined) rpcParams.includeArchived = params.includeArchived;
    if (params.cursor) rpcParams.cursor = params.cursor;
    if (params.limit !== undefined) rpcParams.limit = params.limit;
    return this.sendRpc<ThreadsListResult>('threads.list', rpcParams);
  }

  public async updateThread(params: ThreadsUpdateParams): Promise<ThreadsUpdateResult> {
    return this.sendRpc<ThreadsUpdateResult>('threads.update', params, 30000);
  }

  public async moveThread(params: ThreadsMoveParams): Promise<ThreadsMoveResult> {
    return this.sendRpc<ThreadsMoveResult>('threads.move', params, 30000);
  }

  public async getThread(threadID: string): Promise<{ thread: RemoteThreadSummary }> {
    return this.sendRpc<{ thread: RemoteThreadSummary }>('threads.get', { threadID }, 30000);
  }

  /**
   * threads.history { threadID, cursor?, limit? } -> RemoteThreadHistoryPage
   */
  public async getThreadHistory(
    threadID: string,
    cursor?: string | null,
    limit?: number
  ): Promise<RemoteThreadHistoryPage> {
    const rpcParams: Record<string, unknown> = { threadID };
    if (cursor) rpcParams.cursor = cursor;
    if (limit !== undefined) rpcParams.limit = limit;
    return this.sendRpc<RemoteThreadHistoryPage>('threads.history', rpcParams, 30000);
  }

  /**
   * threads.subscribe { threadID } -> { threadID, subscribed, snapshot }
   */
  public async subscribeThread(params: ThreadsSubscribeParams): Promise<ThreadsSubscribeResult> {
    return this.sendRpc<ThreadsSubscribeResult>('threads.subscribe', {
      threadID: params.threadID
    }, 30000);
  }

  /**
   * threads.unsubscribe { threadID } -> { threadID, subscribed }
   */
  public async unsubscribeThread(params: ThreadsUnsubscribeParams): Promise<ThreadsUnsubscribeResult> {
    return this.sendRpc<ThreadsUnsubscribeResult>('threads.unsubscribe', {
      threadID: params.threadID
    });
  }

  /**
   * threads.create { projectID, actionID, actionJournalID? } -> RemoteThreadsCreateResult
   */
  public async createThread(
    params: RemoteThreadsCreateParams & { actionJournalID?: string }
  ): Promise<RemoteThreadsCreateResult> {
    const rpcParams: Record<string, unknown> = {
      projectID: params.projectID,
      actionID: params.actionID
    };
    if (params.options !== undefined) rpcParams.options = params.options;
    if (params.actionJournalID !== undefined) rpcParams.actionJournalID = params.actionJournalID;
    return this.sendRpc<RemoteThreadsCreateResult>('threads.create', rpcParams, 60000);
  }

  /** providers.list { projectID? } -> provider catalog + swarm info (+ draft defaults when projectID given). */
  public async listProviders(projectID?: string): Promise<RemoteProvidersListResultWithCatalog> {
    return this.sendRpc<RemoteProvidersListResultWithCatalog>(
      'providers.list',
      projectID ? { projectID } : {},
      30000
    );
  }

  /**
   * providers.rememberSelection { projectID?, provider?, model?, effort?, fastMode?, swarmEnabled?,
   * swarmBeesProvider?, swarmBeesModel?, swarmBeesEffort? } -> RemoteProvidersListResult
   * 只在用户主动改动输入区选择时调用；返回的 draft 是刷新后的默认值（与 providers.list 一致）。
   */
  public async rememberProviderSelection(
    params: RemoteProvidersRememberSelectionParams
  ): Promise<RemoteProvidersListResult> {
    const rpcParams: Record<string, unknown> = {};
    if (params.projectID) rpcParams.projectID = params.projectID;
    if (params.provider !== undefined) rpcParams.provider = params.provider;
    if (params.model !== undefined) rpcParams.model = params.model;
    if (params.effort !== undefined) rpcParams.effort = params.effort;
    if (params.fastMode !== undefined) rpcParams.fastMode = params.fastMode;
    if (params.swarmEnabled !== undefined) rpcParams.swarmEnabled = params.swarmEnabled;
    if (params.swarmBeesProvider !== undefined) rpcParams.swarmBeesProvider = params.swarmBeesProvider;
    if (params.swarmBeesModel !== undefined) rpcParams.swarmBeesModel = params.swarmBeesModel;
    if (params.swarmBeesEffort !== undefined) rpcParams.swarmBeesEffort = params.swarmBeesEffort;
    return this.sendRpc<RemoteProvidersListResult>('providers.rememberSelection', rpcParams, 30000);
  }

  /** composer.quota { threadID } | { projectID } -> { budgets }（输入框额度条）。 */
  public async getComposerQuota(scope: ComposerQuotaScope): Promise<RemoteComposerQuotaResult> {
    return this.sendRpc<RemoteComposerQuotaResult>(
      'composer.quota',
      scope.threadID ? { threadID: scope.threadID } : { projectID: scope.projectID },
      30000
    );
  }

  /** push.publicKey {} -> { publicKey } (base64url VAPID public key) */
  public async getPushPublicKey(): Promise<PushPublicKeyResult> {
    return this.sendRpc<PushPublicKeyResult>('push.publicKey', {}, 30000);
  }

  /** push.subscribe { endpoint, p256dh, auth } -> { subscribed: true } */
  public async subscribePush(params: PushSubscribeParams): Promise<PushSubscribeResult> {
    return this.sendRpc<PushSubscribeResult>(
      'push.subscribe',
      { endpoint: params.endpoint, p256dh: params.p256dh, auth: params.auth },
      30000
    );
  }

  /** push.unsubscribe { endpoint } -> { unsubscribed: true } */
  public async unsubscribePush(endpoint: string): Promise<PushUnsubscribeResult> {
    return this.sendRpc<PushUnsubscribeResult>('push.unsubscribe', { endpoint }, 30000);
  }

  /**
   * threads.branch { threadID, messageID, actionID } -> { threadID }
   */
  public async branchThread(params: ThreadsBranchParams): Promise<ThreadsBranchResult> {
    return this.sendRpc<ThreadsBranchResult>('threads.branch', { ...params }, 60000);
  }

  /**
   * threads.continue { threadID, actionID } -> { threadID }（新会话 ID）
   */
  public async forwardThread(params: RemoteThreadsContinueParams): Promise<RemoteThreadsContinueResult> {
    return this.sendRpc<RemoteThreadsContinueResult>('threads.continue', { ...params }, 60000);
  }

  /**
   * threads.commands { threadID } | { projectID, provider } -> 斜杠命令清单（只读）
   */
  public async listThreadCommands(
    params: RemoteThreadsCommandsParams
  ): Promise<RemoteThreadsCommandsResult> {
    return this.sendRpc<RemoteThreadsCommandsResult>('threads.commands', { ...params }, 30000);
  }

  /**
   * threads.sendMessage { threadID, text, actionID, actionJournalID? } -> RemoteThreadsSendMessageResult
   */
  public async sendThreadMessage(
    params: RemoteThreadsSendMessageParams & { actionJournalID?: string }
  ): Promise<RemoteThreadsSendMessageResult> {
    const rpcParams: Record<string, unknown> = {
      threadID: params.threadID,
      actionID: params.actionID
    };
    if (params.text !== undefined) rpcParams.text = params.text;
    if (params.whenRunning !== undefined) rpcParams.whenRunning = params.whenRunning;
    if (params.options !== undefined) rpcParams.options = params.options;
    if (params.attachments !== undefined) rpcParams.attachments = params.attachments;
    if (params.actionJournalID !== undefined) rpcParams.actionJournalID = params.actionJournalID;
    return this.sendRpc<RemoteThreadsSendMessageResult>('threads.sendMessage', rpcParams, 60000);
  }

  public async listThreadQueue(threadID: string): Promise<RemoteThreadsQueueListResult> {
    return this.sendRpc<RemoteThreadsQueueListResult>('threads.queue.list', { threadID }, 30000);
  }

  public async updateThreadQueue(params: RemoteThreadsQueueMutationParams): Promise<RemoteThreadsQueueMutationResult> {
    return this.sendRpc<RemoteThreadsQueueMutationResult>('threads.queue.update', params, 30000);
  }

  public async removeThreadQueue(params: RemoteThreadsQueueMutationParams): Promise<RemoteThreadsQueueMutationResult> {
    return this.sendRpc<RemoteThreadsQueueMutationResult>('threads.queue.remove', params, 30000);
  }

  /**
   * threads.queue.move { threadID, followUpID, beforeFollowUpID?, actionID } -> RemoteThreadsQueueMutationResult
   */
  public async moveThreadQueue(params: RemoteThreadsQueueMoveParams): Promise<RemoteThreadsQueueMutationResult> {
    const rpcParams: Record<string, unknown> = {
      threadID: params.threadID,
      followUpID: params.followUpID,
      actionID: params.actionID
    };
    if (params.beforeFollowUpID !== undefined) rpcParams.beforeFollowUpID = params.beforeFollowUpID;
    return this.sendRpc<RemoteThreadsQueueMutationResult>('threads.queue.move', rpcParams, 30000);
  }

  public async sendThreadQueueNow(params: RemoteThreadsQueueMutationParams): Promise<RemoteThreadsQueueMutationResult> {
    return this.sendRpc<RemoteThreadsQueueMutationResult>('threads.queue.sendNow', params, 60000);
  }

  public async interruptAndSendThread(
    params: RemoteThreadsQueueInterruptAndSendParams
  ): Promise<RemoteThreadsQueueInterruptAndSendResult> {
    return this.sendRpc<RemoteThreadsQueueInterruptAndSendResult>('threads.queue.interruptAndSend', params, 60000);
  }

  /**
   * threads.queue.edit { threadID, action, followUpID?, draft? } -> RuntimeQueueEditResult
   */
  public async editThreadQueueDraft(params: RuntimeQueueEditParams): Promise<RuntimeQueueEditResult> {
    return this.sendRpc<RuntimeQueueEditResult>('threads.queue.edit', { ...params }, 30000);
  }

  /**
   * threads.bulk.preview { projectID, before?, includePinned, includeDormant } -> { threadIDs, skippedRunning }
   */
  public async previewBulkThreads(params: RemoteThreadsBulkPreviewParams): Promise<RemoteThreadsBulkPreviewResult> {
    return this.sendRpc<RemoteThreadsBulkPreviewResult>('threads.bulk.preview', { ...params }, 30000);
  }

  /**
   * threads.bulk.archive { projectID, threadIDs } -> { undoToken, skippedRunning, threadIDs }
   */
  public async archiveBulkThreads(params: RemoteThreadsBulkArchiveParams): Promise<RemoteThreadsBulkArchiveResult> {
    return this.sendRpc<RemoteThreadsBulkArchiveResult>('threads.bulk.archive', { ...params }, 60000);
  }

  /**
   * threads.bulk.markDormant { projectID, threadIDs } -> { undoToken, skippedRunning, threadIDs }
   */
  public async markDormantBulkThreads(params: RemoteThreadsBulkMarkDormantParams): Promise<RemoteThreadsBulkMarkDormantResult> {
    return this.sendRpc<RemoteThreadsBulkMarkDormantResult>('threads.bulk.markDormant', { ...params }, 60000);
  }

  /**
   * threads.bulk.markRead { projectID, threadIDs } -> { undoToken, threadIDs }
   */
  public async markBulkThreadsRead(params: RemoteThreadsBulkMarkReadParams): Promise<RemoteThreadsBulkMarkReadResult> {
    return this.sendRpc<RemoteThreadsBulkMarkReadResult>('threads.bulk.markRead', { ...params }, 30000);
  }

  /**
   * threads.bulk.undo { projectID, token } -> { ok }
   */
  public async undoBulkThreads(params: RemoteThreadsBulkUndoParams): Promise<RemoteThreadsBulkUndoResult> {
    return this.sendRpc<RemoteThreadsBulkUndoResult>('threads.bulk.undo', { ...params }, 30000);
  }

  /**
   * threads.title.regenerate { threadId } -> { title }
   */
  public async regenerateThreadTitle(threadID: string): Promise<RemoteThreadsTitleRegenerateResult> {
    const params: RemoteThreadsTitleRegenerateParams = { threadId: threadID };
    return this.sendRpc<RemoteThreadsTitleRegenerateResult>('threads.title.regenerate', params, 30000);
  }

  /**
   * threads.turn.recall { threadID } -> { threadID, turnID, prompt, attachments }
   */
  public async recallTurn(threadID: string): Promise<RemoteThreadsTurnRecallResult> {
    const params: RemoteThreadsTurnRecallParams = { threadID };
    return this.sendRpc<RemoteThreadsTurnRecallResult>('threads.turn.recall', params, 30000);
  }

  /**
   * threads.retain { threadId } -> {}
   */
  public async retainThread(threadID: string): Promise<RemoteThreadsRetainResult> {
    const params: RemoteThreadsRetainParams = { threadId: threadID };
    return this.sendRpc<RemoteThreadsRetainResult>('threads.retain', params, 30000);
  }

  /**
   * threads.provisional.release { threadId } -> {}
   */
  public async releaseProvisionalThread(threadID: string): Promise<RemoteThreadsProvisionalReleaseResult> {
    const params: RemoteThreadsProvisionalReleaseParams = { threadId: threadID };
    return this.sendRpc<RemoteThreadsProvisionalReleaseResult>('threads.provisional.release', params, 30000);
  }

  /**
   * threads.provider.limitText { threadID } -> { threadID, text }
   */
  public async limitProviderText(threadID: string): Promise<RuntimeLimitTextResult> {
    const params: RemoteThreadsTurnRecallParams = { threadID };
    return this.sendRpc<RuntimeLimitTextResult>('threads.provider.limitText', params, 30000);
  }

  /**
   * threads.worktree.preview { threadID } -> RuntimeWorktreePreviewResult
   */
  public async previewWorktreeMove(params: RuntimeWorktreePreviewParams): Promise<RuntimeWorktreePreviewResult> {
    return this.sendRpc<RuntimeWorktreePreviewResult>('threads.worktree.preview', { ...params }, 30000);
  }

  /**
   * threads.worktree.execute { threadID, branch?, transferFiles } -> RuntimeWorktreeExecuteResult
   */
  public async executeWorktreeMove(params: RuntimeWorktreeExecuteParams): Promise<RuntimeWorktreeExecuteResult> {
    return this.sendRpc<RuntimeWorktreeExecuteResult>('threads.worktree.execute', { ...params }, 60000);
  }

  /**
   * threads.compact { threadID } -> RemoteThreadsSendMessageResult
   */
  public async compactThread(threadID: string): Promise<RemoteThreadsSendMessageResult> {
    const params: RemoteThreadsTurnRecallParams = { threadID };
    return this.sendRpc<RemoteThreadsSendMessageResult>('threads.compact', params, 60000);
  }

  public async uploadAttachment(params: RemoteAttachmentUploadParams): Promise<RemoteAttachmentUploadResult> {
    return this.sendRpc<RemoteAttachmentUploadResult>('attachments.upload', params, 60000);
  }

  /**
   * threads.actionStatus { actionID } -> RemoteThreadsActionStatusResult
   */
  public async getThreadActionStatus(actionID: string): Promise<RemoteThreadsActionStatusResult> {
    const params: RemoteThreadsActionStatusParams = { actionID };
    return this.sendRpc<RemoteThreadsActionStatusResult>('threads.actionStatus', {
      actionID: params.actionID
    });
  }

  /**
   * threads.settleAction { actionID, actionJournalID, projectID?, threadID? } -> RemoteThreadsSettleActionResult
   */
  public async settleThreadAction(
    params: RemoteThreadsSettleActionParams
  ): Promise<RemoteThreadsSettleActionResult> {
    const rpcParams: Record<string, unknown> = {
      actionID: params.actionID,
      actionJournalID: params.actionJournalID
    };
    if (params.projectID !== undefined) rpcParams.projectID = params.projectID;
    if (params.threadID !== undefined) rpcParams.threadID = params.threadID;
    return this.sendRpc<RemoteThreadsSettleActionResult>('threads.settleAction', rpcParams, 30000);
  }

  /**
   * requests.list { threadID } -> the current pending request snapshot.
   */
  public async listRequests(params: RemoteRequestsListParams): Promise<RemoteRequestsListResult> {
    return this.sendRpc<RemoteRequestsListResult>('requests.list', {
      threadID: params.threadID
    }, 30000);
  }

  /**
   * requests.respond { threadID, requestID, turnID, runID, optionID|answers }.
   */
  public async respondToRequest(params: RemoteRequestsRespondParams): Promise<RemoteRequestsRespondResult> {
    return this.sendRpc<RemoteRequestsRespondResult>('requests.respond', params, 30000);
  }

  /**
   * threads.interrupt { threadID, turnID }.
   */
  public async interruptThread(params: RemoteThreadsInterruptParams): Promise<RemoteThreadsInterruptResult> {
    return this.sendRpc<RemoteThreadsInterruptResult>('threads.interrupt', params, 30000);
  }

  /**
   * threads.killRunningCommand { threadID, messageID? }.
   */
  public async killRunningCommand(
    params: RemoteThreadsKillRunningCommandParams
  ): Promise<RemoteThreadsKillRunningCommandResult> {
    return this.sendRpc<RemoteThreadsKillRunningCommandResult>('threads.killRunningCommand', params, 30000);
  }

  /**
   * 注册单个连接实例的编排状态变更监听器
   */
  public addSwarmListener(listener: (state: RemoteSwarmState) => void): () => void {
    this.swarmListeners.add(listener);
    return () => {
      this.swarmListeners.delete(listener);
    };
  }

  /**
   * 注册单个连接实例的压缩建议（threads.compactSuggestion）监听器
   */
  public addCompactSuggestionListener(listener: (offer: RuntimeCompactSuggestionParams) => void): () => void {
    this.compactSuggestionListeners.add(listener);
    return () => {
      this.compactSuggestionListeners.delete(listener);
    };
  }

  /**
   * threads.swarm.get { threadID } -> RemoteSwarmState
   */
  public async getThreadSwarm(threadID: string): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.get', { threadID }, 30000);
  }

  /**
   * threads.swarm.set { threadID, enabled, actionID } -> RemoteSwarmState
   */
  public async setThreadSwarm(params: {
    threadID: string;
    enabled: boolean;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.set', params, 30000);
  }

  /**
   * threads.swarm.stopBee { threadID, beeThreadID, actionID } -> RemoteSwarmState
   */
  public async stopSwarmBee(params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.stopBee', params, 30000);
  }

  /**
   * threads.swarm.stopAll { threadID, actionID } -> RemoteSwarmState
   * native 子代理异步停止，返回态里 status 可能仍是 running，以 threads.swarm.changed 为准。
   */
  public async stopAllSwarmBees(params: { threadID: string; actionID: string }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.stopAll', params, 30000);
  }

  /**
   * threads.swarm.clearFinished { threadID, actionID } -> RemoteSwarmState
   */
  public async clearFinishedSwarmBees(params: { threadID: string; actionID: string }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.clearFinished', params, 30000);
  }

  /**
   * threads.swarm.setPlansOnly { threadID, plansOnly, actionID } -> RemoteSwarmState
   * plansOnly 传 null 表示清除会话级覆盖，回到全局默认。
   */
  public async setThreadSwarmPlansOnly(params: {
    threadID: string;
    plansOnly: boolean | null;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.setPlansOnly', params, 30000);
  }

  /**
   * threads.swarm.setMode { threadID, mode, actionID } -> RemoteThreadSummary
   */
  public async setThreadSwarmMode(params: {
    threadID: string;
    mode: 'inherited' | 'collaborative' | 'leadPlansOnly' | 'alwaysBees';
    actionID: string;
  }): Promise<RemoteThreadSummary> {
    return this.sendRpc<RemoteThreadSummary>('threads.swarm.setMode', params, 30000);
  }

  /**
   * threads.swarm.forwardQuestion { threadID, beeThreadID, requestID, actionID } -> RemoteSwarmState
   */
  public async forwardSwarmQuestion(params: {
    threadID: string;
    beeThreadID: string;
    requestID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.forwardQuestion', params, 30000);
  }

  /**
   * threads.swarm.mergeDiff { threadID, beeThreadID } -> { diff, truncated, filesChanged }
   */
  public async getSwarmMergeDiff(params: {
    threadID: string;
    beeThreadID: string;
  }): Promise<RemoteSwarmMergeDiffResult> {
    return this.sendRpc<RemoteSwarmMergeDiffResult>('threads.swarm.mergeDiff', params, 30000);
  }

  /**
   * threads.swarm.approveMerge { threadID, beeThreadID, actionID } -> RemoteSwarmState
   */
  public async approveSwarmMerge(params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.approveMerge', params, 30000);
  }

  /**
   * threads.swarm.discardMerge { threadID, beeThreadID, actionID } -> RemoteSwarmState
   */
  public async discardSwarmMerge(params: {
    threadID: string;
    beeThreadID: string;
    actionID: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.discardMerge', params, 30000);
  }

  /**
   * threads.swarm.setBeeTiers { threadID, beeTiers, actionID? } -> RemoteSwarmState
   */
  public async setThreadSwarmBeeTiers(params: {
    threadID: string;
    beeTiers: string;
    actionID?: string;
  }): Promise<RemoteSwarmState> {
    return this.sendRpc<RemoteSwarmState>('threads.swarm.setBeeTiers', params, 30000);
  }

  /**
   * swarm.tiers.get {} -> { tiers }
   */
  public async getSwarmTiers(): Promise<RemoteSwarmTiersResult> {
    return this.sendRpc<RemoteSwarmTiersResult>('swarm.tiers.get', {}, 30000);
  }

  /**
   * swarm.tiers.set { tiers, actionID } -> { tiers }
   */
  public async setSwarmTiers(params: { tiers: RemoteSwarmTier[]; actionID: string }): Promise<RemoteSwarmTiersResult> {
    return this.sendRpc<RemoteSwarmTiersResult>('swarm.tiers.set', params, 30000);
  }

  /**
   * capabilities.get {} -> RemoteCapabilityDeclaration
   */
  public async getCapabilities(): Promise<RemoteCapabilityDeclaration> {
    const declaration = await this.sendRpc<RemoteCapabilityDeclaration>('capabilities.get', {});
    this.capabilities = declaration;
    return declaration;
  }

  /**
   * 拉取最新看板并统一走 revision 校验后推送给 UI。
   * 页面恢复探测与操作后同步都经由这里，避免迟到的旧快照覆盖已推送的新 revision。
   */
  public async refreshBoard(timeoutMs = 10000): Promise<Board | null> {
    const socket = this.ws;
    const board = await this.getBoard(timeoutMs);
    if (!this.isReady || socket !== this.ws) {
      throw new Error('连接已更换，请重新同步看板');
    }
    this.publishBoard(board);
    return board;
  }

  /**
   * 操作后取最新 board 保持与 Mac 强一致
   */
  private syncBoardInBackground(): void {
    // 写操作后必须拿到写后的快照：不合并写操作之前就已发出的在途请求。
    this.boardInFlight = null;
    this.refreshBoard().catch(() => {
      // 后续可通过 WebSocket notification kanban.taskUpdated 补充更新
    });
  }

  /**
   * 释放所有资源与计时器
   */
  public dispose(): void {
    this.isDisposed = true;
    this.authGeneration++;
    this.pendingPairToken = null;
    this.credentials = null;
    this.isAuthenticated = false;
    this.authenticatedGeneration = -1;
    this.capabilities = null;
    this.currentBoard = null;

    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    this.clearConnectTimer();
    this.clearStableTimer();

    this.stopHeartbeat();
    this.cleanupPendingRequests('页面已离开');

    if (this.ws) {
      try {
        this.ws.onclose = null;
        this.ws.onerror = null;
        this.ws.onmessage = null;
        this.ws.onopen = null;
        this.ws.close();
      } catch {
        // 忽略
      }
      this.ws = null;
    }

    if (typeof window !== 'undefined') {
      window.removeEventListener('pagehide', this.handlePageHide);
    }
  }
}
