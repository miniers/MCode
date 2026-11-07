// mcode 远程控制 Phase 1 契约接口定义

import type { ReviewDecision, TaskStatus } from './generated';

export type {
  TaskStatus,
  TaskPriority,
  ReviewDecision,
  KanbanDisplayColumn,
  GoalStatus,
  Project,
  Task as TaskItem,
  Board,
  GoalPolicySummary,
  GoalProgress,
  Goal,
  KanbanCreateTaskParams,
  KanbanTaskMutationResult,
  GoalCreateParams,
  GoalMutationParams,
  GoalReplanParams,
  GoalTaskMutationParams,
  KanbanGetDiffParams as GetDiffParams,
  KanbanGetDiffResult as GetDiffResult,
  RemoteProjectSummary,
  RemoteThreadSummary,
  RemoteThreadExecutionStatus,
  RemoteThreadMessage,
  RemoteThreadHistoryPage,
  RemoteCapabilityDeclaration,
  RemoteProjectsListResult as ProjectsListResult,
  RemoteThreadsListParams as ThreadsListParams,
  RemoteThreadsListResult as ThreadsListResult,
  RemoteThreadsUpdateParams as ThreadsUpdateParams,
  RemoteThreadsUpdateResult as ThreadsUpdateResult,
  RemoteThreadsMoveParams as ThreadsMoveParams,
  RemoteThreadsMoveResult as ThreadsMoveResult,
  RemoteThreadsChangedParams as ThreadsChangedParams,
  RemoteThreadsHistoryParams as ThreadsHistoryParams,
  RemoteThreadsCreateParams,
  RemoteThreadsCreateResult,
  RemoteComposerOptions,
  RemoteProvider,
  RemoteProviderModel,
  RemoteSwarmPair,
  RemoteSwarmComposerInfo,
  RemoteComposerDraftDefaults,
  RemoteProvidersListResult,
  RemoteThreadsSendMessageParams,
  RemoteThreadsSendMessageResult,
  RemoteAttachment,
  RemoteAttachmentUploadParams,
  RemoteAttachmentUploadResult,
  RemoteFollowUp,
  RemoteThreadsQueueListResult,
  RemoteThreadsQueueMutationParams,
  RemoteThreadsQueueMutationResult,
  RemoteThreadsQueueInterruptAndSendParams,
  RemoteThreadsQueueInterruptAndSendResult,
  RemoteThreadsQueueChangedParams,
  RemoteThreadsActionStatusParams,
  RemoteThreadsActionStatusResult,
  RemoteThreadsSettleActionParams,
  RemoteThreadsSettleActionResult,
  RemoteRequest,
  RemoteRequestsListParams,
  RemoteRequestsListResult,
  RemoteRequestsRespondParams,
  RemoteRequestsRespondResult,
  RemoteRequestsChangedParams,
  RemoteThreadsInterruptParams,
  RemoteThreadsInterruptResult,
  RemoteThreadsContinueParams,
  RemoteThreadsContinueResult,
  RemoteCommand,
  RemoteThreadsCommandsParams,
  RemoteThreadsCommandsResult,
  RemoteThreadsSwarmBatchParams,
  RemoteActionState,
  RemoteThreadStreamSnapshot,
  RemoteThreadsProjectionPayload as RemoteThreadProjectionPayload,
  RemoteThreadsResyncPayload as RemoteThreadResyncPayload,
  RemoteThreadsSubscribeParams as ThreadsSubscribeParams,
  RemoteThreadsSubscribeResult as ThreadsSubscribeResult,
  RemoteThreadsUnsubscribeParams as ThreadsUnsubscribeParams,
  RemoteThreadsUnsubscribeResult as ThreadsUnsubscribeResult,
  RemoteThreadsEventParams as ThreadsEventParams,
  RemoteThreadsGetDiffParams as ThreadsGetDiffParams,
  RemoteThreadsGetDiffResult as ThreadsGetDiffResult,
  RemoteReviewTurn,
  RemoteFileEntryKind,
  RemoteFileContentKind,
  RemoteFileEntry,
  RemoteFilesListParams,
  RemoteFilesListResult,
  RemoteFilesReadParams,
  RemoteFilesReadResult,
  RemoteFilesSearchParams,
  RemoteFilesSearchResult,
  RemoteMediaSource,
  RemoteMediaVariant,
  RemoteMediaRef,
  RemoteThreadsMediaReadParams,
  RemoteThreadsMediaReadResult,
  RemoteGitStatusParams,
  RemoteGitStatusResult,
  RemoteGitBranchesParams,
  RemoteGitBranchesResult
} from './generated';

export type MoveTaskParams = {
  taskID: string;
  status: TaskStatus;
  expectedRevision?: number;
  actionID?: string;
};

export type StartTaskParams = {
  taskID: string;
  expectedRevision?: number;
  actionID?: string;
};

export type ReviewTaskParams = {
  taskID: string;
  decision: ReviewDecision;
  feedback?: string;
  expectedRevision?: number;
  actionID?: string;
};

// JSON-RPC 2.0 基础协议
export interface JsonRpcRequest<T = Record<string, unknown>> {
  jsonrpc: '2.0';
  id: number;
  method: string;
  params?: T;
}

export interface JsonRpcResponse<T = unknown> {
  jsonrpc: '2.0';
  id: number;
  result?: T;
  error?: {
    code: number;
    message: string;
    data?: unknown;
  };
}

export interface JsonRpcNotification<T = unknown> {
  jsonrpc: '2.0';
  method: string;
  params: T;
}

export type ConnectionState =
  | 'unpaired'
  | 'connecting'
  | 'connected'
  | 'reconnecting'
  | 'auth_failed'
  | 'disconnected';
