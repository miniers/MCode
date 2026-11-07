// 编排中断续跑与「始终允许」名单的远程扩展调用，对应宿主 internal/remote 下的
// swarm_ext_methods.go 与 always_allow_methods.go；参数与结果类型来自 web/src/types/generated.ts，
// 此处按原名再导出。

import { useMemo } from 'react';
import type {
  RemoteAlwaysAllowedListResult,
  RemoteAlwaysAllowedRevokeResult,
  RemoteSwarmExtDismissResult,
  RemoteSwarmExtInterruptedBee,
  RemoteSwarmExtInterruptedResult,
  RemoteSwarmExtResumeResult,
  RemoteSwarmExtRetryBeeResult,
  RemoteThreadsSwarmRetryBeeParams
} from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export type {
  RemoteAlwaysAllowedListResult,
  RemoteAlwaysAllowedRevokeResult,
  RemoteSwarmExtDismissResult,
  RemoteSwarmExtInterruptedBee,
  RemoteSwarmExtInterruptedResult,
  RemoteSwarmExtResumeResult,
  RemoteSwarmExtRetryBeeResult,
  RemoteThreadsSwarmRetryBeeParams
};

export const REMOTE_SWARM_INTERRUPTED_METHOD = 'threads.swarm.interrupted';
export const REMOTE_SWARM_RESUME_METHOD = 'threads.swarm.resume';
export const REMOTE_SWARM_DISMISS_INTERRUPTED_METHOD = 'threads.swarm.dismissInterrupted';
export const REMOTE_SWARM_RETRY_BEE_METHOD = 'threads.swarm.retryBee';
export const REMOTE_ALWAYS_ALLOWED_LIST_METHOD = 'requests.alwaysAllowed.list';
export const REMOTE_ALWAYS_ALLOWED_REVOKE_METHOD = 'requests.alwaysAllowed.revoke';

/** 编排中断续跑与「始终允许」名单的远程调用集合。 */
export interface RemoteSwarmExtApi {
  /** 宿主是否提供中断查询；为假时续跑横幅整块隐藏。 */
  supportsInterrupted: boolean;
  /** 宿主是否提供「继续运行」。 */
  supportsResume: boolean;
  /** 宿主是否提供「忽略」。 */
  supportsDismissInterrupted: boolean;
  /** 宿主是否提供失败/已停止子代理的「沿用改动续做」；为假时菜单里不出现该入口。 */
  supportsRetryBee: boolean;
  /** 宿主是否提供「始终允许」名单查询与撤销。 */
  supportsAlwaysAllowed: boolean;
  fetchInterrupted: (threadID: string) => Promise<RemoteSwarmExtInterruptedResult>;
  resumeInterrupted: (threadID: string) => Promise<RemoteSwarmExtResumeResult>;
  dismissInterrupted: (threadID: string) => Promise<RemoteSwarmExtDismissResult>;
  /** 沿用指定子代理的原 worktree 与半成品改动重新派发；threadID 可传主会话或 bee 会话。 */
  retryBee: (params: RemoteThreadsSwarmRetryBeeParams) => Promise<RemoteSwarmExtRetryBeeResult>;
  listAlwaysAllowed: (projectID: string) => Promise<RemoteAlwaysAllowedListResult>;
  revokeAlwaysAllowed: (projectID: string, key: string) => Promise<RemoteAlwaysAllowedRevokeResult>;
}

/**
 * 取远程扩展调用集合。
 * 返回对象按 call / supportsMethod 记忆，能力和连接状态变化前保持同一引用，可直接作为 effect 依赖。
 */
export function useRemoteSwarmExtApi(): RemoteSwarmExtApi {
  const { call, supportsMethod } = useRemoteCall();
  const supportsInterrupted = supportsMethod(REMOTE_SWARM_INTERRUPTED_METHOD);
  const supportsResume = supportsMethod(REMOTE_SWARM_RESUME_METHOD);
  const supportsDismissInterrupted = supportsMethod(REMOTE_SWARM_DISMISS_INTERRUPTED_METHOD);
  const supportsRetryBee = supportsMethod(REMOTE_SWARM_RETRY_BEE_METHOD);
  const supportsAlwaysAllowed =
    supportsMethod(REMOTE_ALWAYS_ALLOWED_LIST_METHOD) && supportsMethod(REMOTE_ALWAYS_ALLOWED_REVOKE_METHOD);

  return useMemo<RemoteSwarmExtApi>(
    () => ({
      supportsInterrupted,
      supportsResume,
      supportsDismissInterrupted,
      supportsRetryBee,
      supportsAlwaysAllowed,
      fetchInterrupted: (threadID: string) =>
        call<RemoteSwarmExtInterruptedResult>(REMOTE_SWARM_INTERRUPTED_METHOD, { threadID }),
      resumeInterrupted: (threadID: string) =>
        call<RemoteSwarmExtResumeResult>(REMOTE_SWARM_RESUME_METHOD, { threadID }),
      dismissInterrupted: (threadID: string) =>
        call<RemoteSwarmExtDismissResult>(REMOTE_SWARM_DISMISS_INTERRUPTED_METHOD, { threadID }),
      retryBee: (params: RemoteThreadsSwarmRetryBeeParams) =>
        call<RemoteSwarmExtRetryBeeResult>(REMOTE_SWARM_RETRY_BEE_METHOD, params),
      listAlwaysAllowed: (projectID: string) =>
        call<RemoteAlwaysAllowedListResult>(REMOTE_ALWAYS_ALLOWED_LIST_METHOD, { projectID }),
      revokeAlwaysAllowed: (projectID: string, key: string) =>
        call<RemoteAlwaysAllowedRevokeResult>(REMOTE_ALWAYS_ALLOWED_REVOKE_METHOD, { projectID, key })
    }),
    [call, supportsInterrupted, supportsResume, supportsDismissInterrupted, supportsRetryBee, supportsAlwaysAllowed]
  );
}
