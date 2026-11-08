// 会话分割的远程调用封装。

import { useCallback, useMemo } from 'react';
import type { RemoteThreadsSplitSessionResult } from '../types/generated';
import { useRemoteCall } from './useRemoteCall';

export type { RemoteThreadsSplitSessionResult };

export const REMOTE_THREADS_SPLIT_SESSION_METHOD = 'threads.splitSession';

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
