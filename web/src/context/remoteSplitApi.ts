// 总管会话分割（threads.splitSession）：宿主在点击时立刻追加一条分割线消息，
// 下一条消息触发的换新前言才决定带走的长期信息。这里只负责发起调用与计算按钮状态。
// 显示与禁用规则与桌面 internal/ui/steward_split.go 的 splitSessionViewFor 一一对应：
// 只有总管会话、已有消息才显示；这一轮没结束或已断线则禁用。
// 类型来自 web/src/types/generated.ts（由 docs/remote-control.schema.json 生成），协议无需改动。

import { useCallback, useMemo } from 'react';
import type { RemoteThreadsSplitSessionResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteThreadsSplitSessionResult };

export const REMOTE_THREADS_SPLIT_SESSION_METHOD = 'threads.splitSession';

/** 分割按钮这一帧的显示与禁用状态，与桌面 splitSessionView 同构。 */
export interface SplitSessionView {
  /** 是否渲染按钮：必须是总管会话，且已经有消息。 */
  show: boolean;
  /** 这一轮还没结束、正在发送或已断线：按钮保留但禁用。 */
  disabled: boolean;
}

/** splitSessionViewFor 的输入：三项都由 ThreadView 从会话状态里取。 */
export interface SplitSessionViewInput {
  /** 会话是否总管主会话（thread.isSteward）。 */
  isSteward: boolean;
  /** 会话是否已有可见消息：空会话没有可分割的历史。 */
  hasMessages: boolean;
  /** 这一轮是否还没结束（正在发送、线程运行中、子代理忙碌）或已断线。 */
  busy: boolean;
}

/**
 * 计算分割按钮的显示与禁用状态：空的总管会话与普通会话整块不渲染，
 * 其余情况保留按钮，忙碌或断线时禁用（桌面对应 sending || IsRunning || SwarmBusy）。
 */
export function splitSessionViewFor(input: SplitSessionViewInput): SplitSessionView {
  if (!input.isSteward || !input.hasMessages) return { show: false, disabled: false };
  return { show: true, disabled: input.busy };
}

/** 分割会话的远程调用集合：能力判断按方法进行，宿主没声明时上层不显示入口。 */
export interface RemoteSplitApi {
  /** 宿主是否声明 threads.splitSession；为假时整块入口不显示。 */
  supported: boolean;
  /** 追加分割线；宿主错误文案原样抛出（-32000 运行中/空会话，-32002 线程不存在）。 */
  splitSession: (threadID: string) => Promise<RemoteThreadsSplitSessionResult>;
}

/** 取分割会话的调用集合；能力未变化前返回同一对象。 */
export function useRemoteSplitApi(): RemoteSplitApi {
  const { call, supportsMethod } = useRemoteCall();
  const supported = supportsMethod(REMOTE_THREADS_SPLIT_SESSION_METHOD);

  const splitSession = useCallback(
    async (threadID: string): Promise<RemoteThreadsSplitSessionResult> => {
      return call<RemoteThreadsSplitSessionResult>(REMOTE_THREADS_SPLIT_SESSION_METHOD, { threadID });
    },
    [call]
  );

  return useMemo<RemoteSplitApi>(() => ({ supported, splitSession }), [supported, splitSession]);
}
