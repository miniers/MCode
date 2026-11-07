// 总管（steward.*）的远程调用封装，对应宿主 internal/remote/steward_topics.go。
// 只读列表与写操作都在这里；收到 steward.changed 推送时上层按版本号重新拉取话题。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成）。

import { useCallback, useMemo, useState } from 'react';
import type {
  RemoteStewardOpenResult,
  RemoteStewardTopic,
  RemoteStewardTopicsListResult,
  RemoteStewardTopicsStartResult
} from '../types/generated';
import { RpcError } from '../services/rpc';
import { useRemoteCall, useRemoteNotification } from './useRemoteCall';

export type { RemoteStewardOpenResult, RemoteStewardTopic, RemoteStewardTopicsListResult, RemoteStewardTopicsStartResult };

export const REMOTE_STEWARD_TOPICS_LIST_METHOD = 'steward.topics.list';
export const REMOTE_STEWARD_OPEN_METHOD = 'steward.open';
export const REMOTE_STEWARD_TOPICS_START_METHOD = 'steward.topics.start';
export const REMOTE_STEWARD_TOPICS_REPLAN_METHOD = 'steward.topics.replan';
export const REMOTE_STEWARD_TOPICS_CONFIRM_METHOD = 'steward.topics.confirm';
export const REMOTE_STEWARD_TOPICS_CLOSE_METHOD = 'steward.topics.close';
export const REMOTE_STEWARD_TOPICS_DELETE_METHOD = 'steward.topics.delete';
export const REMOTE_STEWARD_TOPICS_REVIEW_METHOD = 'steward.topics.tasks.review';
export const REMOTE_STEWARD_CHANGED_METHOD = 'steward.changed';

/** 宿主 CAS 版本冲突的错误码，与 internal/remote/wire/jsonrpc.go 的 ErrCodeVersionConflict 一致。 */
export const REMOTE_VERSION_CONFLICT_CODE = -32009;

/** 一条待验收任务的验收决定：reject 必须带非空 feedback，宿主会再校验一次。 */
export interface StewardTaskReview {
  projectID: string;
  threadID: string;
  taskID: string;
  decision: 'accept' | 'reject';
  feedback?: string;
  revision: number;
}

/** 总管的远程调用集合：能力判断按方法逐个进行，宿主没声明的方法上层不显示入口。 */
export interface RemoteStewardApi {
  /** 宿主是否提供话题列表；为假时侧栏不显示该分组。 */
  supportsTopics: boolean;
  /** 宿主是否提供 steward.open；为假时不显示总管固定行。 */
  supportsOpen: boolean;
  /** 宿主是否提供 steward.topics.start；为假时不显示「交给它」入口。 */
  supportsStart: boolean;
  supportsReplan: boolean;
  supportsConfirm: boolean;
  supportsClose: boolean;
  supportsDelete: boolean;
  /** 宿主是否提供任务验收；为假时话题面板不显示待验收任务。 */
  supportsReviewTask: boolean;
  /** 拉取话题列表；projectID 为空表示全部项目。 */
  listTopics: (projectID?: string) => Promise<RemoteStewardTopic[]>;
  /** 打开总管主会话，返回会话 ID。 */
  openSteward: (projectID: string) => Promise<string>;
  /**
   * 用一句需求开新话题；threadID 是提出需求的会话（可选）。
   * 宿主只按 projectID + request 开话题，threadID 仅用于归属校验。
   */
  startTopic: (projectID: string, request: string, threadID?: string) => Promise<RemoteStewardTopicsStartResult>;
  replanTopic: (projectID: string, threadID: string) => Promise<void>;
  confirmTopic: (projectID: string, threadID: string) => Promise<void>;
  closeTopic: (projectID: string, threadID: string) => Promise<void>;
  deleteTopic: (projectID: string, threadID: string) => Promise<void>;
  /** 验收通过或打回话题下的一条任务。 */
  reviewTask: (review: StewardTaskReview) => Promise<void>;
}

/** 报告错误是否为宿主返回的版本冲突（话题或任务已被别处改过，应提示刷新）。 */
export function isRemoteVersionConflict(error: unknown): boolean {
  return error instanceof RpcError && error.code === REMOTE_VERSION_CONFLICT_CODE;
}

/** 把动作失败的错误转成给用户看的一句话；版本冲突单独提示刷新。 */
export function stewardActionErrorText(error: unknown, fallback: string): string {
  if (isRemoteVersionConflict(error)) return '版本已变化，请刷新后重试';
  if (error instanceof Error && error.message) return error.message;
  return fallback;
}

/** 取总管的调用集合；能力未变化前返回同一对象。 */
export function useRemoteStewardApi(): RemoteStewardApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsTopics = supportsMethod(REMOTE_STEWARD_TOPICS_LIST_METHOD);
  const supportsOpen = supportsMethod(REMOTE_STEWARD_OPEN_METHOD);
  const supportsStart = supportsMethod(REMOTE_STEWARD_TOPICS_START_METHOD);
  const supportsReplan = supportsMethod(REMOTE_STEWARD_TOPICS_REPLAN_METHOD);
  const supportsConfirm = supportsMethod(REMOTE_STEWARD_TOPICS_CONFIRM_METHOD);
  const supportsClose = supportsMethod(REMOTE_STEWARD_TOPICS_CLOSE_METHOD);
  const supportsDelete = supportsMethod(REMOTE_STEWARD_TOPICS_DELETE_METHOD);
  const supportsReviewTask = supportsMethod(REMOTE_STEWARD_TOPICS_REVIEW_METHOD);

  const listTopics = useCallback(
    async (projectID?: string): Promise<RemoteStewardTopic[]> => {
      const result = await call<RemoteStewardTopicsListResult>(
        REMOTE_STEWARD_TOPICS_LIST_METHOD,
        projectID ? { projectID } : {}
      );
      return Array.isArray(result?.topics) ? result.topics : [];
    },
    [call]
  );

  const openSteward = useCallback(
    async (projectID: string): Promise<string> => {
      const result = await call<RemoteStewardOpenResult>(REMOTE_STEWARD_OPEN_METHOD, { projectID });
      return result?.threadID ?? '';
    },
    [call]
  );

  const startTopic = useCallback(
    async (projectID: string, request: string, threadID?: string): Promise<RemoteStewardTopicsStartResult> => {
      return call<RemoteStewardTopicsStartResult>(REMOTE_STEWARD_TOPICS_START_METHOD, {
        projectID,
        ...(threadID ? { threadID } : {}),
        request
      });
    },
    [call]
  );

  const replanTopic = useCallback(
    async (projectID: string, threadID: string): Promise<void> => {
      await call<Record<string, never>>(REMOTE_STEWARD_TOPICS_REPLAN_METHOD, { projectID, threadID });
    },
    [call]
  );

  const confirmTopic = useCallback(
    async (projectID: string, threadID: string): Promise<void> => {
      await call<Record<string, never>>(REMOTE_STEWARD_TOPICS_CONFIRM_METHOD, { projectID, threadID });
    },
    [call]
  );

  const closeTopic = useCallback(
    async (projectID: string, threadID: string): Promise<void> => {
      await call<Record<string, never>>(REMOTE_STEWARD_TOPICS_CLOSE_METHOD, { projectID, threadID });
    },
    [call]
  );

  const deleteTopic = useCallback(
    async (projectID: string, threadID: string): Promise<void> => {
      await call<Record<string, never>>(REMOTE_STEWARD_TOPICS_DELETE_METHOD, { projectID, threadID });
    },
    [call]
  );

  const reviewTask = useCallback(
    async (review: StewardTaskReview): Promise<void> => {
      await call<Record<string, never>>(REMOTE_STEWARD_TOPICS_REVIEW_METHOD, {
        projectID: review.projectID,
        threadID: review.threadID,
        taskID: review.taskID,
        decision: review.decision,
        ...(review.feedback ? { feedback: review.feedback } : {}),
        revision: review.revision
      });
    },
    [call]
  );

  return useMemo<RemoteStewardApi>(
    () => ({
      supportsTopics,
      supportsOpen,
      supportsStart,
      supportsReplan,
      supportsConfirm,
      supportsClose,
      supportsDelete,
      supportsReviewTask,
      listTopics,
      openSteward,
      startTopic,
      replanTopic,
      confirmTopic,
      closeTopic,
      deleteTopic,
      reviewTask
    }),
    [
      supportsTopics,
      supportsOpen,
      supportsStart,
      supportsReplan,
      supportsConfirm,
      supportsClose,
      supportsDelete,
      supportsReviewTask,
      listTopics,
      openSteward,
      startTopic,
      replanTopic,
      confirmTopic,
      closeTopic,
      deleteTopic,
      reviewTask
    ]
  );
}

/**
 * 订阅 steward.changed，返回一个只增不减的版本号：推送到达时加一，
 * 调用方把它放进依赖数组即可重新拉取话题。载荷 projectID 为空串表示全部项目。
 */
export function useStewardChangeVersion(projectID: string | null, enabled = true): number {
  const [version, setVersion] = useState(0);
  useRemoteNotification(
    REMOTE_STEWARD_CHANGED_METHOD,
    (params) => {
      const changed = (params as { projectID?: unknown } | null)?.projectID;
      if (typeof changed === 'string' && changed && projectID && changed !== projectID) return;
      setVersion((value) => value + 1);
    },
    enabled
  );
  return version;
}
